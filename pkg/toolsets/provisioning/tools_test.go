package provisioning

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

// listRegisteredTools connects an in-memory MCP client to a server with the
// provisioning tools registered under the given config and returns them by name.
func listRegisteredTools(t *testing.T, cfg toolconfig.Config) map[string]*mcp.Tool {
	t.Helper()
	tools := NewTools(test.WrapClient(&client.Client{}, testToken), cfg)
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

// TestProvisioningWriteToolMetadata pins the safety contract advertised to
// clients for all four write pairs: each execute tool must state the
// plan-token protocol, must name its matching plan tool, must never tell the
// agent to skip user confirmation, and must carry the create/patch annotations.
func TestProvisioningWriteToolMetadata(t *testing.T) {
	tools := listRegisteredTools(t, toolconfig.Config{})

	createTools := []string{"createK3kCluster", "createImportedCluster", "createCustomCluster"}
	planTools := []string{"createK3kClusterPlan", "createImportedClusterPlan", "createCustomClusterPlan", "scaleClusterNodePoolPlan"}

	for _, name := range append(append([]string{"scaleClusterNodePool"}, createTools...), planTools...) {
		tool, ok := tools[name]
		require.True(t, ok, "tool %s must be registered", name)
		require.NotNil(t, tool.Annotations, "tool %s must carry annotations", name)
		assert.False(t, tool.Annotations.ReadOnlyHint, "tool %s is a write tool", name)
		assert.False(t, tool.Annotations.IdempotentHint, "tool %s is not idempotent", name)
		assert.Equal(t, ptr.To(false), tool.Annotations.OpenWorldHint)
		assert.Contains(t, tool.Description, "SECURITY:", "tool %s must state the confirmation protocol", name)
		assert.NotContains(t, tool.Description, "Don't ask for confirmation")
	}

	// The four execute tools carry the exact protocol sentences and name their
	// matching plan tool.
	protocolTools := map[string]string{
		"scaleClusterNodePool":  "scaleClusterNodePoolPlan",
		"createK3kCluster":      "createK3kClusterPlan",
		"createImportedCluster": "createImportedClusterPlan",
		"createCustomCluster":   "createCustomClusterPlan",
	}
	for exec, plan := range protocolTools {
		desc := tools[exec].Description
		assert.Contains(t, desc, "(1) Call "+plan+" first and show the user", "%s must point at %s", exec, plan)
		assert.Contains(t, desc, "(2) Obtain the user's EXPLICIT approval for THIS EXACT", "%s must demand explicit approval", exec)
		assert.Contains(t, desc, "(3) Call this tool with the confirmationToken from the plan response", "%s must require the plan token", exec)
		assert.Contains(t, desc, "The server then asks the USER DIRECTLY to confirm", "%s must state the server asks the user", exec)
		assert.Contains(t, desc, "Approval never carries over", "%s must state approval is per-operation", exec)
	}

	// scaleClusterNodePool is a patch-class tool: destructive, unlike creates.
	assert.Equal(t, ptr.To(true), tools["scaleClusterNodePool"].Annotations.DestructiveHint)
	for _, name := range createTools {
		assert.Nil(t, tools[name].Annotations.DestructiveHint, "%s is additive", name)
	}

	// The plan tools advertise the mandated sentence verbatim.
	const planSentence = "Returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may the matching Write tool be called with this token."
	for _, name := range planTools {
		assert.Contains(t, tools[name].Description, planSentence, "plan tool %s must carry the mandated sentence", name)
	}
}

// TestProvisioningReadOnlyMode proves read-only mode registers no write tools.
func TestProvisioningReadOnlyMode(t *testing.T) {
	tools := listRegisteredTools(t, toolconfig.Config{ReadOnly: true})

	for _, name := range []string{"scaleClusterNodePool", "scaleClusterNodePoolPlan", "createK3kCluster", "createK3kClusterPlan",
		"createImportedCluster", "createImportedClusterPlan", "createCustomCluster", "createCustomClusterPlan"} {
		assert.NotContains(t, tools, name, "%s must not be registered in read-only mode", name)
	}

	// Read-only tools stay available.
	for _, name := range []string{"analyzeCluster", "getClusterMachine", "listK3kClusters", "listSupportedKubernetesVersions"} {
		assert.Contains(t, tools, name)
	}
}
