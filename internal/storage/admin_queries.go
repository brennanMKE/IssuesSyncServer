package storage

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// hashTokenBytes returns the SHA-256 of a raw token string as a byte slice.
func hashTokenBytes(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// AllUsers returns all user rows ordered by email.
func AllUsers(ctx context.Context, db *pgxpool.Pool) ([]UserRow, error) {
	rows, err := db.Query(ctx,
		`SELECT id, email, display_name, global_role, status, created_at
		 FROM users
		 ORDER BY email`,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: all users: %w", err)
	}
	defer rows.Close()

	var results []UserRow
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.GlobalRole, &u.Status, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan user: %w", err)
		}
		results = append(results, u)
	}
	return results, rows.Err()
}

// UserWithPasskeysAndSessions returns full detail for a single user.
func UserWithPasskeysAndSessions(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID) (*UserDetail, error) {
	var u UserRow
	err := db.QueryRow(ctx,
		`SELECT id, email, display_name, global_role, status, created_at
		 FROM users WHERE id = $1`,
		userID,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.GlobalRole, &u.Status, &u.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("storage: user detail: %w", err)
	}

	passkeys, err := UserPasskeys(ctx, db, userID)
	if err != nil {
		return nil, err
	}

	sessions, err := UserSessions(ctx, db, userID)
	if err != nil {
		return nil, err
	}

	return &UserDetail{User: u, Passkeys: passkeys, Sessions: sessions}, nil
}

// DeactivateUser sets a user's status to disabled.
func DeactivateUser(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID) error {
	_, err := db.Exec(ctx,
		`UPDATE users SET status = 'disabled', updated_at = now() WHERE id = $1`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("storage: deactivate user: %w", err)
	}
	return nil
}

// RevokeSession sets revoked_at = now() for the given session.
func RevokeSession(ctx context.Context, db *pgxpool.Pool, sessionID uuid.UUID) error {
	_, err := db.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE id = $1`,
		sessionID,
	)
	if err != nil {
		return fmt.Errorf("storage: revoke session: %w", err)
	}
	return nil
}

// AllProjects returns all project rows ordered by display_name.
func AllProjects(ctx context.Context, db *pgxpool.Pool) ([]ProjectRow, error) {
	rows, err := db.Query(ctx,
		`SELECT id, slug, display_name, repo_url, created_by, created_at, archived_at
		 FROM projects
		 ORDER BY display_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: all projects: %w", err)
	}
	defer rows.Close()

	var results []ProjectRow
	for rows.Next() {
		var p ProjectRow
		var repoURL *string
		if err := rows.Scan(&p.ID, &p.Slug, &p.DisplayName, &repoURL, &p.CreatedBy, &p.CreatedAt, &p.ArchivedAt); err != nil {
			return nil, fmt.Errorf("storage: scan project: %w", err)
		}
		if repoURL != nil {
			p.RepoURL = *repoURL
		}
		results = append(results, p)
	}
	return results, rows.Err()
}

// ProjectBySlug returns a single project row by slug.
func ProjectBySlug(ctx context.Context, db *pgxpool.Pool, slug string) (*ProjectRow, error) {
	var p ProjectRow
	var repoURL *string
	err := db.QueryRow(ctx,
		`SELECT id, slug, display_name, repo_url, created_by, created_at, archived_at
		 FROM projects WHERE slug = $1`,
		slug,
	).Scan(&p.ID, &p.Slug, &p.DisplayName, &repoURL, &p.CreatedBy, &p.CreatedAt, &p.ArchivedAt)
	if err != nil {
		return nil, fmt.Errorf("storage: project by slug: %w", err)
	}
	if repoURL != nil {
		p.RepoURL = *repoURL
	}
	return &p, nil
}

// CreateProject inserts a new project row.
func CreateProject(ctx context.Context, db *pgxpool.Pool, slug, displayName, repoURL string, createdBy uuid.UUID) error {
	_, err := db.Exec(ctx,
		`INSERT INTO projects (slug, display_name, repo_url, created_by)
		 VALUES ($1, $2, $3, $4)`,
		slug, displayName, nullStr(repoURL), createdBy,
	)
	if err != nil {
		return fmt.Errorf("storage: create project: %w", err)
	}
	return nil
}

// UpdateProject updates display_name and repo_url for the given project.
func UpdateProject(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID, displayName, repoURL string) error {
	_, err := db.Exec(ctx,
		`UPDATE projects SET display_name = $2, repo_url = $3 WHERE id = $1`,
		projectID, displayName, nullStr(repoURL),
	)
	if err != nil {
		return fmt.Errorf("storage: update project: %w", err)
	}
	return nil
}

// ArchiveProject sets archived_at = now() for the given project.
func ArchiveProject(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID) error {
	_, err := db.Exec(ctx,
		`UPDATE projects SET archived_at = now() WHERE id = $1`,
		projectID,
	)
	if err != nil {
		return fmt.Errorf("storage: archive project: %w", err)
	}
	return nil
}

// ProjectMembers returns all members of the given project with user info.
func ProjectMembers(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID) ([]MemberRow, error) {
	rows, err := db.Query(ctx,
		`SELECT u.id, u.email, u.display_name, pm.role
		 FROM project_members pm
		 JOIN users u ON u.id = pm.user_id
		 WHERE pm.project_id = $1
		 ORDER BY u.email`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: project members: %w", err)
	}
	defer rows.Close()

	var results []MemberRow
	for rows.Next() {
		var m MemberRow
		if err := rows.Scan(&m.UserID, &m.Email, &m.DisplayName, &m.Role); err != nil {
			return nil, fmt.Errorf("storage: scan member: %w", err)
		}
		results = append(results, m)
	}
	return results, rows.Err()
}

// AddProjectMember adds a user to a project with the given role.
func AddProjectMember(ctx context.Context, db *pgxpool.Pool, projectID, userID uuid.UUID, role string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO project_members (project_id, user_id, role)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		projectID, userID, role,
	)
	if err != nil {
		return fmt.Errorf("storage: add project member: %w", err)
	}
	return nil
}

// RemoveProjectMember removes a user from a project.
func RemoveProjectMember(ctx context.Context, db *pgxpool.Pool, projectID, userID uuid.UUID) error {
	_, err := db.Exec(ctx,
		`DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`,
		projectID, userID,
	)
	if err != nil {
		return fmt.Errorf("storage: remove project member: %w", err)
	}
	return nil
}

// ChangeProjectMemberRole updates a member's role in the given project.
func ChangeProjectMemberRole(ctx context.Context, db *pgxpool.Pool, projectID, userID uuid.UUID, role string) error {
	_, err := db.Exec(ctx,
		`UPDATE project_members SET role = $3
		 WHERE project_id = $1 AND user_id = $2`,
		projectID, userID, role,
	)
	if err != nil {
		return fmt.Errorf("storage: change project member role: %w", err)
	}
	return nil
}

// ProjectFiles returns all file rows for the given project ordered by path.
func ProjectFiles(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID) ([]FileRow, error) {
	rows, err := db.Query(ctx,
		`SELECT project_id, path, etag, size, content_type, modified_at, modified_by
		 FROM files
		 WHERE project_id = $1
		 ORDER BY path`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: project files: %w", err)
	}
	defer rows.Close()

	var results []FileRow
	for rows.Next() {
		var f FileRow
		var contentType *string
		if err := rows.Scan(&f.ProjectID, &f.Path, &f.ETag, &f.Size, &contentType, &f.ModifiedAt, &f.ModifiedBy); err != nil {
			return nil, fmt.Errorf("storage: scan file: %w", err)
		}
		if contentType != nil {
			f.ContentType = *contentType
		}
		results = append(results, f)
	}
	return results, rows.Err()
}

// AuditLog returns a paginated slice of audit rows plus total count.
func AuditLog(ctx context.Context, db *pgxpool.Pool, filter AuditFilter) ([]AuditRow, int, error) {
	if filter.PageSize <= 0 {
		filter.PageSize = 50
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}
	offset := (filter.Page - 1) * filter.PageSize

	// Build dynamic WHERE clause.
	where := "1=1"
	args := []interface{}{}
	argIdx := 1

	if filter.UserID != "" {
		where += fmt.Sprintf(" AND a.actor = $%d::uuid", argIdx)
		args = append(args, filter.UserID)
		argIdx++
	}
	if filter.ProjectID != "" {
		where += fmt.Sprintf(" AND a.project_id = $%d::uuid", argIdx)
		args = append(args, filter.ProjectID)
		argIdx++
	}
	if filter.Action != "" {
		where += fmt.Sprintf(" AND a.action = $%d", argIdx)
		args = append(args, filter.Action)
		argIdx++
	}

	// Count query.
	countQuery := fmt.Sprintf(`SELECT count(*) FROM audit_log a WHERE %s`, where)
	var total int
	if err := db.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("storage: audit log count: %w", err)
	}

	// Data query.
	dataArgs := append(args, filter.PageSize, offset)
	dataQuery := fmt.Sprintf(`
		SELECT a.id, a.actor, COALESCE(u.email,''), a.action,
		       a.project_id, COALESCE(p.slug,''),
		       COALESCE(a.path,''), COALESCE(a.etag_before,''), COALESCE(a.etag_after,''),
		       COALESCE(a.ip::text,''), COALESCE(a.user_agent,''), a.created_at
		FROM audit_log a
		LEFT JOIN users u ON u.id = a.actor
		LEFT JOIN projects p ON p.id = a.project_id
		WHERE %s
		ORDER BY a.id DESC
		LIMIT $%d OFFSET $%d`,
		where, argIdx, argIdx+1,
	)

	rows, err := db.Query(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("storage: audit log query: %w", err)
	}
	defer rows.Close()

	var results []AuditRow
	for rows.Next() {
		var row AuditRow
		if err := rows.Scan(
			&row.ID, &row.ActorID, &row.ActorEmail, &row.Action,
			&row.ProjectID, &row.ProjectSlug,
			&row.Path, &row.ETagBefore, &row.ETagAfter,
			&row.IP, &row.UserAgent, &row.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("storage: scan audit row: %w", err)
		}
		results = append(results, row)
	}
	return results, total, rows.Err()
}

// UserSessions returns all sessions for the given user, newest first.
func UserSessions(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID) ([]SessionRow, error) {
	rows, err := db.Query(ctx,
		`SELECT id, user_id, kind, COALESCE(client_label,''), created_at, last_seen_at, expires_at, revoked_at
		 FROM sessions
		 WHERE user_id = $1
		 ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: user sessions: %w", err)
	}
	defer rows.Close()

	var results []SessionRow
	for rows.Next() {
		var s SessionRow
		if err := rows.Scan(&s.ID, &s.UserID, &s.Kind, &s.ClientLabel, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.RevokedAt); err != nil {
			return nil, fmt.Errorf("storage: scan session: %w", err)
		}
		results = append(results, s)
	}
	return results, rows.Err()
}

// UserPasskeys returns all passkeys for the given user.
func UserPasskeys(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID) ([]PasskeyRow, error) {
	rows, err := db.Query(ctx,
		`SELECT id, user_id, COALESCE(label,''), last_used_at, created_at
		 FROM passkeys
		 WHERE user_id = $1
		 ORDER BY created_at`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: user passkeys: %w", err)
	}
	defer rows.Close()

	var results []PasskeyRow
	for rows.Next() {
		var p PasskeyRow
		if err := rows.Scan(&p.ID, &p.UserID, &p.Label, &p.LastUsedAt, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("storage: scan passkey: %w", err)
		}
		results = append(results, p)
	}
	return results, rows.Err()
}

// DeletePasskey removes a passkey row.
func DeletePasskey(ctx context.Context, db *pgxpool.Pool, passkeyID uuid.UUID) error {
	_, err := db.Exec(ctx,
		`DELETE FROM passkeys WHERE id = $1`,
		passkeyID,
	)
	if err != nil {
		return fmt.Errorf("storage: delete passkey: %w", err)
	}
	return nil
}

// RecentActivity returns the most recent audit_log rows, newest first.
func RecentActivity(ctx context.Context, db *pgxpool.Pool, limit int) ([]AuditRow, error) {
	rows, err := db.Query(ctx,
		`SELECT a.id, a.actor, COALESCE(u.email,''), a.action,
		        a.project_id, COALESCE(p.slug,''),
		        COALESCE(a.path,''), COALESCE(a.etag_before,''), COALESCE(a.etag_after,''),
		        COALESCE(a.ip::text,''), COALESCE(a.user_agent,''), a.created_at
		 FROM audit_log a
		 LEFT JOIN users u ON u.id = a.actor
		 LEFT JOIN projects p ON p.id = a.project_id
		 ORDER BY a.id DESC
		 LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("storage: recent activity: %w", err)
	}
	defer rows.Close()

	var results []AuditRow
	for rows.Next() {
		var row AuditRow
		if err := rows.Scan(
			&row.ID, &row.ActorID, &row.ActorEmail, &row.Action,
			&row.ProjectID, &row.ProjectSlug,
			&row.Path, &row.ETagBefore, &row.ETagAfter,
			&row.IP, &row.UserAgent, &row.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("storage: scan recent activity: %w", err)
		}
		results = append(results, row)
	}
	return results, rows.Err()
}

// ProjectCount returns the total number of non-archived projects.
func ProjectCount(ctx context.Context, db *pgxpool.Pool) (int, error) {
	var count int
	err := db.QueryRow(ctx, `SELECT count(*) FROM projects WHERE archived_at IS NULL`).Scan(&count)
	return count, err
}

// UserCount returns the total number of active users.
func UserCount(ctx context.Context, db *pgxpool.Pool) (int, error) {
	var count int
	err := db.QueryRow(ctx, `SELECT count(*) FROM users WHERE status = 'active'`).Scan(&count)
	return count, err
}

// SessionByToken returns the session ID and user ID for a given raw session cookie.
func SessionByToken(ctx context.Context, db *pgxpool.Pool, sessionToken string) (sessionID uuid.UUID, userID uuid.UUID, err error) {
	h := hashTokenBytes(sessionToken)
	err = db.QueryRow(ctx,
		`SELECT id, user_id FROM sessions WHERE refresh_token_hash = $1 AND kind = 'web' AND revoked_at IS NULL AND expires_at > now()`,
		h,
	).Scan(&sessionID, &userID)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("storage: session by token: %w", err)
	}
	return sessionID, userID, nil
}
