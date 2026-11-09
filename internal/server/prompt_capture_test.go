package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

// Exercise the HTTP contract, including persisted results through read routes.
func TestObservationPromptCapture(t *testing.T) {
	for _, tc := range []struct {
		name, option, prompt string
		want                 int
	}{
		{"default", "", "current user prompt", 1},
		{"explicit true", `,"capture_prompt":true`, "current user prompt", 1},
		{"explicit false", `,"capture_prompt":false`, "current user prompt", 0},
		{"missing prompt", "", "", 0},
		{"capture failure is nonfatal", "", "current user prompt", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var warnings bytes.Buffer
			if tc.name == "capture failure is nonfatal" {
				originalCapture, originalOutput := capturePromptForSave, log.Writer()
				capturePromptForSave = func(*store.Store, *bool, store.AddPromptParams) error {
					return errors.New("forced prompt capture failure")
				}
				log.SetOutput(&warnings)
				t.Cleanup(func() { capturePromptForSave = originalCapture; log.SetOutput(originalOutput) })
			}
			h := New(newServerTestStore(t), 0).Handler()
			request := func(method, path, body string, status int) []byte {
				t.Helper()
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
				if rec.Code != status {
					t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
				}
				return rec.Body.Bytes()
			}
			request("POST", "/sessions", `{"id":"capture","project":"engram"}`, http.StatusCreated)
			body := fmt.Sprintf(`{"session_id":"capture","project":"engram","title":"saved","content":"observation","current_prompt":%q%s}`, tc.prompt, tc.option)
			for i := 0; i < 2; i++ {
				saved := request("POST", "/observations", body, http.StatusCreated)
				var result struct {
					ID     int64  `json:"id"`
					Status string `json:"status"`
				}
				if err := json.Unmarshal(saved, &result); err != nil || result.ID <= 0 || result.Status != "saved" {
					t.Fatalf("save result: %s (%v)", saved, err)
				}
				observation := request("GET", fmt.Sprintf("/observations/%d", result.ID), "", http.StatusOK)
				var record map[string]any
				if err := json.Unmarshal(observation, &record); err != nil || record["content"] != "observation" || record["session_id"] != "capture" {
					t.Fatalf("observation: %s (%v)", observation, err)
				}
			}
			var prompts []struct {
				Content, Project string
				SessionID        string `json:"session_id"`
			}
			data := request("GET", "/prompts/recent?project=engram", "", http.StatusOK)
			if err := json.Unmarshal(data, &prompts); err != nil {
				t.Fatal(err)
			}
			if tc.name == "capture failure is nonfatal" && !strings.Contains(warnings.String(), "auto prompt capture error (non-fatal): forced prompt capture failure") {
				t.Fatalf("missing nonfatal diagnostic: %s", warnings.String())
			}
			if len(prompts) != tc.want {
				t.Fatalf("prompts: %s, want %d", data, tc.want)
			}
			if tc.want == 1 && (prompts[0].Content != tc.prompt || prompts[0].Project != "engram" || prompts[0].SessionID != "capture") {
				t.Fatalf("ownership/content: %s", data)
			}
		})
	}
}

func TestObservationPromptCaptureRejectsInvalidOptionWithoutWrites(t *testing.T) {
	h := New(newServerTestStore(t), 0).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/sessions", strings.NewReader(`{"id":"capture","project":"engram"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("session: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/observations", strings.NewReader(`{"session_id":"capture","project":"engram","title":"saved","content":"body","capture_prompt":"false","current_prompt":"prompt"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid json:") {
		t.Fatalf("invalid option: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/observations?project=engram", "/prompts/recent?project=engram"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("rejected save mutated %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}
