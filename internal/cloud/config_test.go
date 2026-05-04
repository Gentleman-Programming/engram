package cloud

import "testing"

func TestConfigFromEnvCloudHost(t *testing.T) {
	t.Run("default bind host stays loopback", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_HOST", "")
		cfg := ConfigFromEnv()
		if cfg.BindHost != "127.0.0.1" {
			t.Fatalf("expected default bind host 127.0.0.1, got %q", cfg.BindHost)
		}
	})

	t.Run("env overrides bind host", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_HOST", "0.0.0.0")
		cfg := ConfigFromEnv()
		if cfg.BindHost != "0.0.0.0" {
			t.Fatalf("expected bind host override 0.0.0.0, got %q", cfg.BindHost)
		}
	})
}

func TestConfigFromEnvAllowedProjects(t *testing.T) {
	t.Setenv("ENGRAM_CLOUD_ALLOWED_PROJECTS", "proj-a, proj-b,proj-a")
	cfg := ConfigFromEnv()
	if len(cfg.AllowedProjects) != 2 {
		t.Fatalf("expected deduplicated allowlist, got %v", cfg.AllowedProjects)
	}
	if cfg.AllowedProjects[0] != "proj-a" || cfg.AllowedProjects[1] != "proj-b" {
		t.Fatalf("unexpected allowlist order/values: %v", cfg.AllowedProjects)
	}
}

func TestConfigFromEnvMaxPushBodyBytes(t *testing.T) {
	t.Run("default remains 8 MiB", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_MAX_PUSH_BYTES", "")
		cfg := ConfigFromEnv()
		if cfg.MaxPushBodyBytes != DefaultMaxPushBodyBytes {
			t.Fatalf("expected default max push bytes %d, got %d", DefaultMaxPushBodyBytes, cfg.MaxPushBodyBytes)
		}
	})

	t.Run("env override works", func(t *testing.T) {
		t.Setenv("ENGRAM_CLOUD_MAX_PUSH_BYTES", "67108864")
		cfg := ConfigFromEnv()
		if cfg.MaxPushBodyBytes != 67108864 {
			t.Fatalf("expected env override 67108864, got %d", cfg.MaxPushBodyBytes)
		}
	})

	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "invalid keeps default", value: "not-a-number"},
		{name: "zero keeps default", value: "0"},
		{name: "negative keeps default", value: "-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENGRAM_CLOUD_MAX_PUSH_BYTES", tc.value)
			cfg := ConfigFromEnv()
			if cfg.MaxPushBodyBytes != DefaultMaxPushBodyBytes {
				t.Fatalf("expected default max push bytes %d for %q, got %d", DefaultMaxPushBodyBytes, tc.value, cfg.MaxPushBodyBytes)
			}
		})
	}
}

func TestIsDefaultJWTSecret(t *testing.T) {
	t.Run("default secret returns true", func(t *testing.T) {
		if !IsDefaultJWTSecret(DefaultJWTSecret) {
			t.Fatal("expected default jwt secret to be recognized")
		}
	})

	t.Run("custom secret returns false", func(t *testing.T) {
		if IsDefaultJWTSecret("custom-super-secret-value-1234567890") {
			t.Fatal("expected custom jwt secret to be treated as non-default")
		}
	})
}
