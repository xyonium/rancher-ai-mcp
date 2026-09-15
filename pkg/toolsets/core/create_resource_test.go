package core

import (
	"context"
	"encoding/json"
	"errors"
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"
)

func createResourceScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return scheme
}

// virtualMachineManifest is a custom resource served by harvesterhci.io.
const virtualMachineManifest = `apiVersion: harvesterhci.io/v1beta1
kind: VirtualMachine
metadata:
  name: test-vm
  namespace: default
spec:
  runStrategy: Always`

// fakeGates returns a real confirmation gate whose elicitation is replaced by
// the given function. A nil fn installs one that fails the test if called.
func fakeGates(t *testing.T, fn func(ctx context.Context, ss *mcp.ServerSession, params *mcp.ElicitParams) (*mcp.ElicitResult, error)) *confirm.Gate {
	t.Helper()
	gate, err := confirm.NewGate()
	require.NoError(t, err)
	if fn == nil {
		fn = func(context.Context, *mcp.ServerSession, *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			t.Error("elicitation must not be called")
			return nil, errors.New("unexpected elicitation")
		}
	}
	gate.ElicitFunc = fn
	return gate
}

// approveElicit accepts the confirmation form with "approve".
func approveElicit(context.Context, *mcp.ServerSession, *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": "approve"}}, nil
}

// declineElicit rejects the confirmation form.
func declineElicit(context.Context, *mcp.ServerSession, *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	return &mcp.ElicitResult{Action: "decline"}, nil
}

// countCreates returns how many create actions the fake dynamic client recorded,
// optionally filtered to one GVR.
func countCreates(dyn *dynamicfake.FakeDynamicClient, gvr *schema.GroupVersionResource) int {
	n := 0
	for _, action := range dyn.Actions() {
		if action.GetVerb() != "create" {
			continue
		}
		if gvr != nil && action.GetResource() != *gvr {
			continue
		}
		n++
	}
	return n
}

// newCreateTestTools builds Tools backed by false discovery serving the
// Harvester API and a fake dynamic client, wrapped so tokens are validated too.
func newCreateTestTools(t *testing.T, cfg toolconfig.Config, dyn *dynamicfake.FakeDynamicClient) *Tools {
	t.Helper()
	c := &client.Client{
		ClientSetCreator: fakeDiscoveryClientset(t, listAPIResourcesDiscovery),
		DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
	return NewTools(test.WrapClient(c, "fakeToken"), cfg)
}

// TestCreateCustomCRManifestDriven proves the create tool resolves the GVR from
// the manifest's own apiVersion/kind via discovery, so custom resources work.
func TestCreateCustomCRManifestDriven(t *testing.T) {
	fakeToken := "fakeToken"
	gvr := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachines"}

	gate := fakeGates(t, approveElicit)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createResourceScheme(), map[schema.GroupVersionResource]string{
		gvr: "VirtualMachineList",
	})
	tools := newCreateTestTools(t, toolconfig.Config{Gate: gate}, dyn)
	req := &mcp.CallToolRequest{}

	token, err := gate.IssueToken(confirm.Operation{
		Tool: "createKubernetesResource", Cluster: "local", Namespace: "default",
		Kind: "VirtualMachine", Name: "test-vm", Payload: canonicalManifestPayload(t, virtualMachineManifest),
	})
	require.NoError(t, err)

	result, _, err := tools.createKubernetesResource(middleware.WithToken(t.Context(), fakeToken), req, createKubernetesResourceParams{
		Name:              "test-vm",
		Namespace:         "default",
		Kind:              "VirtualMachine",
		Cluster:           "local",
		Manifest:          virtualMachineManifest,
		ConfirmationToken: token,
	})
	require.NoError(t, err)

	var parsed struct {
		LLM []map[string]any `json:"llm"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))
	require.Len(t, parsed.LLM, 1)
	assert.Equal(t, "VirtualMachine", parsed.LLM[0]["kind"])
	assert.Equal(t, "harvesterhci.io/v1beta1", parsed.LLM[0]["apiVersion"])

	require.Equal(t, 1, countCreates(dyn, &gvr), "create must land on the discovered CR GVR")
	created := dyn.Actions()[len(dyn.Actions())-1].(clienttesting.CreateAction).GetObject()
	assert.Equal(t, "test-vm", created.(*unstructured.Unstructured).GetName())
}

// canonicalManifestPayload parses the manifest and canonicalizes it exactly like
// the tool does, so a token issued in the test matches the operation.
func canonicalManifestPayload(t *testing.T, manifest string) []byte {
	t.Helper()
	obj := &unstructured.Unstructured{}
	require.NoError(t, yaml.Unmarshal([]byte(manifest), obj))
	payload, err := json.Marshal(obj.Object)
	require.NoError(t, err)
	return payload
}

// TestCreateRequiresToken proves a create without a confirmation token is
// rejected by the token gate and never reaches the cluster.
func TestCreateRequiresToken(t *testing.T) {
	fakeToken := "fakeToken"
	gvr := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachines"}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createResourceScheme(), map[schema.GroupVersionResource]string{
		gvr: "VirtualMachineList",
	})
	tools := newCreateTestTools(t, toolconfig.Config{Gate: fakeGates(t, approveElicit)}, dyn)

	_, _, err := tools.createKubernetesResource(middleware.WithToken(t.Context(), fakeToken), &mcp.CallToolRequest{}, createKubernetesResourceParams{
		Name:      "test-vm",
		Namespace: "default",
		Kind:      "VirtualMachine",
		Cluster:   "local",
		Manifest:  virtualMachineManifest,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Zero(t, countCreates(dyn, nil), "no create must reach the cluster without a token")
}

// TestCreateDeclined proves a declined elicitation yields the standard
// cancellation result and executes nothing.
func TestCreateDeclined(t *testing.T) {
	fakeToken := "fakeToken"
	gvr := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachines"}

	gate := fakeGates(t, declineElicit)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createResourceScheme(), map[schema.GroupVersionResource]string{
		gvr: "VirtualMachineList",
	})
	tools := newCreateTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	token, err := gate.IssueToken(confirm.Operation{
		Tool: "createKubernetesResource", Cluster: "local", Namespace: "default",
		Kind: "VirtualMachine", Name: "test-vm", Payload: canonicalManifestPayload(t, virtualMachineManifest),
	})
	require.NoError(t, err)

	result, _, err := tools.createKubernetesResource(middleware.WithToken(t.Context(), fakeToken), &mcp.CallToolRequest{}, createKubernetesResourceParams{
		Name:              "test-vm",
		Namespace:         "default",
		Kind:              "VirtualMachine",
		Cluster:           "local",
		Manifest:          virtualMachineManifest,
		ConfirmationToken: token,
	})
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "Operation cancelled by the user. Nothing was executed.", result.Content[0].(*mcp.TextContent).Text)
	assert.Zero(t, countCreates(dyn, nil), "declined create must not reach the cluster")
}

// TestCreateAutoWriteSkipsGate proves auto-write mode bypasses both the token
// and the user confirmation.
func TestCreateAutoWriteSkipsGate(t *testing.T) {
	fakeToken := "fakeToken"
	gvr := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachines"}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createResourceScheme(), map[schema.GroupVersionResource]string{
		gvr: "VirtualMachineList",
	})
	// fakeGates(t, nil) makes elicitation a test failure if it is ever invoked.
	tools := newCreateTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil), AutoWrite: true}, dyn)

	result, _, err := tools.createKubernetesResource(middleware.WithToken(t.Context(), fakeToken), &mcp.CallToolRequest{}, createKubernetesResourceParams{
		Name:      "test-vm",
		Namespace: "default",
		Kind:      "VirtualMachine",
		Cluster:   "local",
		Manifest:  virtualMachineManifest,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	assert.Equal(t, 1, countCreates(dyn, &gvr), "auto-write create must be executed")
}

// TestCreateKindMismatchRejected proves the manifest's kind is authoritative:
// a mismatching kind parameter is rejected before any cluster call.
func TestCreateKindMismatchRejected(t *testing.T) {
	fakeToken := "fakeToken"
	dyn := dynamicfake.NewSimpleDynamicClient(createResourceScheme())
	tools := newCreateTestTools(t, toolconfig.Config{Gate: fakeGates(t, approveElicit)}, dyn)

	_, _, err := tools.createKubernetesResource(middleware.WithToken(t.Context(), fakeToken), &mcp.CallToolRequest{}, createKubernetesResourceParams{
		Name:      "test-vm",
		Namespace: "default",
		Kind:      "ConfigMap",
		Cluster:   "local",
		Manifest:  virtualMachineManifest,
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, `kind parameter "ConfigMap" does not match manifest kind "VirtualMachine"`)
	assert.Zero(t, countCreates(dyn, nil))
}

func TestCreateKubernetesResource(t *testing.T) {
	fakeToken := "fakeToken"

	configMapYAML := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
  namespace: default
data:
  key1: value1
  key2: value2`

	configMapJSON := `{
		"apiVersion": "v1",
		"kind": "ConfigMap",
		"metadata": {"name": "test-config", "namespace": "default"},
		"data": {"key1": "value1", "key2": "value2"}
	}`

	tests := map[string]struct {
		params         createKubernetesResourceParams
		fakeDynClient  *dynamicfake.FakeDynamicClient
		expectToken    bool
		expectedResult string
		expectedError  string
	}{
		"create configmap from YAML": {
			params: createKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "ConfigMap",
				Cluster:   "local",
				Manifest:  configMapYAML,
			},
			expectToken: true,
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createResourceScheme(), map[schema.GroupVersionResource]string{
				{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
			}),
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "v1",
						"data": {"key1": "value1", "key2": "value2"},
						"kind": "ConfigMap",
						"metadata": {"name": "test-config", "namespace": "default"}
					}
				],
				"uiContext": [
					{"namespace": "default", "kind": "ConfigMap", "cluster": "local", "name": "test-config", "type": "configmap"}
				]
			}`,
		},
		"create configmap from JSON when tool is configured with URL": {
			params: createKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Manifest:  configMapJSON,
			},
			expectToken: true,
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createResourceScheme(), map[schema.GroupVersionResource]string{
				{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
			}),
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "v1",
						"data": {"key1": "value1", "key2": "value2"},
						"kind": "ConfigMap",
						"metadata": {"name": "test-config", "namespace": "default"}
					}
				],
				"uiContext": [
					{"namespace": "default", "kind": "ConfigMap", "cluster": "local", "name": "test-config", "type": "configmap"}
				]
			}`,
		},

		"create configmap - malformed manifest": {
			params: createKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Manifest:  "foo: [bar",
			},
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(createResourceScheme()),
			expectedError: `failed to parse manifest`,
		},
		"create configmap - not an object": {
			params: createKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Manifest:  "invalid-resource-type",
			},
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createResourceScheme(), map[schema.GroupVersionResource]string{
				{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
			}),
			expectedError: "failed to parse manifest",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			gate := fakeGates(t, approveElicit)
			tools := newCreateTestTools(t, toolconfig.Config{Gate: gate}, tt.fakeDynClient)

			params := tt.params
			if tt.expectToken {
				// The execute tool requires the single-use token minted by the
				// plan tool for exactly this operation.
				params.ConfirmationToken = issueTokenFor(t, gate, params)
			}

			result, _, err := tools.createKubernetesResource(middleware.WithToken(t.Context(), fakeToken), &mcp.CallToolRequest{}, params)

			if tt.expectedError != "" {
				assert.ErrorContains(t, err, tt.expectedError)
			} else {
				require.NoError(t, err)
				assert.JSONEq(t, tt.expectedResult, result.Content[0].(*mcp.TextContent).Text)
			}
		})
	}
}

// issueTokenFor mints the confirmation token the plan tool would have issued for
// the given create parameters.
func issueTokenFor(t *testing.T, gate *confirm.Gate, params createKubernetesResourceParams) string {
	t.Helper()
	obj := &unstructured.Unstructured{}
	require.NoError(t, yaml.Unmarshal([]byte(params.Manifest), obj))
	payload, err := json.Marshal(obj.Object)
	require.NoError(t, err)
	namespace := params.Namespace
	if namespace == "" {
		namespace = obj.GetNamespace()
	}
	token, err := gate.IssueToken(confirm.Operation{
		Tool: "createKubernetesResource", Cluster: params.Cluster, Namespace: namespace,
		Kind: obj.GetKind(), Name: obj.GetName(), Payload: payload,
	})
	require.NoError(t, err)
	return token
}
