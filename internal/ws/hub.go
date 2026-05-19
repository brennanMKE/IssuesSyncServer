// Package ws implements the WebSocket hub and event fan-out for the
// /v1/events endpoint.
package ws

import (
	"log/slog"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event carries a pre-serialized JSON payload destined for all clients
// subscribed to the given ProjectID.
type Event struct {
	ProjectID string
	Payload   []byte // pre-serialized JSON
}

// client represents a single connected WebSocket peer.
type client struct {
	userID        string
	conn          *websocket.Conn
	send          chan []byte
	subscriptions map[string]bool // folderId → true
}

// Hub manages all active WebSocket clients and fans events out per-folder
// subscription.
type Hub struct {
	broadcast  chan Event
	register   chan *client
	unregister chan *client
	clients    map[*client]bool
	mu         sync.RWMutex
	db         *pgxpool.Pool
}

// NewHub constructs a new Hub backed by the given Postgres pool (used for
// event replay queries).
func NewHub(db *pgxpool.Pool) *Hub {
	return &Hub{
		broadcast:  make(chan Event, 256),
		register:   make(chan *client, 64),
		unregister: make(chan *client, 64),
		clients:    make(map[*client]bool),
		db:         db,
	}
}

// Run is the main event-loop goroutine. Call go hub.Run() from main.
func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.mu.Lock()
			h.clients[c] = true
			h.mu.Unlock()
			slog.Debug("ws: client registered", "user_id", c.userID)

		case c := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
			}
			h.mu.Unlock()
			slog.Debug("ws: client unregistered", "user_id", c.userID)

		case ev := <-h.broadcast:
			h.mu.RLock()
			for c := range h.clients {
				if c.subscriptions[ev.ProjectID] {
					select {
					case c.send <- ev.Payload:
					default:
						// Slow client: drop the frame and unregister.
						slog.Warn("ws: dropping slow client", "user_id", c.userID)
						h.mu.RUnlock()
						h.mu.Lock()
						if _, ok := h.clients[c]; ok {
							delete(h.clients, c)
							close(c.send)
						}
						h.mu.Unlock()
						h.mu.RLock()
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast sends a pre-serialized JSON payload to all clients subscribed to
// the given projectID. It is safe to call from any goroutine.
func (h *Hub) Broadcast(projectID string, payload []byte) {
	h.broadcast <- Event{ProjectID: projectID, Payload: payload}
}

// BroadcastAccessRevoked sends an accessRevoked event to all clients
// subscribed to the given projectID. Only the client whose userID matches the
// revoked member receives the event.
func (h *Hub) BroadcastAccessRevoked(projectID, userID string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.subscriptions[projectID] && c.userID == userID {
			payload := []byte(`{"type":"accessRevoked","folderId":"` + projectID + `"}`)
			select {
			case c.send <- payload:
			default:
				slog.Warn("ws: dropping accessRevoked for slow client", "user_id", c.userID)
			}
		}
	}
}
