package ws

import (
	"context"
)

type outboundMessage struct {
	payload    []byte
	recipients map[string]struct{}
}

type Hub struct {
	register   chan *Client
	unregister chan *Client
	broadcast  chan outboundMessage
	clients    map[*Client]struct{}
}

func NewHub() *Hub {
	return &Hub{
		register:   make(chan *Client),
		unregister: make(chan *Client),
		broadcast:  make(chan outboundMessage, 256),
		clients:    make(map[*Client]struct{}),
	}
}

func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			for client := range h.clients {
				delete(h.clients, client)
				client.close()
			}
			return
		case client := <-h.register:
			h.clients[client] = struct{}{}
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.close()
			}
		case message := <-h.broadcast:
			for client := range h.clients {
				if len(message.recipients) > 0 {
					if _, ok := message.recipients[client.userID]; !ok {
						continue
					}
				}

				select {
				case client.send <- message.payload:
				default:
					delete(h.clients, client)
					client.close()
				}
			}
		}
	}
}
