package client

import (
	"context"
	"errors"
	"testing"
	"time"

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

func newClientWithDiscovery(t *testing.T, resources []*metav1.APIResourceList) *Client {
	t.Helper()
	resetDiscoveryCache()
	cs := fake.NewClientset()
	fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	require.True(t, ok)
	fd.Resources = resources
	return &Client{
		ClientSetCreator: func(*rest.Config) (kubernetes.Interface, error) { return cs, nil },
	}
}

var harvesterResources = []*metav1.APIResourceList{
	{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "pods", Kind: "Pod", Namespaced: true},
		{Name: "pods/status", Kind: "Pod", Namespaced: true}, // subresource must be skipped
	}},
	{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
		{Name: "deployments", Kind: "Deployment", Namespaced: true},
	}},
	{GroupVersion: "harvesterhci.io/v1beta1", APIResources: []metav1.APIResource{
		{Name: "virtualmachines", Kind: "VirtualMachine", Namespaced: true},
	}},
	{GroupVersion: "kubevirt.io/v1", APIResources: []metav1.APIResource{
		{Name: "virtualmachines", Kind: "VirtualMachine", Namespaced: true},
	}},
}

func TestResolveFromHardcodedTable(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	gvr, err := c.ResolveGVR(context.Background(), "tok", "local", "Deployment", "")
	require.NoError(t, err)
	assert.Equal(t, "apps", gvr.Group)
	assert.Equal(t, "deployments", gvr.Resource)
}

func TestResolveWithAPIVersion(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	gvr, err := c.ResolveGVR(context.Background(), "tok", "local", "VirtualMachine", "harvesterhci.io/v1beta1")
	require.NoError(t, err)
	assert.Equal(t, "harvesterhci.io", gvr.Group)
	assert.Equal(t, "virtualmachines", gvr.Resource)
}

func TestResolveQualifiedKind(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	for _, kind := range []string{"harvesterhci.io/VirtualMachine", "VirtualMachine.harvesterhci.io"} {
		gvr, err := c.ResolveGVR(context.Background(), "tok", "local", kind, "")
		require.NoError(t, err, kind)
		assert.Equal(t, "harvesterhci.io", gvr.Group)
	}
}

func TestResolveAmbiguousKind(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	_, err := c.ResolveGVR(context.Background(), "tok", "local", "VirtualMachine", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harvesterhci.io")
	assert.Contains(t, err.Error(), "kubevirt.io")
}

func TestResolveUnknownKind(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	_, err := c.ResolveGVR(context.Background(), "tok", "local", "NoSuchThing", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown kind")
	assert.Contains(t, err.Error(), "listAPIResources")
}

func TestFetchAPIResourcesToleratesPartialFailure(t *testing.T) {
	// ServerPreferredResources returns partial results plus
	// ErrGroupDiscoveryFailed when some aggregated API groups are
	// unreachable; fetchAPIResources must keep the partial data. Pin the
	// behavior of the client-go helper the implementation relies on.
	partialErr := &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
		{Group: "broken.example.io", Version: "v1"}: errors.New("connection refused"),
	}}
	assert.True(t, discovery.IsGroupDiscoveryFailedError(partialErr))
	assert.False(t, discovery.IsGroupDiscoveryFailedError(errors.New("boom")))
}

func TestDiscoveryCacheTTL(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	// first call populates the cache
	_, err := c.ResolveGVR(context.Background(), "tok", "local", "VirtualMachine", "harvesterhci.io/v1beta1")
	require.NoError(t, err)
	// shrink the cached entry's expiry to the past; next resolution must refetch
	discoveryCache.Range(func(key, value any) bool {
		e := value.(discoveryEntry)
		e.expiry = time.Now().Add(-time.Second)
		discoveryCache.Store(key, e)
		return true
	})
	_, err = c.ResolveGVR(context.Background(), "tok", "local", "VirtualMachine", "harvesterhci.io/v1beta1")
	require.NoError(t, err)
}
