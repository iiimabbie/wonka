package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

// ── GET /v1/admin/agents ─────────────────────────────────────────────────────

func handleAdminAgents(c echo.Context) error {
	ctx := c.Request().Context()
	type entry struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Enabled    bool   `json:"enabled"`
		Balance    int    `json:"balance"`
		OwnerID    string `json:"owner_id"`
		OwnerEmail string `json:"owner_email"`
	}
	rows, err := pool.Query(ctx, `
		SELECT a.id, a.name, a.enabled,
		       COALESCE(ab.balance, 0),
		       COALESCE(a.owner::text, ''),
		       COALESCE(u.email, '')
		FROM agents a
		LEFT JOIN agent_balances ab ON ab.id = a.id
		LEFT JOIN users u ON u.id = a.owner
		ORDER BY a.name
	`)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	agents := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.ID, &e.Name, &e.Enabled, &e.Balance, &e.OwnerID, &e.OwnerEmail); err != nil {
			continue
		}
		agents = append(agents, e)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"agents": agents})
}

// ── PATCH /v1/admin/agents/:agentId ─────────────────────────────────────────

func handleAdminPatchAgent(c echo.Context) error {
	ctx := c.Request().Context()
	agentID := c.Param("agentId")
	type req struct {
		Enabled *bool `json:"enabled"`
	}
	var r req
	if err := c.Bind(&r); err != nil || r.Enabled == nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "enabled field required"})
	}

	var name string
	err := pool.QueryRow(ctx,
		`UPDATE agents SET enabled = $1, updated_at = now() WHERE id = $2 RETURNING name`,
		*r.Enabled, agentID,
	).Scan(&name)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "agent not found"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"id":      agentID,
		"name":    name,
		"enabled": *r.Enabled,
	})
}

// ── GET /v1/admin/users ──────────────────────────────────────────────────────

func handleAdminUsers(c echo.Context) error {
	ctx := c.Request().Context()
	type entry struct {
		ID         string `json:"id"`
		Email      string `json:"email"`
		Name       string `json:"name"`
		Role       string `json:"role"`
		AgentCount int    `json:"agent_count"`
	}
	rows, err := pool.Query(ctx, `
		SELECT u.id, u.email, u.name, u.role, COUNT(a.id)
		FROM users u
		LEFT JOIN agents a ON a.owner = u.id
		GROUP BY u.id
		ORDER BY u.email
	`)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	users := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.ID, &e.Email, &e.Name, &e.Role, &e.AgentCount); err != nil {
			continue
		}
		users = append(users, e)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"users": users})
}

// ── DELETE /v1/admin/users/:userId ───────────────────────────────────────────

func handleAdminDeleteUser(c echo.Context) error {
	ctx := c.Request().Context()
	user := c.Get("user").(*User)
	userID := c.Param("userId")

	if userID == user.ID {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "cannot delete yourself"})
	}

	// Check user exists
	var exists bool
	pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, userID).Scan(&exists)
	if !exists {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "user not found"})
	}

	// Unbind agents owned by this user
	var unbound int
	pool.QueryRow(ctx,
		`WITH updated AS (UPDATE agents SET owner = NULL WHERE owner = $1 RETURNING id) SELECT COUNT(*) FROM updated`,
		userID,
	).Scan(&unbound)

	_, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":          "ok",
		"deleted_user_id": userID,
		"unbound_agents":  unbound,
	})
}

// ── POST /v1/admin/adjust ────────────────────────────────────────────────────

func handleAdminAdjust(c echo.Context) error {
	ctx := c.Request().Context()
	type req struct {
		AgentID string `json:"agent_id"`
		Delta   int    `json:"delta"`
		Reason  string `json:"reason"`
	}
	var r req
	if err := c.Bind(&r); err != nil || r.AgentID == "" || r.Delta == 0 || r.Reason == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "agent_id, delta, reason required"})
	}

	// Resolve by ID or name
	var agentID, agentName string
	err := pool.QueryRow(ctx,
		`SELECT id, name FROM agents WHERE id::text = $1 OR name = $1`, r.AgentID,
	).Scan(&agentID, &agentName)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "agent not found"})
	}

	rb := make([]byte, 8)
	rand.Read(rb)
	idempotencyKey := fmt.Sprintf("admin-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(rb))

	_, err = pool.Exec(ctx,
		`INSERT INTO candy_ledger (agent_id, delta, reason, idempotency_key) VALUES ($1, $2, $3, $4)`,
		agentID, r.Delta, r.Reason, idempotencyKey,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}

	var newBalance int
	pool.QueryRow(ctx,
		`SELECT COALESCE(balance, 0) FROM agent_balances WHERE id = $1`, agentID,
	).Scan(&newBalance)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"agent":       agentName,
		"delta":       r.Delta,
		"reason":      r.Reason,
		"new_balance": newBalance,
	})
}

// ── GET /v1/admin/settings ───────────────────────────────────────────────────

func handleAdminGetSettings(c echo.Context) error {
	ctx := c.Request().Context()
	var aiBaseURL, aiModel, aiKey string
	pool.QueryRow(ctx,
		`SELECT ai_base_url, ai_model, ai_api_key FROM settings LIMIT 1`,
	).Scan(&aiBaseURL, &aiModel, &aiKey)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"ai_base_url": aiBaseURL,
		"ai_model":    aiModel,
		"ai_key_set":  aiKey != "",
	})
}

// ── PUT /v1/admin/settings ───────────────────────────────────────────────────

func handleAdminPutSettings(c echo.Context) error {
	ctx := c.Request().Context()
	type req struct {
		AIBaseURL string `json:"ai_base_url"`
		AIModel   string `json:"ai_model"`
		AIApiKey  string `json:"ai_api_key"`
	}
	var r req
	if err := c.Bind(&r); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	_, err := pool.Exec(ctx,
		`UPDATE settings SET ai_base_url = $1, ai_model = $2, ai_api_key = $3`,
		r.AIBaseURL, r.AIModel, r.AIApiKey,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"ai_base_url": r.AIBaseURL,
		"ai_model":    r.AIModel,
		"ai_key_set":  r.AIApiKey != "",
	})
}
