package core

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

func patchResourcePlanScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	return scheme
}

// newPatchPlanTestTools builds Tools backed by fake discovery (so custom
// resource kinds resolve) and a fake dynamic client, wrapped so tokens are
// validated too.
func newPatchPlanTestTools(t *testing.T, cfg toolconfig.Config, dyn *dynamicfake.FakeDynamicClient) *Tools {
	t.Helper()
	c := &client.Client{
		ClientSetCreator: fakeDiscoveryClientset(t, listAPIResourcesDiscovery),
		DynClientCreator: func(*rest.Config) (dynamic.Interface, error) {
			return dyn, nil
		},
	}
	return NewTools(test.WrapClient(c, "test-token"), cfg)
}

func TestUpdateKubernetesResourcePlan(t *testing.T) {
	tests := map[string]struct {
		params         updateKubernetesResourceParams
		expectedResult string
		expectedError  string
	}{
		"update configmap plan - add new key": {
			params: updateKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "ConfigMap",
				Cluster:   "local",
				Patch: []jsonPatch{
					{
						Op:    "add",
						Path:  "/data/key3",
						Value: "value3",
					},
				},
			},
			expectedResult: `{"plan": [{
				"type": "update",
				"payload": {
					"original": {"apiVersion":"v1","data":{"key1":"value1","key2":"value2"},"kind":"ConfigMap","metadata":{"name":"test-config","namespace":"default"}},
					"patch": [{"op": "add", "path": "/data/key3", "value": "value3"}],
					"patched": {"apiVersion":"v1","data":{"key1":"value1","key2":"value2","key3":"value3"},"kind":"ConfigMap","metadata":{"name":"test-config","namespace":"default"}}
				},
				"resource": {
					"name": "test-config",
					"kind": "ConfigMap",
					"cluster": "local",
					"namespace": "default"
				}
			}]}`,
		},
		"update configmap plan - replace existing key": {
			params: updateKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "ConfigMap",
				Cluster:   "local",
				Patch: []jsonPatch{
					{
						Op:    "replace",
						Path:  "/data/key1",
						Value: "updated-value",
					},
				},
			},
			expectedResult: `{"plan": [{
				"type": "update",
				"payload": {
					"original": {"apiVersion":"v1","data":{"key1":"value1","key2":"value2"},"kind":"ConfigMap","metadata":{"name":"test-config","namespace":"default"}},
					"patch": [{"op": "replace", "path": "/data/key1", "value": "updated-value"}],
					"patched": {"apiVersion":"v1","data":{"key1":"updated-value","key2":"value2"},"kind":"ConfigMap","metadata":{"name":"test-config","namespace":"default"}}
				},
				"resource": {
					"name": "test-config",
					"kind": "ConfigMap",
					"cluster": "local",
					"namespace": "default"
				}
			}]}`,
		},
		"update configmap plan - remove key": {
			params: updateKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "ConfigMap",
				Cluster:   "local",
				Patch: []jsonPatch{
					{
						Op:   "remove",
						Path: "/data/key2",
					},
				},
			},
			expectedResult: `{"plan": [{
				"type": "update",
				"payload": {
					"original": {"apiVersion":"v1","data":{"key1":"value1","key2":"value2"},"kind":"ConfigMap","metadata":{"name":"test-config","namespace":"default"}},
					"patch": [{"op": "remove", "path": "/data/key2"}],
					"patched": {"apiVersion":"v1","data":{"key1":"value1"},"kind":"ConfigMap","metadata":{"name":"test-config","namespace":"default"}}
				},
				"resource": {
					"name": "test-config",
					"kind": "ConfigMap",
					"cluster": "local",
					"namespace": "default"
				}
			}]}`,
		},
		"update plan - multiple patches": {
			params: updateKubernetesResourceParams{
				Name:      "my-deploy",
				Namespace: "staging",
				Kind:      "Deployment",
				Cluster:   "local",
				Patch: []jsonPatch{
					{
						Op:    "replace",
						Path:  "/spec/replicas",
						Value: 3,
					},
					{
						Op:    "add",
						Path:  "/metadata/labels/env",
						Value: "staging",
					},
				},
			},
			expectedResult: `{"plan": [{
				"type": "update",
				"payload": {
					"original": {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"labels":{"existing":"label"},"name":"my-deploy","namespace":"staging"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"myapp"}},"strategy":{},"template":{"metadata":{},"spec":{"containers":null}}},"status":{}},
					"patch": [
						{"op": "replace", "path": "/spec/replicas", "value": 3},
						{"op": "add", "path": "/metadata/labels/env", "value": "staging"}
					],
					"patched": {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"labels":{"env":"staging","existing":"label"},"name":"my-deploy","namespace":"staging"},"spec":{"replicas":3,"selector":{"matchLabels":{"app":"myapp"}},"strategy":{},"template":{"metadata":{},"spec":{"containers":null}}},"status":{}}
				},
				"resource": {
					"name": "my-deploy",
					"kind": "Deployment",
					"cluster": "local",
					"namespace": "staging"
				}
			}]}`,
		},
		"update plan - cluster-scoped resource": {
			params: updateKubernetesResourceParams{
				Name:      "my-ns",
				Namespace: "",
				Kind:      "Namespace",
				Cluster:   "local",
				Patch: []jsonPatch{
					{
						Op:    "add",
						Path:  "/metadata/labels/team",
						Value: "platform",
					},
				},
			},
			expectedResult: `{"plan": [{
				"type": "update",
				"payload": {
					"original": {"apiVersion":"v1","kind":"Namespace","metadata":{"labels":{"existing":"label"},"name":"my-ns"},"spec":{},"status":{}},
					"patch": [{"op": "add", "path": "/metadata/labels/team", "value": "platform"}],
					"patched": {"apiVersion":"v1","kind":"Namespace","metadata":{"labels":{"existing":"label","team":"platform"},"name":"my-ns"},"spec":{},"status":{}}
				},
				"resource": {
					"name": "my-ns",
					"kind": "Namespace",
					"cluster": "local",
					"namespace": ""
				}
			}]}`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var fakeDynClient *dynamicfake.FakeDynamicClient

			// Create different mock resources based on the test kind
			if tt.params.Kind == "ConfigMap" {
				fakeConfigMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "default",
					},
					Data: map[string]string{
						"key1": "value1",
						"key2": "value2",
					},
				}
				fakeDynClient = dynamicfake.NewSimpleDynamicClient(patchResourcePlanScheme(), fakeConfigMap)
			} else if tt.params.Kind == "Deployment" {
				fakeDeployment := &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-deploy",
						Namespace: "staging",
						Labels:    map[string]string{"existing": "label"},
					},
					Spec: appsv1.DeploymentSpec{
						Replicas: &[]int32{1}[0],
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"app": "myapp",
							},
						},
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{},
						},
					},
				}
				fakeDynClient = dynamicfake.NewSimpleDynamicClient(patchResourcePlanScheme(), fakeDeployment)
			} else if tt.params.Kind == "Namespace" {
				fakeNamespace := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name:   "my-ns",
						Labels: map[string]string{"existing": "label"},
					},
				}
				fakeDynClient = dynamicfake.NewSimpleDynamicClient(patchResourcePlanScheme(), fakeNamespace)
			}

			tools := newPatchPlanTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil)}, fakeDynClient)
			req := test.NewCallToolRequest("https://localhost:8080")
			ctx := middleware.WithToken(t.Context(), "test-token")

			result, _, err := tools.updateKubernetesResourcePlan(ctx, req, tt.params)

			if tt.expectedError != "" {
				assert.ErrorContains(t, err, tt.expectedError)
			} else {
				require.NoError(t, err)
				// The response also carries a confirmation block whose token
				// varies per run; only the plan is compared here (the token is
				// covered by TestUpdateKubernetesResourcePlanToken).
				var parsed struct {
					Plan []response.PlanResource `json:"plan"`
				}
				require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))
				planBytes, err := json.Marshal(parsed.Plan)
				require.NoError(t, err)
				assert.JSONEq(t, tt.expectedResult, `{"plan":`+string(planBytes)+`}`)
			}
		})
	}
}

// TestUpdateKubernetesResourcePlanToken exercises the plan-token round trip: the
// token in the plan response must be accepted by the same gate for the exact
// patch the execute tool will apply.
func TestUpdateKubernetesResourcePlanToken(t *testing.T) {
	fakeConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-config",
			Namespace: "default",
		},
		Data: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}
	dyn := dynamicfake.NewSimpleDynamicClient(patchResourcePlanScheme(), fakeConfigMap)
	gate := fakeGates(t, nil)
	tools := newPatchPlanTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := updateKubernetesResourceParams{
		Name:      "test-config",
		Namespace: "default",
		Kind:      "ConfigMap",
		Cluster:   "local",
		Patch:     jsonPatchList{{Op: "add", Path: "/data/key3", Value: "value3"}},
	}

	result, _, err := tools.updateKubernetesResourcePlan(middleware.WithToken(t.Context(), "test-token"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	var parsed struct {
		Plan         []response.PlanResource `json:"plan"`
		Confirmation struct {
			Token     string    `json:"confirmationToken"`
			ExpiresAt time.Time `json:"expiresAt"`
			Note      string    `json:"note"`
		} `json:"confirmation"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))
	require.NotEmpty(t, parsed.Confirmation.Token, "plan response must carry a confirmationToken")
	require.Len(t, parsed.Plan, 1)
	assert.Equal(t, "update", string(parsed.Plan[0].Type))
	assert.Equal(t, "test-config", parsed.Plan[0].Resource.Name)
	assert.Equal(t, "ConfigMap", parsed.Plan[0].Resource.Kind)
	assert.WithinDuration(t, time.Now().Add(gate.TokenTTL), parsed.Confirmation.ExpiresAt, time.Minute)

	op := confirm.Operation{
		Tool: "patchKubernetesResource", Cluster: params.Cluster, Namespace: params.Namespace,
		Kind: params.Kind, Name: params.Name, Payload: patchPayload(t, params.Patch),
	}
	require.NoError(t, gate.RequireToken(op, parsed.Confirmation.Token),
		"plan token must be accepted by the same gate for the exact patch bytes")

	// The token is single-use: a second validation of the same plan fails.
	assert.ErrorIs(t, gate.RequireToken(op, parsed.Confirmation.Token), confirm.ErrTokenConsumed)
}

// TestUpdateKubernetesResourcePlanCustomCR proves the plan tool patches a custom
// resource by resolving it through the optional apiVersion hint.
func TestUpdateKubernetesResourcePlanCustomCR(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachines"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(patchResourcePlanScheme(), map[schema.GroupVersionResource]string{
		gvr: "VirtualMachineList",
	}, fakeVirtualMachineForPatch)
	gate := fakeGates(t, nil)
	tools := newPatchPlanTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := updateKubernetesResourceParams{
		Name:       "vm-1",
		Namespace:  "default",
		Kind:       "VirtualMachine",
		APIVersion: "harvesterhci.io/v1beta1",
		Cluster:    "local",
		Patch:      jsonPatchList{{Op: "replace", Path: "/spec/runStrategy", Value: "Once"}},
	}

	result, _, err := tools.updateKubernetesResourcePlan(middleware.WithToken(t.Context(), "test-token"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	var parsed struct {
		Plan         []response.PlanResource `json:"plan"`
		Confirmation struct {
			Token string `json:"confirmationToken"`
		} `json:"confirmation"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))
	require.Len(t, parsed.Plan, 1)
	assert.Equal(t, "VirtualMachine", parsed.Plan[0].Resource.Kind)
	require.NotEmpty(t, parsed.Confirmation.Token)

	require.NoError(t, gate.RequireToken(confirm.Operation{
		Tool: "patchKubernetesResource", Cluster: "local", Namespace: "default",
		Kind: "VirtualMachine", Name: "vm-1", Payload: patchPayload(t, params.Patch),
	}, parsed.Confirmation.Token), "plan token must match the patch operation")
}
