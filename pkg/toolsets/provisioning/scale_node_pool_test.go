package provisioning

import (
	"context"
	"testing"

	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
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

func TestScaleNodePool(t *testing.T) {
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
			expectedResult: `{
  "llm": [
    {
      "apiVersion": "provisioning.cattle.io/v1",
      "kind": "Cluster",
      "metadata": {
        "name": "test-cluster",
        "namespace": "fleet-default"
      },
      "spec": {
        "localClusterAuthEndpoint": {},
        "rkeConfig": {
          "chartValues": null,
          "dataDirectories": {},
          "machineGlobalConfig": null,
          "machinePoolDefaults": {},
          "machinePools": [
            {
              "controlPlaneRole": true,
              "etcdRole": true,
              "name": "test-nodepool",
              "quantity": 3,
              "workerRole": true
            }
          ],
          "upgradeStrategy": {
            "controlPlaneDrainOptions": {
              "deleteEmptyDirData": false,
              "disableEviction": false,
              "enabled": false,
              "force": false,
              "gracePeriod": 0,
              "skipWaitForDeleteTimeoutSeconds": 0,
              "timeout": 0
            },
            "workerDrainOptions": {
              "deleteEmptyDirData": false,
              "disableEviction": false,
              "enabled": false,
              "force": false,
              "gracePeriod": 0,
              "skipWaitForDeleteTimeoutSeconds": 0,
              "timeout": 0
            }
          }
        }
      },
      "status": {
        "clusterName": "c-m-abc123",
        "observedGeneration": 0,
        "ready": true
      }
    }
  ],
  "uiContext": [
    {
      "namespace": "fleet-default",
      "kind": "Cluster",
      "cluster": "test-cluster",
      "name": "test-cluster",
      "type": "provisioning.cattle.io.cluster"
    }
  ]
}`,
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
			expectedResult: `{
  "llm": [
    {
      "apiVersion": "provisioning.cattle.io/v1",
      "kind": "Cluster",
      "metadata": {
        "name": "test-cluster",
        "namespace": "fleet-default"
      },
      "spec": {
        "localClusterAuthEndpoint": {},
        "rkeConfig": {
          "chartValues": null,
          "dataDirectories": {},
          "machineGlobalConfig": null,
          "machinePoolDefaults": {},
          "machinePools": [
            {
              "controlPlaneRole": true,
              "etcdRole": true,
              "name": "test-nodepool-etcd",
              "quantity": 3,
              "workerRole": true
            },
            {
              "name": "test-nodepool",
              "quantity": 1,
              "workerRole": true
            }
          ],
          "upgradeStrategy": {
            "controlPlaneDrainOptions": {
              "deleteEmptyDirData": false,
              "disableEviction": false,
              "enabled": false,
              "force": false,
              "gracePeriod": 0,
              "skipWaitForDeleteTimeoutSeconds": 0,
              "timeout": 0
            },
            "workerDrainOptions": {
              "deleteEmptyDirData": false,
              "disableEviction": false,
              "enabled": false,
              "force": false,
              "gracePeriod": 0,
              "skipWaitForDeleteTimeoutSeconds": 0,
              "timeout": 0
            }
          }
        }
      },
      "status": {
        "clusterName": "c-m-abc123",
        "observedGeneration": 0,
        "ready": true
      }
    }
  ],
  "uiContext": [
    {
      "namespace": "fleet-default",
      "kind": "Cluster",
      "cluster": "test-cluster",
      "name": "test-cluster",
      "type": "provisioning.cattle.io.cluster"
    }
  ]
}`,
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
			expectedResult: `{
  "llm": [
    {
      "apiVersion": "provisioning.cattle.io/v1",
      "kind": "Cluster",
      "metadata": {
        "name": "test-cluster",
        "namespace": "fleet-default"
      },
      "spec": {
        "localClusterAuthEndpoint": {},
        "rkeConfig": {
          "chartValues": null,
          "dataDirectories": {},
          "machineGlobalConfig": null,
          "machinePoolDefaults": {},
          "machinePools": [
            {
              "name": "test-nodepool",
              "quantity": 2,
              "workerRole": true
            }
          ],
          "upgradeStrategy": {
            "controlPlaneDrainOptions": {
              "deleteEmptyDirData": false,
              "disableEviction": false,
              "enabled": false,
              "force": false,
              "gracePeriod": 0,
              "skipWaitForDeleteTimeoutSeconds": 0,
              "timeout": 0
            },
            "workerDrainOptions": {
              "deleteEmptyDirData": false,
              "disableEviction": false,
              "enabled": false,
              "force": false,
              "gracePeriod": 0,
              "skipWaitForDeleteTimeoutSeconds": 0,
              "timeout": 0
            }
          }
        }
      },
      "status": {
        "clusterName": "c-m-abc123",
        "observedGeneration": 0,
        "ready": true
      }
    }
  ],
  "uiContext": [
    {
      "namespace": "fleet-default",
      "kind": "Cluster",
      "cluster": "test-cluster",
      "name": "test-cluster",
      "type": "provisioning.cattle.io.cluster"
    }
  ]
}`,
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
			expectedResult: `{
  "llm": [
    {
      "apiVersion": "provisioning.cattle.io/v1",
      "kind": "Cluster",
      "metadata": {
        "name": "test-cluster",
        "namespace": "fleet-default"
      },
      "spec": {
        "localClusterAuthEndpoint": {},
        "rkeConfig": {
          "chartValues": null,
          "dataDirectories": {},
          "machineGlobalConfig": null,
          "machinePoolDefaults": {},
          "machinePools": [
            {
              "name": "test-nodepool",
              "quantity": 1,
              "workerRole": true
            }
          ],
          "upgradeStrategy": {
            "controlPlaneDrainOptions": {
              "deleteEmptyDirData": false,
              "disableEviction": false,
              "enabled": false,
              "force": false,
              "gracePeriod": 0,
              "skipWaitForDeleteTimeoutSeconds": 0,
              "timeout": 0
            },
            "workerDrainOptions": {
              "deleteEmptyDirData": false,
              "disableEviction": false,
              "enabled": false,
              "force": false,
              "gracePeriod": 0,
              "skipWaitForDeleteTimeoutSeconds": 0,
              "timeout": 0
            }
          }
        }
      },
      "status": {
        "clusterName": "c-m-abc123",
        "observedGeneration": 0,
        "ready": true
      }
    }
  ],
  "uiContext": [
    {
      "namespace": "fleet-default",
      "kind": "Cluster",
      "cluster": "test-cluster",
      "name": "test-cluster",
      "type": "provisioning.cattle.io.cluster"
    }
  ]
}`,
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
			gate := fakeGates(t, approveElicit)
			tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}
			req := &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name: "scaleClusterNodePool",
				},
			}

			params := test.params
			// The execute tool requires the single-use token minted by the plan
			// tool for exactly this operation. Validation-only cases fail before
			// the gate is reached, so they carry no token.
			if test.expectedError == "" {
				params.ConfirmationToken = issueScaleToken(t, &tools, gate, req, params)
			}

			result, _, err := tools.scaleClusterNodePool(context.Background(), req, params)

			if test.expectedError != "" {
				assert.ErrorContains(t, err, test.expectedError)
			} else {
				assert.NoError(t, err)

				text, ok := result.Content[0].(*mcp.TextContent)
				assert.Truef(t, ok, "expected type *mcp.TextContent")

				assert.Truef(t, ok, "expected expectedResult to be a JSON string")
				assert.JSONEq(t, test.expectedResult, text.Text)
			}
		})
	}
}

// TestScaleNodePoolRequiresToken proves a scale without a confirmation token is
// rejected by the token gate and never patches the cluster.
func TestScaleNodePoolRequiresToken(t *testing.T) {
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
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: fakeGates(t, approveElicit)}}

	_, _, err := tools.scaleClusterNodePool(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "scaleClusterNodePool"},
	}, scaleNodePoolParameters{
		Cluster:      "test-cluster",
		Namespace:    "fleet-default",
		NodePoolName: "test-nodepool",
		DesiredSize:  3,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Zero(t, countPatches(dyn), "no patch must reach the cluster without a token")
}

// TestScaleNodePoolTokenMismatchRejected proves a token minted for one patch
// cannot be reused for a different patch on the same node pool.
func TestScaleNodePoolTokenMismatchRejected(t *testing.T) {
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
	gate := fakeGates(t, approveElicit)
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "scaleClusterNodePool"}}

	// The user approved scaling to 3; the agent then sends a different patch.
	approved := scaleNodePoolParameters{Cluster: "test-cluster", Namespace: "fleet-default", NodePoolName: "test-nodepool", DesiredSize: 3}
	token := issueScaleToken(t, &tools, gate, req, approved)

	tampered := approved
	tampered.DesiredSize = 5
	tampered.ConfirmationToken = token

	_, _, err := tools.scaleClusterNodePool(context.Background(), req, tampered)
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenMismatch)
	assert.Zero(t, countPatches(dyn), "a mismatching patch must not reach the cluster")
}

// TestScaleNodePoolDeclined proves a declined elicitation yields the standard
// cancellation result and patches nothing.
func TestScaleNodePoolDeclined(t *testing.T) {
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
	gate := fakeGates(t, declineElicit)
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "scaleClusterNodePool"}}

	params := scaleNodePoolParameters{Cluster: "test-cluster", Namespace: "fleet-default", NodePoolName: "test-nodepool", DesiredSize: 3}
	params.ConfirmationToken = issueScaleToken(t, &tools, gate, req, params)

	result, _, err := tools.scaleClusterNodePool(context.Background(), req, params)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "Operation cancelled by the user. Nothing was executed.", result.Content[0].(*mcp.TextContent).Text)
	assert.Zero(t, countPatches(dyn), "declined scale must not reach the cluster")
}

// TestScaleNodePoolAutoWrite proves auto-write mode bypasses both the token and
// the user confirmation.
func TestScaleNodePoolAutoWrite(t *testing.T) {
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
	// fakeGates(t, nil) makes elicitation a test failure if it is ever invoked.
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: fakeGates(t, nil), AutoWrite: true}}

	result, _, err := tools.scaleClusterNodePool(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "scaleClusterNodePool"},
	}, scaleNodePoolParameters{
		Cluster:      "test-cluster",
		Namespace:    "fleet-default",
		NodePoolName: "test-nodepool",
		DesiredSize:  3,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	assert.Equal(t, 1, countPatches(dyn), "auto-write scale must be executed")
}
