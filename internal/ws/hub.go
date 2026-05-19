// Package ws implements the WebSocket hub and event fan-out for the
// /v1/events endpoint.
package ws

// Hub manages active WebSocket connections and broadcasts events to
// subscribed clients. It is implemented in Phase E.
type Hub struct {
	// Phase E — event fan-out channels and subscriber registry go here.
}

// NewHub constructs a new Hub.
func NewHub() *Hub {
	return &Hub{}
}

// Run starts the hub's event loop. It must be called in a goroutine.
// Phase E will implement the select loop; for now it blocks until the
// hub is garbage-collected.
func (h *Hub) Run() {
	// Phase E — implement subscribe/unsubscribe/broadcast select loop.
	select {}
}
