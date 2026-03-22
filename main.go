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

		se.Router.POST("/v1/candies/transfer", func(e *core.RequestEvent) error {
			return handleTransfer(e, app)
		})

		se.Router.GET("/v1/transfers/history", func(e *core.RequestEvent) error {
			return handleTransferHistory(e, app)
		})

		se.Router.GET("/v1/market", func(e *core.RequestEvent) error {
			return handleMarket(e, app)
		})

		se.Router.POST("/v1/market/buy", func(e *core.RequestEvent) error {
			return handleMarketBuy(e, app)
		})

		se.Router.POST("/v1/market/sell", func(e *core.RequestEvent) error {
			return handleMarketSell(e, app)
		})

		se.Router.GET("/v1/inventory", func(e *core.RequestEvent) error {
			return handleInventory(e, app)
		})

		se.Router.GET("/v1/inventory/history", func(e *core.RequestEvent) error {
			return handleInventoryHistory(e, app)
		})

		se.Router.POST("/v1/market/refresh", func(e *core.RequestEvent) error {
			return handleMarketRefresh(e, app)
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

// --- POST /v1/candies/transfer ---
func handleTransfer(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	fromAgent, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

	var body struct {
		ToAgent        string  `json:"to_agent"`
		Amount         float64 `json:"amount"`
		Reason         string  `json:"reason"`
		IdempotencyKey string  `json:"idempotencyKey"`
	}

	if err := e.BindBody(&body); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if body.ToAgent == "" {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "to_agent is required",
		})
	}
	if body.Amount <= 0 {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "amount must be positive",
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

	// Resolve target agent by name
	toAgent, findErr := app.FindFirstRecordByFilter("agents", "name = {:name} && enabled = true", map[string]any{
		"name": body.ToAgent,
	})
	if findErr != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "target agent not found or disabled",
		})
	}

	if toAgent.Id == fromAgent.Id {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "cannot transfer to yourself",
		})
	}

	// Check idempotency
	existing, _ := app.FindFirstRecordByFilter("transfers",
		"idempotency_key = {:key}",
		map[string]any{"key": body.IdempotencyKey},
	)
	if existing != nil {
		return e.JSON(http.StatusOK, map[string]string{
			"status": "duplicate",
			"id":     existing.Id,
		})
	}

	// Check sender balance
	type BalResult struct {
		Total float64 `db:"total"`
	}
	var bal BalResult
	_ = app.DB().NewQuery(`
		SELECT COALESCE(SUM(delta), 0) as total
		FROM candy_ledger
		WHERE agent_id = {:agentId}
	`).Bind(map[string]any{
		"agentId": fromAgent.Id,
	}).One(&bal)

	if bal.Total < body.Amount {
		return e.JSON(http.StatusBadRequest, map[string]any{
			"error":   "insufficient balance",
			"balance": bal.Total,
			"amount":  body.Amount,
		})
	}

	// Run in transaction
	txErr := app.RunInTransaction(func(txApp core.App) error {
		// 1. Create transfer record
		transfersCol, err := txApp.FindCollectionByNameOrId("transfers")
		if err != nil {
			return err
		}
		transfer := core.NewRecord(transfersCol)
		transfer.Set("from_agent", fromAgent.Id)
		transfer.Set("to_agent", toAgent.Id)
		transfer.Set("amount", body.Amount)
		transfer.Set("reason", body.Reason)
		transfer.Set("idempotency_key", body.IdempotencyKey)
		if err := txApp.Save(transfer); err != nil {
			return err
		}

		// 2. Debit from sender
		ledgerCol, err := txApp.FindCollectionByNameOrId("candy_ledger")
		if err != nil {
			return err
		}
		debit := core.NewRecord(ledgerCol)
		debit.Set("agent_id", fromAgent.Id)
		debit.Set("agent", fromAgent.Id)
		debit.Set("delta", -body.Amount)
		debit.Set("reason", "transfer to "+toAgent.GetString("name")+": "+body.Reason)
		debit.Set("idempotency_key", body.IdempotencyKey+"-debit")
		debit.Set("transfer_id", transfer.Id)
		if err := txApp.Save(debit); err != nil {
			return err
		}

		// 3. Credit to receiver
		credit := core.NewRecord(ledgerCol)
		credit.Set("agent_id", toAgent.Id)
		credit.Set("agent", toAgent.Id)
		credit.Set("delta", body.Amount)
		credit.Set("reason", "transfer from "+fromAgent.GetString("name")+": "+body.Reason)
		credit.Set("idempotency_key", body.IdempotencyKey+"-credit")
		credit.Set("transfer_id", transfer.Id)
		if err := txApp.Save(credit); err != nil {
			return err
		}

		return nil
	})

	if txErr != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "transfer failed: " + txErr.Error(),
		})
	}

	// Get new balances
	var fromBal, toBal BalResult
	_ = app.DB().NewQuery(`SELECT COALESCE(SUM(delta), 0) as total FROM candy_ledger WHERE agent_id = {:id}`).
		Bind(map[string]any{"id": fromAgent.Id}).One(&fromBal)
	_ = app.DB().NewQuery(`SELECT COALESCE(SUM(delta), 0) as total FROM candy_ledger WHERE agent_id = {:id}`).
		Bind(map[string]any{"id": toAgent.Id}).One(&toBal)

	return e.JSON(http.StatusOK, map[string]any{
		"status":          "ok",
		"from":            fromAgent.GetString("name"),
		"to":              toAgent.GetString("name"),
		"amount":          body.Amount,
		"reason":          body.Reason,
		"from_new_balance": fromBal.Total,
		"to_new_balance":  toBal.Total,
	})
}

// --- GET /v1/transfers/history ---
func handleTransferHistory(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	agent, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

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
		FromName       string  `db:"from_name" json:"from"`
		ToName         string  `db:"to_name" json:"to"`
		Amount         float64 `db:"amount" json:"amount"`
		Reason         string  `db:"reason" json:"reason"`
		IdempotencyKey string  `db:"idempotency_key" json:"idempotency_key"`
		CreatedAt      string  `db:"created_at" json:"created_at"`
	}

	var entries []Entry
	err = app.DB().NewQuery(`
		SELECT t.id,
		       fa.name as from_name,
		       ta.name as to_name,
		       t.amount, t.reason, t.idempotency_key,
		       COALESCE(t.created_at, '') as created_at
		FROM transfers t
		JOIN agents fa ON fa.id = t.from_agent
		JOIN agents ta ON ta.id = t.to_agent
		WHERE t.from_agent = {:agentId} OR t.to_agent = {:agentId}
		ORDER BY t.created_at DESC
		LIMIT {:limit} OFFSET {:offset}
	`).Bind(map[string]any{
		"agentId": agent.Id,
		"limit":   limit,
		"offset":  offset,
	}).All(&entries)

	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to query transfer history",
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
