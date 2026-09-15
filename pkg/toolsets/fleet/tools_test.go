package fleet

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

// listRegisteredTools connects an in-memory MCP client to a server with the
// fleet tools registered and returns the tools by name.
func listRegisteredTools(t *testing.T) map[string]*mcp.Tool {
	t.Helper()
	tools := NewTools(test.WrapClient(&client.Client{}, "fakeToken"))
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v1.0.0"}, nil)
	tools.AddTools(server)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)
	clientSession, err := mcpClient.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	defer clientSession.Close()

	list, err := clientSession.ListTools(t.Context(), &mcp.ListToolsParams{})
	require.NoError(t, err)

	byName := make(map[string]*mcp.Tool, len(list.Tools))
	for _, tool := range list.Tools {
		byName[tool.Name] = tool
	}
	return byName
}

// TestFleetToolAnnotations pins the safety contract advertised to clients: every
// fleet tool is read-only and must be annotated as such.
func TestFleetToolAnnotations(t *testing.T) {
	tools := listRegisteredTools(t)

	for _, name := range []string{"getBundle", "getGitRepo", "listGitRepos", "analyzeFleetResources"} {
		tool, ok := tools[name]
		require.True(t, ok, "tool %s must be registered", name)
		require.NotNil(t, tool.Annotations, "tool %s must carry annotations", name)
		assert.True(t, tool.Annotations.ReadOnlyHint, "tool %s must be annotated as read-only", name)
		assert.Equal(t, ptr.To(false), tool.Annotations.OpenWorldHint, "tool %s must not be open-world", name)
	}
}
