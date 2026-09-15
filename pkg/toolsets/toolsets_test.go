package toolsets

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllToolSets(t *testing.T) {
	c, err := client.NewClient(true, "https://fake-url")
	require.NoError(t, err)
	toolsets := allToolSets(c, toolconfig.Config{})

	assert.NotNil(t, toolsets)
	assert.Len(t, toolsets, 3, "should have exactly 3 toolsets (core, fleet, and provisioning)")
}

// TestWriteToolInventoryMatchesRegistration guards the cross-task invariant
// that the writeTools inventory in instructions.go is exactly the set of
// mutating tools the server really registers. A renamed or freshly added write
// tool that is missing from (or stale in) the inventory fails here, so the
// safety instructions can never advertise a tool set that does not match the
// wire. Tool access is read off the MCP annotations, the same signal TOOLS.md
// uses to mark a tool as Write.
func TestWriteToolInventoryMatchesRegistration(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  toolconfig.Config
	}{
		{"default", toolconfig.Config{}},
		{"enable-exec", toolconfig.Config{EnableExec: true}},
		{"auto-write", toolconfig.Config{AutoWrite: true}},
		{"auto-write+exec", toolconfig.Config{AutoWrite: true, EnableExec: true}},
		{"read-only", toolconfig.Config{ReadOnly: true}},
		{"read-only+exec", toolconfig.Config{ReadOnly: true, EnableExec: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registered := listAllRegisteredTools(t, tc.cfg)

			if tc.cfg.ReadOnly {
				// Read-only mode registers mutating tools ONLY. allWriteTools
				// deliberately still names the inventory, so the comparison is
				// against the empty set here.
				assert.Empty(t, registeredWriteTools(registered),
					"read-only mode must register no write tool")
				return
			}

			want := append([]string{}, allWriteTools(tc.cfg)...)
			// The plan tools are mutating too (they mint tokens) and the exec
			// plan tool exists only with EnableExec; both are write-annotated
			// and none of them is exempt from the inventory check.
			want = append(want,
				"createKubernetesResourcePlan",
				"patchKubernetesResourcePlan",
				"deleteKubernetesResourcePlan",
				"createProjectPlan",
				"createCustomClusterPlan",
				"createImportedClusterPlan",
				"createK3kClusterPlan",
				"scaleClusterNodePoolPlan",
			)
			if tc.cfg.EnableExec {
				want = append(want, "execPodPlan")
			}
			sort.Strings(want)

			assert.Equal(t, want, registeredWriteTools(registered),
				"the registered write tools must equal the instructions inventory plus the plan tools")
		})
	}
}

// TestExecToolPairRegistration pins that --enable-exec adds exactly the exec
// pair and nothing else, and that read-only mode still wins over it.
func TestExecToolPairRegistration(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  toolconfig.Config
	}{
		{"enable-exec", toolconfig.Config{EnableExec: true}},
		{"enable-exec+auto-write", toolconfig.Config{EnableExec: true, AutoWrite: true}},
		{"enable-exec+read-only", toolconfig.Config{EnableExec: true, ReadOnly: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registered := listAllRegisteredTools(t, tc.cfg)
			execTools := []string{}
			for _, name := range registeredWriteTools(registered) {
				if strings.HasPrefix(name, "execPod") {
					execTools = append(execTools, name)
				}
			}
			if tc.cfg.ReadOnly {
				assert.Empty(t, execTools, "read-only mode wins over EnableExec")
				return
			}
			assert.Equal(t, []string{"execPod", "execPodPlan"}, execTools,
				"EnableExec must register exactly the exec pair")
		})
	}
}

// listAllRegisteredTools boots the server in-memory with every toolset
// registered under cfg and returns the tools by name.
func listAllRegisteredTools(t *testing.T, cfg toolconfig.Config) map[string]*mcp.Tool {
	t.Helper()
	c, err := client.NewClient(true, "https://fake-url")
	require.NoError(t, err)

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "v1.0.0"}, nil)
	AddAllTools(c, server, cfg)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	defer serverSession.Close()

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1.0.0"}, nil)
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

// registeredWriteTools returns the sorted names of the tools that are not
// annotated read-only.
func registeredWriteTools(byName map[string]*mcp.Tool) []string {
	names := make([]string, 0, len(byName))
	for name, tool := range byName {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func TestToolSchemasValidity(t *testing.T) {
	c, err := client.NewClient(true, "https://fake-url")
	require.NoError(t, err)

	mcpServer := mcp.NewServer(&mcp.Implementation{
		Name:    "test-server",
		Version: "v1.0.0",
	}, nil)
	AddAllTools(c, mcpServer, toolconfig.Config{})

	handler := mcp.NewStreamableHTTPHandler(func(request *http.Request) *mcp.Server {
		return mcpServer
	}, &mcp.StreamableHTTPOptions{})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	serverAddr := "http://" + listener.Addr().String()
	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Shutdown(context.Background())

	ctx := context.Background()
	transport := &mcp.StreamableClientTransport{Endpoint: serverAddr}
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1.0.0"}, nil)

	var cs *mcp.ClientSession
	require.Eventually(t, func() bool {
		var err error
		cs, err = mcpClient.Connect(ctx, transport, nil)
		return err == nil
	}, 2*time.Second, 100*time.Millisecond)
	defer cs.Close()

	toolsResult, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	require.NoError(t, err)
	require.NotEmpty(t, toolsResult.Tools)

	for _, tool := range toolsResult.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			schemaBytes, err := json.Marshal(tool.InputSchema)
			require.NoError(t, err)

			var schemaMap map[string]any
			require.NoError(t, json.Unmarshal(schemaBytes, &schemaMap), "schema for %s must be valid JSON", tool.Name)

			validateSchemaNode(t, tool.Name, "", schemaMap)
		})
	}
}

// validateSchemaNode checks that a schema node complies with Gemini/LLM strict requirements:
// 1. If "properties" is present, "type" must be "object".
// 2. If "items" is present, "type" must be "array".
// 3. No "anyOf" containing null unions or array/items inside anyOf.
func validateSchemaNode(t *testing.T, toolName, path string, node map[string]any) {
	nodeType, hasType := node["type"].(string)
	types, hasTypes := node["types"].([]any)
	if hasTypes {
		assert.Fail(t, fmt.Sprintf("[%s%s] multi-type 'types' is not supported by Gemini: %v", toolName, path, types))
	}

	if _, hasProps := node["properties"]; hasProps {
		assert.True(t, hasType && nodeType == "object", "[%s%s] 'properties' is only allowed when type == 'object', got type=%v", toolName, path, node["type"])
	}

	if _, hasItems := node["items"]; hasItems {
		assert.True(t, hasType && nodeType == "array", "[%s%s] 'items' is only allowed when type == 'array', got type=%v", toolName, path, node["type"])
	}

	if anyOf, hasAnyOf := node["anyOf"].([]any); hasAnyOf {
		for i, sub := range anyOf {
			if subMap, ok := sub.(map[string]any); ok {
				validateSchemaNode(t, toolName, fmt.Sprintf("%s.anyOf[%d]", path, i), subMap)
			}
		}
	}

	if props, ok := node["properties"].(map[string]any); ok {
		for propName, propVal := range props {
			if propMap, ok := propVal.(map[string]any); ok {
				validateSchemaNode(t, toolName, fmt.Sprintf("%s.%s", path, propName), propMap)
			}
		}
	}

	if items, ok := node["items"].(map[string]any); ok {
		validateSchemaNode(t, toolName, fmt.Sprintf("%s.items", path), items)
	}
}
