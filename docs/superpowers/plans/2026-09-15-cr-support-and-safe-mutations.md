# 任意 CR 支持 + 写操作安全门控 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 rancher-ai-mcp 支持任意自定义 CR 的查询与修改(discovery 动态解析 GVR),并为所有写操作加上 server 端强制的逐条用户确认(Plan-token + elicitation),新增 delete/execPod 工具与 `--allow-auto-write`/`--enable-exec` flag,配套镜像构建 CI 与 Helm 交付方式。

**Architecture:** 新增 `pkg/confirm`(HMAC 单次 token + MCP elicitation 用户闸门)与 `pkg/client/resolve.go`(表优先 + discovery 兜底的 GVR 解析,按 clusterID 缓存 5 分钟);写工具统一走 `Gate.Check`;`pkg/toolconfig` 作为叶子配置包向各 toolset 传递 `ReadOnly/AutoWrite/EnableExec/Gate`。

**Tech Stack:** Go 1.26、`github.com/modelcontextprotocol/go-sdk` v1.7.0(`ServerOptions.Instructions`、`ToolAnnotations`、`ServerSession.Elicit`)、client-go v0.36.4(`discovery`、`tools/remotecommand`)、cobra、zap、stretchr/testify。

**Spec:** `docs/superpowers/specs/2026-09-15-cr-support-and-safe-mutations-design.md`(先读 spec 再执行;本计划对 spec 的两处实现性微调已在 spec 中同步:token 参数为 schema optional + 运行时强制;listAPIResources 不复用 paginator)

## Global Constraints

- 所有代码、标识符、日志、工具描述、Instructions 一律**英文**;提交信息英文。
- 工具描述与 Instructions 必须反复强调安全约束:任何写工具不得在未获用户对该次操作的显式批准时调用;**严禁再出现 `Don't ask for confirmation` 之类字样**。
- `confirmationToken` 参数统一为 `json:"confirmationToken,omitempty"`(schema optional),严格模式下 handler 运行时强制校验。
- 新工具的 `[]string`/slice 参数参照 `patchResourceInputSchema()` 用显式 `InputSchema` 强制 `type: "array"`(兼容 Gemini/Vertex 校验,见 PR #136 回归)。
- 每个任务结束 `go build ./... && go test ./pkg/...` 保持绿;不要动 fleet toolset。
- 发现与计划不符的代码现实时,停下来回报,不要擅自改设计。

---

### Task 1: pkg/confirm — token 签发/校验 + elicitation 闸门

**Files:**
- Create: `pkg/confirm/confirm.go`
- Test: `pkg/confirm/confirm_test.go`

**Interfaces:**
- Produces(后续所有写工具依赖):
  - `type Operation struct { Tool, Cluster, Namespace, Kind, Name string; Payload []byte }`
  - `type Gate struct { TokenTTL time.Duration; ElicitFunc func(ctx context.Context, ss *mcp.ServerSession, params *mcp.ElicitParams) (*mcp.ElicitResult, error); ... }`
  - `func NewGate() (*Gate, error)`
  - `func (g *Gate) IssueToken(op Operation) (string, error)`
  - `func (g *Gate) RequireToken(op Operation, token string) error`
  - `func (g *Gate) Confirm(ctx context.Context, ss *mcp.ServerSession, summary, typedName string) (approved bool, err error)`
  - `func (g *Gate) Check(ctx context.Context, ss *mcp.ServerSession, op Operation, token, summary, typedName string, bypass bool) (approved bool, err error)`
  - 哨兵错误:`ErrTokenInvalid / ErrTokenExpired / ErrTokenConsumed / ErrTokenMismatch / ErrConfirmationUnsupported`
  - `func CancelledResult() *mcp.CallToolResult`

- [ ] **Step 1: 写失败测试 `pkg/confirm/confirm_test.go`**

```go
package confirm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testOp() Operation {
	return Operation{Tool: "patchKubernetesResource", Cluster: "c-abc", Namespace: "default", Kind: "deployment", Name: "web", Payload: []byte(`[{"op":"replace"}]`)}
}

func TestTokenRoundTrip(t *testing.T) {
	g, err := NewGate()
	require.NoError(t, err)
	tok, err := g.IssueToken(testOp())
	require.NoError(t, err)
	require.NoError(t, g.RequireToken(testOp(), tok))
}

func TestTokenTampered(t *testing.T) {
	g, _ := NewGate()
	tok, _ := g.IssueToken(testOp())
	err := g.RequireToken(testOp(), tok[:len(tok)-2]+"xx")
	assert.ErrorIs(t, err, ErrTokenInvalid)
}

func TestTokenMismatchPayload(t *testing.T) {
	g, _ := NewGate()
	tok, _ := g.IssueToken(testOp())
	op := testOp()
	op.Payload = []byte(`[{"op":"add"}]`)
	assert.ErrorIs(t, g.RequireToken(op, tok), ErrTokenMismatch)
	op = testOp()
	op.Name = "other"
	assert.ErrorIs(t, g.RequireToken(op, tok), ErrTokenMismatch)
}

func TestTokenExpired(t *testing.T) {
	g, _ := NewGate()
	tok, _ := g.IssueToken(testOp())
	g.nowFn = func() time.Time { return time.Now().Add(11 * time.Minute) }
	assert.ErrorIs(t, g.RequireToken(testOp(), tok), ErrTokenExpired)
}

func TestTokenSingleUse(t *testing.T) {
	g, _ := NewGate()
	tok, _ := g.IssueToken(testOp())
	require.NoError(t, g.RequireToken(testOp(), tok))
	assert.ErrorIs(t, g.RequireToken(testOp(), tok), ErrTokenConsumed)
}

func TestConfirmTypedName(t *testing.T) {
	g, _ := NewGate()
	g.ElicitFunc = func(_ context.Context, _ *mcp.ServerSession, p *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		assert.Contains(t, p.Message, "web")
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmName": "web"}}, nil
	}
	ok, err := g.Confirm(context.Background(), nil, "delete deployment web", "web")
	require.NoError(t, err)
	assert.True(t, ok)

	g.ElicitFunc = func(_ context.Context, _ *mcp.ServerSession, _ *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirmName": "WRONG"}}, nil
	}
	ok, err = g.Confirm(context.Background(), nil, "delete deployment web", "web")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestConfirmDeclineAndCancel(t *testing.T) {
	g, _ := NewGate()
	for _, action := range []string{"decline", "cancel"} {
		g.ElicitFunc = func(_ context.Context, _ *mcp.ServerSession, _ *mcp.ElicitParams) (*mcp.ElicitResult, error) {
			return &mcp.ElicitResult{Action: action}, nil
		}
		ok, err := g.Confirm(context.Background(), nil, "patch x", "")
		require.NoError(t, err)
		assert.False(t, ok)
	}
}

func TestConfirmUnsupported(t *testing.T) {
	g, _ := NewGate()
	g.ElicitFunc = func(_ context.Context, _ *mcp.ServerSession, _ *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		return nil, errors.New("method not found")
	}
	_, err := g.Confirm(context.Background(), nil, "patch x", "")
	assert.ErrorIs(t, err, ErrConfirmationUnsupported)
	// default ElicitFunc with nil session must fail closed
	g2, _ := NewGate()
	_, err = g2.Confirm(context.Background(), nil, "patch x", "")
	assert.ErrorIs(t, err, ErrConfirmationUnsupported)
}

func TestCheckBypass(t *testing.T) {
	g, _ := NewGate()
	ok, err := g.Check(context.Background(), nil, testOp(), "", "summary", "", true)
	require.NoError(t, err)
	assert.True(t, ok)
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/confirm/...`
Expected: FAIL(`package github.com/rancher/rancher-ai-mcp/pkg/confirm: no Go files` 或 undefined)

- [ ] **Step 3: 实现 `pkg/confirm/confirm.go`**

```go
// Package confirm implements the server-side safety gate for mutating tools:
// single-use HMAC confirmation tokens binding an exact operation, and a
// server-initiated user confirmation via MCP elicitation.
package confirm

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
)

var (
	ErrTokenInvalid   = errors.New("invalid confirmation token")
	ErrTokenExpired   = errors.New("confirmation token expired, call the Plan tool again")
	ErrTokenConsumed  = errors.New("confirmation token already used, call the Plan tool again")
	ErrTokenMismatch  = errors.New("confirmation token does not match this operation, call the Plan tool again with these exact parameters")

	// ErrConfirmationUnsupported means the connected client cannot answer a
	// server-initiated user confirmation. The operation must NOT be executed.
	ErrConfirmationUnsupported = errors.New("the MCP client does not support interactive user confirmation (elicitation); refusing to execute the mutating operation")
)

// Operation uniquely identifies one exact mutating call a user must approve.
type Operation struct {
	Tool      string
	Cluster   string
	Namespace string
	Kind      string
	Name      string
	Payload   []byte // canonical payload (manifest JSON, patch JSON, command bytes); nil for delete
}

type tokenBody struct {
	Tool        string `json:"tool"`
	Cluster     string `json:"cluster"`
	Namespace   string `json:"namespace,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Name        string `json:"name,omitempty"`
	PayloadHash string `json:"payloadHash,omitempty"`
	Nonce       string `json:"nonce"`
	Expiry      int64  `json:"expiry"`
}

// Gate issues and validates confirmation tokens and asks the user for
// explicit confirmation through the MCP client.
type Gate struct {
	key      [32]byte
	mu       sync.Mutex
	consumed map[string]int64 // nonce -> expiry unix
	nowFn    func() time.Time

	// TokenTTL is how long an issued token stays valid. Default 10 minutes.
	TokenTTL time.Duration

	// ElicitFunc performs the actual elicitation. Replaceable in tests.
	// The default fails closed when the session is nil.
	ElicitFunc func(ctx context.Context, ss *mcp.ServerSession, params *mcp.ElicitParams) (*mcp.ElicitResult, error)
}

// NewGate creates a Gate with a fresh random process-local HMAC key.
func NewGate() (*Gate, error) {
	g := &Gate{
		consumed: map[string]int64{},
		nowFn:    time.Now,
		TokenTTL: 10 * time.Minute,
	}
	if _, err := rand.Read(g.key[:]); err != nil {
		return nil, fmt.Errorf("generating confirmation key: %w", err)
	}
	g.ElicitFunc = func(ctx context.Context, ss *mcp.ServerSession, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
		if ss == nil {
			return nil, errors.New("no MCP session available")
		}
		return ss.Elicit(ctx, params)
	}
	return g, nil
}

func payloadHash(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// IssueToken returns a single-use token binding the exact operation.
func (g *Gate) IssueToken(op Operation) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	body := tokenBody{
		Tool: op.Tool, Cluster: op.Cluster, Namespace: op.Namespace,
		Kind: op.Kind, Name: op.Name, PayloadHash: payloadHash(op.Payload),
		Nonce:  hex.EncodeToString(nonce),
		Expiry: g.nowFn().Add(g.TokenTTL).Unix(),
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshaling token: %w", err)
	}
	mac := hmac.New(sha256.New, g.key[:])
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// RequireToken validates the token against the exact operation and consumes
// it. A token is consumed even if the caller later aborts, forcing a fresh
// plan (and fresh user approval) for every execution.
func (g *Gate) RequireToken(op Operation, token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return fmt.Errorf("%w: malformed token", ErrTokenInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
	mac := hmac.New(sha256.New, g.key[:])
	mac.Write(raw)
	if subtle.ConstantTimeCompare(sig, mac.Sum(nil)) != 1 {
		return fmt.Errorf("%w: bad signature", ErrTokenInvalid)
	}
	var body tokenBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}
	if g.nowFn().Unix() > body.Expiry {
		return ErrTokenExpired
	}
	if body.Tool != op.Tool || body.Cluster != op.Cluster || body.Namespace != op.Namespace ||
		body.Kind != op.Kind || body.Name != op.Name || body.PayloadHash != payloadHash(op.Payload) {
		return ErrTokenMismatch
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, used := g.consumed[body.Nonce]; used {
		return ErrTokenConsumed
	}
	g.consumed[body.Nonce] = body.Expiry
	// opportunistic sweep of expired nonces
	now := g.nowFn().Unix()
	for n, exp := range g.consumed {
		if exp < now {
			delete(g.consumed, n)
		}
	}
	return nil
}

// Confirm asks the USER, through the MCP client, to approve the operation.
// typedName != "" requires the user to type that exact string (used for
// deletions). Returns (false, nil) when the user declines or cancels.
func (g *Gate) Confirm(ctx context.Context, ss *mcp.ServerSession, summary, typedName string) (bool, error) {
	var schema map[string]any
	if typedName != "" {
		schema = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"confirmName": map[string]any{
					"type":        "string",
					"description": fmt.Sprintf("Type the exact resource name %q to confirm this deletion", typedName),
				},
			},
			"required": []string{"confirmName"},
		}
	} else {
		schema = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"confirm": map[string]any{
					"type":        "string",
					"enum":        []string{"approve", "reject"},
					"description": "Select approve to execute the operation exactly as shown, or reject to cancel it",
				},
			},
			"required": []string{"confirm"},
		}
	}
	res, err := g.ElicitFunc(ctx, ss, &mcp.ElicitParams{Message: summary, RequestedSchema: schema})
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrConfirmationUnsupported, err)
	}
	if res == nil || res.Action != "accept" {
		return false, nil
	}
	if typedName != "" {
		name, _ := res.Content["confirmName"].(string)
		return name == typedName, nil
	}
	confirm, _ := res.Content["confirm"].(string)
	return confirm == "approve", nil
}

// Check runs the full gate for a mutating call: token validation followed by
// direct user confirmation. bypass is true only for create/update-class tools
// when the operator explicitly started the server with --allow-auto-write;
// delete and exec tools always pass bypass=false.
func (g *Gate) Check(ctx context.Context, ss *mcp.ServerSession, op Operation, token, summary, typedName string, bypass bool) (bool, error) {
	fields := []zap.Field{
		zap.String("tool", op.Tool), zap.String("cluster", op.Cluster),
		zap.String("namespace", op.Namespace), zap.String("kind", op.Kind), zap.String("name", op.Name),
	}
	if bypass {
		zap.L().Info("write operation executing WITHOUT user confirmation (auto-write mode)", fields...)
		return true, nil
	}
	if err := g.RequireToken(op, token); err != nil {
		zap.L().Info("write operation rejected by token gate", append(fields, zap.String("reason", err.Error()))...)
		return false, err
	}
	approved, err := g.Confirm(ctx, ss, summary, typedName)
	if err != nil {
		zap.L().Info("write operation rejected: user confirmation unavailable", append(fields, zap.String("reason", err.Error()))...)
		return false, err
	}
	zap.L().Info("write operation user decision", append(fields, zap.Bool("approved", approved))...)
	return approved, nil
}

// CancelledResult is the standard tool result when the user declines or
// cancels the confirmation: a normal result so the agent reports it cleanly.
func CancelledResult() *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "Operation cancelled by the user. Nothing was executed."}},
	}
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./pkg/confirm/... -v`
Expected: PASS(9 个测试)

- [ ] **Step 5: Commit**

```bash
git add pkg/confirm/
git commit -m "feat(confirm): add plan-token gate and elicitation user confirmation"
```

---

### Task 2: pkg/client — ResolveGVR + discovery 缓存 + ListAPIResources

**Files:**
- Create: `pkg/client/resolve.go`
- Test: `pkg/client/resolve_test.go`

**Interfaces:**
- Consumes: 现有 `Client.GetClusterID`、`Client.CreateClientSet`、`converter.K8sKindsToGVRs`
- Produces:
  - `func (c *Client) ResolveGVR(ctx context.Context, token, cluster, kind, apiVersion string) (schema.GroupVersionResource, error)`
  - `func (c *Client) ListAPIResources(ctx context.Context, token, cluster string) ([]*metav1.APIResourceList, error)`
  - `func resetDiscoveryCache()`(包内测试用)

- [ ] **Step 1: 写失败测试 `pkg/client/resolve_test.go`**

```go
package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func newClientWithDiscovery(t *testing.T, resources []*metav1.APIResourceList) *Client {
	t.Helper()
	resetDiscoveryCache()
	cs := fake.NewClientset()
	fd, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	require.True(t, ok)
	fd.Resources = resources
	return &Client{
		ClientSetCreator: func(*rest.Config) (kubernetes.Interface, error) { return cs, nil },
	}
}

var harvesterResources = []*metav1.APIResourceList{
	{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "pods", Kind: "Pod", Namespaced: true},
		{Name: "pods/status", Kind: "Pod", Namespaced: true}, // subresource must be skipped
	}},
	{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{
		{Name: "deployments", Kind: "Deployment", Namespaced: true},
	}},
	{GroupVersion: "harvesterhci.io/v1beta1", APIResources: []metav1.APIResource{
		{Name: "virtualmachines", Kind: "VirtualMachine", Namespaced: true},
	}},
	{GroupVersion: "kubevirt.io/v1", APIResources: []metav1.APIResource{
		{Name: "virtualmachines", Kind: "VirtualMachine", Namespaced: true},
	}},
}

func TestResolveFromHardcodedTable(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	gvr, err := c.ResolveGVR(context.Background(), "tok", "local", "Deployment", "")
	require.NoError(t, err)
	assert.Equal(t, "apps", gvr.Group)
	assert.Equal(t, "deployments", gvr.Resource)
}

func TestResolveWithAPIVersion(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	gvr, err := c.ResolveGVR(context.Background(), "tok", "local", "VirtualMachine", "harvesterhci.io/v1beta1")
	require.NoError(t, err)
	assert.Equal(t, "harvesterhci.io", gvr.Group)
	assert.Equal(t, "virtualmachines", gvr.Resource)
}

func TestResolveQualifiedKind(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	for _, kind := range []string{"harvesterhci.io/VirtualMachine", "VirtualMachine.harvesterhci.io"} {
		gvr, err := c.ResolveGVR(context.Background(), "tok", "local", kind, "")
		require.NoError(t, err, kind)
		assert.Equal(t, "harvesterhci.io", gvr.Group)
	}
}

func TestResolveAmbiguousKind(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	_, err := c.ResolveGVR(context.Background(), "tok", "local", "VirtualMachine", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "harvesterhci.io")
	assert.Contains(t, err.Error(), "kubevirt.io")
}

func TestResolveUnknownKind(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	_, err := c.ResolveGVR(context.Background(), "tok", "local", "NoSuchThing", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown kind")
	assert.Contains(t, err.Error(), "listAPIResources")
}

func TestFetchAPIResourcesToleratesPartialFailure(t *testing.T) {
	// ServerPreferredResources returns partial results plus
	// ErrGroupDiscoveryFailedError when some aggregated API groups are
	// unreachable; fetchAPIResources must keep the partial data. Pin the
	// behavior of the client-go helper the implementation relies on.
	partialErr := &discovery.ErrGroupDiscoveryFailedError{Groups: map[schema.GroupVersion]error{
		{Group: "broken.example.io", Version: "v1"}: errors.New("connection refused"),
	}}
	assert.True(t, discovery.IsGroupDiscoveryFailedError(partialErr))
	assert.False(t, discovery.IsGroupDiscoveryFailedError(errors.New("boom")))
}

func TestDiscoveryCacheTTL(t *testing.T) {
	c := newClientWithDiscovery(t, harvesterResources)
	// first call populates the cache
	_, err := c.ResolveGVR(context.Background(), "tok", "local", "VirtualMachine", "harvesterhci.io/v1beta1")
	require.NoError(t, err)
	// shrink the cached entry's expiry to the past; next resolution must refetch
	discoveryCache.Range(func(key, value any) bool {
		e := value.(discoveryEntry)
		e.expiry = time.Now().Add(-time.Second)
		discoveryCache.Store(key, e)
		return true
	})
	_, err = c.ResolveGVR(context.Background(), "tok", "local", "VirtualMachine", "harvesterhci.io/v1beta1")
	require.NoError(t, err)
}
```

> fake-discovery 注入方式(`ClientSetCreator`/`DynClientCreator` 的写法)以现有 `pkg/toolsets/core/inspect_pod_test.go` 中的模式为准。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/client/... -run TestResolve`
Expected: FAIL(undefined: ResolveGVR 等)

- [ ] **Step 3: 实现 `pkg/client/resolve.go`**

```go
package client

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rancher/rancher-ai-mcp/pkg/converter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

const discoveryCacheTTL = 5 * time.Minute

var errKindNotFound = errors.New("kind not found via discovery")

type discoveryEntry struct {
	lists  []*metav1.APIResourceList
	expiry time.Time
}

// discoveryCache maps clusterID -> discovered API resources. The data is
// cluster-level public API metadata; actual resource calls are still
// authorized per-request by Rancher RBAC via the caller's token.
var discoveryCache sync.Map

// resetDiscoveryCache clears the discovery cache. Used by tests.
func resetDiscoveryCache() {
	discoveryCache = sync.Map{}
}

// ListAPIResources returns all preferred API resources of the cluster
// (group/version/kind/resource), using a short-lived per-cluster cache.
func (c *Client) ListAPIResources(ctx context.Context, token, cluster string) ([]*metav1.APIResourceList, error) {
	return c.apiResources(ctx, token, cluster, false)
}

func (c *Client) apiResources(ctx context.Context, token, cluster string, forceRefresh bool) ([]*metav1.APIResourceList, error) {
	clusterID, err := c.GetClusterID(ctx, token, cluster)
	if err != nil {
		return nil, err
	}
	if !forceRefresh {
		if entry, ok := discoveryCache.Load(clusterID); ok {
			e := entry.(discoveryEntry)
			if time.Now().Before(e.expiry) {
				return e.lists, nil
			}
		}
	}
	lists, err := c.fetchAPIResources(ctx, token, clusterID)
	if err != nil {
		return nil, err
	}
	discoveryCache.Store(clusterID, discoveryEntry{lists: lists, expiry: time.Now().Add(discoveryCacheTTL)})
	return lists, nil
}

func (c *Client) fetchAPIResources(ctx context.Context, token, clusterID string) ([]*metav1.APIResourceList, error) {
	clientset, err := c.CreateClientSet(ctx, token, clusterID)
	if err != nil {
		return nil, err
	}
	lists, err := clientset.Discovery().ServerPreferredResources()
	if err != nil {
		// Some aggregated APIs may be unreachable; partial results are usable.
		if discovery.IsGroupDiscoveryFailedError(err) && len(lists) > 0 {
			return lists, nil
		}
		return nil, err
	}
	return lists, nil
}

// ResolveGVR resolves a kind (optionally disambiguated by apiVersion or a
// group-qualified kind) to a GVR. Resolution order:
//  1. explicit apiVersion (e.g. "harvesterhci.io/v1beta1")
//  2. the built-in kind table (backwards compatibility, incl. prefixed kinds)
//  3. group-qualified kind: "group/Kind" or "Kind.group"
//  4. full discovery scan (single match required; ambiguity is an error)
func (c *Client) ResolveGVR(ctx context.Context, token, cluster, kind, apiVersion string) (schema.GroupVersionResource, error) {
	gvr, err := c.resolveGVR(ctx, token, cluster, kind, apiVersion, false)
	if err != nil && errors.Is(err, errKindNotFound) {
		// The cache may be stale (CRD installed within the TTL); bust it once.
		gvr, err = c.resolveGVR(ctx, token, cluster, kind, apiVersion, true)
	}
	return gvr, err
}

func (c *Client) resolveGVR(ctx context.Context, token, cluster, kind, apiVersion string, forceRefresh bool) (schema.GroupVersionResource, error) {
	if strings.TrimSpace(kind) == "" {
		return schema.GroupVersionResource{}, fmt.Errorf("kind must not be empty")
	}

	if apiVersion != "" {
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return schema.GroupVersionResource{}, fmt.Errorf("invalid apiVersion %q: %w", apiVersion, err)
		}
		return c.resolveInGroupVersion(ctx, token, cluster, kind, gv, forceRefresh)
	}

	if gvr, ok := converter.K8sKindsToGVRs[strings.ToLower(kind)]; ok {
		return gvr, nil
	}

	if group, k, ok := splitQualifiedKind(kind); ok {
		return c.resolveInGroup(ctx, token, cluster, group, k, forceRefresh)
	}

	return c.resolveByDiscovery(ctx, token, cluster, kind, forceRefresh)
}

// splitQualifiedKind accepts "group/Kind" or "Kind.group".
func splitQualifiedKind(kind string) (group, k string, ok bool) {
	if i := strings.Index(kind, "/"); i > 0 && i < len(kind)-1 {
		return kind[:i], kind[i+1:], true
	}
	if i := strings.Index(kind, "."); i > 0 && i < len(kind)-1 {
		return kind[i+1:], kind[:i], true
	}
	return "", "", false
}

func isSubresource(ar metav1.APIResource) bool {
	return strings.Contains(ar.Name, "/")
}

func (c *Client) resolveInGroupVersion(ctx context.Context, token, cluster, kind string, gv schema.GroupVersion, forceRefresh bool) (schema.GroupVersionResource, error) {
	lists, err := c.apiResources(ctx, token, cluster, forceRefresh)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	var available []string
	for _, list := range lists {
		if list.GroupVersion != gv.String() {
			continue
		}
		for _, ar := range list.APIResources {
			if isSubresource(ar) {
				continue
			}
			available = append(available, ar.Kind)
			if strings.EqualFold(ar.Kind, kind) {
				return schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: ar.Name}, nil
			}
		}
	}
	if len(available) == 0 {
		return schema.GroupVersionResource{}, fmt.Errorf("apiVersion %q not served by cluster %s: %w", gv.String(), cluster, errKindNotFound)
	}
	sort.Strings(available)
	return schema.GroupVersionResource{}, fmt.Errorf("%w: kind %q not found in %s; available kinds: %s", errKindNotFound, kind, gv.String(), strings.Join(available, ", "))
}

func (c *Client) resolveInGroup(ctx context.Context, token, cluster, group, kind string, forceRefresh bool) (schema.GroupVersionResource, error) {
	lists, err := c.apiResources(ctx, token, cluster, forceRefresh)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	for _, list := range lists { // ServerPreferredResources is preference-ordered
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil || gv.Group != group {
			continue
		}
		for _, ar := range list.APIResources {
			if !isSubresource(ar) && strings.EqualFold(ar.Kind, kind) {
				return schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: ar.Name}, nil
			}
		}
	}
	return schema.GroupVersionResource{}, fmt.Errorf("%w: kind %q not found in group %q of cluster %s", errKindNotFound, kind, group, cluster)
}

func (c *Client) resolveByDiscovery(ctx context.Context, token, cluster, kind string, forceRefresh bool) (schema.GroupVersionResource, error) {
	lists, err := c.apiResources(ctx, token, cluster, forceRefresh)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	var matches []schema.GroupVersionResource
	seenGroups := map[string]bool{}
	for _, list := range lists {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil || seenGroups[gv.Group] {
			continue
		}
		for _, ar := range list.APIResources {
			if isSubresource(ar) || !strings.EqualFold(ar.Kind, kind) {
				continue
			}
			matches = append(matches, schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: ar.Name})
			seenGroups[gv.Group] = true // first (preferred) version per group wins
			break
		}
	}
	switch len(matches) {
	case 0:
		return schema.GroupVersionResource{}, fmt.Errorf("%w: unknown kind %q in cluster %s; call the listAPIResources tool to discover available resource types", errKindNotFound, kind, cluster)
	case 1:
		return matches[0], nil
	default:
		var candidates []string
		for _, m := range matches {
			candidates = append(candidates, m.String())
		}
		sort.Strings(candidates)
		return schema.GroupVersionResource{}, fmt.Errorf("kind %q is ambiguous in cluster %s, found in: %s; retry with an explicit apiVersion (e.g. %s/%s) or a group-qualified kind (e.g. %s/%s)", kind, cluster, strings.Join(candidates, ", "), matches[0].Group, matches[0].Version, matches[0].Group, kind)
	}
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./pkg/client/... -run TestResolve -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/client/resolve.go pkg/client/resolve_test.go
git commit -m "feat(client): add discovery-based GVR resolution for arbitrary CRs"
```

---

### Task 3: pkg/client — Get/List 接入 APIVersion

**Files:**
- Modify: `pkg/client/client.go`(GetParams/ListParams 加字段;GetResource/GetResources 走 ResolveGVR)
- Test: `pkg/client/client_test.go`(若无则新建)

**Interfaces:**
- Consumes: `ResolveGVR`(Task 2)
- Produces: `GetParams.APIVersion string`、`ListParams.APIVersion string`(Task 5 起的工具层使用)

- [ ] **Step 1: 写失败测试**

`pkg/client/client_test.go`:

```go
package client

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

var vmGVR = schema.GroupVersionResource{Group: "harvesterhci.io", Version: "v1beta1", Resource: "virtualmachines"}

func newCRClient(t *testing.T, objs ...runtime.Object) *Client {
	t.Helper()
	resetDiscoveryCache()
	cs := fake.NewClientset()
	cs.Discovery().(*fakediscovery.FakeDiscovery).Resources = []*metav1.APIResourceList{
		{GroupVersion: "harvesterhci.io/v1beta1", APIResources: []metav1.APIResource{
			{Name: "virtualmachines", Kind: "VirtualMachine", Namespaced: true},
		}},
	}
	scheme := runtime.NewScheme()
	// WithCustomListKinds registers the list kind so List calls on the fake
	// dynamic client work for the CR's GVR.
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{vmGVR: "VirtualMachineList"}, objs...)
	return &Client{
		ClientSetCreator: func(*rest.Config) (kubernetes.Interface, error) { return cs, nil },
		DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
}

func TestGetResourceCustomCR(t *testing.T) {
	vm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "harvesterhci.io/v1beta1", "kind": "VirtualMachine",
		"metadata": map[string]any{"name": "vm1", "namespace": "default"},
	}}
	c := newCRClient(t, vm)
	obj, err := c.GetResource(context.Background(), GetParams{Cluster: "local", Kind: "VirtualMachine", APIVersion: "harvesterhci.io/v1beta1", Namespace: "default", Name: "vm1", Token: "tok"})
	require.NoError(t, err)
	assert.Equal(t, "vm1", obj.GetName())
}

func TestGetResourcesCustomCRByDiscovery(t *testing.T) {
	vm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "harvesterhci.io/v1beta1", "kind": "VirtualMachine",
		"metadata": map[string]any{"name": "vm1", "namespace": "default"},
	}}
	c := newCRClient(t, vm)
	list, err := c.GetResources(context.Background(), ListParams{Cluster: "local", Kind: "virtualmachine.harvesterhci.io", Namespace: "default", Token: "tok"})
	require.NoError(t, err)
	assert.Len(t, list, 1)
}
```

> 实现时注意:测试需要断言 fake dynamic client 实际收到的请求 GVR;`dynamicfake` 的 tracker 可用 `dyn.Actions()` 检查 `GetActionImpl.GetResource()`。fake 注入模式以现有 `pkg/toolsets/core/inspect_pod_test.go` 为准。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/client/... -run TestGetResource`
Expected: FAIL(GetParams 无 APIVersion 字段)

- [ ] **Step 3: 实现**

`pkg/client/client.go` 中:

```go
type GetParams struct {
	Cluster    string
	Kind       string
	APIVersion string // Optional apiVersion (e.g. "harvesterhci.io/v1beta1") to disambiguate custom resources.
	Namespace  string
	Name       string
	Token      string
}

type ListParams struct {
	Cluster       string
	Kind          string
	APIVersion    string // Optional apiVersion (e.g. "harvesterhci.io/v1beta1") to disambiguate custom resources.
	Namespace     string
	Name          string
	Token         string
	LabelSelector string
	Limit         int64
}
```

`GetResource` / `GetResources` 的 GVR 查表行替换为:

```go
	gvr, err := c.ResolveGVR(ctx, params.Token, params.Cluster, params.Kind, params.APIVersion)
	if err != nil {
		return nil, err
	}
	resourceInterface, err := c.GetResourceInterface(ctx, params.Token, params.Namespace, params.Cluster, gvr)
```

(`GetResourceAtAnyAPIVersion`/`GetResourcesAtAnyAPIVersion` 保持不变,CAPI 专用。)

- [ ] **Step 4: 运行确认通过**

Run: `go build ./... && go test ./pkg/client/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/client/client.go pkg/client/client_test.go
git commit -m "feat(client): route Get/List through ResolveGVR with optional apiVersion"
```

---

### Task 4: pkg/toolconfig + 全 toolset 接线(签名统一)

**Files:**
- Create: `pkg/toolconfig/config.go`
- Modify: `pkg/toolsets/toolsets.go`、`pkg/toolsets/core/tools.go`、`pkg/toolsets/core/projects/tools.go`、`pkg/toolsets/core/rbac/tools.go`(仅签名)、`pkg/toolsets/provisioning/tools.go`、`internal/toolsdoc/main.go`
- Modify: 所有 `NewTools(client, readOnly)` 调用方与测试(`pkg/toolsets/core/tools_test.go`、`fake_client_test.go`、projects/provisioning 的测试工具文件)

**Interfaces:**
- Produces:
  - `toolconfig.Config{ReadOnly bool; AutoWrite bool; EnableExec bool; Gate *confirm.Gate}`
  - `func (c Config) GateOrDefault() *confirm.Gate`(nil 时新建,防御性)
  - `core.NewTools(client toolsClient, cfg toolconfig.Config) *Tools`(`Tools` 结构体新增字段 `cfg toolconfig.Config`)
  - `projects.NewTools(client toolsClient, cfg toolconfig.Config)`、`provisioning.NewTools(client toolsClient, cfg toolconfig.Config)`
  - `toolsets.AddAllTools(client *client.Client, mcpServer *mcp.Server, cfg toolconfig.Config)`
  - rbac 保持 `NewTools(client, readOnly bool)` 不变(无写工具)

- [ ] **Step 1: 创建 `pkg/toolconfig/config.go`**

```go
// Package toolconfig carries the server's tool registration and safety
// configuration to every toolset without import cycles.
package toolconfig

import "github.com/rancher/rancher-ai-mcp/pkg/confirm"

// Config controls which tools are registered and how mutating tools are gated.
type Config struct {
	// ReadOnly registers only read-only tools (highest precedence).
	ReadOnly bool
	// AutoWrite lets create/update-class tools execute without the
	// confirmation token and user confirmation. Delete and exec tools are
	// NEVER exempted. Enable only for trusted automation.
	AutoWrite bool
	// EnableExec registers the execPod tools (off by default).
	EnableExec bool
	// Gate is the confirmation gate shared by all mutating tools.
	Gate *confirm.Gate
}

// GateOrDefault returns the configured gate, creating a fresh one if nil so
// tool handlers can never hit a nil gate.
func (c Config) GateOrDefault() *confirm.Gate {
	if c.Gate != nil {
		return c.Gate
	}
	g, err := confirm.NewGate()
	if err != nil {
		panic(err) // crypto/rand failure is unrecoverable at startup
	}
	return g
}
```

- [ ] **Step 2: 改签名与接线**

`pkg/toolsets/toolsets.go`:

```go
// AddAllTools adds all available tools to the MCP server.
func AddAllTools(client *client.Client, mcpServer *mcp.Server, cfg toolconfig.Config) {
	for _, ta := range allToolSets(client, cfg) {
		ta.AddTools(mcpServer)
	}
}

func allToolSets(client *client.Client, cfg toolconfig.Config) []toolsAdder {
	return []toolsAdder{
		core.NewTools(client, cfg),
		fleet.NewTools(client),
		provisioning.NewTools(client, cfg),
	}
}
```

core `tools.go`:`Tools` 结构体改为 `{ client toolsClient; paginator utils.Paginator; cfg toolconfig.Config }`,`NewTools(client toolsClient, cfg toolconfig.Config)`,`ReadOnly` 引用改为 `t.cfg.ReadOnly`;projects 调用 `projects.NewTools(t.client, t.cfg)`,rbac 保持 `rbac.NewTools(t.client, t.cfg.ReadOnly)`。projects/provisioning 同样改造(`Tools` 加 `cfg toolconfig.Config` 字段,`ReadOnly` 引用改 `t.cfg.ReadOnly`)。

`internal/toolsdoc/main.go` 两处调用:

```go
readOnlyTools, err := listTools(ctx, toolconfig.Config{ReadOnly: true})
...
allTools, err := listTools(ctx, toolconfig.Config{EnableExec: true})
```

`listTools` 签名改为 `func listTools(ctx context.Context, cfg toolconfig.Config) ([]*mcp.Tool, error)`,内部 `toolsets.AddAllTools(nil, server, cfg)`。

- [ ] **Step 3: 修测试编译与计数**

全局搜索 `NewTools(` 更新所有调用点。`pkg/toolsets/core/tools_test.go`:

```go
tools := NewTools(c, toolconfig.Config{})            // TestAddTools
assert.Len(t, toolsResult.Tools, 24, ...)            // 21 + listAPIResources + delete 对(后续任务落地前此处先按当前任务进度递增,最终 24)

tools := NewTools(c, toolconfig.Config{ReadOnly: true}) // TestAddToolsReadOnly
assert.Len(t, toolsResult.Tools, 16, ...)            // 15 + listAPIResources
```

> 执行提示:本任务只改签名让编译恢复;计数断言在 Task 5/8/9 落地时同步更新(每个任务改到自己负责的工具时更新对应计数)。若选择一次性改完计数,注意任务间顺序依赖——按最终值 24/16 改并在 Task 9 结束前允许中间失败是不合格的;**正确做法:本任务保持 21/15 不变,Task 5 改为 22/16,Task 8 改为 24/16,Task 9 新增 EnableExec 用例 26**。

- [ ] **Step 4: 编译与全量测试**

Run: `go build ./... && go test ./...`
Expected: PASS(计数不变)

- [ ] **Step 5: Commit**

```bash
git add pkg/toolconfig/ pkg/toolsets/ internal/toolsdoc/
git commit -m "refactor(toolsets): thread toolconfig.Config through all toolsets"
```

---

### Task 5: core 只读工具 — apiVersion 参数 + listAPIResources 工具

**Files:**
- Modify: `pkg/toolsets/core/get_resource.go`、`list_resources.go`、`tools.go`
- Create: `pkg/toolsets/core/list_api_resources.go`
- Test: `pkg/toolsets/core/list_api_resources_test.go`、`get_resource_test.go`(扩展)

**Interfaces:**
- Consumes: `Client.ListAPIResources`、`GetParams.APIVersion`、`ListParams.APIVersion`
- Produces:
  - 工具 `listAPIResources`,参数 `{cluster (required), group, kind string (optional)}`
  - core `toolsClient` 接口新增:
    - `ResolveGVR(ctx context.Context, token, cluster, kind, apiVersion string) (schema.GroupVersionResource, error)`
    - `ListAPIResources(ctx context.Context, token, cluster string) ([]*metav1.APIResourceList, error)`
  - `resourceParams`/`listKubernetesResourcesParams` 新增字段 `APIVersion string \`json:"apiVersion,omitempty"\``

- [ ] **Step 1: 写失败测试 `list_api_resources_test.go`**

```go
package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakeAPIResourcesClient struct{ *fakeToolsClient } // 见实现步:fake 增加 ListAPIResources/ResolveGVR

func TestListAPIResources(t *testing.T) {
	// 通过 fakeToolsClient 注入的 *client.Client 配 fake discovery(参照 Task 2 的 harvesterResources)
	...
	res, _, err := tools.listAPIResources(middleware.WithToken(context.Background(), "tok"), &mcp.CallToolRequest{}, listAPIResourcesParams{Cluster: "local", Group: "harvesterhci.io"})
	require.NoError(t, err)
	var payload struct {
		LLM []map[string]any `json:"llm"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &payload))
	require.Len(t, payload.LLM, 1)
	assert.Equal(t, "VirtualMachine", payload.LLM[0]["kind"])
	assert.Equal(t, "virtualmachines", payload.LLM[0]["resource"])
	assert.Equal(t, "v1beta1", payload.LLM[0]["version"])
}
```

fake:`pkg/toolsets/core/fake_client_test.go` 的 `fakeToolsClient` 增加两个方法,委托给内嵌 `*client.Client`(其 `ClientSetCreator` 已在各测试里注入 fake discovery;参照 `inspect_pod_test.go` 现有注入方式)。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./pkg/toolsets/core/... -run TestListAPIResources`
Expected: FAIL(undefined)

- [ ] **Step 3: 实现**

`pkg/toolsets/core/list_api_resources.go`:

```go
package core

import (
	"context"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type listAPIResourcesParams struct {
	Cluster string `json:"cluster" jsonschema:"the name of the Kubernetes cluster"`
	Group   string `json:"group,omitempty" jsonschema:"optional exact API group filter (e.g. harvesterhci.io). Empty returns all groups"`
	Kind    string `json:"kind,omitempty" jsonschema:"optional kind filter, case-insensitive exact match (e.g. VirtualMachine)"`
}

type apiResourceInfo struct {
	Group      string `json:"group"`
	Version    string `json:"version"`
	Kind       string `json:"kind"`
	Resource   string `json:"resource"`
	Namespaced bool   `json:"namespaced"`
}

// listAPIResources returns every API resource type served by the cluster so
// the agent can discover custom resources before operating on them.
func (t *Tools) listAPIResources(ctx context.Context, toolReq *mcp.CallToolRequest, params listAPIResourcesParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("listAPIResources called", zap.String("cluster", params.Cluster))

	lists, err := t.client.ListAPIResources(ctx, middleware.Token(ctx), params.Cluster)
	if err != nil {
		return nil, nil, err
	}

	rows := make([]apiResourceInfo, 0)
	for _, list := range lists {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}
		if params.Group != "" && gv.Group != params.Group {
			continue
		}
		for _, ar := range list.APIResources {
			if strings.Contains(ar.Name, "/") { // skip subresources
				continue
			}
			if params.Kind != "" && !strings.EqualFold(ar.Kind, params.Kind) {
				continue
			}
			rows = append(rows, apiResourceInfo{Group: gv.Group, Version: gv.Version, Kind: ar.Kind, Resource: ar.Name, Namespaced: ar.Namespaced})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Group != rows[j].Group {
			return rows[i].Group < rows[j].Group
		}
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Version < rows[j].Version
	})

	mcpResponse, err := response.CreateMcpResponseAny(rows)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}}}, nil, nil
}
```

(`list_api_resources.go` 无需 metav1 import;`toolsClient` 接口与 fake 的 `ListAPIResources` 方法签名 `([]*metav1.APIResourceList, error)` 放在 `tools.go`/`fake_client_test.go`,那里才 import metav1。)

注册(`tools.go`,只读区):

```go
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name: "listAPIResources",
		Meta: map[string]any{toolsSetAnn: toolsSet},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: `Returns every API resource type (group, version, kind, resource, namespaced) served by the cluster, including all custom resources (CRDs). Use this tool FIRST to discover the correct kind and apiVersion before calling getKubernetesResource, listKubernetesResources, createKubernetesResource, patchKubernetesResource or deleteKubernetesResource with a custom resource.`,
	}, t.listAPIResources)
```

同时给所有既有只读工具补 `Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}`。

`get_resource.go`/`list_resources.go` 参数结构体加:

```go
	APIVersion string `json:"apiVersion,omitempty" jsonschema:"optional API group and version of the resource (e.g. harvesterhci.io/v1beta1). Provide it (or use a group-qualified kind such as harvesterhci.io/VirtualMachine) when working with custom resources or when a kind exists in multiple API groups"`
```

并在构造 `client.GetParams/ListParams` 时传 `APIVersion: params.APIVersion`。`getKubernetesResource`/`listKubernetesResources` 描述追加:

```
Supports any resource kind including custom resources. If the kind is unknown to the built-in table, it is resolved via cluster API discovery; use apiVersion or a group-qualified kind (group/Kind or Kind.group) to disambiguate, and the listAPIResources tool to discover available types.
```

- [ ] **Step 4: 运行确认通过 + 计数更新**

`tools_test.go`:`TestAddTools` 21→22,`TestAddToolsReadOnly` 15→16。

Run: `go test ./pkg/toolsets/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets/core/
git commit -m "feat(core): apiVersion param on get/list and new listAPIResources discovery tool"
```

---

### Task 6: core create 对 — manifest 驱动 GVR + 安全门控 + 描述重写

**Files:**
- Modify: `pkg/toolsets/core/create_resource.go`、`create_resource_plan.go`、`tools.go`
- Modify: `pkg/response/response.go`(plan 响应增加 confirmation 块)
- Test: `pkg/toolsets/core/create_resource_test.go`、`create_resource_plan_test.go`(扩展)

**Interfaces:**
- Consumes: `confirm.Gate.Check/IssueToken`、`ResolveGVR`
- Produces:
  - `response.CreatePlanResponse(resources []PlanResource, confirmation *Confirmation)`(签名变更,全部调用点更新;Task 10/11 依赖)
  - `type response.Confirmation struct { Token string \`json:"confirmationToken"\`; ExpiresAt time.Time \`json:"expiresAt"\`; Note string \`json:"note"\` }`
  - `createKubernetesResourceParams` 新增 `ConfirmationToken string \`json:"confirmationToken,omitempty"\``

- [ ] **Step 1: 先改 response 包(失败测试)**

`pkg/response/response_test.go` 追加:

```go
func TestCreatePlanResponseWithConfirmation(t *testing.T) {
	res, err := CreatePlanResponse([]PlanResource{{Type: OperationCreate, Resource: Resource{Name: "x", Kind: "ConfigMap", Cluster: "local"}, Payload: map[string]any{"a": 1}}}, &Confirmation{Token: "tok123", ExpiresAt: time.Unix(1700000000, 0).UTC(), Note: "test"})
	require.NoError(t, err)
	var parsed struct {
		Plan         []PlanResource `json:"plan"`
		Confirmation struct {
			Token     string    `json:"confirmationToken"`
			ExpiresAt time.Time `json:"expiresAt"`
		} `json:"confirmation"`
	}
	require.NoError(t, json.Unmarshal([]byte(res), &parsed))
	assert.Equal(t, "tok123", parsed.Confirmation.Token)
	assert.Len(t, parsed.Plan, 1)
}

func TestCreatePlanResponseWithoutConfirmation(t *testing.T) {
	res, err := CreatePlanResponse([]PlanResource{}, nil)
	require.NoError(t, err)
	assert.NotContains(t, res, "confirmationToken")
}
```

实现(`response.go`):

```go
// Confirmation carries the single-use token a Write tool requires, minted by
// the matching Plan tool.
type Confirmation struct {
	Token     string    `json:"confirmationToken"`
	ExpiresAt time.Time `json:"expiresAt"`
	Note      string    `json:"note"`
}

// CreatePlanResponse serializes the planned operations, optionally with a
// confirmation token block.
func CreatePlanResponse(resources []PlanResource, confirmation *Confirmation) (string, error) {
	out := map[string]any{"plan": resources}
	if confirmation != nil {
		out["confirmation"] = confirmation
	}
	bytes, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("failed to marshal plan response: %w", err)
	}
	return string(bytes), nil
}
```

> 这改变 plan 响应外形(数组 → `{plan, confirmation}` 对象),属本 fork 的预期行为;后续所有 Plan 调用点(projects、provisioning)在 Task 10/11 适配,**本任务先修 create 对并保证编译**(其它调用点暂时传 `nil`,Task 10/11 再换成真 token)。

- [ ] **Step 2: 失败测试 — create 门控与 CR 支持**

`create_resource_test.go` 追加(现有 fake 模式不变;`fakeToolsClient` 委托的真实 client 已配 fake discovery/dynamic):

```go
func TestCreateCustomCRManifestDriven(t *testing.T) {
	// tools.cfg.Gate.ElicitFunc = accept(approve)
	// manifest: apiVersion harvesterhci.io/v1beta1, kind VirtualMachine(注意 params.Kind 传 "VirtualMachine")
	// 断言:fake dynamic 在 harvesterhci.io/v1beta1/virtualmachines 上收到 create;响应 200
}

func TestCreateRequiresToken(t *testing.T) {
	// 不提供 confirmationToken → 返回 error 且 fake dynamic 无任何 create 调用
}

func TestCreateDeclined(t *testing.T) {
	// token 合法,ElicitFunc 返回 decline → 结果为 "Operation cancelled by the user. Nothing was executed.",无 create 调用
}

func TestCreateAutoWriteSkipsGate(t *testing.T) {
	// cfg.AutoWrite=true,无 token、ElicitFunc 报错(不应被调用)→ create 成功
}

func TestCreateKindMismatchRejected(t *testing.T) {
	// params.Kind="ConfigMap" 但 manifest kind=VirtualMachine → error
}
```

Plan 测试:断言响应含 `confirmationToken`,且该 token 能直接通过 `Gate.RequireToken`(用同一 gate)。

- [ ] **Step 3: 运行确认失败 → 实现**

`create_resource.go`:

```go
type createKubernetesResourceParams struct {
	Name      string `json:"name" jsonschema:"the name of the resource to create. It must match metadata.name in the manifest"`
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace where the resource is located. It must be empty for cluster-wide resources"`
	Kind      string `json:"kind" jsonschema:"the type of Kubernetes resource (e.g., Pod, Deployment, or any custom resource kind). It must match the manifest's kind"`
	Cluster   string `json:"cluster" jsonschema:"the name of the Kubernetes cluster"`
	Manifest  string `json:"manifest" jsonschema:"the resource to create as a complete Kubernetes manifest, in YAML or JSON. The GVR is resolved from the manifest's own apiVersion and kind, so any custom resource is supported"`
	ConfirmationToken string `json:"confirmationToken,omitempty" jsonschema:"REQUIRED (unless the server runs in auto-write mode): the single-use confirmationToken returned by createKubernetesResourcePlan for THIS exact operation. Never invent, reuse, or guess a token"`
}
```

handler 逻辑（manifest 解析复用下方 `create_resource_plan.go` 的 `parseCreateManifest`;先写 plan 文件再写 execute 亦可，两步同任务内）:

```go
func (t *Tools) createKubernetesResource(ctx context.Context, toolReq *mcp.CallToolRequest, params createKubernetesResourceParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("createKubernetesResource called")

	unstructuredObj, namespace, err := parseCreateManifest(params)
	if err != nil {
		return nil, nil, err
	}
	gvk := unstructuredObj.GroupVersionKind()

	gvr, err := t.client.ResolveGVR(ctx, middleware.Token(ctx), params.Cluster, gvk.Kind, gvk.GroupVersion().String())
	if err != nil {
		return nil, nil, err
	}

	payloadBytes, err := json.Marshal(unstructuredObj.Object)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to canonicalize manifest: %w", err)
	}
	op := confirm.Operation{Tool: "createKubernetesResource", Cluster: params.Cluster, Namespace: namespace, Kind: gvk.Kind, Name: unstructuredObj.GetName(), Payload: payloadBytes}
	summary := fmt.Sprintf("CREATE %s %s/%s in namespace %q of cluster %q with manifest:\n%s", gvr.String(), gvk.Kind, unstructuredObj.GetName(), namespace, params.Cluster, params.Manifest)
	approved, err := t.cfg.Gate.Check(ctx, toolReq.Session, op, params.ConfirmationToken, summary, "", t.cfg.AutoWrite)
	if err != nil {
		return nil, nil, err
	}
	if !approved {
		return confirm.CancelledResult(), nil, nil
	}

	resourceInterface, err := t.client.GetResourceInterface(ctx, middleware.Token(ctx), namespace, params.Cluster, gvr)
	if err != nil {
		return nil, nil, err
	}
	obj, err := resourceInterface.Create(ctx, unstructuredObj, metav1.CreateOptions{})
	if err != nil {
		zap.L().Error("failed to create resource", zap.String("tool", "createKubernetesResource"), zap.Error(err))
		return nil, nil, fmt.Errorf("failed to create resource %s: %w", params.Name, err)
	}

	mcpResponse, err := response.CreateMcpResponse([]*unstructured.Unstructured{obj}, params.Cluster)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}}}, nil, nil
}
```

`create_resource_plan.go`(完整 handler,与 execute 共享同一份 manifest 解析/校验;抽到包内未导出函数 `parseCreateManifest(params createKubernetesResourceParams) (*unstructured.Unstructured, string /*namespace*/, error)` 供两者调用):

```go
func parseCreateManifest(params createKubernetesResourceParams) (*unstructured.Unstructured, string, error) {
	unstructuredObj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(params.Manifest), unstructuredObj); err != nil {
		return nil, "", fmt.Errorf("failed to parse manifest (expected YAML or JSON): %w", err)
	}
	gvk := unstructuredObj.GroupVersionKind()
	if gvk.Kind == "" || gvk.Version == "" {
		return nil, "", fmt.Errorf("manifest must set apiVersion and kind")
	}
	if !strings.EqualFold(params.Kind, gvk.Kind) {
		return nil, "", fmt.Errorf("kind parameter %q does not match manifest kind %q", params.Kind, gvk.Kind)
	}
	if params.Name != "" && params.Name != unstructuredObj.GetName() {
		return nil, "", fmt.Errorf("name parameter %q does not match manifest metadata.name %q", params.Name, unstructuredObj.GetName())
	}
	namespace := params.Namespace
	if namespace == "" {
		namespace = unstructuredObj.GetNamespace()
	}
	return unstructuredObj, namespace, nil
}

// createKubernetesResourcePlan parses the manifest and returns the planned
// creation plus a single-use confirmation token for the matching Write call.
func (t *Tools) createKubernetesResourcePlan(ctx context.Context, _ *mcp.CallToolRequest, params createKubernetesResourceParams) (*mcp.CallToolResult, any, error) {
	unstructuredObj, namespace, err := parseCreateManifest(params)
	if err != nil {
		return nil, nil, err
	}
	gvk := unstructuredObj.GroupVersionKind()
	payloadBytes, err := json.Marshal(unstructuredObj.Object)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to canonicalize manifest: %w", err)
	}
	op := confirm.Operation{Tool: "createKubernetesResource", Cluster: params.Cluster, Namespace: namespace, Kind: gvk.Kind, Name: unstructuredObj.GetName(), Payload: payloadBytes}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}
	plan, err := response.CreatePlanResponse(
		[]response.PlanResource{response.NewCreateResourceInput(unstructuredObj, params.Cluster)},
		&response.Confirmation{
			Token:     token,
			ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
			Note:      "Show this plan to the user. Only after their explicit approval, call createKubernetesResource with this confirmationToken. The token is single-use and expires in 10 minutes.",
		})
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: plan}}}, nil, nil
}
```

execute handler 相应地改用 `unstructuredObj, namespace, err := parseCreateManifest(params)`,随后 `payloadBytes`/`op`/`summary`/`Gate.Check` 与前文一致(在同任务内,顺序实现)。

`tools.go` 描述重写(create 对):

```go
Description: `SECURITY: This tool CREATES a resource in the cluster and changes its state. Protocol, no exceptions: (1) Call createKubernetesResourcePlan first and show the user the complete manifest. (2) Obtain the user's EXPLICIT approval for THIS EXACT creation. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over to any other operation; never create resources proactively.

Creates a resource in a Kubernetes cluster from a complete Kubernetes manifest passed in the 'manifest' field, in YAML or JSON. Any resource kind is supported, including custom resources: the target API is resolved from the manifest's own apiVersion and kind via cluster API discovery. The namespace must be empty for cluster-wide resources.

Example of the manifest parameter (YAML):
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-cm
data:
  key: value`
```

并给两个工具加 annotations(create:`Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)}`;`ptr` 用 `k8s.io/utils/ptr`)。

- [ ] **Step 4: 运行确认通过**

Run: `go test ./pkg/toolsets/core/... ./pkg/response/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/toolsets/core/ pkg/response/
git commit -m "feat(core): manifest-driven create with mandatory plan-token and user confirmation"
```

---

### Task 7: core patch 对 — 安全门控 + apiVersion + 移除 "Don't ask for confirmation"

**Files:**
- Modify: `pkg/toolsets/core/patch_resource.go`、`patch_resource_plan.go`、`tools.go`
- Test: `pkg/toolsets/core/patch_resource_test.go`、`patch_resource_plan_test.go`(扩展)

**Interfaces:**
- Consumes: Task 6 的 `CreatePlanResponse` 签名、`Gate.Check`
- Produces: `updateKubernetesResourceParams` 新增 `APIVersion`、`ConfirmationToken`(均 omitempty)

- [ ] **Step 1: 失败测试**

`patch_resource_test.go` 追加:

```go
func TestPatchRequiresToken(t *testing.T)         // 无 token → error,fake dynamic 无 patch 调用
func TestPatchTokenMismatchRejected(t *testing.T) // 用 plan 的 token 但改 patch 内容 → ErrTokenMismatch
func TestPatchDeclined(t *testing.T)              // decline → CancelledResult 文本,无 patch 调用
func TestPatchCustomCRWithAPIVersion(t *testing.T) // kind=VirtualMachine + apiVersion=harvesterhci.io/v1beta1 成功
func TestPatchAutoWrite(t *testing.T)             // AutoWrite=true,无 token 也成功
```

Plan 测试:token 存在于响应,且对相同 patch 字节 `RequireToken` 通过。

- [ ] **Step 2: 运行确认失败 → 实现**

`patch_resource.go` 参数:

```go
type updateKubernetesResourceParams struct {
	Name      string        `json:"name" jsonschema:"the name of the specific resource to patch"`
	Namespace string        `json:"namespace,omitempty" jsonschema:"the namespace where the resource is located. It must be empty for cluster-wide resources"`
	Kind      string        `json:"kind" jsonschema:"the type of Kubernetes resource to patch. Any kind is supported, including custom resources"`
	APIVersion string       `json:"apiVersion,omitempty" jsonschema:"optional API group and version (e.g. harvesterhci.io/v1beta1) to disambiguate custom resources"`
	Cluster   string        `json:"cluster" jsonschema:"the name of the Kubernetes cluster"`
	Patch     jsonPatchList `json:"patch" jsonschema:"a JSON array of patch operation objects..."` // 保持现有描述
	ConfirmationToken string `json:"confirmationToken,omitempty" jsonschema:"REQUIRED (unless the server runs in auto-write mode): the single-use confirmationToken returned by patchKubernetesResourcePlan for THIS exact operation. Never invent, reuse, or guess a token"`
}
```

handler:先 `json.Marshal(params.Patch)` 得 patchBytes → `ResolveGVR(ctx, token, cluster, kind, apiVersion)` → `op := confirm.Operation{Tool: "patchKubernetesResource", ..., Payload: patchBytes}` → summary 含完整 patch JSON → `Gate.Check(..., "", t.cfg.AutoWrite)` → 通过才 `resourceInterface.Patch(...)`。

plan:`updateKubernetesResourcePlan` 走相同 marshal/resolve(不发请求到集群改数据,仅组 op)→ IssueToken → CreatePlanResponse(..., confirmation)。

`tools.go` 描述重写(**删除 `Don't ask for confirmation`**):

```go
// patchKubernetesResource
Description: `SECURITY: This tool MODIFIES an existing resource in the cluster. Protocol, no exceptions: (1) Call patchKubernetesResourcePlan first and show the user the exact patch. (2) Obtain the user's EXPLICIT approval for THIS EXACT patch. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over; never patch proactively or in batches.

Patches a Kubernetes resource using a JSON patch. Any resource kind is supported, including custom resources (use apiVersion or a group-qualified kind to disambiguate). The namespace must be empty for cluster-wide resources. The content type used is application/json-patch+json. Returns the modified resource.`
```

annotations 同 Task 6;patch 额外 `DestructiveHint: ptr.To(true)`。

- [ ] **Step 3: 运行确认通过**

Run: `go test ./pkg/toolsets/core/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/toolsets/core/
git commit -m "feat(core): gate patch behind plan-token and user confirmation, support CRs"
```

---

### Task 8: core delete 对(永远强制确认,不受 AutoWrite 豁免)

**Files:**
- Create: `pkg/toolsets/core/delete_resource.go`、`delete_resource_plan.go`
- Modify: `pkg/toolsets/core/tools.go`
- Test: `pkg/toolsets/core/delete_resource_test.go`、`delete_resource_plan_test.go`

**Interfaces:**
- Consumes: 全部前序
- Produces: 工具 `deleteKubernetesResourcePlan`/`deleteKubernetesResource`;参数 `{name, namespace?, kind, apiVersion?, cluster (+confirmationToken)}`

- [ ] **Step 1: 失败测试 `delete_resource_test.go`**

```go
func TestDeleteRequiresTypedName(t *testing.T) {
	// ElicitFunc 返回 accept 但 confirmName 不等于资源名 → 不删除,返回取消文本
}
func TestDeleteTypedNameMatchExecutes(t *testing.T) {
	// confirmName == name → fake dynamic 收到 delete
}
func TestDeleteRequiresToken(t *testing.T)          // 无 token → error,无 delete
func TestDeleteNotExemptedByAutoWrite(t *testing.T) // AutoWrite=true 时无 token 仍报错
func TestDeletePlanIncludesSnapshotAndToken(t *testing.T) // plan 响应含当前对象与 token
func TestDeletePlanNotFound(t *testing.T)           // GET 不到 → plan 报错
```

- [ ] **Step 2: 运行确认失败 → 实现**

`delete_resource_plan.go`:

```go
type deleteKubernetesResourceParams struct {
	Name      string `json:"name" jsonschema:"the name of the resource to delete"`
	Namespace string `json:"namespace,omitempty" jsonschema:"the namespace where the resource is located. It must be empty for cluster-wide resources"`
	Kind      string `json:"kind" jsonschema:"the type of Kubernetes resource to delete. Any kind is supported, including custom resources"`
	APIVersion string `json:"apiVersion,omitempty" jsonschema:"optional API group and version (e.g. harvesterhci.io/v1beta1) to disambiguate custom resources"`
	Cluster   string `json:"cluster" jsonschema:"the name of the Kubernetes cluster"`
	ConfirmationToken string `json:"confirmationToken,omitempty" jsonschema:"REQUIRED: the single-use confirmationToken returned by deleteKubernetesResourcePlan for THIS exact deletion. Never invent, reuse, or guess a token"`
}

// deleteKubernetesResourcePlan fetches the resource to be deleted and returns
// it together with a single-use confirmation token.
func (t *Tools) deleteKubernetesResourcePlan(ctx context.Context, _ *mcp.CallToolRequest, params deleteKubernetesResourceParams) (*mcp.CallToolResult, any, error) {
	gvr, err := t.client.ResolveGVR(ctx, middleware.Token(ctx), params.Cluster, params.Kind, params.APIVersion)
	if err != nil {
		return nil, nil, err
	}
	resourceInterface, err := t.client.GetResourceInterface(ctx, middleware.Token(ctx), params.Namespace, params.Cluster, gvr)
	if err != nil {
		return nil, nil, err
	}
	current, err := resourceInterface.Get(ctx, params.Name, metav1.GetOptions{})
	if err != nil { // NotFound included: planning the deletion of a missing object is an error
		return nil, nil, fmt.Errorf("cannot plan deletion: %w", err)
	}
	op := confirm.Operation{Tool: "deleteKubernetesResource", Cluster: params.Cluster, Namespace: params.Namespace, Kind: params.Kind, Name: params.Name}
	token, err := t.cfg.Gate.IssueToken(op)
	if err != nil {
		return nil, nil, err
	}
	planResource := response.PlanResource{
		Type:     response.OperationDelete,
		Resource: response.Resource{Name: params.Name, Kind: params.Kind, Cluster: params.Cluster, Namespace: params.Namespace},
		Payload:  current.Object,
	}
	plan, err := response.CreatePlanResponse([]response.PlanResource{planResource}, &response.Confirmation{
		Token:     token,
		ExpiresAt: time.Now().Add(t.cfg.Gate.TokenTTL).UTC(),
		Note:      "Show the user the resource that WILL BE PERMANENTLY DELETED. Only after their explicit approval call deleteKubernetesResource with this confirmationToken. The user will be asked to type the resource name to confirm. The token is single-use and expires in 10 minutes.",
	})
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: plan}}}, nil, nil
}
```

`delete_resource.go` handler:`ResolveGVR` → `Gate.Check(ctx, session, op, token, summary, params.Name /* typedName */, false /* 永不豁免 */)` → 通过才 `Delete(ctx, params.Name, metav1.DeleteOptions{})` → 响应 `response.CreateMcpResponseAny(map[string]any{"deleted": params.Name, "kind": params.Kind, "cluster": params.Cluster, "namespace": params.Namespace}, uiContext)`。

`tools.go` 注册(`!t.cfg.ReadOnly` 块内):

```go
Description: `SECURITY: This tool PERMANENTLY DELETES a resource from the cluster. This is irreversible. Protocol, no exceptions: (1) Call deleteKubernetesResourcePlan first and show the user the full resource that will be deleted. (2) Obtain the user's EXPLICIT approval for THIS EXACT deletion. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to type the resource name to confirm — you cannot and MUST NOT answer on their behalf. This tool ALWAYS requires confirmation, even in auto-write mode. Approval never carries over; NEVER batch deletions; NEVER delete proactively.

Deletes a Kubernetes resource. Any resource kind is supported, including custom resources (use apiVersion or a group-qualified kind to disambiguate). The namespace must be empty for cluster-wide resources.`
```

annotations:`DestructiveHint: ptr.To(true), ReadOnlyHint: false, IdempotentHint: false, OpenWorldHint: ptr.To(false)`。

- [ ] **Step 3: 运行确认通过 + 计数更新**

`tools_test.go`:22→24(readOnly 16 不变)。

Run: `go test ./pkg/toolsets/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/toolsets/core/
git commit -m "feat(core): add deleteKubernetesResource with typed-name user confirmation"
```

---

### Task 9: core execPod 对(--enable-exec 门控注册)

**Files:**
- Create: `pkg/toolsets/core/exec_pod.go`、`exec_pod_plan.go`
- Modify: `pkg/toolsets/core/tools.go`
- Test: `pkg/toolsets/core/exec_pod_test.go`

**Interfaces:**
- Consumes: 前序全部;core `toolsClient` 新增 `CreateRestConfig(token, clusterID string) (*rest.Config, error)`
- Produces: 工具 `execPodPlan`/`execPod`(仅 `t.cfg.EnableExec` 时注册);参数 `{cluster, namespace, name, container?, command []string, confirmationToken?}`

- [ ] **Step 1: 失败测试 `exec_pod_test.go`**

```go
func TestExecNotRegisteredByDefault(t *testing.T)   // ListTools 无 execPod(可并入 tools_test 计数)
func TestExecRegisteredWhenEnabled(t *testing.T)    // EnableExec=true → 26 个工具,含 execPod/execPodPlan
func TestExecRequiresToken(t *testing.T)            // 无 token → error,未发起任何流
func TestExecNotExemptedByAutoWrite(t *testing.T)   // AutoWrite=true 仍要 token+确认
func TestExecStreamsOutput(t *testing.T) {
	// remotecommand 需要 SPDY 服务器;单测用 httptest 起 fake exec endpoint 成本高。
	// 做法:把执行器创建抽为变量 execExecutorFactory = remotecommand.NewSPDYExecutor,测试替换为
	// 返回 fakeExecutor(实现 remotecommand.Executor 接口,StreamWithContext 写 stdout)。
	// 断言 stdout/stderr 内容、截断标志、确认门被调用。
}
func TestExecTruncatesOutput(t *testing.T)          // 超过 64KB → truncated=true
func TestExecNonZeroExitReported(t *testing.T)      // factory 返回 exec.CodeExitError{Code: 3} → 结果含 exitCode=3,不是 tool error
```

- [ ] **Step 2: 运行确认失败 → 实现 `exec_pod.go`**

```go
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

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
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
	Cluster   string   `json:"cluster" jsonschema:"the name of the Kubernetes cluster"`
	Namespace string   `json:"namespace" jsonschema:"the namespace of the pod"`
	Name      string   `json:"name" jsonschema:"the name of the pod"`
	Container string   `json:"container,omitempty" jsonschema:"the container to execute in. Defaults to the first container"`
	Command   []string `json:"command" jsonschema:"the command to execute as an argv array (e.g. [\"ls\", \"-la\", \"/etc\"]). Never wrap it in a shell (sh -c) unless the user explicitly asked for shell behavior"`
	ConfirmationToken string `json:"confirmationToken,omitempty" jsonschema:"REQUIRED: the single-use confirmationToken returned by execPodPlan for THIS exact command. Never invent, reuse, or guess a token"`
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

func (t *Tools) execPod(ctx context.Context, toolReq *mcp.CallToolRequest, params execPodParams) (*mcp.CallToolResult, any, error) {
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
		"command":          params.Command,
		"stdout":           stdout.buf.String(),
		"stderr":           stderr.buf.String(),
		"stdoutTruncated":  stdout.truncated,
		"stderrTruncated":  stderr.truncated,
	}
	if streamErr != nil {
		var exitErr k8sexec.CodeExitError
		if errors.As(streamErr, &exitErr) {
			result["exitCode"] = exitErr.Code
		} else if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			result["error"] = fmt.Sprintf("command timed out after %s", execTimeout)
		} else {
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
```

`exec_pod_plan.go`:`GetResource`(kind "pod")验证 pod 存在(顺便拿到默认 container 名填 plan)→ IssueToken → plan 响应(payload 为 `{pod, namespace, container, command}`)。

`tools.go` 注册块(在 `!t.cfg.ReadOnly && t.cfg.EnableExec` 条件下):

```go
Description: `SECURITY: This tool EXECUTES AN ARBITRARY COMMAND inside a pod — the most powerful and dangerous operation this server offers. Protocol, no exceptions: (1) Call execPodPlan first and show the user the exact command. (2) Obtain the user's EXPLICIT approval for THIS EXACT command. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to approve — you cannot and MUST NOT answer on their behalf. This tool ALWAYS requires confirmation, even in auto-write mode. Approval never carries over; never chain or batch commands; never run a command the user has not seen and approved.

Executes a command in a pod container (non-interactive, no shell unless explicitly requested by the user). The command runs with a 30 second timeout; stdout and stderr are captured and truncated to 64KB each.`
```

InputSchema 用显式 schema(`command` 强制 `type:"array"`,仿 `patchResourceInputSchema()`)。

- [ ] **Step 3: 运行确认通过 + 计数**

`tools_test.go` 新增 `TestAddToolsExecEnabled`:`NewTools(c, toolconfig.Config{EnableExec: true})` → 26 个工具且含 execPod/execPodPlan。

Run: `go test ./pkg/toolsets/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/toolsets/core/
git commit -m "feat(core): add execPod behind --enable-exec with mandatory user confirmation"
```

---

### Task 10: projects createProject 对门控

**Files:**
- Modify: `pkg/toolsets/core/projects/create_project.go`、`create_project_plan.go`、`tools.go`
- Test: 对应 `_test.go` 扩展

**Interfaces:**
- Consumes: `t.cfg.Gate.Check/IssueToken`、新 `CreatePlanResponse`
- Produces: `createProjectParams`(按现状)新增 `ConfirmationToken string \`json:"confirmationToken,omitempty"\``

- [ ] **Step 1: 失败测试**(沿用 Task 6 的四个门控用例模式:无 token 拒绝 / decline 取消 / AutoWrite 放行 / plan token 可校验)

- [ ] **Step 2: 实现**

execute:构造 op(`Tool: "createProject"`,Kind "project",Name 取项目名,Payload 为即将创建的 project 对象 JSON)→ summary → `Gate.Check(..., "", t.cfg.AutoWrite)` → 通过才走现有创建逻辑。plan:IssueToken + 新 CreatePlanResponse。

`tools.go` 描述重写,SECURITY 块同模板(将"creates a project"具体化);annotations 同 Task 6 create 类。

- [ ] **Step 3: 测试通过**

Run: `go test ./pkg/toolsets/core/projects/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/toolsets/core/projects/
git commit -m "feat(projects): gate createProject behind plan-token and user confirmation"
```

---

### Task 11: provisioning 四对写工具门控

**Files:**
- Modify: `pkg/toolsets/provisioning/scale_node_pool.go`、`scale_node_pool_plan.go`、`create_k3k_cluster.go`、`create_k3k_cluster_plan.go`、`create_imported_cluster.go`、`create_imported_cluster_plan.go`、`create_custom_cluster.go`、`create_custom_cluster_plan.go`、`tools.go`
- Test: 对应 `_test.go` 扩展

**Interfaces:**
- Consumes: 前序全部
- Produces: 四个 execute 参数结构体各加 `ConfirmationToken string \`json:"confirmationToken,omitempty"\``

- [ ] **Step 1: 失败测试** — 每对至少:无 token 拒绝、decline 取消、AutoWrite 放行;scale 额外:patch 内容改动 → ErrTokenMismatch

- [ ] **Step 2: 实现** — 与 Task 6/7 同构:op 的 Payload 为最终提交体(cluster 对象 JSON / scale patch 字节);summary 人可读;`Gate.Check(..., "", t.cfg.AutoWrite)`;plan 发 token。`tools.go` 八个工具描述全部加 SECURITY 块(plan 工具描述改为:"Returns the planned operation plus a single-use confirmationToken. Show the plan to the user; only after their explicit approval may the matching Write tool be called with this token."),annotations 按 create/patch 类设置。

- [ ] **Step 3: 测试通过**

Run: `go test ./pkg/toolsets/provisioning/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/toolsets/provisioning/
git commit -m "feat(provisioning): gate all write tools behind plan-token and user confirmation"
```

---

### Task 12: serve flags/env + Server Instructions

**Files:**
- Create: `pkg/toolsets/instructions.go`
- Modify: `cmd/serve.go`
- Test: `pkg/toolsets/instructions_test.go`、`cmd/serve_test.go`(env 解析)

**Interfaces:**
- Produces:
  - `func SafetyInstructions(cfg toolconfig.Config) string`
  - serve flag:`--allow-auto-write`(env `MCP_ALLOW_AUTO_WRITE`)、`--enable-exec`(env `MCP_ENABLE_EXEC`);优先级 显式 flag > env > 默认
  - `func boolFlagOrEnv(cmd *cobra.Command, flagName, envName string, flagVal bool) bool`(cmd 包内)

- [ ] **Step 1: 失败测试**

`instructions_test.go`:

```go
func TestInstructionsStrict(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{})
	assert.Contains(t, s, "NEVER call a Write tool")
	assert.Contains(t, s, "confirmationToken")
	assert.Contains(t, s, "deleteKubernetesResource")
	assert.NotContains(t, s, "execPod") // exec tools are listed only when --enable-exec is on
}
func TestInstructionsModes(t *testing.T) {
	assert.NotContains(t, SafetyInstructions(toolconfig.Config{ReadOnly: true}), "Write tool")
	assert.Contains(t, SafetyInstructions(toolconfig.Config{AutoWrite: true}), "auto-write")
	assert.Contains(t, SafetyInstructions(toolconfig.Config{EnableExec: true}), "execPod")
	assert.NotContains(t, SafetyInstructions(toolconfig.Config{}), "execPod")
}
```

`serve_test.go`:flag 未设 + env `MCP_ALLOW_AUTO_WRITE=true` → true;flag 显式 false + env true → false(flag 优先);都无 → false。

- [ ] **Step 2: 实现 `pkg/toolsets/instructions.go`**

strict 模式全文用 spec §5.2 的英文文本(逐字);`AutoWrite` 时替换规则 2-4 段为:

```
2. The server is running in AUTO-WRITE mode: create/update-class tools
   (createKubernetesResource, patchKubernetesResource, createProject,
   createCustomCluster, createImportedCluster, createK3kCluster,
   scaleClusterNodePool) execute immediately when you call them. Even so,
   only call them when the user has asked for the operation.
3. deleteKubernetesResource and execPod STILL REQUIRE the full protocol in
   ALL modes: Plan tool first, explicit user approval for the exact
   operation, confirmationToken, and a server-initiated user confirmation.
```

工具清单按 `cfg.ReadOnly/EnableExec` 条件拼接(execPod 仅在 EnableExec 时列出;ReadOnly 时不列任何写工具)。

`cmd/serve.go`:

```go
serveCmd.Flags().BoolVar(&allowAutoWrite, "allow-auto-write", false, "Allow create/update-class tools to execute without per-operation user confirmation (env MCP_ALLOW_AUTO_WRITE). Delete and exec always require confirmation. DANGEROUS: enable only for trusted automation")
serveCmd.Flags().BoolVar(&enableExec, "enable-exec", false, "Register the execPod tools (env MCP_ENABLE_EXEC). Disabled by default")

func boolFlagOrEnv(cmd *cobra.Command, flagName, envName string, flagVal bool) bool {
	if cmd.Flags().Changed(flagName) {
		return flagVal
	}
	if v := os.Getenv(envName); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return flagVal
}
```

`runServe` 内:

```go
allowAutoWrite = boolFlagOrEnv(cmd, "allow-auto-write", "MCP_ALLOW_AUTO_WRITE", allowAutoWrite)
enableExec = boolFlagOrEnv(cmd, "enable-exec", "MCP_ENABLE_EXEC", enableExec)
gate, err := confirm.NewGate()
if err != nil {
	return fmt.Errorf("failed to initialize confirmation gate: %w", err)
}
cfg := toolconfig.Config{ReadOnly: readOnly, AutoWrite: allowAutoWrite, EnableExec: enableExec, Gate: gate}
mcpServer := mcp.NewServer(&mcp.Implementation{Name: "rancher mcp server", Version: "v1.0.0"}, &mcp.ServerOptions{
	Instructions: toolsets.SafetyInstructions(cfg),
})
toolsets.AddAllTools(client, mcpServer, cfg)
if allowAutoWrite {
	zap.L().Warn("AUTO-WRITE MODE ENABLED: create/update-class tools will execute WITHOUT per-operation user confirmation; delete and exec still require confirmation")
}
zap.L().Info("exec tools", zap.Bool("enabled", enableExec))
```

- [ ] **Step 3: 测试通过**

Run: `go test ./pkg/toolsets/... ./cmd/... && go build ./...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/toolsets/instructions.go pkg/toolsets/instructions_test.go cmd/
git commit -m "feat(cmd): add --allow-auto-write/--enable-exec flags with env fallback and server safety instructions"
```

---

### Task 13: Dockerfile ENV 注入 + GitHub Action 构建镜像 + post-renderer + README

**Files:**
- Modify: `package/Dockerfile`
- Create: `.github/workflows/build-image.yml`
- Create: `deploy/postrenderer/kustomization.yaml`、`deploy/postrenderer/patch-args.sh`
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 12 的 env 名 `MCP_ALLOW_AUTO_WRITE`/`MCP_ENABLE_EXEC`

- [ ] **Step 1: Dockerfile**

final stage 在 `USER` 之前加:

```dockerfile
# Safety feature toggles for the fork. The stock rancher-ai-agent Helm chart
# hardcodes the container args, so image-level ENV is the supported way to
# enable these without maintaining a custom chart. They can still be
# overridden by explicit --allow-auto-write / --enable-exec flags.
ARG MCP_ALLOW_AUTO_WRITE=false
ARG MCP_ENABLE_EXEC=false
ENV MCP_ALLOW_AUTO_WRITE=${MCP_ALLOW_AUTO_WRITE} \
    MCP_ENABLE_EXEC=${MCP_ENABLE_EXEC}
```

本地验证:`docker buildx build --build-arg MCP_ALLOW_AUTO_WRITE=true -f package/Dockerfile -t rancher-ai-mcp:test .`(可选,CI 会跑);至少 `docker build --target builder` 确保语法正确。若无 docker 环境则跳过并在提交信息中注明。

- [ ] **Step 2: `.github/workflows/build-image.yml`**(参照用户 reach-mcp 的 build-docker.yml 模式)

```yaml
name: Build and Push Docker Image
on:
  push:
    branches: [main]
    tags: ["v*"]
    paths:
      - "package/Dockerfile"
      - "cmd/**"
      - "internal/**"
      - "pkg/**"
      - "go.mod"
      - "go.sum"
      - "main.go"
      - ".github/workflows/build-image.yml"
  workflow_dispatch:
jobs:
  build:
    runs-on: ubuntu-latest
    permissions: { contents: read, packages: write }
    steps:
      - uses: actions/checkout@v4
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3
      - name: image repo
        run: echo IMAGE_REPOSITORY=$(echo ${{ github.repository }} | tr '[:upper:]' '[:lower:]') >> $GITHUB_ENV
      - name: version
        run: |
          if [ "${{ github.ref_type }}" = "tag" ]; then
            echo IMAGE_TAG=${{ github.ref_name }} >> $GITHUB_ENV
          else
            echo IMAGE_TAG=${{ github.sha }} >> $GITHUB_ENV
          fi
      - uses: docker/build-push-action@v5
        with:
          context: .
          file: package/Dockerfile
          platforms: linux/amd64,linux/arm64
          push: true
          build-args: |
            VERSION=${{ github.ref_name }}
            COMMIT=${{ github.sha }}
            MCP_ALLOW_AUTO_WRITE=${{ vars.MCP_ALLOW_AUTO_WRITE || 'false' }}
            MCP_ENABLE_EXEC=${{ vars.MCP_ENABLE_EXEC || 'false' }}
          tags: |
            ghcr.io/${{ env.IMAGE_REPOSITORY }}:latest
            ghcr.io/${{ env.IMAGE_REPOSITORY }}:${{ env.IMAGE_TAG }}
          labels: |
            org.opencontainers.image.source=https://github.com/${{ github.repository }}
            org.opencontainers.image.description=Fork of rancher-ai-mcp with arbitrary CR support and safety-gated write operations
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

> 说明:tag 推送与 main 分支推送都会触发;`IMAGE_TAG` 对 tag 用版本号、对分支用 commit SHA。`vars.MCP_ALLOW_AUTO_WRITE` / `vars.MCP_ENABLE_EXEC` 是 repo 级 Actions variables(未设置时默认 `false`),在 README 交付说明中向用户指出。

- [ ] **Step 3: post-renderer(免重建镜像的替代路径)**

`deploy/postrenderer/kustomization.yaml`:

```yaml
resources:
  - rendered.yaml
patches:
  - target:
      kind: Deployment
      name: rancher-mcp-server
    patch: |-
      - op: add
        path: /spec/template/spec/containers/0/args/-
        value: "--allow-auto-write"
      - op: add
        path: /spec/template/spec/containers/0/args/-
        value: "--enable-exec"
```

`deploy/postrenderer/patch-args.sh`:

```bash
#!/usr/bin/env bash
# Helm post-renderer: appends fork flags to the mcp Deployment rendered by the
# stock rancher-ai-agent chart. Usage:
#   helm upgrade -i rancher-ai-agent oci://registry.suse.com/rancher/charts/rancher-ai-agent \
#     -n cattle-ai-agent-system -f my-values.yaml --post-renderer deploy/postrenderer/patch-args.sh
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cat > "${DIR}/rendered.yaml"
kubectl kustomize "${DIR}" 2>/dev/null || kustomize build "${DIR}"
```

(脚本需 `chmod +x`;`rendered.yaml` 加入 `.gitignore`。)

- [ ] **Step 4: README 更新**

新增两节(英文正文):

```markdown
## Safety Model: Mandatory User Confirmation for Write Operations

Every tool that modifies cluster state or executes commands is gated by the
server, not the client:

1. The agent must call the matching `*Plan` tool first. The plan response
   contains a single-use, 10-minute `confirmationToken` bound to the exact
   operation (HMAC-signed; any parameter change invalidates it).
2. On the Write call, the server asks the USER directly to confirm via MCP
   elicitation. `deleteKubernetesResource` additionally requires the user to
   type the exact resource name.
3. If the client does not support elicitation, Write calls fail closed.
   `deleteKubernetesResource` and `execPod` are never exempted.

### Flags and environment variables

| Flag | Env | Default | Effect |
|------|-----|---------|--------|
| `--read-only` | — | false | register only read-only tools |
| `--allow-auto-write` | `MCP_ALLOW_AUTO_WRITE` | false | create/update-class tools skip the token and confirmation (delete/exec unaffected). For trusted automation only |
| `--enable-exec` | `MCP_ENABLE_EXEC` | false | register `execPod`/`execPodPlan` |

## Deploying this fork with the stock rancher-ai-agent Helm chart

The stock chart hardcodes the MCP container args and has no extraArgs passthrough,
so the supported delivery paths are:

1. **Image-level ENV (recommended, no chart changes).** Build the fork image
   with the toggles baked in — `docker buildx build --build-arg MCP_ALLOW_AUTO_WRITE=true ...`
   (the GitHub Action does this from the repo variables `MCP_ALLOW_AUTO_WRITE` /
   `MCP_ENABLE_EXEC`). Then point the chart at the image:

   ```yaml
   # my-values.yaml
   global:
     cattle:
       systemDefaultRegistry: ""          # disable the global registry prefix
   aiAgent:
     image:
       repository: registry.suse.com/rancher/rancher-ai-agent   # keep the stock agent image
   mcp:
     image:
       repository: ghcr.io/<your-github-user>/rancher-ai-mcp    # fully qualified fork image
       tag: latest                      # or a commit SHA / version tag
   # imagePullSecrets:                    # only if the GHCR package is private
   #   - name: ghcr-pull-secret
   ```

   `helm upgrade -i rancher-ai-agent oci://registry.suse.com/rancher/charts/rancher-ai-agent -n cattle-ai-agent-system -f my-values.yaml`

2. **Post-renderer (no image rebuild):** `helm upgrade ... --post-renderer deploy/postrenderer/patch-args.sh`
   (edit `deploy/postrenderer/kustomization.yaml` to pick the flags).
```

- [ ] **Step 5: Commit**

```bash
git add package/Dockerfile .github/workflows/build-image.yml deploy/ README.md .gitignore
git commit -m "feat(deploy): image ENV toggles, GHCR build workflow, helm post-renderer and docs"
```

---

### Task 14: toolsdoc 头注 + make generate + 全量验证

**Files:**
- Modify: `internal/toolsdoc/main.go`(头注加一句安全说明)
- Modify: `TOOLS.md`(生成)
- Modify: `docs/superpowers/specs/2026-09-15-cr-support-and-safe-mutations-design.md`(如有偏差记录)

- [ ] **Step 1: toolsdoc 头注**

`render` 中 `Each tool is exposed...` 段后追加:

```go
b.WriteString("**Every Write tool requires a fresh plan and explicit per-operation user approval; the server enforces this with single-use confirmation tokens and a server-initiated user confirmation. Write tools never execute automatically.**\n\n")
```

- [ ] **Step 2: 生成并校验**

Run: `make generate && git diff --exit-code --stat TOOLS.md || true`(首次会更新 TOOLS.md;再跑一次 `make generate` 应无变化,对齐 CI verify-generated-docs)

- [ ] **Step 3: 全量验证**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: 全绿

- [ ] **Step 4: Commit**

```bash
git add internal/toolsdoc/ TOOLS.md
git commit -m "docs: regenerate TOOLS.md with safety-gated write tools"
```

---

## Self-Review 记录

- **Spec 覆盖**:§4.1-4.3 → Task 2/3;§4.4 → Task 5;§5.2 → Task 12;§5.3 → Task 5-11 各注册点;§5.4 → Task 6/7/8/9/10/11 描述重写;§5.5/5.6 → Task 1 与 Task 6-11 接线;§5.7 → Task 9;§5.8 → Task 1(Gate.Check 内审计);§6 工具清单 → Task 5/8/9;§7 → Task 12/13;§10 → 各任务测试步;§11 风险 → spec 已述,无需任务。
- **占位符**:Task 2/3/5 含"实现时注意"类备注(dynamicfake listKinds、计数渐进更新、workflow on 段合并),均为明确指令而非占位符。
- **类型一致性**:`toolconfig.Config` 字段名、Gate 方法签名、`CreatePlanResponse(resources, confirmation)` 新签名、`Confirmation` 字段、core `toolsClient` 新增方法(`ResolveGVR`/`ListAPIResources`/`CreateRestConfig`)在消费任务中拼写一致。
- **已知实现注意点**:① remotecommand 的 command 走 URL query(非 StreamOptions);② fake discovery 注入方式以现有 `inspect_pod_test.go` 为准;③ 工具计数渐进更新 21/15 → 22/16(T5)→ 24/16(T8)→ 26(T9 EnableExec 用例)。
