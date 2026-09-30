package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v2/internal/store"
)

type fakePromptAttester struct {
	calls int
	id    int64
	err   error
}

func (f *fakePromptAttester) AttestPromptSource(session, inbox, syncID, owner, project string) (int64, error) {
	f.calls++
	if session != "session" || inbox != "inbox" || syncID != "exact" || owner != "owner" || project != "prompt" {
		return 0, errors.New("wrong tuple")
	}
	return f.id, f.err
}

type failingPromptWriter struct {
	calls  int
	failAt int
}

func (w *failingPromptWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func TestPromptSourceAttestationNoPreview(t *testing.T) {
	s, err := store.New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	remote := &fakePromptAttester{id: 42}
	var out bytes.Buffer
	err = attestPromptSource(s, "missing", "owner", "https://cloud.example.test", remote, strings.NewReader("yes\n"), &out)
	if err == nil || !strings.Contains(err.Error(), "no unambiguous") || remote.calls != 0 || out.Len() != 0 {
		t.Fatalf("error=%v calls=%d output=%q", err, remote.calls, out.String())
	}
}

func TestPromptSourceAttestationOutputFailures(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failAt      int
		wantCalls   int
		wantMarkers int
	}{
		{"preview", 1, 0, 0},
		{"question", 2, 0, 0},
		{"success", 3, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.New(testConfig(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if err := s.CreateSession("session", "owner", "/work"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`INSERT INTO user_prompts(sync_id,session_id,source_inbox_id,project,content) VALUES ('exact','session','inbox','prompt','body')`); err != nil {
				t.Fatal(err)
			}
			remote := &fakePromptAttester{id: 42}
			out := &failingPromptWriter{failAt: tc.failAt}
			err = attestPromptSource(s, "exact", "owner", "https://cloud.example.test", remote, strings.NewReader("yes\n"), out)
			if !errors.Is(err, io.ErrClosedPipe) || remote.calls != tc.wantCalls {
				t.Fatalf("error=%v remote calls=%d want %d", err, remote.calls, tc.wantCalls)
			}
			var count int
			if err := s.DB().QueryRow(`SELECT count(*) FROM prompt_source_confirmations`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != tc.wantMarkers {
				t.Fatalf("markers=%d want %d", count, tc.wantMarkers)
			}
		})
	}
}

func TestPromptSourceAttestationEndpointRejectsUserinfo(t *testing.T) {
	for _, raw := range []string{"https://user:secret@cloud.example.test", "https://user@cloud.example.test"} {
		_, err := promptSourceAttestationEndpoint(raw)
		if err == nil || strings.Contains(err.Error(), "user") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), raw) {
			t.Fatalf("endpoint error unsafe or missing: %v", err)
		}
	}
	if endpoint, err := promptSourceAttestationEndpoint("https://cloud.example.test"); err != nil || endpoint != "https://cloud.example.test" {
		t.Fatalf("valid endpoint=%q error=%v", endpoint, err)
	}
}

func TestPromptSourceAttestationConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		id           int64
		remoteErr    error
		wantErr      string
		calls        int
	}{
		{"accepted", "yes", 42, nil, "", 1},
		{"declined", "no", 42, nil, "declined", 0},
		{"invalid", "maybe", 42, nil, "confirmation", 0},
		{"remote failure", "yes", 0, errors.New("denied"), "remote attestation failed", 1},
		{"no positive ID", "yes", 0, nil, "positive", 1},
		{"local owner conflict after remote success", "yes", 42, nil, "remote attestation succeeded (ID 42), but local confirmation failed", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			s, err := store.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			if err := s.CreateSession("session", "owner", "/work"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().Exec(`INSERT INTO user_prompts(sync_id,session_id,source_inbox_id,project,content) VALUES ('exact','session','inbox','prompt','body')`); err != nil {
				t.Fatal(err)
			}
			if tc.name == "local owner conflict after remote success" {
				if _, err := s.DB().Exec(`UPDATE sessions SET project='different' WHERE id='session'`); err != nil {
					t.Fatal(err)
				}
			}
			remote := &fakePromptAttester{id: tc.id, err: tc.remoteErr}
			var out bytes.Buffer
			err = attestPromptSource(s, "exact", "owner", "https://cloud.example.test", remote, strings.NewReader(tc.answer+"\n"), &out)
			if (err == nil) != (tc.wantErr == "") || tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v want %q", err, tc.wantErr)
			}
			if remote.calls != tc.calls {
				t.Fatalf("remote calls=%d want %d", remote.calls, tc.calls)
			}
			for _, want := range []string{"session", "inbox", "exact", "prompt", "live", "Human-asserted owner: \"owner\""} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("preview %q missing %q", out.String(), want)
				}
			}
			var count int
			if err := s.DB().QueryRow(`SELECT count(*) FROM prompt_source_confirmations WHERE remote_target=? AND sync_id='exact'`, "https://cloud.example.test").Scan(&count); err != nil {
				t.Fatal(err)
			}
			expected := 0
			if tc.wantErr == "" {
				expected = 1
			}
			if count != expected {
				t.Fatalf("confirmations=%d want %d", count, expected)
			}
		})
	}
}
