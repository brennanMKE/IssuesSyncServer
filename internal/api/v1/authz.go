package v1

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sync.sstools.co/internal/storage"
)

// ErrForbidden is returned when the user lacks the required role.
var ErrForbidden = errors.New("forbidden")

// roleRank maps role names to a numeric rank for comparison.
// Higher rank = more permissions.
var roleRank = map[string]int{
	"viewer": 1,
	"editor": 2,
	"admin":  3,
}

// ProjectRole returns the authenticated user's role in the given project.
// Returns ("admin", nil) for global admins regardless of project membership.
// Returns ("", ErrForbidden) if the user has no access to the project.
func ProjectRole(ctx context.Context, db *pgxpool.Pool, userID, projectID uuid.UUID) (string, error) {
	role, err := storage.ProjectRole(ctx, db, userID, projectID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrForbidden
		}
		return "", err
	}
	return role, nil
}

// RequireRole returns an http.StatusForbidden error (as a sentinel) when the
// user's role rank is below the minimum required, and nil otherwise.
// Role hierarchy: viewer < editor < admin.
func RequireRole(userRole, minRole string) error {
	if roleRank[userRole] >= roleRank[minRole] {
		return nil
	}
	return ErrForbidden
}

// writeErrStatus maps known sentinel errors to HTTP status codes and writes the
// appropriate JSON error response. Returns true if an error was written.
func writeErrStatus(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, storage.ErrETagConflict):
		writeError(w, http.StatusConflict, "etag conflict")
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not found")
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
	return true
}
