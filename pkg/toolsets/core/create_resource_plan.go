package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
)

// createKubernetesResourcePlan parses the manifest and returns the planned
// creation plus a single-use confirmation token for the matching Write call.
func (t *Tools) createKubernetesResourcePlan(_ context.Context, _ *mcp.CallToolRequest, params createKubernetesResourceParams) (*mcp.CallToolResult, any, error) {
	unstructuredObj, namespace, err := parseCreateManifest(params)
	if err != nil {
		return nil, nil, err
	}
	gvk := unstructuredObj.GroupVersionKind()
	payloadBytes, err := json.Marshal(unstructuredObj.Object)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to canonicalize manifest: %w", err)
	}
	op := confirm.Operation{Tool: "createKubernetesResource", Cluster: params.Cluster, Namespace: namespace, Kind: gvk.Kind, Name: unstructuredObj.GetName(), Payload: payloadBytes}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}
	plan, err := response.CreatePlanResponse(
		[]response.PlanResource{response.NewCreateResourceInput(unstructuredObj, params.Cluster)},
		&response.Confirmation{
			Token:     token,
			ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
			Note:      "Show this plan to the user. Only after their explicit approval, call createKubernetesResource with this confirmationToken. The token is single-use and expires in 10 minutes.",
		})
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: plan}}}, nil, nil
}
