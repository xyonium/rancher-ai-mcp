// pkg/toolsets/dispatch/dispatch_test.go
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeParams struct {
	Mode    string   `json:"mode"`
	Cluster string   `json:"cluster,omitempty"`
	Names   []string `json:"names,omitempty"`
}

func TestValidateMissingRequired(t *testing.T) {
	err := Validate("mode", "x", fakeParams{Mode: "x"}, []string{"cluster", "names"})
	if err == nil || !strings.Contains(err.Error(), `mode="x" is missing required parameter(s): cluster, names`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateOK(t *testing.T) {
	if err := Validate("mode", "x", fakeParams{Mode: "x", Cluster: "c", Names: []string{"a"}}, []string{"cluster", "names"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDispatchUnknownKeyListsValidValues(t *testing.T) {
	cases := map[string]Case[fakeParams]{
		"b": {Handler: func(context.Context, *mcp.CallToolRequest, fakeParams) (*mcp.CallToolResult, any, error) {
			return nil, nil, nil
		}},
		"a": {Handler: func(context.Context, *mcp.CallToolRequest, fakeParams) (*mcp.CallToolResult, any, error) {
			return nil, nil, nil
		}},
	}
	_, _, err := Dispatch(context.Background(), &mcp.CallToolRequest{}, "mode", "zzz", fakeParams{Mode: "zzz"}, cases)
	if err == nil || !strings.Contains(err.Error(), `unknown mode "zzz"`) || !strings.Contains(err.Error(), "a, b") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDispatchRunsHandler(t *testing.T) {
	called := false
	cases := map[string]Case[fakeParams]{
		"x": {
			Required: []string{"cluster"},
			Handler: func(_ context.Context, _ *mcp.CallToolRequest, p fakeParams) (*mcp.CallToolResult, any, error) {
				called = true
				return nil, nil, errors.New("stop here")
			},
		},
	}
	_, _, err := Dispatch(context.Background(), &mcp.CallToolRequest{}, "mode", "x", fakeParams{Mode: "x", Cluster: "c"}, cases)
	if err == nil || err.Error() != "stop here" || !called {
		t.Fatalf("handler not invoked: err=%v called=%v", err, called)
	}
}

func TestMergeMapsPanicsOnDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate case key")
		}
	}()
	c := Case[fakeParams]{Handler: func(context.Context, *mcp.CallToolRequest, fakeParams) (*mcp.CallToolResult, any, error) {
		return nil, nil, nil
	}}
	MergeMaps(map[string]Case[fakeParams]{"a": c}, map[string]Case[fakeParams]{"a": c})
}

func TestSchemas(t *testing.T) {
	q := QueryInputSchema()
	if got := len(q.Properties["resource"].Enum); got != len(QueryResources) {
		t.Fatalf("resource enum size = %d, want %d", got, len(QueryResources))
	}
	if len(q.Required) != 1 || q.Required[0] != "resource" {
		t.Fatalf("query required = %v, want [resource]", q.Required)
	}
	if q.Properties["clusters"].Type != "array" || q.Properties["clusters"].Types != nil {
		t.Fatalf("clusters must be forced to plain array type, got %+v", q.Properties["clusters"])
	}
	p := PlanInputSchema()
	if len(p.Properties["operation"].Enum) != len(ChangeOperations) {
		t.Fatal("plan schema operation enum mismatch")
	}
	for _, r := range p.Required {
		if r == "confirmationToken" {
			t.Fatal("plan schema must NOT require confirmationToken")
		}
	}
	e := ExecuteInputSchema(true)
	found := false
	for _, r := range e.Required {
		if r == "confirmationToken" {
			found = true
		}
	}
	if !found {
		t.Fatal("execute schema must require confirmationToken in strict mode")
	}
	if slices.Contains(ExecuteInputSchema(false).Required, "confirmationToken") {
		t.Fatal("execute schema must NOT require confirmationToken in auto-write mode")
	}
	if e.Properties["command"].Type != "array" || e.Properties["command"].Types != nil {
		t.Fatal("command must be forced to plain array type")
	}
	if e.Properties["patch"].Type != "array" || e.Properties["patch"].Types != nil {
		t.Fatal("patch must be forced to plain array type")
	}
	if it := e.Properties["patch"].Items; it != nil && it.Type == "integer" {
		t.Fatal("patch items must not constrain to integer — that rejects real JSON patches")
	}
	if it := p.Properties["patch"].Items; it != nil && it.Type == "integer" {
		t.Fatal("plan patch items must not constrain to integer — that rejects real JSON patches")
	}
	d := DiagnoseInputSchema()
	if len(d.Properties["target"].Enum) != len(DiagnoseTargets) {
		t.Fatal("diagnose schema target enum mismatch")
	}
}

func TestPatchSchemaSerializesNoBooleanSubschema(t *testing.T) {
	for name, s := range map[string]*jsonschema.Schema{
		"plan":    PlanInputSchema(),
		"execute": ExecuteInputSchema(false),
	} {
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		items := m["properties"].(map[string]any)["patch"].(map[string]any)["items"]
		if b, ok := items.(bool); ok {
			t.Fatalf("%s: patch.items serialized as boolean %v — strict upstreams (Volcano Engine Ark 11133, codebuddyCN 400001) reject boolean subschemas", name, b)
		}
	}
}
