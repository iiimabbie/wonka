package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v4"
)

type dbItem struct {
	ID          string
	Name        string
	ItemType    string
	AnchorPrice int
}

// ── POST /v1/market/refresh (X-Admin-Key) ────────────────────────────────────

func handleMarketRefresh(c echo.Context) error {
	adminKey := os.Getenv("WONKA_ADMIN_KEY")
	if adminKey == "" {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "WONKA_ADMIN_KEY not configured"})
	}
	if c.Request().Header.Get("X-Admin-Key") != adminKey {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid admin key"})
	}
	return doMarketRefresh(c)
}

// ── POST /v1/market/hourly-refresh (X-Admin-Key) ─────────────────────────────

func handleHourlyRefresh(c echo.Context) error {
	adminKey := os.Getenv("WONKA_ADMIN_KEY")
	if adminKey == "" {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "WONKA_ADMIN_KEY not configured"})
	}
	if c.Request().Header.Get("X-Admin-Key") != adminKey {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid admin key"})
	}
	res, err := runHourlyPriceRefresh()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

type refreshResult struct {
	Listings []struct {
		Name  string  `json:"name"`
		Price int     `json:"price"`
		Delta float64 `json:"delta"`
	} `json:"listings"`
	Event      string `json:"event,omitempty"`
	AIFallback bool   `json:"ai_fallback,omitempty"`
	AIError    string `json:"ai_error,omitempty"`
	Count      int    `json:"count"`
}

// ── Full refresh (daily: event + pricing) ────────────────────────────────────

func runMarketRefresh() (*refreshResult, error) {
	ctx := context.Background()

	rows, err := pool.Query(ctx,
		`SELECT id, name, type, anchor_price FROM market_items WHERE enabled = true`,
	)
	if err != nil {
		return nil, err
	}
	var allItems []dbItem
	for rows.Next() {
		var i dbItem
		rows.Scan(&i.ID, &i.Name, &i.ItemType, &i.AnchorPrice)
		allItems = append(allItems, i)
	}
	rows.Close()

	if len(allItems) == 0 {
		return nil, fmt.Errorf("no enabled items found")
	}

	effects, eventDesc, model, aiErr := generateAIPricing(ctx, allItems)
	pool.Exec(ctx, `UPDATE market_listings SET expired = true WHERE expired = false`)

	var eventID string
	if aiErr == nil && eventDesc != "" {
		effectJSON, _ := json.Marshal(effects)
		pool.QueryRow(ctx,
			`INSERT INTO market_events (description, effect, model) VALUES ($1, $2, $3) RETURNING id`,
			eventDesc, string(effectJSON), model,
		).Scan(&eventID)
	}

	expiresAt := time.Now().Add(12 * time.Hour)
	res := &refreshResult{}

	for _, item := range allItems {
		var delta float64
		if effects != nil {
			delta = effects[item.Name]
		}
		if delta == 0 && aiErr != nil {
			delta = rand.Float64()*0.6 - 0.3
		}
		if delta > 0.5 {
			delta = 0.5
		}
		price := int(math.Max(1, math.Round(float64(item.AnchorPrice)*(1+delta))))

		if eventID != "" {
			pool.Exec(ctx,
				`INSERT INTO market_listings (item_id, price, event_id, expired, expires_at) VALUES ($1, $2, $3, false, $4)`,
				item.ID, price, eventID, expiresAt,
			)
		} else {
			pool.Exec(ctx,
				`INSERT INTO market_listings (item_id, price, expired, expires_at) VALUES ($1, $2, false, $3)`,
				item.ID, price, expiresAt,
			)
		}
		pool.Exec(ctx, `INSERT INTO market_price_history (item_id, price) VALUES ($1, $2)`, item.ID, price)
		res.Listings = append(res.Listings, struct {
			Name  string  `json:"name"`
			Price int     `json:"price"`
			Delta float64 `json:"delta"`
		}{item.Name, price, delta})
	}

	res.Count = len(res.Listings)
	res.Event = eventDesc
	if aiErr != nil {
		res.AIFallback = true
		res.AIError = aiErr.Error()
	}

	// After daily refresh: update anchor_price toward 7-day avg
	go updateAnchorPrices(ctx)

	return res, nil
}

func doMarketRefresh(c echo.Context) error {
	res, err := runMarketRefresh()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, res)
}

// ── Hourly refresh (volume-driven price only, no new event) ──────────────────

type volumeItem struct {
	ID           string
	Name         string
	AnchorPrice  int
	CurrentPrice int
	BuyCount     int
	SellCount    int
}

func runHourlyPriceRefresh() (*refreshResult, error) {
	ctx := context.Background()

	rows, err := pool.Query(ctx,
		`SELECT id, name, type, anchor_price FROM market_items WHERE enabled = true`,
	)
	if err != nil {
		return nil, err
	}
	var allItems []dbItem
	for rows.Next() {
		var i dbItem
		rows.Scan(&i.ID, &i.Name, &i.ItemType, &i.AnchorPrice)
		allItems = append(allItems, i)
	}
	rows.Close()

	if len(allItems) == 0 {
		return nil, fmt.Errorf("no enabled items found")
	}

	// Get current prices
	currentPrices := map[string]int{}
	priceRows, _ := pool.Query(ctx, `
		SELECT mi.id, ml.price
		FROM market_listings ml
		JOIN market_items mi ON mi.id = ml.item_id
		WHERE ml.expired = false
	`)
	if priceRows != nil {
		for priceRows.Next() {
			var id string
			var price int
			priceRows.Scan(&id, &price)
			currentPrices[id] = price
		}
		priceRows.Close()
	}

	// Get latest event for context
	var latestEvent string
	pool.QueryRow(ctx,
		`SELECT description FROM market_events ORDER BY created_at DESC LIMIT 1`,
	).Scan(&latestEvent)

	// Build volume data for each item (batch, 1 query)
	buyMap := map[string]int{}
	sellMap := map[string]int{}
	volRows, _ := pool.Query(ctx, `
		SELECT item_id,
			COUNT(*) FILTER (WHERE acquired_at >= now() - interval '1 hour') AS buy_1h,
			COUNT(*) FILTER (WHERE sold_at >= now() - interval '1 hour') AS sell_1h
		FROM inventories
		WHERE acquired_at >= now() - interval '1 hour' OR sold_at >= now() - interval '1 hour'
		GROUP BY item_id
	`)
	if volRows != nil {
		for volRows.Next() {
			var id string
			var b, s int
			volRows.Scan(&id, &b, &s)
			buyMap[id] = b
			sellMap[id] = s
		}
		volRows.Close()
	}

	var volItems []volumeItem
	for _, item := range allItems {
		cur := currentPrices[item.ID]
		if cur == 0 {
			cur = item.AnchorPrice
		}
		volItems = append(volItems, volumeItem{
			ID:           item.ID,
			Name:         item.Name,
			AnchorPrice:  item.AnchorPrice,
			CurrentPrice: cur,
			BuyCount:     buyMap[item.ID],
			SellCount:    sellMap[item.ID],
		})
	}

	// Check if any trades happened at all
	totalTrades := 0
	for _, v := range volItems {
		totalTrades += v.BuyCount + v.SellCount
	}

	res := &refreshResult{}

	if totalTrades == 0 {
		// No trades: apply gentle decay toward anchor_price (2% per hour)
		pool.Exec(ctx, `UPDATE market_listings SET expired = true WHERE expired = false`)
		expiresAt := time.Now().Add(1 * time.Hour)
		for _, v := range volItems {
			cur := float64(v.CurrentPrice)
			anchor := float64(v.AnchorPrice)
			newPrice := int(math.Round(cur + (anchor-cur)*0.02))
			if newPrice < 1 {
				newPrice = 1
			}
			pool.Exec(ctx,
				`INSERT INTO market_listings (item_id, price, expired, expires_at) VALUES ($1, $2, false, $3)`,
				v.ID, newPrice, expiresAt,
			)
			pool.Exec(ctx, `INSERT INTO market_price_history (item_id, price) VALUES ($1, $2)`, v.ID, newPrice)
			delta := float64(newPrice-v.CurrentPrice) / float64(v.CurrentPrice)
			res.Listings = append(res.Listings, struct {
				Name  string  `json:"name"`
				Price int     `json:"price"`
				Delta float64 `json:"delta"`
			}{v.Name, newPrice, delta})
		}
		res.Count = len(res.Listings)
		res.Event = "（無交易，價格緩慢回歸）"
		return res, nil
	}

	// Has trades: ask AI to adjust prices based on volume
	effects, _, aiModel, aiErr := generateHourlyPricing(ctx, volItems, latestEvent)

	pool.Exec(ctx, `UPDATE market_listings SET expired = true WHERE expired = false`)
	expiresAt := time.Now().Add(1 * time.Hour)

	for _, v := range volItems {
		var newPrice int
		if aiErr == nil && effects != nil {
			delta := effects[v.Name]
			if delta > 0.2 {
				delta = 0.2
			}
			if delta < -0.2 {
				delta = -0.2
			}
			newPrice = int(math.Max(1, math.Round(float64(v.CurrentPrice)*(1+delta))))
		} else {
			// fallback: simple supply/demand
			net := v.BuyCount - v.SellCount
			delta := float64(net) * 0.03
			if delta > 0.2 {
				delta = 0.2
			}
			if delta < -0.2 {
				delta = -0.2
			}
			newPrice = int(math.Max(1, math.Round(float64(v.CurrentPrice)*(1+delta))))
		}

		pool.Exec(ctx,
			`INSERT INTO market_listings (item_id, price, expired, expires_at) VALUES ($1, $2, false, $3)`,
			v.ID, newPrice, expiresAt,
		)
		pool.Exec(ctx, `INSERT INTO market_price_history (item_id, price) VALUES ($1, $2)`, v.ID, newPrice)
		delta := float64(newPrice-v.CurrentPrice) / math.Max(1, float64(v.CurrentPrice))
		res.Listings = append(res.Listings, struct {
			Name  string  `json:"name"`
			Price int     `json:"price"`
			Delta float64 `json:"delta"`
		}{v.Name, newPrice, delta})
	}

	res.Count = len(res.Listings)
	if aiErr != nil {
		res.AIFallback = true
		res.AIError = aiErr.Error()
		_ = aiModel
	}
	return res, nil
}

// updateAnchorPrices: after daily refresh, update anchor_price toward 7-day avg
func updateAnchorPrices(ctx context.Context) {
	rows, _ := pool.Query(ctx, `SELECT id FROM market_items WHERE enabled = true`)
	if rows == nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	for _, id := range ids {
		var avg float64
		err := pool.QueryRow(ctx, `
			SELECT AVG(price) FROM market_price_history
			WHERE item_id = $1 AND refreshed_at >= now() - interval '7 days'
		`, id).Scan(&avg)
		if err != nil || avg == 0 {
			continue
		}

		var current int
		pool.QueryRow(ctx, `SELECT anchor_price FROM market_items WHERE id = $1`, id).Scan(&current)

		// Nudge anchor 10% toward 7-day avg
		newAnchor := int(math.Round(float64(current) + (avg-float64(current))*0.1))
		if newAnchor < 1 {
			newAnchor = 1
		}
		pool.Exec(ctx, `UPDATE market_items SET anchor_price = $1 WHERE id = $2`, newAnchor, id)
	}
}

func pickRandomItems[T any](items []T, n int) []T {
	if len(items) <= n {
		return items
	}
	cp := make([]T, len(items))
	copy(cp, items)
	rand.Shuffle(len(cp), func(i, j int) { cp[i], cp[j] = cp[j], cp[i] })
	return cp[:n]
}

// ── AI Pricing (daily) ───────────────────────────────────────────────────────

type aiItem struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	AvgPrice7d int    `json:"avg_price_7d"`
	History    []int  `json:"recent_prices"`
	RecentBuys int    `json:"recent_buys"`
}

func getAIConfig(ctx context.Context) (baseURL, model, apiKey string) {
	pool.QueryRow(ctx, `SELECT ai_base_url, ai_model, ai_api_key FROM settings LIMIT 1`).
		Scan(&baseURL, &model, &apiKey)
	if baseURL == "" {
		baseURL = os.Getenv("WONKA_AI_BASE_URL")
	}
	if model == "" {
		model = os.Getenv("WONKA_AI_MODEL")
	}
	if apiKey == "" {
		apiKey = os.Getenv("WONKA_AI_API_KEY")
	}
	return
}

func callAI(ctx context.Context, prompt string) (string, string, error) {
	aiBaseURL, aiModel, aiKey := getAIConfig(ctx)
	if aiKey == "" || aiBaseURL == "" || aiModel == "" {
		return "", "", fmt.Errorf("AI not configured")
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": aiModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.9,
		"max_tokens":  1000,
	})

	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("POST", aiBaseURL+"/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aiKey)

	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("AI request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("AI returned %d: %s", resp.StatusCode, string(respBody))
	}

	var aiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &aiResp); err != nil || len(aiResp.Choices) == 0 {
		return "", "", fmt.Errorf("failed to parse AI response")
	}
	return aiResp.Choices[0].Message.Content, aiModel, nil
}

func generateAIPricing(ctx context.Context, items []dbItem) (map[string]float64, string, string, error) {
	aiBaseURL, aiModel, aiKey := getAIConfig(ctx)
	if aiKey == "" || aiBaseURL == "" || aiModel == "" {
		return nil, "", "", fmt.Errorf("AI not configured")
	}

	evtRows, _ := pool.Query(ctx,
		`SELECT description FROM market_events ORDER BY created_at DESC LIMIT 5`,
	)
	var recentEvents []string
	if evtRows != nil {
		for evtRows.Next() {
			var d string
			evtRows.Scan(&d)
			recentEvents = append(recentEvents, d)
		}
		evtRows.Close()
	}

	var itemList []aiItem
	for _, item := range items {
		info := aiItem{
			Name: item.Name,
			Type: item.ItemType,
		}
		// 7-day average price (what AI sees instead of anchor)
		var avg7d float64
		pool.QueryRow(ctx,
			`SELECT COALESCE(AVG(price), 0) FROM market_price_history WHERE item_id = $1 AND refreshed_at >= now() - interval '7 days'`,
			item.ID,
		).Scan(&avg7d)
		if avg7d > 0 {
			info.AvgPrice7d = int(math.Round(avg7d))
		} else {
			info.AvgPrice7d = item.AnchorPrice // fallback for new items with no history
		}
		phRows, _ := pool.Query(ctx,
			`SELECT price FROM market_price_history WHERE item_id = $1 ORDER BY refreshed_at DESC LIMIT 10`,
			item.ID,
		)
		if phRows != nil {
			for phRows.Next() {
				var p int
				phRows.Scan(&p)
				info.History = append(info.History, p)
			}
			phRows.Close()
		}
		var buyCount int
		pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM inventories WHERE item_id = $1 AND acquired_at >= now() - interval '3 days'`,
			item.ID,
		).Scan(&buyCount)
		info.RecentBuys = buyCount
		itemList = append(itemList, info)
	}

	latestEvent := ""
	historyText := ""
	for i, ev := range recentEvents {
		if i == 0 {
			latestEvent = ev
		}
		historyText += fmt.Sprintf("%d. %s\n", i+1, ev)
	}
	if historyText == "" {
		historyText = "（尚無歷史事件）"
	}

	continuationHint := ""
	if latestEvent != "" {
		continuationHint = fmt.Sprintf(`
【重要】最新進行中的事件：「%s」
請優先考慮延續這個事件的發展（例如事件的後續影響、餘震、相關反應），而不是創造全新的事件。
只有在劇情自然結束（例如事件解決了、勝負分出了）時，才可以引入全新事件。
延續事件時，event 欄位應描述該事件的「後續發展」，例如「第二天」「消息傳出」「調查持續」等。`, latestEvent)
	}

	itemsJSON, _ := json.Marshal(itemList)
	prompt := fmt.Sprintf(`你是一個糖果市場的分析師，負責管理一個有趣的糖果股市世界。請根據市場情況決定本次刷新的事件與物品漲跌幅。

物品清單（含 7 天均價、近期成交價、近 3 天購買次數）：
%s

近期事件歷史（最新在前）：
%s
%s

請用 JSON 回覆，格式如下（不要包含 markdown code block）：
{"event": "事件描述（一句話，有故事感）", "effects": {"物品名稱": 漲跌幅數值}}

定價規則：
1. 漲跌幅數值是相對「7 天均價」的比例，例如 0.2 = 漲 20%%，-0.3 = 跌 30%%
2. 每件物品都必須有 effect 值
3. 大部分時候（70%%）：物品在 ±10%% 內微幅波動，市場平穩
4. 偶爾（25%%）：受事件影響，個別物品 ±15~30%% 中幅波動
5. 罕見（5%%）：重大事件導致某物品大漲 +40~50%% 或大跌 -40~60%%
6. 參考 recent_prices 的趨勢：連漲可繼續緩漲或回調，連跌可繼續探底或反彈，不要無規律亂跳
7. 允許長期趨勢：某些物品可以連續多次緩慢上漲或下跌（牛市/熊市），不需要每次都反轉
8. 事件與物品漲跌要有邏輯關聯，跌的物品要說得通為什麼跌
9. 參考 recent_buys：購買多的物品需求大有上漲壓力，沒人買的物品可能繼續下跌
10. 供需邏輯：物品在劇情中被大量消耗/使用 = 供給減少、稀缺性增加，應有上漲壓力；物品滯銷/囤積過剩 = 供過於求，應有下跌壓力。注意區分「需求旺盛導致消耗」（利多）vs「物品本身不受歡迎」（利空）`, string(itemsJSON), historyText, continuationHint)

	content, model, err := callAI(ctx, prompt)
	if err != nil {
		return nil, "", "", err
	}

	var parsed struct {
		Event   string             `json:"event"`
		Effects map[string]float64 `json:"effects"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, "", "", fmt.Errorf("failed to parse AI JSON: %w", err)
	}
	return parsed.Effects, parsed.Event, model, nil
}

// ── AI Pricing (hourly, volume-driven) ───────────────────────────────────────

type hourlyAIItem struct {
	Name         string `json:"name"`
	CurrentPrice int    `json:"current_price"`
	BuyCount1h   int    `json:"buy_count_1h"`
	SellCount1h  int    `json:"sell_count_1h"`
	NetFlow      int    `json:"net_flow"`
}

func generateHourlyPricing(ctx context.Context, items []volumeItem, latestEvent string) (map[string]float64, string, string, error) {

	var aiItems []hourlyAIItem
	for _, v := range items {
		aiItems = append(aiItems, hourlyAIItem{
			Name:         v.Name,
			CurrentPrice: v.CurrentPrice,
			BuyCount1h:   v.BuyCount,
			SellCount1h:  v.SellCount,
			NetFlow:      v.BuyCount - v.SellCount,
		})
	}

	itemsJSON, _ := json.Marshal(aiItems)
	prompt := fmt.Sprintf(`你是一個糖果市場的即時報價系統。根據過去 1 小時的交易量，微調各物品的市場價格。

當前事件背景：%s

各物品過去 1 小時交易數據：
%s

請用 JSON 回覆，格式如下（不要包含 markdown code block）：
{"effects": {"物品名稱": 漲跌幅數值}}

定價規則：
1. 漲跌幅數值是相對「current_price」的比例，幅度限制在 ±20%% 以內
2. net_flow 為正（買多於賣）→ 上漲壓力；net_flow 為負 → 下跌壓力
3. net_flow 為 0 → 微幅（±2%%）向隱形參考價靠近
4. 每件物品都必須有 effect 值
5. 不要生成新事件，只根據交易量調價`, latestEvent, string(itemsJSON))

	content, model, err := callAI(ctx, prompt)
	if err != nil {
		return nil, "", "", err
	}

	var parsed struct {
		Effects map[string]float64 `json:"effects"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, "", "", fmt.Errorf("failed to parse AI JSON: %w", err)
	}
	return parsed.Effects, "", model, nil
}
