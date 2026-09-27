# Tool Consolidation Implementation Plan (43 → 7 Tools)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse the MCP tool surface from 43 tools to 7 (5 in read-only mode) by introducing enum-dispatched merged tools (`rancherQuery`, `diagnose`, `planChange`, `executeChange`) while keeping every existing handler method and the confirmation gate unchanged.

**Architecture:** A new leaf package `pkg/toolsets/dispatch` holds the flat params structs, the generic `Case[P]`/`CallHandler[P]` types, a reflection-based required-field validator, and the four hand-built input schemas. Each existing toolset package gains a `merged*.go` file exposing its capabilities as case maps (`QueryCases()`, `DiagnoseCases()`, `PlanCases()`, `ExecuteCases()`); closures inside the defining package translate flat params to the existing unexported params structs and call the existing handler methods verbatim. A new package `pkg/toolsets/merged` unions the maps and registers the 7 tools. Old per-tool `AddTools` registrations are deleted. The `confirm.Gate` is untouched: `Operation.Tool` simply carries the operation name.

**Tech Stack:** Go, github.com/modelcontextprotocol/go-sdk v1.7.0, github.com/google/jsonschema-go v0.4.3.

**Spec:** `docs/superpowers/specs/2026-09-27-tool-consolidation-design.md`

## Global Constraints

- Do not modify any existing tool handler method (e.g. `getProject`, `createKubernetesResource`, `execPod`). Only the registration layer changes.
- No new module dependencies. Use only packages already in go.mod.
- Full mode registers exactly 7 tools; `--read-only` exactly 5; `--enable-exec` does not change tool count (execPod is an enum value gated at runtime).
- The k8s-generic tools keep their current names unchanged: `getKubernetesResource`, `listKubernetesResources`, `listAPIResources`.
- Handlers already pass the correct auto-write `bypass` argument to `Gate.Check` (`false` for delete/exec, `cfg.AutoWrite` for create/update class). This behavior must be preserved by reusing the handlers unchanged — do NOT build a separate exemption table.
- `gofmt`-clean, `go vet ./...` clean, `go test ./...` green after every task.
- Commit after every task with a conventional commit message.

---

### Task 1: dispatch package — params, cases, validation, schemas

**Files:**
- Create: `pkg/toolsets/dispatch/params.go`
- Create: `pkg/toolsets/dispatch/case.go`
- Create: `pkg/toolsets/dispatch/validate.go`
- Create: `pkg/toolsets/dispatch/schemas.go`
- Test: `pkg/toolsets/dispatch/dispatch_test.go`

**Interfaces:**
- Produces (used by every later task):
  - `dispatch.QueryParams`, `dispatch.DiagnoseParams`, `dispatch.ChangeParams` — flat merged params structs
  - `dispatch.K3kSync`, `dispatch.K3kPersistence`, `dispatch.K3kLimits` — nested structs for the createK3kCluster operation
  - `dispatch.Case[P]{Required []string; Handler CallHandler[P]}`
  - `dispatch.CallHandler[P] func(ctx context.Context, req *mcp.CallToolRequest, p P) (*mcp.CallToolResult, any, error)`
  - `dispatch.MergeMaps[P any](maps ...map[string]Case[P]) map[string]Case[P]` — panics on duplicate key
  - `dispatch.Dispatch[P any](ctx, req, discriminator, key string, p P, cases map[string]Case[P]) (*mcp.CallToolResult, any, error)`
  - `dispatch.QueryResources`, `dispatch.DiagnoseTargets`, `dispatch.ChangeOperations []string`
  - `dispatch.QueryInputSchema()`, `dispatch.DiagnoseInputSchema()`, `dispatch.PlanInputSchema()`, `dispatch.ExecuteInputSchema() *jsonschema.Schema`

- [ ] **Step 1: Write the failing test**

```go
// pkg/toolsets/dispatch/dispatch_test.go
package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

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
		"b": {Handler: func(context.Context, *mcp.CallToolRequest, fakeParams) (*mcp.CallToolResult, any, error) { return nil, nil, nil }},
		"a": {Handler: func(context.Context, *mcp.CallToolRequest, fakeParams) (*mcp.CallToolResult, any, error) { return nil, nil, nil }},
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
	c := Case[fakeParams]{Handler: func(context.Context, *mcp.CallToolRequest, fakeParams) (*mcp.CallToolResult, any, error) { return nil, nil, nil }}
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
	e := ExecuteInputSchema()
	found := false
	for _, r := range e.Required {
		if r == "confirmationToken" {
			found = true
		}
	}
	if !found {
		t.Fatal("execute schema must require confirmationToken")
	}
	if e.Properties["command"].Type != "array" || e.Properties["command"].Types != nil {
		t.Fatal("command must be forced to plain array type")
	}
	if e.Properties["patch"].Type != "array" || e.Properties["patch"].Types != nil {
		t.Fatal("patch must be forced to plain array type")
	}
	d := DiagnoseInputSchema()
	if len(d.Properties["target"].Enum) != len(DiagnoseTargets) {
		t.Fatal("diagnose schema target enum mismatch")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolsets/dispatch/ -v 2>&1 | head -5`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement the package**

`pkg/toolsets/dispatch/params.go`:

```go
// Package dispatch defines the shared types of the merged (enum-dispatched)
// tools: rancherQuery, diagnose, planChange and executeChange. It is a leaf
// package: it must never import any toolset package.
package dispatch

import "encoding/json"

// QueryParams is the flat parameter set of the rancherQuery tool. Only
// Resource is schema-required; every other field is required only for the
// resource values listed in its description and enforced at runtime by
// Validate, which returns an error naming the missing fields.
type QueryParams struct {
	Resource     string   `json:"resource" jsonschema:"required. Which Rancher resource to query: project|projects|resourceUsage|clusters|user|roleTemplate|roleTemplates|clusterRTBs|projectRTBs|gitRepo|gitRepos|bundle|clusterImages|k3kClusters|clusterMachine|supportedVersions"`
	Cluster      string   `json:"cluster,omitempty" jsonschema:"cluster name or ID. Required by: project, projects, resourceUsage, clusterRTBs, projectRTBs, clusterMachine"`
	Name         string   `json:"name,omitempty" jsonschema:"object identifier. Required by: project (project name), user (username), roleTemplate, gitRepo, bundle, clusterMachine (machine name)"`
	Workspace    string   `json:"workspace,omitempty" jsonschema:"Fleet workspace. Required by: gitRepo, gitRepos, bundle"`
	Namespace    string   `json:"namespace,omitempty" jsonschema:"namespace filter. Optional for: resourceUsage"`
	Project      string   `json:"project,omitempty" jsonschema:"project filter. Optional for: resourceUsage (name or ID), projectRTBs (project ID)"`
	User         string   `json:"user,omitempty" jsonschema:"user ID filter. Optional for: clusterRTBs, projectRTBs"`
	Group        string   `json:"group,omitempty" jsonschema:"group filter. Optional for: clusterRTBs, projectRTBs"`
	Clusters     []string `json:"clusters,omitempty" jsonschema:"cluster name filter list. Optional for: clusterImages, k3kClusters (empty = all clusters)"`
	Distribution string   `json:"distribution,omitempty" jsonschema:"kubernetes distribution: rke2 or k3s. Required by: supportedVersions"`
}

// DiagnoseParams is the flat parameter set of the diagnose tool.
type DiagnoseParams struct {
	Target    string `json:"target" jsonschema:"required. What to diagnose: cluster|machines|nodes|fleet|deployment|pod"`
	Cluster   string `json:"cluster,omitempty" jsonschema:"cluster name or ID. Required by: cluster, machines, nodes, deployment, pod"`
	Namespace string `json:"namespace,omitempty" jsonschema:"namespace. Optional for: cluster, machines. Required by: deployment, pod"`
	Name      string `json:"name,omitempty" jsonschema:"object name. Required by: deployment, pod"`
	Workspace string `json:"workspace,omitempty" jsonschema:"Fleet workspace. Required by: fleet"`
}

// K3kSync mirrors provisioning.SyncConfig for the createK3kCluster operation.
type K3kSync struct {
	PriorityClasses bool `json:"priorityClasses,omitempty" jsonschema:"sync priorityClasses"`
	Ingresses       bool `json:"ingresses,omitempty" jsonschema:"sync ingresses"`
}

// K3kPersistence mirrors provisioning.PersistenceConfig.
type K3kPersistence struct {
	Type             string `json:"type,omitempty" jsonschema:"persistence type, e.g. pvc or ephemeral"`
	StorageClassName string `json:"storageClassName,omitempty" jsonschema:"storage class to use for the PVC"`
	StorageRequest   string `json:"storageRequest,omitempty" jsonschema:"storage request size, e.g. 5Gi"`
}

// K3kLimits mirrors provisioning.ResourceLimits.
type K3kLimits struct {
	CPU    string `json:"cpu,omitempty" jsonschema:"CPU limit, e.g. 1 or 500m"`
	Memory string `json:"memory,omitempty" jsonschema:"memory limit, e.g. 2Gi or 512Mi"`
}

// ChangeParams is the flat parameter set of the planChange and executeChange
// tools. Only Operation is schema-required (plus ConfirmationToken for
// executeChange); per-operation requirements are enforced by Validate.
type ChangeParams struct {
	Operation         string `json:"operation" jsonschema:"required. Which change: createKubernetesResource|patchKubernetesResource|deleteKubernetesResource|scaleClusterNodePool|execPod|createProject|createCustomCluster|createImportedCluster|createK3kCluster"`
	Cluster           string `json:"cluster,omitempty" jsonschema:"cluster name or ID. Required by all operations except createCustomCluster, createImportedCluster"`
	Namespace         string `json:"namespace,omitempty" jsonschema:"namespace (empty for cluster-wide resources). Required by: scaleClusterNodePool, execPod; optional for: createKubernetesResource, patchKubernetesResource, deleteKubernetesResource, createK3kCluster (k3k namespace)"`
	Name              string `json:"name,omitempty" jsonschema:"object name. Required by every operation except none; for execPod it is the pod name"`
	Description       string `json:"description,omitempty" jsonschema:"optional human description. Used by: createProject, createCustomCluster, createImportedCluster"`
	ConfirmationToken string `json:"confirmationToken,omitempty" jsonschema:"REQUIRED by executeChange (unless the server runs in auto-write mode): the single-use confirmationToken returned by planChange for THIS exact operation and parameters. Never invent, reuse, or guess a token"`
	// k8s-generic operations
	Kind       string          `json:"kind,omitempty" jsonschema:"Kubernetes resource kind (custom resources supported). Required by: createKubernetesResource, patchKubernetesResource, deleteKubernetesResource"`
	APIVersion string          `json:"apiVersion,omitempty" jsonschema:"optional API group and version (e.g. harvesterhci.io/v1beta1) to disambiguate custom resources"`
	Manifest   string          `json:"manifest,omitempty" jsonschema:"complete Kubernetes manifest in YAML or JSON. Required by: createKubernetesResource"`
	Patch      json.RawMessage `json:"patch,omitempty" jsonschema:"RFC 6902 JSON patch array. Required by: patchKubernetesResource. Example: [{\"op\":\"replace\",\"path\":\"/spec/replicas\",\"value\":3}]"`
	// provisioning cluster operations
	CNI                      string `json:"CNI,omitempty" jsonschema:"CNI to use. Required by: createCustomCluster"`
	Version                 string `json:"version,omitempty" jsonschema:"rke2/k3s version. Required by: createCustomCluster; optional for: createK3kCluster"`
	Distribution            string `json:"distribution,omitempty" jsonschema:"rke2 or k3s. Required by: createCustomCluster"`
	VersionManagementSetting string `json:"VersionManagementSetting,omitempty" jsonschema:"version management setting: system-default, true or false. Optional for: createImportedCluster"`
	TargetCluster           string `json:"targetCluster,omitempty" jsonschema:"downstream cluster hosting the K3k cluster. Required by: createK3kCluster"`
	Mode                    string `json:"mode,omitempty" jsonschema:"k3k mode: shared or virtual. Optional for: createK3kCluster"`
	Servers                 int32  `json:"servers,omitempty" jsonschema:"number of k3k server (control plane) nodes. Optional for: createK3kCluster"`
	Agents                  int32  `json:"agents,omitempty" jsonschema:"number of k3k agent (worker) nodes. Optional for: createK3kCluster"`
	Sync                    K3kSync        `json:"sync,omitempty" jsonschema:"k3k shared-mode sync options. Optional for: createK3kCluster"`
	Persistence             K3kPersistence `json:"persistence,omitempty" jsonschema:"k3k etcd persistence. Optional for: createK3kCluster"`
	ServerLimit             K3kLimits      `json:"serverLimit,omitempty" jsonschema:"k3k server resource limits. Optional for: createK3kCluster"`
	WorkerLimit             K3kLimits      `json:"workerLimit,omitempty" jsonschema:"k3k worker resource limits. Optional for: createK3kCluster"`
	// createProject quotas
	DisplayName       string `json:"displayName,omitempty" jsonschema:"project display name. Optional for: createProject"`
	CPULimit          int    `json:"cpuLimit,omitempty" jsonschema:"max CPU (mCPUs) for containers in the project. Optional for: createProject"`
	CPUReservation    int    `json:"cpuReservation,omitempty" jsonschema:"reserved CPU (mCPUs). Optional for: createProject"`
	MemoryLimit       int    `json:"memoryLimit,omitempty" jsonschema:"max memory (MiB). Optional for: createProject"`
	MemoryReservation int    `json:"memoryReservation,omitempty" jsonschema:"reserved memory (MiB). Optional for: createProject"`
	// scaleClusterNodePool
	NodePoolName     string `json:"nodePoolName,omitempty" jsonschema:"the node pool to scale. Required by: scaleClusterNodePool"`
	DesiredSize      int    `json:"desiredSize,omitempty" jsonschema:"target pool size; ignored when amountToAdd/amountToSubtract is set. Optional for: scaleClusterNodePool"`
	AmountToAdd      int    `json:"amountToAdd,omitempty" jsonschema:"nodes to add. Optional for: scaleClusterNodePool"`
	AmountToSubtract int    `json:"amountToSubtract,omitempty" jsonschema:"nodes to remove. Optional for: scaleClusterNodePool"`
	// execPod
	Container string   `json:"container,omitempty" jsonschema:"container to execute in. Optional for: execPod (defaults to the first container)"`
	Command   []string `json:"command,omitempty" jsonschema:"argv array to execute, e.g. [\"ls\",\"-la\"]. Required by: execPod. Never wrap in a shell unless the user explicitly asked"`
}
```

`pkg/toolsets/dispatch/case.go`:

```go
package dispatch

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CallHandler is the uniform shape of an enum-dispatched tool call.
type CallHandler[P any] func(ctx context.Context, req *mcp.CallToolRequest, p P) (*mcp.CallToolResult, any, error)

// Case binds one enum value to its required fields and its handler.
type Case[P any] struct {
	// Required lists json field names that must be non-zero for this case.
	Required []string
	Handler  CallHandler[P]
}

// MergeMaps unions case maps and panics on duplicate keys: a duplicate enum
// value would silently shadow one handler, which must be a build-time failure.
func MergeMaps[P any](maps ...map[string]Case[P]) map[string]Case[P] {
	out := make(map[string]Case[P])
	for _, m := range maps {
		for k, v := range m {
			if _, dup := out[k]; dup {
				panic(fmt.Sprintf("dispatch: duplicate case key %q", k))
			}
			out[k] = v
		}
	}
	return out
}

// Dispatch validates the discriminator value and required fields, then runs
// the case handler.
func Dispatch[P any](ctx context.Context, req *mcp.CallToolRequest, discriminator, key string, p P, cases map[string]Case[P]) (*mcp.CallToolResult, any, error) {
	c, ok := cases[key]
	if !ok {
		keys := make([]string, 0, len(cases))
		for k := range cases {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, nil, fmt.Errorf("unknown %s %q; valid values: %s", discriminator, key, strings.Join(keys, ", "))
	}
	if err := Validate(discriminator, key, p, c.Required); err != nil {
		return nil, nil, err
	}
	return c.Handler(ctx, req, p)
}
```

`pkg/toolsets/dispatch/validate.go`:

```go
package dispatch

import (
	"fmt"
	"reflect"
	"strings"
)

// Validate checks that every json field name in required is non-zero in p.
// The error names the discriminator value so the caller can self-correct.
func Validate[P any](discriminator, value string, p P, required []string) error {
	if len(required) == 0 {
		return nil
	}
	rt := reflect.TypeOf(p)
	rv := reflect.ValueOf(p)
	byJSONName := make(map[string]int, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		byJSONName[name] = i
	}
	var missing []string
	for _, name := range required {
		idx, ok := byJSONName[name]
		if !ok {
			return fmt.Errorf("dispatch: unknown required field %q (programming error)", name)
		}
		if rv.Field(idx).IsZero() {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s=%q is missing required parameter(s): %s", discriminator, value, strings.Join(missing, ", "))
	}
	return nil
}
```

`pkg/toolsets/dispatch/schemas.go`:

```go
package dispatch

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// The enum sources of truth. The schemas are built from these lists, and the
// merged registration test asserts the runtime case maps have exactly these
// keys.
var (
	QueryResources = []string{
		"project", "projects", "resourceUsage", "clusters",
		"user", "roleTemplate", "roleTemplates", "clusterRTBs", "projectRTBs",
		"gitRepo", "gitRepos", "bundle",
		"clusterImages", "k3kClusters", "clusterMachine", "supportedVersions",
	}
	DiagnoseTargets = []string{"cluster", "machines", "nodes", "fleet", "deployment", "pod"}
	ChangeOperations = []string{
		"createKubernetesResource", "patchKubernetesResource", "deleteKubernetesResource",
		"scaleClusterNodePool", "execPod",
		"createProject", "createCustomCluster", "createImportedCluster", "createK3kCluster",
	}
)

// forcePlainType collapses a nullable multi-type property (["null","array"])
// into a single type so strict agent clients (Gemini/Vertex) accept the schema.
// Same trick as the existing patchResourceInputSchema.
func forcePlainType(s *jsonschema.Schema, prop, typ string) {
	if p, ok := s.Properties[prop]; ok {
		p.Type = typ
		p.Types = nil
	}
}

func enumOf(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func mustSchema[P any](s *jsonschema.Schema, err error) *jsonschema.Schema {
	if err != nil {
		panic(fmt.Errorf("dispatch: building input schema: %w", err))
	}
	return s
}

// QueryInputSchema builds the rancherQuery input schema.
func QueryInputSchema() *jsonschema.Schema {
	s := mustSchema[QueryParams](jsonschema.For[QueryParams](nil))
	s.Properties["resource"].Enum = enumOf(QueryResources)
	forcePlainType(s, "clusters", "array")
	return s
}

// DiagnoseInputSchema builds the diagnose input schema.
func DiagnoseInputSchema() *jsonschema.Schema {
	s := mustSchema[DiagnoseParams](jsonschema.For[DiagnoseParams](nil))
	s.Properties["target"].Enum = enumOf(DiagnoseTargets)
	return s
}

// PlanInputSchema builds the planChange input schema (no token required).
func PlanInputSchema() *jsonschema.Schema {
	s := mustSchema[ChangeParams](jsonschema.For[ChangeParams](nil))
	s.Properties["operation"].Enum = enumOf(ChangeOperations)
	forcePlainType(s, "command", "array")
	forcePlainType(s, "patch", "array")
	return s
}

// ExecuteInputSchema builds the executeChange input schema: the plan schema
// plus a required confirmationToken.
func ExecuteInputSchema() *jsonschema.Schema {
	s := mustSchema[ChangeParams](jsonschema.For[ChangeParams](nil))
	s.Properties["operation"].Enum = enumOf(ChangeOperations)
	forcePlainType(s, "command", "array")
	forcePlainType(s, "patch", "array")
	s.Required = append(s.Required, "confirmationToken")
	return s
}
```

Note: verify `jsonschema.Schema.Enum` is `[]any` (it is in v0.4.3, field `Enum []any json:"enum,omitempty"`). If `mustSchema`'s generic param is awkward, drop it — the signature only needs `func mustSchema(s *jsonschema.Schema, err error) *jsonschema.Schema`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/toolsets/dispatch/ -v`
Expected: PASS. If `sync`/`persistence`/`serverLimit`/`workerLimit` properties come out as `Types: ["null","object"]`, add `forcePlainType(s, "sync", "object")` etc. to both plan and execute schema builders.

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets/dispatch
git commit -m "feat(dispatch): flat params, case dispatch, validation and merged input schemas"
```

---

### Task 2: core query + diagnose cases

**Files:**
- Create: `pkg/toolsets/core/merged_cases.go`
- Test: `pkg/toolsets/core/merged_cases_test.go`

**Interfaces:**
- Consumes: `dispatch.QueryParams/DiagnoseParams/Case` (Task 1); existing handlers `t.listClusters(struct{})`, `t.getClusterImages(getClusterImagesParams)`, `t.getNodes(getNodesParams)`, `t.getDeploymentDetails(specificResourceParams)`, `t.inspectPod(specificResourceParams)` — all in package core.
- Produces: `func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams]` and `func (t *Tools) DiagnoseCases() map[string]dispatch.Case[dispatch.DiagnoseParams]` on core `*Tools`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/toolsets/core/merged_cases_test.go
package core

import (
	"testing"

	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

func TestQueryCasesKeys(t *testing.T) {
	tools := NewTools(nil, testCfg()) // reuse the cfg helper used by existing core tests; if none, toolconfig.Config{ReadOnly: true}
	cases := tools.QueryCases()
	for _, want := range []string{"clusters", "clusterImages"} {
		if _, ok := cases[want]; !ok {
			t.Errorf("missing query case %q", want)
		}
	}
	if len(cases) != 2 {
		t.Errorf("core owns exactly 2 query cases, got %d", len(cases))
	}
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

var _ = dispatch.QueryParams{} // keep import if unused above
```

(If existing core tests define a config helper under another name, use it; otherwise add `func testCfg() toolconfig.Config { return toolconfig.Config{ReadOnly: true} }` in this file. `NewTools(nil, cfg)` is safe here because the tests only inspect the maps, never invoke handlers.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolsets/core/ -run 'TestQueryCasesKeys|TestDiagnoseCasesKeys' -v`
Expected: FAIL — `QueryCases` undefined.

- [ ] **Step 3: Implement**

```go
// pkg/toolsets/core/merged_cases.go
package core

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// QueryCases returns core's slice of the rancherQuery dispatch table.
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return map[string]dispatch.Case[dispatch.QueryParams]{
		"clusters": {
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listClusters(ctx, req, struct{}{})
			},
		},
		"clusterImages": {
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getClusterImages(ctx, req, getClusterImagesParams{Clusters: p.Clusters})
			},
		},
	}
}

// DiagnoseCases returns core's slice of the diagnose dispatch table.
func (t *Tools) DiagnoseCases() map[string]dispatch.Case[dispatch.DiagnoseParams] {
	return map[string]dispatch.Case[dispatch.DiagnoseParams]{
		"nodes": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.getNodes(ctx, req, getNodesParams{Cluster: p.Cluster})
			},
		},
		"deployment": {
			Required: []string{"cluster", "namespace", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.getDeploymentDetails(ctx, req, specificResourceParams{Cluster: p.Cluster, Namespace: p.Namespace, Name: p.Name})
			},
		},
		"pod": {
			Required: []string{"cluster", "namespace", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.inspectPod(ctx, req, specificResourceParams{Cluster: p.Cluster, Namespace: p.Namespace, Name: p.Name})
			},
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/toolsets/core/ -run 'TestQueryCasesKeys|TestDiagnoseCasesKeys' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets/core/merged_cases.go pkg/toolsets/core/merged_cases_test.go
git commit -m "feat(core): query/diagnose case tables for merged tools"
```

---

### Task 3: projects cases (query + plan + execute)

**Files:**
- Create: `pkg/toolsets/core/projects/merged_cases.go`
- Test: `pkg/toolsets/core/projects/merged_cases_test.go`

**Interfaces:**
- Consumes: existing `t.getProject(getProjectParams)`, `t.listProjects(listProjectsParams)`, `t.getResourceUsage(getResourceUsageParams)`, `t.createProjectPlan(createProjectParams)`, `t.createProject(createProjectParams)`; `createProjectParams{Cluster, Name, Description, DisplayName, CPULimit, CPUReservation, MemoryLimit, MemoryReservation, ConfirmationToken}`.
- Produces: `func (t *Tools) QueryCases()`, `func (t *Tools) PlanCases()`, `func (t *Tools) ExecuteCases()` on projects `*Tools`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/toolsets/core/projects/merged_cases_test.go
package projects

import (
	"iter"
	"maps"
	"slices"
	"testing"
)

func TestMergedCaseKeys(t *testing.T) {
	tools := NewTools(nil, testCfg()) // projects tests already build a cfg with a fake gate — reuse that helper (fakeGates/testCfg in existing test files)
	if got := maps.Keys(tools.QueryCases()); !equalKeys(got, "project", "projects", "resourceUsage") {
		t.Errorf("query cases = %v", got)
	}
	if got := maps.Keys(tools.PlanCases()); !equalKeys(got, "createProject") {
		t.Errorf("plan cases = %v", got)
	}
	if got := maps.Keys(tools.ExecuteCases()); !equalKeys(got, "createProject") {
		t.Errorf("execute cases = %v", got)
	}
	if !slices.Equal(tools.PlanCases()["createProject"].Required, []string{"cluster", "name"}) {
		t.Errorf("createProject required = %v", tools.PlanCases()["createProject"].Required)
	}
}

func equalKeys(got iter.Seq[string], want ...string) bool {
	var g []string
	for k := range got {
		g = append(g, k)
	}
	slices.Sort(g)
	slices.Sort(want)
	return slices.Equal(g, want)
}
```

(Adjust to the actual cfg helper in existing projects tests — `fakeGates` exists in create_project_test.go; `NewTools(nil, cfg)` must not be called with a nil gate for plan/execute handlers, but these tests only inspect map keys.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolsets/core/projects/ -run TestMergedCaseKeys -v`
Expected: FAIL — `QueryCases` undefined.

- [ ] **Step 3: Implement**

```go
// pkg/toolsets/core/projects/merged_cases.go
package projects

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// QueryCases returns projects' slice of the rancherQuery dispatch table.
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return map[string]dispatch.Case[dispatch.QueryParams]{
		"project": {
			Required: []string{"cluster", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getProject(ctx, req, getProjectParams{Name: p.Name, Cluster: p.Cluster})
			},
		},
		"projects": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listProjects(ctx, req, listProjectsParams{Cluster: p.Cluster})
			},
		},
		"resourceUsage": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getResourceUsage(ctx, req, getResourceUsageParams{Cluster: p.Cluster, Project: p.Project, Namespace: p.Namespace})
			},
		},
	}
}

// changeParams maps flat merged params to createProjectParams.
func changeParams(p dispatch.ChangeParams) createProjectParams {
	return createProjectParams{
		Cluster: p.Cluster, Name: p.Name,
		Description: p.Description, DisplayName: p.DisplayName,
		CPULimit: p.CPULimit, CPUReservation: p.CPUReservation,
		MemoryLimit: p.MemoryLimit, MemoryReservation: p.MemoryReservation,
		ConfirmationToken: p.ConfirmationToken,
	}
}

// PlanCases returns projects' slice of the planChange dispatch table.
func (t *Tools) PlanCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createProject": {
			Required: []string{"cluster", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createProjectPlan(ctx, req, changeParams(p))
			},
		},
	}
}

// ExecuteCases returns projects' slice of the executeChange dispatch table.
func (t *Tools) ExecuteCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createProject": {
			Required: []string{"cluster", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createProject(ctx, req, changeParams(p))
			},
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/toolsets/core/projects/ -run TestMergedCaseKeys -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets/core/projects/merged_cases.go pkg/toolsets/core/projects/merged_cases_test.go
git commit -m "feat(projects): query/plan/execute case tables for merged tools"
```

---

### Task 4: rbac cases

**Files:**
- Create: `pkg/toolsets/core/rbac/merged_cases.go`
- Test: `pkg/toolsets/core/rbac/merged_cases_test.go`

**Interfaces:**
- Consumes: `t.getUser(getUserParams{Username})`, `t.getRoleTemplate(getRoleTemplateParams{Name})`, `t.listRoleTemplates(struct{})`, `t.listClusterRoleTemplateBindings(listCRTBParams{Cluster, User, Group})`, `t.listProjectRoleTemplateBindings(listPRTBParams{Cluster, ProjectID, User, Group})`.
- Produces: `func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams]`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/toolsets/core/rbac/merged_cases_test.go
package rbac

import "testing"

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolsets/core/rbac/ -run TestQueryCasesKeys -v`
Expected: FAIL — `QueryCases` undefined.

- [ ] **Step 3: Implement**

```go
// pkg/toolsets/core/rbac/merged_cases.go
package rbac

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// QueryCases returns rbac's slice of the rancherQuery dispatch table.
// Field mapping notes: the merged "name" parameter carries the username for
// "user"; the merged "project" parameter carries the project ID for
// "projectRTBs".
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return map[string]dispatch.Case[dispatch.QueryParams]{
		"user": {
			Required: []string{"name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getUser(ctx, req, getUserParams{Username: p.Name})
			},
		},
		"roleTemplate": {
			Required: []string{"name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getRoleTemplate(ctx, req, getRoleTemplateParams{Name: p.Name})
			},
		},
		"roleTemplates": {
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listRoleTemplates(ctx, req, struct{}{})
			},
		},
		"clusterRTBs": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listClusterRoleTemplateBindings(ctx, req, listCRTBParams{Cluster: p.Cluster, User: p.User, Group: p.Group})
			},
		},
		"projectRTBs": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listProjectRoleTemplateBindings(ctx, req, listPRTBParams{Cluster: p.Cluster, ProjectID: p.Project, User: p.User, Group: p.Group})
			},
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/toolsets/core/rbac/ -run TestQueryCasesKeys -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets/core/rbac/merged_cases.go pkg/toolsets/core/rbac/merged_cases_test.go
git commit -m "feat(rbac): query case table for merged tools"
```

---

### Task 5: fleet cases

**Files:**
- Create: `pkg/toolsets/fleet/merged_cases.go`
- Test: `pkg/toolsets/fleet/merged_cases_test.go`

**Interfaces:**
- Consumes: `t.getGitRepo(getGitRepoParams{Name, Workspace})`, `t.listGitRepos(listGitRepoParams{Workspace})`, `t.getBundle(getBundleParams{Name, Workspace})`, `t.analyzeFleetResources(analyzeFleetResourcesParams{Workspace})`.
- Produces: `func (t *Tools) QueryCases()` and `func (t *Tools) DiagnoseCases()`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/toolsets/fleet/merged_cases_test.go
package fleet

import "testing"

func TestMergedCaseKeys(t *testing.T) {
	tools := NewTools(nil) // fleet.NewTools signature: check fleet/tools.go — it takes only the client
	q := tools.QueryCases()
	for _, k := range []string{"gitRepo", "gitRepos", "bundle"} {
		if _, ok := q[k]; !ok {
			t.Errorf("missing query case %q", k)
		}
	}
	if got := q["gitRepo"].Required; len(got) != 2 || got[0] != "workspace" || got[1] != "name" {
		t.Errorf("gitRepo required = %v, want [workspace name]", got)
	}
	d := tools.DiagnoseCases()
	if len(d) != 1 {
		t.Fatalf("fleet owns exactly 1 diagnose case, got %d", len(d))
	}
	if got := d["fleet"].Required; len(got) != 1 || got[0] != "workspace" {
		t.Errorf("fleet required = %v, want [workspace]", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolsets/fleet/ -run TestMergedCaseKeys -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// pkg/toolsets/fleet/merged_cases.go
package fleet

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// QueryCases returns fleet's slice of the rancherQuery dispatch table.
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return map[string]dispatch.Case[dispatch.QueryParams]{
		"gitRepo": {
			Required: []string{"workspace", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getGitRepo(ctx, req, getGitRepoParams{Name: p.Name, Workspace: p.Workspace})
			},
		},
		"gitRepos": {
			Required: []string{"workspace"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listGitRepos(ctx, req, listGitRepoParams{Workspace: p.Workspace})
			},
		},
		"bundle": {
			Required: []string{"workspace", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getBundle(ctx, req, getBundleParams{Name: p.Name, Workspace: p.Workspace})
			},
		},
	}
}

// DiagnoseCases returns fleet's slice of the diagnose dispatch table.
func (t *Tools) DiagnoseCases() map[string]dispatch.Case[dispatch.DiagnoseParams] {
	return map[string]dispatch.Case[dispatch.DiagnoseParams]{
		"fleet": {
			Required: []string{"workspace"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.analyzeFleetResources(ctx, req, analyzeFleetResourcesParams{Workspace: p.Workspace})
			},
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/toolsets/fleet/ -run TestMergedCaseKeys -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets/fleet/merged_cases.go pkg/toolsets/fleet/merged_cases_test.go
git commit -m "feat(fleet): query/diagnose case tables for merged tools"
```

---

### Task 6: provisioning cases (query + diagnose + plan + execute)

**Files:**
- Create: `pkg/toolsets/provisioning/merged_cases.go`
- Test: `pkg/toolsets/provisioning/merged_cases_test.go`

**Interfaces:**
- Consumes: `t.getClusterMachine(getClusterMachineParams{Cluster, MachineName})`, `t.getK3kClusters(getK3kClustersParams{Clusters})`, `t.listSupportedKubernetesVersions(listSupportedK8sVersionsParams{Distribution})`, `t.analyzeCluster(inspectClusterParams{Cluster, Namespace})`, `t.analyzeClusterMachines(inspectClusterMachinesParams{Cluster, Namespace})`, `t.createCustomClusterPlan/createCustomCluster(createCustomClusterParams)`, `t.createImportedClusterPlan/createImportedCluster(createImportedClusterParams)`, `t.createK3kClusterPlan/createK3kCluster(createK3kClusterParams)`, `t.scaleClusterNodePoolPlan/scaleClusterNodePool(scaleNodePoolParameters)`; exported nested types `provisioning.SyncConfig`, `provisioning.ResourceLimits`, `provisioning.PersistenceConfig`.
- Produces: `QueryCases()`, `DiagnoseCases()`, `PlanCases()`, `ExecuteCases()` on provisioning `*Tools`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/toolsets/provisioning/merged_cases_test.go
package provisioning

import "testing"

func TestMergedCaseKeys(t *testing.T) {
	tools := NewTools(nil, testCfg()) // reuse the cfg helper from existing provisioning tests
	if got := len(tools.QueryCases()); got != 3 {
		t.Errorf("provisioning owns 3 query cases, got %d", got)
	}
	for _, k := range []string{"clusterMachine", "k3kClusters", "supportedVersions"} {
		if _, ok := tools.QueryCases()[k]; !ok {
			t.Errorf("missing query case %q", k)
		}
	}
	if got := len(tools.DiagnoseCases()); got != 2 {
		t.Errorf("provisioning owns 2 diagnose cases, got %d", got)
	}
	plan := tools.PlanCases()
	exec := tools.ExecuteCases()
	for _, k := range []string{"createCustomCluster", "createImportedCluster", "createK3kCluster", "scaleClusterNodePool"} {
		if _, ok := plan[k]; !ok {
			t.Errorf("missing plan case %q", k)
		}
		if _, ok := exec[k]; !ok {
			t.Errorf("missing execute case %q", k)
		}
	}
	if got := plan["createCustomCluster"].Required; len(got) != 4 {
		t.Errorf("createCustomCluster required = %v, want 4 entries [name CNI version distribution]", got)
	}
	if got := plan["scaleClusterNodePool"].Required; len(got) != 3 || got[0] != "cluster" || got[1] != "namespace" || got[2] != "nodePoolName" {
		t.Errorf("scaleClusterNodePool required = %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolsets/provisioning/ -run TestMergedCaseKeys -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// pkg/toolsets/provisioning/merged_cases.go
package provisioning

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// QueryCases returns provisioning's slice of the rancherQuery dispatch table.
// Field mapping note: the merged "name" parameter carries the machine name
// for "clusterMachine".
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return map[string]dispatch.Case[dispatch.QueryParams]{
		"clusterMachine": {
			Required: []string{"cluster", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getClusterMachine(ctx, req, getClusterMachineParams{Cluster: p.Cluster, MachineName: p.Name})
			},
		},
		"k3kClusters": {
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.getK3kClusters(ctx, req, getK3kClustersParams{Clusters: p.Clusters})
			},
		},
		"supportedVersions": {
			Required: []string{"distribution"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
				return t.listSupportedKubernetesVersions(ctx, req, listSupportedK8sVersionsParams{Distribution: p.Distribution})
			},
		},
	}
}

// DiagnoseCases returns provisioning's slice of the diagnose dispatch table.
func (t *Tools) DiagnoseCases() map[string]dispatch.Case[dispatch.DiagnoseParams] {
	return map[string]dispatch.Case[dispatch.DiagnoseParams]{
		"cluster": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.analyzeCluster(ctx, req, inspectClusterParams{Cluster: p.Cluster, Namespace: p.Namespace})
			},
		},
		"machines": {
			Required: []string{"cluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
				return t.analyzeClusterMachines(ctx, req, inspectClusterMachinesParams{Cluster: p.Cluster, Namespace: p.Namespace})
			},
		},
	}
}

// customClusterParams maps flat merged params to createCustomClusterParams.
func customClusterParams(p dispatch.ChangeParams) createCustomClusterParams {
	return createCustomClusterParams{
		Name: p.Name, Description: p.Description,
		CNI: p.CNI, Version: p.Version, Distribution: p.Distribution,
		ConfirmationToken: p.ConfirmationToken,
	}
}

func importedClusterParams(p dispatch.ChangeParams) createImportedClusterParams {
	return createImportedClusterParams{
		Name: p.Name, Description: p.Description,
		VersionManagementSetting: p.VersionManagementSetting,
		ConfirmationToken:        p.ConfirmationToken,
	}
}

func k3kClusterParams(p dispatch.ChangeParams) createK3kClusterParams {
	return createK3kClusterParams{
		Name: p.Name, Namespace: p.Namespace, TargetCluster: p.TargetCluster,
		Version: p.Version, Mode: p.Mode, Servers: p.Servers, Agents: p.Agents,
		Sync: SyncConfig{PriorityClasses: p.Sync.PriorityClasses, Ingresses: p.Sync.Ingresses},
		Persistence: PersistenceConfig{Type: p.Persistence.Type, StorageClassName: p.Persistence.StorageClassName, StorageRequest: p.Persistence.StorageRequest},
		ServerLimit: ResourceLimits{CPU: p.ServerLimit.CPU, Memory: p.ServerLimit.Memory},
		WorkerLimit: ResourceLimits{CPU: p.WorkerLimit.CPU, Memory: p.WorkerLimit.Memory},
		ConfirmationToken: p.ConfirmationToken,
	}
}

func scaleParams(p dispatch.ChangeParams) scaleNodePoolParameters {
	return scaleNodePoolParameters{
		Cluster: p.Cluster, Namespace: p.Namespace, NodePoolName: p.NodePoolName,
		DesiredSize: p.DesiredSize, AmountToAdd: p.AmountToAdd, AmountToSubtract: p.AmountToSubtract,
		ConfirmationToken: p.ConfirmationToken,
	}
}

// PlanCases returns provisioning's slice of the planChange dispatch table.
func (t *Tools) PlanCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createCustomCluster": {
			Required: []string{"name", "CNI", "version", "distribution"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createCustomClusterPlan(ctx, req, customClusterParams(p))
			},
		},
		"createImportedCluster": {
			Required: []string{"name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createImportedClusterPlan(ctx, req, importedClusterParams(p))
			},
		},
		"createK3kCluster": {
			Required: []string{"name", "namespace", "targetCluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createK3kClusterPlan(ctx, req, k3kClusterParams(p))
			},
		},
		"scaleClusterNodePool": {
			Required: []string{"cluster", "namespace", "nodePoolName"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.scaleClusterNodePoolPlan(ctx, req, scaleParams(p))
			},
		},
	}
}

// ExecuteCases returns provisioning's slice of the executeChange dispatch table.
func (t *Tools) ExecuteCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createCustomCluster": {
			Required: []string{"name", "CNI", "version", "distribution"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createCustomCluster(ctx, req, customClusterParams(p))
			},
		},
		"createImportedCluster": {
			Required: []string{"name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createImportedCluster(ctx, req, importedClusterParams(p))
			},
		},
		"createK3kCluster": {
			Required: []string{"name", "namespace", "targetCluster"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createK3kCluster(ctx, req, k3kClusterParams(p))
			},
		},
		"scaleClusterNodePool": {
			Required: []string{"cluster", "namespace", "nodePoolName"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.scaleClusterNodePool(ctx, req, scaleParams(p))
			},
		},
	}
}
```

Note: `createK3kClusterParams` carries the field `Persistence PersistenceConfig` (verified in `create_k3k_cluster.go`), so the converter above maps `dispatch.K3kPersistence` → `PersistenceConfig` field-by-field.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/toolsets/provisioning/ -run TestMergedCaseKeys -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets/provisioning/merged_cases.go pkg/toolsets/provisioning/merged_cases_test.go
git commit -m "feat(provisioning): full case tables for merged tools"
```

---

### Task 7: core plan/execute cases (incl. execPod gating and patch conversion)

**Files:**
- Create: `pkg/toolsets/core/merged_change_cases.go`
- Test: `pkg/toolsets/core/merged_change_cases_test.go`

**Interfaces:**
- Consumes: `t.createKubernetesResourcePlan/createKubernetesResource(createKubernetesResourceParams)`, `t.updateKubernetesResourcePlan/updateKubernetesResource(updateKubernetesResourceParams{Patch: jsonPatchList})`, `t.deleteKubernetesResourcePlan/deleteKubernetesResource(deleteKubernetesResourceParams)`, `t.execPodPlan/execPod(execPodParams)`; `dispatch.ChangeParams`; `t.cfg.EnableExec`.
- Produces: `func (t *Tools) PlanCases()` and `func (t *Tools) ExecuteCases()` on core `*Tools`.

**Key behaviors to test:**
- The `execPod` case exists in both maps regardless of `--enable-exec`, but its handler returns an error naming the flag when disabled.
- Cross-operation token rejection: a token issued for `createProject` must be rejected by `executeChange(patchKubernetesResource)` — this falls out of the unchanged handlers (`Gate.RequireToken` compares `Operation.Tool`), so the test issues a token via the real `Gate.IssueToken` with `Tool: "createProject"` and calls the patch execute case handler with it.

- [ ] **Step 1: Write the failing test**

```go
// pkg/toolsets/core/merged_change_cases_test.go
package core

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

func changeCfg(t *testing.T, enableExec bool) toolconfig.Config {
	t.Helper()
	gate, err := confirm.NewGate()
	if err != nil {
		t.Fatal(err)
	}
	// existing core tests wrap the gate with fakes that avoid real elicitation;
	// for these tests only token issuance/validation paths are exercised.
	return toolconfig.Config{Gate: gate, EnableExec: enableExec}
}

func TestPlanExecuteCaseKeys(t *testing.T) {
	tools := NewTools(nil, changeCfg(t, false))
	want := []string{"createKubernetesResource", "patchKubernetesResource", "deleteKubernetesResource", "execPod"}
	if len(tools.PlanCases()) != len(want) || len(tools.ExecuteCases()) != len(want) {
		t.Fatalf("core owns %d plan and %d execute cases", len(tools.PlanCases()), len(tools.ExecuteCases()))
	}
	for _, k := range want {
		if _, ok := tools.PlanCases()[k]; !ok {
			t.Errorf("missing plan case %q", k)
		}
		if _, ok := tools.ExecuteCases()[k]; !ok {
			t.Errorf("missing execute case %q", k)
		}
	}
}

func TestExecPodCaseGatedOnEnableExec(t *testing.T) {
	tools := NewTools(nil, changeCfg(t, false))
	c := tools.PlanCases()["execPod"]
	_, _, err := c.Handler(context.Background(), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "execPod", Cluster: "c", Namespace: "default", Name: "p", Command: []string{"true"},
	})
	if err == nil || !strings.Contains(err.Error(), "--enable-exec") {
		t.Fatalf("expected --enable-exec error, got %v", err)
	}
}

func TestPatchExecuteRejectsForeignToken(t *testing.T) {
	cfg := changeCfg(t, false)
	tools := NewTools(nil, cfg)
	token, err := cfg.Gate.IssueToken(confirm.Operation{Tool: "createProject", Cluster: "c", Kind: "project", Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	c := tools.ExecuteCases()["patchKubernetesResource"]
	_, _, err = c.Handler(context.Background(), &mcp.CallToolRequest{}, dispatch.ChangeParams{
		Operation: "patchKubernetesResource", Cluster: "c", Kind: "deployment", Namespace: "default", Name: "d",
		Patch:             json.RawMessage(`[{"op":"replace","path":"/spec/replicas","value":2}]`),
		ConfirmationToken: token,
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "token") {
		t.Fatalf("expected token rejection, got %v", err)
	}
}
```

(The patch handler resolves the GVR before checking the token — check `updateKubernetesResource`: it calls `patchBytes()` then `ResolveGVR` then `Gate.Check`. With a nil client `ResolveGVR` will panic. Look at the existing `TestPatchTokenMismatchRejected` in patch_resource_test.go for the fake-client pattern it uses, and reuse that fake here. If the fake-client plumbing is heavy, assert instead that `patchBytes`-level conversion works and move the cross-token assertion to the merged registration test in Task 8 where the full in-memory server exists. Do not weaken the assertion silently — pick whichever form the existing fakes support.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolsets/core/ -run 'TestPlanExecuteCaseKeys|TestExecPodCaseGatedOnEnableExec|TestPatchExecuteRejectsForeignToken' -v`
Expected: FAIL — `PlanCases` undefined.

- [ ] **Step 3: Implement**

```go
// pkg/toolsets/core/merged_change_cases.go
package core

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// errExecDisabled is returned when the execPod operation is used but the
// server was not started with --enable-exec. The enum always lists the
// operation; availability is enforced here, at runtime.
var errExecDisabled = errors.New("operation execPod is disabled: the server must be started with --enable-exec")

// patchList converts the flat raw patch into the handler's jsonPatchList,
// which accepts both a JSON array and a stringified array.
func patchList(raw json.RawMessage) (jsonPatchList, error) {
	var pl jsonPatchList
	if err := json.Unmarshal(raw, &pl); err != nil {
		return nil, err
	}
	return pl, nil
}

// PlanCases returns core's slice of the planChange dispatch table.
func (t *Tools) PlanCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createKubernetesResource": {
			Required: []string{"cluster", "kind", "name", "manifest"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createKubernetesResourcePlan(ctx, req, createKubernetesResourceParams{
					Cluster: p.Cluster, Kind: p.Kind, Name: p.Name, Namespace: p.Namespace, Manifest: p.Manifest,
				})
			},
		},
		"patchKubernetesResource": {
			Required: []string{"cluster", "kind", "name", "patch"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				pl, err := patchList(p.Patch)
				if err != nil {
					return nil, nil, err
				}
				return t.updateKubernetesResourcePlan(ctx, req, updateKubernetesResourceParams{
					Cluster: p.Cluster, Kind: p.Kind, Name: p.Name, Namespace: p.Namespace, APIVersion: p.APIVersion, Patch: pl,
				})
			},
		},
		"deleteKubernetesResource": {
			Required: []string{"cluster", "kind", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.deleteKubernetesResourcePlan(ctx, req, deleteKubernetesResourceParams{
					Cluster: p.Cluster, Kind: p.Kind, Name: p.Name, Namespace: p.Namespace, APIVersion: p.APIVersion,
				})
			},
		},
		"execPod": {
			Required: []string{"cluster", "namespace", "name", "command"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				if !t.cfg.EnableExec {
					return nil, nil, errExecDisabled
				}
				return t.execPodPlan(ctx, req, execPodParams{
					Cluster: p.Cluster, Namespace: p.Namespace, Name: p.Name, Container: p.Container, Command: p.Command,
				})
			},
		},
	}
}

// ExecuteCases returns core's slice of the executeChange dispatch table.
// The handlers are the unchanged gated handlers: they validate the token
// against Operation{Tool: <operation name>} and run the user confirmation.
func (t *Tools) ExecuteCases() map[string]dispatch.Case[dispatch.ChangeParams] {
	return map[string]dispatch.Case[dispatch.ChangeParams]{
		"createKubernetesResource": {
			Required: []string{"cluster", "kind", "name", "manifest"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.createKubernetesResource(ctx, req, createKubernetesResourceParams{
					Cluster: p.Cluster, Kind: p.Kind, Name: p.Name, Namespace: p.Namespace, Manifest: p.Manifest,
					ConfirmationToken: p.ConfirmationToken,
				})
			},
		},
		"patchKubernetesResource": {
			Required: []string{"cluster", "kind", "name", "patch"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				pl, err := patchList(p.Patch)
				if err != nil {
					return nil, nil, err
				}
				return t.updateKubernetesResource(ctx, req, updateKubernetesResourceParams{
					Cluster: p.Cluster, Kind: p.Kind, Name: p.Name, Namespace: p.Namespace, APIVersion: p.APIVersion, Patch: pl,
					ConfirmationToken: p.ConfirmationToken,
				})
			},
		},
		"deleteKubernetesResource": {
			Required: []string{"cluster", "kind", "name"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				return t.deleteKubernetesResource(ctx, req, deleteKubernetesResourceParams{
					Cluster: p.Cluster, Kind: p.Kind, Name: p.Name, Namespace: p.Namespace, APIVersion: p.APIVersion,
					ConfirmationToken: p.ConfirmationToken,
				})
			},
		},
		"execPod": {
			Required: []string{"cluster", "namespace", "name", "command"},
			Handler: func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
				if !t.cfg.EnableExec {
					return nil, nil, errExecDisabled
				}
				return t.execPod(ctx, req, execPodParams{
					Cluster: p.Cluster, Namespace: p.Namespace, Name: p.Name, Container: p.Container, Command: p.Command,
					ConfirmationToken: p.ConfirmationToken,
				})
			},
		},
	}
}
```

Note: `createKubernetesResourceParams` has no `APIVersion` field (the manifest carries it) — check `create_resource.go` and only map fields that exist.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/toolsets/core/ -run 'TestPlanExecuteCaseKeys|TestExecPodCaseGatedOnEnableExec|TestPatchExecuteRejectsForeignToken' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets/core/merged_change_cases.go pkg/toolsets/core/merged_change_cases_test.go
git commit -m "feat(core): plan/execute case tables with execPod runtime gating"
```

---

### Task 8: merged package, registration swap, delete old AddTools

**Files:**
- Create: `pkg/toolsets/merged/tools.go`
- Test: `pkg/toolsets/merged/tools_test.go`
- Modify: `pkg/toolsets/toolsets.go` (delegate to merged)
- Modify: `pkg/toolsets/core/tools.go` (slim AddTools to the 3 k8s-generic tools, exported as `AddKubernetesTools`)
- Modify: `pkg/toolsets/core/projects/tools.go` (delete AddTools)
- Modify: `pkg/toolsets/core/rbac/tools.go` (delete AddTools)
- Modify: `pkg/toolsets/fleet/tools.go` (delete AddTools)
- Modify: `pkg/toolsets/provisioning/tools.go` (delete AddTools)
- Modify: registration tests that referenced the deleted methods — `pkg/toolsets/core/tools_test.go` (TestAddTools, TestAddToolsExecEnabled, TestPatchToolMetadata, TestDeleteToolMetadata, listRegisteredToolsInMemory), `pkg/toolsets/core/projects/tools_test.go`, `pkg/toolsets/core/rbac/tools_test.go`, `pkg/toolsets/fleet/tools_test.go`, `pkg/toolsets/provisioning/tools_test.go` — delete the per-package registration tests; their assertions are replaced by the merged invariants test below.

**Interfaces:**
- Consumes: all `QueryCases/DiagnoseCases/PlanCases/ExecuteCases` from Tasks 2–7; `dispatch.*` schemas.
- Produces: `func Register(client *client.Client, mcpServer *mcp.Server, cfg toolconfig.Config)` in package merged; core keeps `func (t *Tools) AddKubernetesTools(mcpServer *mcp.Server)` registering exactly `getKubernetesResource`, `listKubernetesResources`, `listAPIResources`.

- [ ] **Step 1: Write the failing test**

```go
// pkg/toolsets/merged/tools_test.go
package merged

import (
	"context"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

func listTools(t *testing.T, cfg toolconfig.Config) []string {
	t.Helper()
	gate, err := confirm.NewGate()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Gate = gate
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	Register(&client.Client{}, server, cfg) // &client.Client{} is fine: registration never calls it
	ct, st := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	mc := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	sess, err := mc.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func TestRegisterFullModeExactly7(t *testing.T) {
	got := listTools(t, toolconfig.Config{EnableExec: true})
	want := []string{"diagnose", "executeChange", "getKubernetesResource", "listAPIResources", "listKubernetesResources", "planChange", "rancherQuery"}
	if !slices.Equal(got, want) {
		t.Fatalf("full mode tools = %v, want %v", got, want)
	}
}

func TestRegisterReadOnlyExactly5(t *testing.T) {
	got := listTools(t, toolconfig.Config{ReadOnly: true})
	want := []string{"diagnose", "getKubernetesResource", "listAPIResources", "listKubernetesResources", "rancherQuery"}
	if !slices.Equal(got, want) {
		t.Fatalf("read-only tools = %v, want %v", got, want)
	}
}

func TestCaseMapsMatchSchemaEnums(t *testing.T) {
	// Build the same maps Register builds and assert keys == schema enum lists.
	m := buildCaseMaps(&client.Client{}, toolconfig.Config{ReadOnly: false, EnableExec: false})
	assertKeys(t, "query", m.query, dispatch.QueryResources)
	assertKeys(t, "diagnose", m.diagnose, dispatch.DiagnoseTargets)
	assertKeys(t, "plan", m.plan, dispatch.ChangeOperations)
	assertKeys(t, "execute", m.execute, dispatch.ChangeOperations)
}

func assertKeys[P any](t *testing.T, name string, m map[string]dispatch.Case[P], want []string) {
	t.Helper()
	var got []string
	for k := range m {
		got = append(got, k)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s case keys = %v, want %v", name, got, want)
	}
}
```

(The in-memory list pattern already exists in the repo as `listRegisteredToolsInMemory` in `pkg/toolsets/core/tools_test.go` — adapt that code rather than inventing it; it shows the exact go-sdk v1.7.0 calls. If `Register` needs a real `*client.Client` that cannot be zero-valued, check how existing tests construct it — `pkg/client/test/wrapper.go` may help, or make `Register` accept the existing `toolsClient` interfaces. Decide by what compiles; the assertion targets stay the same.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolsets/merged/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

`pkg/toolsets/merged/tools.go`:

```go
// Package merged registers the consolidated tool surface: 3 k8s-generic
// tools plus the 4 enum-dispatched merged tools (rancherQuery, diagnose,
// planChange, executeChange). It is the only tool-registration entry point.
package merged

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/core"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/fleet"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/provisioning"
	"k8s.io/utils/ptr"
)

const toolsSetAnn = "toolset"

// caseMaps unions the per-toolset dispatch tables.
type caseMaps struct {
	query    map[string]dispatch.Case[dispatch.QueryParams]
	diagnose map[string]dispatch.Case[dispatch.DiagnoseParams]
	plan     map[string]dispatch.Case[dispatch.ChangeParams]
	execute  map[string]dispatch.Case[dispatch.ChangeParams]
}

func buildCaseMaps(c *client.Client, cfg toolconfig.Config) caseMaps {
	coreT := core.NewTools(c, cfg)
	fleetT := fleet.NewTools(c)
	provT := provisioning.NewTools(c, cfg)
	return caseMaps{
		query:    dispatch.MergeMaps(coreT.QueryCases(), fleetT.QueryCases(), provT.QueryCases()),
		diagnose: dispatch.MergeMaps(coreT.DiagnoseCases(), fleetT.DiagnoseCases(), provT.DiagnoseCases()),
		plan:     dispatch.MergeMaps(coreT.PlanCases(), provT.PlanCases()),
		execute:  dispatch.MergeMaps(coreT.ExecuteCases(), provT.ExecuteCases()),
	}
}
```

Wait — projects/rbac cases are missing here. core.Tools.QueryCases() only returns core's own 2. Fix the design: `core.Tools.QueryCases()` (Task 2) must union in projects + rbac. Adjust: in Task 2's `merged_cases.go`, change QueryCases to:

```go
func (t *Tools) QueryCases() map[string]dispatch.Case[dispatch.QueryParams] {
	return dispatch.MergeMaps(
		map[string]dispatch.Case[dispatch.QueryParams]{ /* core's own clusters + clusterImages */ },
		projects.NewTools(t.client, t.cfg).QueryCases(),
		rbac.NewTools(t.client, t.cfg.ReadOnly).QueryCases(),
	)
}
```

and correspondingly PlanCases/ExecuteCases (Task 7) union in `projects.NewTools(...).PlanCases()/ExecuteCases()`. **Implementer: apply this correction in Tasks 2 and 7** (the tests there assert core's own keys — update them to assert the unioned sets: query = clusters, clusterImages, project, projects, resourceUsage, user, roleTemplate, roleTemplates, clusterRTBs, projectRTBs; plan/execute add createProject).

The registration function:

```go
// Register registers the full consolidated surface: the 3 k8s-generic tools,
// rancherQuery and diagnose, and — unless read-only — planChange and
// executeChange.
func Register(c *client.Client, mcpServer *mcp.Server, cfg toolconfig.Config) {
	coreT := core.NewTools(c, cfg)
	coreT.AddKubernetesTools(mcpServer)
	cases := buildCaseMaps(c, cfg)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "rancherQuery",
		Meta:        map[string]any{toolsSetAnn: "merged"},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr.To(false)},
		InputSchema: dispatch.QueryInputSchema(),
		Description: `Read-only queries over Rancher API resources (for Kubernetes resources use getKubernetesResource/listKubernetesResources). The resource parameter selects what to query; each resource value has its own required parameters — see the field descriptions. A missing parameter produces an error naming it, retry with it set. resource=projects lists projects in a cluster; resource=project returns one project with its namespaces and members; resource=gitRepo/gitRepos/bundle read Fleet resources (need workspace); resource=clusterRTBs/projectRTBs list role template bindings (optional user/group/project filters); resource=clusterImages/k3kClusters optionally filter by the clusters list; resource=supportedVersions needs distribution (rke2|k3s); resource=resourceUsage returns CPU/memory usage for a cluster, project or namespace.`},
		func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.QueryParams) (*mcp.CallToolResult, any, error) {
			return dispatch.Dispatch(ctx, req, "resource", p.Resource, p, cases.query)
		},
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "diagnose",
		Meta:        map[string]any{toolsSetAnn: "merged"},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr.To(false)},
		InputSchema: dispatch.DiagnoseInputSchema(),
		Description: `Troubleshooting and inspection bundle. target selects the diagnostic: cluster (complete cluster configuration: provisioning/management cluster, CAPI machines, machine pools), machines (Machine/MachineSet/MachineDeployment summary), nodes (per-node CPU/memory utilization), fleet (Fleet GitRepo/Bundle/BundleDeployment diagnostics for a workspace), deployment (a Deployment and its pods), pod (everything about a pod: owner workload, CPU/memory, logs). Required parameters: cluster/machines/nodes need cluster (namespace optional for cluster/machines); fleet needs workspace; deployment/pod need cluster, namespace and name.`},
		func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.DiagnoseParams) (*mcp.CallToolResult, any, error) {
			return dispatch.Dispatch(ctx, req, "target", p.Target, p, cases.diagnose)
		},
	)

	if cfg.ReadOnly {
		return
	}

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "planChange",
		Meta:        map[string]any{toolsSetAnn: "merged"},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)},
		InputSchema: dispatch.PlanInputSchema(),
		Description: `SECURITY: This tool only PLANS a change; it changes nothing. It returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may executeChange be called with the same operation and parameters plus this token.

Plans one change selected by operation: createKubernetesResource (manifest in YAML or JSON), patchKubernetesResource (RFC 6902 JSON patch), deleteKubernetesResource (returns the resource that would be deleted), scaleClusterNodePool, execPod (only when the server runs with --enable-exec), createProject, createCustomCluster, createImportedCluster, createK3kCluster. Each operation has its own required parameters — see the field descriptions; a missing parameter produces an error naming it.`},
		func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
			return dispatch.Dispatch(ctx, req, "operation", p.Operation, p, cases.plan)
		},
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "executeChange",
		Meta:        map[string]any{toolsSetAnn: "merged"},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr.To(true), IdempotentHint: false, OpenWorldHint: ptr.To(false)},
		InputSchema: dispatch.ExecuteInputSchema(),
		Description: toolconfig.SecurityProtocol(cfg, `SECURITY: This tool CHANGES cluster state or EXECUTES a command in a pod. Protocol, no exceptions: (1) Call planChange first with the same operation and parameters and show the user the complete returned plan. (2) Obtain the user's EXPLICIT approval for THIS EXACT change. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over to any other call; never execute proactively or in batches. The deleteKubernetesResource and execPod operations ALWAYS require this protocol, even in auto-write mode; deleteKubernetesResource additionally asks the user to type the resource name.`) + `

Executes one change selected by operation (same values and required parameters as planChange). The confirmationToken binds the exact operation and parameters: reusing a token across operations or parameters fails.`},
		func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
			return dispatch.Dispatch(ctx, req, "operation", p.Operation, p, cases.execute)
		},
	)
}
```

Then:

- `pkg/toolsets/toolsets.go`: replace `AddAllTools` body with `merged.Register(client, mcpServer, cfg)`; delete the `toolsAdder` interface and `allToolSets`; delete now-unused imports. (`pkg/toolsets` keeps `instructions.go`.)
- `pkg/toolsets/core/tools.go`: rename `AddTools` → `AddKubernetesTools`, delete every registration except `getKubernetesResource`, `listKubernetesResources`, `listAPIResources`, and delete the `projects.NewTools(...).AddTools(...)` / `rbac.NewTools(...).AddTools(...)` calls at the end.
- Delete `AddTools` in `projects/tools.go`, `rbac/tools.go`, `fleet/tools.go`, `provisioning/tools.go`. Keep each package's `NewTools`/`Tools`/constants.
- Delete the per-package registration tests listed above (they test deleted methods). Keep all handler tests.

- [ ] **Step 4: Run tests**

Run: `go build ./... && go test ./pkg/toolsets/... ./pkg/toolsets/merged/ -v 2>&1 | tail -20`
Expected: build OK; merged tests PASS (7/5 invariants, enum-map match). Existing handler tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets
git commit -m "feat(merged): register consolidated 7-tool surface; remove per-tool AddTools"
```

---

### Task 9: server safety instructions rewrite

**Files:**
- Modify: `pkg/toolsets/instructions.go`
- Modify: `pkg/toolsets/instructions_test.go` (update goldens)

**Interfaces:**
- Consumes: `toolconfig.Config`.
- Produces: unchanged signature `SafetyInstructions(cfg toolconfig.Config) string`.

- [ ] **Step 1: Update the test first**

In `instructions_test.go`, the golden test compares `SafetyInstructions` output against a spec file (find the golden file under `pkg/toolsets/testdata/` or embedded — locate it via `grep -rn "SAFETY RULES" pkg/toolsets/`). Update the golden to the new text from Step 3, keeping the existing test mechanics (readOnly/strict/autoWrite + `--enable-exec` variants). Run to watch it fail.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/toolsets/ -run TestSafety -v` (use the actual test names)
Expected: FAIL — golden mismatch.

- [ ] **Step 3: Implement**

Rewrite `instructions.go`:

- Replace `writeTools` with:

```go
// writeOperations lists every mutating operation of planChange/executeChange,
// in the order the instruction templates reference them.
var writeOperations = []string{
	"createKubernetesResource",
	"patchKubernetesResource",
	"deleteKubernetesResource",
	"createProject",
	"createCustomCluster",
	"createImportedCluster",
	"createK3kCluster",
	"scaleClusterNodePool",
}
```

- `allWriteTools` → `allWriteOperations(cfg)` — same body, appends `"execPod"` when `cfg.EnableExec`.
- `readOnlyInstructions` — unchanged concept, new names:

```go
func readOnlyInstructions() string {
	return `SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:

1. The server is running in read-only mode (--read-only): it registers
   read-only tools ONLY (rancherQuery, diagnose, getKubernetesResource,
   listKubernetesResources, listAPIResources). planChange and executeChange
   are unregistered here, so no operation can change any state. Do not look
   for, invent or attempt any such operation.
2. Use the read-only tools to observe, inspect, list and explain cluster state.
   Prefer read-only tools whenever they can answer the question.
3. If the user asks for a change, tell them this server runs in read-only mode
   and cannot perform it.`
}
```

- `strictInstructions` — same 7-rule structure, rendered with `renderToolList(allWriteOperations(cfg), "1. planChange and executeChange MODIFY cluster")` hmm — the wrap helper expects a prefix ending where the list starts. Use prefix `"1. The planChange and executeChange tools (operation: "` and append `)\n   MODIFY cluster state...` — wait, the current code does `fmt.Fprintf(&b, "%s\n   MODIFY cluster state or EXECUTE...", tools)` where `tools` already ends with `)`. Keep that mechanism; only the prefix and the surrounding rule text change:

Rule 2–7 text: replace "a Write tool" → "executeChange", "the corresponding Plan tool" → "planChange with the same operation and parameters", "Write tools REQUIRE" → "executeChange REQUIRES", "call the Write tool with the token" → "call executeChange with the token", "Every single Write call" → "Every executeChange call", "Write tools are never for exploration" → "planChange/executeChange are never for exploration (use rancherQuery/diagnose)". Full new body:

```go
func strictInstructions(cfg toolconfig.Config) string {
	ops := renderToolList(allWriteOperations(cfg), "1. The planChange and executeChange tools (operation: ")

	var b strings.Builder
	b.WriteString("SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:\n\n")
	fmt.Fprintf(&b, `%s
   MODIFY cluster state or EXECUTE commands inside pods. They are DANGEROUS.

2. NEVER call executeChange unless the user has EXPLICITLY requested this
   exact operation AND you have shown them the full details (target cluster,
   namespace, resource kind and name, complete manifest / patch / command)
   AND they have clearly approved THIS SPECIFIC operation.

3. ALWAYS call planChange first with the same operation and parameters, and
   show the user the returned plan. executeChange REQUIRES the single-use
   confirmationToken from the matching planChange response. NEVER invent,
   guess, reuse, or bypass tokens.

4. After the user approves the plan, call executeChange with the token. The
   server will then ask the USER DIRECTLY to confirm (you will not see the
   question). NEVER try to answer, simulate, or skip that confirmation — you
   cannot, and any attempt is a critical security violation.

5. Approval NEVER carries over. Every executeChange call needs its own fresh
   plan and its own explicit user approval. NEVER batch, chain, loop, or
   automate change calls. NEVER execute "proactively" or "to be safe".

6. If the user declines or cancels, DO NOT retry. Report that nothing was
   executed. Never pressure the user into approving.

7. Prefer read-only tools (rancherQuery, diagnose, getKubernetesResource,
   listKubernetesResources, listAPIResources) whenever they can answer the
   question. planChange/executeChange are never for exploration.`, ops)

	return b.String()
}
```

Check `renderToolList`'s contract (prefix + `name,` items + closing `)`; width 77 counts the prefix) — the golden test then verifies the wrap. Keep the helper untouched.

- `autoWriteInstructions` — same adaptation: rule 1 prefix identical to strict; rule 2 lists the auto-write-exempt operations (all except deleteKubernetesResource/execPod) by name; the guardRule becomes:

```go
	guardRule := "3. The deleteKubernetesResource operation STILL REQUIRES the full\n" +
		"   protocol in ALL modes: planChange first, explicit user approval for the\n" +
		"   exact operation, confirmationToken, and a server-initiated user\n" +
		"   confirmation (typed resource name included)."
	if cfg.EnableExec {
		guardRule = "3. The deleteKubernetesResource and execPod operations STILL REQUIRE the\n" +
			"   full protocol in ALL modes: planChange first, explicit user approval\n" +
			"   for the exact operation, confirmationToken, and a server-initiated\n" +
			"   user confirmation (delete additionally requires the typed name)."
	}
```

and rule 2's parenthetical list of auto-write-exempt operations stays the same seven operation names, framed as "(operation: createKubernetesResource, patchKubernetesResource, ..., scaleClusterNodePool)".

- `pkg/toolconfig/descriptions.go`: `AutoWriteProtocol` mentions `deleteKubernetesResource and execPod` as tools — reword to "the deleteKubernetesResource and execPod OPERATIONS (executeChange) are NOT exempt...". Keep `SecurityProtocol` signature.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/toolsets/ -v`
Expected: PASS (updated goldens).

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets/instructions.go pkg/toolsets/instructions_test.go pkg/toolconfig/descriptions.go
git commit -m "feat(toolsets): rewrite safety instructions for planChange/executeChange protocol"
```

---

### Task 10: docs, TOOLS.md, final verification, baseline compare readiness

**Files:**
- Modify: `TOOLS.md` (regenerate)
- Modify: `README.md` (tool list references)
- Modify: `scripts/baseline/main.go` (add `capture -v2` using `mapsTo`, add `compare`)
- Modify: `docs/rancher-ai-mcp-fork-analysis.md` if it lists tools (check first)

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Regenerate TOOLS.md**

Run: `go generate ./...` (or `make generate` if that's the documented path — check Makefile)
Expected: TOOLS.md now lists exactly 7 tools (5 read-only + planChange/executeChange as Write). Verify with `grep -c '^| `' TOOLS.md` and `grep 'executeChange\|planChange\|rancherQuery\|diagnose' TOOLS.md`.

- [ ] **Step 2: Update README.md tool references**

Run: `grep -n "createKubernetesResource\|getProject\|43\|tool" README.md | head -30`
Replace any per-tool inventory with the 7-tool description; keep the safety-protocol section coherent with the new planChange/executeChange flow.

- [ ] **Step 3: Extend the baseline harness with v2 capture + compare**

In `scripts/baseline/main.go`:
- `capture -v2`: for each case in calls.json, call `mapsTo.tool` with `mapsTo.params` instead of `tool`/`params`; write to `.baseline/golden/v2/`.
- `compare`: for every case id present in both `v1/` and `v2/`, byte-compare the `.norm.json` files; print `OK <id>` / `DIFF <id>` and a summary. Known-expected DIFFs, printed but not counted as failures (hardcode this list with comments): `execPodPlan`, `execPod` (old server ran without `--enable-exec` → "unknown tool"; new returns the explicit `--enable-exec` error), and any case whose v1 norm is a TLS error from the KDM endpoint (`listSupportedKubernetesVersions`, `createCustomClusterPlan`) — these depend on the deployment's env, not the refactor.

- [ ] **Step 4: Full verification**

Run: `gofmt -l . && go vet ./... && go test ./... && go build ./...`
Expected: no output from gofmt, vet clean, all tests PASS, build OK.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "docs+test: regenerate TOOLS.md, update README, baseline v2/compare harness"
```

---

## Notes for the executor

- The baseline golden corpus for v1 already exists at `.baseline/golden/v1/` (43 cases, captured against the live server on 2026-09-27) with `scripts/baseline/calls.json` carrying the `mapsTo` mapping. Do not re-capture v1.
- Live post-deploy comparison is NOT part of this plan (it happens after the new server is deployed): run `go run ./scripts/baseline capture -v2` then `go run ./scripts/baseline compare`.
- The upstream merge (`rancher/rancher-ai-mcp`, 5 commits incl. `moveNamespace`) is a follow-up AFTER this lands; `moveNamespace` becomes a new enum value in the planChange/executeChange case maps plus the gate treatment — do not merge it during this refactor.
