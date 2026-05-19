// Package v1 contains the HTTP handlers for the /v1/* REST API.
package v1

import (
	"encoding/json"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync.sstools.co/internal/auth"
	"sync.sstools.co/internal/storage"
	"sync.sstools.co/internal/ws"
)

// Deps holds the dependencies required by all v1 handlers.
type Deps struct {
	DB            *pgxpool.Pool
	S3            *s3.Client
	S3Bucket      string
	RPDisplayName string
	Cache         *storage.LRUCache
	Auth          *auth.Service
	Hub           *ws.Hub
}

// writeError writes a JSON error response with the given status code and message.
func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
