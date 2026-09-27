package provisioning

import (
	"context"

	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
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

// The provisioning tools have no AddTools method: analyzeCluster,
// analyzeClusterMachines, getClusterMachine, listK3kClusters,
// listSupportedKubernetesVersions, scaleClusterNodePool, createK3kCluster,
// createImportedCluster and createCustomCluster are exposed as cases of the
// merged rancherQuery / diagnose / planChange / executeChange tools. The Tools
// type only holds the handlers and their clients.
