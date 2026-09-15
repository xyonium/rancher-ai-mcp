package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// listAPIResourcesDiscovery mirrors the API resources a Harvester-enabled
// cluster serves, including subresources (which discovery strips before the
// tool sees them; the tool skips them again defensively).
var listAPIResourcesDiscovery = []*metav1.APIResourceList{
	{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "pods", Kind: "Pod", Namespaced: true},
		{Name: "pods/status", Kind: "Pod", Namespaced: true},
	}},
	{GroupVersion: "harvesterhci.io/v1beta1", APIResources: []metav1.APIResource{
		{Name: "virtualmachines", Kind: "VirtualMachine", Namespaced: true},
		{Name: "virtualmachines/status", Kind: "VirtualMachine", Namespaced: true},
	}},
	{GroupVersion: "kubevirt.io/v1", APIResources: []metav1.APIResource{
		{Name: "virtualmachines", Kind: "VirtualMachine", Namespaced: true},
	}},
}

// stubAPIResourcesClient overrides ListAPIResources only, so the tool can be
// fed raw discovery output that still contains subresources.
type stubAPIResourcesClient struct {
	toolsClient
	lists []*metav1.APIResourceList
}

func (s *stubAPIResourcesClient) ListAPIResources(context.Context, string, string) ([]*metav1.APIResourceList, error) {
	return s.lists, nil
}

// TestListAPIResourcesSkipsSubresources verifies the tool never reports
// subresources, even when the discovery payload contains them.
func TestListAPIResourcesSkipsSubresources(t *testing.T) {
	tools := &Tools{client: &stubAPIResourcesClient{lists: listAPIResourcesDiscovery}}

	res, _, err := tools.listAPIResources(middleware.WithToken(t.Context(), "tok"), &mcp.CallToolRequest{}, listAPIResourcesParams{Cluster: "local"})
	require.NoError(t, err)

	var payload struct {
		LLM []map[string]any `json:"llm"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &payload))
	require.Len(t, payload.LLM, 3)
	assert.Equal(t, "pods", payload.LLM[0]["resource"])
	assert.Equal(t, "virtualmachines", payload.LLM[1]["resource"])
	assert.Equal(t, "virtualmachines", payload.LLM[2]["resource"])
}

// fakeDiscoveryClientset returns a ClientSetCreator serving the given discovery
// resources. Discovery is resolved through the package-level
// discovery.ServerPreferredResources helper, which honors the fake's Resources
// field, unlike the fake's ServerPreferredResources method.
func fakeDiscoveryClientset(t *testing.T, resources []*metav1.APIResourceList) func(*rest.Config) (kubernetes.Interface, error) {
	t.Helper()
	cs := fake.NewClientset()
	fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	require.True(t, ok)
	fd.Resources = resources

	return func(*rest.Config) (kubernetes.Interface, error) { return cs, nil }
}

// newToolsWithDiscovery wraps a *client.Client whose clientset serves the given
// discovery resources.
func newToolsWithDiscovery(t *testing.T, resources []*metav1.APIResourceList) *Tools {
	t.Helper()
	c := &client.Client{
		ClientSetCreator: fakeDiscoveryClientset(t, resources),
	}
	return NewTools(test.WrapClient(c, "fakeToken"), toolconfig.Config{})
}

func TestListAPIResources(t *testing.T) {
	fakeToken := "fakeToken"
	tools := newToolsWithDiscovery(t, listAPIResourcesDiscovery)

	tests := map[string]struct {
		params   listAPIResourcesParams
		expected []map[string]any
	}{
		"all resources sorted by group, kind, version": {
			params: listAPIResourcesParams{Cluster: "local"},
			expected: []map[string]any{
				{"group": "", "version": "v1", "kind": "Pod", "resource": "pods", "namespaced": true},
				{"group": "harvesterhci.io", "version": "v1beta1", "kind": "VirtualMachine", "resource": "virtualmachines", "namespaced": true},
				{"group": "kubevirt.io", "version": "v1", "kind": "VirtualMachine", "resource": "virtualmachines", "namespaced": true},
			},
		},
		"filter by group": {
			params: listAPIResourcesParams{Cluster: "local", Group: "harvesterhci.io"},
			expected: []map[string]any{
				{"group": "harvesterhci.io", "version": "v1beta1", "kind": "VirtualMachine", "resource": "virtualmachines", "namespaced": true},
			},
		},
		"filter by kind case-insensitively": {
			params: listAPIResourcesParams{Cluster: "local", Kind: "virtualmachine"},
			expected: []map[string]any{
				{"group": "harvesterhci.io", "version": "v1beta1", "kind": "VirtualMachine", "resource": "virtualmachines", "namespaced": true},
				{"group": "kubevirt.io", "version": "v1", "kind": "VirtualMachine", "resource": "virtualmachines", "namespaced": true},
			},
		},
		"filter by group and kind": {
			params: listAPIResourcesParams{Cluster: "local", Group: "kubevirt.io", Kind: "VirtualMachine"},
			expected: []map[string]any{
				{"group": "kubevirt.io", "version": "v1", "kind": "VirtualMachine", "resource": "virtualmachines", "namespaced": true},
			},
		},
		"no match": {
			params:   listAPIResourcesParams{Cluster: "local", Group: "no.such.group"},
			expected: []map[string]any{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			res, _, err := tools.listAPIResources(middleware.WithToken(t.Context(), fakeToken), &mcp.CallToolRequest{}, tt.params)
			require.NoError(t, err)

			var payload struct {
				LLM []map[string]any `json:"llm"`
			}
			require.NoError(t, json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &payload))
			assert.Equal(t, tt.expected, payload.LLM)
		})
	}
}

func TestListAPIResourcesInvalidToken(t *testing.T) {
	tools := newToolsWithDiscovery(t, listAPIResourcesDiscovery)

	_, _, err := tools.listAPIResources(middleware.WithToken(t.Context(), "wrongToken"), &mcp.CallToolRequest{}, listAPIResourcesParams{Cluster: "local"})
	assert.ErrorContains(t, err, "invalid token")
}

// TestResolveGVRDelegationThroughFakeToolsClient guards the fake used by the
// core toolset tests: it must delegate ResolveGVR and ListAPIResources to the
// wrapped client, following exactly the same resolution rules.
func TestResolveGVRDelegationThroughFakeToolsClient(t *testing.T) {
	fakeToken := "fakeToken"
	c := &client.Client{
		ClientSetCreator: fakeDiscoveryClientset(t, listAPIResourcesDiscovery),
	}
	fakeClient := newFakeToolsClient(c, fakeToken)

	gvr, err := fakeClient.ResolveGVR(t.Context(), fakeToken, "local", "VirtualMachine", "harvesterhci.io/v1beta1")
	require.NoError(t, err)
	assert.Equal(t, schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachines"}, gvr)

	lists, err := fakeClient.ListAPIResources(t.Context(), fakeToken, "local")
	require.NoError(t, err)
	assert.NotEmpty(t, lists)

	_, err = fakeClient.ResolveGVR(t.Context(), "wrongToken", "local", "VirtualMachine", "")
	assert.ErrorContains(t, err, "invalid token")
	_, err = fakeClient.ListAPIResources(t.Context(), "wrongToken", "local")
	assert.ErrorContains(t, err, "invalid token")

	// The fake must be usable as the toolsClient interface.
	var _ toolsClient = fakeClient
}

// TestDiscoveryHelperHonorsFakeResources documents why the client uses the
// package-level discovery helper: the fake's ServerPreferredResources method is
// stubbed out and ignores the Resources field. Unlike the method, the helper
// also drops subresources (k8s.io/client-go discovery.ServerPreferredResources).
//
// The fake builds its group list by ranging over a map, so the order of the
// returned lists is nondeterministic across processes. Consequently the
// assertions below are on membership, never on position.
func TestDiscoveryHelperHonorsFakeResources(t *testing.T) {
	tools := newToolsWithDiscovery(t, listAPIResourcesDiscovery)

	lists, err := tools.client.ListAPIResources(t.Context(), "fakeToken", "local")
	require.NoError(t, err)
	require.Len(t, lists, 3)

	byGroupVersion := make(map[string]*metav1.APIResourceList, len(lists))
	for _, list := range lists {
		byGroupVersion[list.GroupVersion] = list
	}
	require.Len(t, byGroupVersion, 3, "each group version must be served exactly once")

	// Subresources (pods/status, virtualmachines/status) are dropped by the helper.
	pods, ok := byGroupVersion["v1"]
	require.True(t, ok, "v1 must be served")
	require.Len(t, pods.APIResources, 1)
	assert.Equal(t, "pods", pods.APIResources[0].Name)

	harvester, ok := byGroupVersion["harvesterhci.io/v1beta1"]
	require.True(t, ok, "harvesterhci.io/v1beta1 must be served")
	require.Len(t, harvester.APIResources, 1)
	assert.Equal(t, "virtualmachines", harvester.APIResources[0].Name)

	kubevirt, ok := byGroupVersion["kubevirt.io/v1"]
	require.True(t, ok, "kubevirt.io/v1 must be served")
	require.Len(t, kubevirt.APIResources, 1)
	assert.Equal(t, "virtualmachines", kubevirt.APIResources[0].Name)

	var discoveryIface discovery.DiscoveryInterface = fake.NewClientset().Discovery()
	preferred, err := discoveryIface.ServerPreferredResources()
	require.NoError(t, err)
	assert.Empty(t, preferred, "the fake's method stub ignores the Resources field")
}
