package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/client/test"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

// execPodGVR is the GVR the discovery fixture serves pods under.
var execPodGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}

// execPodTestScheme is the scheme used by the fake dynamic client backing the
// exec tests.
func execPodTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return scheme
}

// newExecPodTestTools builds Tools backed by fake discovery, a fake dynamic
// client holding the pod under test, and a real *client.Client pointed at a
// fixed Rancher URL, so the exec URL built from CreateRestConfig is
// deterministic. The bearer token flows through the wrapped client, which
// validates it exactly like the toolsClient implementations do.
func newExecPodTestTools(t *testing.T, cfg toolconfig.Config, dyn *dynamicfake.FakeDynamicClient) *Tools {
	t.Helper()
	t.Setenv("RANCHER_URL", "https://rancher.example.com")
	c, err := client.NewClient(true, "")
	require.NoError(t, err)
	c.ClientSetCreator = fakeDiscoveryClientset(t, listAPIResourcesDiscovery)
	c.DynClientCreator = func(*rest.Config) (dynamic.Interface, error) { return dyn, nil }
	return NewTools(test.WrapClient(c, "fakeToken"), cfg)
}

// execPodTestDynamicClient returns a fake dynamic client pre-loaded with the pod
// the exec tools target: two containers and one init container, in namespace
// "default".
func execPodTestDynamicClient() *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(execPodTestScheme(), map[schema.GroupVersionResource]string{
		execPodGVR: "PodList",
	}, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers:     []corev1.Container{{Name: "first-container"}, {Name: "second-container"}},
			InitContainers: []corev1.Container{{Name: "init-container"}},
		},
	})
}

// execParamsFor returns the parameters of the exec every test in this file
// operates on.
func execParamsFor() execPodParams {
	return execPodParams{
		Cluster:   "local",
		Namespace: "default",
		Name:      "test-pod",
		Container: "first-container",
		Command:   []string{"ls", "-la", "/etc"},
	}
}

// issueExecToken mints the confirmation token the plan tool would have issued
// for the given exec parameters: the command array is the bound payload.
func issueExecToken(t *testing.T, gate *confirm.Gate, params execPodParams) string {
	t.Helper()
	token, err := gate.IssueToken(confirm.Operation{
		Tool: "execPod", Cluster: params.Cluster, Namespace: params.Namespace,
		Kind: "pod", Name: params.Name, Payload: commandPayload(params.Command),
	})
	require.NoError(t, err)
	return token
}

// fakeExecutor is a remotecommand.Executor that writes fixed output to the
// supplied streams instead of speaking SPDY to a real API server. It records
// the options it was handed so tests can assert the stream is non-interactive.
type fakeExecutor struct {
	stdout    string
	stderr    string
	streamErr error
	options   remotecommand.StreamOptions
}

func (f *fakeExecutor) Stream(options remotecommand.StreamOptions) error {
	return f.StreamWithContext(context.Background(), options)
}

func (f *fakeExecutor) StreamWithContext(_ context.Context, options remotecommand.StreamOptions) error {
	f.options = options
	if f.stdout != "" && options.Stdout != nil {
		_, _ = io.WriteString(options.Stdout, f.stdout)
	}
	if f.stderr != "" && options.Stderr != nil {
		_, _ = io.WriteString(options.Stderr, f.stderr)
	}
	return f.streamErr
}

// execFactoryCapture records the arguments the tool passed to the executor
// factory, so tests can assert which cluster request was about to be made.
type execFactoryCapture struct {
	config *rest.Config
	method string
	url    *url.URL
}

// fakeExecFactory replaces execExecutorFactory for one test with a factory that
// returns the given executor. A nil URL in the capture means no stream was ever
// about to start.
func fakeExecFactory(t *testing.T, executor remotecommand.Executor) *execFactoryCapture {
	t.Helper()
	previous := execExecutorFactory
	t.Cleanup(func() { execExecutorFactory = previous })

	capture := &execFactoryCapture{}
	execExecutorFactory = func(config *rest.Config, method string, u *url.URL) (remotecommand.Executor, error) {
		capture.config, capture.method, capture.url = config, method, u
		return executor, nil
	}
	return capture
}

// TestExecRequiresToken proves an exec without a confirmation token is rejected
// by the token gate and never starts a stream.
func TestExecRequiresToken(t *testing.T) {
	dyn := execPodTestDynamicClient()
	// fakeGates(t, nil) makes elicitation a test failure if it is ever invoked.
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil), EnableExec: true}, dyn)
	capture := fakeExecFactory(t, &fakeExecutor{})

	_, _, err := tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, execParamsFor())
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Nil(t, capture.url, "no stream may be started without a token")
}

// TestExecNotExemptedByAutoWrite is the hard lock of the exec tool: even when
// the server explicitly runs with AutoWrite enabled, an exec without a valid
// token is still rejected and nothing is executed.
func TestExecNotExemptedByAutoWrite(t *testing.T) {
	dyn := execPodTestDynamicClient()
	// autoWriteTools would execute a create/patch without any token; an exec
	// must NOT. Elicitation is a test failure if it is ever invoked.
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil), AutoWrite: true, EnableExec: true}, dyn)
	capture := fakeExecFactory(t, &fakeExecutor{})

	_, _, err := tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, execParamsFor())
	require.Error(t, err, "auto-write mode must never bypass the exec confirmation")
	assert.ErrorIs(t, err, confirm.ErrTokenInvalid)
	assert.Nil(t, capture.url, "auto-write mode must not execute anything without a token")
}

// TestExecTokenBindsExactCommand proves the token binds the exact command: a
// token minted for one command must be rejected when a different command is
// executed, and no stream is started. The second case pins that the command
// really participates in the binding — a token that matches the pod identity
// but was minted without a command payload is rejected too.
func TestExecTokenBindsExactCommand(t *testing.T) {
	// Elicitation must never be reached: the gate must reject on the token
	// alone, before the user is asked anything.
	gate := fakeGates(t, nil)
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: gate, EnableExec: true}, dyn)
	capture := fakeExecFactory(t, &fakeExecutor{})

	// Token minted for a different command.
	params := execParamsFor()
	params.ConfirmationToken = issueExecToken(t, gate, execPodParams{
		Cluster: params.Cluster, Namespace: params.Namespace, Name: params.Name,
		Container: params.Container, Command: []string{"rm", "-rf", "/"},
	})
	_, _, err := tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenMismatch)
	assert.Nil(t, capture.url, "the command must not run when the token does not match it")

	// Token minted for the same pod identity but with no command bound at all.
	payloadlessToken, err := gate.IssueToken(confirm.Operation{
		Tool: "execPod", Cluster: params.Cluster, Namespace: params.Namespace,
		Kind: "pod", Name: params.Name,
	})
	require.NoError(t, err)
	params.ConfirmationToken = payloadlessToken

	_, _, err = tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.Error(t, err)
	assert.ErrorIs(t, err, confirm.ErrTokenMismatch, "a token that does not bind the command must never execute it")
	assert.Nil(t, capture.url)
}

// TestExecDeclined proves a declined elicitation yields the standard
// cancellation result and starts no stream.
func TestExecDeclined(t *testing.T) {
	gate := fakeGates(t, declineElicit)
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: gate, EnableExec: true}, dyn)
	capture := fakeExecFactory(t, &fakeExecutor{})

	params := execParamsFor()
	params.ConfirmationToken = issueExecToken(t, gate, params)

	result, _, err := tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	assert.Equal(t, "Operation cancelled by the user. Nothing was executed.", result.Content[0].(*mcp.TextContent).Text)
	assert.Nil(t, capture.url, "a declined exec must not start a stream")
}

// TestExecStreamsOutput proves the command runs non-interactively (no TTY, no
// stdin), lands on the right URL with the argv as query parameters, and that
// both streams' output plus the exit code are reported to the agent.
func TestExecStreamsOutput(t *testing.T) {
	gate := fakeGates(t, approveElicit)
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: gate, EnableExec: true}, dyn)
	executor := &fakeExecutor{stdout: "file1\nfile2\n", stderr: "warning: something\n"}
	capture := fakeExecFactory(t, executor)

	params := execParamsFor()
	params.ConfirmationToken = issueExecToken(t, gate, params)

	result, _, err := tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	var parsed struct {
		LLM map[string]any `json:"llm"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))
	assert.Equal(t, "test-pod", parsed.LLM["pod"])
	assert.Equal(t, "default", parsed.LLM["namespace"])
	assert.Equal(t, "first-container", parsed.LLM["container"])
	assert.Equal(t, []any{"ls", "-la", "/etc"}, parsed.LLM["command"])
	assert.Equal(t, "file1\nfile2\n", parsed.LLM["stdout"])
	assert.Equal(t, "warning: something\n", parsed.LLM["stderr"])
	assert.Equal(t, false, parsed.LLM["stdoutTruncated"])
	assert.Equal(t, false, parsed.LLM["stderrTruncated"])
	assert.Equal(t, float64(0), parsed.LLM["exitCode"], "a clean exit must be reported as exitCode 0")

	// The stream must be non-interactive: no TTY, no stdin, both outputs wired.
	assert.False(t, executor.options.Tty, "exec must never allocate a TTY")
	assert.Nil(t, executor.options.Stdin, "exec must never accept stdin")
	assert.NotNil(t, executor.options.Stdout)
	assert.NotNil(t, executor.options.Stderr)

	// The request must be a POST to the pod's exec subresource, with the
	// command argv passed as query parameters exactly as given, authenticated
	// with the caller's token.
	require.NotNil(t, capture.config)
	assert.Equal(t, "fakeToken", capture.config.BearerToken)
	require.NotNil(t, capture.url)
	assert.Equal(t, http.MethodPost, capture.method)
	assert.Equal(t, "/k8s/clusters/local/api/v1/namespaces/default/pods/test-pod/exec", capture.url.Path)
	q := capture.url.Query()
	assert.Equal(t, "first-container", q.Get("container"))
	assert.Equal(t, []string{"ls", "-la", "/etc"}, q["command"])
	assert.Equal(t, "true", q.Get("stdout"))
	assert.Equal(t, "true", q.Get("stderr"))
}

// TestExecOmitsEmptyContainer proves an empty container parameter sends no
// container query parameter at all, leaving the choice to the API server.
func TestExecOmitsEmptyContainer(t *testing.T) {
	gate := fakeGates(t, approveElicit)
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: gate, EnableExec: true}, dyn)
	capture := fakeExecFactory(t, &fakeExecutor{})

	params := execParamsFor()
	params.Container = ""
	params.ConfirmationToken = issueExecToken(t, gate, params)

	_, _, err := tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)
	require.NotNil(t, capture.url)
	assert.NotContains(t, capture.url.Query(), "container")
}

// TestExecTruncatesOutput proves each stream is capped at 64KB and the
// truncation is flagged in the response.
func TestExecTruncatesOutput(t *testing.T) {
	gate := fakeGates(t, approveElicit)
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: gate, EnableExec: true}, dyn)
	executor := &fakeExecutor{
		stdout: strings.Repeat("a", execOutputLimit+1024),
		stderr: strings.Repeat("b", execOutputLimit+1024),
	}
	fakeExecFactory(t, executor)

	params := execParamsFor()
	params.ConfirmationToken = issueExecToken(t, gate, params)

	result, _, err := tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	var parsed struct {
		LLM map[string]any `json:"llm"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))
	assert.Len(t, parsed.LLM["stdout"], execOutputLimit, "stdout must be capped at the limit")
	assert.Len(t, parsed.LLM["stderr"], execOutputLimit, "stderr must be capped at the limit")
	assert.Equal(t, true, parsed.LLM["stdoutTruncated"], "truncation must be flagged")
	assert.Equal(t, true, parsed.LLM["stderrTruncated"], "truncation must be flagged")
}

// TestExecNonZeroExitReported proves a command failing with a non-zero exit
// code is reported as a result with that exit code, not as a tool error.
func TestExecNonZeroExitReported(t *testing.T) {
	gate := fakeGates(t, approveElicit)
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: gate, EnableExec: true}, dyn)
	executor := &fakeExecutor{stdout: "partial output\n", streamErr: k8sexec.CodeExitError{Err: io.EOF, Code: 3}}
	fakeExecFactory(t, executor)

	params := execParamsFor()
	params.ConfirmationToken = issueExecToken(t, gate, params)

	result, _, err := tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err, "a non-zero exit code is a result, not a tool error")

	var parsed struct {
		LLM map[string]any `json:"llm"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))
	assert.Equal(t, float64(3), parsed.LLM["exitCode"])
	assert.Equal(t, "partial output\n", parsed.LLM["stdout"])
}

// TestExecEmptyCommandRejected proves an empty command array is rejected before
// the gate is even consulted.
func TestExecEmptyCommandRejected(t *testing.T) {
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil), EnableExec: true}, dyn)
	capture := fakeExecFactory(t, &fakeExecutor{})

	params := execParamsFor()
	params.Command = nil

	_, _, err := tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.Error(t, err)
	assert.ErrorContains(t, err, "command must not be empty")
	assert.Nil(t, capture.url)
}

// TestExecInvalidToken proves the wrapped client's token validation is enforced
// on the exec path too: a wrong bearer token never reaches the cluster.
func TestExecInvalidToken(t *testing.T) {
	gate := fakeGates(t, approveElicit)
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: gate, EnableExec: true}, dyn)
	capture := fakeExecFactory(t, &fakeExecutor{})

	params := execParamsFor()
	params.ConfirmationToken = issueExecToken(t, gate, params)

	_, _, err := tools.execPod(middleware.WithToken(t.Context(), "wrongToken"), &mcp.CallToolRequest{}, params)
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid token")
	assert.Nil(t, capture.url)
}

// TestExecPlanIncludesCommandAndToken proves the plan validates the pod, shows
// the user the exact command together with a single-use token, and does not ask
// for anything yet: the user confirmation happens at execute time.
func TestExecPlanIncludesCommandAndToken(t *testing.T) {
	gate := fakeGates(t, nil)
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: gate, EnableExec: true}, dyn)

	params := execParamsFor()
	params.Container = "" // let the plan fill in the first container name

	result, _, err := tools.execPodPlan(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	var parsed struct {
		Plan []struct {
			Type    string `json:"type"`
			Payload struct {
				Pod       string   `json:"pod"`
				Namespace string   `json:"namespace"`
				Container string   `json:"container"`
				Command   []string `json:"command"`
			} `json:"payload"`
			Resource struct {
				Name      string `json:"name"`
				Kind      string `json:"kind"`
				Cluster   string `json:"cluster"`
				Namespace string `json:"namespace"`
			} `json:"resource"`
		} `json:"plan"`
		Confirmation struct {
			Token string `json:"confirmationToken"`
			Note  string `json:"note"`
		} `json:"confirmation"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &parsed))

	require.Len(t, parsed.Plan, 1)
	assert.Equal(t, "execute", parsed.Plan[0].Type)
	assert.Equal(t, "test-pod", parsed.Plan[0].Resource.Name)
	assert.Equal(t, "pod", parsed.Plan[0].Resource.Kind)
	assert.Equal(t, "local", parsed.Plan[0].Resource.Cluster)
	assert.Equal(t, "default", parsed.Plan[0].Resource.Namespace)

	// The payload shows the exact command and the resolved default container.
	assert.Equal(t, "test-pod", parsed.Plan[0].Payload.Pod)
	assert.Equal(t, "default", parsed.Plan[0].Payload.Namespace)
	assert.Equal(t, "first-container", parsed.Plan[0].Payload.Container, "the plan must fill in the first container")
	assert.Equal(t, []string{"ls", "-la", "/etc"}, parsed.Plan[0].Payload.Command)

	require.NotEmpty(t, parsed.Confirmation.Token, "plan response must carry a confirmationToken")
	assert.Contains(t, parsed.Confirmation.Note, "exact command")

	// The token binds the pod and the exact command — the container is display
	// context only and is not part of the binding.
	require.NoError(t, gate.RequireToken(confirm.Operation{
		Tool: "execPod", Cluster: params.Cluster, Namespace: params.Namespace,
		Kind: "pod", Name: params.Name, Payload: commandPayload(params.Command),
	}, parsed.Confirmation.Token), "plan token must be accepted for the exact command")

	// The token is single-use: a second validation of the same plan fails.
	assert.ErrorIs(t, gate.RequireToken(confirm.Operation{
		Tool: "execPod", Cluster: params.Cluster, Namespace: params.Namespace,
		Kind: "pod", Name: params.Name, Payload: commandPayload(params.Command),
	}, parsed.Confirmation.Token), confirm.ErrTokenConsumed)
}

// TestExecPlanRoundTripExecutes proves the command shown in the plan is the
// command that runs: a token minted by execPodPlan is accepted by execPod, and
// the exec lands on the URL of the pod the plan validated.
func TestExecPlanRoundTripExecutes(t *testing.T) {
	gate := fakeGates(t, approveElicit)
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: gate, EnableExec: true}, dyn)
	executor := &fakeExecutor{stdout: "executed\n"}
	capture := fakeExecFactory(t, executor)

	params := execParamsFor()
	params.Container = ""

	// Plan mints the token and resolves the default container.
	planResult, _, err := tools.execPodPlan(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)
	var parsed struct {
		Plan []struct {
			Payload struct {
				Container string `json:"container"`
			} `json:"payload"`
		} `json:"plan"`
		Confirmation struct {
			Token string `json:"confirmationToken"`
		} `json:"confirmation"`
	}
	require.NoError(t, json.Unmarshal([]byte(planResult.Content[0].(*mcp.TextContent).Text), &parsed))
	require.NotEmpty(t, parsed.Confirmation.Token)
	require.Len(t, parsed.Plan, 1)
	require.Equal(t, "first-container", parsed.Plan[0].Payload.Container)

	// Execute with the token from the plan, carrying over the container the plan
	// resolved — the container is display context, so it does not invalidate the
	// token, and it is what the request is addressed to.
	params.Container = parsed.Plan[0].Payload.Container
	params.ConfirmationToken = parsed.Confirmation.Token
	result, _, err := tools.execPod(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.NoError(t, err)

	var execParsed struct {
		LLM map[string]any `json:"llm"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &execParsed))
	assert.Equal(t, "executed\n", execParsed.LLM["stdout"])

	require.NotNil(t, capture.url)
	assert.Equal(t, "/k8s/clusters/local/api/v1/namespaces/default/pods/test-pod/exec", capture.url.Path)
	assert.Equal(t, "first-container", capture.url.Query().Get("container"))
}

// TestExecPlanNotFound proves planning an exec in a missing pod is an error:
// there is nothing for the user to approve.
func TestExecPlanNotFound(t *testing.T) {
	dyn := execPodTestDynamicClient()
	// fakeGates(t, nil) makes elicitation a test failure if it is ever invoked.
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil), EnableExec: true}, dyn)

	params := execParamsFor()
	params.Name = "missing-pod"
	capture := fakeExecFactory(t, &fakeExecutor{})

	_, _, err := tools.execPodPlan(middleware.WithToken(t.Context(), "fakeToken"), &mcp.CallToolRequest{}, params)
	require.Error(t, err)
	assert.ErrorContains(t, err, "cannot plan exec")
	assert.ErrorContains(t, err, "missing-pod")
	assert.Nil(t, capture.url)
}

// TestExecPlanInvalidToken proves the wrapped client's token validation is
// enforced by the plan path too.
func TestExecPlanInvalidToken(t *testing.T) {
	dyn := execPodTestDynamicClient()
	tools := newExecPodTestTools(t, toolconfig.Config{Gate: fakeGates(t, nil), EnableExec: true}, dyn)

	_, _, err := tools.execPodPlan(middleware.WithToken(t.Context(), "wrongToken"), &mcp.CallToolRequest{}, execParamsFor())
	require.Error(t, err)
	assert.ErrorContains(t, err, "invalid token")
}

// TestExecInputSchema pins the Gemini/Vertex compatibility workaround: the
// command parameter must be declared as a plain array, never as a nullable
// ["null","array"] type.
func TestExecInputSchema(t *testing.T) {
	schema := execPodInputSchema()
	require.NotNil(t, schema)

	command, ok := schema.Properties["command"]
	require.True(t, ok, "command must be a declared parameter")
	assert.Equal(t, "array", command.Type)
	assert.Nil(t, command.Types, "command must not be nullable")

	for _, name := range []string{"cluster", "namespace", "name", "container", "command", "confirmationToken"} {
		assert.Contains(t, schema.Properties, name, "parameter %s must be declared", name)
	}
}

// TestCappedBuffer documents the truncation helper's contract, including the
// boundary where a write lands exactly on the limit.
func TestCappedBuffer(t *testing.T) {
	tests := map[string]struct {
		writes        []string
		limit         int
		expected      string
		expectedTrunc bool
	}{
		"under the limit": {
			writes:        []string{"hello", " world"},
			limit:         64,
			expected:      "hello world",
			expectedTrunc: false,
		},
		"exactly on the limit": {
			writes:        []string{"hello", " world"},
			limit:         11,
			expected:      "hello world",
			expectedTrunc: false,
		},
		"single write over the limit": {
			writes:        []string{"hello world"},
			limit:         5,
			expected:      "hello",
			expectedTrunc: true,
		},
		"second write over the limit": {
			writes:        []string{"hello", " world"},
			limit:         8,
			expected:      "hello wo",
			expectedTrunc: true,
		},
		"write after full": {
			writes:        []string{"hello", "world"},
			limit:         5,
			expected:      "hello",
			expectedTrunc: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := &cappedBuffer{limit: tt.limit}
			for _, w := range tt.writes {
				n, err := c.Write([]byte(w))
				require.NoError(t, err)
				assert.Equal(t, len(w), n, "a capped buffer must report the full write as consumed")
			}
			assert.Equal(t, tt.expected, c.buf.String())
			assert.Equal(t, tt.expectedTrunc, c.truncated)
		})
	}
}

// TestCommandPayload pins the canonical payload form the token binds: the argv
// joined with NUL bytes, so argument boundaries are preserved and a command
// whose arguments were flattened into one cannot pass for the argv form.
func TestCommandPayload(t *testing.T) {
	assert.Equal(t, []byte("ls\x00-la"), commandPayload([]string{"ls", "-la"}))
	assert.NotEqual(t, commandPayload([]string{"ls -la"}), commandPayload([]string{"ls", "-la"}),
		"one flattened argument must not produce the same payload as two arguments")
	assert.Equal(t, []byte(""), commandPayload(nil))
}

var _ remotecommand.Executor = (*fakeExecutor)(nil)
