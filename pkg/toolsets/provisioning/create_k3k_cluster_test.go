package provisioning

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

func TestCreateK3kCluster(t *testing.T) {
	scheme := runtime.NewScheme()

	tests := map[string]struct {
		params         createK3kClusterParams
		fakeDynClient  *dynamicfake.FakeDynamicClient
		expectedError  string
		expectedResult string
	}{
		"create cluster with minimum parameters (tests defaults)": {
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, k3kCustomListKinds(), newManagementCluster("downstream-1", true)),
			params: createK3kClusterParams{
				Name:          "min-cluster",
				Namespace:     "default",
				TargetCluster: "downstream-1",
			},
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "k3k.io/v1beta1",
						"kind": "Cluster",
						"metadata": {
							"name": "min-cluster",
							"namespace": "default"
						},
						"spec": {}
					}
				],
				"uiContext": [
					{
						"cluster": "downstream-1",
						"kind": "Cluster",
						"name": "min-cluster",
						"namespace": "default",
						"type": "cluster"
					}
				]
			}`,
		},
		"create cluster with advanced optional parameters": {
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, k3kCustomListKinds(), newManagementCluster("downstream-2", true)),
			params: createK3kClusterParams{
				Name:          "adv-cluster",
				Namespace:     "default",
				TargetCluster: "downstream-2",
				Version:       "v1.30.0-k3s1",
				Mode:          "virtual",
				Servers:       3,
				Agents:        3,
				Sync: SyncConfig{
					PriorityClasses: true,
					Ingresses:       true,
				},
				ServerLimit: ResourceLimits{
					CPU:    "2",
					Memory: "4Gi",
				},
				Persistence: PersistenceConfig{
					Type:             "dynamic",
					StorageClassName: "longhorn",
					StorageRequest:   "10Gi",
				},
			},
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "k3k.io/v1beta1",
						"kind": "Cluster",
						"metadata": {
							"name": "adv-cluster",
							"namespace": "default"
						},
						"spec": {
							"agents": 3,
							"mode": "virtual",
							"persistence": {
								"storageClassName": "longhorn",
								"storageRequest": "10Gi",
								"type": "dynamic"
							},
							"serverLimit": {
								"cpu": "2",
								"memory": "4Gi"
							},
							"servers": 3,
							"sync": {
								"ingresses": {
									"enabled": true
								},
								"priorityClasses": {
									"enabled": true
								}
							},
							"version": "v1.30.0-k3s1"
						}
					}
				],
				"uiContext": [
					{
						"cluster": "downstream-2",
						"kind": "Cluster",
						"name": "adv-cluster",
						"namespace": "default",
						"type": "cluster"
					}
				]
			}`,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			c := &client.Client{
				DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) {
					return test.fakeDynClient, nil
				},
			}
			gate := fakeGates(t, approveElicit)
			tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}

			params := test.params
			// The execute tool requires the single-use token minted by the plan
			// tool for exactly this operation.
			params.ConfirmationToken = issueK3kClusterToken(t, &tools, gate, params)

			result, _, err := tools.createK3kCluster(middleware.WithToken(t.Context(), testToken), &mcp.CallToolRequest{}, params)

			if test.expectedError != "" {
				assert.ErrorContains(t, err, test.expectedError)
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, result.Content)
				assert.JSONEq(t, test.expectedResult, result.Content[0].(*mcp.TextContent).Text)
			}
		})
	}
}

// TestCreateK3kClusterRequiresToken proves a create without a confirmation
// token is rejected by the token gate and never reaches the cluster.
func TestCreateK3kClusterRequiresToken(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), k3kCustomListKinds(), newManagementCluster("downstream-1", true))
	c := &client.Client{
		DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: fakeGates(t, approveElicit)}}

	_, _, err := tools.createK3kCluster(middleware.WithToken(t.Context(), testToken), &mcp.CallToolRequest{}, createK3kClusterParams{
		Name:          "min-cluster",
		Namespace:     "default",
		TargetCluster: "downstream-1",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Zero(t, countCreatesFake(dyn), "no create must reach the cluster without a token")
}

// TestCreateK3kClusterTokenMismatchRejected proves a token minted for one K3k
// cluster cannot be reused to create a different one.
func TestCreateK3kClusterTokenMismatchRejected(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), k3kCustomListKinds(), newManagementCluster("downstream-1", true))
	c := &client.Client{
		DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
	gate := fakeGates(t, approveElicit)
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}

	tampered := createK3kClusterParams{Name: "other-cluster", Namespace: "default", TargetCluster: "downstream-1"}
	tampered.ConfirmationToken = issueK3kClusterToken(t, &tools, gate, createK3kClusterParams{Name: "min-cluster", Namespace: "default", TargetCluster: "downstream-1"})

	_, _, err := tools.createK3kCluster(middleware.WithToken(t.Context(), testToken), &mcp.CallToolRequest{}, tampered)
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenMismatch)
	assert.Zero(t, countCreatesFake(dyn), "a mismatching cluster must not reach the cluster")
}

// TestCreateK3kClusterDeclined proves a declined elicitation yields the standard
// cancellation result and creates nothing.
func TestCreateK3kClusterDeclined(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), k3kCustomListKinds(), newManagementCluster("downstream-1", true))
	c := &client.Client{
		DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
	gate := fakeGates(t, declineElicit)
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}

	params := createK3kClusterParams{Name: "min-cluster", Namespace: "default", TargetCluster: "downstream-1"}
	params.ConfirmationToken = issueK3kClusterToken(t, &tools, gate, params)

	result, _, err := tools.createK3kCluster(middleware.WithToken(t.Context(), testToken), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "Operation cancelled by the user. Nothing was executed.", result.Content[0].(*mcp.TextContent).Text)
	assert.Zero(t, countCreatesFake(dyn), "declined create must not reach the cluster")
}

// TestCreateK3kClusterAutoWrite proves auto-write mode bypasses both the token
// and the user confirmation.
func TestCreateK3kClusterAutoWrite(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), k3kCustomListKinds(), newManagementCluster("downstream-1", true))
	c := &client.Client{
		DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
	// fakeGates(t, nil) makes elicitation a test failure if it is ever invoked.
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: fakeGates(t, nil), AutoWrite: true}}

	result, _, err := tools.createK3kCluster(middleware.WithToken(t.Context(), testToken), &mcp.CallToolRequest{}, createK3kClusterParams{
		Name:          "min-cluster",
		Namespace:     "default",
		TargetCluster: "downstream-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	assert.Equal(t, 1, countCreatesFake(dyn), "auto-write create must be executed")
}
