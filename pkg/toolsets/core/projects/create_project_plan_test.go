package projects

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateProjectPlan(t *testing.T) {
	fakeToken := "fakeToken"

	tests := map[string]struct {
		params         createProjectParams
		expectedError  string
		validateResult func(t *testing.T, result string)
	}{
		"create project plan": {
			params: createProjectParams{
				Cluster:     "local",
				Name:        "test-project",
				DisplayName: "Test Project",
				Description: "A test project",
			},
			validateResult: func(t *testing.T, result string) {
				var parsed struct {
					Plan []response.PlanResource `json:"plan"`
				}
				err := json.Unmarshal([]byte(result), &parsed)
				require.NoError(t, err)
				require.Len(t, parsed.Plan, 1)

				planResource := parsed.Plan[0]
				assert.Equal(t, response.OperationCreate, planResource.Type)
				assert.Equal(t, "test-project", planResource.Resource.Name)
				assert.Equal(t, "Project", planResource.Resource.Kind)
				assert.Equal(t, "local", planResource.Resource.Cluster)
				assert.Equal(t, "local", planResource.Resource.Namespace)

				// Validate the payload contains the expected project structure
				payload, ok := planResource.Payload.(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "management.cattle.io/v3", payload["apiVersion"])
				assert.Equal(t, "Project", payload["kind"])

				metadata, ok := payload["metadata"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "test-project", metadata["name"])
				assert.Equal(t, "local", metadata["namespace"])

				spec, ok := payload["spec"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "local", spec["clusterName"])
				assert.Equal(t, "Test Project", spec["displayName"])
				assert.Equal(t, "A test project", spec["description"])
			},
		},
		"create project plan with resource limits": {
			params: createProjectParams{
				Cluster:           "local",
				Name:              "project-with-limits",
				DisplayName:       "Project with Limits",
				CPULimit:          2000,
				CPUReservation:    1000,
				MemoryLimit:       4096,
				MemoryReservation: 2048,
			},
			validateResult: func(t *testing.T, result string) {
				var parsed struct {
					Plan []response.PlanResource `json:"plan"`
				}
				err := json.Unmarshal([]byte(result), &parsed)
				require.NoError(t, err)
				require.Len(t, parsed.Plan, 1)

				planResource := parsed.Plan[0]
				payload, ok := planResource.Payload.(map[string]any)
				require.True(t, ok)

				spec, ok := payload["spec"].(map[string]any)
				require.True(t, ok)

				resourceLimits, ok := spec["containerDefaultResourceLimit"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "2000m", resourceLimits["limitsCpu"])
				assert.Equal(t, "1000m", resourceLimits["requestsCpu"])
				assert.Equal(t, "4096Mi", resourceLimits["limitsMemory"])
				assert.Equal(t, "2048Mi", resourceLimits["requestsMemory"])
			},
		},
		"create project plan minimal": {
			params: createProjectParams{
				Cluster: "local",
				Name:    "minimal-project",
			},
			validateResult: func(t *testing.T, result string) {
				var parsed struct {
					Plan []response.PlanResource `json:"plan"`
				}
				err := json.Unmarshal([]byte(result), &parsed)
				require.NoError(t, err)
				require.Len(t, parsed.Plan, 1)

				planResource := parsed.Plan[0]
				assert.Equal(t, response.OperationCreate, planResource.Type)
				assert.Equal(t, "minimal-project", planResource.Resource.Name)

				payload, ok := planResource.Payload.(map[string]any)
				require.True(t, ok)

				spec, ok := payload["spec"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "local", spec["clusterName"])

				// Empty resource limits should still be present
				_, ok = spec["containerDefaultResourceLimit"]
				assert.True(t, ok)
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := &client.Client{}
			tools := NewTools(test.WrapClient(c, fakeToken), toolconfig.Config{Gate: fakeGates(t, nil)})
			req := &mcp.CallToolRequest{}

			result, _, err := tools.createProjectPlan(context.Background(), req, tt.params)

			if tt.expectedError != "" {
				assert.ErrorContains(t, err, tt.expectedError)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, result.Content, 1)

				textContent, ok := result.Content[0].(*mcp.TextContent)
				require.True(t, ok)

				if tt.validateResult != nil {
					tt.validateResult(t, textContent.Text)
				}
			}
		})
	}
}

// TestCreateProjectPlanToken exercises the plan-token round trip: the token in
// the plan response must be accepted by the same gate for the exact operation
// the execute tool will perform. The plan key keeps its existing shape; the
// confirmation block is additive.
func TestCreateProjectPlanToken(t *testing.T) {
	gate := fakeGates(t, nil)
	tools := NewTools(test.WrapClient(&client.Client{}, "fakeToken"), toolconfig.Config{Gate: gate})

	params := createProjectParams{
		Cluster:     "local",
		Name:        "test-project",
		DisplayName: "Test Project",
		Description: "A test project",
	}

	result, _, err := tools.createProjectPlan(context.Background(), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	var parsed struct {
		Plan         []response.PlanResource `json:"plan"`
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
	assert.Equal(t, response.OperationCreate, parsed.Plan[0].Type)
	assert.Equal(t, "test-project", parsed.Plan[0].Resource.Name)
	assert.Equal(t, "local", parsed.Plan[0].Resource.Cluster)
	assert.WithinDuration(t, time.Now().Add(gate.TokenTTL), parsed.Confirmation.ExpiresAt, time.Minute)

	// The token binds the exact project object the execute tool would submit.
	project, err := tools.createProjectObj(params)
	require.NoError(t, err)
	payload, err := json.Marshal(project.Object)
	require.NoError(t, err)

	err = gate.RequireToken(confirm.Operation{
		Tool: "createProject", Cluster: "local", Kind: "project", Name: "test-project", Payload: payload,
	}, parsed.Confirmation.Token)
	require.NoError(t, err, "plan token must be accepted by the same gate for the exact operation")

	// The token is single-use: a second validation of the same plan fails.
	err = gate.RequireToken(confirm.Operation{
		Tool: "createProject", Cluster: "local", Kind: "project", Name: "test-project", Payload: payload,
	}, parsed.Confirmation.Token)
	assert.ErrorIs(t, err, confirm.ErrTokenConsumed)
}
