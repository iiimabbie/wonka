package main

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

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

// adminKeyMiddleware checks X-Admin-Key header (for internal/cron calls)
func adminKeyMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		adminKey := os.Getenv("WONKA_ADMIN_KEY")
		if adminKey == "" {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "WONKA_ADMIN_KEY not configured"})
		}
		provided := c.Request().Header.Get("X-Admin-Key")
		// Audit Remediation: use constant time compare for secrets
		if subtle.ConstantTimeCompare([]byte(provided), []byte(adminKey)) != 1 {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "forbidden"})
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

	// 1. Run migrations
	if err := runMigrations(databaseURL); err != nil {
		log.Fatal("Migration error: ", err)
	}

	// 2. Init DB pool
	var err error
	pool, err = initDB(databaseURL)
	if err != nil {
		log.Fatal("DB init error: ", err)
	}
	defer pool.Close()

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())

	// Health check
	e.GET("/health", func(c echo.Context) error {
		return c.String(http.StatusOK, "OK")
	})

	// ── Public Routes (Auth) ─────────────────────────────────────────────────
	e.POST("/v1/register", handleRegister)
	e.POST("/v1/login", handleLogin)
	e.POST("/v1/refresh-token", handleRefreshToken)

	// ── Agent Routes (Agent Key) ─────────────────────────────────────────────
	agent := e.Group("/v1", agentAuthMiddleware)
	agent.GET("/balance", handleGetBalance)
	agent.POST("/candies/adjust", handleAdjustCandies)
	agent.GET("/candies/history", handleGetHistory)
	agent.GET("/leaderboard", handleGetLeaderboard)
	agent.POST("/transfer", handleTransfer)
	agent.GET("/transfers", handleTransferHistory)
	agent.GET("/summary", handleGetSummary)

	// Market & Inventory
	agent.GET("/market", handleMarket)
	agent.POST("/market/buy", handleMarketBuy)
	agent.POST("/market/sell", handleMarketSell)
	agent.GET("/inventory", handleInventory)
	agent.GET("/inventory/history", handleInventoryHistory)
	agent.GET("/market/prices", handlePriceHistory)
	agent.GET("/market/events", handleMarketEvents)
	agent.GET("/market/snapshot", handleMarketSnapshot) // v4 Snapshot API

	// ── User Routes (JWT) ────────────────────────────────────────────────────
	user := e.Group("/v1/u", userAuthMiddleware)
	user.GET("/profile", handleUserProfile)
	user.POST("/agents", handleCreateAgent)
	user.GET("/agents", handleListAgents)
	user.GET("/agents/:agentId/balance", handleGetAgentBalance)
	user.GET("/agents/:agentId/inventory", handleGetAgentInventory)
	user.GET("/agents/:agentId/history", handleGetAgentHistory)
	user.POST("/agents/:agentId/regenerate-key", handleRegenerateKey)

	// ── Internal Admin (X-Admin-Key) ─────────────────────────────────────────
	admin := e.Group("/v1/admin", adminKeyMiddleware)
	admin.POST("/market/refresh", handleMarketRefresh)
	admin.POST("/market/hourly-refresh", handleHourlyRefresh)

	// Start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("🦊 Wonka v4 starting on :%s", port)
	e.Logger.Fatal(e.Start(":" + port))
}
