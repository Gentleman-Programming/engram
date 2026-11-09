package store

import (
	"encoding/json"
	"strconv"
	"testing"
)

func TestCaptureProjectProvenanceRecreatedSessionDoesNotInheritDeletedEvidence(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.RegisterSessionWithProjectProvenance("recreated", "example", "", SessionOwnershipShared, false, false, &ProjectProvenance{Source: "explicit_override"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSession("recreated"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession("recreated", "example", ""); err != nil {
		t.Fatal(err)
	}
	p, err := st.CaptureProjectProvenance("session", "recreated")
	if err != nil || p != nil {
		t.Fatalf("recreated session inherited deleted evidence: %+v, %v", p, err)
	}
}

func TestCaptureProjectProvenanceAutomaticPrompt(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateSession("prompt-evidence", "example", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CapturePromptForSave(nil, AddPromptParams{
		SessionID: "prompt-evidence", Project: "example", Content: "current prompt",
		ProjectProvenance: &ProjectProvenance{Source: "git_root"},
	}); err != nil {
		t.Fatal(err)
	}
	prompts, err := st.RecentPrompts("example", 10)
	if err != nil || len(prompts) != 1 {
		t.Fatalf("prompts=%+v err=%v", prompts, err)
	}
	p, err := st.CaptureProjectProvenance("prompt", strconv.FormatInt(prompts[0].ID, 10))
	if err != nil || p == nil || p.Source != "git_root" {
		t.Fatalf("evidence=%+v err=%v", p, err)
	}
}

func TestProjectProvenanceRejectsNonStringFields(t *testing.T) {
	for _, payload := range []string{`{"source":null}`, `{"source":"git_root","directory":null}`, `{"source":"git_root","raw_project":null}`} {
		var p ProjectProvenance
		if err := json.Unmarshal([]byte(payload), &p); err == nil {
			t.Errorf("accepted %s", payload)
		}
	}
}
