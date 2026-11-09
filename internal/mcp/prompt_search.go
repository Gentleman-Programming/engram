package mcp

import (
	"context"
	"fmt"
	"strings"

	projectpkg "github.com/Gentleman-Programming/engram/v3/internal/project"
	"github.com/Gentleman-Programming/engram/v3/internal/store"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func handleSearchPrompts(s *store.Store, cfg MCPConfig, activity *SessionActivity) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, ok := req.GetArguments()["query"].(string)
		if !ok || query == "" {
			return mcp.NewToolResultError("query parameter is required"), nil
		}
		project := ""
		if raw, supplied := req.GetArguments()["project"]; supplied {
			var valid bool
			project, valid = raw.(string)
			if !valid || strings.TrimSpace(project) == "" {
				return mcp.NewToolResultError("project must not be blank"), nil
			}
		}
		all := false
		if raw, supplied := req.GetArguments()["all_projects"]; supplied {
			var valid bool
			all, valid = raw.(bool)
			if !valid {
				return mcp.NewToolResultError("all_projects must be a boolean"), nil
			}
		}
		if all && project != "" {
			return mcp.NewToolResultError("project and all_projects cannot be used together"), nil
		}
		res := projectpkg.DetectionResult{Source: projectpkg.SourceAllProjects}
		if !all {
			var err error
			res, err = resolveReadProjectWithProcessOverride(s, project, cfg.DefaultProject)
			if err != nil {
				return readProjectErrorResult(activity, res, err), nil
			}
		}
		prompts, err := s.SearchPrompts(query, res.Project, intArg(req, "limit", 10))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Search prompts error: %s", err)), nil
		}
		if prompts == nil {
			prompts = []store.Prompt{}
		}
		return respondWithProject(res, fmt.Sprintf("Found %d stored prompts.", len(prompts)), map[string]any{"results": prompts}), nil
	}
}
