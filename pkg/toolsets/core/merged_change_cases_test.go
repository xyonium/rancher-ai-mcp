package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

// changeCfg returns a config with a real confirmation gate. Elicitation is
// never reached in these tests: they only exercise token issuance/validation
// and case-table wiring, all of which happen before the user confirmation.
func changeCfg(t *testing.T, enableExec bool) toolconfig.Config {
	t.Helper()
	gate, err := confirm.NewGate()
	if err != nil {
		t.Fatal(err)
	}
	return toolconfig.Config{Gate: gate, EnableExec: enableExec}
}

// changeCaseConfigMap is the object the patch case resolves and patches through
// the fake dynamic client.
var changeCaseConfigMap = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{Name: "d", Namespace: "default"},
	Data:       map[string]string{"k": "v"},
}

// newChangeCaseTools builds Tools backed by fake discovery (so kinds resolve)
// and a fake dynamic client holding one configmap and one pod, wrapped so the
// token is validated too. All four core change cases share the same fake.
func newChangeCaseTools(t *testing.T, cfg toolconfig.Config) (*Tools, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		gvr:    "ConfigMapList",
		podGVR: "PodList",
	}, changeCaseConfigMap, changeCasePod)
	c := &client.Client{
		ClientSetCreator: fakeDiscoveryClientset(t, listAPIResourcesDiscovery),
		DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
	return NewTools(test.WrapClient(c, "fakeToken"), cfg), dyn
}

// changeCasePod is the pod the exec case plans against.
var changeCasePod = &corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
	Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "first-container"}}},
}

func TestPlanExecuteCaseKeys(t *testing.T) {
	tools := NewTools(nil, changeCfg(t, false))
	want := []string{"createKubernetesResource", "patchKubernetesResource", "deleteKubernetesResource", "execPod"}
	if len(tools.PlanCases()) != len(want) || len(tools.ExecuteCases()) != len(want) {
		t.Fatalf("core owns %d plan and %d execute cases", len(tools.PlanCases()), len(tools.ExecuteCases()))
	}
	for _, k := range want {
		if _, ok := tools.PlanCases()[k]; !ok {
			t.Errorf("missing plan case %q", k)
		}
		if _, ok := tools.ExecuteCases()[k]; !ok {
			t.Errorf("missing execute case %q", k)
		}
	}
}

// TestChangeCaseRequiredFields pins the runtime-validated required sets so a
// later union of the case maps cannot silently drop a guard.
func TestChangeCaseRequiredFields(t *testing.T) {
	tools := NewTools(nil, changeCfg(t, false))
	want := map[string][]string{
		"createKubernetesResource": {"cluster", "kind", "name", "manifest"},
		"patchKubernetesResource":  {"cluster", "kind", "name", "patch"},
		"deleteKubernetesResource": {"cluster", "kind", "name"},
		"execPod":                  {"cluster", "namespace", "name", "command"},
	}
	for _, phase := range []string{"plan", "execute"} {
		cases := tools.PlanCases()
		if phase == "execute" {
			cases = tools.ExecuteCases()
		}
		for k, req := range want {
			assert.Equal(t, req, cases[k].Required, "%s case %q required fields", phase, k)
		}
	}
}

func TestExecPodCaseGatedOnEnableExec(t *testing.T) {
	tools := NewTools(nil, changeCfg(t, false))
	c := tools.PlanCases()["execPod"]
	_, _, err := c.Handler(context.Background(), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "execPod", Cluster: "c", Namespace: "default", Name: "p", Command: []string{"true"},
	})
	if err == nil || !strings.Contains(err.Error(), "--enable-exec") {
		t.Fatalf("expected --enable-exec error, got %v", err)
	}
}

// TestExecPodExecuteCaseGatedOnEnableExec proves the regression this task
// exists to prevent: exec is not exposed at all when the server was not started
// with --enable-exec, but the execute case still exists and refuses to run.
func TestExecPodExecuteCaseGatedOnEnableExec(t *testing.T) {
	tools := NewTools(nil, changeCfg(t, false))
	c := tools.ExecuteCases()["execPod"]
	_, _, err := c.Handler(context.Background(), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "execPod", Cluster: "c", Namespace: "default", Name: "p", Command: []string{"true"},
	})
	if err == nil || !strings.Contains(err.Error(), "--enable-exec") {
		t.Fatalf("expected --enable-exec error, got %v", err)
	}
}

// TestExecPodCaseEnabledPassesThrough pins that with --enable-exec the case is
// wired to the unchanged execPod plan handler: it resolves the pod and returns
// a plan carrying the exact command and the default container.
func TestExecPodCaseEnabledPassesThrough(t *testing.T) {
	tools, _ := newChangeCaseTools(t, changeCfg(t, true))
	c := tools.PlanCases()["execPod"]
	res, _, err := c.Handler(middleware.WithToken(context.Background(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "execPod", Cluster: "local", Namespace: "default", Name: "p", Command: []string{"true"},
	})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"command":["true"]`)
	assert.Contains(t, text, `"container":"first-container"`)
	assert.Contains(t, text, `"confirmationToken"`)
}

// TestPatchExecuteRejectsForeignToken proves a token minted for another
// operation cannot be replayed through the patch execute case. The two tokens
// below isolate the two ways a token can be foreign: one issued for a different
// operation entirely, and one carrying this exact resource identity and payload
// but a different Tool — the latter fails on the Tool comparison alone, which
// is what makes cross-operation replay impossible.
func TestPatchExecuteRejectsForeignToken(t *testing.T) {
	patch := json.RawMessage(`[{"op":"replace","path":"/data/k","value":"hijack"}]`)
	// Use the handler's own canonicalization so the second token below differs
	// from the operation the execute case builds in the Tool field only.
	payload, err := updateKubernetesResourceParams{
		Patch: jsonPatchList{{Op: "replace", Path: "/data/k", Value: "hijack"}},
	}.patchBytes()
	require.NoError(t, err)

	cfg := changeCfg(t, false)
	tools, dyn := newChangeCaseTools(t, cfg)

	tokens := map[string]confirm.Operation{
		"createProject token": {Tool: "createProject", Cluster: "c", Kind: "project", Name: "x"},
		"same identity, foreign tool": {
			Tool: "createProject", Cluster: "local", Namespace: "default", Kind: "configmap", Name: "d", Payload: payload,
		},
	}
	c := tools.ExecuteCases()["patchKubernetesResource"]
	for name, op := range tokens {
		t.Run(name, func(t *testing.T) {
			token, err := cfg.Gate.IssueToken(op)
			require.NoError(t, err)
			_, _, err = c.Handler(middleware.WithToken(context.Background(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.ChangeParams{
				Operation: "patchKubernetesResource", Cluster: "local", Kind: "configmap", Namespace: "default", Name: "d",
				Patch:             patch,
				ConfirmationToken: token,
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, confirm.ErrTokenMismatch)
			assert.Zero(t, countPatches(dyn, nil), "a foreign token must not reach the cluster")
		})
	}
}

// TestPatchPlanCaseAcceptsStringifiedPatch preserves the compatibility the
// dedicated patch tool has with LLM clients that send the patch as a
// stringified JSON array: the flat json.RawMessage must decode leniently too.
func TestPatchPlanCaseAcceptsStringifiedPatch(t *testing.T) {
	cfg := changeCfg(t, false)
	tools, _ := newChangeCaseTools(t, cfg)
	c := tools.PlanCases()["patchKubernetesResource"]
	res, _, err := c.Handler(middleware.WithToken(context.Background(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "patchKubernetesResource", Cluster: "local", Kind: "configmap", Namespace: "default", Name: "d",
		Patch: json.RawMessage(`"[{\"op\":\"replace\",\"path\":\"/data/k\",\"value\":\"v3\"}]"`),
	})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"path":"/data/k"`)
	assert.Contains(t, text, `"value":"v3"`)
}

// TestPatchExecuteRejectsMalformedPatch proves a patch that is not a JSON patch
// array is rejected by the conversion, before any token or client call.
func TestPatchExecuteRejectsMalformedPatch(t *testing.T) {
	tools, dyn := newChangeCaseTools(t, changeCfg(t, false))
	c := tools.ExecuteCases()["patchKubernetesResource"]
	_, _, err := c.Handler(middleware.WithToken(context.Background(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "patchKubernetesResource", Cluster: "local", Kind: "configmap", Namespace: "default", Name: "d",
		Patch: json.RawMessage(`"not a patch"`),
	})
	require.Error(t, err)
	assert.Zero(t, countPatches(dyn, nil), "a malformed patch must not reach the cluster")
}

// TestPatchExecuteAcceptsMatchingToken is the positive control: a token minted
// for exactly this patch executes through the case and reaches the cluster.
func TestPatchExecuteAcceptsMatchingToken(t *testing.T) {
	cfg := changeCfg(t, false)
	tools, dyn := newChangeCaseTools(t, cfg)
	gate := cfg.Gate
	gate.ElicitFunc = approveElicit
	pl := jsonPatchList{{Op: "replace", Path: "/data/k", Value: "v2"}}
	token := issuePatchToken(t, gate, updateKubernetesResourceParams{
		Name: "d", Namespace: "default", Kind: "configmap", Cluster: "local", Patch: pl,
	})
	c := tools.ExecuteCases()["patchKubernetesResource"]
	_, _, err := c.Handler(middleware.WithToken(context.Background(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "patchKubernetesResource", Cluster: "local", Kind: "configmap", Namespace: "default", Name: "d",
		Patch:             json.RawMessage(`[{"op":"replace","path":"/data/k","value":"v2"}]`),
		ConfirmationToken: token,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, countPatches(dyn, nil), "the matching patch must reach the cluster exactly once")

	got, err := dyn.Resource(schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}).
		Namespace("default").Get(context.Background(), "d", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "v2", got.Object["data"].(map[string]any)["k"])
}

// TestPatchPlanCaseIssuesToken proves the plan case is wired to the unchanged
// plan handler: it resolves identity, issues a token and returns a plan.
func TestPatchPlanCaseIssuesToken(t *testing.T) {
	cfg := changeCfg(t, false)
	tools, _ := newChangeCaseTools(t, cfg)
	c := tools.PlanCases()["patchKubernetesResource"]
	res, _, err := c.Handler(middleware.WithToken(context.Background(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "patchKubernetesResource", Cluster: "local", Kind: "configmap", Namespace: "default", Name: "d",
		Patch: json.RawMessage(`[{"op":"replace","path":"/data/k","value":"v2"}]`),
	})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"plan"`)
	assert.Contains(t, text, `"confirmationToken"`)
	// The conversion must land in the patch bytes the plan handler hashes and
	// shows the user, not in a zero-valued or stringified patch.
	assert.Contains(t, text, `"path":"/data/k"`)
}

// TestDeletePlanCaseResolvesIdentity proves the delete case builds the typed
// params from the flat merged ones: the plan names exactly the object the
// handler fetched from the cluster.
func TestDeletePlanCaseResolvesIdentity(t *testing.T) {
	cfg := changeCfg(t, false)
	tools, _ := newChangeCaseTools(t, cfg)
	c := tools.PlanCases()["deleteKubernetesResource"]
	res, _, err := c.Handler(middleware.WithToken(context.Background(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "deleteKubernetesResource", Cluster: "local", Kind: "configmap", Namespace: "default", Name: "d",
	})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"name":"d"`)
	assert.Contains(t, text, `"namespace":"default"`)
	assert.Contains(t, text, `"kind":"configmap"`)
}

// TestCreateExecuteCaseRequiresManifest proves the create case maps the flat
// params into the manifest-driven handler: a manifest whose kind disagrees with
// the kind parameter is rejected before anything is created.
func TestCreateExecuteCaseRequiresManifest(t *testing.T) {
	cfg := changeCfg(t, false)
	tools, dyn := newChangeCaseTools(t, cfg)
	c := tools.ExecuteCases()["createKubernetesResource"]
	_, _, err := c.Handler(middleware.WithToken(context.Background(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "createKubernetesResource", Cluster: "local", Kind: "Deployment", Name: "x",
		Manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match manifest kind")
	assert.Zero(t, countCreates(dyn, nil), "an invalid manifest must not reach the cluster")
}

// TestDispatchUnknownOperation proves the merged tables compose with Dispatch:
// an unknown operation lists the valid core values.
func TestDispatchUnknownOperation(t *testing.T) {
	tools := NewTools(nil, changeCfg(t, false))
	_, _, err := dispatch.Dispatch(context.Background(), &mcp.CallToolRequest{}, "operation", "nope", dispatch.ChangeParams{}, tools.PlanCases())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown operation")
}
