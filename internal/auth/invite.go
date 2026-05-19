package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InviteClaims holds the validated payload from a redeemed invite token.
type InviteClaims struct {
	Email      string
	Role       string
	ProjectIDs []string
}

// inviteJWTClaims is the internal JWT claims structure for invite tokens.
type inviteJWTClaims struct {
	jwt.RegisteredClaims
	Email      string   `json:"email"`
	Role       string   `json:"role"`
	ProjectIDs []string `json:"project_ids"`
}

// MintInviteToken signs a JWT invite token, stores the hash in the invites
// table, and returns the raw token string.
func MintInviteToken(ctx context.Context, db *pgxpool.Pool, createdByUserID uuid.UUID, email, role string, projectIDs []string, key []byte) (string, error) {
	return MintInviteTokenWithDuration(ctx, db, createdByUserID, email, role, projectIDs, key, 7*24*time.Hour)
}

// MintInviteTokenWithDuration is like MintInviteToken but accepts a custom
// expiry duration (used by Bootstrap for 24-hour admin enrollment tokens).
func MintInviteTokenWithDuration(ctx context.Context, db *pgxpool.Pool, createdByUserID uuid.UUID, email, role string, projectIDs []string, key []byte, dur time.Duration) (string, error) {
	now := time.Now().UTC()
	jti := uuid.New().String()

	if projectIDs == nil {
		projectIDs = []string{}
	}

	claims := inviteJWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(dur)),
		},
		Email:      email,
		Role:       role,
		ProjectIDs: projectIDs,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	rawToken, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("invite: sign token: %w", err)
	}

	tokenHash := inviteHash(rawToken)

	_, err = db.Exec(ctx,
		`INSERT INTO invites (email, role, project_ids, token_hash, created_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		email,
		role,
		uuidSliceFromStrings(projectIDs),
		tokenHash,
		createdByUserID,
		now.Add(dur),
	)
	if err != nil {
		return "", fmt.Errorf("invite: insert: %w", err)
	}

	return rawToken, nil
}

// RedeemInviteToken validates the JWT invite token, verifies the DB row is
// unused and unexpired, marks it used, and returns the claims.
func RedeemInviteToken(ctx context.Context, db *pgxpool.Pool, tokenStr string, key []byte) (*InviteClaims, error) {
	var claims inviteJWTClaims
	_, err := jwt.ParseWithClaims(tokenStr, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return nil, fmt.Errorf("invite: parse token: %w", err)
	}

	tokenHash := inviteHash(tokenStr)

	var usedAt *time.Time
	var expiresAt time.Time
	err = db.QueryRow(ctx,
		`SELECT used_at, expires_at FROM invites WHERE token_hash = $1`,
		tokenHash,
	).Scan(&usedAt, &expiresAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("invite: not found")
		}
		return nil, fmt.Errorf("invite: query: %w", err)
	}

	if usedAt != nil {
		return nil, fmt.Errorf("invite: already used")
	}
	if time.Now().UTC().After(expiresAt) {
		return nil, fmt.Errorf("invite: expired")
	}

	// Mark used.
	_, err = db.Exec(ctx,
		`UPDATE invites SET used_at = now() WHERE token_hash = $1`,
		tokenHash,
	)
	if err != nil {
		return nil, fmt.Errorf("invite: mark used: %w", err)
	}

	return &InviteClaims{
		Email:      claims.Email,
		Role:       claims.Role,
		ProjectIDs: claims.ProjectIDs,
	}, nil
}

// inviteHash returns the SHA-256 of the raw JWT token string.
func inviteHash(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// uuidSliceFromStrings converts []string to []uuid.UUID, ignoring parse errors.
func uuidSliceFromStrings(ss []string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ss))
	for _, s := range ss {
		if id, err := uuid.Parse(s); err == nil {
			out = append(out, id)
		}
	}
	return out
}
