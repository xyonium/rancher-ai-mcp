package projects

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// QueryCases returns projects' slice of the rancherQuery dispatch table.
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return map[string]dispatch.Case[dispatch.QueryParams]{
		"project": {
			Required: []string{"cluster", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getProject(ctx, req, getProjectParams{Name: p.Name, Cluster: p.Cluster})
			},
		},
		"projects": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listProjects(ctx, req, listProjectsParams{Cluster: p.Cluster})
			},
		},
		"resourceUsage": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getResourceUsage(ctx, req, getResourceUsageParams{Cluster: p.Cluster, Project: p.Project, Namespace: p.Namespace})
			},
		},
	}
}

// changeParams maps flat merged params to createProjectParams.
func changeParams(p dispatch.ChangeParams) createProjectParams {
	return createProjectParams{
		Cluster: p.Cluster, Name: p.Name,
		Description: p.Description, DisplayName: p.DisplayName,
		CPULimit: p.CPULimit, CPUReservation: p.CPUReservation,
		MemoryLimit: p.MemoryLimit, MemoryReservation: p.MemoryReservation,
		ConfirmationToken: p.ConfirmationToken,
	}
}

// PlanCases returns projects' slice of the planChange dispatch table.
func (t *Tools) PlanCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createProject": {
			Required: []string{"cluster", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createProjectPlan(ctx, req, changeParams(p))
			},
		},
	}
}

// ExecuteCases returns projects' slice of the executeChange dispatch table.
func (t *Tools) ExecuteCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createProject": {
			Required: []string{"cluster", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createProject(ctx, req, changeParams(p))
			},
		},
	}
}
