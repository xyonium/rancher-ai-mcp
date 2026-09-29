package projects

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/converter"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type moveNamespaceParams struct {
	Namespace string `json:"namespace" jsonschema:"the name of the namespace to move"`
	Project   string `json:"project" jsonschema:"the name or ID of the destination project"`
	Cluster   string `json:"cluster" jsonschema:"the name of the cluster containing the namespace and project"`

	ConfirmationToken string `json:"confirmationToken,omitempty" jsonschema:"REQUIRED (unless the server runs in auto-write mode): the single-use confirmationToken returned by planChange (operation moveNamespace) for THIS exact operation. Never invent, reuse, or guess a token"`
}

// moveNamespace assigns a namespace to a project. The move is gated behind a
// single-use plan token plus a direct user confirmation, and the token binds
// the resolved cluster ID, namespace and destination project ID.
func (t *Tools) moveNamespace(ctx context.Context, toolReq *mcp.CallToolRequest, params moveNamespaceParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("moveNamespace called", zap.String("namespace", params.Namespace), zap.String("project", params.Project), zap.String("cluster", params.Cluster))

	clusterID, projectID, namespace, err := t.movedNamespaceObj(ctx, params)
	if err != nil {
		return nil, nil, err
	}

	// The token binds the resolved identity of the move, canonicalized the same
	// way the plan tool does: any drift in name-to-ID resolution between plan
	// and execute invalidates the token and forces a fresh plan.
	payloadBytes, err := moveNamespacePayload(clusterID, params.Namespace, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal move payload: %w", err)
	}
	op := confirm.Operation{Tool: "moveNamespace", Cluster: params.Cluster, Namespace: params.Namespace, Kind: "namespace", Name: params.Namespace, Payload: payloadBytes}
	summary := fmt.Sprintf("MOVE namespace %s in cluster %q to project %q (ID %s). The namespace's field.cattle.io/projectId label and annotation will be set to %s and %s:%s respectively.",
		params.Namespace, params.Cluster, params.Project, projectID, projectID, clusterID, projectID)
	approved, err := t.cfg.Gate.Check(ctx, toolReq.Session, op, params.ConfirmationToken, summary, "", t.cfg.AutoWrite)
	if err != nil {
		return nil, nil, err
	}
	if !approved {
		return confirm.CancelledResult(), nil, nil
	}

	resourceInterface, err := t.client.GetResourceInterface(
		ctx, middleware.Token(ctx), "", clusterID, converter.K8sKindsToGVRs["namespace"])
	if err != nil {
		return nil, nil, err
	}

	updatedNamespace, err := resourceInterface.Update(ctx, namespace, metav1.UpdateOptions{})
	if err != nil {
		zap.L().Error("failed to move namespace", zap.String("tool", "moveNamespace"), zap.Error(err))
		return nil, nil, fmt.Errorf("failed to move namespace %q: %w", params.Namespace, err)
	}

	mcpResponse, err := response.CreateMcpResponse([]*unstructured.Unstructured{updatedNamespace}, clusterID)
	if err != nil {
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}

// movedNamespaceObj resolves the cluster and destination project IDs and
// returns the namespace object as it will look after the move (projectId
// label and annotation set), WITHOUT persisting anything. Both the plan and
// the execute handlers build on it so the gated payload and the shown plan
// always describe the same change.
func (t *Tools) movedNamespaceObj(ctx context.Context, params moveNamespaceParams) (clusterID, projectID string, updated *unstructured.Unstructured, err error) {
	clusterID, err = t.client.GetClusterID(ctx, middleware.Token(ctx), params.Cluster)
	if err != nil {
		return "", "", nil, err
	}

	projectID, _, err = GetProjectID(ctx, t.client, middleware.Token(ctx), clusterID, params.Project)
	if err != nil {
		return "", "", nil, err
	}

	namespace, err := t.client.GetResource(ctx, client.GetParams{
		Cluster: clusterID,
		Kind:    "namespace",
		Name:    params.Namespace,
		Token:   middleware.Token(ctx),
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to get namespace %q: %w", params.Namespace, err)
	}

	labels := namespace.GetLabels()
	if labels == nil {
		labels = make(map[string]string)
	}
	labels["field.cattle.io/projectId"] = projectID
	namespace.SetLabels(labels)

	annotations := namespace.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations["field.cattle.io/projectId"] = fmt.Sprintf("%s:%s", clusterID, projectID)
	namespace.SetAnnotations(annotations)

	return clusterID, projectID, namespace, nil
}

// moveNamespacePayload canonicalizes the identity a moveNamespace token
// binds: the resolved cluster ID, the namespace, and the resolved destination
// project ID. Map keys marshal in sorted order, so plan and execute produce
// byte-identical payloads for the same resolved inputs.
func moveNamespacePayload(clusterID, namespace, projectID string) ([]byte, error) {
	return json.Marshal(map[string]string{
		"cluster":   clusterID,
		"namespace": namespace,
		"project":   projectID,
	})
}
