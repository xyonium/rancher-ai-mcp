package core

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/core/projects"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/core/rbac"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/provisioning"
	"github.com/rancher/rancher-ai-mcp/pkg/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

const (
	toolsSet    = "rancher"
	toolsSetAnn = "toolset"
)

type toolsClient interface {
	GetResource(ctx context.Context, params client.GetParams) (*unstructured.Unstructured, error)
	GetResourceInterface(ctx context.Context, token string, namespace string, cluster string, gvr schema.GroupVersionResource) (dynamic.ResourceInterface, error)
	GetResources(ctx context.Context, params client.ListParams) ([]*unstructured.Unstructured, error)
	CreateClientSet(ctx context.Context, token string, cluster string) (kubernetes.Interface, error)
	GetClusterID(ctx context.Context, token string, clusterNameOrID string) (string, error)
	ResolveGVR(ctx context.Context, token, cluster, kind, apiVersion string) (schema.GroupVersionResource, error)
	ListAPIResources(ctx context.Context, token, cluster string) ([]*metav1.APIResourceList, error)
	CreateRestConfig(token string, clusterID string) (*rest.Config, error)
}

// Tools contains all tools for the MCP server
type Tools struct {
	client    toolsClient
	paginator utils.Paginator
	cfg       toolconfig.Config
}

// NewTools creates and returns a new Tools instance.
func NewTools(client toolsClient, cfg toolconfig.Config) *Tools {
	return &Tools{
		client:    client,
		paginator: utils.NewResourcePaginator(),
		cfg:       cfg,
	}
}

// AddTools registers all Rancher Kubernetes tools with the provided MCP server.
// Each tool is configured with metadata identifying it as part of the rancher toolset.
func (t *Tools) AddTools(mcpServer *mcp.Server) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "getKubernetesResource",
		Meta: map[string]any{
			toolsSetAnn: toolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: `Fetches a Kubernetes resource from the cluster. The namespace must be empty for all namespaces or cluster-wide resources.

Supports any resource kind including custom resources. If the kind is unknown to the built-in table, it is resolved via cluster API discovery; use apiVersion or a group-qualified kind (group/Kind or Kind.group) to disambiguate, and the listAPIResources tool to discover available types.`},
		t.getResource,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "listKubernetesResources",
		Meta: map[string]any{
			toolsSetAnn: toolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: `Returns a list of Kubernetes resources. The namespace must be empty for all namespaces or cluster-wide resources. Supports an optional JSONPath predicate to filter which resources are returned.

Results are paginated with limit (page size, default 100) and offset (how many resources to skip from the start, default 0). To page through results, keep limit the same and increase offset by limit each time: offset=0 is the first page, offset=100 is the second page, offset=200 is the third page, and so on (with limit=100). When more resources remain, the response includes the exact offset value to pass in for the next page.

Supports any resource kind including custom resources. If the kind is unknown to the built-in table, it is resolved via cluster API discovery; use apiVersion or a group-qualified kind (group/Kind or Kind.group) to disambiguate, and the listAPIResources tool to discover available types.`},
		t.listKubernetesResources,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "inspectPod",
		Meta: map[string]any{
			toolsSetAnn: toolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: `Returns all information related to a Pod. It includes its parent Deployment or StatefulSet, the CPU and memory consumption and the logs. It must be used for troubleshooting problems with pods.`},
		t.inspectPod,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "getDeployment",
		Meta: map[string]any{
			toolsSetAnn: toolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: `Returns a Deployment and its Pods. It must be used for troubleshooting problems with deployments.`},
		t.getDeploymentDetails,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "getNodeMetrics",
		Meta: map[string]any{
			toolsSetAnn: toolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: `Returns a list of all nodes in a specified Kubernetes cluster, including their current resource utilization metrics.`},
		t.getNodes,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "getClusterImages",
		Meta: map[string]any{
			toolsSetAnn: toolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"clusters": map[string]any{
					"type":        "array",
					"description": "list of clusters to get images from. Empty to return images for all clusters",
					"items": map[string]any{
						"type": "string",
					},
				},
			},
		},
		Description: `Returns all container images running across the specified clusters, along with the pods (name and namespace) using each image. Use in priority this tool to audit clusters for container registry or image usage, or to find which pods are running a specific container image. If clusters is empty, returns data for all clusters.`},
		t.getClusterImages,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "listClusters",
		Meta: map[string]any{
			toolsSetAnn: toolsSet + "," + provisioning.ToolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		// InputSchema explicitly includes "properties" to satisfy OpenAI's requirement
		// that object schemas must have a "properties" field, even when there are no parameters.
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Description: `Returns a list of all Rancher clusters, including local and downstream clusters.`},
		t.listClusters,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "listAPIResources",
		Meta:        map[string]any{toolsSetAnn: toolsSet},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: `Returns every API resource type (group, version, kind, resource, namespaced) served by the cluster, including all custom resources (CRDs). Use this tool FIRST to discover the correct kind and apiVersion before calling getKubernetesResource, listKubernetesResources, createKubernetesResource, patchKubernetesResource or deleteKubernetesResource with a custom resource.`,
	}, t.listAPIResources)

	projects.NewTools(t.client, t.cfg).AddTools(mcpServer)

	rbac.NewTools(t.client, t.cfg.ReadOnly).AddTools(mcpServer)

	if !t.cfg.ReadOnly {
		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "createKubernetesResource",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: toolconfig.SecurityProtocol(t.cfg, `SECURITY: This tool CREATES a resource in the cluster and changes its state. Protocol, no exceptions: (1) Call createKubernetesResourcePlan first and show the user the complete manifest. (2) Obtain the user's EXPLICIT approval for THIS EXACT creation. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over to any other operation; never create resources proactively.`) + `

Creates a resource in a Kubernetes cluster from a complete Kubernetes manifest passed in the 'manifest' field, in YAML or JSON. Any resource kind is supported, including custom resources: the target API is resolved from the manifest's own apiVersion and kind via cluster API discovery. The namespace must be empty for cluster-wide resources.

Example of the manifest parameter (YAML):
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
data:
  key: value`},
			t.createKubernetesResource,
		)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "createKubernetesResourcePlan",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool only PLANS a creation; it changes nothing. It returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may createKubernetesResource be called with this confirmationToken.

Plans to create a resource in a Kubernetes cluster from a complete Kubernetes manifest passed in the 'manifest' field, in YAML or JSON. Any resource kind is supported, including custom resources: the target API is resolved from the manifest's own apiVersion and kind via cluster API discovery. The namespace must be empty for cluster-wide resources.`},
			t.createKubernetesResourcePlan)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "patchKubernetesResource",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr.To(true), IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			InputSchema: patchResourceInputSchema(),
			Description: toolconfig.SecurityProtocol(t.cfg, `SECURITY: This tool MODIFIES an existing resource in the cluster. Protocol, no exceptions: (1) Call patchKubernetesResourcePlan first and show the user the exact patch. (2) Obtain the user's EXPLICIT approval for THIS EXACT patch. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over; never patch proactively or in batches.`) + `

Patches a Kubernetes resource using a JSON patch. Any resource kind is supported, including custom resources (use apiVersion or a group-qualified kind to disambiguate). The namespace must be empty for cluster-wide resources. The content type used is application/json-patch+json. Returns the modified resource.`},
			t.updateKubernetesResource,
		)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "patchKubernetesResourcePlan",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			InputSchema: patchResourceInputSchema(),
			Description: `SECURITY: This tool only PLANS an update; it changes nothing. It returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may patchKubernetesResource be called with this confirmationToken.

Plans to patch a Kubernetes resource using a JSON patch. It returns the JSON representation of the planned update without actually applying it in the cluster. Any resource kind is supported, including custom resources (use apiVersion or a group-qualified kind to disambiguate). The namespace must be empty for cluster-wide resources. The content type used is application/json-patch+json. `},
			t.updateKubernetesResourcePlan)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "deleteKubernetesResource",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr.To(true), IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool PERMANENTLY DELETES a resource from the cluster. This is irreversible. Protocol, no exceptions: (1) Call deleteKubernetesResourcePlan first and show the user the full resource that will be deleted. (2) Obtain the user's EXPLICIT approval for THIS EXACT deletion. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to type the resource name to confirm — you cannot and MUST NOT answer on their behalf. This tool ALWAYS requires confirmation, even in auto-write mode. Approval never carries over; NEVER batch deletions; NEVER delete proactively.

Deletes a Kubernetes resource. Any resource kind is supported, including custom resources (use apiVersion or a group-qualified kind to disambiguate). The namespace must be empty for cluster-wide resources.`},
			t.deleteKubernetesResource,
		)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "deleteKubernetesResourcePlan",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool only PLANS a deletion; it changes nothing. It fetches the resource and returns it together with a single-use confirmationToken. Show the plan to the user; only after their explicit approval may deleteKubernetesResource be called with this confirmationToken.

Plans to delete a Kubernetes resource. It returns the current resource that would be permanently deleted, without actually deleting it in the cluster. Any resource kind is supported, including custom resources (use apiVersion or a group-qualified kind to disambiguate). The namespace must be empty for cluster-wide resources.`},
			t.deleteKubernetesResourcePlan)
	}

	// The exec tools are the most dangerous tools this server can offer, so
	// they are opt-in: they exist only when the operator explicitly started the
	// server with --enable-exec (and never in read-only mode).
	if !t.cfg.ReadOnly && t.cfg.EnableExec {
		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "execPod",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr.To(true), IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			InputSchema: execPodInputSchema(),
			Description: `SECURITY: This tool EXECUTES AN ARBITRARY COMMAND inside a pod — the most powerful and dangerous operation this server offers. Protocol, no exceptions: (1) Call execPodPlan first and show the user the exact command. (2) Obtain the user's EXPLICIT approval for THIS EXACT command. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to approve — you cannot and MUST NOT answer on their behalf. This tool ALWAYS requires confirmation, even in auto-write mode. Approval never carries over; never chain or batch commands; never run a command the user has not seen and approved.

Executes a command in a pod container (non-interactive, no shell unless explicitly requested by the user). The command runs with a 30 second timeout; stdout and stderr are captured and truncated to 64KB each.`},
			t.execPod,
		)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name:        "execPodPlan",
			Meta:        map[string]any{toolsSetAnn: toolsSet},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			InputSchema: execPodInputSchema(),
			Description: `SECURITY: This tool only PLANS a command execution; it changes nothing. It validates the pod and returns the exact command plus a single-use confirmationToken. Show the exact command to the user; only after their explicit approval may execPod be called with this confirmationToken. The user is then asked directly to approve the exact command.

Plans to execute an arbitrary command in a pod container (non-interactive, no shell unless explicitly requested by the user). The namespace must be the pod's namespace.`,
		}, t.execPodPlan)
	}
}
