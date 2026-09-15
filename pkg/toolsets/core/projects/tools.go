package projects

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

// AddTools registers all project tools with the provided MCP server.
func (t *Tools) AddTools(mcpServer *mcp.Server) {
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "getProject",
		Meta: map[string]any{
			toolsSetAnn: toolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr.To(false)},
		Description: `Returns a project resource and its associated namespaces and members.`},
		t.getProject,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "listProjects",
		Meta: map[string]any{
			toolsSetAnn: toolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr.To(false)},
		Description: `Returns a list of project resources for a specified cluster.`},
		t.listProjects,
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "getResourceUsage",
		Meta: map[string]any{
			toolsSetAnn: toolsSet,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr.To(false)},
		Description: `Returns the resource usage for a namespace, project or all projects in a cluster.
Usage totals are provided for the entire project as well as broken down by namespace.
The resource usage includes CPU and memory requests, limits and actual usage, as well as the total number of pods.`},
		t.getResourceUsage,
	)

	if !t.cfg.ReadOnly {
		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "createProject",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: toolconfig.SecurityProtocol(t.cfg, `SECURITY: This tool CREATES a project in the cluster and changes its state. Protocol, no exceptions: (1) Call createProjectPlan first and show the user the complete project object. (2) Obtain the user's EXPLICIT approval for THIS EXACT creation. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over to any other operation; never create resources proactively.`) + `

Creates a project resource for a specified cluster with the given containerResourceQuota.`},
			t.createProject)

		mcp.AddTool(mcpServer, &mcp.Tool{
			Name: "createProjectPlan",
			Meta: map[string]any{
				toolsSetAnn: toolsSet,
			},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
			Description: `SECURITY: This tool only PLANS a creation; it changes nothing. Returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may the matching Write tool be called with this token.

Plans to create a project resource for a specified cluster. It returns the JSON representation of the project to be created without actually creating it in the cluster.`},
			t.createProjectPlan)
	}
}
