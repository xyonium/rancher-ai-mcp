package provisioning

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"go.uber.org/zap"
	"k8s.io/utils/ptr"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	provisioningV1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestScaleNodePoolPlan(t *testing.T) {
	tests := []struct {
		name           string
		fakeClientset  kubernetes.Interface
		fakeDynClient  *dynamicfake.FakeDynamicClient
		params         scaleNodePoolParameters
		expectedError  string
		expectedResult string
	}{
		{
			name:          "scale node pool with desired size",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         true,
						ControlPlaneRole: true,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](1),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:      "test-cluster",
				Namespace:    "fleet-default",
				NodePoolName: "test-nodepool",
				DesiredSize:  3,
			},
			expectedError: "",
			expectedResult: `{"plan": [
  {
    "type": "update",
    "payload": [
      {
        "op": "replace",
        "path": "/spec/rkeConfig/machinePools/0/quantity",
        "value": 3
      }
    ],
    "resource": {
      "name": "test-cluster",
      "kind": "provisioningcluster",
      "cluster": "local",
      "namespace": "fleet-default"
    }
  }
]}`,
		},
		{
			name:          "refuse to scale etcd node pool below 3 nodes",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         true,
						ControlPlaneRole: true,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](5),
					},
					{
						EtcdRole:         false,
						ControlPlaneRole: false,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](1),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:      "test-cluster",
				Namespace:    "fleet-default",
				NodePoolName: "test-nodepool",
				DesiredSize:  1,
			},
			expectedError:  "scaling an etcd node pool to less than 3 nodes can result in a loss of quorum and potential data loss",
			expectedResult: "",
		},
		{
			name:          "scale non-etcd pool to less than three nodes",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         true,
						ControlPlaneRole: true,
						WorkerRole:       true,
						Name:             "test-nodepool-etcd",
						Quantity:         ptr.To[int32](3),
					},
					{
						EtcdRole:         false,
						ControlPlaneRole: false,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](5),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:      "test-cluster",
				Namespace:    "fleet-default",
				NodePoolName: "test-nodepool",
				DesiredSize:  1,
			},
			expectedError: "",
			expectedResult: `{"plan": [
  {
    "type": "update",
    "payload": [
      {
        "op": "replace",
        "path": "/spec/rkeConfig/machinePools/1/quantity",
        "value": 1
      }
    ],
    "resource": {
      "name": "test-cluster",
      "kind": "provisioningcluster",
      "cluster": "local",
      "namespace": "fleet-default"
    }
  }
]}`,
		},
		{
			name:          "fail to provide desired size or amount to add/subtract",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         true,
						ControlPlaneRole: true,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](1),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "test-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 0,
				AmountToAdd:      0,
			},
			expectedError:  "either desiredSize, amountToAdd, or amountToSubtract must be specified. A node pool cannot be scaled to 0 nodes",
			expectedResult: "",
		},
		{
			name:          "try to scale to negative nodes",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         false,
						ControlPlaneRole: true,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](1),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "test-nodepool",
				DesiredSize:      -10,
				AmountToSubtract: 0,
				AmountToAdd:      0,
			},
			expectedError:  "desired size must be greater than or equal to 0",
			expectedResult: "",
		},
		{
			name:          "add a single node",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         false,
						ControlPlaneRole: false,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](1),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "test-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 0,
				AmountToAdd:      1,
			},
			expectedError: "",
			expectedResult: `{"plan": [
  {
    "type": "update",
    "payload": [
      {
        "op": "replace",
        "path": "/spec/rkeConfig/machinePools/0/quantity",
        "value": 2
      }
    ],
    "resource": {
      "name": "test-cluster",
      "kind": "provisioningcluster",
      "cluster": "local",
      "namespace": "fleet-default"
    }
  }
]}`,
		},
		{
			name:          "try to scale an etcd pool to an even number of nodes",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         true,
						ControlPlaneRole: true,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](1),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "test-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 0,
				AmountToAdd:      1,
			},
			expectedError: "etcd node pools should have an odd number of nodes to ensure fault tolerance and maintain quorum. Scaling to an even number of nodes can lead to split-brain scenarios and potential data loss",
		},
		{
			name:          "try to scale an etcd pool to more than 7 nodes",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         true,
						ControlPlaneRole: true,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](1),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "test-nodepool",
				DesiredSize:      8,
				AmountToSubtract: 0,
				AmountToAdd:      0,
			},
			expectedError: "it is not recommended to have more than 7 etcd nodes in a cluster as it can lead to performance issues",
		},
		{
			name:          "add two node to an etcd pool with less than three initial nodes",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         true,
						ControlPlaneRole: true,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](1),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "test-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 0,
				AmountToAdd:      2,
			},
			expectedError: "",
			expectedResult: `{"plan": [
  {
    "type": "update",
    "payload": [
      {
        "op": "replace",
        "path": "/spec/rkeConfig/machinePools/0/quantity",
        "value": 3
      }
    ],
    "resource": {
      "name": "test-cluster",
      "kind": "provisioningcluster",
      "cluster": "local",
      "namespace": "fleet-default"
    }
  }
]}`,
		},
		{
			name:          "subtract a single node",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         false,
						ControlPlaneRole: false,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](2),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "test-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 1,
				AmountToAdd:      0,
			},
			expectedError: "",
			expectedResult: `{"plan": [
  {
    "type": "update",
    "payload": [
      {
        "op": "replace",
        "path": "/spec/rkeConfig/machinePools/0/quantity",
        "value": 1
      }
    ],
    "resource": {
      "name": "test-cluster",
      "kind": "provisioningcluster",
      "cluster": "local",
      "namespace": "fleet-default"
    }
  }
]}`,
		},
		{
			name:          "refuse to subtract a node if it would scale pool to zero nodes",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         false,
						ControlPlaneRole: false,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](1),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "test-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 1,
				AmountToAdd:      0,
			},
			expectedError:  "A node pool cannot be scaled to 0 nodes or a negative number of nodes",
			expectedResult: "",
		},
		{
			name:          "refuse to subtract a node if it would scale pool to negative nodes",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         false,
						ControlPlaneRole: false,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](1),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "test-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 3,
				AmountToAdd:      0,
			},
			expectedError:  "A node pool cannot be scaled to 0 nodes or a negative number of nodes",
			expectedResult: "",
		},
		{
			name:          "refuse to subtract a node if it would result in etcd quorum loss",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         true,
						ControlPlaneRole: false,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](3),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "test-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 2,
				AmountToAdd:      0,
			},
			expectedError:  "scaling an etcd node pool to less than 3 nodes can result in a loss of quorum and potential data loss",
			expectedResult: "",
		},
		{
			name:          "scale a pool that doesn't exist",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         true,
						ControlPlaneRole: false,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](3),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "fake-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 2,
				AmountToAdd:      0,
			},
			expectedError:  "node pool fake-nodepool not found in cluster test-cluster",
			expectedResult: "",
		},
		{
			name:          "scale a cluster that doesn't have pools",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "fake-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 2,
				AmountToAdd:      0,
			},
			expectedError:  "cluster test-cluster has no Node Pools, cannot scale",
			expectedResult: "",
		},
		{
			name:          "attempt to both add and remove nodes",
			fakeClientset: newFakeClientSet(),
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
				newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
					{
						EtcdRole:         true,
						ControlPlaneRole: false,
						WorkerRole:       true,
						Name:             "test-nodepool",
						Quantity:         ptr.To[int32](3),
					},
				})),
			params: scaleNodePoolParameters{
				Cluster:          "test-cluster",
				Namespace:        "fleet-default",
				NodePoolName:     "fake-nodepool",
				DesiredSize:      0,
				AmountToSubtract: 2,
				AmountToAdd:      2,
			},
			expectedError:  "cannot specify both amountToAdd and amountToSubtract",
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

			result, _, err := tools.scaleClusterNodePoolPlan(context.Background(), &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name: "scaleClusterNodePoolPlan",
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
				// TestScaleNodePoolPlanToken).
				assert.JSONEq(t, test.expectedResult, planResponseWithoutConfirmation(t, text.Text))
			}
		})
	}
}

// TestScaleNodePoolPlanToken exercises the plan-token round trip: the token in
// the plan response must be accepted by the same gate for the exact patch the
// execute tool will send. The plan key keeps its existing shape; the
// confirmation block is additive.
func TestScaleNodePoolPlanToken(t *testing.T) {
	gate := fakeGates(t, nil)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
		newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
			{
				WorkerRole: true,
				Name:       "test-nodepool",
				Quantity:   ptr.To[int32](1),
			},
		}))
	c := &client.Client{
		ClientSetCreator: func(inConfig *rest.Config) (kubernetes.Interface, error) { return newFakeClientSet(), nil },
		DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "scaleClusterNodePoolPlan"}}

	params := scaleNodePoolParameters{Cluster: "test-cluster", Namespace: "fleet-default", NodePoolName: "test-nodepool", DesiredSize: 3}

	result, _, err := tools.scaleClusterNodePoolPlan(context.Background(), req, params)
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

	// The token binds the exact patch bytes the execute tool sends.
	patchBytes, err := tools.scaleClusterNodePoolPatch(context.Background(), req, params, zap.NewNop())
	require.NoError(t, err)

	err = gate.RequireToken(confirm.Operation{
		Tool: "scaleClusterNodePool", Cluster: "test-cluster", Namespace: "fleet-default",
		Kind: "nodepool", Name: "test-nodepool", Payload: patchBytes,
	}, parsed.Confirmation.Token)
	require.NoError(t, err, "plan token must be accepted by the same gate for the exact operation")

	// The token is single-use: a second validation of the same plan fails.
	err = gate.RequireToken(confirm.Operation{
		Tool: "scaleClusterNodePool", Cluster: "test-cluster", Namespace: "fleet-default",
		Kind: "nodepool", Name: "test-nodepool", Payload: patchBytes,
	}, parsed.Confirmation.Token)
	assert.ErrorIs(t, err, confirm.ErrTokenConsumed)
}
