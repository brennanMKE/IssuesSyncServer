package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds the WebAuthn and auth configuration needed to build a Service.
type Config struct {
	RPID            string
	RPDisplayName   string
	RPOrigins       []string // typically []string{BaseURL}
	JWTKey          []byte
	InviteKey       []byte
}

// User implements webauthn.User so the library can work with our user records.
type User struct {
	ID          []byte
	Name        string // email address
	DisplayName string
	Credentials []webauthn.Credential
}

func (u *User) WebAuthnID() []byte                         { return u.ID }
func (u *User) WebAuthnName() string                       { return u.Name }
func (u *User) WebAuthnDisplayName() string                { return u.DisplayName }
func (u *User) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

// Service wraps the WebAuthn library and the DB pool for auth operations.
type Service struct {
	wn        *webauthn.WebAuthn
	db        *pgxpool.Pool
	jwtKey    []byte
	inviteKey []byte
}

// NewService initialises a Service from cfg. Returns an error if the WebAuthn
// library rejects the config.
func NewService(cfg Config, db *pgxpool.Pool) (*Service, error) {
	wn, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPDisplayName,
		RPOrigins:     cfg.RPOrigins,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: webauthn.New: %w", err)
	}
	return &Service{
		wn:        wn,
		db:        db,
		jwtKey:    cfg.JWTKey,
		inviteKey: cfg.InviteKey,
	}, nil
}

// BeginRegistration starts a new WebAuthn credential creation ceremony for the
// given user. The returned SessionData must be stored by the caller for the
// subsequent FinishRegistration call.
func (s *Service) BeginRegistration(ctx context.Context, userID uuid.UUID) (*protocol.CredentialCreation, *webauthn.SessionData, error) {
	u, err := s.loadUser(ctx, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: load user: %w", err)
	}

	creation, sess, err := s.wn.BeginRegistration(u)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: begin registration: %w", err)
	}
	return creation, sess, nil
}

// FinishRegistration completes a WebAuthn credential creation ceremony and
// persists the new passkey row in Postgres.
func (s *Service) FinishRegistration(ctx context.Context, userID uuid.UUID, sess *webauthn.SessionData, r *http.Request) error {
	u, err := s.loadUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("auth: load user: %w", err)
	}

	cred, err := s.wn.FinishRegistration(u, *sess, r)
	if err != nil {
		return fmt.Errorf("auth: finish registration: %w", err)
	}

	// Encode the public key as hex for storage.
	pubKeyHex := hex.EncodeToString(cred.PublicKey)
	credIDHex := hex.EncodeToString(cred.ID)
	_ = credIDHex // stored as bytea via cred.ID directly

	// Convert transports.
	transports := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		transports[i] = string(t)
	}

	_, err = s.db.Exec(ctx,
		`INSERT INTO passkeys (user_id, credential_id, public_key, sign_count, transports, label)
		 VALUES ($1, $2, decode($3, 'hex'), $4, $5, $6)`,
		userID,
		cred.ID,
		pubKeyHex,
		cred.Authenticator.SignCount,
		transports,
		"",
	)
	if err != nil {
		return fmt.Errorf("auth: insert passkey: %w", err)
	}

	slog.Info("passkey registered", "user_id", userID)
	return nil
}

// BeginAssertion starts a WebAuthn authentication ceremony for the user with
// the given email address.
func (s *Service) BeginAssertion(ctx context.Context, email string) (*protocol.CredentialAssertion, *webauthn.SessionData, error) {
	u, err := s.loadUserByEmail(ctx, email)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: load user by email: %w", err)
	}

	assertion, sess, err := s.wn.BeginLogin(u)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: begin assertion: %w", err)
	}
	return assertion, sess, nil
}

// FinishAssertion completes a WebAuthn authentication ceremony. It updates the
// sign_count and last_used_at for the matching passkey row and returns the
// authenticated User.
func (s *Service) FinishAssertion(ctx context.Context, sess *webauthn.SessionData, r *http.Request) (*User, error) {
	// We need to identify the user from the session's UserID (which is the raw
	// UUID bytes we stored).
	userUUID, err := uuid.FromBytes(sess.UserID)
	if err != nil {
		return nil, fmt.Errorf("auth: parse session user id: %w", err)
	}

	u, err := s.loadUser(ctx, userUUID)
	if err != nil {
		return nil, fmt.Errorf("auth: load user: %w", err)
	}

	cred, err := s.wn.FinishLogin(u, *sess, r)
	if err != nil {
		return nil, fmt.Errorf("auth: finish assertion: %w", err)
	}

	// Update sign_count and last_used_at.
	_, err = s.db.Exec(ctx,
		`UPDATE passkeys SET sign_count = $1, last_used_at = $2 WHERE credential_id = $3`,
		cred.Authenticator.SignCount,
		time.Now().UTC(),
		cred.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("auth: update passkey: %w", err)
	}

	slog.Info("passkey assertion verified", "user_id", userUUID)
	return u, nil
}

// loadUser fetches a user and their credentials from Postgres.
func (s *Service) loadUser(ctx context.Context, id uuid.UUID) (*User, error) {
	var email, displayName string
	err := s.db.QueryRow(ctx,
		`SELECT email, display_name FROM users WHERE id = $1 AND status = 'active'`,
		id,
	).Scan(&email, &displayName)
	if err != nil {
		return nil, fmt.Errorf("query user: %w", err)
	}

	creds, err := s.loadCredentials(ctx, id)
	if err != nil {
		return nil, err
	}

	return &User{
		ID:          id[:],
		Name:        email,
		DisplayName: displayName,
		Credentials: creds,
	}, nil
}

// loadUserByEmail fetches a user by email and their credentials.
func (s *Service) loadUserByEmail(ctx context.Context, email string) (*User, error) {
	var id uuid.UUID
	var displayName string
	err := s.db.QueryRow(ctx,
		`SELECT id, display_name FROM users WHERE email = $1 AND status = 'active'`,
		email,
	).Scan(&id, &displayName)
	if err != nil {
		return nil, fmt.Errorf("query user by email: %w", err)
	}

	creds, err := s.loadCredentials(ctx, id)
	if err != nil {
		return nil, err
	}

	return &User{
		ID:          id[:],
		Name:        email,
		DisplayName: displayName,
		Credentials: creds,
	}, nil
}

// loadCredentials fetches all passkey credentials for a user.
func (s *Service) loadCredentials(ctx context.Context, userID uuid.UUID) ([]webauthn.Credential, error) {
	rows, err := s.db.Query(ctx,
		`SELECT credential_id, public_key, sign_count, transports FROM passkeys WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("query passkeys: %w", err)
	}
	defer rows.Close()

	var creds []webauthn.Credential
	for rows.Next() {
		var credID, pubKey []byte
		var signCount uint32
		var transports []string
		if err := rows.Scan(&credID, &pubKey, &signCount, &transports); err != nil {
			return nil, fmt.Errorf("scan passkey: %w", err)
		}

		transportTypes := make([]protocol.AuthenticatorTransport, len(transports))
		for i, t := range transports {
			transportTypes[i] = protocol.AuthenticatorTransport(t)
		}

		creds = append(creds, webauthn.Credential{
			ID:        credID,
			PublicKey: pubKey,
			Transport: transportTypes,
			Authenticator: webauthn.Authenticator{
				SignCount: signCount,
			},
		})
	}
	return creds, rows.Err()
}

// randBytes returns n cryptographically random bytes.
func randBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
