package rbac

import (
	"context"

	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type toolsClient interface {
	GetClusterID(ctx context.Context, token string, clusterNameOrID string) (string, error)
	GetResource(ctx context.Context, params client.GetParams) (*unstructured.Unstructured, error)
	GetResources(ctx context.Context, params client.ListParams) ([]*unstructured.Unstructured, error)
	GetResourceInterface(ctx context.Context, token string, namespace string, cluster string, gvr schema.GroupVersionResource) (dynamic.ResourceInterface, error)
}

// Tools contains tools for interacting with RBAC in Rancher.
type Tools struct {
	client   toolsClient
	ReadOnly bool
}

// NewTools creates and returns a new Tools instance.
func NewTools(client toolsClient, readOnly bool) *Tools {
	return &Tools{
		client:   client,
		ReadOnly: readOnly,
	}
}

// The RBAC tools have no AddTools method: listClusterRoleTemplateBindings,
// listProjectRoleTemplateBindings, listRoleTemplates, getUser and
// getRoleTemplate are exposed as cases of the merged rancherQuery tool, all of
// them read-only. The Tools type only holds the handlers and their client.
