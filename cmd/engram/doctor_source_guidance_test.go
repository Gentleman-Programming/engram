package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestDoctorExplainsUnsupportedObservationSources(t *testing.T) {
	for _, tc := range []struct {
		name, typ, title, content string
		missing, residual         []string
		repairable                bool
	}{
		{"missing content", "decision", "Valid title", "", []string{"content"}, []string{"content"}, false},
		{"missing type", "", "Valid title", "Private source content.", []string{"type"}, []string{"type"}, false},
		{"missing title and content", "decision", "", "", []string{"title", "content"}, []string{"title", "content"}, false},
		{"recoverable title with missing type", "", "", "Private source content.", []string{"type", "title"}, []string{"type"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			s, err := store.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.CreateSession("source-guidance", "engram", "/work/engram"); err != nil {
				t.Fatal(err)
			}
			id, err := s.AddObservation(store.AddObservationParams{SessionID: "source-guidance", Project: "engram", Scope: "project", Type: "decision", Title: "Valid title", Content: "Private source content."})
			if err != nil {
				t.Fatal(err)
			}
			// Historical corruption is not admitted through the current capture API.
			if _, err := s.DB().Exec(`UPDATE observations SET type = ?, title = ?, content = ? WHERE id = ?`, tc.typ, tc.title, tc.content, id); err != nil {
				t.Fatal(err)
			}
			before, err := s.GetObservation(id)
			if err != nil {
				t.Fatal(err)
			}
			mutationsBefore, err := s.ListPendingProjectMutations("engram")
			if err != nil {
				t.Fatal(err)
			}
			oldExit := exitFunc
			defer func() { exitFunc = oldExit }()
			exitFunc = func(code int) { t.Fatalf("unexpected exit %d", code) }

			withArgs(t, "engram", "doctor", "--check", "sync_mutation_required_fields", "--json")
			stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
			if stderr != "" {
				t.Fatalf("stderr=%q", stderr)
			}
			var report struct {
				Status string `json:"status"`
				Checks []struct {
					Findings []struct {
						ReasonCode   string `json:"reason_code"`
						SafeNextStep string `json:"safe_next_step"`
					} `json:"findings"`
				} `json:"checks"`
			}
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatal(err)
			}
			if report.Status != "blocked" {
				t.Fatalf("diagnostic=%s", stdout)
			}
			found := false
			for _, check := range report.Checks {
				for _, finding := range check.Findings {
					if finding.ReasonCode == "observation_source_missing_required_fields" {
						found = true
						if !strings.Contains(finding.SafeNextStep, "cannot reconstruct missing type or content") || !strings.Contains(finding.SafeNextStep, "manual investigation") {
							t.Errorf("guidance=%q", finding.SafeNextStep)
						}
					}
				}
			}
			if !found {
				t.Fatalf("source finding missing: %s", stdout)
			}

			for _, mode := range []string{"--plan", "--dry-run"} {
				withArgs(t, "engram", "doctor", "repair", "--check", "sync_mutation_required_fields", mode)
				stdout, stderr := captureOutput(t, func() { cmdDoctor(cfg) })
				if stderr != "" {
					t.Fatalf("stderr=%q", stderr)
				}
				var plan struct {
					Applied      bool                                       `json:"applied"`
					Repairs      []store.ObservationSourceTitleRepairAction `json:"source_repairs"`
					Unrepairable []struct {
						ID            int64    `json:"id"`
						SyncID        string   `json:"sync_id"`
						Project       string   `json:"project"`
						MissingFields []string `json:"missing_fields"`
						Reason        string   `json:"reason"`
					} `json:"source_unrepairable"`
				}
				if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
					t.Fatal(err)
				}
				wantRepairs := 0
				if tc.repairable {
					wantRepairs = 1
				}
				if plan.Applied || len(plan.Repairs) != wantRepairs || len(plan.Unrepairable) != 1 {
					t.Fatalf("plan=%s", stdout)
				}
				issue := plan.Unrepairable[0]
				if issue.ID != id || issue.SyncID != before.SyncID || issue.Project != "engram" || !reflect.DeepEqual(issue.MissingFields, tc.residual) || !strings.Contains(issue.Reason, "cannot reconstruct missing type or content") || !strings.Contains(issue.Reason, "manual investigation") {
					t.Fatalf("unrepairable=%+v", issue)
				}
				if !tc.repairable && strings.Contains(stdout, "Private source content.") {
					t.Fatal("unrepairable source leaked content")
				}
				after, err := s.GetObservation(id)
				if err != nil {
					t.Fatal(err)
				}
				mutationsAfter, err := s.ListPendingProjectMutations("engram")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(mutationsBefore, mutationsAfter) {
					t.Fatal("preview changed source or pending mutations")
				}
			}
		})
	}
}
