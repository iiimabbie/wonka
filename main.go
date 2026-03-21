package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

func main() {
	app := pocketbase.New()

	// Register API routes
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		se.Router.POST("/v1/candies/adjust", func(e *core.RequestEvent) error {
			return handleAdjust(e, app)
		})

		se.Router.GET("/v1/candies/balance", func(e *core.RequestEvent) error {
			return handleBalance(e, app)
		})

		se.Router.GET("/v1/candies/history", func(e *core.RequestEvent) error {
			return handleHistory(e, app)
		})

		se.Router.GET("/v1/candies/leaderboard", func(e *core.RequestEvent) error {
			return handleLeaderboard(e, app)
		})

		se.Router.GET("/v1/candies/summary", func(e *core.RequestEvent) error {
			return handleSummary(e, app)
		})

		return se.Next()
	})

	// Auto-create collections on startup
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		ensureCollections(app)
		return se.Next()
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

// --- Auth middleware: resolve agent from API key ---
func resolveAgent(e *core.RequestEvent, app *pocketbase.PocketBase) (*core.Record, error) {
	apiKey := e.Request.Header.Get("X-API-Key")
	if apiKey == "" {
		return nil, e.JSON(http.StatusUnauthorized, map[string]string{
			"error": "missing X-API-Key header",
		})
	}

	hash := sha256.Sum256([]byte(apiKey))
	keyHash := hex.EncodeToString(hash[:])

	record, err := app.FindFirstRecordByFilter("agents", "key_hash = {:hash} && enabled = true", map[string]any{
		"hash": keyHash,
	})
	if err != nil {
		return nil, e.JSON(http.StatusUnauthorized, map[string]string{
			"error": "invalid or disabled API key",
		})
	}

	return record, nil
}

// --- GET /v1/candies/balance ---
func handleBalance(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	agent, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

	type Result struct {
		Total   float64 `db:"total"`
		LastMod string  `db:"last_mod"`
	}
	var result Result

	err = app.DB().NewQuery(`
		SELECT COALESCE(SUM(delta), 0) as total,
		       COALESCE(MAX(created_at), '') as last_mod
		FROM candy_ledger
		WHERE agent_id = {:agentId}
	`).Bind(map[string]any{
		"agentId": agent.Id,
	}).One(&result)

	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to query balance",
		})
	}

	return e.JSON(http.StatusOK, map[string]any{
		"agent":    agent.GetString("name"),
		"balance":  result.Total,
		"last_mod": result.LastMod,
	})
}

// --- POST /v1/candies/adjust ---
func handleAdjust(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	agent, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

	var body struct {
		Delta          float64 `json:"delta"`
		Reason         string  `json:"reason"`
		IdempotencyKey string  `json:"idempotencyKey"`
	}

	if err := e.BindBody(&body); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if body.Delta == 0 {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "delta cannot be zero",
		})
	}

	if body.Delta < -1000 || body.Delta > 1000 {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "delta must be between -1000 and 1000",
		})
	}

	if body.Reason == "" {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "reason is required",
		})
	}

	if body.IdempotencyKey == "" {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "idempotencyKey is required",
		})
	}

	// Check idempotency
	existing, _ := app.FindFirstRecordByFilter("candy_ledger",
		"idempotency_key = {:key} && agent_id = {:agentId}",
		map[string]any{
			"key":     body.IdempotencyKey,
			"agentId": agent.Id,
		},
	)
	if existing != nil {
		return e.JSON(http.StatusOK, map[string]string{
			"status": "duplicate",
			"id":     existing.Id,
		})
	}

	// Create ledger entry
	collection, err := app.FindCollectionByNameOrId("candy_ledger")
	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "candy_ledger collection not found",
		})
	}

	record := core.NewRecord(collection)
	record.Set("agent_id", agent.Id) // kept for backward compatibility
	record.Set("agent", agent.Id)
	record.Set("delta", body.Delta)
	record.Set("reason", body.Reason)
	record.Set("idempotency_key", body.IdempotencyKey)

	if err := app.Save(record); err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to save ledger entry",
		})
	}

	// Get new balance
	type Result struct {
		Total float64 `db:"total"`
	}
	var result Result
	_ = app.DB().NewQuery(`
		SELECT COALESCE(SUM(delta), 0) as total
		FROM candy_ledger
		WHERE agent_id = {:agentId}
	`).Bind(map[string]any{
		"agentId": agent.Id,
	}).One(&result)

	return e.JSON(http.StatusOK, map[string]any{
		"status":      "ok",
		"id":          record.Id,
		"delta":       body.Delta,
		"reason":      body.Reason,
		"new_balance": result.Total,
	})
}

// --- GET /v1/candies/leaderboard ---
func handleLeaderboard(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	_, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

	type Entry struct {
		Name    string  `db:"name" json:"name"`
		Balance float64 `db:"balance" json:"balance"`
	}

	var entries []Entry
	err = app.DB().NewQuery(`
		SELECT a.name, COALESCE(SUM(cl.delta), 0) as balance
		FROM agents a
		LEFT JOIN candy_ledger cl ON cl.agent_id = a.id
		WHERE a.enabled = true
		GROUP BY a.id, a.name
		ORDER BY balance DESC
	`).All(&entries)

	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to query leaderboard",
		})
	}

	if entries == nil {
		entries = []Entry{}
	}

	return e.JSON(http.StatusOK, map[string]any{
		"leaderboard": entries,
	})
}

// --- GET /v1/candies/summary ---
func handleSummary(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	agent, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

	type WeekEntry struct {
		Earned float64 `db:"earned" json:"earned"`
		Spent  float64 `db:"spent" json:"spent"`
	}

	var week WeekEntry
	err = app.DB().NewQuery(`
		SELECT
			COALESCE(SUM(CASE WHEN delta > 0 THEN delta ELSE 0 END), 0) as earned,
			COALESCE(SUM(CASE WHEN delta < 0 THEN delta ELSE 0 END), 0) as spent
		FROM candy_ledger
		WHERE agent_id = {:agentId}
		  AND created_at >= datetime('now', '-7 days')
	`).Bind(map[string]any{
		"agentId": agent.Id,
	}).One(&week)

	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to query summary",
		})
	}

	type Result struct {
		Total float64 `db:"total"`
	}
	var balance Result
	_ = app.DB().NewQuery(`
		SELECT COALESCE(SUM(delta), 0) as total
		FROM candy_ledger
		WHERE agent_id = {:agentId}
	`).Bind(map[string]any{
		"agentId": agent.Id,
	}).One(&balance)

	return e.JSON(http.StatusOK, map[string]any{
		"agent":       agent.GetString("name"),
		"balance":     balance.Total,
		"week_earned": week.Earned,
		"week_spent":  week.Spent,
		"week_net":    week.Earned + week.Spent,
	})
}

// --- GET /v1/candies/history ---
func handleHistory(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	agent, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

	// Parse limit and offset
	limit, _ := strconv.Atoi(e.Request.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(e.Request.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	type Entry struct {
		Id             string  `db:"id" json:"id"`
		AgentName      string  `db:"agent_name" json:"agent_name"`
		Delta          float64 `db:"delta" json:"delta"`
		Reason         string  `db:"reason" json:"reason"`
		IdempotencyKey string  `db:"idempotency_key" json:"idempotency_key"`
		CreatedAt      string  `db:"created_at" json:"created_at"`
	}

	var entries []Entry
	err = app.DB().NewQuery(`
		SELECT cl.id, a.name as agent_name, cl.delta, cl.reason, cl.idempotency_key, COALESCE(cl.created_at, '') as created_at
		FROM candy_ledger cl
		JOIN agents a ON a.id = cl.agent_id
		WHERE cl.agent_id = {:agentId}
		ORDER BY cl.created_at DESC, cl.rowid DESC
		LIMIT {:limit} OFFSET {:offset}
	`).Bind(map[string]any{
		"agentId": agent.Id,
		"limit":   limit,
		"offset":  offset,
	}).All(&entries)

	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to query history",
		})
	}

	if entries == nil {
		entries = []Entry{}
	}

	return e.JSON(http.StatusOK, map[string]any{
		"agent":   agent.GetString("name"),
		"entries": entries,
		"limit":   limit,
		"offset":  offset,
	})
}
