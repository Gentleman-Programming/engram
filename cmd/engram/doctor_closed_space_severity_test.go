package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/diagnostic"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestDoctorClosedSpaceSeverity(t *testing.T) {
	binary := buildCompatibilityBinary(t)
	for _, scenario := range []string{"reserved", "inert", "pending", "mixed"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := testConfig(t)
			s, err := store.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			if err := s.CreateSession("preserved-session", "fixture", "/work/fixture"); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddObservation(store.AddObservationParams{SessionID: "preserved-session", Type: "decision", Title: "Preserve local record", Content: "Doctor must not change this observation.", Project: "fixture", Scope: "project"}); err != nil {
				t.Fatal(err)
			}
			// SQL constructs historical foreign state that normal writers cannot create.
			keys := []string{}
			if scenario == "inert" || scenario == "mixed" {
				keys = append(keys, "satellite:inert")
			}
			if scenario == "pending" || scenario == "mixed" {
				keys = append(keys, "satellite:pending")
			}
			for _, key := range keys {
				if _, err := s.DB().Exec(`INSERT INTO sync_state (target_key, lifecycle, updated_at) VALUES (?, 'idle', datetime('now'))`, key); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "pending" || scenario == "mixed" {
				if _, err := s.DB().Exec(`INSERT INTO sync_mutations (target_key, entity, entity_key, op, payload, source, project) VALUES ('satellite:pending', ?, 'historical-observation', ?, '{}', ?, '')`, store.SyncEntityObservation, store.SyncOpUpsert, store.SyncSourceLocal); err != nil {
					t.Fatal(err)
				}
			}
			before, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			states, err := s.ListSyncStates()
			if err != nil {
				t.Fatal(err)
			}
			pending, err := s.ListPendingSyncMutations("satellite:pending", 100)
			if err != nil {
				t.Fatal(err)
			}
			// Run even when a severity assertion fails, so RED also verifies preservation.
			defer func() {
				after, err := s.Export()
				if err != nil {
					t.Fatal(err)
				}
				beforeRecords, afterRecords := *before, *after
				// ExportedAt is generated per export, not persisted record evidence.
				beforeRecords.ExportedAt, afterRecords.ExportedAt = "", ""
				if !reflect.DeepEqual(beforeRecords, afterRecords) {
					t.Errorf("exported records changed: before=%+v after=%+v", beforeRecords, afterRecords)
				} else {
					t.Log("exported records unchanged (only ExportedAt excluded)")
				}
				afterStates, err := s.ListSyncStates()
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(states, afterStates) {
					t.Errorf("sync states changed: before=%+v after=%+v", states, afterStates)
				} else {
					t.Log("sync states unchanged")
				}
				afterPending, err := s.ListPendingSyncMutations("satellite:pending", 100)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(pending, afterPending) {
					t.Errorf("pending journal changed: before=%+v after=%+v", pending, afterPending)
				} else {
					t.Log("pending journal unchanged")
				}
			}()
			wantStatus := diagnostic.StatusOK
			if scenario == "inert" {
				wantStatus = diagnostic.StatusWarning
			}
			if scenario == "pending" || scenario == "mixed" {
				wantStatus = diagnostic.StatusError
			}
			for _, format := range []string{"json", "text"} {
				args := []string{"doctor", "--check", diagnostic.CheckSyncTargetClosedSpace}
				if format == "json" {
					args = append(args, "--json")
				}
				command := exec.Command(binary, args...)
				command.Dir = cfg.DataDir
				command.Env = append(os.Environ(), "HOME="+cfg.DataDir, "USERPROFILE="+cfg.DataDir, "ENGRAM_DATA_DIR="+cfg.DataDir, "ENGRAM_URL=", "ENGRAM_SOCKET=", "ENGRAM_PROJECT=")
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				if err := command.Run(); err != nil {
					t.Fatalf("exit=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
				}
				if stderr.Len() != 0 {
					t.Fatalf("stderr=%q", stderr.String())
				}
				if format == "json" {
					var report diagnostic.Report
					if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
						t.Fatal(err)
					}
					if report.Status != wantStatus || len(report.Checks) != 1 || report.Checks[0].Result != wantStatus || len(report.Checks[0].Findings) != len(keys) {
						t.Fatalf("stdout=%s", stdout.String())
					}
					for _, finding := range report.Checks[0].Findings {
						var evidence struct {
							TargetKey string `json:"target_key"`
							Lifecycle string `json:"lifecycle"`
							Pending   int    `json:"unacked_mutations"`
						}
						if err := json.Unmarshal(finding.Evidence, &evidence); err != nil {
							t.Fatal(err)
						}
						severity, count, guidance := diagnostic.SeverityWarning, 0, "no action is required"
						if evidence.TargetKey == "satellite:pending" {
							severity, count, guidance = diagnostic.SeverityError, 1, "Review the 1 unacknowledged mutation(s)"
						} else if evidence.TargetKey != "satellite:inert" {
							t.Fatalf("unexpected target: %+v", evidence)
						}
						if finding.Severity != severity || finding.ReasonCode != diagnostic.ReasonForeignSyncTarget || !finding.RequiresConfirmation || evidence.Lifecycle != "idle" || evidence.Pending != count || !strings.Contains(finding.SafeNextStep, guidance) {
							t.Fatalf("finding=%+v evidence=%+v", finding, evidence)
						}
					}
				} else if !strings.Contains(stdout.String(), wantStatus) {
					t.Fatalf("text=%q", stdout.String())
				}
			}
		})
	}
}
