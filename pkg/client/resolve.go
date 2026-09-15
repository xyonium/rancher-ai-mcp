package client

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rancher/rancher-ai-mcp/pkg/converter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

const discoveryCacheTTL = 5 * time.Minute

var errKindNotFound = errors.New("kind not found via discovery")

type discoveryEntry struct {
	lists  []*metav1.APIResourceList
	expiry time.Time
}

// discoveryCache maps clusterID -> discovered API resources. The data is
// cluster-level public API metadata; actual resource calls are still
// authorized per-request by Rancher RBAC via the caller's token.
var discoveryCache sync.Map

// resetDiscoveryCache clears the discovery cache. Used by tests.
func resetDiscoveryCache() {
	discoveryCache = sync.Map{}
}

// ListAPIResources returns all preferred API resources of the cluster
// (group/version/kind/resource), using a short-lived per-cluster cache.
func (c *Client) ListAPIResources(ctx context.Context, token, cluster string) ([]*metav1.APIResourceList, error) {
	return c.apiResources(ctx, token, cluster, false)
}

func (c *Client) apiResources(ctx context.Context, token, cluster string, forceRefresh bool) ([]*metav1.APIResourceList, error) {
	clusterID, err := c.GetClusterID(ctx, token, cluster)
	if err != nil {
		return nil, err
	}
	if !forceRefresh {
		if entry, ok := discoveryCache.Load(clusterID); ok {
			e := entry.(discoveryEntry)
			if time.Now().Before(e.expiry) {
				return e.lists, nil
			}
		}
	}
	lists, err := c.fetchAPIResources(ctx, token, clusterID)
	if err != nil {
		return nil, err
	}
	discoveryCache.Store(clusterID, discoveryEntry{lists: lists, expiry: time.Now().Add(discoveryCacheTTL)})
	return lists, nil
}

func (c *Client) fetchAPIResources(ctx context.Context, token, clusterID string) ([]*metav1.APIResourceList, error) {
	clientset, err := c.CreateClientSet(ctx, token, clusterID)
	if err != nil {
		return nil, err
	}
	// The package-level helper honors fake discovery's Resources field, unlike
	// the equivalent method on the discovery interface which the fake stubs out.
	lists, err := discovery.ServerPreferredResources(clientset.Discovery())
	if err != nil {
		// Some aggregated APIs may be unreachable; partial results are usable.
		if discovery.IsGroupDiscoveryFailedError(err) && len(lists) > 0 {
			return lists, nil
		}
		return nil, err
	}
	return lists, nil
}

// ResolveGVR resolves a kind (optionally disambiguated by apiVersion or a
// group-qualified kind) to a GVR. Resolution order:
//  1. explicit apiVersion (e.g. "harvesterhci.io/v1beta1")
//  2. the built-in kind table (backwards compatibility, incl. prefixed kinds)
//  3. group-qualified kind: "group/Kind" or "Kind.group"
//  4. full discovery scan (single match required; ambiguity is an error)
func (c *Client) ResolveGVR(ctx context.Context, token, cluster, kind, apiVersion string) (schema.GroupVersionResource, error) {
	gvr, err := c.resolveGVR(ctx, token, cluster, kind, apiVersion, false)
	if err != nil && errors.Is(err, errKindNotFound) {
		// The cache may be stale (CRD installed within the TTL); bust it once.
		gvr, err = c.resolveGVR(ctx, token, cluster, kind, apiVersion, true)
	}
	return gvr, err
}

func (c *Client) resolveGVR(ctx context.Context, token, cluster, kind, apiVersion string, forceRefresh bool) (schema.GroupVersionResource, error) {
	if strings.TrimSpace(kind) == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("kind must not be empty")
	}

	if apiVersion != "" {
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return schema.GroupVersionResource{}, fmt.Errorf("invalid apiVersion %q: %w", apiVersion, err)
		}
		return c.resolveInGroupVersion(ctx, token, cluster, kind, gv, forceRefresh)
	}

	if gvr, ok := converter.K8sKindsToGVRs[strings.ToLower(kind)]; ok {
		return gvr, nil
	}

	if group, k, ok := splitQualifiedKind(kind); ok {
		return c.resolveInGroup(ctx, token, cluster, group, k, forceRefresh)
	}

	return c.resolveByDiscovery(ctx, token, cluster, kind, forceRefresh)
}

// splitQualifiedKind accepts "group/Kind" or "Kind.group".
func splitQualifiedKind(kind string) (group, k string, ok bool) {
	if i := strings.Index(kind, "/"); i > 0 && i < len(kind)-1 {
		return kind[:i], kind[i+1:], true
	}
	if i := strings.Index(kind, "."); i > 0 && i < len(kind)-1 {
		return kind[i+1:], kind[:i], true
	}
	return "", "", false
}

func isSubresource(ar metav1.APIResource) bool {
	return strings.Contains(ar.Name, "/")
}

func (c *Client) resolveInGroupVersion(ctx context.Context, token, cluster, kind string, gv schema.GroupVersion, forceRefresh bool) (schema.GroupVersionResource, error) {
	lists, err := c.apiResources(ctx, token, cluster, forceRefresh)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	var available []string
	for _, list := range lists {
		if list.GroupVersion != gv.String() {
			continue
		}
		for _, ar := range list.APIResources {
			if isSubresource(ar) {
				continue
			}
			available = append(available, ar.Kind)
			if strings.EqualFold(ar.Kind, kind) {
				return schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: ar.Name}, nil
			}
		}
	}
	if len(available) == 0 {
		return schema.GroupVersionResource{}, fmt.Errorf("apiVersion %q not served by cluster %s: %w", gv.String(), cluster, errKindNotFound)
	}
	sort.Strings(available)
	return schema.GroupVersionResource{}, fmt.Errorf("%w: kind %q not found in %s; available kinds: %s", errKindNotFound, kind, gv.String(), strings.Join(available, ", "))
}

func (c *Client) resolveInGroup(ctx context.Context, token, cluster, group, kind string, forceRefresh bool) (schema.GroupVersionResource, error) {
	lists, err := c.apiResources(ctx, token, cluster, forceRefresh)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	for _, list := range lists { // ServerPreferredResources is preference-ordered
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil || gv.Group != group {
			continue
		}
		for _, ar := range list.APIResources {
			if !isSubresource(ar) && strings.EqualFold(ar.Kind, kind) {
				return schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: ar.Name}, nil
			}
		}
	}
	return schema.GroupVersionResource{}, fmt.Errorf("%w: kind %q not found in group %q of cluster %s", errKindNotFound, kind, group, cluster)
}

func (c *Client) resolveByDiscovery(ctx context.Context, token, cluster, kind string, forceRefresh bool) (schema.GroupVersionResource, error) {
	lists, err := c.apiResources(ctx, token, cluster, forceRefresh)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	var matches []schema.GroupVersionResource
	seenGroups := map[string]bool{}
	for _, list := range lists {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil || seenGroups[gv.Group] {
			continue
		}
		for _, ar := range list.APIResources {
			if isSubresource(ar) || !strings.EqualFold(ar.Kind, kind) {
				continue
			}
			matches = append(matches, schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: ar.Name})
			seenGroups[gv.Group] = true // first (preferred) version per group wins
			break
		}
	}
	switch len(matches) {
	case 0:
		return schema.GroupVersionResource{}, fmt.Errorf("%w: unknown kind %q in cluster %s; call the listAPIResources tool to discover available resource types", errKindNotFound, kind, cluster)
	case 1:
		return matches[0], nil
	default:
		var candidates []string
		for _, m := range matches {
			candidates = append(candidates, m.String())
		}
		sort.Strings(candidates)
		return schema.GroupVersionResource{}, fmt.Errorf("kind %q is ambiguous in cluster %s, found in: %s; retry with an explicit apiVersion (e.g. %s/%s) or a group-qualified kind (e.g. %s/%s)", kind, cluster, strings.Join(candidates, ", "), matches[0].Group, matches[0].Version, matches[0].Group, kind)
	}
}
