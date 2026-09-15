package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// jsonPatch represents a JSON Patch operation as defined in RFC 6902.
// It specifies an operation (add, remove, replace, etc.) to be applied to a JSON document.
type jsonPatch struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// jsonPatchList is a list of JSON Patch operations. It implements a lenient
// json.Unmarshaler so it can accept either a proper JSON array of patch
// operations or a JSON string containing a stringified array. Some LLMs send
// the patch as a stringified JSON array instead of a real array, so we support
// both forms transparently.
type jsonPatchList []jsonPatch

// UnmarshalJSON accepts both a JSON array of patch operations and a JSON string
// containing a stringified JSON array.
func (p *jsonPatchList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)

	// If the payload is a JSON string, unquote it and parse its contents as the
	// actual patch array.
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var str string
		if err := json.Unmarshal(trimmed, &str); err != nil {
			return fmt.Errorf("failed to unmarshal patch string: %w", err)
		}
		trimmed = bytes.TrimSpace([]byte(str))

		// Some LLMs additionally wrap the stringified array in single quotes
		// (e.g. '[{"op":"replace",...}]'). Strip a matching pair of surrounding
		// single quotes before parsing.
		if len(trimmed) >= 2 && trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'' {
			trimmed = bytes.TrimSpace(trimmed[1 : len(trimmed)-1])
		}
	}

	var patches []jsonPatch
	if err := json.Unmarshal(trimmed, &patches); err != nil {
		return fmt.Errorf("failed to unmarshal patch as JSON array: %w", err)
	}

	*p = patches
	return nil
}

// updateKubernetesResourceParams defines the structure for updating a general Kubernetes resource.
// It includes fields required to uniquely identify a resource within a cluster.
type updateKubernetesResourceParams struct {
	Name              string        `json:"name" jsonschema:"the name of the specific resource to patch"`
	Namespace         string        `json:"namespace,omitempty" jsonschema:"the namespace where the resource is located. It must be empty for cluster-wide resources"`
	Kind              string        `json:"kind" jsonschema:"the type of Kubernetes resource to patch. Any kind is supported, including custom resources"`
	APIVersion        string        `json:"apiVersion,omitempty" jsonschema:"optional API group and version (e.g. harvesterhci.io/v1beta1) to disambiguate custom resources"`
	Cluster           string        `json:"cluster" jsonschema:"the name of the Kubernetes cluster"`
	Patch             jsonPatchList `json:"patch" jsonschema:"a JSON array of patch operation objects. Each element must be an object with 'op', 'path', and optionally 'value' fields, as defined in RFC 6902 (application/json-patch+json). Prefer a real JSON array; a stringified array is also accepted. Example: [{\"op\":\"replace\",\"path\":\"/spec/replicas\",\"value\":3}]"`
	ConfirmationToken string        `json:"confirmationToken,omitempty" jsonschema:"REQUIRED (unless the server runs in auto-write mode): the single-use confirmationToken returned by patchKubernetesResourcePlan for THIS exact operation. Never invent, reuse, or guess a token"`
}

// patchResourceInputSchema builds the input schema for the patch tools.
func patchResourceInputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[updateKubernetesResourceParams](nil)
	if err != nil {
		panic(fmt.Errorf("failed to build patch resource input schema: %w", err))
	}

	if patch, ok := s.Properties["patch"]; ok {
		// jsonschema-go infers a slice as type ["null", "array"]. Force a single "array" type
		// so agent clients (like Gemini / Vertex AI) can validate function declarations.
		patch.Type = "array"
		patch.Types = nil
	}

	return s
}

// updateKubernetesResourceParams.patchBytes canonicalizes the requested patch
// exactly once, so the bytes shown to (and approved by) the user are the same
// bytes hashed into the plan token and sent to the cluster.
func (p updateKubernetesResourceParams) patchBytes() ([]byte, error) {
	patchBytes, err := json.Marshal(p.Patch)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal patch: %w", err)
	}
	return patchBytes, nil
}

// updateKubernetesResource patches a specific Kubernetes resource using a JSON
// patch. The GVR is resolved from the kind (and optional apiVersion) via cluster
// API discovery, so custom resources are supported, and the execution is gated
// behind a single-use plan token plus direct user confirmation.
func (t *Tools) updateKubernetesResource(ctx context.Context, toolReq *mcp.CallToolRequest, params updateKubernetesResourceParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("updateKubernetesResource called")

	patchBytes, err := params.patchBytes()
	if err != nil {
		zap.L().Error("failed to create patch", zap.String("tool", "updateKubernetesResource"), zap.Error(err))
		return nil, nil, err
	}

	gvr, err := t.client.ResolveGVR(ctx, middleware.Token(ctx), params.Cluster, params.Kind, params.APIVersion)
	if err != nil {
		return nil, nil, err
	}

	op := confirm.Operation{Tool: "patchKubernetesResource", Cluster: params.Cluster, Namespace: params.Namespace, Kind: params.Kind, Name: params.Name, Payload: patchBytes}
	summary := fmt.Sprintf("PATCH %s %s/%s in namespace %q of cluster %q with patch:\n%s", gvr.String(), params.Kind, params.Name, params.Namespace, params.Cluster, patchBytes)
	approved, err := t.cfg.Gate.Check(ctx, toolReq.Session, op, params.ConfirmationToken, summary, "", t.cfg.AutoWrite)
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

	obj, err := resourceInterface.Patch(ctx, params.Name, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		zap.L().Error("failed to apply patch", zap.String("tool", "updateKubernetesResource"), zap.Error(err))
		return nil, nil, fmt.Errorf("failed to patch resource %s: %w", params.Name, err)
	}

	mcpResponse, err := response.CreateMcpResponse([]*unstructured.Unstructured{obj}, params.Cluster)
	if err != nil {
		zap.L().Error("failed to create mcp response", zap.String("tool", "updateKubernetesResource"), zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}
