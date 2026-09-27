package projects

import (
	"context"

	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	toolsSet     = "rancher"
	toolsSetAnn  = "toolset"
	LocalCluster = "local"
)

type toolsClient interface {
	GetClusterID(ctx context.Context, token string, clusterNameOrID string) (string, error)
	GetResource(ctx context.Context, params client.GetParams) (*unstructured.Unstructured, error)
	GetResources(ctx context.Context, params client.ListParams) ([]*unstructured.Unstructured, error)
	GetResourceInterface(ctx context.Context, token string, namespace string, cluster string, gvr schema.GroupVersionResource) (dynamic.ResourceInterface, error)
}

// Tools contains tools for accessing project information.
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

// The project tools have no AddTools method: getProject, listProjects,
// getResourceUsage and createProject are exposed as cases of the merged
// rancherQuery / planChange / executeChange tools. The Tools type only holds
// the handlers and the clients they need.
