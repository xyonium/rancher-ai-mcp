package core

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/utils"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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

// AddKubernetesTools registers the three generic Kubernetes tools with the
// provided MCP server. They are the only tools core still registers directly:
// every Rancher-specific capability is exposed through the merged tools
// (rancherQuery, diagnose, planChange, executeChange), which map enum values
// onto core's handler methods.
func (t *Tools) AddKubernetesTools(mcpServer *mcp.Server) {
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
		Name:        "listAPIResources",
		Meta:        map[string]any{toolsSetAnn: toolsSet},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: `Returns every API resource type (group, version, kind, resource, namespaced) served by the cluster, including all custom resources (CRDs). Use this tool FIRST to discover the correct kind and apiVersion before calling getKubernetesResource, listKubernetesResources, createKubernetesResource, patchKubernetesResource or deleteKubernetesResource with a custom resource.`,
	}, t.listAPIResources)
}
