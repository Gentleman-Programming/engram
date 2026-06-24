package scrub

import (
	"strings"
	"testing"
)

// TestFalsePositives feeds realistic, NON-sensitive dev-memory content and
// reports what trips. These SHOULD ideally be clean; anything flagged here is a
// false positive that, in strict mode, would block a legitimate save.
func TestFalsePositives(t *testing.T) {
	cases := []struct {
		desc string
		in   string
	}{
		{"plain architecture note", "Refactored ChargeService to batch queries; root cause was an N+1 in the listing path."},
		{"git short sha", "Fixed in commit a1b2c3d4e on main."},
		{"git full sha (40 hex)", "Regression introduced by 0a1b2c3d4e5f60718293a4b5c6d7e8f901234567."},
		{"sha256 digest (64 hex)", "artifact sha256 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"uuid", "trace id 550e8400-e29b-41d4-a716-446655440000 in the logs"},
		{"version + PR ref", "PR #42 bumps some-library to 1.2.3."},
		{"internal url", "evidence served by https://internal-services.example.com/svc/charges/chargebacks"},
		{"topic key", "stored under topic_key architecture/auth-model for the upsert path"},
		{"prose with numbers", "The retry budget is 3 attempts over 800ms with exponential backoff and jitter."},
	}
	for _, c := range cases {
		got := Scan([]byte(c.in))
		blocksStrict := false
		tags := make([]string, 0, len(got))
		for _, f := range got {
			tags = append(tags, string(f.Category)+":"+f.Detector)
			if f.Blocks(ModeStrict) {
				blocksStrict = true
			}
		}
		// The contract: legit dev content must NEVER block in strict mode.
		if blocksStrict {
			t.Errorf("FALSE POSITIVE blocks strict: %q -> %s", c.desc, strings.Join(tags, ","))
			continue
		}
		if len(got) == 0 {
			t.Logf("CLEAN              %s", c.desc)
		} else {
			t.Logf("heuristic-only(ok) %-26s -> %s", c.desc, strings.Join(tags, ","))
		}
	}
}
