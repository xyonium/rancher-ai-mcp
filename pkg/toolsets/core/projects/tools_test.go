package projects

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
// projects tools registered under the given config and returns the tools by name.
func listRegisteredTools(t *testing.T, cfg toolconfig.Config) map[string]*mcp.Tool {
	t.Helper()
	tools := NewTools(test.WrapClient(&client.Client{}, "fakeToken"), cfg)
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

// TestProjectsToolAnnotations pins the safety contract advertised to clients:
// every read-only tool must be annotated read-only, and createProject must be
// advertised as a non-read-only, non-idempotent, non-open-world write.
func TestProjectsToolAnnotations(t *testing.T) {
	tools := listRegisteredTools(t, toolconfig.Config{})

	for _, name := range []string{"getProject", "listProjects", "getResourceUsage"} {
		tool, ok := tools[name]
		require.True(t, ok, "tool %s must be registered", name)
		require.NotNil(t, tool.Annotations, "tool %s must carry annotations", name)
		assert.True(t, tool.Annotations.ReadOnlyHint, "tool %s must be annotated as read-only", name)
		assert.Equal(t, ptr.To(false), tool.Annotations.OpenWorldHint)
	}

	create, ok := tools["createProject"]
	require.True(t, ok)
	require.NotNil(t, create.Annotations)
	assert.False(t, create.Annotations.ReadOnlyHint)
	assert.False(t, create.Annotations.IdempotentHint)
	assert.Nil(t, create.Annotations.DestructiveHint, "an additive create is not destructive")
	assert.Equal(t, ptr.To(false), create.Annotations.OpenWorldHint)
	assert.Contains(t, create.Description, "SECURITY:", "the write tool must state the confirmation protocol")
	assert.Contains(t, create.Description, "createProjectPlan", "the protocol must name the matching plan tool")
	assert.NotContains(t, create.Description, "Don't ask for confirmation")

	plan, ok := tools["createProjectPlan"]
	require.True(t, ok)
	require.NotNil(t, plan.Annotations)
	assert.False(t, plan.Annotations.ReadOnlyHint, "the plan tool mints tokens and is not read-only")
	assert.Contains(t, plan.Description, "confirmationToken")

	// The plan tools advertise the mandated sentence verbatim, exactly as the
	// provisioning suite pins it for its plan tools.
	const planSentence = "Returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may the matching Write tool be called with this token."
	assert.Contains(t, plan.Description, planSentence, "createProjectPlan must carry the mandated sentence verbatim")
}

// TestProjectsToolsReadOnlyMode proves read-only mode registers no write tools.
func TestProjectsToolsReadOnlyMode(t *testing.T) {
	tools := listRegisteredTools(t, toolconfig.Config{ReadOnly: true})

	_, ok := tools["createProject"]
	assert.False(t, ok, "createProject must not be registered in read-only mode")
	_, ok = tools["createProjectPlan"]
	assert.False(t, ok, "createProjectPlan must not be registered in read-only mode")

	// The read-only tools must still be there.
	for _, name := range []string{"getProject", "listProjects", "getResourceUsage"} {
		assert.Contains(t, tools, name)
	}
}

// TestProjectsAutoWriteDescription proves the spec §5.1 requirement that in
// auto-write mode createProject's description truthfully declares the
// automation mode; the default-mode text is pinned separately above.
func TestProjectsAutoWriteDescription(t *testing.T) {
	tools := listRegisteredTools(t, toolconfig.Config{AutoWrite: true})

	create := tools["createProject"]
	require.NotNil(t, create, "createProject must be registered in auto-write mode")
	desc := create.Description
	assert.Contains(t, desc, "AUTO-WRITE", "createProject must declare the automation mode")
	assert.Contains(t, desc, "IMMEDIATELY", "createProject must state it executes immediately")
	assert.Contains(t, desc, "NO confirmationToken", "createProject must state no token is needed")
	assert.Contains(t, desc, "NO server-initiated user confirmation", "createProject must state the server will not ask the user")
	assert.Contains(t, desc, "deleteKubernetesResource", "createProject must name deleteKubernetesResource as still gated")
	assert.Contains(t, desc, "execPod", "createProject must name execPod as still gated")
	assert.Contains(t, desc, "ALWAYS require", "createProject must state delete/exec always require the full protocol")
	assert.Contains(t, desc, "ONLY when the user has explicitly asked", "createProject must require an explicit user request")
	assert.NotContains(t, desc, "The server then asks the USER DIRECTLY to confirm",
		"createProject must not promise a confirmation in auto-write mode")
	assert.NotContains(t, desc, "(3) Call this tool with the confirmationToken",
		"createProject must not require the plan token in auto-write mode")

	// The plan tool keeps minting tokens in every mode.
	plan := tools["createProjectPlan"]
	require.NotNil(t, plan)
	assert.Contains(t, plan.Description, "single-use confirmationToken")
	assert.NotContains(t, plan.Description, "AUTO-WRITE")
}
