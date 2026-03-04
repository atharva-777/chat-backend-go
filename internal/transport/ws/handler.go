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
	"github.com/atharva-777/chat-backend-go/internal/chat"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
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
	chatService *chat.Service
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
	MessageID   string    `json:"message_id,omitempty"`
	SenderID    string    `json:"sender_id,omitempty"`
	UserID      string    `json:"user_id,omitempty"`
	ClientMsgID string    `json:"client_msg_id,omitempty"`
	Content     string    `json:"content,omitempty"`
	Error       string    `json:"error,omitempty"`
	SentAt      time.Time `json:"sent_at,omitempty"`
}

func NewHandler(hub *Hub, authService *auth.Service, chatService *chat.Service, allowedOrigin string) *Handler {
	return &Handler{
		hub:         hub,
		authService: authService,
		chatService: chatService,
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
	in.Type = strings.TrimSpace(in.Type)
	switch in.Type {
	case "ping":
		c.queueEvent(Event{Type: "pong", SentAt: time.Now().UTC()})
	case "message.send":
		c.handleMessageSend(in)
	case "message.read":
		c.handleMessageRead(in)
	case "typing.start", "typing.stop":
		c.handleTyping(in)
	default:
		c.queueEvent(Event{Type: "error", Error: "unsupported_event_type", SentAt: time.Now().UTC()})
	}
}

func (c *Client) handleMessageSend(in Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msg, recipients, err := c.handler.chatService.SendMessage(ctx, c.userID, chat.SendMessageInput{
		ChatID:      in.ChatID,
		ClientMsgID: in.ClientMsgID,
		Content:     in.Content,
	})
	if err != nil {
		c.queueEvent(Event{Type: "error", Error: mapChatError(err), SentAt: time.Now().UTC()})
		return
	}

	clientMsgID := ""
	if msg.ClientMsgID != nil {
		clientMsgID = *msg.ClientMsgID
	}

	c.queueEvent(Event{
		Type:        "message.ack",
		ChatID:      msg.ChatID,
		MessageID:   msg.ID,
		ClientMsgID: clientMsgID,
		SentAt:      msg.SentAt,
	})

	c.broadcastEvent(Event{
		Type:        "message.new",
		ChatID:      msg.ChatID,
		MessageID:   msg.ID,
		SenderID:    msg.SenderID,
		ClientMsgID: clientMsgID,
		Content:     msg.Content,
		SentAt:      msg.SentAt,
	}, recipientSet(recipients))
}

func (c *Client) handleMessageRead(in Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receipt, recipients, err := c.handler.chatService.MarkRead(ctx, c.userID, chat.MarkReadInput{
		ChatID:    in.ChatID,
		MessageID: in.MessageID,
	})
	if err != nil {
		c.queueEvent(Event{Type: "error", Error: mapChatError(err), SentAt: time.Now().UTC()})
		return
	}

	c.broadcastEvent(Event{
		Type:      "message.read",
		ChatID:    receipt.ChatID,
		MessageID: receipt.MessageID,
		UserID:    receipt.UserID,
		SentAt:    receipt.ReadAt,
	}, recipientSet(recipients))
}

func (c *Client) handleTyping(in Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	recipients, err := c.handler.chatService.ChatRecipientsForMember(ctx, c.userID, in.ChatID)
	if err != nil {
		c.queueEvent(Event{Type: "error", Error: mapChatError(err), SentAt: time.Now().UTC()})
		return
	}

	c.broadcastEvent(Event{
		Type:   in.Type,
		ChatID: strings.TrimSpace(in.ChatID),
		UserID: c.userID,
		SentAt: time.Now().UTC(),
	}, recipientSet(recipients))
}

func mapChatError(err error) string {
	switch {
	case errors.Is(err, chat.ErrInvalidInput):
		return "invalid_payload"
	case errors.Is(err, chat.ErrForbiddenChat):
		return "forbidden_chat"
	case errors.Is(err, chat.ErrChatNotFound):
		return "chat_not_found"
	case errors.Is(err, chat.ErrMessageNotFound):
		return "message_not_found"
	case errors.Is(err, chat.ErrUserNotFound):
		return "user_not_found"
	case strings.Contains(strings.ToLower(err.Error()), "invalid input syntax for type uuid"):
		return "invalid_payload"
	default:
		return "server_error"
	}
}

func recipientSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		set[id] = struct{}{}
	}
	return set
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
