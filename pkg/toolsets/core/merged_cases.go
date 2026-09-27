package core

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/core/projects"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/core/rbac"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// QueryCases returns the rancherQuery dispatch table owned by the rancher core
// toolset: core's own cases unioned with the projects and rbac sub-toolsets.
// The sub-toolsets have no separate registration surface any more, so core is
// their home in the merged table. MergeMaps panics on a duplicate key, so a
// case owned by two toolsets is a registration-time failure.
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return dispatch.MergeMaps(
		map[string]dispatch.Case[dispatch.QueryParams]{
			"clusters": {
				Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
					return t.listClusters(ctx, req, struct{}{})
				},
			},
			"clusterImages": {
				Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
					return t.getClusterImages(ctx, req, getClusterImagesParams{Clusters: p.Clusters})
				},
			},
		},
		projects.NewTools(t.client, t.cfg).QueryCases(),
		rbac.NewTools(t.client, t.cfg.ReadOnly).QueryCases(),
	)
}

// DiagnoseCases returns core's slice of the diagnose dispatch table.
func (t *Tools) DiagnoseCases() map[string]dispatch.Case[dispatch.DiagnoseParams] {
	return map[string]dispatch.Case[dispatch.DiagnoseParams]{
		"nodes": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.getNodes(ctx, req, getNodesParams{Cluster: p.Cluster})
			},
		},
		"deployment": {
			Required: []string{"cluster", "namespace", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.getDeploymentDetails(ctx, req, specificResourceParams{Cluster: p.Cluster, Namespace: p.Namespace, Name: p.Name})
			},
		},
		"pod": {
			Required: []string{"cluster", "namespace", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.inspectPod(ctx, req, specificResourceParams{Cluster: p.Cluster, Namespace: p.Namespace, Name: p.Name})
			},
		},
	}
}
