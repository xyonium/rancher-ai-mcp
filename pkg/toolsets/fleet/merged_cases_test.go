package fleet

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

func TestMergedCaseKeys(t *testing.T) {
	tools := NewTools(nil)
	q := tools.QueryCases()
	for _, k := range []string{"gitRepo", "gitRepos", "bundle"} {
		if _, ok := q[k]; !ok {
			t.Errorf("missing query case %q", k)
		}
	}
	if got := q["gitRepo"].Required; len(got) != 2 || got[0] != "workspace" || got[1] != "name" {
		t.Errorf("gitRepo required = %v, want [workspace name]", got)
	}
	d := tools.DiagnoseCases()
	if len(d) != 1 {
		t.Fatalf("fleet owns exactly 1 diagnose case, got %d", len(d))
	}
	if got := d["fleet"].Required; len(got) != 1 || got[0] != "workspace" {
		t.Errorf("fleet required = %v, want [workspace]", got)
	}
}

// newFleetQueryCaseTools builds Tools over a fake dynamic client holding one
// GitRepo and one Bundle in the fleet-default workspace.
func newFleetQueryCaseTools(t *testing.T) *Tools {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(listGitReposScheme(), map[schema.GroupVersionResource]string{
		{Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "gitrepos"}: "GitRepoList",
		{Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "bundles"}:  "BundleList",
	}, fakeGitRepo1, fakeBundle1)
	c := &client.Client{DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil }}
	return NewTools(test.WrapClient(c, "fakeToken"))
}

// TestQueryCaseClosuresMapFlatParams pins the T5 field mapping of the fleet
// query cases: the flat workspace and name parameters must reach the typed
// handler params, so each case resolves exactly the object named on the wire.
func TestQueryCaseClosuresMapFlatParams(t *testing.T) {
	tools := newFleetQueryCaseTools(t)
	ctx := middleware.WithToken(context.Background(), "fakeToken")
	cases := tools.QueryCases()

	res, _, err := cases["gitRepo"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.QueryParams{
		Resource: "gitRepo", Workspace: "fleet-default", Name: "gitrepo-1",
	})
	require.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"name":"gitrepo-1"`)
	assert.Contains(t, text, `"namespace":"fleet-default"`)

	// A different name must not return the same object: the mapping is load
	// bearing, not a wildcard.
	_, _, err = cases["gitRepo"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.QueryParams{
		Resource: "gitRepo", Workspace: "fleet-default", Name: "missing",
	})
	require.Error(t, err)

	res, _, err = cases["gitRepos"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.QueryParams{
		Resource: "gitRepos", Workspace: "fleet-default",
	})
	require.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, `"name":"gitrepo-1"`)

	// A workspace with no GitRepos must come back empty, proving the workspace
	// parameter is what scopes the list.
	res, _, err = cases["gitRepos"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.QueryParams{
		Resource: "gitRepos", Workspace: "other-workspace",
	})
	require.NoError(t, err)
	assert.NotContains(t, res.Content[0].(*mcp.TextContent).Text, `"name":"gitrepo-1"`)

	res, _, err = cases["bundle"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.QueryParams{
		Resource: "bundle", Workspace: "fleet-default", Name: "bundle-1",
	})
	require.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, `"name":"bundle-1"`)
}

// TestDiagnoseCaseClosureMapsWorkspace pins the fleet diagnose case: the flat
// workspace parameter must reach the analyzer, which receives it as its scope.
func TestDiagnoseCaseClosureMapsWorkspace(t *testing.T) {
	var gotWorkspace string
	tools := &Tools{
		client: &fakeFleetClient{},
		resourceAnalyzer: &recordingAnalyzer{
			onAnalyze: func(namespace string) (string, error) {
				gotWorkspace = namespace
				return "fleet is healthy", nil
			},
		},
	}

	res, _, err := tools.DiagnoseCases()["fleet"].Handler(
		middleware.WithToken(context.Background(), "fakeToken"), &mcp.CallToolRequest{},
		dispatch.DiagnoseParams{Target: "fleet", Workspace: "fleet-default"})
	require.NoError(t, err)
	assert.Equal(t, "fleet-default", gotWorkspace, "the flat workspace must reach the analyzer")
	assert.Equal(t, "fleet is healthy", res.Content[0].(*mcp.TextContent).Text)
}

// recordingAnalyzer is a resourceAnalyzer that reports the namespace it was
// called with.
type recordingAnalyzer struct {
	onAnalyze func(namespace string) (string, error)
}

func (r *recordingAnalyzer) analyzeFleetResources(_ context.Context, _ *rest.Config, namespace string) (string, error) {
	return r.onAnalyze(namespace)
}
