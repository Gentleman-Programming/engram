//go:build mcp_contract_update

package mcp

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteMCPToolContractFixture(t *testing.T) {
	mode := os.Getenv("ENGRAM_MCP_CONTRACT_WRITE")
	if mode == "" {
		t.Skip("set ENGRAM_MCP_CONTRACT_WRITE to update the checked-in fixture")
	}
	live, err := observeMCPToolContract(NewServer(newMCPTestStore(t)).ListTools())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMCPToolContractFixture(mode, filepath.Join("testdata", "tool-contract-v1.json"), live, os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != ""); err != nil {
		t.Fatal(err)
	}
}

func TestMCPToolContractWriterSuccesses(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		fixture    []byte
		live       map[string]mcpToolSchema
	}{
		{
			name: "initial v1 creates missing target",
			mode: "initial-v1",
			live: map[string]mcpToolSchema{
				"x": {Types: []string{"string"}},
			},
		},
		{
			name:    "promote v1 replaces compatible fixture",
			mode:    "promote-v1",
			fixture: []byte(formatMCPToolContract(map[string]mcpToolSchema{"x": {Types: []string{"string"}}})),
			live: map[string]mcpToolSchema{
				"x": {Types: []string{"string", "null"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "fixture.json")
			if tc.fixture != nil {
				if err := os.WriteFile(target, tc.fixture, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			if err := writeMCPToolContractFixture(tc.mode, target, tc.live, false); err != nil {
				t.Fatalf("writeMCPToolContractFixture: %v", err)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			want := []byte(formatMCPToolContract(tc.live))
			if !bytes.Equal(got, want) {
				t.Fatalf("written fixture = %q, want %q", got, want)
			}
		})
	}
}

func TestMCPToolContractWriterRefusals(t *testing.T) {
	fixture := []byte(formatMCPToolContract(map[string]mcpToolSchema{"x": {Types: []string{"number"}}}))
	for _, tc := range []struct {
		name, mode, want string
		ci               bool
		live             map[string]mcpToolSchema
	}{
		{"CI", "promote-v1", "refuses CI", true, map[string]mcpToolSchema{"x": {Types: []string{"number"}}}},
		{"invalid mode", "invalid", "must be", false, map[string]mcpToolSchema{"x": {Types: []string{"number"}}}},
		{"initial overwrite", "initial-v1", "refuses to overwrite", false, map[string]mcpToolSchema{"x": {Types: []string{"number"}}}},
		{"incompatible promotion", "promote-v1", "incompatible", false, map[string]mcpToolSchema{"x": {Types: []string{"integer"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "fixture.json")
			if err := os.WriteFile(target, fixture, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := writeMCPToolContractFixture(tc.mode, target, tc.live, tc.ci); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func writeMCPToolContractFixture(mode, target string, live map[string]mcpToolSchema, ci bool) error {
	if ci {
		return fmt.Errorf("fixture writer refuses CI")
	}
	current, statErr := os.ReadFile(target)
	switch mode {
	case "initial-v1":
		if statErr == nil {
			return fmt.Errorf("initial-v1 refuses to overwrite %s", target)
		}
		if !os.IsNotExist(statErr) {
			return statErr
		}
	case "promote-v1":
		if statErr != nil {
			return fmt.Errorf("promote-v1 requires an existing fixture: %w", statErr)
		}
		v1, err := readMCPToolContractFixture(current)
		if err != nil {
			return err
		}
		if err := compareMCPToolContract(v1, live); err != nil {
			return fmt.Errorf("promote-v1 refuses incompatible contract: %w", err)
		}
	default:
		return fmt.Errorf("ENGRAM_MCP_CONTRACT_WRITE must be initial-v1 or promote-v1")
	}
	formatted := []byte(formatMCPToolContract(live))
	if _, err := readMCPToolContractFixture(formatted); err != nil {
		return fmt.Errorf("validate formatted fixture: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".tool-contract-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(formatted); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, target)
}
