package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// createKubernetesResourceParams defines the structure for creating a general Kubernetes resource.
type createKubernetesResourceParams struct {
	Name              string `json:"name" jsonschema:"the name of the resource to create. It must match metadata.name in the manifest"`
	Namespace         string `json:"namespace,omitempty" jsonschema:"the namespace where the resource is located. It must be empty for cluster-wide resources"`
	Kind              string `json:"kind" jsonschema:"the type of Kubernetes resource (e.g., Pod, Deployment, or any custom resource kind). It must match the manifest's kind"`
	Cluster           string `json:"cluster" jsonschema:"the name of the Kubernetes cluster"`
	Manifest          string `json:"manifest" jsonschema:"the resource to create as a complete Kubernetes manifest, in YAML or JSON. The GVR is resolved from the manifest's own apiVersion and kind, so any custom resource is supported"`
	ConfirmationToken string `json:"confirmationToken,omitempty" jsonschema:"REQUIRED (unless the server runs in auto-write mode): the single-use confirmationToken returned by createKubernetesResourcePlan for THIS exact operation. Never invent, reuse, or guess a token"`
}

// parseCreateManifest parses and validates the manifest of a create request. The
// manifest is authoritative: its apiVersion and kind drive GVR resolution, and
// the kind/name parameters, when set, must agree with it. It returns the object
// and the namespace to operate in (the parameter wins over metadata.namespace).
func parseCreateManifest(params createKubernetesResourceParams) (*unstructured.Unstructured, string, error) {
	unstructuredObj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(params.Manifest), unstructuredObj); err != nil {
		return nil, "", fmt.Errorf("failed to parse manifest (expected YAML or JSON): %w", err)
	}
	gvk := unstructuredObj.GroupVersionKind()
	if gvk.Kind == "" || gvk.Version == "" {
		return nil, "", fmt.Errorf("manifest must set apiVersion and kind")
	}
	if !strings.EqualFold(params.Kind, gvk.Kind) {
		return nil, "", fmt.Errorf("kind parameter %q does not match manifest kind %q", params.Kind, gvk.Kind)
	}
	if params.Name != "" && params.Name != unstructuredObj.GetName() {
		return nil, "", fmt.Errorf("name parameter %q does not match manifest metadata.name %q", params.Name, unstructuredObj.GetName())
	}
	namespace := params.Namespace
	if namespace == "" {
		namespace = unstructuredObj.GetNamespace()
	}
	return unstructuredObj, namespace, nil
}

// createKubernetesResource creates a new Kubernetes resource. The GVR is
// resolved from the manifest's own apiVersion and kind, and the execution is
// gated behind a single-use plan token plus direct user confirmation.
func (t *Tools) createKubernetesResource(ctx context.Context, toolReq *mcp.CallToolRequest, params createKubernetesResourceParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("createKubernetesResource called")

	unstructuredObj, namespace, err := parseCreateManifest(params)
	if err != nil {
		return nil, nil, err
	}
	gvk := unstructuredObj.GroupVersionKind()

	gvr, err := t.client.ResolveGVR(ctx, middleware.Token(ctx), params.Cluster, gvk.Kind, gvk.GroupVersion().String())
	if err != nil {
		return nil, nil, err
	}

	payloadBytes, err := json.Marshal(unstructuredObj.Object)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to canonicalize manifest: %w", err)
	}
	op := confirm.Operation{Tool: "createKubernetesResource", Cluster: params.Cluster, Namespace: namespace, Kind: gvk.Kind, Name: unstructuredObj.GetName(), Payload: payloadBytes}
	summary := fmt.Sprintf("CREATE %s %s/%s in namespace %q of cluster %q with manifest:\n%s", gvr.String(), gvk.Kind, unstructuredObj.GetName(), namespace, params.Cluster, params.Manifest)
	approved, err := t.cfg.Gate.Check(ctx, toolReq.Session, op, params.ConfirmationToken, summary, "", t.cfg.AutoWrite)
	if err != nil {
		return nil, nil, err
	}
	if !approved {
		return confirm.CancelledResult(), nil, nil
	}

	resourceInterface, err := t.client.GetResourceInterface(ctx, middleware.Token(ctx), namespace, params.Cluster, gvr)
	if err != nil {
		return nil, nil, err
	}
	obj, err := resourceInterface.Create(ctx, unstructuredObj, metav1.CreateOptions{})
	if err != nil {
		zap.L().Error("failed to create resource", zap.String("tool", "createKubernetesResource"), zap.Error(err))
		return nil, nil, fmt.Errorf("failed to create resource %s: %w", params.Name, err)
	}

	mcpResponse, err := response.CreateMcpResponse([]*unstructured.Unstructured{obj}, params.Cluster)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}}}, nil, nil
}
