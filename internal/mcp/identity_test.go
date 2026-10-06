package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcppkg "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestServerIdentityVersion(t *testing.T) {
	s := newMCPTestStore(t)
	tests := []struct {
		name    string
		server  *server.MCPServer
		version string
	}{
		{"default constructor", NewServer(s), "dev"},
		{"filtered constructor", NewServerWithTools(s, ProfileAgent), "dev"},
		{"empty config", NewServerWithConfig(s, MCPConfig{}, nil), "dev"},
		{"configured version", NewServerWithConfig(s, MCPConfig{Version: "3.1.0"}, nil), "3.1.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := tt.server.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"identity-test","version":"1"}}}`))
			result, ok := response.(mcppkg.JSONRPCResponse)
			if !ok {
				t.Fatalf("expected successful initialize, got %#v", response)
			}
			initialized, ok := result.Result.(mcppkg.InitializeResult)
			if !ok {
				t.Fatalf("expected initialize result, got %#v", result.Result)
			}
			if got := initialized.ServerInfo; got.Name != "engram" || got.Version != tt.version {
				t.Fatalf("serverInfo = %+v, want engram version %q", got, tt.version)
			}
		})
	}
}
