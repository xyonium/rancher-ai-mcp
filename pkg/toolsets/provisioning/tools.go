package provisioning

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/utils/ptr"
)

const (
	ToolsSet    = "provisioning"
	toolsSetAnn = "toolset"
)

type toolsClient interface {
	GetResource(ctx context.Context, params client.GetParams) (*unstructured.Unstructured, error)
	GetResourceAtAnyAPIVersion(ctx context.Context, params client.GetParams) (*unstructured.Unstructured, error)
	GetResourcesAtAnyAPIVersion(ctx context.Context, params client.ListParams) ([]*unstructured.Unstructured, error)
	GetResourceByGVR(ctx context.Context, params client.GetParams, gvr schema.GroupVersionResource) (*unstructured.Unstructured, error)
	GetResources(ctx context.Context, params client.ListParams) ([]*unstructured.Unstructured, error)
	GetResourceInterface(ctx context.Context, token string, namespace string, cluster string, gvr schema.GroupVersionResource) (dynamic.ResourceInterface, error)
	RancherURL() string
}

// Tools contains tools for accessing provisioning information.
type Tools struct {
	client toolsClient
	cfg    toolconfig.Config
}

// NewTools creates and returns a new Tools instance.
func NewTools(client toolsClient, cfg toolconfig.Config) *Tools {
	return &Tools{
		client: client,
		cfg:    cfg,
	}
}

func (t *Tools) AddTools(mcpServer *mcp.Server) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "analyzeCluster",
		Meta: map[string]any{
			toolsSetAnn: ToolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr.To(false)},
		Description: `Gets a cluster's complete configuration including provisioning and management clusters, the CAPI cluster, CAPI machines, and machine pool configs.
This should be used when a complete overview of the clusters current state and its configuration is required.`},
		t.analyzeCluster)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "analyzeClusterMachines",
		Meta: map[string]any{
			toolsSetAnn: ToolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr.To(false)},
		Description: `Gets all Machine related resources for a cluster including Machines, MachineSets, and MachineDeployments.
This should be used when a summary or overview of just the existing machine resources is required.`},
		t.analyzeClusterMachines)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "getClusterMachine",
		Meta: map[string]any{
			toolsSetAnn: ToolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr.To(false)},
		Description: `Gets a specific machine and its parent MachineSet and MachineDeployment.
This should be used when detailed information about a specific machine is required.`},
		t.getClusterMachine)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "listK3kClusters",
		Meta: map[string]any{
			toolsSetAnn: ToolsSet,
		},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"clusters": map[string]any{
					"type":        "array",
					"description": "list of clusters to get virtual clusters from. Empty to return virtual clusters for all clusters",
					"items": map[string]any{
						"type": "string",
					},
				},
			},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr.To(false)},
		Description: `List K3k virtual clusters deployed across downstream clusters.`},
		t.getK3kClusters)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "listSupportedKubernetesVersions",
		Meta: map[string]any{
			toolsSetAnn: ToolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr.To(false)},
		Description: `Returns the currently supported rke2 and k3s versions that can be provisioned.
This should only be used when information about the supported rke2 and k3s is needed. This is often required to support provisioning custom and imported clusters.`},
		t.listSupportedKubernetesVersions)

	if !t.cfg.ReadOnly {
		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "scaleClusterNodePool",
			Meta: map[string]any{
				toolsSetAnn: ToolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr.To(true), IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool SCALES a node pool of an existing cluster and changes its state. Protocol, no exceptions: (1) Call scaleClusterNodePoolPlan first and show the user the exact patch. (2) Obtain the user's EXPLICIT approval for THIS EXACT change. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over; never scale proactively or in batches.

Changes the size of an existing node pool for an rke2 or k3s cluster.
This should be used when the user wants to change the size of an existing node pool for an rke2 or k3s cluster.
Pools cannot be scaled to zero nodes, and etcd node pools cannot be scaled below 3 nodes to prevent loss of quorum.
The local cluster does not support node pool scaling.`},
			t.scaleClusterNodePool)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "scaleClusterNodePoolPlan",
			Meta: map[string]any{
				toolsSetAnn: ToolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool only PLANS a change; it changes nothing. Returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may the matching Write tool be called with this token.

Plans to change the size of an existing node pool for an rke2 or k3s cluster. It returns the JSON representation of the updated node pool resource without actually applying the change in the cluster.
This should be used when the user wants to change the size of an existing node pool for an rke2 or k3s cluster.
Pools cannot be scaled to zero nodes, and etcd node pools cannot be scaled below 3 nodes to prevent loss of quorum.
The local cluster does not support node pool scaling.`},
			t.scaleClusterNodePoolPlan)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "createK3kCluster",
			Meta: map[string]any{
				toolsSetAnn: ToolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool CREATES a cluster and changes its state. Protocol, no exceptions: (1) Call createK3kClusterPlan first and show the user the complete cluster object. (2) Obtain the user's EXPLICIT approval for THIS EXACT creation. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over to any other operation; never create resources proactively.

Create a new K3k cluster in a specific downstream cluster.`},
			t.createK3kCluster)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "createK3kClusterPlan",
			Meta: map[string]any{
				toolsSetAnn: ToolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool only PLANS a creation; it changes nothing. Returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may the matching Write tool be called with this token.

Plans to create a new K3k cluster in a specific downstream cluster. It returns the JSON representation of the resource to be created without actually creating it.`},
			t.createK3kClusterPlan)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "createImportedCluster",
			Meta: map[string]any{
				toolsSetAnn: ToolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool CREATES a cluster and changes its state. Protocol, no exceptions: (1) Call createImportedClusterPlan first and show the user the complete cluster object. (2) Obtain the user's EXPLICIT approval for THIS EXACT creation. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over to any other operation; never create resources proactively.

Creates an imported cluster within Rancher.
This should only be used when the user wants to create a new imported cluster. Do not use this tool when the user asks to create a new custom cluster.`},
			t.createImportedCluster)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "createImportedClusterPlan",
			Meta: map[string]any{
				toolsSetAnn: ToolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool only PLANS a creation; it changes nothing. Returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may the matching Write tool be called with this token.

Plans to create an imported cluster within Rancher. It returns the JSON representation of the resource to be created without actually creating it in the cluster.
This should only be used when the user wants to create a new imported cluster. Do not use this tool when the user asks to create a new custom cluster.`},
			t.createImportedClusterPlan)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "createCustomCluster",
			Meta: map[string]any{
				toolsSetAnn: ToolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool CREATES a cluster and changes its state. Protocol, no exceptions: (1) Call createCustomClusterPlan first and show the user the complete cluster object. (2) Obtain the user's EXPLICIT approval for THIS EXACT creation. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over to any other operation; never create resources proactively.

Creates a custom cluster within Rancher.
This should only be used when the user wants to create a new custom cluster. Do not use this tool if a user asks to create an imported cluster.`},
			t.createCustomCluster)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "createCustomClusterPlan",
			Meta: map[string]any{
				toolsSetAnn: ToolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool only PLANS a creation; it changes nothing. Returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may the matching Write tool be called with this token.

Plans to create a custom cluster within Rancher. It returns the JSON representation of the resource to be created without actually creating it in the cluster.
This should only be used when the user wants to create a new custom cluster. Do not use this tool if a user asks to create an imported cluster.`},
			t.createCustomClusterPlan)
	}

}
