package httptransport

import (
	"errors"
	"net/http"
	"strings"

	"github.com/atharva-777/chat-backend-go/internal/auth"
	"github.com/gin-gonic/gin"
)

type AuthHandler struct {
	service *auth.Service
}

type registerRequest struct {
	Email       string `json:"email" binding:"required,email"`
	Username    string `json:"username" binding:"required,min=3,max=40"`
	Password    string `json:"password" binding:"required,min=8,max=72"`
	DisplayName string `json:"display_name" binding:"omitempty,max=80"`
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8,max=72"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type userResponse struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name,omitempty"`
}

type authResponse struct {
	User                 userResponse `json:"user"`
	AccessToken          string       `json:"access_token"`
	RefreshToken         string       `json:"refresh_token"`
	AccessTokenExpiresAt string       `json:"access_token_expires_at"`
	RefreshTokenExpires  string       `json:"refresh_token_expires_at"`
}

func NewAuthHandler(service *auth.Service) *AuthHandler {
	return &AuthHandler{service: service}
}

func (h *AuthHandler) RegisterRoutes(r gin.IRouter, authMiddleware gin.HandlerFunc) {
	authRoutes := r.Group("/auth")
	authRoutes.POST("/register", h.Register)
	authRoutes.POST("/login", h.Login)
	authRoutes.POST("/refresh", h.Refresh)
	authRoutes.POST("/logout", h.Logout)

	protected := authRoutes.Group("")
	protected.Use(authMiddleware)
	protected.GET("/me", h.Me)
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid register payload"})
		return
	}

	result, err := h.service.Register(c.Request.Context(), auth.RegisterInput{
		Email:       req.Email,
		Username:    req.Username,
		Password:    req.Password,
		DisplayName: req.DisplayName,
	}, clientMetaFromRequest(c))
	if err != nil {
		writeAuthError(c, err)
		return
	}

	c.JSON(http.StatusCreated, toAuthResponse(result))
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid login payload"})
		return
	}

	result, err := h.service.Login(c.Request.Context(), auth.LoginInput{
		Email:    req.Email,
		Password: req.Password,
	}, clientMetaFromRequest(c))
	if err != nil {
		writeAuthError(c, err)
		return
	}

	c.JSON(http.StatusOK, toAuthResponse(result))
}

func (h *AuthHandler) Refresh(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid refresh payload"})
		return
	}

	result, err := h.service.Refresh(c.Request.Context(), req.RefreshToken, clientMetaFromRequest(c))
	if err != nil {
		writeAuthError(c, err)
		return
	}

	c.JSON(http.StatusOK, toAuthResponse(result))
}

func (h *AuthHandler) Logout(c *gin.Context) {
	var req logoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid logout payload"})
		return
	}

	if err := h.service.Logout(c.Request.Context(), req.RefreshToken); err != nil {
		writeAuthError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "logged_out"})
}

func (h *AuthHandler) Me(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	user, err := h.service.GetUserByID(c.Request.Context(), userID)
	if err != nil {
		writeAuthError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"user": toUserResponse(user)})
}

func toAuthResponse(result auth.AuthResult) authResponse {
	return authResponse{
		User:                 toUserResponse(result.User),
		AccessToken:          result.AccessToken,
		RefreshToken:         result.RefreshToken,
		AccessTokenExpiresAt: result.AccessTokenExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		RefreshTokenExpires:  result.RefreshTokenExpires.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
}

func toUserResponse(user auth.User) userResponse {
	return userResponse{
		ID:          user.ID,
		Email:       user.Email,
		Username:    user.Username,
		DisplayName: user.DisplayName,
	}
}

func clientMetaFromRequest(c *gin.Context) auth.ClientMeta {
	return auth.ClientMeta{
		UserAgent: strings.TrimSpace(c.Request.UserAgent()),
		IPAddress: strings.TrimSpace(c.ClientIP()),
	}
}

func writeAuthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrInvalidInput):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, auth.ErrInvalidCredentials):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid email or password"})
	case errors.Is(err, auth.ErrEmailExists):
		c.JSON(http.StatusConflict, gin.H{"error": "email already exists"})
	case errors.Is(err, auth.ErrUsernameExists):
		c.JSON(http.StatusConflict, gin.H{"error": "username already exists"})
	case errors.Is(err, auth.ErrTokenExpired):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "refresh token expired"})
	case errors.Is(err, auth.ErrInvalidToken):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
	case errors.Is(err, auth.ErrUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}
