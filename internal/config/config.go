package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds runtime configuration from environment variables.
type Config struct {
	AppEnv          string
	HTTPPort        string
	PostgresDSN     string
	RedisAddr       string
	RedisPassword   string
	RedisDB         int
	JWTSecret       string
	AccessTokenTTL  int
	RefreshTokenTTL int
	WSAllowedOrigin string
}

func Load() Config {
	// Load .env in local development; ignore if file is missing.
	if err := godotenv.Load(); err != nil {
		log.Printf("config: .env not loaded (%v), falling back to system env", err)
	}

	return Config{
		AppEnv:          getEnv("APP_ENV", "development"),
		HTTPPort:        getEnvFirst([]string{"HTTP_PORT", "PORT"}, "8080"),
		PostgresDSN:     getEnv("POSTGRES_DSN", ""),
		RedisAddr:       getEnv("REDIS_ADDR", ""),
		RedisPassword:   getEnv("REDIS_PASSWORD", ""),
		RedisDB:         getEnvAsInt("REDIS_DB", 0),
		JWTSecret:       getEnv("JWT_SECRET", "change-me"),
		AccessTokenTTL:  getEnvAsInt("ACCESS_TOKEN_TTL_MINUTES", 15),
		RefreshTokenTTL: getEnvAsInt("REFRESH_TOKEN_TTL_HOURS", 720),
		WSAllowedOrigin: getEnv("WS_ALLOWED_ORIGIN", "*"),
	}
}

func getEnvFirst(keys []string, defaultValue string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return defaultValue
}

func getEnvAsInt(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("config: %s=%q is invalid int, using default %d", key, value, defaultValue)
		return defaultValue
	}
	return parsed
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
