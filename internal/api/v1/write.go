package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"
	"sync.sstools.co/internal/storage"
)

const maxBodySize = 25 * 1024 * 1024 // 25 MB

var issueIDPattern = regexp.MustCompile(`^\d{4}$`)

// computeETag returns the lowercase hex SHA-256 of b.
func computeETag(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// clientIP extracts the best-effort client IP from the request.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	return r.RemoteAddr
}

// readBody reads the request body up to maxBodySize. Returns 413 on overflow.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		} else {
			writeError(w, http.StatusBadRequest, "failed to read body")
		}
		return nil, false
	}
	return body, true
}

// detectContentType sniffs up to 512 bytes to determine content type.
func detectContentType(body []byte) string {
	sniff := body
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	ct := http.DetectContentType(sniff)
	if ct == "" {
		return "application/octet-stream"
	}
	return ct
}

// writeETagJSON writes a 200 (or specified status) JSON body with the new ETag
// and sets the ETag response header.
func writeETagJSON(w http.ResponseWriter, status int, etag string) {
	w.Header().Set("ETag", `"`+etag+`"`)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"etag": etag})
}

// PutIssueHandler handles PUT /v1/folders/{folderId}/issues/{id}.
func PutIssueHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _, ok := resolveUser(w, r, deps)
		if !ok {
			return
		}
		folderID, ok2 := parseFolderID(w, r)
		if !ok2 {
			return
		}
		issueID := r.PathValue("id")
		if !issueIDPattern.MatchString(issueID) {
			writeError(w, http.StatusBadRequest, "invalid issue id: must match ^\\d{4}$")
			return
		}

		role, err := ProjectRole(r.Context(), deps.DB, userID, folderID)
		if writeErrStatus(w, err) {
			return
		}
		if err := RequireRole(role, "editor"); writeErrStatus(w, err) {
			return
		}

		ifMatch := r.Header.Get("If-Match")
		ifNoneMatch := r.Header.Get("If-None-Match")
		isCreate := ifNoneMatch == "*"

		if !isCreate && ifMatch == "" {
			writeError(w, http.StatusPreconditionRequired, "If-Match or If-None-Match: * required")
			return
		}

		body, ok3 := readBody(w, r)
		if !ok3 {
			return
		}

		newETag := computeETag(body)
		s3Key := "projects/" + folderID.String() + "/issues/" + issueID + ".md"
		path := issueID + ".md"
		const ct = "text/markdown; charset=utf-8"

		// Fetch the old ETag for the audit log.
		oldETag, _, _ := storage.FileExists(r.Context(), deps.DB, folderID, path)

		if err := storage.PutObject(r.Context(), deps.S3, deps.S3Bucket, s3Key, body, ct, newETag); err != nil {
			slog.Error("put issue: s3 put", "err", err, "key", s3Key)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		var matchEtag string
		if !isCreate {
			matchEtag = ifMatch
		}
		if err := storage.UpsertFile(r.Context(), deps.DB, folderID, path, newETag, int64(len(body)), ct, userID, matchEtag); err != nil {
			if errors.Is(err, storage.ErrETagConflict) {
				writeError(w, http.StatusConflict, "etag conflict")
			} else {
				slog.Error("put issue: upsert file", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		if deps.Cache != nil {
			deps.Cache.Delete(s3Key)
		}

		_ = storage.WriteAuditLog(r.Context(), deps.DB, userID, "put_issue",
			folderID.String(), path, oldETag, newETag, clientIP(r), r.UserAgent())

		writeETagJSON(w, http.StatusOK, newETag)
	}
}

// postIssueRequest is the JSON body for POST /v1/folders/{folderId}/issues.
type postIssueRequest struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// PostIssueHandler handles POST /v1/folders/{folderId}/issues.
func PostIssueHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _, ok := resolveUser(w, r, deps)
		if !ok {
			return
		}
		folderID, ok2 := parseFolderID(w, r)
		if !ok2 {
			return
		}

		role, err := ProjectRole(r.Context(), deps.DB, userID, folderID)
		if writeErrStatus(w, err) {
			return
		}
		if err := RequireRole(role, "editor"); writeErrStatus(w, err) {
			return
		}

		var req postIssueRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if !issueIDPattern.MatchString(req.ID) {
			writeError(w, http.StatusBadRequest, "invalid issue id: must match ^\\d{4}$")
			return
		}

		path := req.ID + ".md"
		_, exists, err := storage.FileExists(r.Context(), deps.DB, folderID, path)
		if err != nil {
			slog.Error("post issue: file exists check", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if exists {
			writeError(w, http.StatusConflict, "issue already exists")
			return
		}

		bodyBytes := []byte(req.Body)
		newETag := computeETag(bodyBytes)
		s3Key := "projects/" + folderID.String() + "/issues/" + req.ID + ".md"
		const ct = "text/markdown; charset=utf-8"

		if err := storage.PutObject(r.Context(), deps.S3, deps.S3Bucket, s3Key, bodyBytes, ct, newETag); err != nil {
			slog.Error("post issue: s3 put", "err", err, "key", s3Key)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// ifMatchEtag="" → insert-only path (If-None-Match: * semantics).
		if err := storage.UpsertFile(r.Context(), deps.DB, folderID, path, newETag, int64(len(bodyBytes)), ct, userID, ""); err != nil {
			if errors.Is(err, storage.ErrETagConflict) {
				writeError(w, http.StatusConflict, "issue already exists")
			} else {
				slog.Error("post issue: upsert file", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		if deps.Cache != nil {
			deps.Cache.Delete(s3Key)
		}

		_ = storage.WriteAuditLog(r.Context(), deps.DB, userID, "post_issue",
			folderID.String(), path, "", newETag, clientIP(r), r.UserAgent())

		writeETagJSON(w, http.StatusCreated, newETag)
	}
}

// DeleteIssueHandler handles DELETE /v1/folders/{folderId}/issues/{id}.
func DeleteIssueHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _, ok := resolveUser(w, r, deps)
		if !ok {
			return
		}
		folderID, ok2 := parseFolderID(w, r)
		if !ok2 {
			return
		}
		issueID := r.PathValue("id")
		if !issueIDPattern.MatchString(issueID) {
			writeError(w, http.StatusBadRequest, "invalid issue id: must match ^\\d{4}$")
			return
		}

		role, err := ProjectRole(r.Context(), deps.DB, userID, folderID)
		if writeErrStatus(w, err) {
			return
		}
		if err := RequireRole(role, "editor"); writeErrStatus(w, err) {
			return
		}

		ifMatch := r.Header.Get("If-Match")
		if ifMatch == "" {
			writeError(w, http.StatusPreconditionRequired, "If-Match required")
			return
		}

		path := issueID + ".md"
		s3Key := "projects/" + folderID.String() + "/issues/" + issueID + ".md"

		if err := storage.DeleteFile(r.Context(), deps.DB, folderID, path, ifMatch); err != nil {
			if errors.Is(err, storage.ErrETagConflict) {
				writeError(w, http.StatusConflict, "etag conflict")
			} else if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "issue not found")
			} else {
				slog.Error("delete issue: delete file", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		_ = storage.DeleteS3Object(r.Context(), deps.S3, deps.S3Bucket, s3Key)

		if deps.Cache != nil {
			deps.Cache.Delete(s3Key)
		}

		_ = storage.WriteAuditLog(r.Context(), deps.DB, userID, "delete_issue",
			folderID.String(), path, ifMatch, "", clientIP(r), r.UserAgent())

		w.WriteHeader(http.StatusNoContent)
	}
}

// PutAttachmentHandler handles PUT /v1/folders/{folderId}/issues/{id}/attachments/{name}.
func PutAttachmentHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _, ok := resolveUser(w, r, deps)
		if !ok {
			return
		}
		folderID, ok2 := parseFolderID(w, r)
		if !ok2 {
			return
		}
		issueID := r.PathValue("id")
		if !issueIDPattern.MatchString(issueID) {
			writeError(w, http.StatusBadRequest, "invalid issue id: must match ^\\d{4}$")
			return
		}
		name := r.PathValue("name")
		if name == "" {
			writeError(w, http.StatusBadRequest, "missing attachment name")
			return
		}

		role, err := ProjectRole(r.Context(), deps.DB, userID, folderID)
		if writeErrStatus(w, err) {
			return
		}
		if err := RequireRole(role, "editor"); writeErrStatus(w, err) {
			return
		}

		ifMatch := r.Header.Get("If-Match")
		ifNoneMatch := r.Header.Get("If-None-Match")
		isCreate := ifNoneMatch == "*"

		if !isCreate && ifMatch == "" {
			writeError(w, http.StatusPreconditionRequired, "If-Match or If-None-Match: * required")
			return
		}

		body, ok3 := readBody(w, r)
		if !ok3 {
			return
		}

		newETag := computeETag(body)
		ct := detectContentType(body)
		path := issueID + "/" + name
		s3Key := "projects/" + folderID.String() + "/issues/" + issueID + "/" + name

		oldETag, _, _ := storage.FileExists(r.Context(), deps.DB, folderID, path)

		if err := storage.PutObject(r.Context(), deps.S3, deps.S3Bucket, s3Key, body, ct, newETag); err != nil {
			slog.Error("put attachment: s3 put", "err", err, "key", s3Key)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		var matchEtag string
		if !isCreate {
			matchEtag = ifMatch
		}
		if err := storage.UpsertFile(r.Context(), deps.DB, folderID, path, newETag, int64(len(body)), ct, userID, matchEtag); err != nil {
			if errors.Is(err, storage.ErrETagConflict) {
				writeError(w, http.StatusConflict, "etag conflict")
			} else {
				slog.Error("put attachment: upsert file", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		if deps.Cache != nil {
			deps.Cache.Delete(s3Key)
		}

		_ = storage.WriteAuditLog(r.Context(), deps.DB, userID, "put_attachment",
			folderID.String(), path, oldETag, newETag, clientIP(r), r.UserAgent())

		status := http.StatusOK
		if isCreate {
			status = http.StatusCreated
		}
		writeETagJSON(w, status, newETag)
	}
}

// DeleteAttachmentHandler handles DELETE /v1/folders/{folderId}/issues/{id}/attachments/{name}.
func DeleteAttachmentHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _, ok := resolveUser(w, r, deps)
		if !ok {
			return
		}
		folderID, ok2 := parseFolderID(w, r)
		if !ok2 {
			return
		}
		issueID := r.PathValue("id")
		if !issueIDPattern.MatchString(issueID) {
			writeError(w, http.StatusBadRequest, "invalid issue id: must match ^\\d{4}$")
			return
		}
		name := r.PathValue("name")
		if name == "" {
			writeError(w, http.StatusBadRequest, "missing attachment name")
			return
		}

		role, err := ProjectRole(r.Context(), deps.DB, userID, folderID)
		if writeErrStatus(w, err) {
			return
		}
		if err := RequireRole(role, "editor"); writeErrStatus(w, err) {
			return
		}

		path := issueID + "/" + name
		s3Key := "projects/" + folderID.String() + "/issues/" + issueID + "/" + name

		// No ETag check required for attachment deletes per spec.
		// We still need to delete the DB row; use a blank etag to skip the check.
		// Use a direct delete without etag check.
		_, err = deps.DB.Exec(r.Context(),
			`DELETE FROM files WHERE project_id=$1 AND path=$2`,
			folderID, path,
		)
		if err != nil {
			slog.Error("delete attachment: delete file row", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		_ = storage.DeleteS3Object(r.Context(), deps.S3, deps.S3Bucket, s3Key)

		if deps.Cache != nil {
			deps.Cache.Delete(s3Key)
		}

		_ = storage.WriteAuditLog(r.Context(), deps.DB, userID, "delete_attachment",
			folderID.String(), path, "", "", clientIP(r), r.UserAgent())

		w.WriteHeader(http.StatusNoContent)
	}
}

// PutProjectJSONHandler handles PUT /v1/folders/{folderId}/project.json.
func PutProjectJSONHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _, ok := resolveUser(w, r, deps)
		if !ok {
			return
		}
		folderID, ok2 := parseFolderID(w, r)
		if !ok2 {
			return
		}

		role, err := ProjectRole(r.Context(), deps.DB, userID, folderID)
		if writeErrStatus(w, err) {
			return
		}
		if err := RequireRole(role, "admin"); writeErrStatus(w, err) {
			return
		}

		ifMatch := r.Header.Get("If-Match")
		ifNoneMatch := r.Header.Get("If-None-Match")
		isCreate := ifNoneMatch == "*"

		if !isCreate && ifMatch == "" {
			writeError(w, http.StatusPreconditionRequired, "If-Match or If-None-Match: * required")
			return
		}

		body, ok3 := readBody(w, r)
		if !ok3 {
			return
		}

		newETag := computeETag(body)
		const ct = "application/json"
		const path = "project.json"
		s3Key := "projects/" + folderID.String() + "/project.json"

		oldETag, _, _ := storage.FileExists(r.Context(), deps.DB, folderID, path)

		if err := storage.PutObject(r.Context(), deps.S3, deps.S3Bucket, s3Key, body, ct, newETag); err != nil {
			slog.Error("put project.json: s3 put", "err", err, "key", s3Key)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		var matchEtag string
		if !isCreate {
			matchEtag = ifMatch
		}
		if err := storage.UpsertFile(r.Context(), deps.DB, folderID, path, newETag, int64(len(body)), ct, userID, matchEtag); err != nil {
			if errors.Is(err, storage.ErrETagConflict) {
				writeError(w, http.StatusConflict, "etag conflict")
			} else {
				slog.Error("put project.json: upsert file", "err", err)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
			return
		}

		if deps.Cache != nil {
			deps.Cache.Delete(s3Key)
		}

		_ = storage.WriteAuditLog(r.Context(), deps.DB, userID, "put_project_json",
			folderID.String(), path, oldETag, newETag, clientIP(r), r.UserAgent())

		writeETagJSON(w, http.StatusOK, newETag)
	}
}
