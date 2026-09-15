package provisioning

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/converter"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"github.com/rancher/rancher-ai-mcp/pkg/utils"
	"go.uber.org/zap"
)

func (t *Tools) createImportedClusterPlan(_ context.Context, toolReq *mcp.CallToolRequest, params createImportedClusterParams) (*mcp.CallToolResult, any, error) {
	log := utils.NewChildLogger(toolReq, map[string]string{
		"clusterName":              params.Name,
		"clusterDescription":       params.Description,
		"versionManagementSetting": params.VersionManagementSetting,
	})

	log.Debug("Planning imported cluster creation")

	cluster, err := t.createImportedClusterObj(params)
	if err != nil {
		log.Error("failed to plan imported cluster creation", zap.Error(err))
		return nil, nil, fmt.Errorf("failed to plan imported cluster creation: %w", err)
	}

	// The token binds the exact object the execute tool submits, so it must be
	// computed before the display-only fields below are added to the plan.
	payloadBytes, err := cluster.MarshalJSON()
	if err != nil {
		log.Error("failed to marshal cluster object to JSON", zap.Error(err))
		return nil, nil, fmt.Errorf("failed to marshal cluster object to JSON: %w", err)
	}
	op := confirm.Operation{Tool: "createImportedCluster", Cluster: LocalCluster, Kind: "cluster", Name: params.Name, Payload: payloadBytes}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}

	// While not required for the norman API request to create the cluster,
	// we depend on these fields to show the confirmation message
	// in the Rancher UI.
	cluster.SetKind("Cluster")
	cluster.SetAPIVersion(converter.ManagementGroup + "/v3")
	cluster.SetNamespace("fleet-default")
	cluster.SetName(params.Name)

	createResource := response.NewCreateResourceInput(cluster, LocalCluster)
	mcpResponse, err := response.CreatePlanResponse([]response.PlanResource{createResource}, &response.Confirmation{
		Token:     token,
		ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
		Note:      "Show this plan to the user. Only after their explicit approval, call createImportedCluster with this confirmationToken. The token is single-use and expires in 10 minutes.",
	})
	if err != nil {
		zap.L().Error("failed to create plan response", zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}
