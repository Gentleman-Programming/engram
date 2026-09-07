package syncguidance

import (
	"errors"
	"testing"
)

type policyError struct{ err error }

func (e policyError) Error() string       { return e.err.Error() }
func (e policyError) Unwrap() error       { return e.err }
func (policyError) IsPolicyFailure() bool { return true }

func TestAppendGuidancePolicyFailureIsDeterministicAndIdempotent(t *testing.T) {
	err := policyError{err: errors.New(`cloud: push: status 403: sync denied for project "policy-project"`)}
	message := err.Error()
	want := message + "\n\n" + `Cloud sync is blocked by server policy.
The server denied access to project "policy-project". Ask the server administrator to check ENGRAM_CLOUD_ALLOWED_PROJECTS. If this client uses a managed principal, its project grant may also need checking. The client does not expose server allowlist contents.`

	if got := AppendGuidance(message, "policy-project", err); got != want {
		t.Fatalf("policy guidance = %q, want %q", got, want)
	}
	if got := AppendGuidance(want, "policy-project", err); got != want {
		t.Fatalf("policy guidance must be idempotent, got %q", got)
	}
}

func TestAppendGuidancePolicyFailureCompletesHeaderOnlyMessage(t *testing.T) {
	err := policyError{err: errors.New(policyHeader)}

	if got, want := AppendGuidance(policyHeader, "policy-project", err), PolicyGuidance("policy-project"); got != want {
		t.Fatalf("policy guidance = %q, want %q", got, want)
	}
}
