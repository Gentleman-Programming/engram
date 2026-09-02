package cloudconfig

import (
	"os"
	"strings"
)

// EnvCloudToken is the environment variable that overrides cloud.json.token.
const EnvCloudToken = "ENGRAM_CLOUD_TOKEN"

// EnvCloudServer is the environment variable that overrides cloud.json.server_url.
const EnvCloudServer = "ENGRAM_CLOUD_SERVER"

// Source identifies where the effective cloud token came from.
type Source int

const (
	SourceNone Source = iota
	SourceFile
	SourceEnv
)

const (
	LabelSourceNone = "not set"
	LabelSourceFile = "read from cloud.json"
	LabelSourceEnv  = "set via ENGRAM_CLOUD_TOKEN"
)

// SourceLabel returns the user-facing label for a token source.
func SourceLabel(source Source) string {
	switch source {
	case SourceFile:
		return LabelSourceFile
	case SourceEnv:
		return LabelSourceEnv
	default:
		return LabelSourceNone
	}
}

// ApplyServerOverride applies ENGRAM_CLOUD_SERVER when it is non-empty.
func ApplyServerOverride(cfg *Config) *Config {
	if cfg == nil {
		cfg = &Config{}
	}
	if value := strings.TrimSpace(os.Getenv(EnvCloudServer)); value != "" {
		cfg.ServerURL = value
	}
	return cfg
}

// EffectiveToken resolves the environment token before the cloud.json fallback.
// A malformed configuration is treated as unavailable by this convenience API;
// callers that must surface parse failures should call Load first.
func EffectiveToken(dataDir string) (string, Source) {
	if value := strings.TrimSpace(os.Getenv(EnvCloudToken)); value != "" {
		return value, SourceEnv
	}
	cfg, err := Load(dataDir)
	if err != nil || cfg == nil {
		return "", SourceNone
	}
	if value := strings.TrimSpace(cfg.Token); value != "" {
		return value, SourceFile
	}
	return "", SourceNone
}
