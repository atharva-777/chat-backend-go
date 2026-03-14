package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/atharva-777/chat-backend-go/internal/auth"
	"github.com/atharva-777/chat-backend-go/internal/chat"
	"github.com/atharva-777/chat-backend-go/internal/config"
	"github.com/atharva-777/chat-backend-go/internal/store/postgres"
	redistore "github.com/atharva-777/chat-backend-go/internal/store/redis"
	httproutes "github.com/atharva-777/chat-backend-go/internal/transport/http"
	"github.com/atharva-777/chat-backend-go/internal/transport/ws"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
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

	var redisClient *goredis.Client
	redisEnabled := strings.TrimSpace(cfg.RedisAddr) != ""
	if redisEnabled {
		redisClient = redistore.NewClient(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
		defer func() {
			if err := redisClient.Close(); err != nil {
				log.Printf("server: redis close failed: %v", err)
			}
		}()

		if err := redistore.Ping(rootCtx, redisClient); err != nil {
			log.Fatalf("server: redis setup failed: %v", err)
		}
	} else {
		log.Printf("server: redis disabled (REDIS_ADDR empty)")
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

	chatService, err := chat.NewService(pgPool)
	if err != nil {
		log.Fatalf("server: chat setup failed: %v", err)
	}

	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())

	healthHandler := httproutes.HealthHandler{
		Env:           cfg.AppEnv,
		Postgres:      pgPool,
		Redis:         redisClient,
		RedisRequired: redisEnabled,
	}
	healthHandler.RegisterRoutes(router)

	authMiddleware := auth.Middleware(authService)
	authHandler := httproutes.NewAuthHandler(authService)
	authHandler.RegisterRoutes(router, authMiddleware)

	chatHandler := httproutes.NewChatHandler(chatService)
	chatHandler.RegisterRoutes(router, authMiddleware)

	hub := ws.NewHub()
	hubCtx, cancelHub := context.WithCancel(rootCtx)
	defer cancelHub()
	go hub.Run(hubCtx)

	wsHandler := ws.NewHandler(hub, authService, chatService, cfg.WSAllowedOrigin)
	router.GET("/ws", wsHandler.Handle)

	addr := ":" + cfg.HTTPPort
	log.Printf("server: starting on %s", addr)

	server := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server: failed to start: %v", err)
	}
}
