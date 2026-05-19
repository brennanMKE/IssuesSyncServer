package storage

import (
	"time"

	"github.com/google/uuid"
)

// UserRow represents a row from the users table.
type UserRow struct {
	ID          uuid.UUID
	Email       string
	DisplayName string
	GlobalRole  string
	Status      string
	CreatedAt   time.Time
}

// UserDetail holds a user row plus their associated passkeys and sessions.
type UserDetail struct {
	User     UserRow
	Passkeys []PasskeyRow
	Sessions []SessionRow
}

// PasskeyRow represents a row from the passkeys table.
type PasskeyRow struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	Label      string
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

// SessionRow represents a row from the sessions table.
type SessionRow struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	Kind        string
	ClientLabel string
	CreatedAt   time.Time
	LastSeenAt  *time.Time
	ExpiresAt   time.Time
	RevokedAt   *time.Time
}

// ProjectRow represents a row from the projects table.
type ProjectRow struct {
	ID          uuid.UUID
	Slug        string
	DisplayName string
	RepoURL     string
	CreatedBy   uuid.UUID
	CreatedAt   time.Time
	ArchivedAt  *time.Time
}

// MemberRow represents a row from the project_members table with user info.
type MemberRow struct {
	UserID      uuid.UUID
	Email       string
	DisplayName string
	Role        string
}

// FileRow represents a row from the files table.
type FileRow struct {
	ProjectID   uuid.UUID
	Path        string
	ETag        string
	Size        int64
	ContentType string
	ModifiedAt  time.Time
	ModifiedBy  uuid.UUID
}

// AuditRow represents a row from the audit_log table.
type AuditRow struct {
	ID         int64
	ActorID    *uuid.UUID
	ActorEmail string
	Action     string
	ProjectID  *uuid.UUID
	ProjectSlug string
	Path       string
	ETagBefore string
	ETagAfter  string
	IP         string
	UserAgent  string
	CreatedAt  time.Time
}

// AuditFilter specifies optional filters for the audit log query.
type AuditFilter struct {
	UserID    string
	ProjectID string
	Action    string
	Page      int
	PageSize  int
}
