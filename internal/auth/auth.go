// Package auth handles WebAuthn registration and assertion, session issuance,
// invite token minting and redemption, and admin bootstrap.
//
// Phase B — WebAuthn, sessions, invite tokens.
package auth

import (
	// WebAuthn / PassKey support — implemented in Phase B.
	_ "github.com/go-webauthn/webauthn/webauthn"

	// JWT access tokens — implemented in Phase B.
	_ "github.com/golang-jwt/jwt/v5"

	// bcrypt / argon2 for invite-token hashing — implemented in Phase B.
	_ "golang.org/x/crypto/bcrypt"
)
