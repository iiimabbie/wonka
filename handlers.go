package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v4"
)

// ── Auth Handlers ────────────────────────────────────────────────────────────

func handleRegister(c echo.Context) error {
	ctx := c.Request().Context()
	type req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	var r req
	if err := c.Bind(&r); err != nil || r.Email == "" || r.Password == "" || r.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	hash, err := hashPassword(r.Password)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to hash password"})
	}

	var userID string
	err = pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name, role) VALUES ($1, $2, $3, '') RETURNING id`,
		r.Email, hash, r.Name,
	).Scan(&userID)
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": "email already exists"})
	}

	return c.JSON(http.StatusCreated, map[string]string{"id": userID})
}

func handleLogin(c echo.Context) error {
	ctx := c.Request().Context()
	type req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	var r req
	if err := c.Bind(&r); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	jwtSecret := os.Getenv("JWT_SECRET")

	var userID, hash, role string
	err := pool.QueryRow(ctx,
		`SELECT id, password_hash, role FROM users WHERE email = $1`,
		r.Email,
	).Scan(&userID, &hash, &role)
	if err != nil || !checkPassword(hash, r.Password) {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
	}

	token, err := generateJWT(userID, r.Email, role, jwtSecret)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
	}

	// Fetch user name for UI
	var name string
	pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&name)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"token": token,
		"user": map[string]string{
			"id":    userID,
			"email": r.Email,
			"name":  name,
			"role":  role,
		},
	})
}

// ── Candy Handlers ───────────────────────────────────────────────────────────

func handleGetBalance(c echo.Context) error {
	ctx := c.Request().Context()
	agent := c.Get("agent").(*Agent)
	var balance int
	var lastMod *time.Time
	err := pool.QueryRow(ctx,
		`SELECT balance, last_mod FROM agent_balances WHERE id = $1`,
		agent.ID,
	).Scan(&balance, &lastMod)
	if err != nil {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"agent":    agent.Name,
			"balance":  0,
			"last_mod": nil,
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"agent":    agent.Name,
		"balance":  balance,
		"last_mod": lastMod,
	})
}

func handleAdjustCandies(c echo.Context) error {
	ctx := c.Request().Context()
	agent := c.Get("agent").(*Agent)
	type req struct {
		Delta          int    `json:"delta"`
		Reason         string `json:"reason"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	var r req
	if err := c.Bind(&r); err != nil || r.Delta == 0 || r.Reason == "" || r.IdempotencyKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	var count int
	err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM candy_ledger WHERE agent_id = $1 AND idempotency_key = $2`,
		agent.ID, r.IdempotencyKey,
	).Scan(&count)
	if err == nil && count > 0 {
		return c.JSON(http.StatusOK, map[string]string{"status": "duplicate"})
	}

	_, err = pool.Exec(ctx,
		`INSERT INTO candy_ledger (agent_id, delta, reason, idempotency_key) VALUES ($1, $2, $3, $4)`,
		agent.ID, r.Delta, r.Reason, r.IdempotencyKey,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "ledger error"})
	}

	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

func handleGetHistory(c echo.Context) error {
	ctx := c.Request().Context()
	agent := c.Get("agent").(*Agent)
	type entry struct {
		ID             string    `json:"id"`
		AgentName      string    `json:"agent_name"`
		Delta          int       `json:"delta"`
		Reason         string    `json:"reason"`
		IdempotencyKey string    `json:"idempotency_key"`
		CreatedAt      time.Time `json:"created_at"`
	}
	rows, err := pool.Query(ctx,
		`SELECT id, delta, reason, idempotency_key, created_at
		 FROM candy_ledger WHERE agent_id = $1 ORDER BY created_at DESC LIMIT 50`,
		agent.ID,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	entries := []entry{}
	for rows.Next() {
		var e entry
		e.AgentName = agent.Name
		rows.Scan(&e.ID, &e.Delta, &e.Reason, &e.IdempotencyKey, &e.CreatedAt)
		entries = append(entries, e)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"agent":   agent.Name,
		"entries": entries,
	})
}

func handleGetLeaderboard(c echo.Context) error {
	ctx := c.Request().Context()
	rows, err := pool.Query(ctx, `
		SELECT
			ab.name,
			ab.balance,
			COALESCE(inv_val.portfolio_value, 0) AS portfolio_value,
			ab.balance + COALESCE(inv_val.portfolio_value, 0) AS total_assets
		FROM agent_balances ab
		LEFT JOIN (
			SELECT
				inv.agent_id,
				SUM(COALESCE(ml.price, ph.price, 1)) AS portfolio_value
			FROM inventories inv
			LEFT JOIN LATERAL (
				SELECT price FROM market_listings
				WHERE item_id = inv.item_id AND expired = false
				ORDER BY refreshed_at DESC LIMIT 1
			) ml ON true
			LEFT JOIN LATERAL (
				SELECT price FROM market_price_history
				WHERE item_id = inv.item_id
				ORDER BY refreshed_at DESC LIMIT 1
			) ph ON ml.price IS NULL
			WHERE inv.sold_at IS NULL
			GROUP BY inv.agent_id
		) inv_val ON inv_val.agent_id = ab.id
		WHERE ab.name NOT ILIKE 'test%'
		ORDER BY total_assets DESC
	`)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	type item struct {
		Name           string `json:"name"`
		Balance        int    `json:"balance"`
		PortfolioValue int    `json:"portfolio_value"`
		TotalAssets    int    `json:"total_assets"`
	}
	lb := []item{}
	for rows.Next() {
		var i item
		rows.Scan(&i.Name, &i.Balance, &i.PortfolioValue, &i.TotalAssets)
		lb = append(lb, i)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"leaderboard": lb})
}

func handleGetSummary(c echo.Context) error {
	ctx := c.Request().Context()
	agent := c.Get("agent").(*Agent)
	now := time.Now()
	weekStart := now.AddDate(0, 0, -int(now.Weekday()))
	weekStart = time.Date(weekStart.Year(), weekStart.Month(), weekStart.Day(), 0, 0, 0, 0, weekStart.Location())

	var earned, spent int
	pool.QueryRow(ctx,
		`SELECT
			COALESCE(SUM(CASE WHEN delta > 0 THEN delta ELSE 0 END), 0),
			COALESCE(ABS(SUM(CASE WHEN delta < 0 THEN delta ELSE 0 END)), 0)
		 FROM candy_ledger WHERE agent_id = $1 AND created_at >= $2`,
		agent.ID, weekStart,
	).Scan(&earned, &spent)

	var balance int
	pool.QueryRow(ctx,
		`SELECT COALESCE(balance, 0) FROM agent_balances WHERE id = $1`, agent.ID,
	).Scan(&balance)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"agent":       agent.Name,
		"balance":     balance,
		"week_earned": earned,
		"week_spent":  spent,
		"week_net":    earned - spent,
	})
}

// ── Transfer Handlers ────────────────────────────────────────────────────────

func handleTransfer(c echo.Context) error {
	ctx := c.Request().Context()
	agent := c.Get("agent").(*Agent)
	type req struct {
		To             string `json:"to"`
		Amount         int    `json:"amount"`
		Reason         string `json:"reason"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	var r req
	if err := c.Bind(&r); err != nil || r.To == "" || r.Amount <= 0 || r.Reason == "" || r.IdempotencyKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request"})
	}

	// Resolve destination agent
	var toID string
	err := pool.QueryRow(ctx,
		`SELECT id FROM agents WHERE name = $1 AND enabled = true`, r.To,
	).Scan(&toID)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "destination agent not found"})
	}

	if agent.ID == toID {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "cannot transfer to self"})
	}

	// Check balance
	var balance int
	pool.QueryRow(ctx,
		`SELECT COALESCE(balance, 0) FROM agent_balances WHERE id = $1`, agent.ID,
	).Scan(&balance)
	if balance < r.Amount {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "insufficient balance"})
	}

	// Transaction: insert transfer + two ledger entries
	tx, err := pool.Begin(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer tx.Rollback(ctx)

	var transferID string
	err = tx.QueryRow(ctx,
		`INSERT INTO transfers (from_agent, to_agent, amount, reason, idempotency_key)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		agent.ID, toID, r.Amount, r.Reason, r.IdempotencyKey,
	).Scan(&transferID)
	if err != nil {
		// Likely duplicate idempotency key
		return c.JSON(http.StatusOK, map[string]string{"status": "duplicate"})
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO candy_ledger (agent_id, delta, reason, idempotency_key, transfer_id)
		 VALUES ($1, $2, $3, $4, $5)`,
		agent.ID, -r.Amount, "transfer out: "+r.Reason, "out:"+transferID, transferID,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "ledger error"})
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO candy_ledger (agent_id, delta, reason, idempotency_key, transfer_id)
		 VALUES ($1, $2, $3, $4, $5)`,
		toID, r.Amount, "transfer in: "+r.Reason, "in:"+transferID, transferID,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "ledger error"})
	}

	if err := tx.Commit(ctx); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "commit error"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"transfer_id": transferID,
	})
}

func handleTransferHistory(c echo.Context) error {
	ctx := c.Request().Context()
	agent := c.Get("agent").(*Agent)
	type entry struct {
		ID        string    `json:"id"`
		FromAgent string    `json:"from_agent"`
		ToAgent   string    `json:"to_agent"`
		Amount    int       `json:"amount"`
		Reason    string    `json:"reason"`
		CreatedAt time.Time `json:"created_at"`
	}
	rows, err := pool.Query(ctx,
		`SELECT t.id, fa.name, ta.name, t.amount, t.reason, t.created_at
		 FROM transfers t
		 JOIN agents fa ON fa.id = t.from_agent
		 JOIN agents ta ON ta.id = t.to_agent
		 WHERE t.from_agent = $1 OR t.to_agent = $1
		 ORDER BY t.created_at DESC LIMIT 50`,
		agent.ID,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	entries := []entry{}
	for rows.Next() {
		var e entry
		rows.Scan(&e.ID, &e.FromAgent, &e.ToAgent, &e.Amount, &e.Reason, &e.CreatedAt)
		entries = append(entries, e)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"transfers": entries})
}

// ── Agent Handlers (user-auth) ───────────────────────────────────────────────

func handleCreateAgent(c echo.Context) error {
	ctx := c.Request().Context()
	user := c.Get("user").(*User)
	type req struct {
		Name string `json:"name"`
	}
	var r req
	if err := c.Bind(&r); err != nil || r.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "name required"})
	}

	// Generate API key
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "key gen failed"})
	}
	apiKey := hex.EncodeToString(raw)
	h := sha256.Sum256([]byte(apiKey))
	keyHash := hex.EncodeToString(h[:])

	var agentID string
	err := pool.QueryRow(ctx,
		`INSERT INTO agents (name, key_hash, enabled, owner) VALUES ($1, $2, true, $3) RETURNING id`,
		r.Name, keyHash, user.ID,
	).Scan(&agentID)
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]string{"error": "agent name already exists"})
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"id":      agentID,
		"name":    r.Name,
		"api_key": apiKey,
	})
}

func handleListAgents(c echo.Context) error {
	ctx := c.Request().Context()
	user := c.Get("user").(*User)
	type agentItem struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
		Balance int    `json:"balance"`
	}
	rows, err := pool.Query(ctx,
		`SELECT a.id, a.name, a.enabled, COALESCE(ab.balance, 0)
		 FROM agents a
		 LEFT JOIN agent_balances ab ON ab.id = a.id
		 WHERE a.owner = $1 ORDER BY a.created_at`,
		user.ID,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	agents := []agentItem{}
	for rows.Next() {
		var a agentItem
		rows.Scan(&a.ID, &a.Name, &a.Enabled, &a.Balance)
		agents = append(agents, a)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"agents": agents})
}

func handleUserProfile(c echo.Context) error {
	user := c.Get("user").(*User)
	return c.JSON(http.StatusOK, map[string]interface{}{
		"id":    user.ID,
		"email": user.Email,
		"name":  user.Name,
		"role":  user.Role,
	})
}

func handleGetAgentBalance(c echo.Context) error {
	ctx := c.Request().Context()
	user := c.Get("user").(*User)
	agentID := c.Param("agentId")

	// Verify ownership (admin bypass)
	if user.Role != "admin" {
		var ownerID string
		err := pool.QueryRow(ctx,
			`SELECT owner FROM agents WHERE id = $1`, agentID,
		).Scan(&ownerID)
		if err != nil || ownerID != user.ID {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
		}
	}

	var name string
	var balance int
	var lastMod *time.Time
	err := pool.QueryRow(ctx,
		`SELECT a.name, COALESCE(ab.balance, 0), ab.last_mod
		 FROM agents a LEFT JOIN agent_balances ab ON ab.id = a.id
		 WHERE a.id = $1`, agentID,
	).Scan(&name, &balance, &lastMod)
	if err != nil {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "agent not found"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"agent":    name,
		"balance":  balance,
		"last_mod": lastMod,
	})
}

func handleGetAgentInventory(c echo.Context) error {
	ctx := c.Request().Context()
	user := c.Get("user").(*User)
	agentID := c.Param("agentId")

	if user.Role != "admin" {
		var ownerID string
		err := pool.QueryRow(ctx,
			`SELECT owner FROM agents WHERE id = $1`, agentID,
		).Scan(&ownerID)
		if err != nil || ownerID != user.ID {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
		}
	}

	type invItem struct {
		ID            string     `json:"id"`
		ItemName      string     `json:"item_name"`
		ItemType      string     `json:"item_type"`
		AcquiredAt    time.Time  `json:"acquired_at"`
		AcquiredPrice int        `json:"acquired_price"`
		SoldAt        *time.Time `json:"sold_at,omitempty"`
		SoldPrice     *int       `json:"sold_price,omitempty"`
	}
	rows, err := pool.Query(ctx,
		`SELECT inv.id, mi.name, mi.type, inv.acquired_at, inv.acquired_price, inv.sold_at, inv.sold_price
		 FROM inventories inv JOIN market_items mi ON mi.id = inv.item_id
		 WHERE inv.agent_id = $1 AND inv.sold_at IS NULL ORDER BY inv.acquired_at DESC`,
		agentID,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	items := []invItem{}
	for rows.Next() {
		var i invItem
		rows.Scan(&i.ID, &i.ItemName, &i.ItemType, &i.AcquiredAt, &i.AcquiredPrice, &i.SoldAt, &i.SoldPrice)
		items = append(items, i)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"items": items})
}

func handleGetAgentHistory(c echo.Context) error {
	ctx := c.Request().Context()
	user := c.Get("user").(*User)
	agentID := c.Param("agentId")

	if user.Role != "admin" {
		var ownerID string
		err := pool.QueryRow(ctx,
			`SELECT owner FROM agents WHERE id = $1`, agentID,
		).Scan(&ownerID)
		if err != nil || ownerID != user.ID {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
		}
	}

	var agentName string
	pool.QueryRow(ctx, `SELECT name FROM agents WHERE id = $1`, agentID).Scan(&agentName)

	type entry struct {
		ID             string    `json:"id"`
		AgentName      string    `json:"agent_name"`
		Delta          int       `json:"delta"`
		Reason         string    `json:"reason"`
		IdempotencyKey string    `json:"idempotency_key"`
		CreatedAt      time.Time `json:"created_at"`
	}
	rows, err := pool.Query(ctx,
		`SELECT id, delta, reason, idempotency_key, created_at
		 FROM candy_ledger WHERE agent_id = $1 ORDER BY created_at DESC LIMIT 50`,
		agentID,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	entries := []entry{}
	for rows.Next() {
		var e entry
		e.AgentName = agentName
		rows.Scan(&e.ID, &e.Delta, &e.Reason, &e.IdempotencyKey, &e.CreatedAt)
		entries = append(entries, e)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"agent":   agentName,
		"entries": entries,
	})
}

func handleRegenerateKey(c echo.Context) error {
	ctx := c.Request().Context()
	user := c.Get("user").(*User)
	agentID := c.Param("agentId")

	// Owner or admin
	if user.Role != "admin" {
		var ownerID string
		err := pool.QueryRow(ctx,
			`SELECT owner FROM agents WHERE id = $1`, agentID,
		).Scan(&ownerID)
		if err != nil || ownerID != user.ID {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "key gen failed"})
	}
	apiKey := hex.EncodeToString(raw)
	h := sha256.Sum256([]byte(apiKey))
	keyHash := hex.EncodeToString(h[:])

	_, err := pool.Exec(ctx,
		`UPDATE agents SET key_hash = $1, updated_at = now() WHERE id = $2`,
		keyHash, agentID,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{"api_key": apiKey})
}
