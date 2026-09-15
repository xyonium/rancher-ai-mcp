package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// configMapGVR is the GVR the discovery fixture serves ConfigMaps under.
var configMapGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}

// deleteParamsFor returns the parameters of the ConfigMap deletion every test
// in this file operates on.
func deleteParamsFor() deleteKubernetesResourceParams {
	return deleteKubernetesResourceParams{
		Name:      "test-config",
		Namespace: "default",
		Kind:      "ConfigMap",
		Cluster:   "local",
	}
}

// newConfigMapDeleteClient returns a fake dynamic client pre-loaded with the
// ConfigMap the delete tools target.
func newConfigMapDeleteClient() *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createResourceScheme(), map[schema.GroupVersionResource]string{
		configMapGVR: "ConfigMapList",
	}, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default"},
		Data:       map[string]string{"key1": "value1"},
	})
}

// typedNameElicit accepts the confirmation form with the given typed resource
// name, which the gate compares against the exact name it asked for.
func typedNameElicit(name string) func(context.Context, *mcp.ServerSession, *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	return func(context.Context, *mcp.ServerSession, *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmName": name}}, nil
	}
}

// countDeletes returns how many delete actions the fake dynamic client recorded,
// optionally filtered to one GVR.
func countDeletes(dyn *dynamicfake.FakeDynamicClient, gvr *schema.GroupVersionResource) int {
	n := 0
	for _, action := range dyn.Actions() {
		if action.GetVerb() != "delete" {
			continue
		}
		if gvr != nil && action.GetResource() != *gvr {
			continue
		}
		n++
	}
	return n
}

// newDeleteTestTools builds Tools backed by fake discovery (so any kind resolves
// through apiVersion/kind) and a fake dynamic client, wrapped so tokens are
// validated too.
func newDeleteTestTools(t *testing.T, cfg toolconfig.Config, dyn *dynamicfake.FakeDynamicClient) *Tools {
	t.Helper()
	return newCreateTestTools(t, cfg, dyn)
}

// issueDeleteToken mints the confirmation token the plan tool would have issued
// for the given deletion parameters.
func issueDeleteToken(t *testing.T, gate *confirm.Gate, params deleteKubernetesResourceParams) string {
	t.Helper()
	token, err := gate.IssueToken(confirm.Operation{
		Tool: "deleteKubernetesResource", Cluster: params.Cluster, Namespace: params.Namespace,
		Kind: params.Kind, Name: params.Name,
	})
	require.NoError(t, err)
	return token
}

// TestDeleteRequiresTypedName proves the user must type the exact resource name:
// an accepted elicitation whose confirmName does not match the resource name
// deletes nothing and yields the standard cancellation result.
func TestDeleteRequiresTypedName(t *testing.T) {
	gate := fakeGates(t, typedNameElicit("wrong-name"))
	dyn := newConfigMapDeleteClient()
	tools := newDeleteTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := deleteParamsFor()
	params.ConfirmationToken = issueDeleteToken(t, gate, params)

	result, _, err := tools.deleteKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "Operation cancelled by the user. Nothing was executed.", result.Content[0].(*mcp.TextContent).Text)
	assert.Zero(t, countDeletes(dyn, nil), "a mistyped resource name must not delete anything")
}

// TestDeleteTypedNameMatchExecutes proves typing the exact resource name lets
// the deletion through to the cluster, on the discovered GVR.
func TestDeleteTypedNameMatchExecutes(t *testing.T) {
	gate := fakeGates(t, typedNameElicit("test-config"))
	dyn := newConfigMapDeleteClient()
	tools := newDeleteTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := deleteParamsFor()
	params.ConfirmationToken = issueDeleteToken(t, gate, params)

	result, _, err := tools.deleteKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	// The response identifies exactly what disappeared, for the UI to report.
	assert.JSONEq(t, `{
		"llm": {"deleted": "test-config", "kind": "ConfigMap", "cluster": "local", "namespace": "default"},
		"uiContext": [{"namespace": "default", "kind": "ConfigMap", "cluster": "local", "name": "test-config", "type": "configmap"}]
	}`, result.Content[0].(*mcp.TextContent).Text)

	require.Equal(t, 1, countDeletes(dyn, &configMapGVR), "the delete must land on the discovered GVR")
	deleted := dyn.Actions()[len(dyn.Actions())-1].(clienttesting.DeleteAction)
	assert.Equal(t, "test-config", deleted.GetName())
	assert.Equal(t, "default", deleted.GetNamespace())
}

// TestDeleteRequiresToken proves a deletion without a confirmation token is
// rejected by the token gate and never reaches the cluster.
func TestDeleteRequiresToken(t *testing.T) {
	dyn := newConfigMapDeleteClient()
	// fakeGates(t, nil) makes elicitation a test failure if it is ever invoked.
	tools := newDeleteTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil)}, dyn)

	_, _, err := tools.deleteKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, deleteParamsFor())
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Zero(t, countDeletes(dyn, nil), "no delete must reach the cluster without a token")
}

// TestDeleteNotExemptedByAutoWrite is the hard lock of the delete tool: even
// when the server explicitly runs with AutoWrite enabled, a deletion without a
// valid token is still rejected and nothing is deleted.
func TestDeleteNotExemptedByAutoWrite(t *testing.T) {
	dyn := newConfigMapDeleteClient()
	// autoWriteTools would execute a create/patch without any token; a delete
	// must NOT. Elicitation is a test failure if it is ever invoked.
	tools := newDeleteTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil), AutoWrite: true}, dyn)

	_, _, err := tools.deleteKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, deleteParamsFor())
	require.Error(t, err, "auto-write mode must never bypass the delete confirmation")
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Zero(t, countDeletes(dyn, nil), "auto-write mode must not delete anything without a token")
}

// TestDeleteAutoWriteDeclineStillFails proves the lock also holds when a token
// is present but the user declines: auto-write mode must not turn a decline
// into an execution.
func TestDeleteAutoWriteDeclineStillFails(t *testing.T) {
	gate := fakeGates(t, declineElicit)
	dyn := newConfigMapDeleteClient()
	tools := newDeleteTestTools(t, toolconfig.Config{Gate: gate, AutoWrite: true}, dyn)

	params := deleteParamsFor()
	params.ConfirmationToken = issueDeleteToken(t, gate, params)

	result, _, err := tools.deleteKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "Operation cancelled by the user. Nothing was executed.", result.Content[0].(*mcp.TextContent).Text)
	assert.Zero(t, countDeletes(dyn, nil), "a declined delete must not execute even in auto-write mode")
}

// TestDeleteNothingRunsAfterCheckRejects proves no cluster call that could
// mutate state happens once the gate refuses: a token minted for a different
// resource is rejected by the gate and the fake dynamic client records no
// delete.
func TestDeleteNothingRunsAfterCheckRejects(t *testing.T) {
	gate := fakeGates(t, approveElicit)
	dyn := newConfigMapDeleteClient()
	tools := newDeleteTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	// Token minted for another object of the same kind: the gate rejects it.
	otherToken, err := gate.IssueToken(confirm.Operation{
		Tool: "deleteKubernetesResource", Cluster: "local", Namespace: "default",
		Kind: "ConfigMap", Name: "some-other-config",
	})
	require.NoError(t, err)

	params := deleteParamsFor()
	params.ConfirmationToken = otherToken

	_, _, err = tools.deleteKubernetesResource(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenMismatch)
	assert.Zero(t, countDeletes(dyn, nil), "nothing after the gate may run when the gate rejects")
}

// TestDeletePlanIncludesSnapshotAndToken proves the plan shows the user the
// exact resource that will be deleted together with a single-use token that the
// execute tool's operation accepts.
func TestDeletePlanIncludesSnapshotAndToken(t *testing.T) {
	gate := fakeGates(t, nil)
	dyn := newConfigMapDeleteClient()
	tools := newDeleteTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := deleteParamsFor()
	result, _, err := tools.deleteKubernetesResourcePlan(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	var parsed struct {
		Plan []struct {
			Type     string         `json:"type"`
			Payload  map[string]any `json:"payload"`
			Resource struct {
				Name      string `json:"name"`
				Kind      string `json:"kind"`
				Cluster   string `json:"cluster"`
				Namespace string `json:"namespace"`
			} `json:"resource"`
		} `json:"plan"`
		Confirmation struct {
			Token     string `json:"confirmationToken"`
			ExpiresAt string `json:"expiresAt"`
			Note      string `json:"note"`
		} `json:"confirmation"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))

	require.Len(t, parsed.Plan, 1)
	assert.Equal(t, "delete", parsed.Plan[0].Type)
	assert.Equal(t, "test-config", parsed.Plan[0].Resource.Name)
	assert.Equal(t, "ConfigMap", parsed.Plan[0].Resource.Kind)
	assert.Equal(t, "local", parsed.Plan[0].Resource.Cluster)
	assert.Equal(t, "default", parsed.Plan[0].Resource.Namespace)

	// The snapshot is the current object, so the user sees what will disappear.
	assert.Equal(t, "ConfigMap", parsed.Plan[0].Payload["kind"])
	assert.Equal(t, map[string]any{"key1": "value1"}, parsed.Plan[0].Payload["data"])

	require.NotEmpty(t, parsed.Confirmation.Token, "plan response must carry a confirmationToken")
	assert.Contains(t, parsed.Confirmation.Note, "WILL BE PERMANENTLY DELETED")

	require.NoError(t, gate.RequireToken(confirm.Operation{
		Tool: "deleteKubernetesResource", Cluster: params.Cluster, Namespace: params.Namespace,
		Kind: params.Kind, Name: params.Name,
	}, parsed.Confirmation.Token), "plan token must be accepted by the same gate for the exact deletion")

	// The token is single-use: a second validation of the same plan fails.
	assert.ErrorIs(t, gate.RequireToken(confirm.Operation{
		Tool: "deleteKubernetesResource", Cluster: params.Cluster, Namespace: params.Namespace,
		Kind: params.Kind, Name: params.Name,
	}, parsed.Confirmation.Token), confirm.ErrTokenConsumed)
}

// TestDeletePlanNotFound proves planning the deletion of a missing object is an
// error: there is nothing to show the user and nothing to delete.
func TestDeletePlanNotFound(t *testing.T) {
	gate := fakeGates(t, nil)
	dyn := newConfigMapDeleteClient()
	tools := newDeleteTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := deleteParamsFor()
	params.Name = "missing-config"

	_, _, err := tools.deleteKubernetesResourcePlan(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.Error(t, err)
	assert.ErrorContains(t, err, "cannot plan deletion")
	assert.ErrorContains(t, err, "missing-config")
	assert.Zero(t, countDeletes(dyn, nil))
}
