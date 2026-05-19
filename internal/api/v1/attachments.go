package v1

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"sync.sstools.co/internal/storage"
)

// AttachmentHandler handles GET /v1/folders/{folderId}/issues/{id}/attachments/{name}.
// Streams the attachment bytes from S3.
func AttachmentHandler(deps Deps) http.HandlerFunc {
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

		name := r.PathValue("name")
		if name == "" {
			writeError(w, http.StatusBadRequest, "missing attachment name")
			return
		}

		// Verify folder access.
		if _, err := storage.ProjectByID(r.Context(), deps.DB, userID, isAdmin, folderID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "folder not found")
				return
			}
			slog.Error("attachment: verify folder access", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		s3Key, contentType, err := storage.AttachmentByPath(r.Context(), deps.DB, folderID, issueID, name)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "attachment not found")
				return
			}
			slog.Error("attachment: lookup", "err", err, "folder_id", folderID, "issue_id", issueID, "name", name)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := storage.StreamObject(r.Context(), deps.S3, deps.S3Bucket, s3Key, contentType, w); err != nil {
			slog.Error("attachment: stream from s3", "err", err, "key", s3Key)
			// Headers may already be written; nothing more we can do.
			return
		}
	}
}
