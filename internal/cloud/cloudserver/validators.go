package cloudserver

import (
	"fmt"
	"strings"
	"unicode"
)

const (
	maxUsernameLen   = 50
	minUsernameLen   = 3
	maxSearchQueryLen = 500
	maxSearchLimit   = 100
	minSearchLimit   = 1
)

// validateRegisterRequest checks that username, email, and password meet
// basic format requirements before hitting the database or auth layer.
// It does not replace auth.ErrWeakPassword — both validations coexist.
func validateRegisterRequest(username, email, password string) error {
	// Username
	if len(username) < minUsernameLen {
		return fmt.Errorf("username must be at least %d characters", minUsernameLen)
	}
	if len(username) > maxUsernameLen {
		return fmt.Errorf("username must be at most %d characters", maxUsernameLen)
	}
	for _, r := range username {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' {
			return fmt.Errorf("username may only contain letters, digits, underscores, hyphens, and dots")
		}
	}

	// Email — basic structure check: must have @ with content before it.
	// The domain check only requires a dot when the domain has more than one
	// segment, so user@localhost is accepted for local/dev environments.
	atIdx := strings.Index(email, "@")
	if atIdx < 1 {
		return fmt.Errorf("email must contain an @ sign")
	}
	domain := email[atIdx+1:]
	if domain == "" {
		return fmt.Errorf("email domain is invalid")
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return fmt.Errorf("email domain is invalid")
	}
	// If the domain contains a dot, it must not be empty on either side.
	if dotIdx := strings.Index(domain, "."); dotIdx != -1 {
		if dotIdx == 0 || dotIdx == len(domain)-1 {
			return fmt.Errorf("email domain is invalid")
		}
	}

	// Password length — defense in depth alongside auth.ErrWeakPassword.
	// Minimum mirrors the 8-char floor enforced by the auth layer.
	// Maximum guards against bcrypt silently truncating passwords longer
	// than 72 bytes, which would make them weaker than the user expects.
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	if len(password) > 72 {
		return fmt.Errorf("password must be at most 72 characters")
	}

	return nil
}

// validateSearchParams checks that the search query and limit are within
// acceptable bounds before the query reaches Postgres full-text search.
func validateSearchParams(query string, limit int) error {
	if len(query) > maxSearchQueryLen {
		return fmt.Errorf("q must be at most %d characters", maxSearchQueryLen)
	}
	if limit < minSearchLimit || limit > maxSearchLimit {
		return fmt.Errorf("limit must be between %d and %d", minSearchLimit, maxSearchLimit)
	}
	return nil
}
