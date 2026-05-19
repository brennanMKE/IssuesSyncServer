package v1

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"sync.sstools.co/internal/auth"
	"sync.sstools.co/internal/storage"
	"sync.sstools.co/internal/wire"
)

// HostHandler handles GET /v1/host.
// Returns the server display name and the count of projects the authenticated
// user can access.
func HostHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userIDStr, ok := auth.UserIDFromContext(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid user id")
			return
		}

		globalRole, err := storage.UserByID(r.Context(), deps.DB, userID)
		if err != nil {
			slog.Error("host: fetch user", "err", err, "user_id", userID)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		isAdmin := globalRole == "admin"

		folders, err := storage.ProjectsForUser(r.Context(), deps.DB, userID, isAdmin)
		if err != nil {
			slog.Error("host: fetch projects", "err", err, "user_id", userID)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		resp := wire.HostInfo{
			DisplayName: deps.RPDisplayName,
			FolderCount: len(folders),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
