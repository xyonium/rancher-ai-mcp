package provisioning

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// QueryCases returns provisioning's slice of the rancherQuery dispatch table.
// Field mapping note: the merged "name" parameter carries the machine name
// for "clusterMachine".
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return map[string]dispatch.Case[dispatch.QueryParams]{
		"clusterMachine": {
			Required: []string{"cluster", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getClusterMachine(ctx, req, getClusterMachineParams{Cluster: p.Cluster, MachineName: p.Name})
			},
		},
		"k3kClusters": {
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getK3kClusters(ctx, req, getK3kClustersParams{Clusters: p.Clusters})
			},
		},
		"supportedVersions": {
			Required: []string{"distribution"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listSupportedKubernetesVersions(ctx, req, listSupportedK8sVersionsParams{Distribution: p.Distribution})
			},
		},
	}
}

// DiagnoseCases returns provisioning's slice of the diagnose dispatch table.
func (t *Tools) DiagnoseCases() map[string]dispatch.Case[dispatch.DiagnoseParams] {
	return map[string]dispatch.Case[dispatch.DiagnoseParams]{
		"cluster": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.analyzeCluster(ctx, req, inspectClusterParams{Cluster: p.Cluster, Namespace: p.Namespace})
			},
		},
		"machines": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.analyzeClusterMachines(ctx, req, inspectClusterMachinesParams{Cluster: p.Cluster, Namespace: p.Namespace})
			},
		},
	}
}

// customClusterParams maps flat merged params to createCustomClusterParams.
func customClusterParams(p dispatch.ChangeParams) createCustomClusterParams {
	return createCustomClusterParams{
		Name: p.Name, Description: p.Description,
		CNI: p.CNI, Version: p.Version, Distribution: p.Distribution,
		ConfirmationToken: p.ConfirmationToken,
	}
}

// importedClusterParams maps flat merged params to createImportedClusterParams.
func importedClusterParams(p dispatch.ChangeParams) createImportedClusterParams {
	return createImportedClusterParams{
		Name: p.Name, Description: p.Description,
		VersionManagementSetting: p.VersionManagementSetting,
		ConfirmationToken:        p.ConfirmationToken,
	}
}

// k3kClusterParams maps flat merged params to createK3kClusterParams.
func k3kClusterParams(p dispatch.ChangeParams) createK3kClusterParams {
	return createK3kClusterParams{
		Name: p.Name, Namespace: p.Namespace, TargetCluster: p.TargetCluster,
		Version: p.Version, Mode: p.Mode, Servers: p.Servers, Agents: p.Agents,
		Sync:              SyncConfig{PriorityClasses: p.Sync.PriorityClasses, Ingresses: p.Sync.Ingresses},
		Persistence:       PersistenceConfig{Type: p.Persistence.Type, StorageClassName: p.Persistence.StorageClassName, StorageRequest: p.Persistence.StorageRequest},
		ServerLimit:       ResourceLimits{CPU: p.ServerLimit.CPU, Memory: p.ServerLimit.Memory},
		WorkerLimit:       ResourceLimits{CPU: p.WorkerLimit.CPU, Memory: p.WorkerLimit.Memory},
		ConfirmationToken: p.ConfirmationToken,
	}
}

// scaleParams maps flat merged params to scaleNodePoolParameters.
func scaleParams(p dispatch.ChangeParams) scaleNodePoolParameters {
	return scaleNodePoolParameters{
		Cluster: p.Cluster, Namespace: p.Namespace, NodePoolName: p.NodePoolName,
		DesiredSize: p.DesiredSize, AmountToAdd: p.AmountToAdd, AmountToSubtract: p.AmountToSubtract,
		ConfirmationToken: p.ConfirmationToken,
	}
}

// PlanCases returns provisioning's slice of the planChange dispatch table.
func (t *Tools) PlanCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createCustomCluster": {
			Required: []string{"name", "CNI", "version", "distribution"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createCustomClusterPlan(ctx, req, customClusterParams(p))
			},
		},
		"createImportedCluster": {
			Required: []string{"name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createImportedClusterPlan(ctx, req, importedClusterParams(p))
			},
		},
		"createK3kCluster": {
			Required: []string{"name", "namespace", "targetCluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createK3kClusterPlan(ctx, req, k3kClusterParams(p))
			},
		},
		"scaleClusterNodePool": {
			Required: []string{"cluster", "namespace", "nodePoolName"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.scaleClusterNodePoolPlan(ctx, req, scaleParams(p))
			},
		},
	}
}

// ExecuteCases returns provisioning's slice of the executeChange dispatch table.
func (t *Tools) ExecuteCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createCustomCluster": {
			Required: []string{"name", "CNI", "version", "distribution"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createCustomCluster(ctx, req, customClusterParams(p))
			},
		},
		"createImportedCluster": {
			Required: []string{"name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createImportedCluster(ctx, req, importedClusterParams(p))
			},
		},
		"createK3kCluster": {
			Required: []string{"name", "namespace", "targetCluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createK3kCluster(ctx, req, k3kClusterParams(p))
			},
		},
		"scaleClusterNodePool": {
			Required: []string{"cluster", "namespace", "nodePoolName"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.scaleClusterNodePool(ctx, req, scaleParams(p))
			},
		},
	}
}
