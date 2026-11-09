package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/diagnostic"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCmdDoctorRepairEmptyCLIContract(t *testing.T) {
	binary := buildCompatibilityBinary(t)
	const want = "{\n  \"applied\": false,\n  \"actions\": [],\n  \"project_repairs\": [],\n  \"directory_repairs\": [],\n  \"repairs\": [],\n  \"superseded\": [],\n  \"source_repairs\": [],\n  \"source_unrepairable\": []\n}\n"
	for _, mode := range []string{"default", "--plan", "--dry-run", "--apply"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig(t)
			initDoctorStore(t, cfg)
			args := []string{"doctor", "repair", "--check", "sync_mutation_required_fields"}
			if mode != "default" {
				args = append(args, mode)
			}
			command := exec.Command(binary, args...)
			command.Dir = cfg.DataDir
			command.Env = append(os.Environ(), "HOME="+cfg.DataDir, "USERPROFILE="+cfg.DataDir, "ENGRAM_DATA_DIR="+cfg.DataDir, "ENGRAM_URL=", "ENGRAM_SOCKET=", "ENGRAM_PROJECT=")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("exit: %v, stdout=%q, stderr=%q", err, stdout.String(), stderr.String())
			}
			if stdout.String() != want || stderr.String() != "" {
				t.Fatalf("stdout=%q, want %q; stderr=%q, want empty", stdout.String(), want, stderr.String())
			}
			s, err := store.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Errorf("close store: %v", err)
				}
			})
			data, err := s.Export()
			if err != nil {
				t.Fatal(err)
			}
			pending, err := s.ListPendingSyncMutations(store.DefaultSyncTargetKey, 100)
			if err != nil {
				t.Fatal(err)
			}
			if len(data.Sessions) != 0 || len(data.Observations) != 0 || len(data.Prompts) != 0 || len(pending) != 0 {
				t.Fatalf("empty repair changed records or pending counters: data=%+v, pending=%+v", data, pending)
			}
		})
	}
}

func TestCmdDoctorRepairProjectActionsCLIContract(t *testing.T) {
	cfg := testConfig(t)
	s, err := store.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.CreateSession("host", "demo", "/synthetic"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollProject("demo"); err != nil {
		t.Fatal(err)
	}
	// Seed after Store startup: migration itself repairs this fixture on reopen.
	if _, err := s.DB().Exec(`DELETE FROM sync_mutations; INSERT INTO sync_mutations(target_key,entity,entity_key,op,payload,source,project) VALUES ('cloud','session','host','upsert','{"id":"host","project":"demo","directory":"/synthetic","ownership_mode":""}','local','')`); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListPendingProjectMutations("")
	if err != nil || len(rows) != 1 {
		t.Fatalf("fixture: %+v %v", rows, err)
	}
	seq := rows[0].Seq
	for _, mode := range []diagnostic.RepairMode{diagnostic.RepairModeDryRun, diagnostic.RepairModeApply} {
		result, err := diagnostic.RepairSyncMutationRequiredFields(s, "demo", mode)
		if err != nil {
			t.Fatal(err)
		}
		out, stderr := captureOutput(t, func() { writeDoctorSyncMutationRepairJSON(result) })
		if stderr != "" {
			t.Fatalf("stderr: %s", stderr)
		}
		var report struct {
			Applied  bool                                    `json:"applied"`
			Projects []store.SyncMutationProjectRepairAction `json:"project_repairs"`
		}
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatal(err)
		}
		if report.Applied != (mode == diagnostic.RepairModeApply) || len(report.Projects) != 1 || report.Projects[0].Seq != seq || report.Projects[0].Project != "demo" || report.Projects[0].EntityKey != "host" {
			t.Fatalf("%s: %s", mode, out)
		}
		rows, err := s.ListPendingProjectMutations("")
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if mode == diagnostic.RepairModeApply {
			want = "demo"
		}
		if len(rows) != 1 || rows[0].Project != want {
			t.Fatalf("%s journal: %+v", mode, rows)
		}
	}
}
