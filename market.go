package main

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

// --- GET /v1/market ---
func handleMarket(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	_, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

	type Listing struct {
		Id          string  `db:"id" json:"id"`
		ItemName    string  `db:"item_name" json:"item_name"`
		ItemDesc    string  `db:"item_desc" json:"item_description"`
		ItemType    string  `db:"item_type" json:"item_type"`
		BasePrice   float64 `db:"base_price" json:"base_price"`
		Price       float64 `db:"price" json:"price"`
		ImageUrl    string  `db:"image_url" json:"image_url"`
		RefreshedAt string  `db:"refreshed_at" json:"refreshed_at"`
		ExpiresAt   string  `db:"expires_at" json:"expires_at"`
	}

	var listings []Listing
	err = app.DB().NewQuery(`
		SELECT ml.id, mi.name as item_name, mi.description as item_desc,
		       mi.type as item_type, mi.base_price, ml.price,
		       COALESCE(mi.image_url, '') as image_url,
		       COALESCE(ml.refreshed_at, '') as refreshed_at,
		       COALESCE(ml.expires_at, '') as expires_at
		FROM market_listings ml
		JOIN market_items mi ON mi.id = ml.item_id
		WHERE ml.expired = false
		ORDER BY ml.price DESC
	`).All(&listings)

	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to query market",
		})
	}

	if listings == nil {
		listings = []Listing{}
	}

	// Get latest event
	type Event struct {
		Description string `db:"description" json:"description"`
		HappenedAt  string `db:"happened_at" json:"happened_at"`
	}
	var event Event
	_ = app.DB().NewQuery(`
		SELECT description, COALESCE(happened_at, '') as happened_at
		FROM market_events
		ORDER BY happened_at DESC
		LIMIT 1
	`).One(&event)

	return e.JSON(http.StatusOK, map[string]any{
		"listings": listings,
		"event":    event,
	})
}

// --- POST /v1/market/buy ---
func handleMarketBuy(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	agent, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

	var body struct {
		ListingId      string `json:"listing_id"`
		IdempotencyKey string `json:"idempotencyKey"`
	}

	if err := e.BindBody(&body); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if body.ListingId == "" {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "listing_id is required",
		})
	}
	if body.IdempotencyKey == "" {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "idempotencyKey is required",
		})
	}

	// Check idempotency via ledger
	existing, _ := app.FindFirstRecordByFilter("candy_ledger",
		"idempotency_key = {:key} && agent_id = {:agentId}",
		map[string]any{"key": body.IdempotencyKey, "agentId": agent.Id},
	)
	if existing != nil {
		return e.JSON(http.StatusOK, map[string]string{
			"status": "duplicate",
		})
	}

	// Find listing
	listing, findErr := app.FindRecordById("market_listings", body.ListingId)
	if findErr != nil || listing.GetBool("expired") {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "listing not found or expired",
		})
	}

	price := listing.GetFloat("price")

	// Check balance
	type BalResult struct {
		Total float64 `db:"total"`
	}
	var bal BalResult
	_ = app.DB().NewQuery(`
		SELECT COALESCE(SUM(delta), 0) as total FROM candy_ledger WHERE agent_id = {:id}
	`).Bind(map[string]any{"id": agent.Id}).One(&bal)

	if bal.Total < price {
		return e.JSON(http.StatusBadRequest, map[string]any{
			"error":   "insufficient balance",
			"balance": bal.Total,
			"price":   price,
		})
	}

	// Get item info
	itemId := listing.GetString("item_id")
	item, _ := app.FindRecordById("market_items", itemId)
	itemName := ""
	if item != nil {
		itemName = item.GetString("name")
	}

	// Transaction: debit + inventory + price history
	txErr := app.RunInTransaction(func(txApp core.App) error {
		// 1. Debit candy
		ledgerCol, err := txApp.FindCollectionByNameOrId("candy_ledger")
		if err != nil {
			return err
		}
		debit := core.NewRecord(ledgerCol)
		debit.Set("agent_id", agent.Id)
		debit.Set("agent", agent.Id)
		debit.Set("delta", -price)
		debit.Set("reason", "market buy: "+itemName)
		debit.Set("idempotency_key", body.IdempotencyKey)
		if err := txApp.Save(debit); err != nil {
			return err
		}

		// 2. Create inventory record
		invCol, err := txApp.FindCollectionByNameOrId("inventories")
		if err != nil {
			return err
		}
		inv := core.NewRecord(invCol)
		inv.Set("agent_id", agent.Id)
		inv.Set("item_id", itemId)
		inv.Set("acquired_price", price)
		if err := txApp.Save(inv); err != nil {
			return err
		}

		// 3. Price history snapshot
		phCol, err := txApp.FindCollectionByNameOrId("market_price_history")
		if err != nil {
			return err
		}
		ph := core.NewRecord(phCol)
		ph.Set("item_id", itemId)
		ph.Set("price", price)
		if err := txApp.Save(ph); err != nil {
			return err
		}

		return nil
	})

	if txErr != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "purchase failed: " + txErr.Error(),
		})
	}

	// New balance
	var newBal BalResult
	_ = app.DB().NewQuery(`
		SELECT COALESCE(SUM(delta), 0) as total FROM candy_ledger WHERE agent_id = {:id}
	`).Bind(map[string]any{"id": agent.Id}).One(&newBal)

	return e.JSON(http.StatusOK, map[string]any{
		"status":      "ok",
		"item":        itemName,
		"price":       price,
		"new_balance": newBal.Total,
	})
}

// --- POST /v1/market/sell ---
func handleMarketSell(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	agent, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

	var body struct {
		InventoryId    string `json:"inventory_id"`
		IdempotencyKey string `json:"idempotencyKey"`
	}

	if err := e.BindBody(&body); err != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if body.InventoryId == "" {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "inventory_id is required",
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
		map[string]any{"key": body.IdempotencyKey, "agentId": agent.Id},
	)
	if existing != nil {
		return e.JSON(http.StatusOK, map[string]string{
			"status": "duplicate",
		})
	}

	// Find inventory record
	inv, findErr := app.FindRecordById("inventories", body.InventoryId)
	if findErr != nil {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "inventory item not found",
		})
	}

	// Verify ownership
	if inv.GetString("agent_id") != agent.Id {
		return e.JSON(http.StatusForbidden, map[string]string{
			"error": "not your item",
		})
	}

	// Check not already sold
	soldAt := inv.GetString("sold_at")
	if soldAt != "" {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "item already sold",
		})
	}

	acquiredPrice := inv.GetFloat("acquired_price")
	sellPrice := math.Floor(acquiredPrice / 2)

	// Get item name
	itemId := inv.GetString("item_id")
	item, _ := app.FindRecordById("market_items", itemId)
	itemName := ""
	if item != nil {
		itemName = item.GetString("name")
	}

	// Transaction: credit + mark sold
	txErr := app.RunInTransaction(func(txApp core.App) error {
		// 1. Credit candy (half price)
		ledgerCol, err := txApp.FindCollectionByNameOrId("candy_ledger")
		if err != nil {
			return err
		}
		credit := core.NewRecord(ledgerCol)
		credit.Set("agent_id", agent.Id)
		credit.Set("agent", agent.Id)
		credit.Set("delta", sellPrice)
		credit.Set("reason", "market sell: "+itemName)
		credit.Set("idempotency_key", body.IdempotencyKey)
		if err := txApp.Save(credit); err != nil {
			return err
		}

		// 2. Mark inventory as sold
		invRecord, err := txApp.FindRecordById("inventories", body.InventoryId)
		if err != nil {
			return err
		}
		invRecord.Set("sold_at", time.Now().UTC().Format("2006-01-02 15:04:05.000Z"))
		if err := txApp.Save(invRecord); err != nil {
			return err
		}

		return nil
	})

	if txErr != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "sell failed: " + txErr.Error(),
		})
	}

	// New balance
	type BalResult struct {
		Total float64 `db:"total"`
	}
	var newBal BalResult
	_ = app.DB().NewQuery(`
		SELECT COALESCE(SUM(delta), 0) as total FROM candy_ledger WHERE agent_id = {:id}
	`).Bind(map[string]any{"id": agent.Id}).One(&newBal)

	return e.JSON(http.StatusOK, map[string]any{
		"status":         "ok",
		"item":           itemName,
		"acquired_price": acquiredPrice,
		"sell_price":     sellPrice,
		"new_balance":    newBal.Total,
	})
}

// --- GET /v1/inventory ---
func handleInventory(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	agent, err := resolveAgent(e, app)
	if err != nil {
		return err
	}

	type Item struct {
		Id            string  `db:"id" json:"id"`
		ItemName      string  `db:"item_name" json:"item_name"`
		ItemType      string  `db:"item_type" json:"item_type"`
		AcquiredAt    string  `db:"acquired_at" json:"acquired_at"`
		AcquiredPrice float64 `db:"acquired_price" json:"acquired_price"`
	}

	var items []Item
	err = app.DB().NewQuery(`
		SELECT inv.id, mi.name as item_name, mi.type as item_type,
		       COALESCE(inv.acquired_at, '') as acquired_at, inv.acquired_price
		FROM inventories inv
		JOIN market_items mi ON mi.id = inv.item_id
		WHERE inv.agent_id = {:agentId} AND (inv.sold_at IS NULL OR inv.sold_at = '')
		ORDER BY inv.acquired_at DESC
	`).Bind(map[string]any{
		"agentId": agent.Id,
	}).All(&items)

	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to query inventory",
		})
	}

	if items == nil {
		items = []Item{}
	}

	return e.JSON(http.StatusOK, map[string]any{
		"agent": agent.GetString("name"),
		"items": items,
	})
}

// --- GET /v1/inventory/history ---
func handleInventoryHistory(e *core.RequestEvent, app *pocketbase.PocketBase) error {
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

	type Item struct {
		Id            string  `db:"id" json:"id"`
		ItemName      string  `db:"item_name" json:"item_name"`
		ItemType      string  `db:"item_type" json:"item_type"`
		AcquiredAt    string  `db:"acquired_at" json:"acquired_at"`
		AcquiredPrice float64 `db:"acquired_price" json:"acquired_price"`
		SoldAt        string  `db:"sold_at" json:"sold_at"`
	}

	var items []Item
	err = app.DB().NewQuery(`
		SELECT inv.id, mi.name as item_name, mi.type as item_type,
		       COALESCE(inv.acquired_at, '') as acquired_at, inv.acquired_price,
		       COALESCE(inv.sold_at, '') as sold_at
		FROM inventories inv
		JOIN market_items mi ON mi.id = inv.item_id
		WHERE inv.agent_id = {:agentId} AND inv.sold_at IS NOT NULL AND inv.sold_at != ''
		ORDER BY inv.sold_at DESC
		LIMIT {:limit} OFFSET {:offset}
	`).Bind(map[string]any{
		"agentId": agent.Id,
		"limit":   limit,
		"offset":  offset,
	}).All(&items)

	if err != nil {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to query inventory history",
		})
	}

	if items == nil {
		items = []Item{}
	}

	return e.JSON(http.StatusOK, map[string]any{
		"agent":  agent.GetString("name"),
		"items":  items,
		"limit":  limit,
		"offset": offset,
	})
}
