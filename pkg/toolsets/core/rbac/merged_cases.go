package rbac

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// QueryCases returns rbac's slice of the rancherQuery dispatch table.
// Field mapping notes: the merged "name" parameter carries the username for
// "user"; the merged "project" parameter carries the project ID for
// "projectRTBs".
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return map[string]dispatch.Case[dispatch.QueryParams]{
		"user": {
			Required: []string{"name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getUser(ctx, req, getUserParams{Username: p.Name})
			},
		},
		"roleTemplate": {
			Required: []string{"name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getRoleTemplate(ctx, req, getRoleTemplateParams{Name: p.Name})
			},
		},
		"roleTemplates": {
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listRoleTemplates(ctx, req, struct{}{})
			},
		},
		"clusterRTBs": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listClusterRoleTemplateBindings(ctx, req, listCRTBParams{Cluster: p.Cluster, User: p.User, Group: p.Group})
			},
		},
		"projectRTBs": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listProjectRoleTemplateBindings(ctx, req, listPRTBParams{Cluster: p.Cluster, ProjectID: p.Project, User: p.User, Group: p.Group})
			},
		},
	}
}
