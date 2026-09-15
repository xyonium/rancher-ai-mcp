package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// deleteKubernetesResource permanently deletes a Kubernetes resource. The GVR is
// resolved from the kind (and optional apiVersion) via cluster API discovery, so
// custom resources are supported. Unlike create/update-class tools this one is
// NEVER exempted from confirmation: the single-use plan token and the direct user
// confirmation (where the user must type the exact resource name) are always
// required, even when the server runs in auto-write mode.
func (t *Tools) deleteKubernetesResource(ctx context.Context, toolReq *mcp.CallToolRequest, params deleteKubernetesResourceParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("deleteKubernetesResource called")

	gvr, err := t.client.ResolveGVR(ctx, middleware.Token(ctx), params.Cluster, params.Kind, params.APIVersion)
	if err != nil {
		return nil, nil, err
	}

	// The resource identity is the whole operation: there is no payload to bind,
	// so the token's namespace/kind/name fields carry the entire binding.
	op := confirm.Operation{Tool: "deleteKubernetesResource", Cluster: params.Cluster, Namespace: params.Namespace, Kind: params.Kind, Name: params.Name}
	summary := fmt.Sprintf("DELETE %s %s/%s in namespace %q of cluster %q. This PERMANENTLY DELETES the resource and cannot be undone.", gvr.String(), params.Kind, params.Name, params.Namespace, params.Cluster)
	// typedName is the resource name the user must retype, and the final
	// argument is a hard-coded false: delete is never bypassed, not even in
	// auto-write mode.
	approved, err := t.cfg.Gate.Check(ctx, toolReq.Session, op, params.ConfirmationToken, summary, params.Name, false)
	if err != nil {
		return nil, nil, err
	}
	if !approved {
		return confirm.CancelledResult(), nil, nil
	}

	resourceInterface, err := t.client.GetResourceInterface(ctx, middleware.Token(ctx), params.Namespace, params.Cluster, gvr)
	if err != nil {
		return nil, nil, err
	}

	if err := resourceInterface.Delete(ctx, params.Name, metav1.DeleteOptions{}); err != nil {
		zap.L().Error("failed to delete resource", zap.String("tool", "deleteKubernetesResource"), zap.Error(err))
		return nil, nil, fmt.Errorf("failed to delete resource %s: %w", params.Name, err)
	}

	mcpResponse, err := response.CreateMcpResponseAny(
		map[string]any{
			"deleted":   params.Name,
			"kind":      params.Kind,
			"cluster":   params.Cluster,
			"namespace": params.Namespace,
		},
		response.UIContext{
			Namespace: params.Namespace,
			Kind:      params.Kind,
			Cluster:   params.Cluster,
			Name:      params.Name,
			Type:      strings.ToLower(params.Kind),
		},
	)
	if err != nil {
		zap.L().Error("failed to create mcp response", zap.String("tool", "deleteKubernetesResource"), zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}
