package projects

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
)

// moveNamespacePlan plans moving a namespace to a different project. It
// returns the namespace object as it will look after the move (projectId
// label and annotation updated) without changing anything, plus a single-use
// confirmation token for the matching moveNamespace call.
func (t *Tools) moveNamespacePlan(ctx context.Context, _ *mcp.CallToolRequest, params moveNamespaceParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("moveNamespace_plan called", zap.String("namespace", params.Namespace), zap.String("project", params.Project), zap.String("cluster", params.Cluster))

	clusterID, projectID, namespace, err := t.movedNamespaceObj(ctx, params)
	if err != nil {
		return nil, nil, err
	}

	// The token binds the resolved identity of the move, canonicalized the same
	// way the execute tool does.
	payloadBytes, err := moveNamespacePayload(clusterID, params.Namespace, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal move payload: %w", err)
	}
	op := confirm.Operation{Tool: "moveNamespace", Cluster: params.Cluster, Namespace: params.Namespace, Kind: "namespace", Name: params.Namespace, Payload: payloadBytes}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}

	moveResource := response.NewCreateResourceInput(namespace, clusterID)
	mcpResponse, err := response.CreatePlanResponse([]response.PlanResource{moveResource}, &response.Confirmation{
		Token:     token,
		ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
		Note:      "Show this plan to the user. Only after their explicit approval, call executeChange with operation=moveNamespace and this confirmationToken. The token is single-use and expires in 10 minutes.",
	})
	if err != nil {
		zap.L().Error("failed to create plan response", zap.String("tool", "moveNamespace_plan"), zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}
