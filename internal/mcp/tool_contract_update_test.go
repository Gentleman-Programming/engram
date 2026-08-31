//go:build mcp_contract_update

package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteMCPToolContractFixture(t *testing.T) {
	live, err := observeMCPToolContract(NewServer(newMCPTestStore(t)).ListTools())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMCPToolContractFixture(os.Getenv("ENGRAM_MCP_CONTRACT_WRITE"), filepath.Join("testdata", "tool-contract-v1.json"), live, os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != ""); err != nil {
		t.Fatal(err)
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
