package provisioning

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/converter"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"github.com/rancher/rancher-ai-mcp/pkg/utils"
	"go.uber.org/zap"
)

func (t *Tools) scaleClusterNodePoolPlan(ctx context.Context, toolReq *mcp.CallToolRequest, params scaleNodePoolParameters) (*mcp.CallToolResult, any, error) {
	if params.Namespace == "" || params.Namespace == "default" {
		params.Namespace = DefaultClusterResourcesNamespace
	}

	log := utils.NewChildLogger(toolReq, map[string]string{
		"cluster_id":       params.Cluster,
		"namespace":        params.Namespace,
		"nodePoolName":     params.NodePoolName,
		"desiredSize":      strconv.Itoa(params.DesiredSize),
		"amountToAdd":      strconv.Itoa(params.AmountToAdd),
		"amountToSubtract": strconv.Itoa(params.AmountToSubtract),
	})

	log.Debug("Planning cluster node pool scale operation")

	patchBytes, err := t.scaleClusterNodePoolPatch(ctx, toolReq, params, log)
	if err != nil {
		log.Error("failed to determine patch for scaling node pool", zap.Error(err))
		return nil, nil, err
	}

	// The token binds the exact patch bytes the execute tool will send.
	op := confirm.Operation{Tool: "scaleClusterNodePool", Cluster: params.Cluster, Namespace: params.Namespace, Kind: "nodepool", Name: params.NodePoolName, Payload: patchBytes}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}

	updateResource := response.PlanResource{
		Type:    response.OperationUpdate,
		Payload: json.RawMessage(patchBytes),
		Resource: response.Resource{
			Name:      params.Cluster,
			Kind:      converter.ProvisioningClusterResourceKind,
			Cluster:   LocalCluster,
			Namespace: params.Namespace,
		},
	}

	mcpResponse, err := response.CreatePlanResponse([]response.PlanResource{updateResource}, &response.Confirmation{
		Token:     token,
		ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
		Note:      "Show this plan to the user. Only after their explicit approval, call scaleClusterNodePool with this confirmationToken. The token is single-use and expires in 10 minutes.",
	})
	if err != nil {
		zap.L().Error("failed to create plan response", zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}
