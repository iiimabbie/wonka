package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
)

// --- POST /v1/market/refresh (admin-only, called by cron) ---
func handleMarketRefresh(e *core.RequestEvent, app *pocketbase.PocketBase) error {
	// Auth: require admin API key header
	adminKey := os.Getenv("WONKA_ADMIN_KEY")
	if adminKey == "" {
		return e.JSON(http.StatusInternalServerError, map[string]string{
			"error": "WONKA_ADMIN_KEY not configured",
		})
	}

	providedKey := e.Request.Header.Get("X-Admin-Key")
	if providedKey != adminKey {
		return e.JSON(http.StatusUnauthorized, map[string]string{
			"error": "invalid admin key",
		})
	}

	// 1. Get all enabled items
	items, err := app.FindRecordsByFilter("market_items", "enabled = true", "", 0, 0)
	if err != nil || len(items) == 0 {
		return e.JSON(http.StatusBadRequest, map[string]string{
			"error": "no enabled items found",
		})
	}

	// 2. Pick up to 4 random items
	picked := pickRandomItems(items, 4)

	// 3. Try AI pricing
	effects, eventDesc, model, aiErr := generateAIPricing(app, picked)

	// 4. Mark old listings as expired
	_, _ = app.DB().NewQuery(`
		UPDATE market_listings SET expired = true WHERE expired = false
	`).Execute()

	// 5. Create event record
	var eventId string
	if aiErr == nil && eventDesc != "" {
		eventsCol, err := app.FindCollectionByNameOrId("market_events")
		if err == nil {
			eventRecord := core.NewRecord(eventsCol)
			eventRecord.Set("description", eventDesc)
			effectJSON, _ := json.Marshal(effects)
			eventRecord.Set("effect", string(effectJSON))
			eventRecord.Set("model", model)
			if err := app.Save(eventRecord); err == nil {
				eventId = eventRecord.Id
			}
		}
	}

	// 6. Create new listings + price history
	now := time.Now().UTC()
	expiresAt := now.Add(12 * time.Hour).Format("2006-01-02 15:04:05.000Z")

	listingsCol, _ := app.FindCollectionByNameOrId("market_listings")
	phCol, _ := app.FindCollectionByNameOrId("market_price_history")

	type ListingResult struct {
		Name  string  `json:"name"`
		Price float64 `json:"price"`
		Delta float64 `json:"delta"`
	}
	var results []ListingResult

	for _, item := range picked {
		basePrice := item.GetFloat("base_price")
		itemName := item.GetString("name")

		// Calculate price
		var delta float64
		if effects != nil {
			if d, ok := effects[itemName]; ok {
				delta = d
			}
		}

		// If no AI effect, use random ±30%
		if delta == 0 && aiErr != nil {
			delta = (rand.Float64()*0.6 - 0.3) // -0.3 to +0.3
		}

		// Clamp: max +50% up, no floor on down (but price min 1)
		if delta > 0.5 {
			delta = 0.5
		}

		price := math.Max(1, math.Round(basePrice*(1+delta)))

		// Create listing
		if listingsCol != nil {
			listing := core.NewRecord(listingsCol)
			listing.Set("item_id", item.Id)
			listing.Set("price", price)
			listing.Set("expired", false)
			listing.Set("expires_at", expiresAt)
			if eventId != "" {
				listing.Set("event_id", eventId)
			}
			_ = app.Save(listing)
		}

		// Price history
		if phCol != nil {
			ph := core.NewRecord(phCol)
			ph.Set("item_id", item.Id)
			ph.Set("price", price)
			_ = app.Save(ph)
		}

		results = append(results, ListingResult{
			Name:  itemName,
			Price: price,
			Delta: delta,
		})
	}

	resp := map[string]any{
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

	return e.JSON(http.StatusOK, resp)
}

func pickRandomItems(items []*core.Record, n int) []*core.Record {
	if len(items) <= n {
		return items
	}
	rand.Shuffle(len(items), func(i, j int) {
		items[i], items[j] = items[j], items[i]
	})
	return items[:n]
}

// --- AI Pricing ---
func generateAIPricing(app *pocketbase.PocketBase, items []*core.Record) (map[string]float64, string, string, error) {
	aiKey := os.Getenv("WONKA_AI_API_KEY")
	aiBaseURL := os.Getenv("WONKA_AI_BASE_URL")
	aiModel := os.Getenv("WONKA_AI_MODEL")

	if aiKey == "" || aiBaseURL == "" || aiModel == "" {
		return nil, "", "", fmt.Errorf("AI not configured")
	}

	// Get recent events for context
	type EventRow struct {
		Description string `db:"description"`
	}
	var recentEvents []EventRow
	_ = app.DB().NewQuery(`
		SELECT description FROM market_events
		ORDER BY happened_at DESC LIMIT 5
	`).All(&recentEvents)

	// Build item list with recent price history
	type ItemInfo struct {
		Name      string    `json:"name"`
		Type      string    `json:"type"`
		BasePrice float64   `json:"base_price"`
		History   []float64 `json:"recent_prices"`
	}
	var itemList []ItemInfo
	for _, item := range items {
		info := ItemInfo{
			Name:      item.GetString("name"),
			Type:      item.GetString("type"),
			BasePrice: item.GetFloat("base_price"),
		}
		// Fetch last 10 prices
		type PH struct {
			Price float64 `db:"price"`
		}
		var ph []PH
		_ = app.DB().NewQuery(`
			SELECT price FROM market_price_history
			WHERE item_id = {:id} ORDER BY created DESC LIMIT 10
		`).Bind(map[string]any{"id": item.Id}).All(&ph)
		for _, p := range ph {
			info.History = append(info.History, p.Price)
		}
		itemList = append(itemList, info)
	}

	// Build history text
	historyText := ""
	for i, ev := range recentEvents {
		historyText += fmt.Sprintf("%d. %s\n", i+1, ev.Description)
	}
	if historyText == "" {
		historyText = "（尚無歷史事件）"
	}

	itemsJSON, _ := json.Marshal(itemList)

	prompt := fmt.Sprintf(`你是一個糖果市場的分析師。請生成一個今日市場事件，並根據事件內容決定以下物品的價格漲跌幅。

物品清單（含底價與近期成交價）：
%s

近期事件歷史：
%s

請用 JSON 回覆，格式如下（不要包含 markdown code block）：
{"event": "事件描述（一句話，有故事感）", "effects": {"物品名稱": 漲跌幅數值}}

定價規則：
1. 漲跌幅數值是相對底價的比例，例如 0.2 = 漲 20%%，-0.3 = 跌 30%%
2. 正常日：大部分物品在 ±30%% 內小幅波動
3. 偶爾（約 1/5 的機率）可以有一個物品大漲到 +50%% 或大跌到 -60%%
4. 價格不能低於 1（最低 1 糖果幣）
5. 參考 recent_prices 維持合理走勢，不要突然從底價 5 跳到 500
6. 事件內容要有故事感，跟物品漲跌有邏輯關聯`, string(itemsJSON), historyText)

	// Call AI API
	reqBody, _ := json.Marshal(map[string]any{
		"model": aiModel,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.9,
		"max_tokens":  500,
	})

	client := &http.Client{Timeout: 10 * time.Second}
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

	// Parse response
	var aiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &aiResp); err != nil {
		return nil, "", "", fmt.Errorf("failed to parse AI response: %w", err)
	}

	if len(aiResp.Choices) == 0 {
		return nil, "", "", fmt.Errorf("AI returned no choices")
	}

	content := aiResp.Choices[0].Message.Content

	// Parse the JSON from AI content
	var result struct {
		Event   string             `json:"event"`
		Effects map[string]float64 `json:"effects"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, "", "", fmt.Errorf("failed to parse AI JSON: %w (content: %s)", err, content)
	}

	return result.Effects, result.Event, aiModel, nil
}
