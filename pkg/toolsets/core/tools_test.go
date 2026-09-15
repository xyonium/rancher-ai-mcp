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
	for _, tool := range toolsResult.Tools {
		if tool.Name == "listClusters" {
			assert.Equal(t, toolsSet+","+provisioning.ToolsSet, tool.Meta[toolsSetAnn])
		} else {
			assert.Equal(t, toolsSet, tool.Meta[toolsSetAnn])
		}
	}
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
	tools := NewTools(c, toolconfig.Config{ReadOnly: true})

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
