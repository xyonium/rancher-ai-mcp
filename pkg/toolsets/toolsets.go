package toolsets

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/merged"
)

// AddAllTools adds all available tools to the MCP server. The merged package is
// the only registration surface: it registers the 3 k8s-generic tools plus the
// 4 enum-dispatched merged tools (5 tools in read-only mode).
func AddAllTools(client *client.Client, mcpServer *mcp.Server, cfg toolconfig.Config) {
	merged.Register(client, mcpServer, cfg)
}
