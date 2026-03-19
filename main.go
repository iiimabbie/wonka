package main

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"

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
		Total float64 `db:"total"`
	}
	var result Result

	err = app.DB().NewQuery(`
		SELECT COALESCE(SUM(delta), 0) as total
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
		"agent":   agent.GetString("name"),
		"balance": result.Total,
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
	record.Set("agent_id", agent.Id)
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
