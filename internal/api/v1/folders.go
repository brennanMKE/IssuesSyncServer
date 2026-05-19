package v1

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"sync.sstools.co/internal/auth"
	"sync.sstools.co/internal/storage"
)

// FoldersHandler handles GET /v1/folders.
// Returns the list of FolderInfo for projects the authenticated user can access.
func FoldersHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, isAdmin, ok := resolveUser(w, r, deps)
		if !ok {
			return
		}

		folders, err := storage.ProjectsForUser(r.Context(), deps.DB, userID, isAdmin)
		if err != nil {
			slog.Error("folders: list projects", "err", err, "user_id", userID)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(folders)
	}
}

// FolderByIDHandler handles GET /v1/folders/{folderId}.
// Returns a single FolderInfo or 404 if not found / not a member.
func FolderByIDHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, isAdmin, ok := resolveUser(w, r, deps)
		if !ok {
			return
		}

		folderIDStr := r.PathValue("folderId")
		folderID, err := uuid.Parse(folderIDStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid folder id")
			return
		}

		folder, err := storage.ProjectByID(r.Context(), deps.DB, userID, isAdmin, folderID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "folder not found")
				return
			}
			slog.Error("folders: get project", "err", err, "folder_id", folderID)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(folder)
	}
}

// resolveUser is a shared helper that extracts and validates the authenticated
// user from context, looks up their global role, and returns the parsed userID
// and isAdmin flag. On failure it writes an appropriate error response and
// returns ok=false.
func resolveUser(w http.ResponseWriter, r *http.Request, deps Deps) (userID uuid.UUID, isAdmin bool, ok bool) {
	userIDStr, ctxOK := auth.UserIDFromContext(r.Context())
	if !ctxOK {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false, false
	}

	uid, err := uuid.Parse(userIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return uuid.Nil, false, false
	}

	globalRole, err := storage.UserByID(r.Context(), deps.DB, uid)
	if err != nil {
		slog.Error("resolve user: fetch user", "err", err, "user_id", uid)
		writeError(w, http.StatusInternalServerError, "internal error")
		return uuid.Nil, false, false
	}

	return uid, globalRole == "admin", true
}
