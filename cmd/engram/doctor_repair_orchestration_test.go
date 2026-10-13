package main

import (
	"bytes"
	"os"
	"os/exec"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/store"
)

func TestCmdDoctorRepairEmptyCLIContract(t *testing.T) {
	binary := buildCompatibilityBinary(t)
	const want = "{\n  \"applied\": false,\n  \"actions\": [],\n  \"directory_repairs\": [],\n  \"repairs\": [],\n  \"superseded\": [],\n  \"source_repairs\": [],\n  \"source_unrepairable\": []\n}\n"
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
			defer s.Close()
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
