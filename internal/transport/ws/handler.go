package ws

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

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
	hub      *Hub
	upgrader websocket.Upgrader
}

type Client struct {
	userID    string
	conn      *websocket.Conn
	hub       *Hub
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

func NewHandler(hub *Hub, allowedOrigin string) *Handler {
	return &Handler{
		hub: hub,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     buildOriginChecker(allowedOrigin),
		},
	}
}

func (h *Handler) Handle(c *gin.Context) {
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("ws: upgrade failed: %v", err)
		return
	}

	userID := strings.TrimSpace(c.Query("user_id"))
	if userID == "" {
		userID = "anonymous"
	}

	client := &Client{
		userID: userID,
		conn:   conn,
		hub:    h.hub,
		send:   make(chan []byte, 256),
	}

	h.hub.register <- client
	go client.writePump()
	client.readPump()
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
		out := Event{
			Type:        "message.new",
			ChatID:      in.ChatID,
			SenderID:    c.userID,
			ClientMsgID: in.ClientMsgID,
			Content:     in.Content,
			SentAt:      time.Now().UTC(),
		}
		c.broadcastEvent(out)
	case "typing.start", "typing.stop":
		out := Event{
			Type:     in.Type,
			ChatID:   in.ChatID,
			SenderID: c.userID,
			SentAt:   time.Now().UTC(),
		}
		c.broadcastEvent(out)
	default:
		c.queueEvent(Event{Type: "error", Error: "unsupported_event_type", SentAt: time.Now().UTC()})
	}
}

func (c *Client) broadcastEvent(event Event) {
	payload, err := json.Marshal(event)
	if err != nil {
		c.queueEvent(Event{Type: "error", Error: "marshal_failed", SentAt: time.Now().UTC()})
		return
	}
	c.hub.broadcast <- payload
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
