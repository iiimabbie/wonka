package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

var pool *pgxpool.Pool

// agentAuthMiddleware resolves Bearer token as API key → *Agent
func agentAuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := c.Request().Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing api key"})
		}
		apiKey := strings.TrimPrefix(auth, "Bearer ")
		agent, err := resolveAgentFromDB(context.Background(), pool, apiKey)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid api key"})
		}
		c.Set("agent", agent)
		return next(c)
	}
}

// userAuthMiddleware resolves Bearer token as JWT → *User
func userAuthMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		auth := c.Request().Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing token"})
		}
		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		jwtSecret := os.Getenv("JWT_SECRET")
		user, err := resolveUserFromDB(context.Background(), pool, tokenStr, jwtSecret)
		if err != nil {
			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		}
		c.Set("user", user)
		return next(c)
	}
}

// adminMiddleware must run after userAuthMiddleware
func adminMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		user := c.Get("user").(*User)
		if user.Role != "admin" {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "admin only"})
		}
		return next(c)
	}
}

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if os.Getenv("JWT_SECRET") == "" {
		log.Fatal("JWT_SECRET is required")
	}

	// Run migrations
	if err := runMigrations(databaseURL); err != nil {
		log.Fatal("Migration error: ", err)
	}

	// Init DB pool
	var err error
	pool, err = initDB(databaseURL)
	if err != nil {
		log.Fatal("DB init error: ", err)
	}
	defer pool.Close()

	// Ensure settings row exists
	_, err = pool.Exec(context.Background(),
		`INSERT INTO settings (ai_base_url, ai_model, ai_api_key) SELECT '', '', '' WHERE NOT EXISTS (SELECT 1 FROM settings)`)
	if err != nil {
		log.Printf("Warning: failed to ensure settings row: %v", err)
	}

	e := echo.New()
	e.HideBanner = true

	// CORS
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowHeaders: []string{"*"},
	}))

	// Health
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// ── Auth (public) ────────────────────────────────────────────────────────
	e.POST("/v1/auth/register", handleRegister)
	e.POST("/v1/auth/login", handleLogin)

	// ── Candy (agent-key auth) ───────────────────────────────────────────────
	candy := e.Group("/v1/candies", agentAuthMiddleware)
	candy.POST("/adjust", handleAdjustCandies)
	candy.GET("/balance", handleGetBalance)
	candy.GET("/history", handleGetHistory)
	candy.GET("/summary", handleGetSummary)

	// Leaderboard is public
	e.GET("/v1/candies/leaderboard", handleGetLeaderboard)

	// ── Transfers (agent-key auth) ───────────────────────────────────────────
	e.POST("/v1/candies/transfer", handleTransfer, agentAuthMiddleware)
	e.GET("/v1/transfers/history", handleTransferHistory, agentAuthMiddleware)

	// ── Agent + User (JWT auth) ──────────────────────────────────────────────
	user := e.Group("", userAuthMiddleware)
	user.POST("/v1/agents/create", handleCreateAgent)
	user.GET("/v1/agents", handleListAgents)
	user.GET("/v1/user/profile", handleUserProfile)
	user.GET("/v1/agents/:agentId/balance", handleGetAgentBalance)
	user.GET("/v1/agents/:agentId/inventory", handleGetAgentInventory)
	user.GET("/v1/agents/:agentId/history", handleGetAgentHistory)
	user.POST("/v1/agents/:agentId/regenerate-key", handleRegenerateKey)

	// ── Market (public reads, agent-key for buy/sell) ────────────────────────
	e.GET("/v1/market", handleMarket)
	e.POST("/v1/market/buy", handleMarketBuy, agentAuthMiddleware)
	e.POST("/v1/market/sell", handleMarketSell, agentAuthMiddleware)
	e.GET("/v1/market/items", handleMarketItems)
	e.GET("/v1/market/prices", handlePriceHistory)
	e.GET("/v1/market/events", handleMarketEvents)
	e.POST("/v1/market/refresh", handleMarketRefresh)

	// ── Inventory (agent-key) ────────────────────────────────────────────────
	e.GET("/v1/inventory", handleInventory, agentAuthMiddleware)
	e.GET("/v1/inventory/history", handleInventoryHistory, agentAuthMiddleware)

	// ── Admin (JWT + admin role) ─────────────────────────────────────────────
	stub := func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented yet"})
	}
	admin := e.Group("/v1/admin", userAuthMiddleware, adminMiddleware)
	admin.GET("/agents", stub)
	admin.PATCH("/agents/:agentId", stub)
	admin.GET("/users", stub)
	admin.DELETE("/users/:userId", stub)
	admin.POST("/adjust", stub)
	admin.POST("/agents/:agentId/regenerate-key", handleRegenerateKey)
	admin.POST("/market/refresh", handleMarketRefresh)
	admin.GET("/settings", stub)
	admin.PUT("/settings", stub)

	log.Println("🍬 Wonka v3 starting on :8090")
	e.Logger.Fatal(e.Start(":8090"))
}
