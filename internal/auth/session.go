package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenPair holds the short-lived JWT access token and the long-lived opaque
// refresh token for the native app session.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
}

const (
	accessTokenDuration  = 15 * time.Minute
	refreshTokenDuration = 30 * 24 * time.Hour
	webSessionDuration   = 7 * 24 * time.Hour
	refreshGraceWindow   = 30 * time.Second
)

// IssueNativeSession creates a new native (mobile/desktop) app session.
// It signs a 15-min JWT, generates a random opaque refresh token, stores the
// hashed refresh token in the sessions table, and returns the raw token pair.
func IssueNativeSession(ctx context.Context, db *pgxpool.Pool, userID, clientLabel string, jwtKey []byte) (TokenPair, error) {
	now := time.Now().UTC()

	// Sign JWT access token.
	claims := jwt.MapClaims{
		"sub":  userID,
		"iat":  now.Unix(),
		"exp":  now.Add(accessTokenDuration).Unix(),
		"kind": "native",
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	accessToken, err := token.SignedString(jwtKey)
	if err != nil {
		return TokenPair{}, fmt.Errorf("session: sign jwt: %w", err)
	}

	// Generate random refresh token.
	rawRefresh, err := randBytes(32)
	if err != nil {
		return TokenPair{}, fmt.Errorf("session: generate refresh token: %w", err)
	}
	refreshToken := hex.EncodeToString(rawRefresh)

	// Hash the refresh token for storage.
	refreshHash := hashToken(refreshToken)

	_, err = db.Exec(ctx,
		`INSERT INTO sessions (user_id, kind, refresh_token_hash, client_label, expires_at)
		 VALUES ($1, 'native', $2, $3, $4)`,
		userID,
		refreshHash,
		clientLabel,
		now.Add(refreshTokenDuration),
	)
	if err != nil {
		return TokenPair{}, fmt.Errorf("session: insert session: %w", err)
	}

	return TokenPair{AccessToken: accessToken, RefreshToken: refreshToken}, nil
}

// IssueWebSession creates a new web admin session. Returns the raw (unhashed)
// session ID suitable for placing in a cookie.
func IssueWebSession(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID) (string, error) {
	now := time.Now().UTC()

	rawID, err := randBytes(32)
	if err != nil {
		return "", fmt.Errorf("session: generate session id: %w", err)
	}
	sessionID := hex.EncodeToString(rawID)
	sessionHash := hashToken(sessionID)

	_, err = db.Exec(ctx,
		`INSERT INTO sessions (user_id, kind, refresh_token_hash, expires_at)
		 VALUES ($1, 'web', $2, $3)`,
		userID,
		sessionHash,
		now.Add(webSessionDuration),
	)
	if err != nil {
		return "", fmt.Errorf("session: insert web session: %w", err)
	}

	return sessionID, nil
}

// ValidateAccessToken validates a JWT access token and returns the subject
// (userID) claim.
func ValidateAccessToken(tokenStr string, jwtKey []byte) (string, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return jwtKey, nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return "", fmt.Errorf("session: parse token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", fmt.Errorf("session: invalid token claims")
	}

	sub, err := claims.GetSubject()
	if err != nil {
		return "", fmt.Errorf("session: missing sub claim: %w", err)
	}
	return sub, nil
}

// ValidateWebSession looks up a web session by cookie value, verifies it is
// not expired or revoked, and returns the owning userID.
func ValidateWebSession(ctx context.Context, db *pgxpool.Pool, sessionID string) (string, error) {
	hash := hashToken(sessionID)

	var userID string
	var expiresAt time.Time
	var revokedAt *time.Time

	err := db.QueryRow(ctx,
		`SELECT user_id, expires_at, revoked_at FROM sessions
		 WHERE refresh_token_hash = $1 AND kind = 'web'`,
		hash,
	).Scan(&userID, &expiresAt, &revokedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("session: not found")
		}
		return "", fmt.Errorf("session: query: %w", err)
	}

	if revokedAt != nil {
		return "", fmt.Errorf("session: revoked")
	}
	if time.Now().UTC().After(expiresAt) {
		return "", fmt.Errorf("session: expired")
	}

	// Touch last_seen_at.
	_, _ = db.Exec(ctx,
		`UPDATE sessions SET last_seen_at = now() WHERE refresh_token_hash = $1`,
		hash,
	)

	return userID, nil
}

// RotateRefreshToken exchanges an old refresh token for a new TokenPair.
// A 30-second grace window allows racing requests to re-use the just-issued
// pair instead of triggering a revocation.
func RotateRefreshToken(ctx context.Context, db *pgxpool.Pool, oldRefreshToken string, jwtKey []byte) (TokenPair, error) {
	oldHash := hashToken(oldRefreshToken)

	var sessionID uuid.UUID
	var userID string
	var expiresAt, lastSeenAt time.Time
	var revokedAt *time.Time

	err := db.QueryRow(ctx,
		`SELECT id, user_id, expires_at, last_seen_at, revoked_at FROM sessions
		 WHERE refresh_token_hash = $1 AND kind = 'native'`,
		oldHash,
	).Scan(&sessionID, &userID, &expiresAt, &lastSeenAt, &revokedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return TokenPair{}, fmt.Errorf("session: refresh token not found")
		}
		return TokenPair{}, fmt.Errorf("session: query: %w", err)
	}

	if revokedAt != nil {
		return TokenPair{}, fmt.Errorf("session: token revoked")
	}
	if time.Now().UTC().After(expiresAt) {
		return TokenPair{}, fmt.Errorf("session: token expired")
	}

	// 30-second grace: if last_seen_at is very recent this is a racing
	// duplicate request — reject gracefully; caller should retry with the
	// token it just received.
	if time.Since(lastSeenAt) < refreshGraceWindow {
		return TokenPair{}, fmt.Errorf("session: rotation too soon, retry")
	}

	// Generate new tokens.
	rawRefresh, err := randBytes(32)
	if err != nil {
		return TokenPair{}, fmt.Errorf("session: generate refresh token: %w", err)
	}
	newRefreshToken := hex.EncodeToString(rawRefresh)
	newRefreshHash := hashToken(newRefreshToken)

	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":  userID,
		"iat":  now.Unix(),
		"exp":  now.Add(accessTokenDuration).Unix(),
		"kind": "native",
	}
	jwtToken := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	accessToken, err := jwtToken.SignedString(jwtKey)
	if err != nil {
		return TokenPair{}, fmt.Errorf("session: sign jwt: %w", err)
	}

	// Replace the refresh hash and update timestamps in one statement.
	_, err = db.Exec(ctx,
		`UPDATE sessions
		 SET refresh_token_hash = $1,
		     last_seen_at = now(),
		     expires_at   = $2
		 WHERE id = $3`,
		newRefreshHash,
		now.Add(refreshTokenDuration),
		sessionID,
	)
	if err != nil {
		return TokenPair{}, fmt.Errorf("session: update session: %w", err)
	}

	return TokenPair{AccessToken: accessToken, RefreshToken: newRefreshToken}, nil
}

// hashToken returns the SHA-256 of a raw token string as a byte slice.
func hashToken(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}
