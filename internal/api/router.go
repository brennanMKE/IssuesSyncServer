package api

import (
	"encoding/json"
	"net/http"

	"sync.sstools.co/internal/auth"
	"sync.sstools.co/internal/storage"
	"sync.sstools.co/internal/ws"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps holds all dependencies wired in from main.
type Deps struct {
	BuildSHA string
	Auth     *auth.Service
	DB       *pgxpool.Pool
	S3       *s3.Client
	S3Bucket string
	Hub      *ws.Hub
	// Legacy fields kept for compatibility during the transition from Phase A.
	Pool     *pgxpool.Pool
	S3Client *storage.S3Client
	Cache    *storage.LRUCache
}

// NewRouter constructs and returns the root HTTP handler.
func NewRouter(deps Deps) http.Handler {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("GET /healthz", healthzHandler(deps.BuildSHA))

	// Auth endpoints — stubs until Phase C/D wire them fully.
	mux.HandleFunc("POST /v1/auth/begin-assertion", stubNotImplemented)
	mux.HandleFunc("POST /v1/auth/finish-assertion", stubNotImplemented)
	mux.HandleFunc("POST /v1/auth/refresh", stubNotImplemented)
	mux.HandleFunc("POST /v1/auth/begin-registration", stubNotImplemented)
	mux.HandleFunc("POST /v1/auth/finish-registration", stubNotImplemented)
	mux.HandleFunc("POST /v1/auth/logout", stubNotImplemented)

	// Catch-all stubs for remaining /v1/* and /admin/* routes.
	mux.HandleFunc("/v1/", stubNotImplemented)
	mux.HandleFunc("/admin/", stubNotImplemented)

	return mux
}

// stubNotImplemented returns 501 Not Implemented with a JSON body.
func stubNotImplemented(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "not implemented"})
}
