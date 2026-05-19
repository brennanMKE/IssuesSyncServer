package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"sync.sstools.co/internal/auth"
)

const (
	pingInterval  = 30 * time.Second
	pongDeadline  = 10 * time.Second
	writeDeadline = 10 * time.Second
	sendBufSize   = 256
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Allow all origins; TLS + JWT auth is the real gate.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// incomingMsg is the envelope for client → server WebSocket messages.
type incomingMsg struct {
	Type     string  `json:"type"`
	FolderID string  `json:"folderId"`
	Since    *string `json:"since,omitempty"` // last seen event id as string
}

// Handler returns an http.HandlerFunc that upgrades the connection to WebSocket,
// authenticates the caller via the ?token= query param, and starts the
// read/write loops.
func Handler(hub *Hub, jwtKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Authenticate via ?token= because JS WebSocket can't set custom headers.
		tokenStr := r.URL.Query().Get("token")
		if tokenStr == "" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		userID, err := auth.ValidateAccessToken(tokenStr, jwtKey)
		if err != nil {
			http.Error(w, "invalid or expired token", http.StatusUnauthorized)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Warn("ws: upgrade failed", "err", err)
			return
		}

		c := &client{
			userID:        userID,
			conn:          conn,
			send:          make(chan []byte, sendBufSize),
			subscriptions: make(map[string]bool),
		}
		hub.register <- c

		go writeLoop(c, hub)
		readLoop(c, hub)
	}
}

// readLoop reads JSON messages from the client until the connection closes.
func readLoop(c *client, hub *Hub) {
	defer func() {
		hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Time{}) // no overall read deadline; pings keep it alive
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pingInterval + pongDeadline))
		return nil
	})

	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Debug("ws: read error", "user_id", c.userID, "err", err)
			}
			return
		}

		var in incomingMsg
		if err := json.Unmarshal(msg, &in); err != nil {
			slog.Debug("ws: bad message", "user_id", c.userID, "err", err)
			continue
		}

		switch in.Type {
		case "subscribe":
			c.subscriptions[in.FolderID] = true
			ack, _ := json.Marshal(map[string]string{
				"type":     "subscribed",
				"folderId": in.FolderID,
			})
			select {
			case c.send <- ack:
			default:
			}

			// Replay missed events if since is provided.
			if in.Since != nil {
				sinceID, err := strconv.ParseInt(*in.Since, 10, 64)
				if err == nil {
					go func(folderID string, since int64) {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						if err := ReplayEvents(ctx, hub.db, folderID, since, c.send); err != nil {
							slog.Warn("ws: replay error", "folder_id", folderID, "err", err)
						}
					}(in.FolderID, sinceID)
				}
			}

		case "unsubscribe":
			delete(c.subscriptions, in.FolderID)

		default:
			slog.Debug("ws: unknown message type", "type", in.Type, "user_id", c.userID)
		}
	}
}

// writeLoop drains c.send to the WebSocket connection and sends periodic pings.
func writeLoop(c *client, hub *Hub) {
	ticker := time.NewTicker(pingInterval)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case payload, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
			if !ok {
				// Hub closed the channel; send a close frame.
				_ = c.conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				slog.Debug("ws: write error", "user_id", c.userID, "err", err)
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				slog.Debug("ws: ping error", "user_id", c.userID, "err", err)
				return
			}
		}
	}
}
