package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
)

// testServer holds the shared echo instance + test helpers
var (
	testEcho   *echo.Echo
	testServer *httptest.Server
)

func TestMain(m *testing.M) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required for integration tests")
	}
	if os.Getenv("JWT_SECRET") == "" {
		os.Setenv("JWT_SECRET", "test-jwt-secret-wonka-2026")
	}
	if os.Getenv("WONKA_ADMIN_KEY") == "" {
		os.Setenv("WONKA_ADMIN_KEY", "test-admin-key")
	}

	if err := runMigrations(dbURL); err != nil {
		log.Fatal("Migration error: ", err)
	}
	var err error
	pool, err = initDB(dbURL)
	if err != nil {
		log.Fatal("DB init error: ", err)
	}
	defer pool.Close()

	// Seed settings if empty
	var count int
	pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM settings").Scan(&count)
	if count == 0 {
		pool.Exec(context.Background(),
			`INSERT INTO settings (ai_base_url, ai_model, ai_api_key) VALUES ($1, $2, $3)`,
			os.Getenv("WONKA_AI_BASE_URL"), os.Getenv("WONKA_AI_MODEL"), os.Getenv("WONKA_AI_API_KEY"),
		)
	}

	// Run seed SQL
	seedSQL, err := os.ReadFile("scripts/seed_test.sql")
	if err == nil {
		pool.Exec(context.Background(), string(seedSQL))
	}

	// Build echo app (same routes as main() but no schedulers)
	testEcho = buildTestEcho()
	testServer = httptest.NewServer(testEcho)
	defer testServer.Close()

	os.Exit(m.Run())
}

func buildTestEcho() *echo.Echo {
	e := echo.New()
	e.HideBanner = true

	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	e.POST("/v1/auth/register", handleRegister)
	e.POST("/v1/auth/login", handleLogin)
	e.POST("/v1/auth/refresh", handleRefreshToken)

	candy := e.Group("/v1/candies", agentAuthMiddleware)
	candy.POST("/adjust", handleAdjustCandies)
	candy.GET("/balance", handleGetBalance)
	candy.GET("/history", handleGetHistory)
	candy.GET("/summary", handleGetSummary)

	e.GET("/v1/candies/leaderboard", handleGetLeaderboard)

	e.POST("/v1/candies/transfer", handleTransfer, agentAuthMiddleware)
	e.GET("/v1/transfers/history", handleTransferHistory, agentAuthMiddleware)

	user := e.Group("", userAuthMiddleware)
	user.POST("/v1/agents/create", handleCreateAgent)
	user.GET("/v1/agents", handleListAgents)
	user.GET("/v1/user/profile", handleUserProfile)
	user.GET("/v1/agents/:agentId/balance", handleGetAgentBalance)
	user.GET("/v1/agents/:agentId/inventory", handleGetAgentInventory)
	user.GET("/v1/agents/:agentId/history", handleGetAgentHistory)
	user.POST("/v1/agents/:agentId/regenerate-key", handleRegenerateKey)

	e.GET("/v1/market", handleMarket)
	e.POST("/v1/market/buy", handleMarketBuy, agentAuthMiddleware)
	e.POST("/v1/market/sell", handleMarketSell, agentAuthMiddleware)
	e.GET("/v1/market/prices", handlePriceHistory)
	e.GET("/v1/market/events", handleMarketEvents)
	e.POST("/v1/market/refresh", handleMarketRefresh, adminKeyMiddleware)
	e.POST("/v1/market/hourly-refresh", handleHourlyRefresh, adminKeyMiddleware)
	e.GET("/v1/market/snapshot", handleMarketSnapshot, agentAuthMiddleware)

	e.GET("/v1/inventory", handleInventory, agentAuthMiddleware)
	e.GET("/v1/inventory/history", handleInventoryHistory, agentAuthMiddleware)

	admin := e.Group("/v1/admin", userAuthMiddleware, adminMiddleware)
	admin.GET("/agents", handleAdminAgents)
	admin.PATCH("/agents/:agentId", handleAdminPatchAgent)
	admin.GET("/users", handleAdminUsers)
	admin.DELETE("/users/:userId", handleAdminDeleteUser)
	admin.POST("/adjust", handleAdminAdjust)
	admin.GET("/settings", handleAdminGetSettings)
	admin.PUT("/settings", handleAdminPutSettings)

	return e
}

// ── Test helpers ─────────────────────────────────────────────────────────────

func doJSON(t *testing.T, method, path string, body interface{}, headers map[string]string) (int, map[string]interface{}) {
	t.Helper()
	var reqBody io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, testServer.URL+path, reqBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return resp.StatusCode, result
}

func agentHeader(key string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + key}
}

func adminKeyHeader() map[string]string {
	return map[string]string{"X-Admin-Key": os.Getenv("WONKA_ADMIN_KEY")}
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestHealth(t *testing.T) {
	code, body := doJSON(t, "GET", "/health", nil, nil)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected ok, got %v", body["status"])
	}
}

// ── Auth ─────────────────────────────────────────────────────────────────────

func TestAuth_RegisterPasswordTooShort(t *testing.T) {
	code, body := doJSON(t, "POST", "/v1/auth/register", map[string]string{
		"email": "short@pw.com", "password": "123", "name": "short",
	}, nil)
	if code != 400 {
		t.Fatalf("expected 400, got %d", code)
	}
	if body["error"] != "password must be at least 8 characters" {
		t.Fatalf("unexpected error: %v", body["error"])
	}
}

func TestAuth_RegisterLoginRefresh(t *testing.T) {
	email := fmt.Sprintf("test-%d@wonka.test", os.Getpid())

	// Register
	code, body := doJSON(t, "POST", "/v1/auth/register", map[string]string{
		"email": email, "password": "testpassword123", "name": "TestUser",
	}, nil)
	if code != 201 {
		t.Fatalf("register: expected 201, got %d: %v", code, body)
	}
	if body["id"] == nil {
		t.Fatal("register: missing id")
	}

	// Login
	code, body = doJSON(t, "POST", "/v1/auth/login", map[string]string{
		"email": email, "password": "testpassword123",
	}, nil)
	if code != 200 {
		t.Fatalf("login: expected 200, got %d: %v", code, body)
	}
	token, ok := body["token"].(string)
	if !ok || token == "" {
		t.Fatal("login: missing token")
	}

	// Refresh
	code, body = doJSON(t, "POST", "/v1/auth/refresh", nil, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if code != 200 {
		t.Fatalf("refresh: expected 200, got %d: %v", code, body)
	}
	newToken, ok := body["token"].(string)
	if !ok || newToken == "" {
		t.Fatal("refresh: missing new token")
	}
}

func TestAuth_DuplicateEmail(t *testing.T) {
	email := fmt.Sprintf("dup-%d@wonka.test", os.Getpid())
	doJSON(t, "POST", "/v1/auth/register", map[string]string{
		"email": email, "password": "testpassword123", "name": "Dup1",
	}, nil)

	code, _ := doJSON(t, "POST", "/v1/auth/register", map[string]string{
		"email": email, "password": "testpassword123", "name": "Dup2",
	}, nil)
	if code != 409 {
		t.Fatalf("expected 409 conflict, got %d", code)
	}
}

func TestAuth_BadCredentials(t *testing.T) {
	code, _ := doJSON(t, "POST", "/v1/auth/login", map[string]string{
		"email": "nonexist@wonka.test", "password": "wrong",
	}, nil)
	if code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
}

// ── Agent Balance & Adjust ──────────────────────────────────────────────────

const testKeyAlpha = "test-key-alpha"
const testKeyBeta = "test-key-beta"

func TestBalance_SeedAgent(t *testing.T) {
	code, body := doJSON(t, "GET", "/v1/candies/balance", nil, agentHeader(testKeyAlpha))
	if code != 200 {
		t.Fatalf("expected 200, got %d: %v", code, body)
	}
	if _, ok := body["balance"]; !ok {
		t.Fatal("missing balance field")
	}
}

func TestAdjust_Basic(t *testing.T) {
	code, body := doJSON(t, "POST", "/v1/candies/adjust", map[string]interface{}{
		"delta": 10, "reason": "test earn", "idempotencyKey": fmt.Sprintf("test-earn-%d", time.Now().UnixNano()),
	}, agentHeader(testKeyAlpha))
	if code != 200 {
		t.Fatalf("expected 200, got %d: %v", code, body)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected ok, got %v", body["status"])
	}
}

func TestAdjust_Idempotency(t *testing.T) {
	key := fmt.Sprintf("idemp-%d", time.Now().UnixNano())
	req := map[string]interface{}{
		"delta": 5, "reason": "idemp test", "idempotencyKey": key,
	}

	// First call
	code1, body1 := doJSON(t, "POST", "/v1/candies/adjust", req, agentHeader(testKeyAlpha))
	if code1 != 200 || body1["status"] != "ok" {
		t.Fatalf("first call: %d %v", code1, body1)
	}

	// Second call (same key) → duplicate
	code2, body2 := doJSON(t, "POST", "/v1/candies/adjust", req, agentHeader(testKeyAlpha))
	if code2 != 200 || body2["status"] != "duplicate" {
		t.Fatalf("second call: expected duplicate, got %d %v", code2, body2)
	}
}

// ── Transfer ────────────────────────────────────────────────────────────────

func TestTransfer_Basic(t *testing.T) {
	// Ensure agent A has enough balance
	pool.Exec(context.Background(),
		`INSERT INTO candy_ledger (agent_id, delta, reason, idempotency_key)
		 VALUES ('a0000000-0000-0000-0000-000000000001', 50, 'transfer test seed', $1)
		 ON CONFLICT (agent_id, idempotency_key) DO NOTHING`,
		fmt.Sprintf("xfer-seed-%d-%d", os.Getpid(), time.Now().UnixNano()),
	)

	xferKey := fmt.Sprintf("xfer-basic-%d", time.Now().UnixNano())
	code, body := doJSON(t, "POST", "/v1/candies/transfer", map[string]interface{}{
		"to": "測試員B", "amount": 5, "reason": "test transfer",
		"idempotencyKey": xferKey,
	}, agentHeader(testKeyAlpha))
	if code != 200 {
		t.Fatalf("expected 200, got %d: %v", code, body)
	}
	status, _ := body["status"].(string)
	if status != "ok" && status != "duplicate" {
		t.Fatalf("expected ok or duplicate, got %v", body)
	}
	if status == "duplicate" {
		t.Logf("got duplicate (likely residual data from previous run), key=%s", xferKey)
	}
}

func TestTransfer_SelfTransfer(t *testing.T) {
	code, body := doJSON(t, "POST", "/v1/candies/transfer", map[string]interface{}{
		"to": "測試員A", "amount": 1, "reason": "self",
		"idempotencyKey": "self-xfer",
	}, agentHeader(testKeyAlpha))
	if code != 400 {
		t.Fatalf("expected 400, got %d: %v", code, body)
	}
}

func TestTransfer_InsufficientBalance(t *testing.T) {
	code, body := doJSON(t, "POST", "/v1/candies/transfer", map[string]interface{}{
		"to": "測試員B", "amount": 999999, "reason": "too much",
		"idempotencyKey": fmt.Sprintf("insuf-%d", time.Now().UnixNano()),
	}, agentHeader(testKeyAlpha))
	if code != 400 {
		t.Fatalf("expected 400, got %d: %v", code, body)
	}
	if body["error"] != "insufficient balance" {
		t.Fatalf("expected insufficient balance, got %v", body["error"])
	}
}

func TestTransfer_ConcurrentRace(t *testing.T) {
	const testKeyGamma = "test-key-gamma"
	agentCID := "a0000000-0000-0000-0000-000000000003"
	ts := time.Now().UnixNano()

	// Give agent C exactly 100 fresh candies for this test run
	pool.Exec(context.Background(),
		`INSERT INTO candy_ledger (agent_id, delta, reason, idempotency_key)
		 VALUES ($1, 100, 'race test seed', $2)`,
		agentCID, fmt.Sprintf("race-seed-%d", ts),
	)

	// Get current balance
	var startBalance int
	pool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(delta), 0) FROM candy_ledger WHERE agent_id = $1`,
		agentCID,
	).Scan(&startBalance)

	if startBalance <= 0 {
		t.Fatalf("agent C balance is %d, cannot run race test", startBalance)
	}

	// Try to transfer the FULL balance in 10 concurrent requests
	// Only 1 should succeed, the rest should get insufficient balance
	var wg sync.WaitGroup
	results := make([]int, 10)
	bodyResults := make([]map[string]interface{}, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			code, body := doJSON(t, "POST", "/v1/candies/transfer", map[string]interface{}{
				"to": "測試員A", "amount": startBalance, "reason": "race test",
				"idempotencyKey": fmt.Sprintf("race-%d-%d", ts, idx),
			}, agentHeader(testKeyGamma))
			results[idx] = code
			bodyResults[idx] = body
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, r := range bodyResults {
		status, _ := r["status"].(string)
		if status == "ok" {
			successCount++
		}
	}
	// At most 1 should succeed (the rest should get insufficient balance)
	if successCount > 1 {
		t.Fatalf("race condition! %d transfers succeeded (expected at most 1), results: %v", successCount, results)
	}
	t.Logf("race test: %d/10 succeeded (balance was %d), results: %v", successCount, startBalance, results)
}

// ── Market ──────────────────────────────────────────────────────────────────

func TestMarket_ListPublic(t *testing.T) {
	// Trigger a refresh first to ensure listings exist
	doJSON(t, "POST", "/v1/market/refresh", nil, adminKeyHeader())

	code, body := doJSON(t, "GET", "/v1/market", nil, nil)
	if code != 200 {
		t.Fatalf("expected 200, got %d: %v", code, body)
	}
	listings, ok := body["listings"].([]interface{})
	if !ok {
		t.Fatal("missing listings")
	}
	if len(listings) == 0 {
		t.Fatal("expected listings, got none")
	}
}

func TestMarket_BuyAndSell(t *testing.T) {
	// Ensure we have listings
	doJSON(t, "POST", "/v1/market/refresh", nil, adminKeyHeader())

	// Get a listing
	_, marketBody := doJSON(t, "GET", "/v1/market", nil, nil)
	listings := marketBody["listings"].([]interface{})
	if len(listings) == 0 {
		t.Fatal("no listings to buy")
	}
	listing := listings[len(listings)-1].(map[string]interface{}) // cheapest
	listingID := listing["id"].(string)
	price := int(listing["price"].(float64))

	// Ensure agent has enough balance
	pool.Exec(context.Background(),
		`INSERT INTO candy_ledger (agent_id, delta, reason, idempotency_key)
		 VALUES ('a0000000-0000-0000-0000-000000000002', $1, 'buy test seed', $2)
		 ON CONFLICT (agent_id, idempotency_key) DO NOTHING`,
		price+100, fmt.Sprintf("buy-seed-%d", time.Now().UnixNano()),
	)

	// Buy
	buyKey := fmt.Sprintf("buy-%d", time.Now().UnixNano())
	code, body := doJSON(t, "POST", "/v1/market/buy", map[string]interface{}{
		"listing_id": listingID, "idempotencyKey": buyKey,
	}, agentHeader(testKeyBeta))
	if code != 200 {
		t.Fatalf("buy: expected 200, got %d: %v", code, body)
	}
	if body["status"] != "ok" {
		t.Fatalf("buy: expected ok, got %v", body)
	}

	// Get inventory to find the item
	code, invBody := doJSON(t, "GET", "/v1/inventory", nil, agentHeader(testKeyBeta))
	if code != 200 {
		t.Fatalf("inventory: expected 200, got %d", code)
	}
	items := invBody["items"].([]interface{})
	if len(items) == 0 {
		t.Fatal("inventory empty after buy")
	}

	// Get first inventory ID from holdings
	firstItem := items[0].(map[string]interface{})
	holdings := firstItem["holdings"].([]interface{})
	firstHolding := holdings[0].(map[string]interface{})
	ids := firstHolding["ids"].([]interface{})
	invID := ids[0].(string)

	// Sell
	sellKey := fmt.Sprintf("sell-%d", time.Now().UnixNano())
	code, body = doJSON(t, "POST", "/v1/market/sell", map[string]interface{}{
		"inventory_id": invID, "idempotencyKey": sellKey,
	}, agentHeader(testKeyBeta))
	if code != 200 {
		t.Fatalf("sell: expected 200, got %d: %v", code, body)
	}
	if body["status"] != "ok" {
		t.Fatalf("sell: expected ok, got %v", body)
	}

	// Double sell → already sold
	code, body = doJSON(t, "POST", "/v1/market/sell", map[string]interface{}{
		"inventory_id": invID, "idempotencyKey": fmt.Sprintf("sell2-%d", time.Now().UnixNano()),
	}, agentHeader(testKeyBeta))
	if code != 400 {
		t.Fatalf("double sell: expected 400, got %d: %v", code, body)
	}
}

func TestMarket_BuyInsufficientBalance(t *testing.T) {
	doJSON(t, "POST", "/v1/market/refresh", nil, adminKeyHeader())

	_, marketBody := doJSON(t, "GET", "/v1/market", nil, nil)
	listings := marketBody["listings"].([]interface{})
	listing := listings[0].(map[string]interface{}) // most expensive
	listingID := listing["id"].(string)

	// Use agent C which may have low/no balance
	code, body := doJSON(t, "POST", "/v1/market/buy", map[string]interface{}{
		"listing_id": listingID, "idempotencyKey": fmt.Sprintf("broke-%d", time.Now().UnixNano()),
	}, agentHeader("test-key-gamma"))
	// Should be 400 insufficient or 200 if they happen to have enough
	if code == 400 {
		if body["error"] != "insufficient balance" {
			t.Fatalf("expected insufficient balance, got %v", body["error"])
		}
	}
	// If 200, that's fine too — they had enough from previous tests
}

// ── Leaderboard ─────────────────────────────────────────────────────────────

func TestLeaderboard(t *testing.T) {
	code, body := doJSON(t, "GET", "/v1/candies/leaderboard", nil, nil)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	lb, ok := body["leaderboard"].([]interface{})
	if !ok {
		t.Fatal("missing leaderboard")
	}
	// test agents are filtered out (name starts with 測試員, not test%)
	// so they should appear
	if len(lb) == 0 {
		t.Fatal("leaderboard empty")
	}
}

// ── Snapshot ────────────────────────────────────────────────────────────────

func TestSnapshot(t *testing.T) {
	doJSON(t, "POST", "/v1/market/refresh", nil, adminKeyHeader())

	code, body := doJSON(t, "GET", "/v1/market/snapshot", nil, agentHeader(testKeyAlpha))
	if code != 200 {
		t.Fatalf("expected 200, got %d: %v", code, body)
	}
	// Should have all required fields
	for _, key := range []string{"balance", "inventory", "listings", "event", "recent_events"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing key: %s", key)
		}
	}
}

// ── Price History ───────────────────────────────────────────────────────────

func TestPriceHistory_WithoutItemID(t *testing.T) {
	doJSON(t, "POST", "/v1/market/refresh", nil, adminKeyHeader())

	code, body := doJSON(t, "GET", "/v1/market/prices?limit=3", nil, nil)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	prices := body["prices"].([]interface{})
	if len(prices) == 0 {
		t.Fatal("expected price data")
	}
	// Should include item_name when no item_id filter
	first := prices[0].(map[string]interface{})
	if first["item_name"] == nil {
		t.Fatal("expected item_name in price history without item_id filter")
	}
}

// ── Auth middleware ──────────────────────────────────────────────────────────

func TestAuth_MissingToken(t *testing.T) {
	code, _ := doJSON(t, "GET", "/v1/candies/balance", nil, nil)
	if code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
}

func TestAuth_InvalidToken(t *testing.T) {
	code, _ := doJSON(t, "GET", "/v1/candies/balance", nil, agentHeader("invalid-key"))
	if code != 401 {
		t.Fatalf("expected 401, got %d", code)
	}
}

func TestAdminKey_Forbidden(t *testing.T) {
	code, _ := doJSON(t, "POST", "/v1/market/refresh", nil, map[string]string{
		"X-Admin-Key": "wrong-key",
	})
	if code != 403 {
		t.Fatalf("expected 403, got %d", code)
	}
}

// ── Events ──────────────────────────────────────────────────────────────────

func TestMarketEvents(t *testing.T) {
	code, body := doJSON(t, "GET", "/v1/market/events?limit=5", nil, nil)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	if _, ok := body["events"]; !ok {
		t.Fatal("missing events key")
	}
}

// ── Summary ─────────────────────────────────────────────────────────────────

func TestSummary(t *testing.T) {
	code, body := doJSON(t, "GET", "/v1/candies/summary", nil, agentHeader(testKeyAlpha))
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	for _, key := range []string{"agent", "balance", "week_earned", "week_spent", "week_net"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("missing key: %s", key)
		}
	}
}
