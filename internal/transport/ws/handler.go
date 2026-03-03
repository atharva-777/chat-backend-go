package ws

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/atharva-777/chat-backend-go/internal/auth"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 16 * 1024
)

type Handler struct {
	hub         *Hub
	authService *auth.Service
	pgPool      *pgxpool.Pool
	upgrader    websocket.Upgrader
}

type Client struct {
	userID    string
	conn      *websocket.Conn
	hub       *Hub
	handler   *Handler
	send      chan []byte
	closeOnce sync.Once
}

type Event struct {
	Type        string    `json:"type"`
	ChatID      string    `json:"chat_id,omitempty"`
	SenderID    string    `json:"sender_id,omitempty"`
	ClientMsgID string    `json:"client_msg_id,omitempty"`
	Content     string    `json:"content,omitempty"`
	Error       string    `json:"error,omitempty"`
	SentAt      time.Time `json:"sent_at,omitempty"`
}

func NewHandler(hub *Hub, authService *auth.Service, pgPool *pgxpool.Pool, allowedOrigin string) *Handler {
	return &Handler{
		hub:         hub,
		authService: authService,
		pgPool:      pgPool,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     buildOriginChecker(allowedOrigin),
		},
	}
}

func (h *Handler) Handle(c *gin.Context) {
	userID, err := h.authenticate(c)
	if err != nil {
		if errors.Is(err, auth.ErrTokenExpired) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "access token expired"})
			return
		}
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("ws: upgrade failed: %v", err)
		return
	}

	client := &Client{
		userID:  userID,
		conn:    conn,
		hub:     h.hub,
		handler: h,
		send:    make(chan []byte, 256),
	}

	h.hub.register <- client
	go client.writePump()
	client.readPump()
}

func (h *Handler) authenticate(c *gin.Context) (string, error) {
	if h.authService == nil {
		return "", errors.New("auth service is not configured")
	}

	token := strings.TrimSpace(c.Query("access_token"))
	if token == "" {
		token = strings.TrimSpace(c.Query("token"))
	}
	if token == "" {
		bearerToken, err := auth.ExtractBearerToken(c.GetHeader("Authorization"))
		if err != nil {
			return "", err
		}
		token = bearerToken
	}

	return h.authService.ValidateAccessToken(token)
}

func buildOriginChecker(allowedOrigin string) func(*http.Request) bool {
	origin := strings.TrimSpace(allowedOrigin)
	if origin == "" || origin == "*" {
		return func(_ *http.Request) bool { return true }
	}

	return func(r *http.Request) bool {
		return r.Header.Get("Origin") == origin
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("ws: unexpected close from %s: %v", c.userID, err)
			}
			return
		}

		var incoming Event
		if err := json.Unmarshal(raw, &incoming); err != nil {
			c.queueEvent(Event{Type: "error", Error: "invalid_json", SentAt: time.Now().UTC()})
			continue
		}

		c.handleIncoming(incoming)
	}
}

func (c *Client) handleIncoming(in Event) {
	switch in.Type {
	case "ping":
		c.queueEvent(Event{Type: "pong", SentAt: time.Now().UTC()})
	case "message.send":
		recipients, ok := c.authorizeRecipients(in.ChatID)
		if !ok {
			return
		}
		out := Event{
			Type:        "message.new",
			ChatID:      in.ChatID,
			SenderID:    c.userID,
			ClientMsgID: in.ClientMsgID,
			Content:     in.Content,
			SentAt:      time.Now().UTC(),
		}
		c.broadcastEvent(out, recipients)
	case "typing.start", "typing.stop":
		recipients, ok := c.authorizeRecipients(in.ChatID)
		if !ok {
			return
		}
		out := Event{
			Type:     in.Type,
			ChatID:   in.ChatID,
			SenderID: c.userID,
			SentAt:   time.Now().UTC(),
		}
		c.broadcastEvent(out, recipients)
	default:
		c.queueEvent(Event{Type: "error", Error: "unsupported_event_type", SentAt: time.Now().UTC()})
	}
}

func (c *Client) authorizeRecipients(chatID string) (map[string]struct{}, bool) {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		c.queueEvent(Event{Type: "error", Error: "missing_chat_id", SentAt: time.Now().UTC()})
		return nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	recipients, err := c.handler.loadChatRecipients(ctx, chatID)
	if err != nil {
		if strings.Contains(err.Error(), "invalid input syntax for type uuid") {
			c.queueEvent(Event{Type: "error", Error: "invalid_chat_id", SentAt: time.Now().UTC()})
			return nil, false
		}
		c.queueEvent(Event{Type: "error", Error: "authorization_check_failed", SentAt: time.Now().UTC()})
		return nil, false
	}

	if _, ok := recipients[c.userID]; !ok {
		c.queueEvent(Event{Type: "error", Error: "forbidden_chat", SentAt: time.Now().UTC()})
		return nil, false
	}

	return recipients, true
}

func (h *Handler) loadChatRecipients(ctx context.Context, chatID string) (map[string]struct{}, error) {
	if h.pgPool == nil {
		return nil, errors.New("postgres pool is not configured")
	}

	rows, err := h.pgPool.Query(ctx, `
		SELECT user_id
		FROM chat_members
		WHERE chat_id = $1
	`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	recipients := make(map[string]struct{})
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		recipients[userID] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return recipients, nil
}

func (c *Client) broadcastEvent(event Event, recipients map[string]struct{}) {
	payload, err := json.Marshal(event)
	if err != nil {
		c.queueEvent(Event{Type: "error", Error: "marshal_failed", SentAt: time.Now().UTC()})
		return
	}
	c.hub.broadcast <- outboundMessage{payload: payload, recipients: recipients}
}

func (c *Client) queueEvent(event Event) {
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	c.queueRaw(payload)
}

func (c *Client) queueRaw(payload []byte) {
	defer func() {
		_ = recover()
	}()
	select {
	case c.send <- payload:
	default:
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) close() {
	c.closeOnce.Do(func() {
		close(c.send)
		_ = c.conn.Close()
	})
}
