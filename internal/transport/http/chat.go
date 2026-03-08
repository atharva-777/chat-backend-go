package httptransport

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atharva-777/chat-backend-go/internal/auth"
	"github.com/atharva-777/chat-backend-go/internal/chat"
	"github.com/gin-gonic/gin"
)

type ChatHandler struct {
	service *chat.Service
}

type createDirectRequest struct {
	PeerUserID string `json:"peer_user_id" binding:"required"`
}

type createGroupRequest struct {
	Title         string   `json:"title" binding:"required,max=120"`
	MemberUserIDs []string `json:"member_user_ids" binding:"required,min=1"`
}

type sendMessageRequest struct {
	ClientMsgID string `json:"client_msg_id" binding:"omitempty,max=120"`
	Content     string `json:"content" binding:"required,max=4000"`
}

type markReadRequest struct {
	MessageID string `json:"message_id" binding:"required"`
}

type listMessagesResponse struct {
	Messages   []chat.Message `json:"messages"`
	HasMore    bool           `json:"has_more"`
	NextBefore *string        `json:"next_before,omitempty"`
}

func NewChatHandler(service *chat.Service) *ChatHandler {
	return &ChatHandler{service: service}
}

func (h *ChatHandler) RegisterRoutes(r gin.IRouter, authMiddleware gin.HandlerFunc) {
	protected := r.Group("")
	protected.Use(authMiddleware)

	protected.GET("/chats", h.ListChats)
	protected.GET("/chats/:chat_id", h.GetChat)
	protected.POST("/chats/direct", h.CreateDirect)
	protected.POST("/chats/group", h.CreateGroup)
	protected.GET("/chats/:chat_id/messages", h.ListMessages)
	protected.POST("/chats/:chat_id/messages", h.SendMessage)
	protected.POST("/chats/:chat_id/read", h.MarkRead)
	protected.GET("/users/search", h.SearchUsers)
}

func (h *ChatHandler) ListChats(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	limit := queryInt(c, "limit", 20)
	offset := queryInt(c, "offset", 0)

	chats, err := h.service.ListChats(c.Request.Context(), userID, chat.ListChatsOptions{Limit: limit, Offset: offset})
	if err != nil {
		writeChatError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"chats": chats})
}

func (h *ChatHandler) GetChat(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	chatID := strings.TrimSpace(c.Param("chat_id"))
	if chatID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat_id is required"})
		return
	}

	item, err := h.service.GetChatForUser(c.Request.Context(), userID, chatID)
	if err != nil {
		writeChatError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"chat": item})
}

func (h *ChatHandler) CreateDirect(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req createDirectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid direct chat payload"})
		return
	}

	item, err := h.service.CreateDirectChat(c.Request.Context(), userID, req.PeerUserID)
	if err != nil {
		writeChatError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"chat": item})
}

func (h *ChatHandler) CreateGroup(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req createGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid group chat payload"})
		return
	}

	item, err := h.service.CreateGroupChat(c.Request.Context(), userID, req.Title, req.MemberUserIDs)
	if err != nil {
		writeChatError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"chat": item})
}

func (h *ChatHandler) ListMessages(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	chatID := strings.TrimSpace(c.Param("chat_id"))
	if chatID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat_id is required"})
		return
	}

	limit := queryInt(c, "limit", 30)
	beforeRaw := strings.TrimSpace(c.Query("before"))
	var before *time.Time
	if beforeRaw != "" {
		parsed, err := time.Parse(time.RFC3339, beforeRaw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "before must be RFC3339 timestamp"})
			return
		}
		before = &parsed
	}

	page, err := h.service.ListMessages(c.Request.Context(), userID, chatID, chat.ListMessagesOptions{Limit: limit, Before: before})
	if err != nil {
		writeChatError(c, err)
		return
	}

	response := listMessagesResponse{Messages: page.Messages, HasMore: page.HasMore}
	if page.NextBefore != nil {
		formatted := page.NextBefore.UTC().Format(time.RFC3339)
		response.NextBefore = &formatted
	}

	c.JSON(http.StatusOK, response)
}

func (h *ChatHandler) SendMessage(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	chatID := strings.TrimSpace(c.Param("chat_id"))
	if chatID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat_id is required"})
		return
	}

	var req sendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid message payload"})
		return
	}

	msg, _, err := h.service.SendMessage(c.Request.Context(), userID, chat.SendMessageInput{
		ChatID:      chatID,
		ClientMsgID: req.ClientMsgID,
		Content:     req.Content,
	})
	if err != nil {
		writeChatError(c, err)
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": msg})
}

func (h *ChatHandler) MarkRead(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	chatID := strings.TrimSpace(c.Param("chat_id"))
	if chatID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "chat_id is required"})
		return
	}

	var req markReadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid read payload"})
		return
	}

	receipt, _, err := h.service.MarkRead(c.Request.Context(), userID, chat.MarkReadInput{ChatID: chatID, MessageID: req.MessageID})
	if err != nil {
		writeChatError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"receipt": receipt})
}

func (h *ChatHandler) SearchUsers(c *gin.Context) {
	userID, ok := auth.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	query := strings.TrimSpace(c.Query("query"))
	limit := queryInt(c, "limit", 20)

	users, err := h.service.SearchUsers(c.Request.Context(), userID, query, limit)
	if err != nil {
		writeChatError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{"users": users})
}

func queryInt(c *gin.Context, key string, defaultValue int) int {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return defaultValue
	}
	return value
}

func writeChatError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, chat.ErrInvalidInput):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, chat.ErrForbiddenChat):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden chat"})
	case errors.Is(err, chat.ErrChatNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "chat not found"})
	case errors.Is(err, chat.ErrUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
	case errors.Is(err, chat.ErrMessageNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "message not found"})
	case strings.Contains(strings.ToLower(err.Error()), "invalid input syntax for type uuid"):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid uuid in request payload or params"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	}
}
