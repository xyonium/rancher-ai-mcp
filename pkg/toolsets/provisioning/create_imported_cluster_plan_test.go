package provisioning

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestCreateImportedClusterPlan(t *testing.T) {
	tests := []struct {
		name           string
		fakeClientset  kubernetes.Interface
		fakeDynClient  *dynamicfake.FakeDynamicClient
		params         createImportedClusterParams
		expectedError  string
		expectedResult string
	}{
		{
			name:          "valid parameters",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createImportedClusterParams{
				Name:                     "test-cluster",
				Description:              "A test cluster",
				VersionManagementSetting: "true",
			},
			expectedError: "",
			expectedResult: `{
  "payload": {
    "apiVersion": "management.cattle.io/v3",
    "description": "A test cluster",
    "kind": "Cluster",
    "metadata": {
      "annotations": {
        "rancher.io/imported-cluster-version-management": "true"
      },
      "name": "test-cluster",
      "namespace": "fleet-default"
    },
    "name": "test-cluster",
    "type": "cluster"
  },
  "resource": {
    "cluster": "local",
    "kind": "Cluster",
    "name": "test-cluster",
    "namespace": "fleet-default"
  },
  "type": "create"
}`,
		},
		{
			name:          "false version management",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createImportedClusterParams{
				Name:                     "test-cluster",
				Description:              "A test cluster",
				VersionManagementSetting: "false",
			},
			expectedError: "",
			expectedResult: `{
  "payload": {
    "apiVersion": "management.cattle.io/v3",
    "description": "A test cluster",
    "kind": "Cluster",
    "metadata": {
      "annotations": {
        "rancher.io/imported-cluster-version-management": "false"
      },
      "name": "test-cluster",
      "namespace": "fleet-default"
    },
    "name": "test-cluster",
    "type": "cluster"
  },
  "resource": {
    "cluster": "local",
    "kind": "Cluster",
    "name": "test-cluster",
    "namespace": "fleet-default"
  },
  "type": "create"
}`,
		},
		{
			name:          "missing version management, uses default",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createImportedClusterParams{
				Name:                     "test-cluster",
				Description:              "A test cluster",
				VersionManagementSetting: "",
			},
			expectedError: "",
			expectedResult: `{
  "payload": {
    "apiVersion": "management.cattle.io/v3",
    "description": "A test cluster",
    "kind": "Cluster",
    "metadata": {
      "annotations": {
        "rancher.io/imported-cluster-version-management": "system-default"
      },
      "name": "test-cluster",
      "namespace": "fleet-default"
    },
    "name": "test-cluster",
    "type": "cluster"
  },
  "resource": {
    "cluster": "local",
    "kind": "Cluster",
    "name": "test-cluster",
    "namespace": "fleet-default"
  },
  "type": "create"
}`,
		},
		{
			name:          "missing description",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createImportedClusterParams{
				Name:                     "test-cluster",
				Description:              "",
				VersionManagementSetting: "",
			},
			expectedError: "",
			expectedResult: `{
  "payload": {
    "apiVersion": "management.cattle.io/v3",
    "description": "",
    "kind": "Cluster",
    "metadata": {
      "annotations": {
        "rancher.io/imported-cluster-version-management": "system-default"
      },
      "name": "test-cluster",
      "namespace": "fleet-default"
    },
    "name": "test-cluster",
    "type": "cluster"
  },
  "resource": {
    "cluster": "local",
    "kind": "Cluster",
    "name": "test-cluster",
    "namespace": "fleet-default"
  },
  "type": "create"
}`,
		},
		{
			name:          "missing cluster name",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createImportedClusterParams{
				Name:                     "",
				Description:              "whats my name again",
				VersionManagementSetting: "",
			},
			expectedError:  "name is required",
			expectedResult: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := &client.Client{
				ClientSetCreator: func(inConfig *rest.Config) (kubernetes.Interface, error) {
					return test.fakeClientset, nil
				},
				DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) {
					return test.fakeDynClient, nil
				},
			}
			tools := Tools{client: c, cfg: toolconfig.Config{Gate: fakeGates(t, nil)}}

			result, _, err := tools.createImportedClusterPlan(context.Background(), &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name: "createImportedClusterPlan",
				},
			}, test.params)

			if test.expectedError != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, test.expectedError)
			} else {
				assert.NoError(t, err)

				text, ok := result.Content[0].(*mcp.TextContent)
				assert.Truef(t, ok, "expected type *mcp.TextContent")
				assert.Truef(t, ok, "expected expectedResult to be a JSON string")

				var parsed struct {
					Plan []map[string]interface{} `json:"plan"`
				}
				err = json.Unmarshal([]byte(text.Text), &parsed)
				require.NoError(t, err)
				require.NotEmpty(t, parsed.Plan)

				resultBytes, err := json.Marshal(parsed.Plan[0])
				assert.NoError(t, err)

				assert.JSONEq(t, test.expectedResult, string(resultBytes), "expected result does not match actual result")
			}
		})
	}
}

// TestCreateImportedClusterPlanToken exercises the plan-token round trip: the
// token in the plan response must be accepted by the same gate for the exact
// object the execute tool will submit.
func TestCreateImportedClusterPlanToken(t *testing.T) {
	gate := fakeGates(t, nil)
	tools := Tools{cfg: toolconfig.Config{Gate: gate}}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "createImportedClusterPlan"}}

	params := createImportedClusterParams{Name: "test-cluster", Description: "A test cluster"}

	result, _, err := tools.createImportedClusterPlan(context.Background(), req, params)
	require.NoError(t, err)

	var parsed struct {
		Plan         []map[string]any `json:"plan"`
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
	assert.WithinDuration(t, time.Now().Add(gate.TokenTTL), parsed.Confirmation.ExpiresAt, time.Minute)

	// The token binds the exact object the execute tool submits to the API.
	cluster, err := tools.createImportedClusterObj(params)
	require.NoError(t, err)
	payload, err := cluster.MarshalJSON()
	require.NoError(t, err)

	err = gate.RequireToken(confirm.Operation{
		Tool: "createImportedCluster", Cluster: LocalCluster, Kind: "cluster", Name: "test-cluster", Payload: payload,
	}, parsed.Confirmation.Token)
	require.NoError(t, err, "plan token must be accepted by the same gate for the exact operation")

	// The token is single-use: a second validation of the same plan fails.
	err = gate.RequireToken(confirm.Operation{
		Tool: "createImportedCluster", Cluster: LocalCluster, Kind: "cluster", Name: "test-cluster", Payload: payload,
	}, parsed.Confirmation.Token)
	assert.ErrorIs(t, err, confirm.ErrTokenConsumed)
}
