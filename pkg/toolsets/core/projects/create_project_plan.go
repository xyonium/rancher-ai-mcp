package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
)

// createProjectPlan plans the creation of a new project.
// It returns the JSON representation of the project to be created without
// actually creating it, plus a single-use confirmation token for the matching
// createProject call.
func (t *Tools) createProjectPlan(_ context.Context, _ *mcp.CallToolRequest, params createProjectParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("createProject_plan called", zap.String("cluster", params.Cluster))

	project, err := t.createProjectObj(params)
	if err != nil {
		zap.L().Error("failed to create project object", zap.String("tool", "createProject_plan"), zap.Error(err))
		return nil, nil, fmt.Errorf("failed to create project object: %w", err)
	}

	// The token binds the exact project object being created, canonicalized the
	// same way the execute tool does.
	payloadBytes, err := json.Marshal(project.Object)
	if err != nil {
		zap.L().Error("failed to marshal project object", zap.String("tool", "createProject_plan"), zap.Error(err))
		return nil, nil, fmt.Errorf("failed to marshal project object: %w", err)
	}
	op := confirm.Operation{Tool: "createProject", Cluster: params.Cluster, Kind: "project", Name: params.Name, Payload: payloadBytes}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}

	createResource := response.NewCreateResourceInput(project, params.Cluster)
	mcpResponse, err := response.CreatePlanResponse([]response.PlanResource{createResource}, &response.Confirmation{
		Token:     token,
		ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
		Note:      "Show this plan to the user. Only after their explicit approval, call createProject with this confirmationToken. The token is single-use and expires in 10 minutes.",
	})
	if err != nil {
		zap.L().Error("failed to create plan response", zap.String("tool", "createProject_plan"), zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}
