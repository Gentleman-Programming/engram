package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Gentleman-Programming/engram/v3/internal/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestCmdMCPReportsBinaryVersion(t *testing.T) {
	for _, buildVersion := range []string{"3.1.0", "dev", "3.2.0-beta.1"} {
		t.Run(buildVersion, func(t *testing.T) {
			cfg := testConfig(t)
			t.Setenv("ENGRAM_CLOUD_AUTOSYNC", "")
			withArgs(t, "engram", "mcp", "--tools=agent", "--project=identity-test")
			oldVersion, oldServe, oldNew := version, serveMCP, newMCPServerWithConfig
			version = buildVersion
			newMCPServerWithConfig = mcp.NewServerWithConfig
			t.Cleanup(func() {
				version, serveMCP, newMCPServerWithConfig = oldVersion, oldServe, oldNew
			})
			served := false
			serveMCP = func(srv *mcpserver.MCPServer, _ ...mcpserver.StdioOption) error {
				served = true
				response := srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"identity-test","version":"1"}}}`))
				data, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				var envelope struct {
					Result struct {
						ServerInfo struct {
							Name    string `json:"name"`
							Version string `json:"version"`
						} `json:"serverInfo"`
					} `json:"result"`
				}
				if err := json.Unmarshal(data, &envelope); err != nil {
					t.Fatal(err)
				}
				if got := envelope.Result.ServerInfo; got.Name != "engram" || got.Version != buildVersion {
					t.Fatalf("serverInfo = %+v, want engram version %q", got, buildVersion)
				}
				return nil
			}
			stdout, stderr := captureOutput(t, func() { cmdMCP(cfg) })
			if !served {
				t.Fatal("MCP server was not served")
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

func TestMCPHelpCountsMatchServedProfiles(t *testing.T) {
	cfg := testConfig(t)
	s, err := storeNew(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close test store: %v", err)
		}
	})
	withArgs(t, "engram", "--help")
	stdout, stderr := captureOutput(t, main)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	counts := make(map[string]int)
	for _, profile := range []string{"agent", "admin", "all"} {
		srv := mcp.NewServerWithTools(s, mcp.ResolveTools(profile))
		response := srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
		data, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Result struct {
				Tools []json.RawMessage `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		counts[profile] = len(envelope.Result.Tools)
		if counts[profile] == 0 {
			t.Fatalf("profile %s returned no tools: %s", profile, data)
		}
	}
	want := fmt.Sprintf("Profiles: agent (%d tools), admin (%d tools), all (default, %d)", counts["agent"], counts["admin"], counts["all"])
	if !strings.Contains(stdout, want) {
		t.Fatalf("help must contain %q, got:\n%s", want, stdout)
	}
}
