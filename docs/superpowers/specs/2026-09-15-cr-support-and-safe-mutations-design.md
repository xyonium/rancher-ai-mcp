# 设计文档：任意自定义 CR 支持 + 安全门控的集群修改能力

> 日期：2026-09-15
> 基础：`rancher/rancher-ai-mcp` main @ `dc9c367`(v0.36.4)，改造依据见 `docs/rancher-ai-mcp-fork-analysis.md`
> 状态：待评审

## 1. 背景与目标

上游 MCP server 的两个核心局限（分析报告 §4/§5):

1. **kind→GVR 硬编码白名单**(`pkg/converter/grv.go` 的 `K8sKindsToGVRs`，约 60 条）导致 harvester.io、longhorn.io、cert-manager.io 等任意第三方 CRD 的 get/list/create/patch 全部失败，且报错不友好（零值 GVR 打到 API 根路径）。
2. **无任何 exec/命令执行能力**，也无 delete 工具。

同时，上游的写操作确认机制位于 **client 侧**(rancher-ai-agent 的 `AIAgentConfig.humanValidationTools`，框架拦截弹确认 UI),MCP server 自身零强制；`patchKubernetesResource` 的描述甚至写着 `Don't ask for confirmation`。本部署用自有 ingress 直连 MCP（不经 rancher-ai-agent),client 侧确认机制不存在，必须把确认强制收进 server。

**目标**:

- A. 任意自定义 CR 的查询与修改（get/list/create/patch/delete 全部走运行时 discovery 解析）
- B. 新增 `deleteKubernetesResource`、`execPod`（可选开启）
- C. 所有写操作由 server 端强制"先 Plan 展示 → 用户逐条显式批准 → 才执行"，绝不自动执行任何修改命令；工具描述与 server 整体描述反复强调该约束
- D. 提供显式 opt-in 的自动化豁免 flag（仅限 create/patch 类）,flag 可通过现有 Helm chart 以简单方式传递

## 2. 非目标

- 不修改 rancher-ai-agent(Python 侧）；不依赖其 `humanValidationTools`
- 不引入交互式 shell(execPod 非交互、无 stdin/TTY)
- 不改变认证模型：仍是 token 透传 + Rancher RBAC 鉴权
- 不自建 Helm chart（最后手段；见 §7)
- 不动 fleet toolset（只读，无写工具）

## 3. 部署现实与约束（已查证）

- 部署方式：Helm chart `oci://registry.suse.com/rancher/charts/rancher-ai-agent`（已拉取审阅）。`mcp-deployment.yaml` 的容器 args **硬编码**，仅 `insecureSkipTls`→`--insecure`、`mcp.readOnly`→`--read-only`、`log.level`→`--log-level` 三个 values 会被渲染；**无 extraArgs/extraEnv 透传**；`mcp.image.repository/tag` 可覆盖，`global.cattle.systemDefaultRegistry` 与 `imagePullSecrets` 支持私有仓库
- 使用方式：自有 ingress 直连 MCP server,client 是通用 MCP client（非 rancher-ai-agent),**其 elicitation 能力未知** → 严格模式下写工具必须 fail-closed
- chart 的 `AIAgentConfig.humanValidationTools` 机制对本部署无效（agent 被绕过）

## 4. Feature A：任意自定义 CR 支持

### 4.1 GVR 运行时解析链

新增 `pkg/client/resolve.go`:

```go
func (c *Client) ResolveGVR(ctx context.Context, token, cluster, kind, apiVersion string) (schema.GroupVersionResource, error)
```

解析顺序（命中即返回）:

1. **`apiVersion` 非空**（如 `harvesterhci.io/v1beta1`):`schema.ParseGroupVersion` 解析 → 在该 group/version 的 `APIResourceList` 中按 kind 大小写不敏感匹配 → 得到 resource。未命中 → 报错并列出该 group/version 下可用 kind
2. **硬编码表命中**(`converter.K8sKindsToGVRs`，小写）→ 直接使用。保持向后兼容，含 capi/management/provisioning/fleet 前缀 quirk 与 `pod.metrics.k8s.io` 等带点伪 kind（必须先于 group 限定解析，否则会被误判为 kind.group 形式）
3. **group 限定 kind**：支持 `harvesterhci.io/VirtualMachine` 与 `VirtualMachine.harvesterhci.io` 两种形式 → 锁定 group，取该 group 的 preferred version
4. **discovery 兜底**：遍历缓存的 `APIResourceList`，全集群大小写不敏感匹配 kind:
   - 恰好 1 个命中 → 使用
   - 多个 group 命中 → 报错并列出候选 group 列表，引导调用者用 group 限定 kind 或 `apiVersion`
   - 0 命中 → 报错 `unknown kind`，提示先调用 `listAPIResources` 工具发现资源类型

### 4.2 discovery 缓存

- 缓存内容：`[]*metav1.APIResourceList`（经 `ServerPreferredResources()` 获取，用 `discovery.IsGroupDiscoveryFailedError` 容忍部分失败并合并可用结果）
- key:clusterID（不同下游集群 CRD 不同；API 结构是集群级公开元数据，不按用户 token 分 key；实际资源调用仍由调用者 token 过 Rancher RBAC)
- TTL:5 分钟；解析失败时主动作废缓存重试一次再报错
- 实现：`sync.Map` + expiry，与现有 `clusterIdsCache` 风格一致

### 4.3 现有工具接入

| 调用点 | 改动 |
|--------|------|
| `Client.GetResource` / `GetResources` | `GetParams`/`ListParams` 增加 `APIVersion` 字段；查表替换为 `ResolveGVR` |
| `createKubernetesResource`(+Plan) | **修复设计缺陷**：从 manifest 的 `apiVersion`+`kind` 经 discovery 解析 GVR，不再用 kind 参数查表；保留 `kind`/`name` 参数用于与 manifest 交叉校验（不一致则报错）及 token 绑定 |
| `patchKubernetesResource`(+Plan) | 增加可选 `apiVersion` 参数，走 `ResolveGVR` |
| 新增 `deleteKubernetesResource`(+Plan) | 同 patch，走 `ResolveGVR` |
| `getKubernetesResource` / `listKubernetesResources` | 增加可选 `apiVersion` 参数；描述补充任意 CR 用法说明 |
| `Client.GetClusterID` 内部 | 不变（local 集群固定类型，继续用表） |

### 4.4 新增只读工具 `listAPIResources`

让 agent 先发现、再操作（分析报告 §7.1 A3)。

- 参数：`cluster`（必填）、`group`（可选精确过滤）、`kind`（可选，大小写不敏感精确过滤）
- 返回：JSON 数组 `[{group, version, kind, resource, namespaced}]`，按 group/version/kind 稳定排序，一次性全量返回（行数据小且过滤参数可收敛；现有 paginator 只适用于 unstructured 资源对象，不复用）
- 访问级别：Read-only，所有模式注册

## 5. Feature B：写操作安全架构（五层纵深防御)

### 5.1 模式矩阵

| 工具类别 | strict（默认） | `--allow-auto-write` |
|----------|----------------|----------------------|
| create/patch 类：`createKubernetesResource`、`patchKubernetesResource`、`createProject`、`createCustomCluster`、`createImportedCluster`、`createK3kCluster`、`scaleClusterNodePool` | Plan-token + elicitation 用户确认 | 直接执行（描述如实声明自动化模式） |
| `deleteKubernetesResource` | Plan-token + elicitation（**用户键入确切资源名**) | **不受影响**，仍走完整确认 |
| `execPod`（仅 `--enable-exec` 时注册） | Plan-token + elicitation（展示完整命令） | **不受影响**，仍走完整确认 |
| 全部只读工具 | 无门槛 | 无门槛 |

`--read-only` 语义不变且优先级最高：开启时不注册任何写工具。

### 5.2 L0 — Server Instructions（整体描述）

`cmd/serve.go` 创建 server 时设置 `mcp.ServerOptions.Instructions`，握手阶段下发给 client。strict 模式全文（英文，进入代码时以此为准）:

```
SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:

1. Tools marked as Write (createKubernetesResource, patchKubernetesResource,
   deleteKubernetesResource, createProject, createCustomCluster,
   createImportedCluster, createK3kCluster, scaleClusterNodePool, execPod)
   MODIFY cluster state or EXECUTE commands inside pods. They are DANGEROUS.

2. NEVER call a Write tool unless the user has EXPLICITLY requested this exact
   operation AND you have shown them the full details (target cluster,
   namespace, resource kind and name, complete manifest / patch / command)
   AND they have clearly approved THIS SPECIFIC operation.

3. ALWAYS call the corresponding Plan tool first
   (createKubernetesResourcePlan, patchKubernetesResourcePlan,
   deleteKubernetesResourcePlan, createProjectPlan, ...) and show the user the
   returned plan. Write tools REQUIRE the single-use confirmationToken from
   the matching Plan response. NEVER invent, guess, reuse, or bypass tokens.

4. After the user approves the plan, call the Write tool with the token. The
   server will then ask the USER DIRECTLY to confirm (you will not see the
   question). NEVER try to answer, simulate, or skip that confirmation — you
   cannot, and any attempt is a critical security violation.

5. Approval NEVER carries over. Every single Write call needs its own fresh
   plan and its own explicit user approval. NEVER batch, chain, loop, or
   automate Write calls. NEVER execute a Write "proactively" or "to be safe".

6. If the user declines or cancels, DO NOT retry. Report that nothing was
   executed. Never pressure the user into approving.

7. Prefer read-only tools whenever they can answer the question. Write tools
   are never for exploration.
```

`--allow-auto-write` 开启时替换为：声明 create/patch 类处于自动化模式可直调，但 delete/execPod 仍必须逐条确认（保留上述规则中关于这两者的全部条款）。写工具清单按实际注册情况渲染：read-only 模式下不出现任何写工具，execPod 仅在 `--enable-exec` 时出现。

### 5.3 L1 — Tool Annotations

- 写工具：`Annotations: {ReadOnlyHint: false, DestructiveHint: ptr(true), IdempotentHint: false, OpenWorldHint: ptr(false)}`(create 类 `DestructiveHint` 可 nil,delete/exec 必 true)
- 只读工具：`ReadOnlyHint: true`

### 5.4 L2 — 工具描述重写

每个执行类工具描述以 `SECURITY:` 块开头，如实描述强制协议。删除现有的 `Don't ask for confirmation`。模板（以 delete 为例）:

```
SECURITY: This tool PERMANENTLY DELETES a resource. Protocol, no exceptions:
(1) Call deleteKubernetesResourcePlan first and show the user the full plan.
(2) Obtain the user's EXPLICIT approval for THIS EXACT deletion.
(3) Call this tool with the confirmationToken from the plan response.
The server then asks the USER DIRECTLY to type the resource name to confirm.
You cannot and MUST NOT answer on their behalf. Approval never carries over;
never batch deletions; never delete proactively.

Deletes a Kubernetes resource (any kind, including custom resources). ...
```

功能描述部分补充任意 CR 用法（apiVersion 参数、group 限定 kind)。

### 5.5 L3 — Plan-token 两段式

新包 `pkg/confirm`:

- 启动时 `crypto/rand` 生成 32 字节 HMAC key（进程级，重启即失效——可接受，token TTL 本来很短）
- 签发（每个 Plan 工具调用末尾）:
  `payload = tool|cluster|namespace|kind|name|hex(sha256(payloadBytes))|expiryUnix|nonce`
  `token = base64url(payload) + "." + base64url(hmacSHA256(key, payload))`
  Plan 响应 JSON 增加 `confirmationToken`、`expiresAt` 字段
- payloadBytes 规范化：create→manifest unmarshal 后 `json.Marshal(obj.Object)`;patch→`json.Marshal(patchList)`;exec→`strings.Join(command, "\x00")`;delete→空（资源身份即 payload)
- 校验（Execute 工具入口，`confirmationToken` 为必填参数）:HMAC 比对 → 未过期（TTL 10 分钟）→ nonce 未消费（`sync.Map` 惰性清扫）→ **用当前请求参数重算 payload 比对**（任何参数改动即失配）→ 消费 nonce（单次使用）
- 分类报错：无效签名 / 已过期 / 已使用 / 参数与 plan 不一致，各自明确提示重新调用 Plan

### 5.6 L4 — Elicitation 用户确认锁（真正的闸门）

`pkg/confirm` 提供:

```go
func RequireConfirmation(ctx context.Context, ss *mcp.ServerSession, summary, typedName string) error
```

- `summary`：人可读操作摘要（delete 含完整资源身份；execPod 含完整命令数组）
- `typedName != ""`(delete):RequestedSchema 为 `{confirmName: string}`，要求 `Action=="accept"` 且 `Content["confirmName"] == typedName`（用户必须键入确切资源名）
- 其他写工具：RequestedSchema 为 `{confirm: enum[approve, reject]}`，要求 `accept + approve`
- decline/cancel → 返回正常结果文本 `Operation cancelled by the user. Nothing was executed.`（不报 error，便于 agent 正常汇报）
- **session 为 nil、client 未声明 elicitation 能力、或 Elicit 出错 → fail-closed 报错**，错误信息明确说明原因（client 需支持 MCP elicitation;create/patch 类可由运维显式开启 `--allow-auto-write` 跳过；delete/execPod 无豁免）
- 闸门函数作为 `Tools` 的可注入字段，单测可 mock approve/decline/不支持 三态

### 5.7 execPod 加固

- `--enable-exec`(env `MCP_ENABLE_EXEC`）默认关：不开启则工具根本不注册
- 非交互：`remotecommand.NewSPDYExecutor`,`Stdin: nil`,`Tty: false`
- 30 秒 context deadline;stdout/stderr 各 64KB 截断（截断时响应中注明）
- token 绑定完整命令数组；elicitation message 展示完整命令

### 5.8 审计日志

每次写工具执行（含被确认门拦截的尝试）输出 zap info 日志：tool、cluster、namespace、kind、name、确认结果（approved/declined/unsupported/auto-mode)。不记录 token 与 manifest 内容（避免泄露）。

## 6. 工具清单变更

**新增（3+2)**:

| 工具 | 访问 | 说明 |
|------|------|------|
| `listAPIResources` | RO | §4.4，所有模式注册 |
| `deleteKubernetesResourcePlan` | W | 先 GET 目标资源当前快照（GET 失败含 NotFound 则 Plan 直接报错——删除不存在的对象无意义），返回计划 + 快照 + confirmationToken |
| `deleteKubernetesResource` | W | 参数 `name/namespace/kind/cluster/apiVersion?/confirmationToken` |
| `execPodPlan` | W | 返回计划 + token；仅 `--enable-exec` |
| `execPod` | W | 参数 `cluster/namespace/name/container?/command[]/confirmationToken`；仅 `--enable-exec` |

**修改（7 对 + 2)**：全部写工具 Execute 变体增加 `confirmationToken` 参数（schema 中为 optional；严格模式下运行时强制校验非空且有效，`--allow-auto-write` 模式下 create/patch 类跳过校验，delete/execPod 任何模式都强制）；全部 Plan 变体响应增加 token;`getKubernetesResource`/`listKubernetesResources`/`patchKubernetesResource` 增加可选 `apiVersion`;`createKubernetesResource` 改 manifest 驱动解析；所有写工具描述按 §5.4 重写。

**注册计数**（更新 `tools_test.go` 等计数断言）：默认 strict 模式 24 个工具（21+listAPIResources+delete 对）,read-only 模式 16 个（15+listAPIResources)，开启 `--enable-exec` 再 +2。

## 7. 配置与 Helm 传递

**新增启动项**（flag 与 env 双通道；优先级：显式 flag > env > 默认值）:

| Flag | Env | 默认 | 说明 |
|------|-----|------|------|
| `--allow-auto-write` | `MCP_ALLOW_AUTO_WRITE` | false | create/patch 类跳过 token+elicitation;delete/execPod 不受影响；启动日志大字告警 |
| `--enable-exec` | `MCP_ENABLE_EXEC` | false | 注册 execPod 工具对 |

**Helm 交付路径**（官方 chart 无 args/env 透传，已查证 §3):

1. **主路径（零 chart 改动）**:fork 镜像 Dockerfile 增加 `ARG MCP_ALLOW_AUTO_WRITE=false` / `ARG MCP_ENABLE_EXEC=false` → `ENV`。chart 虽写死 `command/args`，镜像级 `ENV` 依然生效。用户本就必须自建 fork 镜像，构建时 `--build-arg MCP_ALLOW_AUTO_WRITE=true` 即可
2. **免重建路径**:repo 新增 `deploy/postrenderer/kustomize` 示例脚本，`helm upgrade ... --post-renderer deploy/postrenderer/patch-args.sh` 给 mcp Deployment 追加 args
3. 自建 chart：最后手段，本期不做

## 8. 文件改动地图

| 类型 | 文件 |
|------|------|
| 新增 | `pkg/confirm/confirm.go`(token 签发/校验 + elicitation 闸门）、`pkg/confirm/confirm_test.go` |
| 新增 | `pkg/client/resolve.go`(ResolveGVR + discovery 缓存）、`pkg/client/resolve_test.go` |
| 新增 | `pkg/toolsets/core/list_api_resources.go`、`delete_resource.go`、`delete_resource_plan.go`、`exec_pod.go`、`exec_pod_plan.go` 及各自 `_test.go` |
| 修改 | `pkg/client/client.go`(Params 加 APIVersion、Get/List 走 ResolveGVR) |
| 修改 | `pkg/toolsets/core/tools.go`、`create_resource.go`、`create_resource_plan.go`、`patch_resource.go`、`patch_resource_plan.go`(apiVersion/token/描述/annotations) |
| 修改 | `pkg/toolsets/core/get_resource.go`、`list_resources.go`(apiVersion 参数） |
| 修改 | `pkg/toolsets/core/projects/tools.go`、`create_project*.go`;`pkg/toolsets/provisioning/tools.go` 及 4 对写工具（token/elicitation/描述/annotations) |
| 修改 | `pkg/toolsets/toolsets.go`、`cmd/serve.go`(SecurityOptions 注入、新 flag/env、ServerOptions.Instructions) |
| 修改 | `pkg/toolsets/core/tools_test.go` 等计数/断言更新 |
| 修改 | `package/Dockerfile`(ARG→ENV)（先读该文件确认现状） |
| 新增 | `deploy/postrenderer/`(helm post-renderer 示例） |
| 生成/文档 | `TOOLS.md`(`make generate`)、`README.md`（安全模型 + flag/env + Helm 传递说明） |

`Tools` 结构体新增字段：`*confirm.Gate`（含 issuer)、`SecurityOptions{AutoWrite, EnableExec}`;`NewTools` 签名扩展，`toolsClient` 接口加 `ResolveGVR`,fake client 同步更新。

## 9. 错误处理语义

- unknown kind → 报错并建议 `listAPIResources`;kind 歧义 → 列出候选 group
- discovery 部分失败 → 容忍合并；全失败 → 原始错误透传
- token 四类失败分类报错（§5.5)
- elicitation decline/cancel → 正常结果"未执行";elicitation 不支持 → fail-closed error（仅 delete/exec 无豁免说明）
- exec 超时/截断在响应文本中显式注明

## 10. 测试策略

- `pkg/confirm`：签发/篡改/过期/重放/参数失配/键名匹配与拒绝/无能力 fail-closed
- resolver:fake discovery 覆盖表命中、apiVersion 直连、group 限定、兜底、歧义、unknown、部分失败容忍、缓存 TTL
- 各新工具：沿用现有 fake client 模式；mock 确认门三态；auto-write 模式行为；exec flag 关闭时不注册
- schema 清洁：新工具的 `[]string` 参数按 `patchResourceInputSchema()` 同款手法强制 `type: "array"`（避免 nullable 联合类型，兼容 Gemini/Vertex 校验）
- `go test ./...` 全绿 + `make generate` 后 `git diff --exit-code TOOLS.md`（对齐 CI verify-generated-docs)

## 11. 风险与开放问题

1. **client elicitation 能力未知** → strict 模式可能全部写操作 fail-closed。缓解：报错信息明确；create/patch 有 `--allow-auto-write`;delete/exec 按既定底线宁可不可用。后续可给常用 MCP client 提 elicitation 支持
2. token 为进程内 key:server 重启或 Plan/Execute 打到不同副本 → token 失效。部署为单副本（chart `replicas: 1`)+ 10 分钟 TTL，可接受；报错引导重新 Plan
3. discovery 缓存陈旧（CRD 增删后 5 分钟内不可见）→ 解析失败时自动 bust 重试缓解
4. ingress 直连暴露面：确认门不替代认证，Rancher token + RBAC 仍是边界；README 强调 ingress 必须保持 TLS 与访问控制
