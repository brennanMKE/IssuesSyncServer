package api

import (
	"net/http"

	"sync.sstools.co/internal/storage"
	"sync.sstools.co/internal/ws"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Deps holds all dependencies wired in from main.
type Deps struct {
	Pool     *pgxpool.Pool
	S3Client *storage.S3Client
	Cache    *storage.LRUCache
	Hub      *ws.Hub
	BuildSHA string
}

// NewRouter constructs and returns the root HTTP handler.
func NewRouter(deps Deps) http.Handler {
	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("GET /healthz", healthzHandler(deps.BuildSHA))

	// Stub /v1/* routes — return 501 until Phase C/D.
	mux.HandleFunc("/v1/", stubHandler)

	// Stub /admin/* routes — return 501 until Phase F.
	mux.HandleFunc("/admin/", stubHandler)

	return mux
}

// stubHandler returns 501 Not Implemented for routes not yet built.
func stubHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
