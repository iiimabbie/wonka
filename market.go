package main

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

// ── GET /v1/market ───────────────────────────────────────────────────────────

func handleMarket(c echo.Context) error {
	type Listing struct {
		ID          string    `json:"id"`
		ItemName    string    `json:"item_name"`
		ItemDesc    string    `json:"item_description"`
		ItemType    string    `json:"item_type"`
		AnchorPrice   int       `json:"-"`
		Price       int       `json:"price"`
		ImageURL    string    `json:"image_url"`
		RefreshedAt time.Time `json:"refreshed_at"`
		ExpiresAt   *time.Time `json:"expires_at"`
	}

	rows, err := pool.Query(context.Background(), `
		SELECT ml.id, mi.name, mi.description, mi.type, mi.anchor_price, ml.price,
		       mi.image_url, ml.refreshed_at, ml.expires_at
		FROM market_listings ml
		JOIN market_items mi ON mi.id = ml.item_id
		WHERE ml.expired = false
		ORDER BY ml.price DESC
	`)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	listings := []Listing{}
	for rows.Next() {
		var l Listing
		rows.Scan(&l.ID, &l.ItemName, &l.ItemDesc, &l.ItemType, &l.AnchorPrice, &l.Price,
			&l.ImageURL, &l.RefreshedAt, &l.ExpiresAt)
		listings = append(listings, l)
	}

	// Latest event
	type Event struct {
		Description string    `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
	}
	var event *Event
	var e Event
	err = pool.QueryRow(context.Background(),
		`SELECT description, created_at FROM market_events ORDER BY created_at DESC LIMIT 1`,
	).Scan(&e.Description, &e.CreatedAt)
	if err == nil {
		event = &e
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"listings": listings,
		"event":    event,
	})
}

// ── POST /v1/market/buy ──────────────────────────────────────────────────────

func handleMarketBuy(c echo.Context) error {
	agent := c.Get("agent").(*Agent)
	type req struct {
		ListingID      string `json:"listing_id"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	var r req
	if err := c.Bind(&r); err != nil || r.ListingID == "" || r.IdempotencyKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "listing_id and idempotencyKey required"})
	}

	// Idempotency check
	var exists bool
	pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM candy_ledger WHERE agent_id = $1 AND idempotency_key = $2)`,
		agent.ID, r.IdempotencyKey,
	).Scan(&exists)
	if exists {
		return c.JSON(http.StatusOK, map[string]string{"status": "duplicate"})
	}

	// Get listing
	var itemID string
	var itemName string
	var price int
	err := pool.QueryRow(context.Background(), `
		SELECT ml.item_id, mi.name, ml.price
		FROM market_listings ml
		JOIN market_items mi ON mi.id = ml.item_id
		WHERE ml.id = $1 AND ml.expired = false
	`, r.ListingID).Scan(&itemID, &itemName, &price)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "listing not found or expired"})
	}

	// Check balance
	var balance int
	pool.QueryRow(context.Background(),
		`SELECT COALESCE(balance, 0) FROM agent_balances WHERE id = $1`, agent.ID,
	).Scan(&balance)
	if balance < price {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "insufficient balance",
			"balance": balance,
			"price":   price,
		})
	}

	// Transaction: debit + inventory + price history
	tx, err := pool.Begin(context.Background())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer tx.Rollback(context.Background())

	_, err = tx.Exec(context.Background(),
		`INSERT INTO candy_ledger (agent_id, delta, reason, idempotency_key) VALUES ($1, $2, $3, $4)`,
		agent.ID, -price, "market buy: "+itemName, r.IdempotencyKey,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "ledger error"})
	}

	_, err = tx.Exec(context.Background(),
		`INSERT INTO inventories (agent_id, item_id, acquired_price) VALUES ($1, $2, $3)`,
		agent.ID, itemID, price,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "inventory error"})
	}

	if err := tx.Commit(context.Background()); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "commit error"})
	}

	// New balance
	var newBalance int
	pool.QueryRow(context.Background(),
		`SELECT COALESCE(balance, 0) FROM agent_balances WHERE id = $1`, agent.ID,
	).Scan(&newBalance)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":      "ok",
		"item":        itemName,
		"price":       price,
		"new_balance": newBalance,
	})
}

// ── POST /v1/market/sell ─────────────────────────────────────────────────────

func handleMarketSell(c echo.Context) error {
	agent := c.Get("agent").(*Agent)
	type req struct {
		InventoryID    string `json:"inventory_id"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	var r req
	if err := c.Bind(&r); err != nil || r.InventoryID == "" || r.IdempotencyKey == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "inventory_id and idempotencyKey required"})
	}

	// Idempotency
	var exists bool
	pool.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM candy_ledger WHERE agent_id = $1 AND idempotency_key = $2)`,
		agent.ID, r.IdempotencyKey,
	).Scan(&exists)
	if exists {
		return c.JSON(http.StatusOK, map[string]string{"status": "duplicate"})
	}

	// Get inventory item (must be unsold + owned)
	var itemID, itemName string
	var acquiredPrice int
	var soldAt *time.Time
	err := pool.QueryRow(context.Background(), `
		SELECT inv.item_id, mi.name, inv.acquired_price, inv.sold_at
		FROM inventories inv
		JOIN market_items mi ON mi.id = inv.item_id
		WHERE inv.id = $1 AND inv.agent_id = $2
	`, r.InventoryID, agent.ID).Scan(&itemID, &itemName, &acquiredPrice, &soldAt)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "inventory item not found"})
	}
	if soldAt != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "item already sold"})
	}

	// Sell price: current listing → last price history → 1 (absolute fallback)
	var sellPrice int
	err = pool.QueryRow(context.Background(), `
		SELECT ml.price FROM market_listings ml
		WHERE ml.item_id = $1 AND ml.expired = false
		ORDER BY ml.refreshed_at DESC LIMIT 1
	`, itemID).Scan(&sellPrice)
	if err != nil {
		// fallback to most recent price history
		err2 := pool.QueryRow(context.Background(),
			`SELECT price FROM market_price_history WHERE item_id = $1 ORDER BY refreshed_at DESC LIMIT 1`, itemID,
		).Scan(&sellPrice)
		if err2 != nil {
			sellPrice = 1 // absolute fallback
		}
	}

	// Transaction: credit + mark sold
	tx, err := pool.Begin(context.Background())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer tx.Rollback(context.Background())

	_, err = tx.Exec(context.Background(),
		`INSERT INTO candy_ledger (agent_id, delta, reason, idempotency_key) VALUES ($1, $2, $3, $4)`,
		agent.ID, sellPrice, "market sell: "+itemName, r.IdempotencyKey,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "ledger error"})
	}

	_, err = tx.Exec(context.Background(),
		`UPDATE inventories SET sold_at = now(), sold_price = $1 WHERE id = $2`,
		sellPrice, r.InventoryID,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "inventory update error"})
	}

	if err := tx.Commit(context.Background()); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "commit error"})
	}

	var newBalance int
	pool.QueryRow(context.Background(),
		`SELECT COALESCE(balance, 0) FROM agent_balances WHERE id = $1`, agent.ID,
	).Scan(&newBalance)

	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":         "ok",
		"item":           itemName,
		"acquired_price": acquiredPrice,
		"sell_price":     sellPrice,
		"new_balance":    newBalance,
	})
}

// ── GET /v1/inventory ────────────────────────────────────────────────────────

func handleInventory(c echo.Context) error {
	agent := c.Get("agent").(*Agent)
	rows, err := pool.Query(context.Background(), `
		SELECT inv.id, mi.name, mi.type, inv.acquired_price
		FROM inventories inv
		JOIN market_items mi ON mi.id = inv.item_id
		WHERE inv.agent_id = $1 AND inv.sold_at IS NULL
		ORDER BY mi.name, inv.acquired_price
	`, agent.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	var rawItems []rawInventoryItem
	for rows.Next() {
		var i rawInventoryItem
		rows.Scan(&i.ID, &i.ItemName, &i.ItemType, &i.AcquiredPrice)
		rawItems = append(rawItems, i)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"agent": agent.Name,
		"items": groupInventory(rawItems),
	})
}

type inventoryHolding struct {
	Price    int      `json:"price"`
	Quantity int      `json:"quantity"`
	IDs      []string `json:"ids"`
}

type groupedInventoryItem struct {
	ItemName string             `json:"item_name"`
	ItemType string             `json:"item_type"`
	Total    int                `json:"total"`
	Holdings []inventoryHolding `json:"holdings"`
}

type rawInventoryItem struct {
	ID            string
	ItemName      string
	ItemType      string
	AcquiredPrice int
}

func groupInventory(rawItems []rawInventoryItem) []groupedInventoryItem {
	// Group by item_name, then by price
	type key struct{ Name string }
	orderMap := map[string]*groupedInventoryItem{}
	var order []string

	for _, r := range rawItems {
		g, exists := orderMap[r.ItemName]
		if !exists {
			g = &groupedInventoryItem{ItemName: r.ItemName, ItemType: r.ItemType}
			orderMap[r.ItemName] = g
			order = append(order, r.ItemName)
		}
		// Find or create holding for this price
		found := false
		for i := range g.Holdings {
			if g.Holdings[i].Price == r.AcquiredPrice {
				g.Holdings[i].Quantity++
				g.Holdings[i].IDs = append(g.Holdings[i].IDs, r.ID)
				found = true
				break
			}
		}
		if !found {
			g.Holdings = append(g.Holdings, inventoryHolding{
				Price:    r.AcquiredPrice,
				Quantity: 1,
				IDs:      []string{r.ID},
			})
		}
		g.Total++
	}

	var result []groupedInventoryItem
	for _, name := range order {
		result = append(result, *orderMap[name])
	}
	if result == nil {
		result = []groupedInventoryItem{}
	}
	return result
}

// ── GET /v1/inventory/history ────────────────────────────────────────────────

func handleInventoryHistory(c echo.Context) error {
	agent := c.Get("agent").(*Agent)

	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	if offset < 0 {
		offset = 0
	}

	type item struct {
		ID            string     `json:"id"`
		ItemName      string     `json:"item_name"`
		ItemType      string     `json:"item_type"`
		AcquiredAt    time.Time  `json:"acquired_at"`
		AcquiredPrice int        `json:"acquired_price"`
		SoldAt        *time.Time `json:"sold_at"`
		SoldPrice     *int       `json:"sold_price"`
	}
	rows, err := pool.Query(context.Background(), `
		SELECT inv.id, mi.name, mi.type, inv.acquired_at, inv.acquired_price, inv.sold_at, inv.sold_price
		FROM inventories inv
		JOIN market_items mi ON mi.id = inv.item_id
		WHERE inv.agent_id = $1 AND inv.sold_at IS NOT NULL
		ORDER BY inv.sold_at DESC
		LIMIT $2 OFFSET $3
	`, agent.ID, limit, offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	items := []item{}
	for rows.Next() {
		var i item
		rows.Scan(&i.ID, &i.ItemName, &i.ItemType, &i.AcquiredAt, &i.AcquiredPrice, &i.SoldAt, &i.SoldPrice)
		items = append(items, i)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"agent":  agent.Name,
		"items":  items,
		"limit":  limit,
		"offset": offset,
	})
}

// ── GET /v1/market/prices ────────────────────────────────────────────────────

func handlePriceHistory(c echo.Context) error {
	itemID := c.QueryParam("item_id")
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	type point struct {
		Price       int       `json:"price"`
		RefreshedAt time.Time `json:"refreshed_at"`
	}

	var rows interface{ Close() }
	var err error
	var pts []point

	if itemID != "" {
		r, e := pool.Query(context.Background(),
			`SELECT price, refreshed_at FROM market_price_history WHERE item_id = $1 ORDER BY refreshed_at DESC LIMIT $2`,
			itemID, limit,
		)
		rows = r
		err = e
		if err == nil {
			defer r.Close()
			for r.Next() {
				var p point
				r.Scan(&p.Price, &p.RefreshedAt)
				pts = append(pts, p)
			}
		}
	} else {
		r, e := pool.Query(context.Background(),
			`SELECT price, refreshed_at FROM market_price_history ORDER BY refreshed_at DESC LIMIT $1`,
			limit,
		)
		rows = r
		err = e
		if err == nil {
			defer r.Close()
			for r.Next() {
				var p point
				r.Scan(&p.Price, &p.RefreshedAt)
				pts = append(pts, p)
			}
		}
	}
	_ = rows

	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	if pts == nil {
		pts = []point{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"prices": pts})
}

// ── GET /v1/market/events ────────────────────────────────────────────────────

func handleMarketEvents(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > 50 {
		limit = 14
	}

	type event struct {
		Description string    `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
	}
	rows, err := pool.Query(context.Background(),
		`SELECT description, created_at FROM market_events ORDER BY created_at DESC LIMIT $1`, limit,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	defer rows.Close()

	events := []event{}
	for rows.Next() {
		var e event
		rows.Scan(&e.Description, &e.CreatedAt)
		events = append(events, e)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"events": events})
}

// ── GET /v1/market/snapshot (agent-key auth) ─────────────────────────────────

func handleMarketSnapshot(c echo.Context) error {
	ctx := context.Background()
	agent := c.Get("agent").(*Agent)

	// 1. Balance
	var balance int
	pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(delta), 0) FROM candy_ledger WHERE agent_id = $1`, agent.ID,
	).Scan(&balance)

	// 2. Inventory (grouped by item + price)
	invRows, err := pool.Query(ctx, `
		SELECT inv.id, mi.name, mi.type, inv.acquired_price
		FROM inventories inv
		JOIN market_items mi ON mi.id = inv.item_id
		WHERE inv.agent_id = $1 AND inv.sold_at IS NULL
		ORDER BY mi.name, inv.acquired_price
	`, agent.ID)
	var rawInv []rawInventoryItem
	if err == nil {
		defer invRows.Close()
		for invRows.Next() {
			var i rawInventoryItem
			invRows.Scan(&i.ID, &i.ItemName, &i.ItemType, &i.AcquiredPrice)
			rawInv = append(rawInv, i)
		}
	}
	inventory := groupInventory(rawInv)

	// 3. Listings with change_pct, volume, recent_prices
	type snapshotListing struct {
		ID           string     `json:"id"`
		ItemName     string     `json:"item_name"`
		ItemDesc     string     `json:"item_description"`
		ItemType     string     `json:"item_type"`
		Price        int        `json:"price"`
		ChangePct    float64    `json:"change_pct"`
		RecentPrices []int      `json:"recent_prices"`
		Buy1h        int        `json:"buy_1h"`
		Sell1h       int        `json:"sell_1h"`
		Buy24h       int        `json:"buy_24h"`
		Sell24h      int        `json:"sell_24h"`
		RefreshedAt  time.Time  `json:"refreshed_at"`
		ExpiresAt    *time.Time `json:"expires_at"`
	}

	// Batch: get all volume data in one query
	type volData struct {
		Buy1h, Sell1h, Buy24h, Sell24h int
	}
	volMap := map[string]*volData{}
	volRows, _ := pool.Query(ctx, `
		SELECT item_id,
			COUNT(*) FILTER (WHERE acquired_at >= now() - interval '1 hour') AS buy_1h,
			COUNT(*) FILTER (WHERE sold_at >= now() - interval '1 hour') AS sell_1h,
			COUNT(*) FILTER (WHERE acquired_at >= now() - interval '24 hours') AS buy_24h,
			COUNT(*) FILTER (WHERE sold_at >= now() - interval '24 hours') AS sell_24h
		FROM inventories GROUP BY item_id
	`)
	if volRows != nil {
		for volRows.Next() {
			var id string
			var v volData
			volRows.Scan(&id, &v.Buy1h, &v.Sell1h, &v.Buy24h, &v.Sell24h)
			volMap[id] = &v
		}
		volRows.Close()
	}

	// Batch: get all recent prices (last 10 per item) using window function
	type pricePoint struct {
		ItemID string
		Price  int
	}
	priceMap := map[string][]int{}
	priceRows, _ := pool.Query(ctx, `
		SELECT item_id, price FROM (
			SELECT item_id, price, ROW_NUMBER() OVER (PARTITION BY item_id ORDER BY refreshed_at DESC) AS rn
			FROM market_price_history
		) sub WHERE rn <= 10
		ORDER BY item_id, rn
	`)
	if priceRows != nil {
		for priceRows.Next() {
			var pp pricePoint
			priceRows.Scan(&pp.ItemID, &pp.Price)
			priceMap[pp.ItemID] = append(priceMap[pp.ItemID], pp.Price)
		}
		priceRows.Close()
	}

	// Now build listings with pre-fetched data
	listRows, err := pool.Query(ctx, `
		SELECT ml.id, mi.id, mi.name, mi.description, mi.type, ml.price,
		       ml.refreshed_at, ml.expires_at
		FROM market_listings ml
		JOIN market_items mi ON mi.id = ml.item_id
		WHERE ml.expired = false
		ORDER BY ml.price DESC
	`)
	var listings []snapshotListing
	if err == nil {
		defer listRows.Close()
		for listRows.Next() {
			var l snapshotListing
			var itemID string
			listRows.Scan(&l.ID, &itemID, &l.ItemName, &l.ItemDesc, &l.ItemType, &l.Price,
				&l.RefreshedAt, &l.ExpiresAt)

			// Recent prices from batch
			l.RecentPrices = priceMap[itemID]
			if l.RecentPrices == nil {
				l.RecentPrices = []int{}
			}

			// change_pct vs previous price
			if len(l.RecentPrices) >= 2 {
				prev := l.RecentPrices[1]
				if prev > 0 {
					l.ChangePct = math.Round(float64(l.Price-prev)/float64(prev)*10000) / 10000
				}
			}

			// Volume from batch
			if v, ok := volMap[itemID]; ok {
				l.Buy1h = v.Buy1h
				l.Sell1h = v.Sell1h
				l.Buy24h = v.Buy24h
				l.Sell24h = v.Sell24h
			}

			listings = append(listings, l)
		}
	}
	if listings == nil {
		listings = []snapshotListing{}
	}

	// 4. Latest event + recent events
	type eventInfo struct {
		Description string    `json:"description"`
		CreatedAt   time.Time `json:"created_at"`
	}
	var latestEvent *eventInfo
	var le eventInfo
	if pool.QueryRow(ctx,
		`SELECT description, created_at FROM market_events ORDER BY created_at DESC LIMIT 1`,
	).Scan(&le.Description, &le.CreatedAt) == nil {
		latestEvent = &le
	}

	evtRows, _ := pool.Query(ctx,
		`SELECT description, created_at FROM market_events ORDER BY created_at DESC LIMIT 5`)
	var recentEvents []eventInfo
	if evtRows != nil {
		for evtRows.Next() {
			var e eventInfo
			evtRows.Scan(&e.Description, &e.CreatedAt)
			recentEvents = append(recentEvents, e)
		}
		evtRows.Close()
	}
	if recentEvents == nil {
		recentEvents = []eventInfo{}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"balance":       balance,
		"inventory":     inventory,
		"listings":      listings,
		"event":         latestEvent,
		"recent_events": recentEvents,
	})
}
