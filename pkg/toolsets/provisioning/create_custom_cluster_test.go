package provisioning

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestCreateCustomCluster(t *testing.T) {
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
			gate := fakeGates(t, approveElicit)
			tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}
			req := &mcp.CallToolRequest{
				Params: &mcp.CallToolParamsRaw{
					Name: "createCustomCluster",
				},
			}

			params := test.params
			// Validation-only cases fail before the gate is reached; the rest
			// carry the single-use token minted by the plan tool for this exact
			// operation.
			if test.expectedError == "" {
				params.ConfirmationToken = issueCustomClusterToken(t, &tools, gate, req, params)
			}

			result, _, err := tools.createCustomCluster(context.Background(), req, params)

			if test.expectedError != "" {
				assert.ErrorContains(t, err, test.expectedError)
			} else {
				assert.NoError(t, err)

				text, ok := result.Content[0].(*mcp.TextContent)
				assert.Truef(t, ok, "expected type *mcp.TextContent")

				assert.Truef(t, ok, "expected expectedResult to be a JSON string")
				assert.JSONEq(t, createCustomClusterOutput(test.params, test.finalK8sVersion), text.Text)
			}

			svr.Close()
		})
	}
}

// TestCreateCustomClusterRequiresToken proves a create without a confirmation
// token is rejected by the token gate and never reaches the cluster.
func TestCreateCustomClusterRequiresToken(t *testing.T) {
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(createDummyKDMData("v1.32.4+rke2r1")))
	}))
	defer svr.Close()

	c, err := client.NewClient(true, svr.URL)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	dyn := dynamicfake.NewSimpleDynamicClient(provisioningSchemes())
	c.DynClientCreator = func(inConfig *rest.Config) (dynamic.Interface, error) { return dyn, nil }
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: fakeGates(t, approveElicit)}}

	_, _, err = tools.createCustomCluster(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "createCustomCluster"},
	}, createCustomClusterParams{
		Name:         "test",
		Distribution: "rke2",
		CNI:          "calico",
		Version:      "v1.32.4",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Zero(t, countCreatesFake(dyn), "no create must reach the cluster without a token")
}

// TestCreateCustomClusterTokenMismatchRejected proves a token minted for one
// cluster cannot be reused to create a different one.
func TestCreateCustomClusterTokenMismatchRejected(t *testing.T) {
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(createDummyKDMData("v1.32.4+rke2r1")))
	}))
	defer svr.Close()

	c, err := client.NewClient(true, svr.URL)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	dyn := dynamicfake.NewSimpleDynamicClient(provisioningSchemes())
	c.DynClientCreator = func(inConfig *rest.Config) (dynamic.Interface, error) { return dyn, nil }
	gate := fakeGates(t, approveElicit)
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "createCustomCluster"}}

	approved := createCustomClusterParams{Name: "test", Distribution: "rke2", CNI: "calico", Version: "v1.32.4"}
	token := issueCustomClusterToken(t, &tools, gate, req, approved)

	tampered := approved
	tampered.CNI = "cilium"
	tampered.ConfirmationToken = token

	_, _, err = tools.createCustomCluster(context.Background(), req, tampered)
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenMismatch)
	assert.Zero(t, countCreatesFake(dyn), "a mismatching cluster must not reach the cluster")
}

// TestCreateCustomClusterDeclined proves a declined elicitation yields the
// standard cancellation result and creates nothing.
func TestCreateCustomClusterDeclined(t *testing.T) {
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(createDummyKDMData("v1.32.4+rke2r1")))
	}))
	defer svr.Close()

	c, err := client.NewClient(true, svr.URL)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	dyn := dynamicfake.NewSimpleDynamicClient(provisioningSchemes())
	c.DynClientCreator = func(inConfig *rest.Config) (dynamic.Interface, error) { return dyn, nil }
	gate := fakeGates(t, declineElicit)
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: gate}}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "createCustomCluster"}}

	params := createCustomClusterParams{Name: "test", Distribution: "rke2", CNI: "calico", Version: "v1.32.4"}
	params.ConfirmationToken = issueCustomClusterToken(t, &tools, gate, req, params)

	result, _, err := tools.createCustomCluster(context.Background(), req, params)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "Operation cancelled by the user. Nothing was executed.", result.Content[0].(*mcp.TextContent).Text)
	assert.Zero(t, countCreatesFake(dyn), "declined create must not reach the cluster")
}

// TestCreateCustomClusterAutoWrite proves auto-write mode bypasses both the
// token and the user confirmation.
func TestCreateCustomClusterAutoWrite(t *testing.T) {
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(createDummyKDMData("v1.32.4+rke2r1")))
	}))
	defer svr.Close()

	c, err := client.NewClient(true, svr.URL)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	dyn := dynamicfake.NewSimpleDynamicClient(provisioningSchemes())
	c.DynClientCreator = func(inConfig *rest.Config) (dynamic.Interface, error) { return dyn, nil }
	// fakeGates(t, nil) makes elicitation a test failure if it is ever invoked.
	tools := Tools{client: c, cfg: toolconfig.Config{Gate: fakeGates(t, nil), AutoWrite: true}}

	result, _, err := tools.createCustomCluster(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "createCustomCluster"},
	}, createCustomClusterParams{
		Name:         "test",
		Distribution: "rke2",
		CNI:          "calico",
		Version:      "v1.32.4",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	assert.Equal(t, 1, countCreatesFake(dyn), "auto-write create must be executed")
}

func createCustomClusterOutput(params createCustomClusterParams, finalK8sVersion string) string {
	return fmt.Sprintf(`{
  "llm" : [ {
    "apiVersion" : "provisioning.cattle.io/v1",
    "kind" : "Cluster",
    "metadata" : {
      "annotations" : {
        "field.cattle.io/description" : "%s"
      },
      "name" : "%s",
      "namespace" : "fleet-default"
    },
    "spec" : {
      "kubernetesVersion" : "%s",
      "localClusterAuthEndpoint" : { },
      "rkeConfig" : {
        "chartValues" : null,
        "dataDirectories" : { },
        "etcd" : {
          "snapshotRetention" : 5,
          "snapshotScheduleCron" : "0 */5 * * *"
        },
        "machineGlobalConfig" : {
          "cni" : "%s"
        },
        "machinePoolDefaults" : { },
        "upgradeStrategy" : {
          "controlPlaneConcurrency" : "1",
          "controlPlaneDrainOptions" : {
            "deleteEmptyDirData" : true,
            "disableEviction" : false,
            "enabled" : false,
            "force" : false,
            "gracePeriod" : -1,
            "ignoreDaemonSets" : true,
            "skipWaitForDeleteTimeoutSeconds" : 0,
            "timeout" : 120
          },
          "workerConcurrency" : "1",
          "workerDrainOptions" : {
            "deleteEmptyDirData" : true,
            "disableEviction" : false,
            "enabled" : false,
            "force" : false,
            "gracePeriod" : -1,
            "ignoreDaemonSets" : true,
            "skipWaitForDeleteTimeoutSeconds" : 0,
            "timeout" : 120
          }
        }
      }
    },
    "status" : {
      "observedGeneration" : 0
    }
  } ],
  "uiContext" : [ {
    "namespace" : "fleet-default",
    "kind" : "Cluster",
    "cluster" : "local",
    "name" : "%s",
    "type" : "provisioning.cattle.io.cluster"
  } ]
}`, params.Description, params.Name, finalK8sVersion, params.CNI, params.Name)
}
