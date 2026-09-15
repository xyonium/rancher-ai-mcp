package core

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// defaultContainerName returns the pod's first container name, or "" when the
// pod spec carries none. It is used only to fill in the container shown in the
// exec plan (display context); the token never binds the container.
func defaultContainerName(pod *unstructured.Unstructured) string {
	containers, found, err := unstructured.NestedSlice(pod.Object, "spec", "containers")
	if err != nil || !found {
		return ""
	}
	for _, c := range containers {
		container, ok := c.(map[string]any)
		if !ok {
			continue
		}
		name, ok := container["name"].(string)
		if ok && name != "" {
			return name
		}
	}
	return ""
}

// execPodPlan validates that the pod exists, resolves the default container
// when none was given, and returns the exact command together with a single-use
// confirmation token. Nothing is executed: the user confirmation happens at
// execPod time and always shows the exact command again.
func (t *Tools) execPodPlan(ctx context.Context, _ *mcp.CallToolRequest, params execPodParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("execPodPlan called")

	if len(params.Command) == 0 {
		return nil, nil, fmt.Errorf("command must not be empty")
	}

	pod, err := t.client.GetResource(ctx, client.GetParams{
		Cluster:   params.Cluster,
		Kind:      "pod",
		Namespace: params.Namespace,
		Name:      params.Name,
		Token:     middleware.Token(ctx),
	})
	if err != nil { // NotFound included: planning an exec in a missing pod is an error
		zap.L().Error("failed to get pod to exec in", zap.String("tool", "execPodPlan"), zap.Error(err))
		return nil, nil, fmt.Errorf("cannot plan exec in pod %s/%s: %w", params.Namespace, params.Name, err)
	}

	// Resolving the default container is display context for the plan only: the
	// token binds the pod name and the exact command array, never the container
	// — execPod uses params.Container verbatim for the request URL.
	container := params.Container
	if container == "" {
		container = defaultContainerName(pod)
	}

	op := confirm.Operation{Tool: "execPod", Cluster: params.Cluster, Namespace: params.Namespace, Kind: "pod", Name: params.Name, Payload: commandPayload(params.Command)}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}

	planResource := response.PlanResource{
		Type: response.OperationExecute,
		Resource: response.Resource{
			Name: params.Name, Kind: "pod", Cluster: params.Cluster, Namespace: params.Namespace,
		},
		Payload: map[string]any{
			"pod":       params.Name,
			"namespace": params.Namespace,
			"container": container,
			"command":   params.Command,
		},
	}
	plan, err := response.CreatePlanResponse([]response.PlanResource{planResource}, &response.Confirmation{
		Token:     token,
		ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
		Note:      "Show the user the exact command that WILL RUN inside the pod. Only after their explicit approval call execPod with this confirmationToken. The user is asked directly to approve the exact command. The token is single-use and expires in 10 minutes.",
	})
	if err != nil {
		zap.L().Error("failed to create plan response", zap.String("tool", "execPodPlan"), zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: plan}}}, nil, nil
}
