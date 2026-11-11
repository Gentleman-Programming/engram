package setup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPiInstallPreservesCustomAtCommandBoundary(t *testing.T) {
	for _, object := range []bool{false, true} {
		for _, failure := range []bool{false, true} {
			t.Run(map[bool]string{false: "string", true: "object"}[object]+map[bool]string{false: "/success", true: "/error-after-persist"}[failure], func(t *testing.T) {
				resetSetupSeams(t)
				dir := t.TempDir()
				t.Setenv("PI_CODING_AGENT_DIR", dir)
				path := filepath.Join(dir, "settings.json")
				var custom any = "npm:gentle-engram@9.9.9"
				if object {
					custom = map[string]any{"source": custom, "extensions": []any{"keep.ts"}, "autoUpdate": false}
				}
				before := map[string]any{"packages": []any{custom, "npm:gentle-engram@0.7.0", "./local"}, "npmCommand": []any{"custom-npm"}}
				write := func(value any) {
					t.Helper()
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				write(before)
				runCommand = func(string, ...string) ([]byte, error) {
					// Pi 0.99 addSourceToSettings replaces the first matching npm
					// identity, retaining object fields but replacing its source.
					var replacement any = piGentleEngramPackage
					if object {
						replacement = map[string]any{"source": piGentleEngramPackage, "extensions": []any{"keep.ts"}, "autoUpdate": false}
					}
					write(map[string]any{"packages": []any{replacement, "npm:gentle-engram@0.7.0", "./local", "npm:added-by-pi"}, "npmCommand": []any{"custom-npm"}, "newSetting": true})
					if failure {
						return nil, errors.New("after persist")
					}
					return nil, nil
				}
				_, err := Install("pi")
				if (err != nil) != failure {
					t.Fatalf("error = %v", err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var after map[string]any
				if err := json.Unmarshal(raw, &after); err != nil {
					t.Fatal(err)
				}
				want := []any{custom, "./local", "npm:added-by-pi", piGentleEngramPackage}
				if failure {
					want = []any{custom, "npm:gentle-engram@0.7.0", "./local", "npm:added-by-pi"}
				}
				if !reflect.DeepEqual(after["packages"], want) || after["newSetting"] != true || !reflect.DeepEqual(after["npmCommand"], before["npmCommand"]) {
					t.Fatalf("custom or unrelated state lost: %s", raw)
				}
				backups, err := filepath.Glob(filepath.Join(dir, ".engram-pi-settings-*"))
				if err != nil || len(backups) != 0 {
					t.Fatalf("unexpected backups: %v %v", backups, err)
				}
			})
		}
	}
}

func TestPiInstallConflictRetainsRecoveryAndLatestSettings(t *testing.T) {
	resetSetupSeams(t)
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	path := filepath.Join(dir, "settings.json")
	original := `{"packages":[{"source":"npm:gentle-engram@9.9.9","filter":9007199254740992}],"keep":true}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	// A changed field is not Pi's recognized source-only replacement, even
	// when the large integer would round to the same float64 value.
	latest := `{"packages":[{"source":"npm:gentle-engram@0.7.1","filter":9007199254740993}],"new":true}`
	runCommand = func(string, ...string) ([]byte, error) {
		return nil, os.WriteFile(path, []byte(latest), 0600)
	}
	if _, err := Install("pi"); err == nil || !strings.Contains(err.Error(), "recovery settings retained") {
		t.Fatalf("missing conflict: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != latest {
		t.Fatalf("conflict overwritten: %s %v", got, err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".engram-pi-settings-*"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("missing recovery: %v %v", backups, err)
	}
	saved, err := os.ReadFile(backups[0])
	if err != nil || !samePiJSON(saved, []byte(original)) {
		t.Fatalf("invalid recovery: %v", err)
	}
}

func TestPiInstallRecoveryIOFailure(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		for _, installFails := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/install-success", true: "/install-error"}[installFails], func(t *testing.T) {
				resetSetupSeams(t)
				dir := t.TempDir()
				t.Setenv("PI_CODING_AGENT_DIR", dir)
				path := filepath.Join(dir, "settings.json")
				const original = `{"packages":["npm:gentle-engram@9.9.9","npm:gentle-engram@0.7.0"],"keep":true}`
				const latest = `{"packages":["npm:gentle-engram@0.7.1","npm:gentle-engram@0.7.0","./added"],"keep":true,"new":true}`
				if err := os.WriteFile(path, []byte(original), 0600); err != nil {
					t.Fatal(err)
				}
				ioErr := errors.New("injected recovery " + operation + " failure")
				installErr := errors.New("injected install failure")
				runCommand = func(string, ...string) ([]byte, error) {
					if err := os.WriteFile(path, []byte(latest), 0600); err != nil {
						t.Fatal(err)
					}
					if operation == "read" {
						readFileFn = func(name string) ([]byte, error) {
							if name == path {
								return nil, ioErr
							}
							return os.ReadFile(name)
						}
					} else {
						writeFileFn = func(name string, data []byte, mode os.FileMode) error {
							if name == path {
								return ioErr
							}
							return os.WriteFile(name, data, mode)
						}
					}
					if installFails {
						return nil, installErr
					}
					return nil, nil
				}
				_, err := Install("pi")
				if !errors.Is(err, ioErr) || errors.Is(err, installErr) != installFails {
					t.Fatalf("recovery lost an error: %v", err)
				}
				backups, globErr := filepath.Glob(filepath.Join(dir, ".engram-pi-settings-*"))
				if globErr != nil || len(backups) != 1 {
					t.Fatalf("missing recovery: %v %v", backups, globErr)
				}
				if !strings.Contains(err.Error(), "recovery settings retained at "+backups[0]) {
					t.Fatalf("recovery path not reported: %v", err)
				}
				got, readErr := os.ReadFile(path)
				if readErr != nil || string(got) != latest {
					t.Fatalf("latest settings changed: %s %v", got, readErr)
				}
				saved, readErr := os.ReadFile(backups[0])
				if readErr != nil {
					t.Fatal(readErr)
				}
				var actual, expected any
				if err := json.Unmarshal(saved, &actual); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(original), &expected); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(actual, expected) {
					t.Fatal("recovery copy does not preserve original settings")
				}
			})
		}
	}
}

func TestPiInstallValidatesSettingsBeforeExternalCommand(t *testing.T) {
	resetSetupSeams(t)
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	path := filepath.Join(dir, "settings.json")
	const original = `{"packages":"not-an-array"}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	runCommand = func(string, ...string) ([]byte, error) { t.Fatal("invalid settings reached Pi"); return nil, nil }
	if _, err := Install("pi"); err == nil {
		t.Fatal("invalid settings accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != original {
		t.Fatalf("settings changed: %v", err)
	}
}

func TestPiInstallManagedFirstAndPrePersistenceFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed-first", true: "failure-before-persist"}[failed], func(t *testing.T) {
			resetSetupSeams(t)
			dir := t.TempDir()
			t.Setenv("PI_CODING_AGENT_DIR", dir)
			path := filepath.Join(dir, "settings.json")
			original := `{"packages":["npm:gentle-engram@0.7.0","npm:gentle-engram@9.9.9"],"npmCommand":["custom-npm"]}`
			if failed {
				original = `{"packages":["npm:gentle-engram@9.9.9","npm:gentle-engram@0.7.0"]}`
			}
			if err := os.WriteFile(path, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			runCommand = func(string, ...string) ([]byte, error) {
				if failed {
					return nil, errors.New("install failed before persistence")
				}
				return nil, os.WriteFile(path, []byte(strings.Replace(original, "0.7.0", "0.7.1", 1)), 0600)
			}
			_, err := Install("pi")
			if (err != nil) != failed {
				t.Fatalf("error = %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if failed && string(got) != original {
				t.Fatalf("failure changed settings: %s", got)
			}
			if !failed && !strings.Contains(string(got), "npm:gentle-engram@9.9.9") {
				t.Fatal("custom entry lost")
			}
			backups, err := filepath.Glob(filepath.Join(dir, ".engram-pi-settings-*"))
			if err != nil || len(backups) != 0 {
				t.Fatalf("unexpected recovery copy: %v %v", backups, err)
			}
		})
	}
}

func TestPiInstallUsesPiNpmIdentity(t *testing.T) {
	for _, sources := range [][]string{
		{" npm:gentle-engram@9.9.9", "npm:gentle-engram@8.8.8"},
		{"npm:gentle-engram@", "npm:gentle-engram@8.8.8"},
		{"npm:gentle-engram@bad\nversion", "npm:gentle-engram@8.8.8"},
		{"npm:\u0085gentle-engram@9.9.9", "npm:gentle-engram@8.8.8"},
		{"npm: gentle-engram@9.9.9 ", "npm:gentle-engram@0.7.0"},
		{"npm:\ufeffgentle-engram@9.9.9\ufeff", "npm:gentle-engram@0.7.0"},
	} {
		t.Run(sources[0], func(t *testing.T) {
			resetSetupSeams(t)
			dir := t.TempDir()
			t.Setenv("PI_CODING_AGENT_DIR", dir)
			path := filepath.Join(dir, "settings.json")
			data, err := json.Marshal(map[string]any{"packages": sources, "npmCommand": []string{"custom-npm"}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			index := 1
			if strings.HasPrefix(sources[0], "npm: ") || strings.Contains(sources[0], "\ufeff") {
				index = 0
			}
			runCommand = func(string, ...string) ([]byte, error) {
				changed := append([]string(nil), sources...)
				changed[index] = piGentleEngramPackage
				data, err := json.Marshal(map[string]any{"packages": changed, "npmCommand": []string{"custom-npm"}})
				if err != nil {
					return nil, err
				}
				return nil, os.WriteFile(path, data, 0600)
			}
			if _, err := Install("pi"); err != nil {
				t.Fatal(err)
			}
			config, err := readJSONConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			packages, err := readRawArrayField(config, "packages", path)
			if err != nil {
				t.Fatal(err)
			}
			for _, source := range sources {
				if isPiManagedPackage(source) {
					continue
				}
				found := false
				for _, raw := range packages {
					var got string
					if json.Unmarshal(raw, &got) == nil && got == source {
						found = true
					}
				}
				if !found {
					t.Fatalf("custom source lost: %q", source)
				}
			}
		})
	}
}
