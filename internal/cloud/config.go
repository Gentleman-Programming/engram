package cloud

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DSN              string
	JWTSecret        string
	CORSOrigins      []string
	MaxPool          int
	Port             int
	BindHost         string
	AdminToken       string
	AllowedProjects  []string
	MaxPushBodyBytes int64
	// TokenPepper is the dedicated secret used to hash managed cloud tokens
	// (see internal/cloud/auth.ManagedTokenHasher). It MUST be distinct from
	// JWTSecret so rotating the dashboard/session signing secret does not
	// implicitly rotate or invalidate stored managed token verifiers.
	TokenPepper string
}

const DefaultJWTSecret = "engram-dev-jwt-secret-for-local-smoke-1234"
const DefaultMaxPushBodyBytes int64 = 8 * 1024 * 1024

func DefaultConfig() Config {
	return Config{
		DSN:              "postgres://engram:engram_dev@localhost:5433/engram_cloud?sslmode=disable",
		JWTSecret:        DefaultJWTSecret,
		CORSOrigins:      []string{"*"},
		MaxPool:          10,
		Port:             8080,
		BindHost:         "127.0.0.1",
		MaxPushBodyBytes: DefaultMaxPushBodyBytes,
	}
}

func IsDefaultJWTSecret(secret string) bool {
	return strings.TrimSpace(secret) == DefaultJWTSecret
}

func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	if v := strings.TrimSpace(os.Getenv("ENGRAM_DATABASE_URL")); v != "" {
		cfg.DSN = v
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_JWT_SECRET")); v != "" {
		cfg.JWTSecret = v
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_ADMIN")); v != "" {
		cfg.AdminToken = v
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_TOKEN_PEPPER")); v != "" {
		cfg.TokenPepper = v
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_PORT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Port = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_HOST")); v != "" {
		cfg.BindHost = v
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_MAX_PUSH_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			cfg.MaxPushBodyBytes = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_CLOUD_ALLOWED_PROJECTS")); v != "" {
		parts := strings.Split(v, ",")
		projects := make([]string, 0, len(parts))
		seen := make(map[string]struct{})
		for _, part := range parts {
			project := strings.TrimSpace(part)
			if project == "" {
				continue
			}
			if _, ok := seen[project]; ok {
				continue
			}
			seen[project] = struct{}{}
			projects = append(projects, project)
		}
		cfg.AllowedProjects = projects
	}
	return cfg
}
