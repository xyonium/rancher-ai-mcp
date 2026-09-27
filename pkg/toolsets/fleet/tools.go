package fleet

import (
	"context"

	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

const (
	toolsSet    = "fleet"
	toolsSetAnn = "toolset"
)

type toolsClient interface {
	GetResource(ctx context.Context, params client.GetParams) (*unstructured.Unstructured, error)
	GetResources(ctx context.Context, params client.ListParams) ([]*unstructured.Unstructured, error)
	CreateRestConfig(token string, clusterID string) (*rest.Config, error)
}

type resourceAnalyzer interface {
	analyzeFleetResources(ctx context.Context, restCfg *rest.Config, namespace string) (string, error)
}

// Tools contains all tools for the MCP server
type Tools struct {
	client           toolsClient
	resourceAnalyzer resourceAnalyzer
}

// NewTools creates and returns a new Tools instance.
func NewTools(client toolsClient) *Tools {
	return &Tools{
		client:           client,
		resourceAnalyzer: newCLI(),
	}
}

// The Fleet tools have no AddTools method: getBundle, getGitRepo, listGitRepos
// and analyzeFleetResources are exposed as cases of the merged rancherQuery and
// diagnose tools, all of them read-only. The Tools type only holds the handlers
// and their clients.
