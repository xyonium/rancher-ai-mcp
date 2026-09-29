package projects

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
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

const (
	moveNsClusterID   = "c-move-namespace"
	moveNsProjectID   = "p-move-namespace"
	moveNsProjectName = "Destination Project"
	moveNsNamespace   = "workloads"
)

// moveNamespaceDynClient returns a fake dynamic client holding the management
// cluster, the destination project (by display name) and the namespace every
// moveNamespace test moves: workloads carries the label and annotation of its
// previous project plus unrelated metadata the move must preserve.
func moveNamespaceDynClient() *dynamicfake.FakeDynamicClient {
	cluster := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "management.cattle.io/v3",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": moveNsClusterID},
	}}
	project := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "management.cattle.io/v3",
		"kind":       "Project",
		"metadata":   map[string]any{"name": moveNsProjectID, "namespace": moveNsClusterID},
		"spec":       map[string]any{"displayName": moveNsProjectName},
	}}
	namespaceResource := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{
			Name: moveNsNamespace,
			Labels: map[string]string{
				"field.cattle.io/projectId": "p-previous",
				"team":                      "platform",
			},
			Annotations: map[string]string{
				"field.cattle.io/projectId": "c-previous:p-previous",
				"example.com/owner":         "platform",
			},
		},
	}

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = metav1.AddMetaToScheme(scheme)
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		{Group: "management.cattle.io", Version: "v3", Resource: "clusters"}: "ClusterList",
		{Group: "management.cattle.io", Version: "v3", Resource: "projects"}: "ProjectList",
	}, cluster, project, namespaceResource)
}

// moveNamespaceTestTools builds Tools over the move fixtures with the given
// gate-bearing config.
func moveNamespaceTestTools(cfg toolconfig.Config, dyn *dynamicfake.FakeDynamicClient) *Tools {
	c := &client.Client{DynClientCreator: func(*rest.Config) (dynamic.Interface, error) {
		return dyn, nil
	}}
	return NewTools(newFakeToolsClient(c, "fakeToken"), cfg)
}

// issueMoveNamespaceToken mints the confirmation token the plan tool would
// have issued for moving namespace to projectID in cluster.
func issueMoveNamespaceToken(t *testing.T, gate *confirm.Gate, cluster, namespace, projectID string) string {
	t.Helper()
	payload, err := moveNamespacePayload(cluster, namespace, projectID)
	require.NoError(t, err)
	token, err := gate.IssueToken(confirm.Operation{
		Tool: "moveNamespace", Cluster: cluster, Namespace: namespace, Kind: "namespace", Name: namespace, Payload: payload,
	})
	require.NoError(t, err)
	return token
}

// countNamespaceUpdates returns how many update actions the fake client recorded.
func countNamespaceUpdates(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "update" {
			n++
		}
	}
	return n
}

// TestMoveNamespace proves an approved move resolves the destination project
// by display name, sets its label and annotation on the namespace, and leaves
// every other label and annotation untouched.
func TestMoveNamespace(t *testing.T) {
	dyn := moveNamespaceDynClient()
	gate := fakeGates(t, approveElicit)
	tools := moveNamespaceTestTools(toolconfig.Config{Gate: gate}, dyn)

	result, _, err := tools.moveNamespace(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, moveNamespaceParams{
		Namespace:         moveNsNamespace,
		Project:           moveNsProjectName,
		Cluster:           moveNsClusterID,
		ConfirmationToken: issueMoveNamespaceToken(t, gate, moveNsClusterID, moveNsNamespace, moveNsProjectID),
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.JSONEq(t, `{
		"llm": [{
			"apiVersion": "v1",
			"kind": "Namespace",
			"metadata": {
				"name": "workloads",
				"labels": {
					"field.cattle.io/projectId": "p-move-namespace",
					"team": "platform"
				},
				"annotations": {
					"field.cattle.io/projectId": "c-move-namespace:p-move-namespace",
					"example.com/owner": "platform"
				}
			},
			"spec": {},
			"status": {}
		}],
		"uiContext": [{
			"cluster": "c-move-namespace",
			"kind": "Namespace",
			"name": "workloads",
			"namespace": "",
			"type": "namespace"
		}]
	}`, result.Content[0].(*mcp.TextContent).Text)
	assert.Equal(t, 1, countNamespaceUpdates(dyn))
}

// TestMoveNamespaceRequiresToken proves a move without a confirmation token is
// rejected by the gate and never reaches the cluster.
func TestMoveNamespaceRequiresToken(t *testing.T) {
	dyn := moveNamespaceDynClient()
	tools := moveNamespaceTestTools(toolconfig.Config{Gate: fakeGates(t, approveElicit)}, dyn)

	_, _, err := tools.moveNamespace(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, moveNamespaceParams{
		Namespace: moveNsNamespace,
		Project:   moveNsProjectName,
		Cluster:   moveNsClusterID,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Zero(t, countNamespaceUpdates(dyn), "no update must reach the cluster without a token")
}

// TestMoveNamespaceDeclined proves a declined confirmation yields the standard
// cancellation result and moves nothing.
func TestMoveNamespaceDeclined(t *testing.T) {
	dyn := moveNamespaceDynClient()
	gate := fakeGates(t, declineElicit)
	tools := moveNamespaceTestTools(toolconfig.Config{Gate: gate}, dyn)

	result, _, err := tools.moveNamespace(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, moveNamespaceParams{
		Namespace:         moveNsNamespace,
		Project:           moveNsProjectName,
		Cluster:           moveNsClusterID,
		ConfirmationToken: issueMoveNamespaceToken(t, gate, moveNsClusterID, moveNsNamespace, moveNsProjectID),
	})
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "Operation cancelled by the user. Nothing was executed.", result.Content[0].(*mcp.TextContent).Text)
	assert.Zero(t, countNamespaceUpdates(dyn), "a declined move must not reach the cluster")
}

// TestMoveNamespaceTokenMismatchRejected proves the token binds the resolved
// identity of the move: a token minted for another namespace or another
// destination project cannot be replayed for this one.
func TestMoveNamespaceTokenMismatchRejected(t *testing.T) {
	dyn := moveNamespaceDynClient()
	gate := fakeGates(t, approveElicit)
	tools := moveNamespaceTestTools(toolconfig.Config{Gate: gate}, dyn)

	for name, token := range map[string]string{
		"other namespace": issueMoveNamespaceToken(t, gate, moveNsClusterID, "other-namespace", moveNsProjectID),
		"other project":   issueMoveNamespaceToken(t, gate, moveNsClusterID, moveNsNamespace, "p-other"),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := tools.moveNamespace(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, moveNamespaceParams{
				Namespace:         moveNsNamespace,
				Project:           moveNsProjectName,
				Cluster:           moveNsClusterID,
				ConfirmationToken: token,
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, confirm.ErrTokenMismatch)
		})
	}
	assert.Zero(t, countNamespaceUpdates(dyn), "a mismatching token must not reach the cluster")
}

// TestMoveNamespaceAutoWrite proves auto-write mode bypasses the token and the
// user confirmation for this update-class operation.
func TestMoveNamespaceAutoWrite(t *testing.T) {
	dyn := moveNamespaceDynClient()
	// fakeGates(t, nil) makes elicitation a test failure if it is ever invoked.
	tools := moveNamespaceTestTools(toolconfig.Config{Gate: fakeGates(t, nil), AutoWrite: true}, dyn)

	result, _, err := tools.moveNamespace(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, moveNamespaceParams{
		Namespace: moveNsNamespace,
		Project:   moveNsProjectName,
		Cluster:   moveNsClusterID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	assert.Equal(t, 1, countNamespaceUpdates(dyn), "auto-write move must be executed")
}

// TestMoveNamespacePlan proves the plan shows the namespace as it will look
// after the move, mints a confirmation token, and persists nothing.
func TestMoveNamespacePlan(t *testing.T) {
	dyn := moveNamespaceDynClient()
	tools := moveNamespaceTestTools(toolconfig.Config{Gate: fakeGates(t, nil)}, dyn)

	result, _, err := tools.moveNamespacePlan(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, moveNamespaceParams{
		Namespace: moveNsNamespace,
		Project:   moveNsProjectName,
		Cluster:   moveNsClusterID,
	})
	require.NoError(t, err)
	plan := result.Content[0].(*mcp.TextContent).Text
	assert.Contains(t, plan, "confirmationToken")
	assert.Contains(t, plan, "p-move-namespace", "the plan must show the resolved destination project ID")
	assert.Contains(t, plan, moveNsClusterID+":"+moveNsProjectID, "the plan must show the moved projectId annotation")
	assert.Zero(t, countNamespaceUpdates(dyn), "a plan must not persist anything")
}
