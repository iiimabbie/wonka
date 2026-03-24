package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

var pool *pgxpool.Pool

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET is required")
	}
	_ = jwtSecret // will be used by handlers later

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

	stub := func(c echo.Context) error {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "not implemented yet"})
	}

	// Candy routes
	e.POST("/v1/candies/adjust", stub)
	e.GET("/v1/candies/balance", stub)
	e.GET("/v1/candies/history", stub)
	e.GET("/v1/candies/leaderboard", stub)
	e.GET("/v1/candies/summary", stub)
	e.POST("/v1/candies/transfer", stub)
	e.GET("/v1/transfers/history", stub)

	// Market routes
	e.GET("/v1/market", stub)
	e.POST("/v1/market/buy", stub)
	e.POST("/v1/market/sell", stub)
	e.GET("/v1/market/items", stub)
	e.GET("/v1/market/prices", stub)
	e.GET("/v1/market/events", stub)
	e.POST("/v1/market/refresh", stub)

	// Inventory routes
	e.GET("/v1/inventory", stub)
	e.GET("/v1/inventory/history", stub)

	// Auth routes
	e.POST("/v1/auth/register", stub)
	e.POST("/v1/auth/login", stub)

	// Agent routes (user-auth)
	e.POST("/v1/agents/create", stub)
	e.GET("/v1/agents", stub)
	e.GET("/v1/user/profile", stub)
	e.GET("/v1/agents/:agentId/balance", stub)
	e.GET("/v1/agents/:agentId/inventory", stub)
	e.GET("/v1/agents/:agentId/history", stub)
	e.POST("/v1/agents/:agentId/regenerate-key", stub)

	// Admin routes
	e.GET("/v1/admin/agents", stub)
	e.PATCH("/v1/admin/agents/:agentId", stub)
	e.GET("/v1/admin/users", stub)
	e.DELETE("/v1/admin/users/:userId", stub)
	e.POST("/v1/admin/adjust", stub)
	e.POST("/v1/admin/agents/:agentId/regenerate-key", stub)
	e.POST("/v1/admin/market/refresh", stub)
	e.GET("/v1/admin/settings", stub)
	e.PUT("/v1/admin/settings", stub)

	log.Println("🍬 Wonka v3 starting on :8090")
	e.Logger.Fatal(e.Start(":8090"))
}
