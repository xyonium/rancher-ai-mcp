package core

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func TestCreateKubernetesResourcePlan(t *testing.T) {
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
		expectedResult string
		expectedError  string
	}{
		"create configmap plan from YAML": {
			params: createKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Manifest:  configMapYAML,
			},
			expectedResult: `[{
				"type": "create",
				"payload": {
					"apiVersion": "v1",
					"kind": "ConfigMap",
					"metadata": {"name": "test-config", "namespace": "default"},
					"data": {"key1": "value1", "key2": "value2"}
				},
				"resource": {
					"name": "test-config",
					"kind": "ConfigMap",
					"cluster": "local",
					"namespace": "default"
				}
			}]`,
		},
		"create configmap plan from JSON": {
			params: createKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Manifest:  configMapJSON,
			},
			expectedResult: `[{
				"type": "create",
				"payload": {
					"apiVersion": "v1",
					"kind": "ConfigMap",
					"metadata": {"name": "test-config", "namespace": "default"},
					"data": {"key1": "value1", "key2": "value2"}
				},
				"resource": {
					"name": "test-config",
					"kind": "ConfigMap",
					"cluster": "local",
					"namespace": "default"
				}
			}]`,
		},
		"create plan - malformed manifest": {
			params: createKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Manifest:  "foo: [bar",
			},
			expectedError: "failed to parse manifest",
		},
		"create plan - not an object": {
			params: createKubernetesResourceParams{
				Name:      "test-config",
				Namespace: "default",
				Kind:      "configmap",
				Cluster:   "local",
				Manifest:  "invalid-resource-type",
			},
			expectedError: "failed to parse manifest",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			gate := fakeGates(t, nil)
			tools := Tools{cfg: toolconfig.Config{Gate: gate}}

			result, _, err := tools.createKubernetesResourcePlan(t.Context(), &mcp.CallToolRequest{}, test.params)

			if test.expectedError != "" {
				assert.ErrorContains(t, err, test.expectedError)
			} else {
				require.NoError(t, err)
				// The response now also carries the confirmation block; only
				// the plan is compared here (the token is covered below).
				var parsed struct {
					Plan []response.PlanResource `json:"plan"`
				}
				require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))
				planBytes, err := json.Marshal(parsed.Plan)
				require.NoError(t, err)
				assert.JSONEq(t, test.expectedResult, string(planBytes))
			}
		})
	}
}

// TestCreateKubernetesResourcePlanToken exercises the plan-token round trip: the
// token in the plan response must be accepted by the same gate for the exact
// operation the execute tool will perform.
func TestCreateKubernetesResourcePlanToken(t *testing.T) {
	configMapYAML := `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
  namespace: default
data:
  key1: value1
  key2: value2`

	params := createKubernetesResourceParams{
		Name:      "test-config",
		Namespace: "default",
		Kind:      "ConfigMap",
		Cluster:   "local",
		Manifest:  configMapYAML,
	}

	gate := fakeGates(t, nil)
	tools := Tools{cfg: toolconfig.Config{Gate: gate}}

	result, _, err := tools.createKubernetesResourcePlan(t.Context(), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	var parsed struct {
		Plan         []response.PlanResource `json:"plan"`
		Confirmation struct {
			Token     string    `json:"confirmationToken"`
			ExpiresAt time.Time `json:"expiresAt"`
			Note      string    `json:"note"`
		} `json:"confirmation"`
	}
	raw := result.Content[0].(*mcp.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(raw), &parsed))
	require.NotEmpty(t, parsed.Confirmation.Token, "plan response must carry a confirmationToken")
	require.Len(t, parsed.Plan, 1)
	assert.Equal(t, "create", string(parsed.Plan[0].Type))
	assert.Equal(t, "test-config", parsed.Plan[0].Resource.Name)
	assert.Equal(t, "ConfigMap", parsed.Plan[0].Resource.Kind)
	assert.WithinDuration(t, time.Now().Add(gate.TokenTTL), parsed.Confirmation.ExpiresAt, time.Minute)

	obj := &unstructured.Unstructured{}
	require.NoError(t, yaml.Unmarshal([]byte(params.Manifest), obj))
	payload, err := json.Marshal(obj.Object)
	require.NoError(t, err)

	err = gate.RequireToken(confirm.Operation{
		Tool: "createKubernetesResource", Cluster: params.Cluster, Namespace: "default",
		Kind: "ConfigMap", Name: "test-config", Payload: payload,
	}, parsed.Confirmation.Token)
	require.NoError(t, err, "plan token must be accepted by the same gate for the exact operation")

	// The token is single-use: a second validation of the same plan fails.
	err = gate.RequireToken(confirm.Operation{
		Tool: "createKubernetesResource", Cluster: params.Cluster, Namespace: "default",
		Kind: "ConfigMap", Name: "test-config", Payload: payload,
	}, parsed.Confirmation.Token)
	assert.ErrorIs(t, err, confirm.ErrTokenConsumed)
}

// TestCreateKubernetesResourcePlanCustomCR proves the plan tool accepts custom
// resource manifests, cross-checks the kind and issues a token bound to them.
func TestCreateKubernetesResourcePlanCustomCR(t *testing.T) {
	params := createKubernetesResourceParams{
		Name:      "test-vm",
		Namespace: "default",
		Kind:      "VirtualMachine",
		Cluster:   "local",
		Manifest:  virtualMachineManifest,
	}

	gate := fakeGates(t, nil)
	tools := Tools{cfg: toolconfig.Config{Gate: gate}}

	result, _, err := tools.createKubernetesResourcePlan(t.Context(), &mcp.CallToolRequest{}, params)
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
	assert.Equal(t, "default", parsed.Plan[0].Resource.Namespace)

	err = gate.RequireToken(confirm.Operation{
		Tool: "createKubernetesResource", Cluster: "local", Namespace: "default",
		Kind: "VirtualMachine", Name: "test-vm", Payload: canonicalManifestPayload(t, virtualMachineManifest),
	}, parsed.Confirmation.Token)
	require.NoError(t, err, "plan token must match the manifest-derived operation")
}
