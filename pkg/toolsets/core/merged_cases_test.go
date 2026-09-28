package core

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

func testCfg() toolconfig.Config { return toolconfig.Config{ReadOnly: true} }

// coreQueryCases is the full key set of core's query table: core's own two
// cases plus the projects and rbac sub-toolsets core unions in for the merged
// rancherQuery tool.
var coreQueryCases = []string{
	"clusters", "clusterImages",
	"project", "projects", "resourceUsage",
	"user", "roleTemplate", "roleTemplates", "clusterRTBs", "projectRTBs",
}

func TestQueryCasesKeys(t *testing.T) {
	tools := NewTools(nil, testCfg())
	cases := tools.QueryCases()
	for _, want := range coreQueryCases {
		if _, ok := cases[want]; !ok {
			t.Errorf("missing query case %q", want)
		}
	}
	if len(cases) != len(coreQueryCases) {
		t.Errorf("core owns exactly %d query cases, got %d", len(coreQueryCases), len(cases))
	}
}

// TestQueryCaseClosuresMapFlatParams proves the case closures are real: they
// must take the flat merged params through Validate and land them in the typed
// handler params of core's own handlers. The fake client records the list
// parameters, so a transposed or dropped field is visible.
func TestQueryCaseClosuresMapFlatParams(t *testing.T) {
	clusters := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "management.cattle.io/v3", "kind": "Cluster",
		"metadata": map[string]any{"name": "local"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(clusterListScheme(), map[schema.GroupVersionResource]string{
		{Group: "management.cattle.io", Version: "v3", Resource: "clusters"}: "ClusterList",
	}, clusters)
	tools := NewTools(newFakeToolsClient(&client.Client{
		DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}, "fakeToken"), toolconfig.Config{})

	chaining := tools.QueryCases()["clusters"]
	res, _, err := chaining.Handler(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.QueryParams{Resource: "clusters"})
	require.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, `"name":"local"`)
	assert.Zero(t, len(chaining.Required), "clusters needs no parameters")

	// clusterImages maps the flat clusters list into the typed params; the
	// handler resolves them and lists pods per cluster.
	fakePodWithImageDyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(podScheme(), map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "pods"}: "PodList",
	}, fakePodWithImage)
	imageTools := NewTools(newFakeToolsClient(&client.Client{
		DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return fakePodWithImageDyn, nil },
	}, "fakeToken"), toolconfig.Config{})
	res, _, err = imageTools.QueryCases()["clusterImages"].Handler(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.QueryParams{
		Resource: "clusterImages", Clusters: []string{"local"},
	})
	require.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"nginx:1.21"`)
	assert.Contains(t, text, `"test-pod"`)
}

// TestDiagnoseCaseClosuresMapFlatParams proves the diagnose case closures wire
// the flat params into the typed handler params: the deployment case resolves
// exactly the deployment named on the wire and returns its pods, and the
// required sets guard the two identity cases.
func TestDiagnoseCaseClosuresMapFlatParams(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(deploymentScheme(), map[schema.GroupVersionResource]string{
		{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
		{Group: "", Version: "v1", Resource: "pods"}:            "PodList",
	}, fakeDeployment, fakeDeploymentPod)
	tools := NewTools(newFakeToolsClient(&client.Client{
		DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}, "fakeToken"), toolconfig.Config{})

	cases := tools.DiagnoseCases()
	for _, k := range []string{"deployment", "pod"} {
		assert.Equal(t, []string{"cluster", "namespace", "name"}, cases[k].Required, "%s required fields", k)
	}

	res, _, err := cases["deployment"].Handler(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.DiagnoseParams{
		Target: "deployment", Cluster: "local", Namespace: "default", Name: "nginx-deployment",
	})
	require.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"name":"nginx-deployment"`)
	assert.Contains(t, text, `"name":"nginx-deployment-abc123"`, "the deployment case must pass the name through to the pod list")

	// A deployment name that does not exist must fail on the mapped identity,
	// not silently list everything.
	_, _, err = cases["deployment"].Handler(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, dispatch.DiagnoseParams{
		Target: "deployment", Cluster: "local", Namespace: "default", Name: "missing",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"missing"`)
}

func TestDiagnoseCasesKeys(t *testing.T) {
	tools := NewTools(nil, testCfg())
	cases := tools.DiagnoseCases()
	for _, want := range []string{"nodes", "deployment", "pod"} {
		if _, ok := cases[want]; !ok {
			t.Errorf("missing diagnose case %q", want)
		}
	}
	if len(cases) != 3 {
		t.Errorf("core owns exactly 3 diagnose cases, got %d", len(cases))
	}
	req := cases["deployment"].Required
	if len(req) != 3 || req[0] != "cluster" || req[1] != "namespace" || req[2] != "name" {
		t.Errorf("deployment required = %v, want [cluster namespace name]", req)
	}
}

// clusterListScheme is the scheme for the cluster-list fake dynamic client.
func clusterListScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = metav1.AddMetaToScheme(scheme)
	return scheme
}
