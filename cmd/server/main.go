package main

import (
	"context"
	"log"
	"time"

	"github.com/atharva-777/chat-backend-go/internal/auth"
	"github.com/atharva-777/chat-backend-go/internal/config"
	"github.com/atharva-777/chat-backend-go/internal/store/postgres"
	redistore "github.com/atharva-777/chat-backend-go/internal/store/redis"
	httproutes "github.com/atharva-777/chat-backend-go/internal/transport/http"
	"github.com/atharva-777/chat-backend-go/internal/transport/ws"
	"github.com/gin-gonic/gin"
)

func main() {
	cfg := config.Load()

	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}

	rootCtx := context.Background()

	pgPool, err := postgres.NewPool(rootCtx, cfg.PostgresDSN)
	if err != nil {
		log.Fatalf("server: postgres setup failed: %v", err)
	}
	defer pgPool.Close()

	redisClient := redistore.NewClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer func() {
		if err := redisClient.Close(); err != nil {
			log.Printf("server: redis close failed: %v", err)
		}
	}()

	if err := redistore.Ping(rootCtx, redisClient); err != nil {
		log.Fatalf("server: redis setup failed: %v", err)
	}

	authService, err := auth.NewService(
		pgPool,
		cfg.JWTSecret,
		time.Duration(cfg.AccessTokenTTL)*time.Minute,
		time.Duration(cfg.RefreshTokenTTL)*time.Hour,
	)
	if err != nil {
		log.Fatalf("server: auth setup failed: %v", err)
	}

	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())

	healthHandler := httproutes.HealthHandler{
		Env:      cfg.AppEnv,
		Postgres: pgPool,
		Redis:    redisClient,
	}
	healthHandler.RegisterRoutes(router)

	authHandler := httproutes.NewAuthHandler(authService)
	authHandler.RegisterRoutes(router, auth.Middleware(authService))

	hub := ws.NewHub()
	hubCtx, cancelHub := context.WithCancel(rootCtx)
	defer cancelHub()
	go hub.Run(hubCtx)

	wsHandler := ws.NewHandler(hub, authService, pgPool, cfg.WSAllowedOrigin)
	router.GET("/ws", wsHandler.Handle)

	addr := ":" + cfg.HTTPPort
	log.Printf("server: starting on %s", addr)
	if err := router.Run(addr); err != nil {
		log.Fatalf("server: failed to start: %v", err)
	}
}
