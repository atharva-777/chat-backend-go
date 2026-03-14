package httptransport

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
)

type HealthHandler struct {
	Env           string
	Postgres      *pgxpool.Pool
	Redis         *goredis.Client
	RedisRequired bool
}

type dependencyStatus struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func (h HealthHandler) RegisterRoutes(r gin.IRoutes) {
	r.GET("/health", h.GetHealth)
}

func (h HealthHandler) GetHealth(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	postgresStatus := dependencyStatus{Status: "up"}
	if h.Postgres == nil {
		postgresStatus = dependencyStatus{Status: "down", Error: "postgres pool is nil"}
	} else if err := h.Postgres.Ping(ctx); err != nil {
		postgresStatus = dependencyStatus{Status: "down", Error: err.Error()}
	}

	redisStatus := dependencyStatus{Status: "disabled"}
	redisHealthy := true

	if h.RedisRequired {
		redisHealthy = false
		if h.Redis == nil {
			redisStatus = dependencyStatus{Status: "down", Error: "redis client is nil"}
		} else if err := h.Redis.Ping(ctx).Err(); err != nil {
			redisStatus = dependencyStatus{Status: "down", Error: err.Error()}
		} else {
			redisStatus = dependencyStatus{Status: "up"}
			redisHealthy = true
		}
	} else if h.Redis != nil {
		if err := h.Redis.Ping(ctx).Err(); err != nil {
			redisStatus = dependencyStatus{Status: "down", Error: err.Error()}
			redisHealthy = false
		} else {
			redisStatus = dependencyStatus{Status: "up"}
		}
	}

	overallStatus := "ok"
	statusCode := http.StatusOK
	if postgresStatus.Status != "up" || !redisHealthy {
		overallStatus = "degraded"
		statusCode = http.StatusServiceUnavailable
	}

	c.JSON(statusCode, gin.H{
		"status": overallStatus,
		"env":    h.Env,
		"dependencies": gin.H{
			"postgres": postgresStatus,
			"redis":    redisStatus,
		},
		"timestamp": time.Now().UTC(),
	})
}
