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
	ID        string
	Name      string
	ItemType  string
	BasePrice int
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

// doMarketRefresh is also called from admin route
func doMarketRefresh(c echo.Context) error {
	ctx := context.Background()

	// 1. Get all enabled items
	rows, err := pool.Query(ctx,
		`SELECT id, name, type, base_price FROM market_items WHERE enabled = true`,
	)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "db error"})
	}
	var allItems []dbItem
	for rows.Next() {
		var i dbItem
		rows.Scan(&i.ID, &i.Name, &i.ItemType, &i.BasePrice)
		allItems = append(allItems, i)
	}
	rows.Close()

	if len(allItems) == 0 {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "no enabled items found"})
	}

	// 2. All enabled items
	picked := allItems

	// 3. Try AI pricing
	effects, eventDesc, model, aiErr := generateAIPricing(ctx, picked)

	// 4. Expire old listings
	pool.Exec(ctx, `UPDATE market_listings SET expired = true WHERE expired = false`)

	// 5. Create event record
	var eventID string
	if aiErr == nil && eventDesc != "" {
		effectJSON, _ := json.Marshal(effects)
		pool.QueryRow(ctx,
			`INSERT INTO market_events (description, effect, model) VALUES ($1, $2, $3) RETURNING id`,
			eventDesc, string(effectJSON), model,
		).Scan(&eventID)
	}

	// 6. Create new listings + price history
	expiresAt := time.Now().Add(12 * time.Hour)

	type result struct {
		Name  string  `json:"name"`
		Price int     `json:"price"`
		Delta float64 `json:"delta"`
	}
	var results []result

	for _, item := range picked {
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

		price := int(math.Max(1, math.Round(float64(item.BasePrice)*(1+delta))))

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

		pool.Exec(ctx,
			`INSERT INTO market_price_history (item_id, price) VALUES ($1, $2)`,
			item.ID, price,
		)

		results = append(results, result{Name: item.Name, Price: price, Delta: delta})
	}

	resp := map[string]interface{}{
		"status":   "ok",
		"listings": results,
		"count":    len(results),
	}
	if eventDesc != "" {
		resp["event"] = eventDesc
	}
	if aiErr != nil {
		resp["ai_fallback"] = true
		resp["ai_error"] = aiErr.Error()
	}

	return c.JSON(http.StatusOK, resp)
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

// ── AI Pricing ───────────────────────────────────────────────────────────────

type aiItem struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	BasePrice  int       `json:"base_price"`
	History    []int     `json:"recent_prices"`
	RecentBuys int       `json:"recent_buys"`
}

func generateAIPricing(ctx context.Context, items []dbItem) (map[string]float64, string, string, error) {
	// Get AI config from settings table
	var aiBaseURL, aiModel, aiKey string
	pool.QueryRow(ctx, `SELECT ai_base_url, ai_model, ai_api_key FROM settings LIMIT 1`).
		Scan(&aiBaseURL, &aiModel, &aiKey)

	if aiBaseURL == "" {
		aiBaseURL = os.Getenv("WONKA_AI_BASE_URL")
	}
	if aiModel == "" {
		aiModel = os.Getenv("WONKA_AI_MODEL")
	}
	if aiKey == "" {
		aiKey = os.Getenv("WONKA_AI_API_KEY")
	}

	if aiKey == "" || aiBaseURL == "" || aiModel == "" {
		return nil, "", "", fmt.Errorf("AI not configured")
	}

	// Recent events for context
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

	// Build item list with history + recent buys
	var itemList []aiItem
	for _, item := range items {
		info := aiItem{
			Name:      item.Name,
			Type:      item.ItemType,
			BasePrice: item.BasePrice,
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

	historyText := ""
	for i, ev := range recentEvents {
		historyText += fmt.Sprintf("%d. %s\n", i+1, ev)
	}
	if historyText == "" {
		historyText = "（尚無歷史事件）"
	}

	itemsJSON, _ := json.Marshal(itemList)
	prompt := fmt.Sprintf(`你是一個糖果市場的分析師。請生成一個今日市場事件，並根據事件內容決定以下所有物品的價格漲跌幅（每件物品都必須有 effect）。

物品清單（含底價、近期成交價、近 3 天購買次數）：
%s

近期事件歷史：
%s

請用 JSON 回覆，格式如下（不要包含 markdown code block）：
{"event": "事件描述（一句話，有故事感）", "effects": {"物品名稱": 漲跌幅數值}}

定價規則：
1. 漲跌幅數值是相對底價的比例，例如 0.2 = 漲 20%%，-0.3 = 跌 30%%
2. 大部分時候（70%%）：物品在 ±10%% 內微幅波動，市場平穩
3. 偶爾（25%%）：受事件影響，個別物品 ±15~30%% 中幅波動
4. 罕見（5%%）：重大事件導致某物品大漲 +40~50%% 或大跌 -40~60%%
5. 價格不能低於 1（最低 1 糖果幣）
6. 重要：參考 recent_prices 的趨勢延續走勢。如果一個物品連續上漲，可以繼續緩漲或開始回調；如果連續下跌，可以繼續探底或觸底反彈。不要每次都隨機跳動
7. 允許長期趨勢：某些物品可以連續多天緩慢上漲或下跌（模擬牛市/熊市），不需要每天都反轉
8. 事件內容要有故事感，跟物品漲跌有邏輯關聯，並延續近期事件的世界觀
9. 參考 recent_buys：購買次數高的物品代表需求大，價格應該有上漲壓力；沒人買的冷門物品可能下跌`, string(itemsJSON), historyText)

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": aiModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.9,
		"max_tokens":  500,
	})

	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("POST", aiBaseURL+"/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+aiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("AI request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, "", "", fmt.Errorf("AI returned %d: %s", resp.StatusCode, string(respBody))
	}

	var aiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &aiResp); err != nil || len(aiResp.Choices) == 0 {
		return nil, "", "", fmt.Errorf("failed to parse AI response")
	}

	var parsed struct {
		Event   string             `json:"event"`
		Effects map[string]float64 `json:"effects"`
	}
	if err := json.Unmarshal([]byte(aiResp.Choices[0].Message.Content), &parsed); err != nil {
		return nil, "", "", fmt.Errorf("failed to parse AI JSON: %w", err)
	}

	return parsed.Effects, parsed.Event, aiModel, nil
}
