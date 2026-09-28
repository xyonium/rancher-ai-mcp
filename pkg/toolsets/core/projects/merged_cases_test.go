package projects

import (
	"context"
	"iter"
	"maps"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// testCfg builds the config the merged-case tests need: a fake gate, so the
// plan/execute handlers a case table registers have a usable confirmation gate.
func testCfg(t *testing.T) toolconfig.Config {
	t.Helper()
	return toolconfig.Config{Gate: fakeGates(t, nil)}
}

func TestMergedCaseKeys(t *testing.T) {
	tools := NewTools(nil, testCfg(t))
	if got := maps.Keys(tools.QueryCases()); !equalKeys(got, "project", "projects", "resourceUsage") {
		t.Errorf("query cases = %v", got)
	}
	if got := maps.Keys(tools.PlanCases()); !equalKeys(got, "createProject") {
		t.Errorf("plan cases = %v", got)
	}
	if got := maps.Keys(tools.ExecuteCases()); !equalKeys(got, "createProject") {
		t.Errorf("execute cases = %v", got)
	}
	if !slices.Equal(tools.PlanCases()["createProject"].Required, []string{"cluster", "name"}) {
		t.Errorf("createProject required = %v", tools.PlanCases()["createProject"].Required)
	}
}

func equalKeys(got iter.Seq[string], want ...string) bool {
	var g []string
	for k := range got {
		g = append(g, k)
	}
	slices.Sort(g)
	slices.Sort(want)
	return slices.Equal(g, want)
}

// mergedCaseScheme serves the GVRs the projects query cases resolve:
// management clusters (for GetClusterID), projects, namespaces and PRTBs.
func mergedCaseScheme() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		{Group: "management.cattle.io", Version: "v3", Resource: "clusters"}:                    "ClusterList",
		{Group: "management.cattle.io", Version: "v3", Resource: "projects"}:                    "ProjectList",
		{Group: "management.cattle.io", Version: "v3", Resource: "projectroletemplatebindings"}: "ProjectRoleTemplateBindingList",
		{Group: "", Version: "v1", Resource: "namespaces"}:                                      "NamespaceList",
		{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}:                         "PodMetricsList",
	}
}

// mergedCaseRuntimeScheme registers the core types the fake dynamic client
// stores (the Namespace the project query cases list).
func mergedCaseRuntimeScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = metav1.AddMetaToScheme(scheme)
	_ = metricsv1beta1.AddToScheme(scheme)
	return scheme
}

// newQueryCaseTools builds a Tools whose fake dynamic client holds one cluster,
// one project and one namespace so the query cases resolve real objects.
func newQueryCaseTools(t *testing.T, cfg toolconfig.Config) *Tools {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(mergedCaseRuntimeScheme(), mergedCaseScheme(),
		fakeMgmtCluster("test-cluster"),
		fakeMgmtProject("test-cluster", "my-project", "My Project"),
		fakeProjectNamespace("ns-1", "my-project"),
	)
	tools := newProjectTestTools(t, cfg, dyn)
	return tools
}

// TestQueryCaseClosuresMapFlatParams proves the case closures take the flat
// merged query params through the typed handler params without transposing or
// dropping fields: "project" resolves the project named on the wire, and
// "projects" lists the projects of the cluster named on the wire.
func TestQueryCaseClosuresMapFlatParams(t *testing.T) {
	tools := newQueryCaseTools(t, testCfg(t))
	cases := tools.QueryCases()

	ctx := middleware.WithToken(context.Background(), "fakeToken")
	res, _, err := cases["project"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.QueryParams{
		Resource: "project", Cluster: "test-cluster", Name: "my-project",
	})
	require.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"name":"my-project"`)
	assert.Contains(t, text, `"namespace":"test-cluster"`)

	// A name that does not exist must fail on the mapped identity, not silently
	// return another project.
	_, _, err = cases["project"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.QueryParams{
		Resource: "project", Cluster: "test-cluster", Name: "other-project",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "other-project")

	res, _, err = cases["projects"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.QueryParams{
		Resource: "projects", Cluster: "test-cluster",
	})
	require.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, `"name":"my-project"`)

	// resourceUsage resolves the same cluster and reports the project totals:
	// the flat namespace filter lands in the typed params.
	res, _, err = cases["resourceUsage"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.QueryParams{
		Resource: "resourceUsage", Cluster: "test-cluster", Namespace: "ns-1",
	})
	require.NoError(t, err)
	usage := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, usage, `"namespace":"ns-1"`)
	assert.Contains(t, usage, `"podCount":0`)
}

// TestExecuteCaseClosurePassesConfirmationToken is the T3 security pin: the
// single-use token minted by the plan case must reach the execute handler
// verbatim, and a token minted for different params must be rejected by the
// gate before anything reaches the cluster.
func TestExecuteCaseClosurePassesConfirmationToken(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(mergedCaseRuntimeScheme(), mergedCaseScheme())
	gate := fakeGates(t, approveElicit)
	tools := newProjectTestTools(t, toolconfig.Config{Gate: gate}, dyn)
	ctx := middleware.WithToken(context.Background(), "fakeToken")

	plan, _, err := tools.PlanCases()["createProject"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "createProject", Cluster: "test-cluster", Name: "planned-project", DisplayName: "Planned",
	})
	require.NoError(t, err)
	planText := plan.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, planText, `"confirmationToken"`)

	// Mint the token the plan handler would have issued, proving the case
	// closure forwards it (and the rest of the flat params) into createProject.
	params := createProjectParams{
		Cluster: "test-cluster", Name: "planned-project", DisplayName: "Planned",
	}
	token := issueProjectToken(t, gate, params)
	_, _, err = tools.ExecuteCases()["createProject"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "createProject", Cluster: "test-cluster", Name: "planned-project", DisplayName: "Planned",
		ConfirmationToken: token,
	})
	require.NoError(t, err)
	require.Equal(t, 1, countProjectCreates(dyn))
	created := dyn.Actions()[len(dyn.Actions())-1].(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured)
	assert.Equal(t, "planned-project", created.GetName())
	assert.Equal(t, "Planned", mustNestedString(t, created, "spec", "displayName"))

	// A token minted for another project must not be replayable here.
	foreign := issueProjectToken(t, gate, createProjectParams{Cluster: "test-cluster", Name: "other-project"})
	_, _, err = tools.ExecuteCases()["createProject"].Handler(ctx, &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "createProject", Cluster: "test-cluster", Name: "planned-project", DisplayName: "Planned",
		ConfirmationToken: foreign,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenMismatch)
	assert.Equal(t, 1, countProjectCreates(dyn), "a foreign token must not reach the cluster")
}

// TestCreateProjectCaseAutoWrite pins the auto-write path of the merged case:
// the flat AutoWrite flag of the config is what the unchanged handler sees, so
// no token is needed and the project is created.
func TestCreateProjectCaseAutoWrite(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(mergedCaseRuntimeScheme(), mergedCaseScheme())
	tools := newProjectTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil), AutoWrite: true}, dyn)

	res, _, err := tools.ExecuteCases()["createProject"].Handler(
		middleware.WithToken(context.Background(), "fakeToken"), &mcp.CallToolRequest{},
		dispatch.ChangeParams{Operation: "createProject", Cluster: "test-cluster", Name: "auto-project"})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)
	assert.Equal(t, 1, countProjectCreates(dyn))
}

// mustNestedString returns a nested string field of an unstructured object,
// failing the test when it is absent.
func mustNestedString(t *testing.T, obj *unstructured.Unstructured, fields ...string) string {
	t.Helper()
	v, found, err := unstructured.NestedString(obj.Object, fields...)
	require.NoError(t, err)
	require.True(t, found, "field %v must be set", fields)
	return v
}
