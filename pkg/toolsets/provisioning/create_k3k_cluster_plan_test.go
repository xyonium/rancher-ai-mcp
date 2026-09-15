package provisioning

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateK3kClusterPlanToken exercises the plan-token round trip: the token
// in the plan response must be accepted by the same gate for the exact
// operation the execute tool will perform. The plan key keeps its existing
// shape; the confirmation block is additive.
func TestCreateK3kClusterPlanToken(t *testing.T) {
	gate := fakeGates(t, nil)
	tools := Tools{cfg: toolconfig.Config{Gate: gate}}
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "createK3kClusterPlan"}}

	params := createK3kClusterParams{Name: "min-cluster", Namespace: "default", TargetCluster: "downstream-1"}

	result, _, err := tools.createK3kClusterPlan(t.Context(), req, params)
	require.NoError(t, err)

	var parsed struct {
		Plan         []map[string]any `json:"plan"`
		Confirmation struct {
			Token     string    `json:"confirmationToken"`
			ExpiresAt time.Time `json:"expiresAt"`
			Note      string    `json:"note"`
		} `json:"confirmation"`
	}
	raw := result.Content[0].(*mcp.TextContent).Text
	require.NoError(t, json.Unmarshal([]byte(raw), &parsed))
	require.NotEmpty(t, parsed.Confirmation.Token, "plan response must carry a confirmationToken")
	require.Len(t, parsed.Plan, 1)
	assert.Equal(t, "min-cluster", parsed.Plan[0]["resource"].(map[string]any)["name"])
	assert.WithinDuration(t, time.Now().Add(gate.TokenTTL), parsed.Confirmation.ExpiresAt, time.Minute)

	// The token binds the exact cluster object the execute tool submits.
	obj := tools.createK3kClusterObj(params)
	payload, err := json.Marshal(obj.Object)
	require.NoError(t, err)

	err = gate.RequireToken(confirm.Operation{
		Tool: "createK3kCluster", Cluster: "downstream-1", Namespace: "default",
		Kind: "cluster", Name: "min-cluster", Payload: payload,
	}, parsed.Confirmation.Token)
	require.NoError(t, err, "plan token must be accepted by the same gate for the exact operation")

	// The token is single-use: a second validation of the same plan fails.
	err = gate.RequireToken(confirm.Operation{
		Tool: "createK3kCluster", Cluster: "downstream-1", Namespace: "default",
		Kind: "cluster", Name: "min-cluster", Payload: payload,
	}, parsed.Confirmation.Token)
	assert.ErrorIs(t, err, confirm.ErrTokenConsumed)
}

// TestCreateK3kClusterPlanValidation proves the plan tool rejects missing
// required parameters before minting anything.
func TestCreateK3kClusterPlanValidation(t *testing.T) {
	tools := Tools{cfg: toolconfig.Config{Gate: fakeGates(t, nil)}}

	for _, params := range []createK3kClusterParams{
		{Namespace: "default", TargetCluster: "downstream-1"},
		{Name: "min-cluster", TargetCluster: "downstream-1"},
		{Name: "min-cluster", Namespace: "default"},
	} {
		_, _, err := tools.createK3kClusterPlan(t.Context(), &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "createK3kClusterPlan"}}, params)
		require.Error(t, err)
	}
}
