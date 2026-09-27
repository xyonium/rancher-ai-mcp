// pkg/toolsets/merged/tools_test.go
package merged

import (
	"context"
	"slices"
	"strings"
	"testing"

	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullModeTools is the merged surface in strict/full mode: the 3 k8s-generic
// tools, the 2 read-only merged tools and the 2 change tools.
var fullModeTools = []string{"diagnose", "executeChange", "getKubernetesResource", "listAPIResources", "listKubernetesResources", "planChange", "rancherQuery"}

// readOnlyModeTools is the merged surface in read-only mode: the same set minus
// the two change tools, which are not registered at all.
var readOnlyModeTools = []string{"diagnose", "getKubernetesResource", "listAPIResources", "listKubernetesResources", "rancherQuery"}

func listTools(t *testing.T, cfg toolconfig.Config) []string {
	t.Helper()
	gate, err := confirm.NewGate()
	require.NoError(t, err)
	cfg.Gate = gate
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	Register(&client.Client{}, server, cfg) // &client.Client{} is fine: registration never calls it
	ct, st := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	mc := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	sess, err := mc.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func TestRegisterFullModeExactly7(t *testing.T) {
	got := listTools(t, toolconfig.Config{EnableExec: true})
	if !slices.Equal(got, fullModeTools) {
		t.Fatalf("full mode tools = %v, want %v", got, fullModeTools)
	}
}

func TestRegisterReadOnlyExactly5(t *testing.T) {
	got := listTools(t, toolconfig.Config{ReadOnly: true})
	if !slices.Equal(got, readOnlyModeTools) {
		t.Fatalf("read-only tools = %v, want %v", got, readOnlyModeTools)
	}
}

// TestRegisterReadOnlyExcludesChangeTools makes the read-only guarantee
// explicit at the tool-name level rather than relying on the count: neither
// planChange nor executeChange exists in read-only mode, so no change can be
// planned or executed through the wire surface.
func TestRegisterReadOnlyExcludesChangeTools(t *testing.T) {
	got := listTools(t, toolconfig.Config{ReadOnly: true})
	for _, name := range []string{"planChange", "executeChange"} {
		assert.NotContains(t, got, name, "%s must not be registered in read-only mode", name)
	}
}

// TestRegisterFullModeIncludesChangeTools is the positive control for the
// read-only exclusion above: with neither ReadOnly nor EnableExec set, the two
// change tools are registered and the read-only tools survive too.
func TestRegisterFullModeIncludesChangeTools(t *testing.T) {
	got := listTools(t, toolconfig.Config{})
	for _, name := range []string{"planChange", "executeChange", "rancherQuery", "diagnose"} {
		assert.Contains(t, got, name, "%s must be registered in full mode", name)
	}
}

// TestRegisterToolsetAnnotation keeps the metadata contract of
// internal/toolsdoc: every merged tool carries the `toolset` Meta annotation,
// so TOOLS.md keeps regenerating by the same mechanism.
func TestRegisterToolsetAnnotation(t *testing.T) {
	cfg := toolconfig.Config{EnableExec: true}
	gate, err := confirm.NewGate()
	require.NoError(t, err)
	cfg.Gate = gate
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	Register(&client.Client{}, server, cfg)

	ct, st := mcp.NewInMemoryTransports()
	_, err = server.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	mc := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	sess, err := mc.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	defer sess.Close()

	res, err := sess.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.Tools)
	for _, tool := range res.Tools {
		ts, ok := tool.Meta[toolsSetAnn].(string)
		assert.True(t, ok && ts != "", "tool %s must carry a non-empty %s meta annotation", tool.Name, toolsSetAnn)
	}
}

// TestMergedToolMetadata pins the safety contract advertised to clients for the
// four merged tools: the query/diagnose pair is read-only and not open-world;
// planChange is a non-destructive write that just plans; executeChange is
// destructive, carries the security protocol and advertises the
// confirmationToken.
func TestMergedToolMetadata(t *testing.T) {
	byName := toolsByName(t, toolconfig.Config{EnableExec: true})

	for _, name := range []string{"rancherQuery", "diagnose"} {
		tool := byName[name]
		require.NotNil(t, tool, "tool %s must be registered", name)
		require.NotNil(t, tool.Annotations)
		assert.True(t, tool.Annotations.ReadOnlyHint, "%s must be read-only", name)
		require.NotNil(t, tool.Annotations.OpenWorldHint)
		assert.False(t, *tool.Annotations.OpenWorldHint, "%s must not be open-world", name)
	}

	plan := byName["planChange"]
	require.NotNil(t, plan)
	require.NotNil(t, plan.Annotations)
	assert.False(t, plan.Annotations.ReadOnlyHint, "planChange mints tokens and is not read-only")
	assert.Nil(t, plan.Annotations.DestructiveHint, "planChange changes nothing and must not be destructive")
	assert.True(t, strings.HasPrefix(plan.Description, "SECURITY: "), "planChange must open with the SECURITY block")
	assert.Contains(t, plan.Description, "single-use confirmationToken")

	exec := byName["executeChange"]
	require.NotNil(t, exec)
	require.NotNil(t, exec.Annotations)
	assert.False(t, exec.Annotations.ReadOnlyHint)
	require.NotNil(t, exec.Annotations.DestructiveHint)
	assert.True(t, *exec.Annotations.DestructiveHint, "executeChange must be advertised as destructive")
	assert.True(t, strings.HasPrefix(exec.Description, "SECURITY: "))
	assert.Contains(t, exec.Description, "planChange")
	assert.Contains(t, exec.Description, "confirmationToken")
	assert.Contains(t, exec.Description, "ALWAYS require", "executeChange must name the operations that stay gated")
}

// TestExecuteChangeAutoWriteDescriptionAndSchema pins the spec §5.1 requirement
// that in auto-write mode the surface truthfully declares the automation mode:
// the description must announce immediate execution without a token, and the
// executeChange schema must not require a confirmationToken the operator has
// opted out of (Task 1 ruling: the schema mirrors the gate).
func TestExecuteChangeAutoWriteDescriptionAndSchema(t *testing.T) {
	byName := toolsByName(t, toolconfig.Config{AutoWrite: true})
	exec := byName["executeChange"]
	require.NotNil(t, exec, "executeChange must be registered in auto-write mode")

	desc := exec.Description
	assert.Contains(t, desc, "AUTO-WRITE", "the description must declare the automation mode")
	assert.Contains(t, desc, "IMMEDIATELY", "it must state the operation executes immediately")
	assert.Contains(t, desc, "NO confirmationToken", "it must state no token is needed")
	assert.Contains(t, desc, "NO server-initiated user confirmation", "it must state the server will not ask the user")
	assert.Contains(t, desc, "ONLY when the user has explicitly asked", "it must still require an explicit user request")
	assert.Contains(t, desc, "deleteKubernetesResource", "it must name deleteKubernetesResource as still gated")
	assert.Contains(t, desc, "execPod", "it must name execPod as still gated")
	assert.NotContains(t, desc, "(3) Call this tool with the confirmationToken",
		"it must not require the plan token in auto-write mode")
	assert.NotContains(t, desc, "The server then asks the USER DIRECTLY to confirm",
		"it must not promise a confirmation the gate will not perform")

	// The schema must not demand the token either: an auto-write call has none.
	assert.NotContains(t, schemaRequired(t, exec), "confirmationToken",
		"the auto-write executeChange schema must not require a confirmationToken")
	assert.Contains(t, schemaRequired(t, exec), "operation")

	// The strict-mode schema still requires it, and the strict description keeps
	// the full protocol: auto-write must be a distinct, truthful variant.
	strict := toolsByName(t, toolconfig.Config{})
	assert.Contains(t, schemaRequired(t, strict["executeChange"]), "confirmationToken")
	assert.Contains(t, strict["executeChange"].Description, "The server then asks the USER DIRECTLY to confirm")

	// planChange keeps minting tokens in every mode, so its description never
	// declares auto-write execution.
	assert.Contains(t, byName["planChange"].Description, "single-use confirmationToken")
	assert.NotContains(t, byName["planChange"].Description, "AUTO-WRITE MODE")
}

// schemaRequired returns the InputSchema's required property names as they go
// over the wire (the SDK serializes the schema to a plain JSON object).
func schemaRequired(t *testing.T, tool *mcp.Tool) []string {
	t.Helper()
	raw, err := json.Marshal(tool.InputSchema)
	require.NoError(t, err)
	var parsed struct {
		Required []string `json:"required"`
	}
	require.NoError(t, json.Unmarshal(raw, &parsed))
	return parsed.Required
}

// toolsByName boots the server in-memory and returns the registered tools by
// name.
func toolsByName(t *testing.T, cfg toolconfig.Config) map[string]*mcp.Tool {
	t.Helper()
	gate, err := confirm.NewGate()
	require.NoError(t, err)
	cfg.Gate = gate
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	Register(&client.Client{}, server, cfg)

	ct, st := mcp.NewInMemoryTransports()
	_, err = server.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	mc := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	sess, err := mc.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	defer sess.Close()

	res, err := sess.ListTools(context.Background(), nil)
	require.NoError(t, err)
	byName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	return byName
}

func TestCaseMapsMatchSchemaEnums(t *testing.T) {
	// Build the same maps Register builds and assert keys == schema enum lists.
	m := buildCaseMaps(&client.Client{}, toolconfig.Config{ReadOnly: false, EnableExec: false})
	assertKeys(t, "query", m.query, dispatch.QueryResources)
	assertKeys(t, "diagnose", m.diagnose, dispatch.DiagnoseTargets)
	assertKeys(t, "plan", m.plan, dispatch.ChangeOperations)
	assertKeys(t, "execute", m.execute, dispatch.ChangeOperations)
}

func assertKeys[P any](t *testing.T, name string, m map[string]dispatch.Case[P], want []string) {
	t.Helper()
	var got []string
	for k := range m {
		got = append(got, k)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s case keys = %v, want %v", name, got, want)
	}
}
