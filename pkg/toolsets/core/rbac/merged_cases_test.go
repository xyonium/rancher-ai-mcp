package rbac

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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

func TestQueryCasesKeys(t *testing.T) {
	tools := NewTools(nil, true)
	cases := tools.QueryCases()
	want := []string{"user", "roleTemplate", "roleTemplates", "clusterRTBs", "projectRTBs"}
	if len(cases) != len(want) {
		t.Fatalf("rbac owns %d query cases, got %d", len(want), len(cases))
	}
	for _, k := range want {
		if _, ok := cases[k]; !ok {
			t.Errorf("missing query case %q", k)
		}
	}
	if got := cases["user"].Required; len(got) != 1 || got[0] != "name" {
		t.Errorf("user required = %v, want [name]", got)
	}
	if got := cases["gitRepo"]; got.Handler != nil {
		t.Error("rbac must not own fleet cases")
	}
}

// newRBACQueryCaseTools builds Tools over a fake dynamic client holding two
// users and three PRTBs in two project namespaces.
func newRBACQueryCaseTools(t *testing.T) *Tools {
	t.Helper()
	user := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "management.cattle.io/v3", "kind": "User",
		"metadata": map[string]any{"name": "u-abc123"},
		"username": "admin", "displayName": "Default Admin",
	}}
	prtbInProject := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "management.cattle.io/v3", "kind": "ProjectRoleTemplateBinding",
		"metadata":    map[string]any{"name": "prtb-1", "namespace": "local-p-abc"},
		"projectName": "local:p-abc", "userName": "u-abc123", "roleTemplateName": "project-owner",
	}}
	prtbOtherProject := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "management.cattle.io/v3", "kind": "ProjectRoleTemplateBinding",
		"metadata":    map[string]any{"name": "prtb-2", "namespace": "local-p-xyz"},
		"projectName": "local:p-xyz", "userName": "u-abc123", "roleTemplateName": "project-member",
	}}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(rbacScheme(), rbacGVRs, user, prtbInProject, prtbOtherProject)
	c := &client.Client{DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil }}
	return NewTools(test.WrapClient(c, fakeToken), false)
}

// TestUserCaseMapsNameToUsername is the T4 field-mapping pin: the merged "name"
// parameter carries the username, so the user case must resolve the user whose
// username matches it — a dropped or renamed mapping returns nothing.
func TestUserCaseMapsNameToUsername(t *testing.T) {
	tools := newRBACQueryCaseTools(t)
	res, _, err := tools.QueryCases()["user"].Handler(
		middleware.WithToken(context.Background(), fakeToken), &mcp.CallToolRequest{},
		dispatch.QueryParams{Resource: "user", Name: "admin"})
	require.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"username":"admin"`)
	assert.Contains(t, text, `"name":"u-abc123"`)
}

// TestProjectRTBsCaseMapsProjectToProjectID is the second T4 mapping pin: the
// merged "project" parameter carries the project ID, so the case must scope the
// listing to that project's backing namespace.
func TestProjectRTBsCaseMapsProjectToProjectID(t *testing.T) {
	tools := newRBACQueryCaseTools(t)
	res, _, err := tools.QueryCases()["projectRTBs"].Handler(
		middleware.WithToken(context.Background(), fakeToken), &mcp.CallToolRequest{},
		dispatch.QueryParams{Resource: "projectRTBs", Cluster: "local", Project: "p-abc"})
	require.NoError(t, err)
	text := res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"name":"prtb-1"`, "the requested project's binding must be listed")
	assert.NotContains(t, text, `"name":"prtb-2"`, "another project's binding must be filtered out by the mapped project ID")

	// Without the project filter both bindings are visible: the mapping is what
	// narrows the list, not an always-on filter.
	res, _, err = tools.QueryCases()["projectRTBs"].Handler(
		middleware.WithToken(context.Background(), fakeToken), &mcp.CallToolRequest{},
		dispatch.QueryParams{Resource: "projectRTBs", Cluster: "local"})
	require.NoError(t, err)
	text = res.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, text, `"name":"prtb-1"`)
	assert.Contains(t, text, `"name":"prtb-2"`)
}

// TestClusterRTBsCaseMapsUserAndGroup pins the optional filters of the CRTBs
// case: the flat user/group parameters must land in the typed params, so a
// filter that matches nothing returns no bindings.
func TestClusterRTBsCaseMapsUserAndGroup(t *testing.T) {
	crtb := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "management.cattle.io/v3", "kind": "ClusterRoleTemplateBinding",
		"metadata":    map[string]any{"name": "crtb-1", "namespace": "local"},
		"clusterName": "local", "userName": "u-abc123", "roleTemplateName": "cluster-owner",
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(rbacScheme(), rbacGVRs, crtb)
	c := &client.Client{DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil }}
	tools := NewTools(test.WrapClient(c, fakeToken), false)
	ctx := middleware.WithToken(context.Background(), fakeToken)

	res, _, err := tools.QueryCases()["clusterRTBs"].Handler(ctx, &mcp.CallToolRequest{},
		dispatch.QueryParams{Resource: "clusterRTBs", Cluster: "local", User: "u-abc123"})
	require.NoError(t, err)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, `"name":"crtb-1"`)

	res, _, err = tools.QueryCases()["clusterRTBs"].Handler(ctx, &mcp.CallToolRequest{},
		dispatch.QueryParams{Resource: "clusterRTBs", Cluster: "local", User: "u-other"})
	require.NoError(t, err)
	assert.NotContains(t, res.Content[0].(*mcp.TextContent).Text, `"name":"crtb-1"`,
		"a non-matching user filter must exclude the binding")
}
