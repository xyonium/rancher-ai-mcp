package core

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// errExecDisabled is returned when the execPod operation is used but the
// server was not started with --enable-exec. The merged enum always lists the
// operation; availability is enforced here, at runtime, because the merged
// tool is registered regardless of the flag.
var errExecDisabled = errors.New("operation execPod is disabled: the server must be started with --enable-exec")

// patchList converts the flat raw patch into the handler's jsonPatchList,
// which accepts both a JSON array and a stringified array. It gives the merged
// tool the same lenient decoding the dedicated patch tool gets from
// jsonPatchList.UnmarshalJSON.
func patchList(raw json.RawMessage) (jsonPatchList, error) {
	var pl jsonPatchList
	if err := json.Unmarshal(raw, &pl); err != nil {
		return nil, err
	}
	return pl, nil
}

// createParams maps flat merged params to createKubernetesResourceParams.
// There is no apiVersion field: the manifest carries it.
func createParams(p dispatch.ChangeParams) createKubernetesResourceParams {
	return createKubernetesResourceParams{
		Name: p.Name, Namespace: p.Namespace, Kind: p.Kind, Cluster: p.Cluster, Manifest: p.Manifest,
		ConfirmationToken: p.ConfirmationToken,
	}
}

// updateParams maps flat merged params to updateKubernetesResourceParams.
func updateParams(p dispatch.ChangeParams, pl jsonPatchList) updateKubernetesResourceParams {
	return updateKubernetesResourceParams{
		Name: p.Name, Namespace: p.Namespace, Kind: p.Kind, APIVersion: p.APIVersion, Cluster: p.Cluster, Patch: pl,
		ConfirmationToken: p.ConfirmationToken,
	}
}

// deleteParams maps flat merged params to deleteKubernetesResourceParams.
func deleteParams(p dispatch.ChangeParams) deleteKubernetesResourceParams {
	return deleteKubernetesResourceParams{
		Name: p.Name, Namespace: p.Namespace, Kind: p.Kind, APIVersion: p.APIVersion, Cluster: p.Cluster,
		ConfirmationToken: p.ConfirmationToken,
	}
}

// execParams maps flat merged params to execPodParams.
func execParams(p dispatch.ChangeParams) execPodParams {
	return execPodParams{
		Cluster: p.Cluster, Namespace: p.Namespace, Name: p.Name, Container: p.Container, Command: p.Command,
		ConfirmationToken: p.ConfirmationToken,
	}
}

// execGated runs fn only when the server was started with --enable-exec. The
// merged enum always lists execPod; this is the runtime enforcement of the
// flag for both the plan and the execute case.
func execGated(t *Tools, fn func() (*mcp.CallToolResult, any, error)) (*mcp.CallToolResult, any, error) {
	if !t.cfg.EnableExec {
		return nil, nil, errExecDisabled
	}
	return fn()
}

// PlanCases returns core's slice of the planChange dispatch table. The plan
// handlers ignore ConfirmationToken (they issue one) and never touch the client
// state, so mapping it through is harmless and keeps one converter per
// operation for both phases.
func (t *Tools) PlanCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createKubernetesResource": {
			Required: []string{"cluster", "kind", "name", "manifest"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createKubernetesResourcePlan(ctx, req, createParams(p))
			},
		},
		"patchKubernetesResource": {
			Required: []string{"cluster", "kind", "name", "patch"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				pl, err := patchList(p.Patch)
				if err != nil {
					return nil, nil, err
				}
				return t.updateKubernetesResourcePlan(ctx, req, updateParams(p, pl))
			},
		},
		"deleteKubernetesResource": {
			Required: []string{"cluster", "kind", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.deleteKubernetesResourcePlan(ctx, req, deleteParams(p))
			},
		},
		"execPod": {
			Required: []string{"cluster", "namespace", "name", "command"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return execGated(t, func() (*mcp.CallToolResult, any, error) {
					return t.execPodPlan(ctx, req, execParams(p))
				})
			},
		},
	}
}

// ExecuteCases returns core's slice of the executeChange dispatch table.
// The handlers are the unchanged gated handlers: they validate the token
// against Operation{Tool: <operation name>} and run the user confirmation.
// ConfirmationToken is forwarded verbatim — the gate is the safety mechanism.
func (t *Tools) ExecuteCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createKubernetesResource": {
			Required: []string{"cluster", "kind", "name", "manifest"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createKubernetesResource(ctx, req, createParams(p))
			},
		},
		"patchKubernetesResource": {
			Required: []string{"cluster", "kind", "name", "patch"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				pl, err := patchList(p.Patch)
				if err != nil {
					return nil, nil, err
				}
				return t.updateKubernetesResource(ctx, req, updateParams(p, pl))
			},
		},
		"deleteKubernetesResource": {
			Required: []string{"cluster", "kind", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.deleteKubernetesResource(ctx, req, deleteParams(p))
			},
		},
		"execPod": {
			Required: []string{"cluster", "namespace", "name", "command"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return execGated(t, func() (*mcp.CallToolResult, any, error) {
					return t.execPod(ctx, req, execParams(p))
				})
			},
		},
	}
}
