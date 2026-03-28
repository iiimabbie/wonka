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

	// Ensure settings row exists (only if table is empty)
	var count int
	err = pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM settings").Scan(&count)
	if err != nil {
		log.Printf("Warning: failed to count settings: %v", err)
	}

	if count == 0 {
		log.Println("🌱 Seeding initial settings from environment variables...")
		_, err = pool.Exec(context.Background(),
			`INSERT INTO settings (ai_base_url, ai_model, ai_api_key) VALUES ($1, $2, $3)`,
			os.Getenv("WONKA_AI_BASE_URL"),
			os.Getenv("WONKA_AI_MODEL"),
			os.Getenv("WONKA_AI_API_KEY"),
		)
		if err != nil {
			log.Printf("Warning: failed to seed initial settings: %v", err)
		}
	}

	e := echo.New()
	e.HideBanner = true

	// CORS
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowHeaders: []string{"*"},
	}))

	// Global rate limit (20 req/s per IP)
	e.Use(middleware.RateLimiter(middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{Rate: 20, Burst: 40, ExpiresIn: 3 * time.Minute},
	)))

	// Health
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})

	// ── Auth (public, stricter rate limit) ───────────────────────────────────
	authLimiter := middleware.RateLimiter(middleware.NewRateLimiterMemoryStoreWithConfig(
		middleware.RateLimiterMemoryStoreConfig{Rate: 5, Burst: 10, ExpiresIn: 3 * time.Minute},
	))
	e.POST("/v1/auth/register", handleRegister, authLimiter)
	e.POST("/v1/auth/login", handleLogin, authLimiter)
	e.POST("/v1/auth/refresh", handleRefreshToken, authLimiter)

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
	e.GET("/v1/market/prices", handlePriceHistory)
	e.GET("/v1/market/events", handleMarketEvents)
	e.POST("/v1/market/refresh", handleMarketRefresh, adminKeyMiddleware)
	e.POST("/v1/market/hourly-refresh", handleHourlyRefresh, adminKeyMiddleware)
	e.GET("/v1/market/snapshot", handleMarketSnapshot, agentAuthMiddleware)

	// Deprecated routes → explicit 404
	gone := func(c echo.Context) error {
		return c.JSON(http.StatusNotFound, map[string]string{"error": "removed, use /v1/market/snapshot instead"})
	}
	e.GET("/v1/market/items", gone)
	e.GET("/v1/market/volume", gone)

	// ── Inventory (agent-key) ────────────────────────────────────────────────
	e.GET("/v1/inventory", handleInventory, agentAuthMiddleware)
	e.GET("/v1/inventory/history", handleInventoryHistory, agentAuthMiddleware)

	// ── Admin (JWT + admin role) ─────────────────────────────────────────────
	admin := e.Group("/v1/admin", userAuthMiddleware, adminMiddleware)
	admin.GET("/agents", handleAdminAgents)
	admin.PATCH("/agents/:agentId", handleAdminPatchAgent)
	admin.GET("/users", handleAdminUsers)
	admin.DELETE("/users/:userId", handleAdminDeleteUser)
	admin.POST("/adjust", handleAdminAdjust)
	admin.POST("/agents/:agentId/regenerate-key", handleRegenerateKey)
	admin.POST("/market/refresh", func(c echo.Context) error { return doMarketRefresh(c) })
	admin.GET("/settings", handleAdminGetSettings)
	admin.PUT("/settings", handleAdminPutSettings)

	// Internal market refresh scheduler: 08:00 and 20:00 Asia/Taipei (event + price)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("🚨 Daily scheduler panicked: %v", r)
			}
		}()
		loc, err := time.LoadLocation("Asia/Taipei")
		if err != nil {
			log.Printf("🚨 Failed to load Asia/Taipei timezone: %v — daily scheduler disabled", err)
			return
		}
		for {
			now := time.Now().In(loc)
			next8 := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, loc)
			next20 := time.Date(now.Year(), now.Month(), now.Day(), 20, 0, 0, 0, loc)
			if now.After(next8) {
				next8 = next8.Add(24 * time.Hour)
			}
			if now.After(next20) {
				next20 = next20.Add(24 * time.Hour)
			}
			nextRun := next8
			if next20.Before(next8) {
				nextRun = next20
			}
			wait := time.Until(nextRun)
			log.Printf("📅 Next daily market refresh scheduled in %v (at %s)", wait.Round(time.Second), nextRun.Format("2006-01-02 15:04:05 MST"))
			time.Sleep(wait)
			log.Println("🔄 Triggering daily market refresh...")
			if res, err := runMarketRefresh(); err != nil {
				log.Printf("⚠️ Daily market refresh error: %v", err)
			} else {
				log.Printf("✅ Daily market refresh complete: %d items, ai_fallback=%v", res.Count, res.AIFallback)
			}
		}
	}()

	// Hourly price refresh scheduler (volume-driven, no new event)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("🚨 Hourly scheduler panicked: %v", r)
			}
		}()
		loc, err := time.LoadLocation("Asia/Taipei")
		if err != nil {
			log.Printf("🚨 Failed to load Asia/Taipei timezone: %v — hourly scheduler disabled", err)
			return
		}
		for {
			now := time.Now().In(loc)
			// Skip if we're within 5 min of a daily refresh (08:00 or 20:00)
			h, m := now.Hour(), now.Minute()
			isDailyWindow := (h == 8 || h == 20) && m < 5
			if isDailyWindow {
				time.Sleep(10 * time.Minute)
				continue
			}
			// Next hour mark
			nextHour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour()+1, 0, 0, 0, loc)
			wait := time.Until(nextHour)
			log.Printf("⏰ Next hourly price refresh in %v", wait.Round(time.Second))
			time.Sleep(wait)

			// Re-check daily window after sleep
			now = time.Now().In(loc)
			h, m = now.Hour(), now.Minute()
			if (h == 8 || h == 20) && m < 10 {
				log.Println("⏭️ Skipping hourly refresh (daily refresh window)")
				continue
			}

			log.Println("⏱️ Triggering hourly price refresh...")
			if res, err := runHourlyPriceRefresh(); err != nil {
				log.Printf("⚠️ Hourly price refresh error: %v", err)
			} else {
				log.Printf("✅ Hourly price refresh complete: %d items, fallback=%v", res.Count, res.AIFallback)
			}
		}
	}()

	log.Println("🍬 Wonka v3 starting on :8090")
	e.Logger.Fatal(e.Start(":8090"))
}
