package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Bootstrap ensures the first admin user exists and prints a one-shot
// enrollment URL. It is idempotent: if a user row for adminEmail already
// exists, it returns nil immediately without creating duplicates.
func Bootstrap(ctx context.Context, db *pgxpool.Pool, adminEmail string, inviteKey []byte, baseURL string) error {
	// Check whether the admin user already exists.
	var adminID uuid.UUID
	err := db.QueryRow(ctx,
		`SELECT id FROM users WHERE email = $1`,
		adminEmail,
	).Scan(&adminID)

	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("bootstrap: check admin: %w", err)
	}

	if err == nil {
		// User already exists — nothing to do.
		slog.Info("bootstrap: admin user already exists", "email", adminEmail)
		return nil
	}

	// Create the admin user.
	err = db.QueryRow(ctx,
		`INSERT INTO users (email, global_role) VALUES ($1, 'admin') RETURNING id`,
		adminEmail,
	).Scan(&adminID)
	if err != nil {
		return fmt.Errorf("bootstrap: insert admin user: %w", err)
	}

	slog.Info("bootstrap: admin user created", "email", adminEmail, "id", adminID)

	// Mint a 24-hour enrollment invite token. The admin user is its own inviter.
	token, err := MintInviteTokenWithDuration(
		ctx, db, adminID,
		adminEmail, "admin",
		[]string{},
		inviteKey,
		24*60*60*1000000000, // 24 hours in nanoseconds
	)
	if err != nil {
		return fmt.Errorf("bootstrap: mint invite token: %w", err)
	}

	enrollURL := fmt.Sprintf("%s/admin/enroll/%s", baseURL, token)

	fmt.Printf("[bootstrap] Admin enrollment URL: %s\n", enrollURL)
	slog.Info("bootstrap: admin enrollment URL ready", "url", enrollURL)

	return nil
}
