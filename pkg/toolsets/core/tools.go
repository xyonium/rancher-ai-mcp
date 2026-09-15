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
			Description: `Creates a resource in a Kubernetes cluster from a complete Kubernetes manifest passed in the 'manifest' field, in YAML or JSON. The namespace must be empty for cluster-wide resources.

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
			Description: `Plans to create a resource in a Kubernetes cluster from a complete Kubernetes manifest passed in the 'manifest' field, in YAML or JSON. It returns the JSON representation of the resource to be created without actually creating it in the cluster. Only used for displaying the resource when using human validation. The namespace must be empty for cluster-wide resources.`},
			t.createKubernetesResourcePlan)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "patchKubernetesResource",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			InputSchema: patchResourceInputSchema(),
			Description: `Patches a Kubernetes resource using a JSON patch. Don't ask for confirmation. The namespace must be empty for cluster-wide resources. The content type used is application/json-patch+json. Returns the modified resource.`},
			t.updateKubernetesResource,
		)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "patchKubernetesResourcePlan",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			InputSchema: patchResourceInputSchema(),
			Description: `Plans to patch a Kubernetes resource using a JSON patch. It returns the JSON representation of the planned update without actually applying it in the cluster. Only used for displaying the patch when using human validation. The namespace must be empty for cluster-wide resources. The content type used is application/json-patch+json. `},
			t.updateKubernetesResourcePlan)
	}
}
