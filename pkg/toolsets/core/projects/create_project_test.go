package projects

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

func createProjectScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	return scheme
}

// projectGVR is the GVR the projects toolset creates projects at.
var projectGVR = schema.GroupVersionResource{Group: "management.cattle.io", Version: "v3", Resource: "projects"}

// newProjectTestTools builds Tools with a real confirmation gate and a fake
// dynamic client that serves the projects GVR.
func newProjectTestTools(t *testing.T, cfg toolconfig.Config, dyn *dynamicfake.FakeDynamicClient) *Tools {
	t.Helper()
	c := &client.Client{
		DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) {
			return dyn, nil
		},
	}
	return NewTools(test.WrapClient(c, "fakeToken"), cfg)
}

// projectDynClient returns a fake dynamic client with an empty project list.
func projectDynClient() *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createProjectScheme(), map[schema.GroupVersionResource]string{
		projectGVR: "ProjectList",
	})
}

// fakeGates returns a real confirmation gate whose elicitation is replaced by
// the given function. A nil fn installs one that fails the test if called.
func fakeGates(t *testing.T, fn func(ctx context.Context, ss *mcp.ServerSession, params *mcp.ElicitParams) (*mcp.ElicitResult, error)) *confirm.Gate {
	t.Helper()
	gate, err := confirm.NewGate()
	require.NoError(t, err)
	if fn == nil {
		fn = func(context.Context, *mcp.ServerSession, *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			t.Error("elicitation must not be called")
			return nil, errors.New("unexpected elicitation")
		}
	}
	gate.ElicitFunc = fn
	return gate
}

// approveElicit accepts the confirmation form with "approve".
func approveElicit(context.Context, *mcp.ServerSession, *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": "approve"}}, nil
}

// declineElicit rejects the confirmation form.
func declineElicit(context.Context, *mcp.ServerSession, *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	return &mcp.ElicitResult{Action: "decline"}, nil
}

// countProjectCreates returns how many create actions the fake client recorded.
func countProjectCreates(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "create" {
			n++
		}
	}
	return n
}

// issueProjectToken mints the confirmation token the plan tool would have
// issued for the given createProject parameters.
func issueProjectToken(t *testing.T, gate *confirm.Gate, params createProjectParams) string {
	t.Helper()
	project, err := (&Tools{}).createProjectObj(params)
	require.NoError(t, err)
	payload, err := json.Marshal(project.Object)
	require.NoError(t, err)
	token, err := gate.IssueToken(confirm.Operation{
		Tool: "createProject", Cluster: params.Cluster, Kind: "project", Name: params.Name, Payload: payload,
	})
	require.NoError(t, err)
	return token
}

// TestCreateProjectRequiresToken proves a createProject without a confirmation
// token is rejected by the token gate and never reaches the cluster.
func TestCreateProjectRequiresToken(t *testing.T) {
	dyn := projectDynClient()
	tools := newProjectTestTools(t, toolconfig.Config{Gate: fakeGates(t, approveElicit)}, dyn)

	_, _, err := tools.createProject(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, createProjectParams{
		Cluster: "local",
		Name:    "test-project",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Zero(t, countProjectCreates(dyn), "no create must reach the cluster without a token")
}

// TestCreateProjectDeclined proves a declined elicitation yields the standard
// cancellation result and executes nothing.
func TestCreateProjectDeclined(t *testing.T) {
	dyn := projectDynClient()
	gate := fakeGates(t, declineElicit)
	tools := newProjectTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := createProjectParams{Cluster: "local", Name: "test-project", DisplayName: "Test Project"}
	params.ConfirmationToken = issueProjectToken(t, gate, params)

	result, _, err := tools.createProject(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "Operation cancelled by the user. Nothing was executed.", result.Content[0].(*mcp.TextContent).Text)
	assert.Zero(t, countProjectCreates(dyn), "declined create must not reach the cluster")
}

// TestCreateProjectAutoWrite proves auto-write mode bypasses both the token and
// the user confirmation.
func TestCreateProjectAutoWrite(t *testing.T) {
	dyn := projectDynClient()
	// fakeGates(t, nil) makes elicitation a test failure if it is ever invoked.
	tools := newProjectTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil), AutoWrite: true}, dyn)

	result, _, err := tools.createProject(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, createProjectParams{
		Cluster: "local",
		Name:    "test-project",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Content)
	assert.Equal(t, 1, countProjectCreates(dyn), "auto-write create must be executed")
}

// TestCreateProjectTokenMismatchRejected proves a token minted for one project
// cannot be reused to create a different project.
func TestCreateProjectTokenMismatchRejected(t *testing.T) {
	dyn := projectDynClient()
	gate := fakeGates(t, approveElicit)
	tools := newProjectTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	tampered := createProjectParams{Cluster: "local", Name: "other-project"}
	tampered.ConfirmationToken = issueProjectToken(t, gate, createProjectParams{Cluster: "local", Name: "test-project"})

	_, _, err := tools.createProject(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, tampered)
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenMismatch)
	assert.Zero(t, countProjectCreates(dyn), "a mismatching project must not reach the cluster")
}

// TestCreateProjectApprovesExactObject proves the user is asked to approve the
// exact project object that will be submitted, and that approval then creates it.
func TestCreateProjectApprovesExactObject(t *testing.T) {
	dyn := projectDynClient()

	var summary string
	gate := fakeGates(t, func(ctx context.Context, _ *mcp.ServerSession, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		summary = params.Message
		return approveElicit(ctx, nil, params)
	})
	tools := newProjectTestTools(t, toolconfig.Config{Gate: gate}, dyn)

	params := createProjectParams{Cluster: "local", Name: "test-project", DisplayName: "Test Project"}
	params.ConfirmationToken = issueProjectToken(t, gate, params)

	_, _, err := tools.createProject(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)
	assert.Contains(t, summary, "CREATE project test-project in cluster \"local\"")
	assert.Contains(t, summary, `"containerDefaultResourceLimit"`, "the approved summary must show the exact object being created")

	require.Equal(t, 1, countProjectCreates(dyn))
	created := dyn.Actions()[len(dyn.Actions())-1].(clienttesting.CreateAction).GetObject()
	assert.Equal(t, "test-project", created.(*unstructured.Unstructured).GetName())
}

func TestCreateProject(t *testing.T) {
	fakeUrl := "https://localhost:8080"
	fakeToken := "fakeToken"

	tests := map[string]struct {
		params        createProjectParams
		fakeDynClient *dynamicfake.FakeDynamicClient

		// used in the CallToolRequest
		requestURL string
		// used in the creation of the Tools.
		rancherURL     string
		expectedResult string
		expectedError  string
	}{
		"create project": {
			params: createProjectParams{
				Cluster:     "local",
				Name:        "test-project",
				DisplayName: "Test Project",
				Description: "A test project",
			},
			requestURL: fakeUrl,
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createProjectScheme(), map[schema.GroupVersionResource]string{
				{Group: "management.cattle.io", Version: "v3", Resource: "projects"}: "ProjectList",
			}),
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "management.cattle.io/v3",
						"kind": "Project",
						"metadata": {"name": "test-project", "namespace": "local"},
						"spec": {
							"clusterName": "local",
							"displayName": "Test Project",
							"description": "A test project",
							"containerDefaultResourceLimit": {}
						}
					}
				],
				"uiContext": [
					{"namespace": "local", "kind": "Project", "cluster": "local", "name": "test-project", "type": "project"}
				]
			}`,
		},
		"create project with resource limits": {
			params: createProjectParams{
				Cluster:           "local",
				Name:              "project-with-limits",
				DisplayName:       "Project with Limits",
				CPULimit:          2000,
				CPUReservation:    1000,
				MemoryLimit:       4096,
				MemoryReservation: 2048,
			},
			requestURL: fakeUrl,
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createProjectScheme(), map[schema.GroupVersionResource]string{
				{Group: "management.cattle.io", Version: "v3", Resource: "projects"}: "ProjectList",
			}),
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "management.cattle.io/v3",
						"kind": "Project",
						"metadata": {"name": "project-with-limits", "namespace": "local"},
						"spec": {
							"clusterName": "local",
							"displayName": "Project with Limits",
							"containerDefaultResourceLimit": {
								"limitsCpu": "2000m",
								"requestsCpu": "1000m",
								"limitsMemory": "4096Mi",
								"requestsMemory": "2048Mi"
							}
						}
					}
				],
				"uiContext": [
					{"namespace": "local", "kind": "Project", "cluster": "local", "name": "project-with-limits", "type": "project"}
				]
			}`,
		},
		"create project when tool is configured with URL": {
			params: createProjectParams{
				Cluster: "local",
				Name:    "configured-url-project",
			},
			rancherURL: fakeUrl,
			fakeDynClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(createProjectScheme(), map[schema.GroupVersionResource]string{
				{Group: "management.cattle.io", Version: "v3", Resource: "projects"}: "ProjectList",
			}),
			expectedResult: `{
				"llm": [
					{
						"apiVersion": "management.cattle.io/v3",
						"kind": "Project",
						"metadata": {"name": "configured-url-project", "namespace": "local"},
						"spec": {
							"clusterName": "local",
							"containerDefaultResourceLimit": {}
						}
					}
				],
				"uiContext": [
					{"namespace": "local", "kind": "Project", "cluster": "local", "name": "configured-url-project", "type": "project"}
				]
			}`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := &client.Client{
				DynClientCreator: func(inConfig *rest.Config) (dynamic.Interface, error) {
					return tt.fakeDynClient, nil
				},
			}
			gate := fakeGates(t, approveElicit)
			tools := NewTools(test.WrapClient(c, fakeToken), toolconfig.Config{Gate: gate})
			req := &mcp.CallToolRequest{}

			params := tt.params
			// The execute tool requires the single-use token minted by the plan
			// tool for exactly this operation.
			params.ConfirmationToken = issueProjectToken(t, gate, params)

			result, _, err := tools.createProject(middleware.WithToken(t.Context(), fakeToken), req, params)

			if tt.expectedError != "" {
				assert.ErrorContains(t, err, tt.expectedError)
			} else {
				require.NoError(t, err)
				assert.JSONEq(t, tt.expectedResult, result.Content[0].(*mcp.TextContent).Text)
			}
		})
	}
}
