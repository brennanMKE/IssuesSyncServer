package v1

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"sync.sstools.co/internal/storage"
)

// IssuesHandler handles GET /v1/folders/{folderId}/issues.
// Returns []IssueMetadata for a project the user can access.
func IssuesHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, isAdmin, ok := resolveUser(w, r, deps)
		if !ok {
			return
		}

		folderID, ok2 := parseFolderID(w, r)
		if !ok2 {
			return
		}

		// Verify the user has access to this project.
		if _, err := storage.ProjectByID(r.Context(), deps.DB, userID, isAdmin, folderID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "folder not found")
				return
			}
			slog.Error("issues: verify folder access", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		issues, err := storage.IssuesForProject(r.Context(), deps.DB, folderID)
		if err != nil {
			slog.Error("issues: list issues", "err", err, "folder_id", folderID)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(issues)
	}
}

// IssueByIDHandler handles GET /v1/folders/{folderId}/issues/{id}.
// Streams the issue markdown from S3, setting ETag and Content-Type headers.
func IssueByIDHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, isAdmin, ok := resolveUser(w, r, deps)
		if !ok {
			return
		}

		folderID, ok2 := parseFolderID(w, r)
		if !ok2 {
			return
		}

		issueID := r.PathValue("id")
		if issueID == "" {
			writeError(w, http.StatusBadRequest, "missing issue id")
			return
		}

		// Verify folder access.
		if _, err := storage.ProjectByID(r.Context(), deps.DB, userID, isAdmin, folderID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "folder not found")
				return
			}
			slog.Error("issue: verify folder access", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		s3Key, etag, err := storage.IssueByID(r.Context(), deps.DB, folderID, issueID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "issue not found")
				return
			}
			slog.Error("issue: lookup", "err", err, "folder_id", folderID, "issue_id", issueID)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		body, _, err := storage.GetObject(r.Context(), deps.S3, deps.S3Bucket, s3Key)
		if err != nil {
			slog.Error("issue: get from s3", "err", err, "key", s3Key)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer body.Close()

		// Set headers before writing the body.
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("ETag", `"`+etag+`"`)
		w.WriteHeader(http.StatusOK)

		buf := make([]byte, 32*1024)
		for {
			n, readErr := body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
			}
			if readErr != nil {
				break
			}
		}
	}
}

// parseFolderID extracts and validates the {folderId} path parameter.
// Writes an error response and returns false on failure.
func parseFolderID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := r.PathValue("folderId")
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid folder id")
		return uuid.Nil, false
	}
	return id, true
}
