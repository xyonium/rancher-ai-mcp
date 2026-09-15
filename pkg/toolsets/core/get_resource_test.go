package core

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

var fakePod = &corev1.Pod{
	ObjectMeta: metav1.ObjectMeta{
		Name:      "rancher",
		Namespace: "default",
	},
	Spec: corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name:  "rancher-container",
				Image: "rancher:latest",
			},
		},
	},
}

func scheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	return scheme
}

// fakeVirtualMachine is a custom resource served by two API groups; it can only
// be fetched by name once the caller passes an explicit apiVersion.
var fakeVirtualMachine = &unstructured.Unstructured{
	Object: map[string]any{
		"apiVersion": "harvesterhci.io/v1beta1",
		"kind":       "VirtualMachine",
		"metadata": map[string]any{
			"name":      "vm-1",
			"namespace": "default",
		},
	},
}

func TestGetResourceAPIVersion(t *testing.T) {
	fakeToken := "fakeToken"

	tests := map[string]struct {
		params        resourceParams
		expectedError string
	}{
		"get custom resource with apiVersion": {
			params: resourceParams{Name: "vm-1", Kind: "VirtualMachine", Namespace: "default", Cluster: "local", APIVersion: "harvesterhci.io/v1beta1"},
		},
		"custom resource without apiVersion is ambiguous": {
			// VirtualMachine is served by both harvesterhci.io and kubevirt.io,
			// so discovery cannot pick one without the apiVersion hint.
			params:        resourceParams{Name: "vm-1", Kind: "VirtualMachine", Namespace: "default", Cluster: "local"},
			expectedError: "ambiguous",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := &client.Client{
				DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) {
					return dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), fakeVirtualMachine), nil
				},
				ClientSetCreator: fakeDiscoveryClientset(t, listAPIResourcesDiscovery),
			}

			tools := NewTools(test.WrapClient(c, fakeToken), toolconfig.Config{})
			result, _, err := tools.getResource(middleware.WithToken(t.Context(), fakeToken), test.NewCallToolRequest(""), tt.params)

			if tt.expectedError != "" {
				assert.ErrorContains(t, err, tt.expectedError)
				return
			}
			require.NoError(t, err)

			var payload struct {
				LLM []map[string]any `json:"llm"`
			}
			require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &payload))
			require.Len(t, payload.LLM, 1)
			assert.Equal(t, "harvesterhci.io/v1beta1", payload.LLM[0]["apiVersion"])
			assert.Equal(t, "VirtualMachine", payload.LLM[0]["kind"])
		})
	}
}
func TestGetResource(t *testing.T) {
	fakeUrl := "https://localhost:8080"
	fakeToken := "fakeToken"

	tests := map[string]struct {
		params        resourceParams
		fakeDynClient *dynamicfake.FakeDynamicClient
		// used in the CallToolRequest
		requestURL string
		// used in the creation of the Tools.
		rancherURL     string
		expectedResult string
		expectedError  string
	}{
		"get pod": {
			params:         resourceParams{Name: "rancher", Kind: "pod", Namespace: "default", Cluster: "local"},
			fakeDynClient:  dynamicfake.NewSimpleDynamicClient(scheme(), fakePod),
			requestURL:     fakeUrl,
			expectedResult: `{"llm":[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"rancher","namespace":"default"},"spec":{"containers":[{"image":"rancher:latest","name":"rancher-container","resources":{}}]},"status":{}}],"uiContext":[{"namespace":"default","kind":"Pod","cluster":"local","name":"rancher","type":"pod"}]}`,
		},
		"get pod when tool is configured with URL": {
			params:         resourceParams{Name: "rancher", Kind: "pod", Namespace: "default", Cluster: "local"},
			fakeDynClient:  dynamicfake.NewSimpleDynamicClient(scheme(), fakePod),
			rancherURL:     fakeUrl,
			expectedResult: `{"llm":[{"apiVersion":"v1","kind":"Pod","metadata":{"name":"rancher","namespace":"default"},"spec":{"containers":[{"image":"rancher:latest","name":"rancher-container","resources":{}}]},"status":{}}],"uiContext":[{"namespace":"default","kind":"Pod","cluster":"local","name":"rancher","type":"pod"}]}`,
		},
		"get pod - not found": {
			params:        resourceParams{Name: "rancher", Kind: "pod", Namespace: "default", Cluster: "local"},
			fakeDynClient: dynamicfake.NewSimpleDynamicClient(scheme()),
			requestURL:    fakeUrl,
			expectedError: `pods "rancher" not found`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := &client.Client{
				DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) {
					return tt.fakeDynClient, nil
				},
			}

			tools := NewTools(test.WrapClient(c, fakeToken), toolconfig.Config{})
			req := test.NewCallToolRequest(tt.requestURL)

			result, _, err := tools.getResource(middleware.WithToken(t.Context(), fakeToken), req, tt.params)
			if tt.expectedError != "" {
				assert.ErrorContains(t, err, tt.expectedError)
			} else {
				require.NoError(t, err)
				assert.JSONEq(t, tt.expectedResult, result.Content[0].(*mcp.TextContent).Text)
			}
		})
	}
}
