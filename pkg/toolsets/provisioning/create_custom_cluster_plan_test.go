package provisioning

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestCreateCustomClusterPlan(t *testing.T) {
	tests := []struct {
		name            string
		fakeClientset   kubernetes.Interface
		fakeDynClient   *dynamicfake.FakeDynamicClient
		params          createCustomClusterParams
		finalK8sVersion string
		expectedError   string
		rke2KdmOutput   string
		k3sKdmOutput    string
	}{
		{
			name:          "Empty cluster name",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "",
				Distribution: "rke2",
			},
			expectedError: "name is required",
		},
		{
			name:          "invalid distribution",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "test",
				Distribution: "gke",
			},
			expectedError: "invalid value for Distribution: gke. Valid values are 'rke2' and 'k3s'",
		},
		{
			name:          "invalid CNI",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "test",
				Distribution: "rke2",
				CNI:          "faker",
			},
			expectedError: "unsupported CNI \"faker\". Valid values are \"calico\", \"canal\", \"cilium\", \"flannel\", \"multus,canal\", \"multus,cilium\", \"multus,calico\", \"none\"",
		},
		{
			name:          "invalid rke2 version",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "test",
				Distribution: "rke2",
				CNI:          "calico",
				Version:      "v2.28.0",
			},
			rke2KdmOutput: createDummyKDMData("v1.32.4+rke2r1", "v1.32.3+rke2r1"),
			expectedError: "unsupported Kubernetes version: v2.28.0 for distribution: rke2. Only support versions [v1.32.4+rke2r1 v1.32.3+rke2r1]",
		},
		{
			name:          "invalid k3s version",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "test",
				Distribution: "k3s",
				CNI:          "calico",
				Version:      "v2.28.0",
			},
			k3sKdmOutput:  createDummyKDMData("v1.32.4+k3s1", "v1.32.3+k3s1"),
			expectedError: "unsupported Kubernetes version: v2.28.0 for distribution: k3s. Only support versions [v1.32.4+k3s1 v1.32.3+k3s1]",
		},
		{
			name:          "valid rke2 cluster",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "test",
				Distribution: "rke2",
				CNI:          "calico",
				Version:      "v1.32.4+rke2r1",
			},
			finalK8sVersion: "v1.32.4+rke2r1",
			rke2KdmOutput:   createDummyKDMData("v1.32.4+rke2r1", "v1.32.3+rke2r1"),
		},
		{
			name:          "valid k3s cluster",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "test",
				Distribution: "k3s",
				CNI:          "calico",
				Version:      "v1.32.4+k3s1",
			},
			finalK8sVersion: "v1.32.4+k3s1",
			k3sKdmOutput:    createDummyKDMData("v1.32.4+k3s1", "v1.32.3+k3s1"),
		},
		{
			name:          "valid k3s cluster with uppercase CNI",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "test",
				Distribution: "k3s",
				CNI:          "Calico",
				Version:      "v1.32.4+k3s1",
			},
			finalK8sVersion: "v1.32.4+k3s1",
			k3sKdmOutput:    createDummyKDMData("v1.32.4+k3s1", "v1.32.3+k3s1"),
		},
		{
			name:          "valid k3s cluster using latest release",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "test",
				Distribution: "k3s",
				CNI:          "calico",
				Version:      "v1.32.4",
			},
			finalK8sVersion: "v1.32.4+k3s3",
			k3sKdmOutput:    createDummyKDMData("v1.32.4+k3s1", "v1.32.4+k3s2", "v1.32.4+k3s3"),
		},
		{
			name:          "valid rke2 cluster using latest release",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "test",
				Distribution: "rke2",
				CNI:          "calico",
				Version:      "v1.32.4",
			},
			finalK8sVersion: "v1.32.4+rke2r3",
			rke2KdmOutput:   createDummyKDMData("v1.32.4+rke2r1", "v1.32.4+rke2r2", "v1.32.4+rke2r3"),
		},
		{
			name:          "valid k3s with no KDM data",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(provisioningSchemes()),
			params: createCustomClusterParams{
				Name:         "test",
				Distribution: "k3s",
				CNI:          "calico",
				Version:      "v1.32.4",
			},
			finalK8sVersion: "v1.32.4+k3s3",
			k3sKdmOutput:    createDummyKDMData(""),
			rke2KdmOutput:   createDummyKDMData("v1.32.4+rke2r1", "v1.32.4+rke2r2", "v1.32.4+rke2r3"),
			expectedError:   "unsupported Kubernetes version: v1.32.4 for distribution: k3s. Only support versions []",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// setup dummy KDM endpoint
			svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1-k3s-release/releases":
					w.Write([]byte(test.k3sKdmOutput))
				case "/v1-rke2-release/releases":
					w.Write([]byte(test.rke2KdmOutput))
				}
			}))

			c, err := client.NewClient(true, svr.URL)
			if err != nil {
				t.Fatalf("failed to create client: %v", err)
			}
			c.ClientSetCreator = func(inConfig *rest.Config) (kubernetes.Interface, error) {
				return test.fakeClientset, nil
			}
			c.DynClientCreator = func(inConfig *rest.Config) (dynamic.Interface, error) {
				return test.fakeDynClient, nil
			}
			tools := Tools{client: c, cfg: toolconfig.Config{Gate: fakeGates(t, nil)}}

			result, _, err := tools.createCustomClusterPlan(context.Background(), &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name: "createCustomClusterPlan",
				},
			}, test.params)

			if test.expectedError != "" {
				assert.ErrorContains(t, err, test.expectedError)
			} else {
				assert.NoError(t, err)

				text, ok := result.Content[0].(*mcp.TextContent)
				assert.Truef(t, ok, "expected type *mcp.TextContent")

				// The response now also carries the confirmation block; only the
				// plan is compared here (the token is covered by
				// TestCreateCustomClusterPlanToken).
				assert.JSONEq(t, createCustomClusterPlanOutput(test.params, test.finalK8sVersion), planResponseWithoutConfirmation(t, text.Text))
			}

			svr.Close()
		})
	}
}

// TestCreateCustomClusterPlanToken exercises the plan-token round trip: the
// token in the plan response must be accepted by the same gate for the exact
// operation the execute tool will perform.
func TestCreateCustomClusterPlanToken(t *testing.T) {
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(createDummyKDMData("v1.32.4+rke2r1", "v1.32.3+rke2r1")))
	}))
	defer svr.Close()

	c, err := client.NewClient(true, svr.URL)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	c.DynClientCreator = func(inConfig *rest.Config) (dynamic.Interface, error) {
		return dynamicfake.NewSimpleDynamicClient(provisioningSchemes()), nil
	}
	gate := fakeGates(t, nil)
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "createCustomClusterPlan"}}

	params := createCustomClusterParams{Name: "test", Distribution: "rke2", CNI: "calico", Version: "v1.32.4+rke2r1"}

	result, _, err := tools.createCustomClusterPlan(context.Background(), req, params)
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

	// The token binds the exact cluster object the execute tool submits.
	obj, err := tools.CreateCustomClusterObj(req, params, zap.NewNop())
	require.NoError(t, err)
	payload, err := json.Marshal(obj.Object)
	require.NoError(t, err)

	err = gate.RequireToken(confirm.Operation{
		Tool: "createCustomCluster", Cluster: LocalCluster, Namespace: DefaultClusterResourcesNamespace,
		Kind: "cluster", Name: "test", Payload: payload,
	}, parsed.Confirmation.Token)
	require.NoError(t, err, "plan token must be accepted by the same gate for the exact operation")

	// The token is single-use: a second validation of the same plan fails.
	err = gate.RequireToken(confirm.Operation{
		Tool: "createCustomCluster", Cluster: LocalCluster, Namespace: DefaultClusterResourcesNamespace,
		Kind: "cluster", Name: "test", Payload: payload,
	}, parsed.Confirmation.Token)
	assert.ErrorIs(t, err, confirm.ErrTokenConsumed)
}

func createCustomClusterPlanOutput(params createCustomClusterParams, finalK8sVersion string) string {
	return fmt.Sprintf(`{"plan": [
  {
    "type": "create",
    "payload": {
      "apiVersion": "provisioning.cattle.io/v1",
      "kind": "Cluster",
      "metadata": {
        "annotations": {
          "field.cattle.io/description": "%s"
        },
        "name": "%s",
        "namespace": "fleet-default"
      },
      "spec": {
        "kubernetesVersion": "%s",
        "localClusterAuthEndpoint": {},
        "rkeConfig": {
          "chartValues": null,
          "dataDirectories": {},
          "etcd": {
            "snapshotRetention": 5,
            "snapshotScheduleCron": "0 */5 * * *"
          },
          "machineGlobalConfig": {
            "cni": "%s"
          },
          "machinePoolDefaults": {},
          "upgradeStrategy": {
            "controlPlaneConcurrency": "1",
            "controlPlaneDrainOptions": {
              "deleteEmptyDirData": true,
              "disableEviction": false,
              "enabled": false,
              "force": false,
              "gracePeriod": -1,
              "ignoreDaemonSets": true,
              "skipWaitForDeleteTimeoutSeconds": 0,
              "timeout": 120
            },
            "workerConcurrency": "1",
            "workerDrainOptions": {
              "deleteEmptyDirData": true,
              "disableEviction": false,
              "enabled": false,
              "force": false,
              "gracePeriod": -1,
              "ignoreDaemonSets": true,
              "skipWaitForDeleteTimeoutSeconds": 0,
              "timeout": 120
            }
          }
        }
      },
      "status": {
        "observedGeneration": 0
      }
    },
    "resource": {
      "name": "%s",
      "kind": "Cluster",
      "cluster": "local",
      "namespace": "fleet-default"
    }
  }
]}`, params.Description, params.Name, finalK8sVersion, params.CNI, params.Name)
}
