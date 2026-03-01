package auth

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const ContextUserIDKey = "auth_user_id"

var ErrMissingAuthorizationHeader = errors.New("missing authorization header")

func Middleware(service *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, err := ExtractBearerToken(c.GetHeader("Authorization"))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing or invalid authorization header"})
			return
		}

		userID, err := service.ValidateAccessToken(token)
		if err != nil {
			if errors.Is(err, ErrTokenExpired) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "access token expired"})
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid access token"})
			return
		}

		c.Set(ContextUserIDKey, userID)
		c.Next()
	}
}

func ExtractBearerToken(header string) (string, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", ErrMissingAuthorizationHeader
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", ErrInvalidToken
	}

	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", ErrInvalidToken
	}

	return token, nil
}

func UserIDFromContext(c *gin.Context) (string, bool) {
	value, exists := c.Get(ContextUserIDKey)
	if !exists {
		return "", false
	}

	userID, ok := value.(string)
	if !ok {
		return "", false
	}

	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", false
	}

	return userID, true
}
