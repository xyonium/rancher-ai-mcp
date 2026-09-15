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

func (t *Tools) createCustomClusterPlan(_ context.Context, toolReq *mcp.CallToolRequest, params createCustomClusterParams) (*mcp.CallToolResult, any, error) {
	log := utils.NewChildLogger(toolReq, map[string]string{
		"Name":         params.Name,
		"Description":  params.Description,
		"CNI":          params.CNI,
		"Version":      params.Version,
		"Distribution": params.Distribution,
	})

	log.Debug("Planning custom cluster creation")

	unstructuredObj, err := t.CreateCustomClusterObj(toolReq, params, log)
	if err != nil {
		log.Error("failed to create custom cluster object", zap.Error(err))
		return nil, nil, err
	}

	// The token binds the exact cluster object being created, canonicalized the
	// same way the execute tool does.
	payloadBytes, err := json.Marshal(unstructuredObj.Object)
	if err != nil {
		log.Error("failed to marshal custom cluster object", zap.Error(err))
		return nil, nil, fmt.Errorf("failed to marshal custom cluster object: %w", err)
	}
	op := confirm.Operation{Tool: "createCustomCluster", Cluster: LocalCluster, Namespace: DefaultClusterResourcesNamespace, Kind: "cluster", Name: params.Name, Payload: payloadBytes}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}

	createResource := response.NewCreateResourceInput(unstructuredObj, LocalCluster)
	mcpResponse, err := response.CreatePlanResponse([]response.PlanResource{createResource}, &response.Confirmation{
		Token:     token,
		ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
		Note:      "Show this plan to the user. Only after their explicit approval, call createCustomCluster with this confirmationToken. The token is single-use and expires in 10 minutes.",
	})
	if err != nil {
		zap.L().Error("failed to create plan response", zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}
