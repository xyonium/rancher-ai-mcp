package core

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// deleteKubernetesResourceParams defines the structure for deleting a general Kubernetes resource.
type deleteKubernetesResourceParams struct {
	Name              string `json:"name" jsonschema:"the name of the resource to delete"`
	Namespace         string `json:"namespace,omitempty" jsonschema:"the namespace where the resource is located. It must be empty for cluster-wide resources"`
	Kind              string `json:"kind" jsonschema:"the type of Kubernetes resource to delete. Any kind is supported, including custom resources"`
	APIVersion        string `json:"apiVersion,omitempty" jsonschema:"optional API group and version (e.g. harvesterhci.io/v1beta1) to disambiguate custom resources"`
	Cluster           string `json:"cluster" jsonschema:"the name of the Kubernetes cluster"`
	ConfirmationToken string `json:"confirmationToken,omitempty" jsonschema:"REQUIRED: the single-use confirmationToken returned by deleteKubernetesResourcePlan for THIS exact deletion. Never invent, reuse, or guess a token"`
}

// deleteKubernetesResourcePlan fetches the resource to be deleted and returns
// it together with a single-use confirmation token.
func (t *Tools) deleteKubernetesResourcePlan(ctx context.Context, _ *mcp.CallToolRequest, params deleteKubernetesResourceParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("deleteKubernetesResource_plan called")

	gvr, err := t.client.ResolveGVR(ctx, middleware.Token(ctx), params.Cluster, params.Kind, params.APIVersion)
	if err != nil {
		return nil, nil, err
	}

	resourceInterface, err := t.client.GetResourceInterface(ctx, middleware.Token(ctx), params.Namespace, params.Cluster, gvr)
	if err != nil {
		return nil, nil, err
	}

	current, err := resourceInterface.Get(ctx, params.Name, metav1.GetOptions{})
	if err != nil { // NotFound included: planning the deletion of a missing object is an error
		zap.L().Error("failed to get resource to delete", zap.String("tool", "deleteKubernetesResource_plan"), zap.Error(err))
		return nil, nil, fmt.Errorf("cannot plan deletion: %w", err)
	}

	// The resource identity IS the payload of a deletion: there is no manifest
	// or patch to hash, so the token binds exactly which object disappears.
	op := confirm.Operation{Tool: "deleteKubernetesResource", Cluster: params.Cluster, Namespace: params.Namespace, Kind: params.Kind, Name: params.Name}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}

	planResource := response.PlanResource{
		Type:     response.OperationDelete,
		Resource: response.Resource{Name: params.Name, Kind: params.Kind, Cluster: params.Cluster, Namespace: params.Namespace},
		Payload:  current.Object,
	}
	plan, err := response.CreatePlanResponse([]response.PlanResource{planResource}, &response.Confirmation{
		Token:     token,
		ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
		Note:      "Show the user the resource that WILL BE PERMANENTLY DELETED. Only after their explicit approval call deleteKubernetesResource with this confirmationToken. The user will be asked to type the resource name to confirm. The token is single-use and expires in 10 minutes.",
	})
	if err != nil {
		zap.L().Error("failed to create plan response", zap.String("tool", "deleteKubernetesResource_plan"), zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: plan}}}, nil, nil
}
