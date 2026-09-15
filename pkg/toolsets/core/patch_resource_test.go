package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

var fakeConfigMapForPatch = &corev1.ConfigMap{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "test-config",
		Namespace: "default",
	},
	Data: map[string]string{
		"key1": "value1",
		"key2": "value2",
	},
}

// fakeVirtualMachineForPatch is a custom resource served by harvesterhci.io; it
// can only be addressed with an explicit apiVersion, because VirtualMachine is
// also served by kubevirt.io.
var fakeVirtualMachineForPatch = &unstructured.Unstructured{
	Object: map[string]any{
		"apiVersion": "harvesterhci.io/v1beta1",
		"kind":       "VirtualMachine",
		"metadata": map[string]any{
			"name":      "vm-1",
			"namespace": "default",
		},
		"spec": map[string]any{
			"runStrategy": "Always",
		},
	},
}

func patchResourceScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return scheme
}

// countPatches returns how many patch actions the fake dynamic client recorded,
// optionally filtered to one GVR.
func countPatches(dyn *dynamicfake.FakeDynamicClient, gvr *schema.GroupVersionResource) int {
	n := 0
	for _, action := range dyn.Actions() {
		if action.GetVerb() != "patch" {
			continue
		}
		if gvr != nil && action.GetResource() != *gvr {
			continue
		}
		n++
	}
	return n
}

// newPatchTestTools builds Tools backed by fake discovery (so any kind resolves
// through apiVersion/kind) and a fake dynamic client, wrapped so tokens are
// validated too.
func newPatchTestTools(t *testing.T, cfg toolconfig.Config, dyn *dynamicfake.FakeDynamicClient) *Tools {
	t.Helper()
	c := &client.Client{
		ClientSetCreator: fakeDiscoveryClientset(t, listAPIResourcesDiscovery),
		DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
	return NewTools(test.WrapClient(c, "fakeToken"), cfg)
}

// patchPayload canonicalizes a patch list exactly like the patch tools do, so a
// token issued in a test matches the operation the handler builds.
func patchPayload(t *testing.T, patches jsonPatchList) []byte {
	t.Helper()
	payload, err := json.Marshal(patches)
	require.NoError(t, err)
	return payload
}

// issuePatchToken mints the confirmation token the plan tool would have issued
// for the given patch parameters.
func issuePatchToken(t *testing.T, gate *confirm.Gate, params updateKubernetesResourceParams) string {
	t.Helper()
	token, err := gate.IssueToken(confirm.Operation{
		Tool: "patchKubernetesResource", Cluster: params.Cluster, Namespace: params.Namespace,
		Kind: params.Kind, Name: params.Name, Payload: patchPayload(t, params.Patch),
	})
	require.NoError(t, err)
	return token
}

// TestPatchRequiresToken proves a patch without a confirmation token is rejected
// by the token gate and never reaches the cluster.
func TestPatchRequiresToken(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
		gvr: "ConfigMapList",
	}, fakeConfigMapForPatch)
	tools := newPatchTestTools(t, toolconfig.Config{Gate: fakeGates(t, approveElicit)}, dyn)

	_, _, err := tools.updateKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, updateKubernetesResourceParams{
		Name:      "test-config",
		Namespace: "default",
		Kind:      "configmap",
		Cluster:   "local",
		Patch:     jsonPatchList{{Op: "add", Path: "/data/key3", Value: "value3"}},
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Zero(t, countPatches(dyn, nil), "no patch must reach the cluster without a token")
}

// TestPatchTokenMismatchRejected proves a token minted for one patch cannot be
// reused for a different patch on the same resource.
func TestPatchTokenMismatchRejected(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
		gvr: "ConfigMapList",
	}, fakeConfigMapForPatch)
	gate := fakeGates(t, approveElicit)
	tools := newPatchTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	planned := updateKubernetesResourceParams{
		Name:      "test-config",
		Namespace: "default",
		Kind:      "configmap",
		Cluster:   "local",
		Patch:     jsonPatchList{{Op: "add", Path: "/data/key3", Value: "value3"}},
	}
	token := issuePatchToken(t, gate, planned)

	// Same resource identity, different patch content: the payload hash differs.
	tampered := planned
	tampered.Patch = jsonPatchList{{Op: "replace", Path: "/data/key1", Value: "hijacked"}}
	tampered.ConfirmationToken = token

	_, _, err := tools.updateKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, tampered)
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenMismatch)
	assert.Zero(t, countPatches(dyn, nil), "a mismatching patch must not reach the cluster")
}

// TestPatchDeclined proves a declined elicitation yields the standard
// cancellation result and executes nothing.
func TestPatchDeclined(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
		gvr: "ConfigMapList",
	}, fakeConfigMapForPatch)
	gate := fakeGates(t, declineElicit)
	tools := newPatchTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := updateKubernetesResourceParams{
		Name:      "test-config",
		Namespace: "default",
		Kind:      "configmap",
		Cluster:   "local",
		Patch:     jsonPatchList{{Op: "add", Path: "/data/key3", Value: "value3"}},
	}
	params.ConfirmationToken = issuePatchToken(t, gate, params)

	result, _, err := tools.updateKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "Operation cancelled by the user. Nothing was executed.", result.Content[0].(*mcp.TextContent).Text)
	assert.Zero(t, countPatches(dyn, nil), "declined patch must not reach the cluster")
}

// TestPatchCustomCRWithAPIVersion proves a custom resource patch is resolved
// through discovery by the optional apiVersion, so CRs are patchable.
func TestPatchCustomCRWithAPIVersion(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachines"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
		gvr: "VirtualMachineList",
	}, fakeVirtualMachineForPatch)
	gate := fakeGates(t, approveElicit)
	tools := newPatchTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := updateKubernetesResourceParams{
		Name:       "vm-1",
		Namespace:  "default",
		Kind:       "VirtualMachine",
		APIVersion: "harvesterhci.io/v1beta1",
		Cluster:    "local",
		Patch:      jsonPatchList{{Op: "replace", Path: "/spec/runStrategy", Value: "Once"}},
	}
	params.ConfirmationToken = issuePatchToken(t, gate, params)

	result, _, err := tools.updateKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	var parsed struct {
		LLM []map[string]any `json:"llm"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))
	require.Len(t, parsed.LLM, 1)
	assert.Equal(t, "VirtualMachine", parsed.LLM[0]["kind"])
	assert.Equal(t, "harvesterhci.io/v1beta1", parsed.LLM[0]["apiVersion"])

	require.Equal(t, 1, countPatches(dyn, &gvr), "patch must land on the discovered CR GVR")
}

// TestPatchAutoWrite proves auto-write mode bypasses both the token and the
// user confirmation.
func TestPatchAutoWrite(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
		gvr: "ConfigMapList",
	}, fakeConfigMapForPatch)
	// fakeGates(t, nil) makes elicitation a test failure if it is ever invoked.
	tools := newPatchTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil), AutoWrite: true}, dyn)

	result, _, err := tools.updateKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, updateKubernetesResourceParams{
		Name:      "test-config",
		Namespace: "default",
		Kind:      "configmap",
		Cluster:   "local",
		Patch:     jsonPatchList{{Op: "add", Path: "/data/key3", Value: "value3"}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	assert.Equal(t, 1, countPatches(dyn, &gvr), "auto-write patch must be executed")
}

// TestPatchSummaryContainsExactPatch proves the user is asked to approve the
// exact patch payload that will be applied.
func TestPatchSummaryContainsExactPatch(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
		gvr: "ConfigMapList",
	}, fakeConfigMapForPatch)

	var summary string
	gate := fakeGates(t, func(ctx context.Context, _ *mcp.ServerSession, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		summary = params.Message
		return approveElicit(ctx, nil, params)
	})
	tools := newPatchTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := updateKubernetesResourceParams{
		Name:      "test-config",
		Namespace: "default",
		Kind:      "configmap",
		Cluster:   "local",
		Patch:     jsonPatchList{{Op: "add", Path: "/data/key3", Value: "value3"}},
	}
	params.ConfirmationToken = issuePatchToken(t, gate, params)

	_, _, err := tools.updateKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)
	assert.Equal(t,
		"PATCH /v1, Resource=configmaps configmap/test-config in namespace \"default\" of cluster \"local\" with patch:\n"+
			`[{"op":"add","path":"/data/key3","value":"value3"}]`,
		summary, "the user must approve exactly this summary, patch JSON included")
}

func TestUpdateKubernetesResource(t *testing.T) {
	fakeUrl := "https://localhost:8080"
	fakeToken := "fakeToken"

	tests := map[string]struct {
		params        updateKubernetesResourceParams
		fakeDynClient *dynamicfake.FakeDynamicClient
		// used in the CallToolRequest
		requestURL string
		// used in the creation of the Tools.
		rancherURL     string
		expectedResult string
		expectedError  string
	}{
		"update configmap - add new key": {
			params: updateKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Patch: []jsonPatch{
					{
						Op:    "add",
						Path:  "/data/key3",
						Value: "value3",
					},
				},
			},
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
				{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
			}, fakeConfigMapForPatch),
			requestURL: fakeUrl,
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "v1",
						"data": {"key1": "value1", "key2": "value2", "key3": "value3"},
						"kind": "ConfigMap",
						"metadata": {"name": "test-config", "namespace": "default"}
					}
				],
				"uiContext": [
					{"cluster": "local", "kind": "ConfigMap", "name": "test-config", "namespace": "default", "type": "configmap"}
				]
			}`,
		},
		"update configmap - replace existing key": {
			params: updateKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Patch: []jsonPatch{
					{
						Op:    "replace",
						Path:  "/data/key1",
						Value: "updated-value",
					},
				},
			},
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
				{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
			}, fakeConfigMapForPatch),
			requestURL: fakeUrl,
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "v1",
						"data": {"key1": "updated-value", "key2": "value2"},
						"kind": "ConfigMap",
						"metadata": {"name": "test-config", "namespace": "default"}
					}
				],
				"uiContext": [
					{"cluster": "local", "kind": "ConfigMap", "name": "test-config", "namespace": "default", "type": "configmap"}
				]
			}`,
		},
		"update configmap - remove key": {
			params: updateKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Patch: []jsonPatch{
					{
						Op:   "remove",
						Path: "/data/key2",
					},
				},
			},
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
				{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
			}, fakeConfigMapForPatch),
			requestURL: fakeUrl,
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "v1",
						"data": {"key1": "value1"},
						"kind": "ConfigMap",
						"metadata": {"name": "test-config", "namespace": "default"}
					}
				],
				"uiContext": [
					{"cluster": "local", "kind": "ConfigMap", "name": "test-config", "namespace": "default", "type": "configmap"}
				]
			}`,
		},
		"update configmap when tool is configured with URL": {
			params: updateKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Patch: []jsonPatch{
					{
						Op:    "replace",
						Path:  "/data/key1",
						Value: "updated-value",
					},
				},
			},
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
				{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
			}, fakeConfigMapForPatch),
			rancherURL: fakeUrl,
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "v1",
						"data": {"key1": "updated-value", "key2": "value2"},
						"kind": "ConfigMap",
						"metadata": {"name": "test-config", "namespace": "default"}
					}
				],
				"uiContext": [
					{"cluster": "local", "kind": "ConfigMap", "name": "test-config", "namespace": "default", "type": "configmap"}
				]
			}`,
		},
		"update configmap - not found": {
			params: updateKubernetesResourceParams{
				Name:      "nonexistent-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Patch: []jsonPatch{
					{
						Op:    "replace",
						Path:  "/data/key1",
						Value: "value",
					},
				},
			},
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourceScheme(), map[schema.GroupVersionResource]string{
				{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
			}),
			requestURL:    fakeUrl,
			expectedError: `configmaps "nonexistent-config" not found`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			gate := fakeGates(t, approveElicit)
			tools := newPatchTestTools(t, toolconfig.Config{Gate: gate}, tt.fakeDynClient)
			req := test.NewCallToolRequest(tt.requestURL)

			params := tt.params
			// The execute tool requires the single-use token minted by the plan
			// tool for exactly this operation.
			params.ConfirmationToken = issuePatchToken(t, gate, params)

			result, _, err := tools.updateKubernetesResource(middleware.WithToken(t.Context(), fakeToken), req, params)

			if tt.expectedError != "" {
				assert.ErrorContains(t, err, tt.expectedError)
			} else {
				require.NoError(t, err)
				assert.JSONEq(t, tt.expectedResult, result.Content[0].(*mcp.TextContent).Text)
			}
		})
	}
}

func TestJsonPatchListUnmarshalJSON(t *testing.T) {
	expected := jsonPatchList{
		{Op: "replace", Path: "/spec/replicas", Value: float64(3)},
		{Op: "add", Path: "/metadata/labels/env", Value: "prod"},
	}

	tests := map[string]struct {
		input string
	}{
		"normal JSON array": {
			input: `[{"op":"replace","path":"/spec/replicas","value":3},{"op":"add","path":"/metadata/labels/env","value":"prod"}]`,
		},
		"JSON string containing stringified array": {
			input: `"[{\"op\":\"replace\",\"path\":\"/spec/replicas\",\"value\":3},{\"op\":\"add\",\"path\":\"/metadata/labels/env\",\"value\":\"prod\"}]"`,
		},
		"JSON string containing stringified array wrapped in single quotes": {
			input: `"'[{\"op\":\"replace\",\"path\":\"/spec/replicas\",\"value\":3},{\"op\":\"add\",\"path\":\"/metadata/labels/env\",\"value\":\"prod\"}]'"`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var got jsonPatchList
			err := json.Unmarshal([]byte(tt.input), &got)
			require.NoError(t, err)
			assert.Equal(t, expected, got)
		})
	}
}
