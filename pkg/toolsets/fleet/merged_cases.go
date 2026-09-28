package fleet

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// QueryCases returns fleet's slice of the rancherQuery dispatch table.
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return map[string]dispatch.Case[dispatch.QueryParams]{
		"gitRepo": {
			Required: []string{"workspace", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getGitRepo(ctx, req, getGitRepoParams{Name: p.Name, Workspace: p.Workspace})
			},
		},
		"gitRepos": {
			Required: []string{"workspace"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listGitRepos(ctx, req, listGitRepoParams{Workspace: p.Workspace})
			},
		},
		"bundle": {
			Required: []string{"workspace", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getBundle(ctx, req, getBundleParams{Name: p.Name, Workspace: p.Workspace})
			},
		},
	}
}

// DiagnoseCases returns fleet's slice of the diagnose dispatch table.
func (t *Tools) DiagnoseCases() map[string]dispatch.Case[dispatch.DiagnoseParams] {
	return map[string]dispatch.Case[dispatch.DiagnoseParams]{
		"fleet": {
			Required: []string{"workspace"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.analyzeFleetResources(ctx, req, analyzeFleetResourcesParams{Workspace: p.Workspace})
			},
		},
	}
}
