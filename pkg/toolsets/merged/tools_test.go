// pkg/toolsets/merged/tools_test.go
package merged

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
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

// TestCaseMapForPhaseBinding pins the plan/execute → case-map selection, the
// only thing making the plan verb unable to mutate: both maps are
// Case[ChangeParams], so a swap would compile and, under --allow-auto-write,
// let planChange execute a create immediately. The identity comparison below
// fails if the two arguments are swapped; TestChangePhaseInversionEndToEnd
// proves the same property through the registered wire surface.
func TestCaseMapForPhaseBinding(t *testing.T) {
	m := buildCaseMaps(&client.Client{}, toolconfig.Config{})

	planMap := caseMapFor(phasePlan, m)
	executeMap := caseMapFor(phaseExecute, m)

	// Identity, not equality: the plan phase must hand Dispatch the plan map
	// itself, never a copy or the execute map.
	planPtr := reflect.ValueOf(planMap).Pointer()
	executePtr := reflect.ValueOf(executeMap).Pointer()
	assert.Equal(t, reflect.ValueOf(m.plan).Pointer(), planPtr, "the plan phase must select cases.plan")
	assert.Equal(t, reflect.ValueOf(m.execute).Pointer(), executePtr, "the execute phase must select cases.execute")
	assert.NotEqual(t, planPtr, executePtr, "the two phases must not select the same map")

	// The two maps are equivalent in type but not in content: the plan cases
	// must never demand a confirmationToken, which is what makes a
	// plan-dispatched create safe under auto-write.
	assert.NotEmpty(t, planMap)
	for op, c := range planMap {
		assert.NotContains(t, c.Required, "confirmationToken",
			"plan case %q must not require a confirmationToken", op)
	}
	assert.Contains(t, executeMap["createKubernetesResource"].Required, "manifest",
		"sanity: the execute map must be the change table, not an empty map")

	// An unknown phase selects nothing rather than silently planning.
	assert.Nil(t, caseMapFor("bogus", m))
}

// newChangeWireClient builds a *client.Client that resolves kinds from fake
// discovery and records every dynamic verb against the given fake client.
func newChangeWireClient(t *testing.T, dyn *dynamicfake.FakeDynamicClient, discovery []*metav1.APIResourceList) *client.Client {
	t.Helper()
	cs := fake.NewClientset()
	fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	require.True(t, ok)
	fd.Resources = discovery
	return &client.Client{
		ClientSetCreator: func(*rest.Config) (kubernetes.Interface, error) { return cs, nil },
		DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
}

// changeDiscovery serves the ConfigMap kind the wire test creates.
var changeDiscovery = []*metav1.APIResourceList{
	{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "configmaps", Kind: "ConfigMap", Namespaced: true},
	}},
}

// TestChangePhaseInversionEndToEnd is the F1 regression test: it drives the
// registered planChange and executeChange tools over an in-memory MCP
// connection, through Dispatch and the real case maps, and asserts the phase
// semantics that the map binding exists to guarantee — a plan-dispatched create
// must not mutate the cluster, an execute-dispatched one must. Swapping
// cases.plan/cases.execute in Register makes the plan call create and fails
// here.
func TestChangePhaseInversionEndToEnd(t *testing.T) {
	const manifest = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: e2e-cm\n  namespace: default\ndata:\n  k: v\n"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
	})
	gate, err := confirm.NewGate()
	require.NoError(t, err)
	gate.ElicitFunc = func(context.Context, *mcp.ServerSession, *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": "approve"}}, nil
	}

	cfg := toolconfig.Config{}
	cfg.Gate = gate
	sess := connectServer(t, newChangeWireClient(t, dyn, changeDiscovery), cfg)
	ctx := context.Background()
	args := map[string]any{
		"operation": "createKubernetesResource", "cluster": "local", "kind": "ConfigMap",
		"namespace": "default", "name": "e2e-cm", "manifest": manifest,
	}

	// PLAN: the plan tool must return a plan carrying the token and leave the
	// cluster untouched. If the maps are swapped, the create runs here.
	planRes, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "planChange", Arguments: args})
	require.NoError(t, err)
	require.False(t, planRes.IsError, "planChange must not fail: %s", textOf(planRes))
	assert.Contains(t, textOf(planRes), `"plan"`)
	assert.Contains(t, textOf(planRes), `"confirmationToken"`)
	assert.Zero(t, createCount(dyn), "planChange must not create anything")

	// EXECUTE: with the token from the plan response, executeChange must create
	// exactly once and return the created object, not a second plan.
	token := tokenOf(t, textOf(planRes))
	execArgs := map[string]any{}
	for k, v := range args {
		execArgs[k] = v
	}
	execArgs["confirmationToken"] = token
	execRes, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "executeChange", Arguments: execArgs})
	require.NoError(t, err)
	require.False(t, execRes.IsError, "executeChange must not fail: %s", textOf(execRes))
	assert.NotContains(t, textOf(execRes), `"confirmationToken"`, "the execute verb must execute, not re-plan")
	assert.Equal(t, 1, createCount(dyn), "executeChange must create exactly once")
}

// connectServer registers the merged surface and returns a connected client
// session.
func connectServer(t *testing.T, c *client.Client, cfg toolconfig.Config) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	Register(c, server, cfg)

	ct, st := mcp.NewInMemoryTransports()
	_, err := server.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	mc := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	sess, err := mc.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	t.Cleanup(func() { sess.Close() })
	return sess
}

// textOf returns the concatenated text content of a tool result.
func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// tokenOf extracts the confirmationToken from a plan response body.
func tokenOf(t *testing.T, body string) string {
	t.Helper()
	var parsed struct {
		Confirmation struct {
			Token string `json:"confirmationToken"`
		} `json:"confirmation"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &parsed))
	require.NotEmpty(t, parsed.Confirmation.Token, "the plan response must carry a token")
	return parsed.Confirmation.Token
}

// createCount returns how many create actions the fake dynamic client recorded.
func createCount(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "create" {
			n++
		}
	}
	return n
}

func assertKeys[P any](t *testing.T, name string, m map[string]dispatch.Case[P], want []string) {
	t.Helper()
	var got []string
	for k := range m {
		got = append(got, k)
	}
	slices.Sort(got)
	// Clone before sorting: callers pass the package-level enum vars of
	// dispatch/schemas.go, and sorting in place would reorder their backing
	// arrays, making any later order-sensitive assertion test-order dependent.
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s case keys = %v, want %v", name, got, want)
	}
}

// ---------------------------------------------------------------------------
// baseline mapping validation
// ---------------------------------------------------------------------------

// baselineCallsPath is the committed case matrix of the post-deploy baseline
// harness. Its mapsTo mappings replay each pre-refactor tool call onto the
// merged surface; nothing else validates them until the post-deploy run, where
// a mapping mistake reads as a false regression.
const baselineCallsPath = "../../../scripts/baseline/calls.json"

// baselineCase mirrors the fields of scripts/baseline/calls.json that the
// mapping validation needs. It is deliberately a separate struct from the
// harness's callCase: that one lives in package main and cannot be imported.
type baselineCase struct {
	ID     string `json:"id"`
	Tool   string `json:"tool"`
	MapsTo struct {
		Tool   string         `json:"tool"`
		Params map[string]any `json:"params"`
	} `json:"mapsTo"`
}

// baselineMapsToTool lists the tools a mapping may target. The three generic
// Kubernetes tools take no dispatch enum; the four merged tools do.
var baselineMapsToTool = map[string]bool{
	"rancherQuery": true, "diagnose": true, "planChange": true, "executeChange": true,
	"getKubernetesResource": true, "listKubernetesResources": true, "listAPIResources": true,
}

// TestBaselineMappingsDispatch pins that all 43 committed baseline mappings
// reach the merged surface: every mapsTo.tool is a registered tool, and every
// enum-dispatched mapping passes dispatch.Validate against the REAL case table
// for its phase. Regressions this catches before the post-deploy run:
//   - a mapping carrying the wrong parameter name (e.g. `name` where
//     scaleClusterNodePool requires `nodePoolName`) — capture -v2 would record
//     a validation error instead of exercising the handler;
//   - an enum value dropped or renamed in dispatch.ChangeOperations (or the
//     query/diagnose enums) without updating calls.json;
//   - a required parameter added to a case table without updating calls.json.
func TestBaselineMappingsDispatch(t *testing.T) {
	raw, err := os.ReadFile(baselineCallsPath)
	require.NoError(t, err, "the committed case matrix must be readable")
	var cases []baselineCase
	require.NoError(t, json.Unmarshal(raw, &cases))
	require.Len(t, cases, 43, "the case matrix is the 43-case v1 corpus")

	m := buildCaseMaps(&client.Client{}, toolconfig.Config{})
	phases := map[string]map[string]dispatch.Case[dispatch.ChangeParams]{
		"planChange":    m.plan,
		"executeChange": m.execute,
	}

	checked := 0
	for _, c := range cases {
		require.NotEmpty(t, c.MapsTo.Tool, "case %s: mapsTo.tool must be set", c.ID)
		require.True(t, baselineMapsToTool[c.MapsTo.Tool],
			"case %s: mapsTo.tool %q is not a merged-surface tool", c.ID, c.MapsTo.Tool)

		var err error
		switch c.MapsTo.Tool {
		case "planChange", "executeChange":
			p := decodeMappedParams[dispatch.ChangeParams](t, c.MapsTo.Params)
			cs, ok := phases[c.MapsTo.Tool][p.Operation]
			if !ok {
				t.Errorf("case %s: %s has no operation %q", c.ID, c.MapsTo.Tool, p.Operation)
				continue
			}
			// Validate mirrors Dispatch's pre-handler check: its error text
			// names the missing parameter(s).
			err = dispatch.Validate("operation", p.Operation, p, cs.Required)
		case "rancherQuery":
			p := decodeMappedParams[dispatch.QueryParams](t, c.MapsTo.Params)
			cs, ok := m.query[p.Resource]
			if !ok {
				t.Errorf("case %s: rancherQuery has no resource %q", c.ID, p.Resource)
				continue
			}
			err = dispatch.Validate("resource", p.Resource, p, cs.Required)
		case "diagnose":
			p := decodeMappedParams[dispatch.DiagnoseParams](t, c.MapsTo.Params)
			cs, ok := m.diagnose[p.Target]
			if !ok {
				t.Errorf("case %s: diagnose has no target %q", c.ID, p.Target)
				continue
			}
			err = dispatch.Validate("target", p.Target, p, cs.Required)
		default:
			// The three generic tools take no enum; their mappings must at
			// least be targeted at the tool they came from.
			require.Equal(t, c.Tool, c.MapsTo.Tool,
				"case %s: a generic-tool mapping must map to itself", c.ID)
		}
		require.NoError(t, err, "case %s: mapping does not dispatch", c.ID)
		checked++
	}
	require.Equal(t, 43, checked, "every case must be validated")
}

// decodeMappedParams reproduces what the wire decode does to a calls.json
// params object: marshal to JSON, unmarshal into the typed params struct.
// Going through JSON (not a field-by-field copy) is what proves the mapping's
// JSON key names are the ones dispatch.Validate reflects over.
func decodeMappedParams[P any](t *testing.T, params map[string]any) P {
	t.Helper()
	var p P
	b, err := json.Marshal(params)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &p))
	return p
}
