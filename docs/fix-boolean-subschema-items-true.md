# 落地文档：修复工具 schema 中的布尔子模式 `items: true`（火山方舟 11133 / codebuddyCN 400001）

> 状态：已落地（fork `0f732a3`，tag `v1.2.0-alpha.4`）；上游联动见文末（rancher/rancher-ai-mcp issue #154 / PR #155）
> 关联上游问题：CLIProxyAPI [issue #6230](https://github.com/router-for-me/CLIProxyAPI/issues/6230)（claude→openai 路径，已修）与 [issue #6324](https://github.com/router-for-me/CLIProxyAPI/issues/6324)（openai→openai 路径，维护者判定"应在工具生产者或上游修复"后关闭）
> 影响面：本 fork 全部 7 个合并工具中，`planChange` / `executeChange` 两个工具的 `patch` 参数

---

## 1. 问题背景与为什么要在本仓库修

### 1.1 症状

客户端（OpenWebUI harness / aiohttp 直连）经 CLIProxyAPI 调用火山方舟 coding plan（`ark.cn-beijing.volces.com/api/coding/v3`）或 9router codebuddyCN 网关时，只要请求携带本 MCP server 的工具声明，**第一个请求即被 400 拒绝**：

```json
{"error":{"message":"[400]: {\"code\":11133,\"msg\":\"Invalid request parameters\",...,\"extError\":{\"code\":\"model_param_invalid\"}}","type":"invalid_request_error","code":"bad_request"}}
```

- 火山方舟报 `code: 11133`，codebuddyCN 报 `extError.code: 400001`（两者是同一类上游校验拒绝）。
- 与工具是否被调用无关 —— 只要工具声明出现在请求的 `tools` 数组里就触发。

### 1.2 根因

`pkg/toolsets/dispatch/schemas.go` 的 `PlanInputSchema()` / `ExecuteInputSchema()` 有意把 `patch` 参数的 items 设为宽松模式：

```go
// json.RawMessage infers items as integer(0-255); a JSON patch is an array
// of objects. Replace the items schema with a permissive one — the handler's
// patchList() validates the real shape.
s.Properties["patch"].Items = &jsonschema.Schema{}
```

`jsonschema-go` v0.4.3（`github.com/google/jsonschema-go`）把空 `&jsonschema.Schema{}` 序列化为 **JSON 布尔字面量 `true`**，于是客户端收到的工具声明是：

```json
"patch": {"description":"RFC 6902 JSON patch array. ...","items":true,"type":"array"}
```

（已用 `TestDumpPatchSchema` 实测确认序列化输出就是 `"items":true`。）

`items: true` 是合法的 JSON Schema 2020-12 布尔子模式（"接受任意 item"），但上述严格上游用 OpenAPI 3.0 风格校验器，**不接受布尔子模式**，直接 400。

### 1.3 为什么不在 CLIProxyAPI 修

- #6324 中上游维护者明确表示：openai→openai 是纯透传路径，**永久修复应在客户端/工具 schema 生产者（即本仓库）或上游提供商**，proxy 不为单一上游校验限制加内置归一化。
- 上游给出的临时 workaround（`requests.payload.override-raw` 按路径改写）只在 CPA 侧生效，且需要按工具名/属性名枚举规则 —— 本仓库直接修源头后，**所有客户端、所有代理、所有严格上游一次性受益**，payload 规则可以删掉。

### 1.4 修改前后对比

| | 修改前 | 修改后 |
|---|---|---|
| 序列化输出 | `{"items":true,"type":"array"}` | `{"items":{},"type":"array"}` |
| JSON Schema 语义 | 接受任意 item | 接受任意 item（`{}` 是等价的全允许 schema） |
| 严格上游（方舟/9router） | ❌ 400 | ✅ 接受 |
| Gemini/Vertex 等宽松客户端 | ✅ | ✅（无变化） |

---

## 2. 落地步骤

### 2.1 唯一改动点

`pkg/toolsets/dispatch/schemas.go`，两个函数各改一行（`PlanInputSchema` ~L76、`ExecuteInputSchema` ~L91）：

```go
// 修改前
s.Properties["patch"].Items = &jsonschema.Schema{}

// 修改后
s.Properties["patch"].Items = &jsonschema.Schema{Types: []string{"null", "object"}}
```

**为什么是 `{Types: ["null","object"]}` 而不是裸 `{}`**：

- 裸 `&jsonschema.Schema{}` 会被序列化为布尔 `true`（问题本身）。
- `Types: ["null","object"]` 序列化为普通对象 schema `{"type":["null","object"]}`，不是布尔，通过严格校验器。
- 语义上仍是全允许（`null` 兼容 jsonschema-go 对 omitempty 字段的可空推断习惯；与 `patchList()` 的运行时校验不冲突，真正形状仍由 handler 校验）。
- 与文件顶部 `forcePlainType` 的既有先例（`patchResourceInputSchema` 为 Gemini/Vertex 做的同类 schema 修正）风格一致。

> 备选：`Items = &jsonschema.Schema{Type: "object"}`（单类型）也可行，若 jsonschema-go 对该写法再次退化为布尔则退回上面的 Types 写法 —— 落地时用下面的测试实测一次序列化输出即可确认。

### 2.2 回归测试（新增到 `pkg/toolsets/dispatch/dispatch_test.go` 或单独文件）

```go
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
			t.Fatalf("%s: patch.items serialized as boolean %v — strict upstreams (Ark 11133) reject boolean subschemas", name, b)
		}
	}
}
```

### 2.3 顺手核查的其它点（预期无需改）

已全仓 grep 确认布尔子模式只有这一处来源，但落地时建议再跑一遍：

- `grep -rn "Items\s*=" pkg/ --include="*.go" | grep -v _test` —— 应只剩 schemas.go 两处。
- `grep -rn "json.RawMessage" pkg/toolsets/` —— `provisioning/scale_node_pool_plan.go` 的 RawMessage 是**运行时 payload**（不进工具 schema），无影响。
- `core/patch_resource.go:patchResourceInputSchema`（旧版单工具，fork 已不注册）只做了 type 修正，没碰 items；如果哪天恢复注册旧工具，同样要处理。

### 2.4 构建与验证流程（遵循本仓既有约定）

1. `gofmt -w pkg/toolsets/dispatch/` && `go vet ./...` && `go test ./...` —— 全绿（plans 文档的 Global Constraints 要求）。
2. `make generate`（即 `go generate ./...` → `go run ./internal/toolsdoc`）重新生成 `TOOLS.md`。**预期 `TOOLS.md` 无 diff**：toolsdoc 读的是 `InputSchema()` 的描述文本，不序列化 items 细节；若有 diff 则检查是否意外改动了描述。
3. **baseline 快照**：`.baseline/` 的 golden 文件（v1/v2）是 `discover()` 对**真实集群**调用 read 工具的结果快照，与本 schema 无关，**不应产生 diff**。提交前 `git status .baseline/` 确认干净；如意外变化，参考 `2abddb9`（"explain the two #142-driven diffs"）的先例单独说明原因。
4. 提交信息建议：`fix(dispatch): serialize patch items as object schema instead of boolean true`（conventional commit，plans 约定）。

---

## 3. 仓库既有约定提醒（来自 agent 历史记录，落地时必读）

- **工具面已合并为 7 个**（`docs/superpowers/plans/2026-09-27-tool-consolidation.md`）：不要恢复/新增 per-tool `AddTools` 注册；改 schema 只动 `dispatch` 包。
- **不要修改任何既有 tool handler 方法**（Global Constraint）；本修复只动 schema 构造层，天然满足。
- **`confirm.Gate` 不可触碰**；本修复与其无关。
- **不引入新 go.mod 依赖**；本修复只用已有的 `jsonschema-go`。
- `TOOLS.md` 是生成文件（`<!-- Code generated ... DO NOT EDIT -->`），只能通过 `make generate` 更新。
- 每个 task 完成后提交一次 conventional commit；`gofmt/go vet/go test` 每步保持绿。

---

## 4. 验收标准

1. `TestPatchSchemaSerializesNoBooleanSubschema` 通过（`items` 不再是布尔）。
2. `go test ./...` 全绿；`make generate` 后 `git diff TOOLS.md` 为空。
3. 端到端：用 fork 镜像重建 MCP server 后，经 CLIProxyAPI 向火山方舟 `kimi-k3-256k` 发送携带工具声明的请求，**不再出现 11133/400001**（此前 100% 复现）。
4. （可选）删除 CLIProxyAPI 侧 #6324 维护者给的 `requests.payload.override-raw` workaround 规则，验证请求依然通过 —— 证明源头修复完整。

---

## 5. 上游联动（已完成）

已向上游 `rancher/rancher-ai-mcp` 提交 [issue #154](https://github.com/rancher/rancher-ai-mcp/issues/154) + [PR #155](https://github.com/rancher/rancher-ai-mcp/pull/155)（分支基于 upstream/main，目标 main —— 上游所有 PR 均以 main 为 base）。

**注意**：上游的触发点与 fork 不同 —— 上游无 dispatch 包，其 `patchResourceInputSchema()` 中 `patch` 是类型化 `jsonPatchList`，items 是对象 schema，但 op 结构体的 `Value any` 字段序列化为 `"value": true`（同样被方舟类严格校验拒绝）。上游 PR 修的是该处（显式列出全部 JSON 类型替代 `true`）；fork 文档 §2.1 的 `items: true` 修复仅适用于本 fork 的 dispatch 层。
