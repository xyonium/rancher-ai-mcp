package toolsets

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allToolsNames is the merged surface AddAllTools must delegate to: the 3
// k8s-generic tools, rancherQuery and diagnose, plus — outside read-only mode —
// planChange and executeChange. The 7/5 invariants themselves live in
// pkg/toolsets/merged; these tests pin the delegation and the write-tool
// contract at the package boundary the server entry point uses.
var allToolsNames = []string{"diagnose", "executeChange", "getKubernetesResource", "listAPIResources", "listKubernetesResources", "planChange", "rancherQuery"}

// TestAddAllToolsDelegatesToMerged proves the single registration entry point
// still adds the whole consolidated surface: toolsets.AddAllTools must not
// drift from merged.Register.
func TestAddAllToolsDelegatesToMerged(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  toolconfig.Config
		want []string
	}{
		{"full", toolconfig.Config{}, allToolsNames},
		{"enable-exec", toolconfig.Config{EnableExec: true}, allToolsNames},
		{"read-only", toolconfig.Config{ReadOnly: true}, []string{"diagnose", "getKubernetesResource", "listAPIResources", "listKubernetesResources", "rancherQuery"}},
		{"read-only+exec", toolconfig.Config{ReadOnly: true, EnableExec: true}, []string{"diagnose", "getKubernetesResource", "listAPIResources", "listKubernetesResources", "rancherQuery"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registered := listAllRegisteredTools(t, tc.cfg)
			got := make([]string, 0, len(registered))
			for name := range registered {
				got = append(got, name)
			}
			sort.Strings(got)
			want := append([]string{}, tc.want...)
			sort.Strings(want)
			assert.Equal(t, want, got)
		})
	}
}

// TestWriteToolInventoryMatchesRegistration guards the invariant that the
// safety instructions never advertise more than the server registers. In the
// merged surface the only non-read-only tools are planChange and executeChange:
// every mutating operation is an enum value inside them, so a write tool that
// appears here but is not one of the two change tools (or vice versa) fails
// this test.
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
			got := registeredWriteTools(registered)

			if tc.cfg.ReadOnly {
				// Read-only mode registers no mutating tool at all: both change
				// tools are unregistered, so no operation can be planned or
				// executed.
				assert.Empty(t, got, "read-only mode must register no write tool")
				return
			}

			assert.Equal(t, []string{"executeChange", "planChange"}, got,
				"the only mutating tools are the two merged change tools")
		})
	}
}

// TestEnableExecDoesNotChangeToolSet pins that --enable-exec is a runtime,
// per-operation gate now: the tool surface is identical with and without it,
// and the execPod operation is refused inside executeChange instead. Read-only
// mode still wins over both.
func TestEnableExecDoesNotChangeToolSet(t *testing.T) {
	without := listAllRegisteredTools(t, toolconfig.Config{})
	with := listAllRegisteredTools(t, toolconfig.Config{EnableExec: true})

	assert.Len(t, with, len(without), "--enable-exec must not add a tool to the merged surface")
	for name := range without {
		assert.Contains(t, with, name, "%s must stay registered with --enable-exec", name)
	}

	readOnly := listAllRegisteredTools(t, toolconfig.Config{EnableExec: true, ReadOnly: true})
	for _, name := range []string{"planChange", "executeChange", "execPod", "execPodPlan"} {
		assert.NotContains(t, readOnly, name, "%s must not be registered in read-only mode", name)
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
