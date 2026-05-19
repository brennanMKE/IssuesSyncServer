package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync.sstools.co/internal/wire"
)

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
