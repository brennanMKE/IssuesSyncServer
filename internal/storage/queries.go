package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync.sstools.co/internal/wire"
)

// ErrETagConflict is returned when an optimistic concurrency check fails.
var ErrETagConflict = errors.New("storage: etag conflict")

// ProjectsForUser returns FolderInfo for all projects the user is a member of
// (or all projects if global admin).
func ProjectsForUser(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID, isAdmin bool) ([]wire.FolderInfo, error) {
	var rows pgx.Rows
	var err error

	if isAdmin {
		rows, err = db.Query(ctx,
			`SELECT id, slug, display_name, repo_url
			 FROM projects
			 WHERE archived_at IS NULL
			 ORDER BY display_name`,
		)
	} else {
		rows, err = db.Query(ctx,
			`SELECT p.id, p.slug, p.display_name, p.repo_url
			 FROM projects p
			 JOIN project_members pm ON pm.project_id = p.id
			 WHERE pm.user_id = $1 AND p.archived_at IS NULL
			 ORDER BY p.display_name`,
			userID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("storage: projects for user: %w", err)
	}
	defer rows.Close()

	var results []wire.FolderInfo
	for rows.Next() {
		var f wire.FolderInfo
		var id uuid.UUID
		if err := rows.Scan(&id, &f.Slug, &f.DisplayName, &f.RepoURL); err != nil {
			return nil, fmt.Errorf("storage: scan project row: %w", err)
		}
		f.ID = id.String()
		results = append(results, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate projects: %w", err)
	}

	if results == nil {
		results = []wire.FolderInfo{}
	}
	return results, nil
}

// ProjectByID returns a single FolderInfo, checking membership for non-admins.
// Returns pgx.ErrNoRows if not found or user is not a member.
func ProjectByID(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID, isAdmin bool, projectID uuid.UUID) (*wire.FolderInfo, error) {
	var f wire.FolderInfo
	var id uuid.UUID
	var err error

	if isAdmin {
		err = db.QueryRow(ctx,
			`SELECT id, slug, display_name, repo_url
			 FROM projects
			 WHERE id = $1 AND archived_at IS NULL`,
			projectID,
		).Scan(&id, &f.Slug, &f.DisplayName, &f.RepoURL)
	} else {
		err = db.QueryRow(ctx,
			`SELECT p.id, p.slug, p.display_name, p.repo_url
			 FROM projects p
			 JOIN project_members pm ON pm.project_id = p.id
			 WHERE p.id = $1 AND pm.user_id = $2 AND p.archived_at IS NULL`,
			projectID,
			userID,
		).Scan(&id, &f.Slug, &f.DisplayName, &f.RepoURL)
	}
	if err != nil {
		return nil, fmt.Errorf("storage: project by id: %w", err)
	}

	f.ID = id.String()
	return &f, nil
}

// IssuesForProject returns IssueMetadata list for a project.
// Only rows whose path matches the pattern NNNN.md are included.
func IssuesForProject(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID) ([]wire.IssueMetadata, error) {
	rows, err := db.Query(ctx,
		`SELECT path, etag FROM files
		 WHERE project_id = $1 AND path ~ '^[0-9]{4}\.md$'
		 ORDER BY path`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: issues for project: %w", err)
	}
	defer rows.Close()

	var results []wire.IssueMetadata
	for rows.Next() {
		var path, etag string
		if err := rows.Scan(&path, &etag); err != nil {
			return nil, fmt.Errorf("storage: scan issue row: %w", err)
		}
		issueID := strings.TrimSuffix(path, ".md")
		etagCopy := etag
		results = append(results, wire.IssueMetadata{
			ID:   issueID,
			ETag: &etagCopy,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate issues: %w", err)
	}

	if results == nil {
		results = []wire.IssueMetadata{}
	}
	return results, nil
}

// IssueByID returns the S3 key and etag for a single issue file.
func IssueByID(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID, issueID string) (s3Key string, etag string, err error) {
	s3Key = fmt.Sprintf("projects/%s/issues/%s.md", projectID.String(), issueID)

	path := issueID + ".md"
	err = db.QueryRow(ctx,
		`SELECT etag FROM files WHERE project_id = $1 AND path = $2`,
		projectID,
		path,
	).Scan(&etag)
	if err != nil {
		return "", "", fmt.Errorf("storage: issue by id: %w", err)
	}
	return s3Key, etag, nil
}

// AttachmentByPath returns the S3 key and content type for an attachment.
func AttachmentByPath(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID, issueID, name string) (s3Key string, contentType string, err error) {
	s3Key = fmt.Sprintf("projects/%s/issues/%s/%s", projectID.String(), issueID, name)

	path := issueID + "/" + name
	err = db.QueryRow(ctx,
		`SELECT content_type FROM files WHERE project_id = $1 AND path = $2`,
		projectID,
		path,
	).Scan(&contentType)
	if err != nil {
		return "", "", fmt.Errorf("storage: attachment by path: %w", err)
	}
	return s3Key, contentType, nil
}

// UserByID returns the user's global_role. Used to determine isAdmin.
func UserByID(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID) (globalRole string, err error) {
	err = db.QueryRow(ctx,
		`SELECT global_role FROM users WHERE id = $1`,
		userID,
	).Scan(&globalRole)
	if err != nil {
		return "", fmt.Errorf("storage: user by id: %w", err)
	}
	return globalRole, nil
}

// ProjectRole returns the role of userID in the given project.
// Global admins always get "admin". Returns ("", pgx.ErrNoRows) if not a member.
func ProjectRole(ctx context.Context, db *pgxpool.Pool, userID, projectID uuid.UUID) (role string, err error) {
	// Check global admin first.
	var globalRole string
	err = db.QueryRow(ctx,
		`SELECT global_role FROM users WHERE id = $1`,
		userID,
	).Scan(&globalRole)
	if err != nil {
		return "", fmt.Errorf("storage: project role lookup user: %w", err)
	}
	if globalRole == "admin" {
		return "admin", nil
	}

	// Look up per-project role.
	err = db.QueryRow(ctx,
		`SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID,
		userID,
	).Scan(&role)
	if err != nil {
		return "", fmt.Errorf("storage: project role: %w", err)
	}
	return role, nil
}

// UpsertFile inserts or updates a file row with ETag concurrency checking.
//
// If ifMatchEtag is non-empty it is treated as an If-Match update: the row
// must already exist with that exact ETag, otherwise ErrETagConflict is returned.
//
// If ifMatchEtag is empty (If-None-Match: * create) the row must not exist;
// if it already exists ErrETagConflict is returned.
func UpsertFile(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID, path, etag string, size int64, contentType string, modifiedBy uuid.UUID, ifMatchEtag string) error {
	if ifMatchEtag != "" {
		// Update-existing path: atomically replace the row only if ETag matches.
		tag, err := db.Exec(ctx,
			`UPDATE files
			 SET etag=$3, size=$4, content_type=$5, modified_at=now(), modified_by=$6
			 WHERE project_id=$1 AND path=$2 AND etag=$7`,
			projectID, path, etag, size, contentType, modifiedBy, ifMatchEtag,
		)
		if err != nil {
			return fmt.Errorf("storage: upsert file update: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrETagConflict
		}
		return nil
	}

	// Create-new path (If-None-Match: *): insert, skip if already exists.
	tag, err := db.Exec(ctx,
		`INSERT INTO files (project_id, path, etag, size, content_type, modified_by)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (project_id, path) DO NOTHING`,
		projectID, path, etag, size, contentType, modifiedBy,
	)
	if err != nil {
		return fmt.Errorf("storage: upsert file insert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrETagConflict
	}
	return nil
}

// DeleteFile removes a file row after verifying the ETag matches.
// Returns ErrETagConflict if the row exists but the ETag does not match.
// Returns pgx.ErrNoRows if the row does not exist.
func DeleteFile(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID, path, ifMatchEtag string) error {
	tag, err := db.Exec(ctx,
		`DELETE FROM files WHERE project_id=$1 AND path=$2 AND etag=$3`,
		projectID, path, ifMatchEtag,
	)
	if err != nil {
		return fmt.Errorf("storage: delete file: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Distinguish: does the row exist at all?
		var exists bool
		_ = db.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM files WHERE project_id=$1 AND path=$2)`,
			projectID, path,
		).Scan(&exists)
		if !exists {
			return pgx.ErrNoRows
		}
		return ErrETagConflict
	}
	return nil
}

// FileExists checks whether a file row exists for the given project and path.
// Returns (etag, true, nil) if found, ("", false, nil) if not found.
func FileExists(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID, path string) (etag string, exists bool, err error) {
	err = db.QueryRow(ctx,
		`SELECT etag FROM files WHERE project_id=$1 AND path=$2`,
		projectID, path,
	).Scan(&etag)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("storage: file exists: %w", err)
	}
	return etag, true, nil
}

// WriteAuditLog appends one row to the audit_log table.
func WriteAuditLog(ctx context.Context, db *pgxpool.Pool, actor uuid.UUID, action, projectID, path, etagBefore, etagAfter, ip, userAgent string) error {
	var projID *uuid.UUID
	if projectID != "" {
		pid, err := uuid.Parse(projectID)
		if err == nil {
			projID = &pid
		}
	}
	_, err := db.Exec(ctx,
		`INSERT INTO audit_log (actor, action, project_id, path, etag_before, etag_after, ip, user_agent, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7::inet, $8, now())`,
		actor, action, projID, path, nullStr(etagBefore), nullStr(etagAfter), nullStr(ip), userAgent,
	)
	if err != nil {
		return fmt.Errorf("storage: write audit log: %w", err)
	}
	return nil
}

// nullStr converts an empty string to nil so pgx stores NULL rather than an empty string.
func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
