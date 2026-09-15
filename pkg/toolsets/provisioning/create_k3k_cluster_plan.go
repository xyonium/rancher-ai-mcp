package provisioning

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"github.com/rancher/rancher-ai-mcp/pkg/utils"
	"go.uber.org/zap"
)

func (t *Tools) createK3kClusterPlan(_ context.Context, toolReq *mcp.CallToolRequest, params createK3kClusterParams) (*mcp.CallToolResult, any, error) {
	log := utils.NewChildLogger(toolReq, map[string]string{
		"clusterName":   params.Name,
		"namespace":     params.Namespace,
		"targetCluster": params.TargetCluster,
	})

	log.Debug("Planning K3k cluster creation")

	if params.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	if params.Namespace == "" {
		return nil, nil, fmt.Errorf("namespace is required")
	}
	if params.TargetCluster == "" {
		return nil, nil, fmt.Errorf("targetCluster is required")
	}

	obj := t.createK3kClusterObj(params)

	// The token binds the exact cluster object being created, canonicalized the
	// same way the execute tool does.
	payloadBytes, err := json.Marshal(obj.Object)
	if err != nil {
		log.Error("failed to marshal K3k cluster object", zap.Error(err))
		return nil, nil, fmt.Errorf("failed to marshal K3k cluster object: %w", err)
	}
	op := confirm.Operation{Tool: "createK3kCluster", Cluster: params.TargetCluster, Namespace: params.Namespace, Kind: "cluster", Name: params.Name, Payload: payloadBytes}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}

	createResource := response.NewCreateResourceInput(obj, params.TargetCluster)
	mcpResponse, err := response.CreatePlanResponse([]response.PlanResource{createResource}, &response.Confirmation{
		Token:     token,
		ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
		Note:      "Show this plan to the user. Only after their explicit approval, call createK3kCluster with this confirmationToken. The token is single-use and expires in 10 minutes.",
	})
	if err != nil {
		log.Error("failed to create plan response", zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}
