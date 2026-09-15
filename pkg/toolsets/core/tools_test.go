package core

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/provisioning"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
)

func TestAddTools(t *testing.T) {
	c, _ := client.NewClient(true, "")
	tools := NewTools(c, toolconfig.Config{})

	// Create a test MCP server
	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "test-server",
		Version: "v1.0.0",
	}, nil)
	assert.NotNil(t, mcpServer)

	tools.AddTools(mcpServer)

	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{})

	// Start server on a random available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	defer listener.Close()

	serverAddr := "http://" + listener.Addr().String()

	server := &http.Server{Handler: handler}
	go func() {
		server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	// Wait for server to be ready by attempting to connect with retries
	ctx := context.Background()
	transport := &mcp.StreamableClientTransport{
		Endpoint: serverAddr,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)

	var cs *mcp.ClientSession
	assert.Eventually(t, func() bool {
		var err error
		cs, err = client.Connect(ctx, transport, nil)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond, "Server should start within 2 seconds")

	assert.NotNil(t, cs)
	defer cs.Close()

	toolsResult, err := cs.ListTools(ctx, &mcp.ListToolsParams{})

	assert.NoError(t, err)
	assert.Len(t, toolsResult.Tools, 24, "incorrect number of tools registered")
	// assert that all tools have the correct toolset annotation
	toolNames := make(map[string]bool, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		toolNames[tool.Name] = true
		if tool.Name == "listClusters" {
			assert.Equal(t, toolsSet+","+provisioning.ToolsSet, tool.Meta[toolsSetAnn])
		} else {
			assert.Equal(t, toolsSet, tool.Meta[toolsSetAnn])
		}
	}
	// The exec tools are off by default: they only exist behind --enable-exec.
	assert.False(t, toolNames["execPod"], "execPod must not be registered without EnableExec")
	assert.False(t, toolNames["execPodPlan"], "execPodPlan must not be registered without EnableExec")
}

// TestAddToolsExecEnabled proves --enable-exec adds exactly the exec pair on top
// of the default set, with the wire-level safety contract of the exec tool.
func TestAddToolsExecEnabled(t *testing.T) {
	c, _ := client.NewClient(true, "")
	tools := NewTools(c, toolconfig.Config{EnableExec: true})

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v1.0.0"}, nil)
	tools.AddTools(mcpServer)

	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	server := &http.Server{Handler: handler}
	go func() {
		server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	ctx := context.Background()
	transport := &mcp.StreamableClientTransport{Endpoint: "http://" + listener.Addr().String()}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)

	var cs *mcp.ClientSession
	assert.Eventually(t, func() bool {
		var err error
		cs, err = mcpClient.Connect(ctx, transport, nil)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond, "Server should start within 2 seconds")
	require.NotNil(t, cs)
	defer cs.Close()

	toolsResult, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	require.NoError(t, err)
	assert.Len(t, toolsResult.Tools, 26, "enable-exec must add exactly the two exec tools")

	byName := make(map[string]*mcp.Tool, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		byName[tool.Name] = tool
	}

	exec := byName["execPod"]
	require.NotNil(t, exec, "execPod must be registered when EnableExec is set")
	require.NotNil(t, exec.Annotations)
	assert.False(t, exec.Annotations.ReadOnlyHint)
	assert.Equal(t, ptr.To(true), exec.Annotations.DestructiveHint, "exec must be advertised as destructive")
	assert.False(t, exec.Annotations.IdempotentHint)
	assert.Equal(t, ptr.To(false), exec.Annotations.OpenWorldHint)
	assert.True(t, strings.HasPrefix(exec.Description, "SECURITY: "), "exec description must open with the SECURITY block")
	assert.Contains(t, exec.Description, "confirmationToken")
	assert.Contains(t, exec.Description, "ALWAYS requires confirmation, even in auto-write mode")
	assert.NotContains(t, exec.Description, "Don't ask for confirmation")

	plan := byName["execPodPlan"]
	require.NotNil(t, plan, "execPodPlan must be registered when EnableExec is set")
	require.NotNil(t, plan.Annotations)
	assert.False(t, plan.Annotations.ReadOnlyHint)
	assert.Nil(t, plan.Annotations.DestructiveHint, "the plan tool changes nothing and must not be marked destructive")
	assert.True(t, strings.HasPrefix(plan.Description, "SECURITY: "), "plan description must open with the SECURITY block")
	assert.Contains(t, plan.Description, "confirmationToken")
}

// TestPatchToolMetadata pins the safety contract advertised to clients: the
// patch execute tool must declare its destructive nature and must never tell
// the agent to skip user confirmation.
func TestPatchToolMetadata(t *testing.T) {
	c, _ := client.NewClient(true, "")
	tools := NewTools(c, toolconfig.Config{})

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v1.0.0"}, nil)
	tools.AddTools(mcpServer)

	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	defer listener.Close()

	serverAddr := "http://" + listener.Addr().String()
	server := &http.Server{Handler: handler}
	go func() {
		server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	ctx := context.Background()
	transport := &mcp.StreamableClientTransport{Endpoint: serverAddr}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)

	var cs *mcp.ClientSession
	assert.Eventually(t, func() bool {
		var err error
		cs, err = mcpClient.Connect(ctx, transport, nil)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond, "Server should start within 2 seconds")
	require.NotNil(t, cs)
	defer cs.Close()

	toolsResult, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	require.NoError(t, err)

	byName := make(map[string]*mcp.Tool, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		byName[tool.Name] = tool
	}

	patch := byName["patchKubernetesResource"]
	require.NotNil(t, patch, "patchKubernetesResource must be registered")
	require.NotNil(t, patch.Annotations)
	assert.False(t, patch.Annotations.ReadOnlyHint)
	assert.Equal(t, ptr.To(true), patch.Annotations.DestructiveHint, "patch must be advertised as destructive")
	assert.False(t, patch.Annotations.IdempotentHint)
	assert.Equal(t, ptr.To(false), patch.Annotations.OpenWorldHint)
	assert.True(t, strings.HasPrefix(patch.Description, "SECURITY: "), "patch description must open with the SECURITY block")
	assert.Contains(t, patch.Description, "confirmationToken")
	assert.NotContains(t, patch.Description, "Don't ask for confirmation")

	plan := byName["patchKubernetesResourcePlan"]
	require.NotNil(t, plan, "patchKubernetesResourcePlan must be registered")
	require.NotNil(t, plan.Annotations)
	assert.False(t, plan.Annotations.ReadOnlyHint)
	assert.Nil(t, plan.Annotations.DestructiveHint, "the plan tool changes nothing and must not be marked destructive")
	assert.True(t, strings.HasPrefix(plan.Description, "SECURITY: "), "plan description must open with the SECURITY block")
	assert.Contains(t, plan.Description, "confirmationToken")
}

func TestAddToolsReadOnly(t *testing.T) {
	c, _ := client.NewClient(true, "")
	// EnableExec is set on purpose: read-only mode has the highest precedence,
	// so the exec tools must stay unregistered even when exec is enabled.
	tools := NewTools(c, toolconfig.Config{ReadOnly: true, EnableExec: true})

	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "test-server",
		Version: "v1.0.0",
	}, nil)
	assert.NotNil(t, mcpServer)

	tools.AddTools(mcpServer)

	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	defer listener.Close()

	serverAddr := "http://" + listener.Addr().String()

	server := &http.Server{Handler: handler}
	go func() {
		server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	ctx := context.Background()
	transport := &mcp.StreamableClientTransport{
		Endpoint: serverAddr,
	}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)

	var cs *mcp.ClientSession
	assert.Eventually(t, func() bool {
		var err error
		cs, err = mcpClient.Connect(ctx, transport, nil)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond, "Server should start within 2 seconds")

	assert.NotNil(t, cs)
	defer cs.Close()

	toolsResult, err := cs.ListTools(ctx, &mcp.ListToolsParams{})

	assert.NoError(t, err)
	assert.Len(t, toolsResult.Tools, 16, "read-only mode should not register mutating tools")

	// Every read-only tool registered directly by this toolset must be annotated
	// as read-only. Sub-toolset read-only tools are annotated separately
	// (see ledger ruling: folded into Task 10).
	coreReadOnlyTools := []string{
		"getKubernetesResource",
		"listKubernetesResources",
		"listAPIResources",
		"inspectPod",
		"getDeployment",
		"getNodeMetrics",
		"getClusterImages",
		"listClusters",
	}
	annotationsByTool := make(map[string]bool, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		annotationsByTool[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
	}
	for _, name := range coreReadOnlyTools {
		assert.True(t, annotationsByTool[name], "tool %s must be annotated as read-only", name)
	}

	toolNames := make(map[string]bool)
	for _, tool := range toolsResult.Tools {
		toolNames[tool.Name] = true
		if tool.Name == "listClusters" {
			assert.Equal(t, toolsSet+","+provisioning.ToolsSet, tool.Meta[toolsSetAnn])
		} else {
			assert.Equal(t, toolsSet, tool.Meta[toolsSetAnn])
		}
	}
	assert.False(t, toolNames["patchKubernetesResource"], "patchKubernetesResource should not be registered in read-only mode")
	assert.False(t, toolNames["patchKubernetesResourcePlan"], "patchKubernetesResourcePlan should not be registered in read-only mode")
	assert.False(t, toolNames["createKubernetesResource"], "createKubernetesResource should not be registered in read-only mode")
	assert.False(t, toolNames["createKubernetesResourcePlan"], "createKubernetesResourcePlan should not be registered in read-only mode")
	assert.False(t, toolNames["createProject"], "createProject should not be registered in read-only mode")
	assert.False(t, toolNames["createProjectPlan"], "createProjectPlan should not be registered in read-only mode")
	assert.False(t, toolNames["deleteKubernetesResource"], "deleteKubernetesResource should not be registered in read-only mode")
	assert.False(t, toolNames["deleteKubernetesResourcePlan"], "deleteKubernetesResourcePlan should not be registered in read-only mode")
	assert.False(t, toolNames["execPod"], "execPod should not be registered in read-only mode, even with EnableExec")
	assert.False(t, toolNames["execPodPlan"], "execPodPlan should not be registered in read-only mode, even with EnableExec")
}

// TestDeleteToolMetadata pins the safety contract advertised to clients: the
// delete execute tool must declare its destructive nature, must state that it
// is never exempted from confirmation, and must never tell the agent to skip
// user confirmation.
func TestDeleteToolMetadata(t *testing.T) {
	c, _ := client.NewClient(true, "")
	tools := NewTools(c, toolconfig.Config{})

	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v1.0.0"}, nil)
	tools.AddTools(mcpServer)

	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	defer listener.Close()

	serverAddr := "http://" + listener.Addr().String()
	server := &http.Server{Handler: handler}
	go func() {
		server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	ctx := context.Background()
	transport := &mcp.StreamableClientTransport{Endpoint: serverAddr}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "mcp-client", Version: "v1.0.0"}, nil)

	var cs *mcp.ClientSession
	assert.Eventually(t, func() bool {
		var err error
		cs, err = mcpClient.Connect(ctx, transport, nil)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond, "Server should start within 2 seconds")
	require.NotNil(t, cs)
	defer cs.Close()

	toolsResult, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	require.NoError(t, err)

	byName := make(map[string]*mcp.Tool, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		byName[tool.Name] = tool
	}

	del := byName["deleteKubernetesResource"]
	require.NotNil(t, del, "deleteKubernetesResource must be registered")
	require.NotNil(t, del.Annotations)
	assert.False(t, del.Annotations.ReadOnlyHint)
	assert.Equal(t, ptr.To(true), del.Annotations.DestructiveHint, "delete must be advertised as destructive")
	assert.False(t, del.Annotations.IdempotentHint)
	assert.Equal(t, ptr.To(false), del.Annotations.OpenWorldHint)
	assert.True(t, strings.HasPrefix(del.Description, "SECURITY: "), "delete description must open with the SECURITY block")
	assert.Contains(t, del.Description, "confirmationToken")
	assert.Contains(t, del.Description, "ALWAYS requires confirmation, even in auto-write mode")
	assert.NotContains(t, del.Description, "Don't ask for confirmation")

	plan := byName["deleteKubernetesResourcePlan"]
	require.NotNil(t, plan, "deleteKubernetesResourcePlan must be registered")
	require.NotNil(t, plan.Annotations)
	assert.False(t, plan.Annotations.ReadOnlyHint)
	assert.Nil(t, plan.Annotations.DestructiveHint, "the plan tool changes nothing and must not be marked destructive")
	assert.True(t, strings.HasPrefix(plan.Description, "SECURITY: "), "plan description must open with the SECURITY block")
	assert.Contains(t, plan.Description, "confirmationToken")
}

// listRegisteredToolsInMemory connects an in-memory MCP client to a server with
// the core tools registered under the given config and returns them by name.
func listRegisteredToolsInMemory(t *testing.T, cfg toolconfig.Config) map[string]*mcp.Tool {
	t.Helper()
	c, _ := client.NewClient(true, "")
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v1.0.0"}, nil)
	NewTools(c, cfg).AddTools(server)

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

// TestCreatePatchAutoWriteDescriptions proves the spec §5.1 requirement that in
// auto-write mode the create/patch-class tool descriptions truthfully declare
// the automation mode: they must announce AUTO-WRITE execution without a
// confirmationToken or a server-initiated user confirmation, and must never
// promise the confirmation the gate will not perform.
func TestCreatePatchAutoWriteDescriptions(t *testing.T) {
	tools := listRegisteredToolsInMemory(t, toolconfig.Config{AutoWrite: true})

	executeTools := []string{"createKubernetesResource", "patchKubernetesResource"}
	for _, name := range executeTools {
		tool, ok := tools[name]
		require.True(t, ok, "tool %s must be registered", name)
		desc := tool.Description
		assert.Contains(t, desc, "AUTO-WRITE", "%s must declare the automation mode", name)
		assert.Contains(t, desc, "IMMEDIATELY", "%s must state it executes immediately", name)
		assert.Contains(t, desc, "NO confirmationToken", "%s must state no token is needed", name)
		assert.Contains(t, desc, "NO server-initiated user confirmation", "%s must state the server will not ask the user", name)
		assert.Contains(t, desc, "deleteKubernetesResource", "%s must name deleteKubernetesResource as still gated", name)
		assert.Contains(t, desc, "execPod", "%s must name execPod as still gated", name)
		assert.Contains(t, desc, "ALWAYS require", "%s must state delete/exec always require the full protocol", name)
		assert.Contains(t, desc, "ONLY when the user has explicitly asked", "%s must require an explicit user request", name)
		// The false promise must be gone, and the token must not be advertised
		// as required.
		assert.NotContains(t, desc, "The server then asks the USER DIRECTLY to confirm",
			"%s must not promise a confirmation in auto-write mode", name)
		assert.NotContains(t, desc, "(3) Call this tool with the confirmationToken",
			"%s must not require the plan token in auto-write mode", name)
	}

	// Delete and exec keep the full protocol in every mode: their descriptions
	// must be untouched by the auto-write variant.
	del := tools["deleteKubernetesResource"]
	require.NotNil(t, del)
	assert.Contains(t, del.Description, "The server then asks the USER DIRECTLY", "delete must still demand the direct confirmation")
	assert.Contains(t, del.Description, "ALWAYS requires confirmation, even in auto-write mode")

	if exec, ok := tools["execPod"]; ok {
		assert.Contains(t, exec.Description, "ALWAYS requires confirmation, even in auto-write mode")
	}
}

// TestCreatePatchAutoWriteDescriptionsDoNotAffectPlans pins that plan tools keep
// the token protocol in auto-write mode: they still mint tokens, so their
// descriptions must still say so.
func TestCreatePatchAutoWriteDescriptionsDoNotAffectPlans(t *testing.T) {
	tools := listRegisteredToolsInMemory(t, toolconfig.Config{AutoWrite: true})

	for _, name := range []string{"createKubernetesResourcePlan", "patchKubernetesResourcePlan", "deleteKubernetesResourcePlan"} {
		tool, ok := tools[name]
		require.True(t, ok, "tool %s must be registered", name)
		assert.Contains(t, tool.Description, "single-use confirmationToken", "plan tool %s mints tokens in every mode", name)
		assert.NotContains(t, tool.Description, "AUTO-WRITE", "plan tool %s must not declare auto-write execution", name)
	}
}
