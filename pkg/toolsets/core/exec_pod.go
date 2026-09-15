package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

const (
	execTimeout     = 30 * time.Second
	execOutputLimit = 64 * 1024
)

// execExecutorFactory is replaceable in tests.
var execExecutorFactory = remotecommand.NewSPDYExecutor

type execPodParams struct {
	Cluster           string   `json:"cluster" jsonschema:"the name of the Kubernetes cluster"`
	Namespace         string   `json:"namespace" jsonschema:"the namespace of the pod"`
	Name              string   `json:"name" jsonschema:"the name of the pod"`
	Container         string   `json:"container,omitempty" jsonschema:"the container to execute in. Defaults to the first container"`
	Command           []string `json:"command" jsonschema:"the command to execute as an argv array (e.g. [\"ls\", \"-la\", \"/etc\"]). Never wrap it in a shell (sh -c) unless the user explicitly asked for shell behavior"`
	ConfirmationToken string   `json:"confirmationToken,omitempty" jsonschema:"REQUIRED: the single-use confirmationToken returned by execPodPlan for THIS exact command. Never invent, reuse, or guess a token"`
}

// execPodInputSchema builds the input schema for the exec tools.
func execPodInputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[execPodParams](nil)
	if err != nil {
		panic(fmt.Errorf("failed to build exec pod input schema: %w", err))
	}

	if command, ok := s.Properties["command"]; ok {
		// jsonschema-go infers a slice as type ["null", "array"]. Force a single "array" type
		// so agent clients (like Gemini / Vertex AI) can validate function declarations.
		command.Type = "array"
		command.Types = nil
	}

	return s
}

// cappedBuffer truncates at limit, recording truncation.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := c.limit - c.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			c.buf.Write(p[:remaining])
			c.truncated = true
		} else {
			c.buf.Write(p)
		}
	} else {
		c.truncated = true
	}
	return len(p), nil
}

func commandPayload(command []string) []byte { return []byte(strings.Join(command, "\x00")) }

// execPod executes a command in a pod container. The execution is gated behind
// a single-use plan token plus a direct user confirmation showing the exact
// command, and that gate is never bypassed — not even in auto-write mode.
func (t *Tools) execPod(ctx context.Context, toolReq *mcp.CallToolRequest, params execPodParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("execPod called")

	if len(params.Command) == 0 {
		return nil, nil, fmt.Errorf("command must not be empty")
	}
	token := middleware.Token(ctx)

	op := confirm.Operation{Tool: "execPod", Cluster: params.Cluster, Namespace: params.Namespace, Kind: "pod", Name: params.Name, Payload: commandPayload(params.Command)}
	summary := fmt.Sprintf("EXECUTE command in pod %s/%s (container %q) of cluster %q:\n\n  %s\n\nThis runs inside the pod with the pod's privileges.", params.Namespace, params.Name, params.Container, params.Cluster, strings.Join(params.Command, " "))
	approved, err := t.cfg.Gate.Check(ctx, toolReq.Session, op, params.ConfirmationToken, summary, "", false) // never exempted
	if err != nil {
		return nil, nil, err
	}
	if !approved {
		return confirm.CancelledResult(), nil, nil
	}

	clusterID, err := t.client.GetClusterID(ctx, token, params.Cluster)
	if err != nil {
		return nil, nil, err
	}
	restConfig, err := t.client.CreateRestConfig(token, clusterID)
	if err != nil {
		return nil, nil, err
	}

	execURL, err := url.Parse(restConfig.Host + fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/exec", url.PathEscape(params.Namespace), url.PathEscape(params.Name)))
	if err != nil {
		return nil, nil, err
	}
	q := execURL.Query()
	if params.Container != "" {
		q.Set("container", params.Container)
	}
	for _, c := range params.Command {
		q.Add("command", c)
	}
	q.Set("stdout", "true")
	q.Set("stderr", "true")
	execURL.RawQuery = q.Encode()

	executor, err := execExecutorFactory(restConfig, http.MethodPost, execURL)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create executor: %w", err)
	}

	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	stdout := &cappedBuffer{limit: execOutputLimit}
	stderr := &cappedBuffer{limit: execOutputLimit}
	streamErr := executor.StreamWithContext(execCtx, remotecommand.StreamOptions{
		Stdout: stdout,
		Stderr: stderr,
		Tty:    false,
	})

	result := map[string]any{
		"pod": params.Name, "namespace": params.Namespace, "container": params.Container,
		"command":         params.Command,
		"stdout":          stdout.buf.String(),
		"stderr":          stderr.buf.String(),
		"stdoutTruncated": stdout.truncated,
		"stderrTruncated": stderr.truncated,
	}
	if streamErr != nil {
		var exitErr k8sexec.CodeExitError
		if errors.As(streamErr, &exitErr) {
			result["exitCode"] = exitErr.Code
		} else if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			result["error"] = fmt.Sprintf("command timed out after %s", execTimeout)
		} else {
			zap.L().Error("failed to execute command in pod", zap.String("tool", "execPod"), zap.Error(streamErr))
			return nil, nil, fmt.Errorf("failed to execute command in pod %s/%s: %w", params.Namespace, params.Name, streamErr)
		}
	} else {
		result["exitCode"] = 0
	}

	mcpResponse, err := response.CreateMcpResponseAny(result)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}}}, nil, nil
}
