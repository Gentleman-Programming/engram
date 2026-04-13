package cloudstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// CreateUser creates a new user and returns the raw API key (only shown once).
func (s *Store) CreateUser(ctx context.Context, name, email string) (userID, rawKey string, err error) {
	raw, hash := generateAPIKey()

	err = s.pool.QueryRow(ctx,
		`INSERT INTO users (name, email, api_key) VALUES ($1, $2, $3) RETURNING id`,
		name, email, hash,
	).Scan(&userID)
	if err != nil {
		return "", "", fmt.Errorf("create user: %w", err)
	}

	return userID, raw, nil
}

// ValidateAPIKey validates a raw API key and returns the user ID if valid.
// Returns empty string if the key is invalid.
func (s *Store) ValidateAPIKey(ctx context.Context, rawKey string) (string, error) {
	hash := hashKey(rawKey)

	var userID string
	err := s.pool.QueryRow(ctx,
		"SELECT id FROM users WHERE api_key = $1", hash,
	).Scan(&userID)
	if err != nil {
		return "", nil // invalid key — not an error, just not found
	}
	return userID, nil
}

// RotateKey generates a new API key for the user and invalidates the old one.
// Returns the new raw key.
func (s *Store) RotateKey(ctx context.Context, userID string) (string, error) {
	raw, hash := generateAPIKey()

	tag, err := s.pool.Exec(ctx,
		"UPDATE users SET api_key = $1 WHERE id = $2", hash, userID,
	)
	if err != nil {
		return "", fmt.Errorf("rotate key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("rotate key: user not found")
	}

	return raw, nil
}

func generateAPIKey() (raw, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed")
	}
	raw = "engram_sk_" + hex.EncodeToString(b)
	hash = hashKey(raw)
	return raw, hash
}

func hashKey(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
