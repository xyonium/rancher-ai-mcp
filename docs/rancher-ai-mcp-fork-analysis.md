# rancher-ai-mcp 深度调研报告 —— Fork 实现全功能（任意 CR / exec）参考文档

> 调研对象：`rancher/rancher-ai-mcp` main 分支（commit `dc9c367`，v0.36.4，2026-09-11）
> 调研日期：2026-09-15
> 目标：为 fork 实现「任意自定义 CRD 资源操作」+「pod exec 命令执行」提供关键代码定位与分析

---

## 1. 调研结论速览

| 问题 | 结论 | 性质 |
|------|------|------|
| 下游集群自定义 CR（harvester.io 等）无法访问 | `pkg/converter/grv.go` 硬编码 kind→GVR 白名单，不在表中的 kind 全部失败 | **工具局限**（代码设计） |
| 没有 exec / runCommand / shell 工具 | 全部 toolsets（core/fleet/provisioning）均无，代码无 remotecommand/SPDY 依赖 | **安全设计上的刻意省略** |
| `mcp.readOnly: false` 的影响 | 只控制 Write 类工具是否注册，与资源类型支持无关 | 与上述两个问题均无关 |

---

## 2. 架构总览

```
UI Extension → ReAct Agent (rancher-ai-agent, Python) → MCP Server (rancher-ai-mcp, Go) → Rancher API (/k8s/clusters/<id>) → 下游集群
```

- MCP Server 以 Deployment 跑在 local 集群 `cattle-ai-agent-system` 命名空间，默认端口 9092
- 每个 toolset 是一组工具的集合，当前 toolsets：`core`、`fleet`、`provisioning`
- 认证：agent 在 HTTP header 中携带 Rancher token，MCP 透传给 Rancher API，由 Rancher RBAC 做鉴权（middleware 取 token：`internal/middleware/context.go`）
- 客户端通过 Rancher 代理访问下游集群：`CreateRestConfig()` 把 `rancherURL + "/k8s/clusters/" + clusterID` 作为 apiserver 地址

## 3. 关键代码位置地图

| 文件 | 作用 |
|------|------|
| `cmd/serve.go` | HTTP/TLS server 启动，readOnly 标记注入 |
| `pkg/client/client.go` | K8s client 封装：GetResource / GetResources / patch / create、cluster ID 解析、rest.Config 构造 |
| `pkg/converter/grv.go` | **kind→GVR 硬编码映射表 `K8sKindsToGVRs`**（核心瓶颈） |
| `pkg/toolsets/toolsets.go` | toolset 注册中心 |
| `pkg/toolsets/core/tools.go` | core 工具集注册（rancher 工具组），Write 工具按 readOnly 条件注册 |
| `pkg/toolsets/core/get_resource.go` / `list_resources.go` / `create_resource.go` / `patch_resource.go` | 通用资源 CRUD 工具（都查 GVR 表） |
| `pkg/toolsets/core/inspect_pod.go` | 唯一用 client-go typed client 的地方：拉 pod 日志（GetLogs，非 exec） |
| `TOOLS.md` | 生成的工具文档 |

---

## 4. 核心瓶颈分析：kind→GVR 硬编码表

### 4.1 问题代码（`pkg/converter/grv.go`）

所有资源类型集中在一个硬编码 map：

```go
// K8sKindsToGVRs maps lowercase Kubernetes resource kind names to their corresponding
// GroupVersionResource (GVR) identifiers. This mapping is used for dynamic client operations
// to resolve resource types across different API groups and versions.
var K8sKindsToGVRs = map[string]schema.GroupVersionResource{
    // --- CORE Kubernetes Resources (Group: "") ---
    "pod":                   {Group: "", Version: "v1", Resource: "pods"},
    "service":               {Group: "", Version: "v1", Resource: "services"},
    "configmap":             {Group: "", Version: "v1", Resource: "configmaps"},
    "secret":                {Group: "", Version: "v1", Resource: "secrets"},
    "event":                 {Group: "", Version: "v1", Resource: "events"},
    "namespace":             {Group: "", Version: "v1", Resource: "namespaces"},
    "node":                  {Group: "", Version: "v1", Resource: "nodes"},
    "serviceaccount":        {Group: "", Version: "v1", Resource: "serviceaccounts"},
    "persistentvolume":      {Group: "", Version: "v1", Resource: "persistentvolumes"},
    "persistentvolumeclaim": {Group: "", Version: "v1", Resource: "persistentvolumeclaims"},
    "resourcequota":         {Group: "", Version: "v1", Resource: "resourcequotas"},
    "limitrange":            {Group: "", Version: "v1", Resource: "limitranges"},

    // --- Apps Resources (Group: "apps") ---
    "deployment":  {Group: "apps", Version: "v1", Resource: "deployments"},
    "statefulset": {Group: "apps", Version: "v1", Resource: "statefulsets"},
    "daemonset":   {Group: "apps", Version: "v1", Resource: "daemonsets"},
    "replicaset":  {Group: "apps", Version: "v1", Resource: "replicasets"},

    // --- Batch ---
    "job":     {Group: "batch", Version: "v1", Resource: "jobs"},
    "cronjob": {Group: "batch", Version: "v1", Resource: "cronjobs"},

    // --- Networking ---
    "ingress":       {Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"},
    "networkpolicy": {Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"},
    "ingressclass":  {Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses"},

    // --- Autoscaling ---
    "horizontalpodautoscaler": {Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"},
    "vpa":                     {Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"},

    // --- RBAC ---
    "role":               {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"},
    "rolebinding":        {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"},
    "clusterrole":        {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"},
    "clusterrolebinding": {Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"},

    // --- Storage ---
    "storageclass":     {Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"},
    "volumeattachment": {Group: "storage.k8s.io", Version: "v1", Resource: "volumeattachments"},
    "csinode":          {Group: "storage.k8s.io", Version: "v1", Resource: "csinodes"},
    "csidriver":        {Group: "storage.k8s.io", Version: "v1", Resource: "csidrivers"},

    // --- Flow Control ---
    "flowschema":                 {Group: "flowcontrol.apiserver.k8s.io", Version: "v1", Resource: "flowschemas"},
    "prioritylevelconfiguration": {Group: "flowcontrol.apiserver.k8s.io", Version: "v1", Resource: "prioritylevelconfigurations"},

    // --- Admission Control ---
    "validatingwebhookconfiguration":   {Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingwebhookconfigurations"},
    "mutatingwebhookconfiguration":     {Group: "admissionregistration.k8s.io", Version: "v1", Resource: "mutatingwebhookconfigurations"},
    "validatingadmissionpolicy":        {Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicies"},
    "validatingadmissionpolicybinding": {Group: "admissionregistration.k8s.io", Version: "v1", Resource: "validatingadmissionpolicybindings"},

    // --- API Extension ---
    "crd":                      {Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"},
    "customresourcedefinition": {Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"},

    // --- Discovery ---
    "endpointslice": {Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"},

    // --- Policy ---
    "poddisruptionbudget": {Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"},

    // --- Scheduling ---
    "priorityclass": {Group: "scheduling.k8s.io", Version: "v1", Resource: "priorityclasses"},

    // --- METRICS ---
    "node.metrics.k8s.io": {Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"},
    "pod.metrics.k8s.io":  {Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"},

    // --- RANCHER CORE (management.cattle.io/v3) ---
    "managementcluster":          {Group: "management.cattle.io", Version: "v3", Resource: "clusters"},
    "project":                    {Group: "management.cattle.io", Version: "v3", Resource: "projects"},
    "user":                       {Group: "management.cattle.io", Version: "v3", Resource: "users"},
    "roletemplate":               {Group: "management.cattle.io", Version: "v3", Resource: "roletemplates"},
    "globalrole":                 {Group: "management.cattle.io", Version: "v3", Resource: "globalroles"},
    "globalrolebinding":          {Group: "management.cattle.io", Version: "v3", Resource: "globalrolebindings"},
    "clusterroletemplatebinding": {Group: "management.cattle.io", Version: "v3", Resource: "clusterroletemplatebindings"},
    "projectroletemplatebinding": {Group: "management.cattle.io", Version: "v3", Resource: "projectroletemplatebindings"},
    "nodetemplate":               {Group: "management.cattle.io", Version: "v3", Resource: "nodetemplates"},
    "nodedriver":                 {Group: "management.cattle.io", Version: "v3", Resource: "nodedrivers"},
    "setting":                    {Group: "management.cattle.io", Version: "v3", Resource: "settings"},

    // --- RANCHER PROVISIONING ---
    "provisioningcluster": {Group: "provisioning.cattle.io", Version: "v1", Resource: "clusters"},

    // --- K3k ---
    "k3kcluster": {Group: "k3k.io", Version: "v1beta1", Resource: "clusters"},

    // --- FLEET ---
    "bundle":           {Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "bundles"},
    "gitrepo":          {Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "gitrepos"},
    "bundledeployment": {Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "bundledeployments"},
    "clustergroup":     {Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "clustergroups"},
    "fleetcluster":     {Group: "fleet.cattle.io", Version: "v1alpha1", Resource: "clusters"},

    // --- CAPI（version 留空，运行时 discovery 决定）---
    "capicluster":           {Group: "cluster.x-k8s.io", Version: "", Resource: "clusters"},
    "capimachine":           {Group: "cluster.x-k8s.io", Version: "", Resource: "machines"},
    "capimachineset":        {Group: "cluster.x-k8s.io", Version: "", Resource: "machinesets"},
    "capimachinedeployment": {Group: "cluster.x-k8s.io", Version: "", Resource: "machinedeployments"},
}
```

**约 60 个条目。harvester.io / longhorn.io / snapshot.storage.k8s.io / cert-manager.io / 任意第三方 CRD 均不在表中。**

注意几个 design quirk：
- kind 冲突用前缀解决：`capi`+kind / `provisioning`+kind / `management`+kind / `fleet`+`cluster` → `fleetcluster`（同名 kind 不同 group 的 Collision 处理）
- metrics 资源用 `"pod.metrics.k8s.io"` 这种带点号的伪 kind
- CAPI 资源 Version 留空，配合 `GetResourceAtAnyAPIVersion` 走 discovery 遍历版本

### 4.2 失败路径（`pkg/client/client.go`）

所有通用资源工具最终都走这里：

```go
// GetResource retrieves a single Kubernetes resource by name.
func (c *Client) GetResource(ctx context.Context, params GetParams) (*unstructured.Unstructured, error) {
    resourceInterface, err := c.GetResourceInterface(ctx, params.Token, params.Namespace, params.Cluster,
        converter.K8sKindsToGVRs[strings.ToLower(params.Kind)])  // ← 不在表中：返回零值 GVR {"","",""}
    if err != nil {
        return nil, err
    }
    obj, err := resourceInterface.Get(ctx, params.Name, metav1.GetOptions{})
    ...
}
```

`K8sKindsToGVRs["harvestervirtualmachineimage"]` 返回零值 `schema.GroupVersionResource{}` → dynamic client 向 `https://rancher/k8s/clusters/<id>/` 根路径发 GET → 404/405 或不可解析错误。**不会报 "unknown kind"，错误信息对用户不友好。**

同理：
- `GetResources`（list）同样查表
- `createKubernetesResource`（`pkg/toolsets/core/create_resource.go`）同样查表：

```go
func (t *Tools) createKubernetesResource(...) {
    resourceInterface, err := t.client.GetResourceInterface(
        ctx, middleware.Token(ctx),
        params.Namespace, params.Cluster,
        converter.K8sKindsToGVRs[strings.ToLower(params.Kind)])  // ← 同样问题
    ...
}
```

- `patchKubernetesResource` 同。

### 4.3 已有但未复用的 discovery 基础设施

代码里其实已经有一套「按 group+resource 查所有版本」的 discovery 逻辑，**但只有 CAPI 工具在用**：

```go
// pkg/client/client.go
// GetResourceAtAnyAPIVersion queries the API server for all supported versions of the
// group and resource related to the passed kind...
func (c *Client) GetResourceAtAnyAPIVersion(ctx context.Context, params GetParams) (*unstructured.Unstructured, error) {
    currentGVK, ok := converter.K8sKindsToGVRs[strings.ToLower(params.Kind)]
    if !ok {
        return nil, fmt.Errorf("unknown kind: %s", params.Kind)   // ← 仍然先查硬编码表
    }
    versions, err := c.getAPIVersionsForGR(ctx, params.Token, params.Cluster, schema.GroupResource{
        Group:    currentGVK.Group,
        Resource: currentGVK.Resource,
    })
    ...
}

// getAPIVersionsForGR 走 client.Discovery().ServerGroups()
func (c *Client) getAPIVersionsForGR(ctx context.Context, token, cluster string, groupResource schema.GroupResource) ([]string, error) {
    ...
    apiGroupList, err := client.Discovery().ServerGroups()
    ...
    for _, apiGroup := range apiGroupList.Groups {
        if apiGroup.Name == groupResource.Group {
            for _, version := range apiGroup.Versions {
                versions = append(versions, version.Version)
            }
        }
    }
    return versions, nil
}
```

另外还有 `GetResourceByGVR(ctx, params, gvr)` —— **已经接受显式 GVR 参数**但没有任何工具调用它。

**结论：fork 改造的最大杠杆点就在这里——把「查硬编码表」换成「查 APIResourceList discovery」，基础设施是现成的。**

---

## 5. exec 缺失分析

### 5.1 确认事实

- TOOLS.md 全量工具清单（core + fleet + provisioning）无任何 exec/run/shell/command 类工具
- `grep` 全仓库无 `remotecommand`、`spdy`、`Executor` 相关 import
- `go.mod` 中没有 `k8s.io/client-go/tools/remotecommand` 的使用

### 5.2 最接近的能力：pod 日志（`pkg/toolsets/core/inspect_pod.go`）

```go
const podLogsTailLines int64 = 50   // 硬编码只拉 50 行

func (t *Tools) getPodLogs(ctx context.Context, cluster string, token string, pod corev1.Pod) (*unstructured.Unstructured, error) {
    clientset, err := t.client.CreateClientSet(ctx, token, cluster)
    ...
    for _, container := range pod.Spec.Containers {
        podLogOptions := corev1.PodLogOptions{
            TailLines: ptr.To(podLogsTailLines),
            Container: container.Name,
        }
        req := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &podLogOptions)
        podLogs, err := req.Stream(ctx)   // ← 普通 REST 日志流，不是 exec
        ...
    }
}
```

注意这是**唯一**使用 typed `clientset` 而非 dynamic client 的工具，说明接 SPDY/exec 在架构上没有障碍，纯粹是没做。

### 5.3 Rancher 侧的可行性

- Rancher 的 `/k8s/clusters/<id>` 代理支持 exec 子资源（Rancher UI 的 "Execute Shell" 就是走这个：`/k8s/clusters/<id>/api/v1/namespaces/<ns>/pods/<name>/exec`）
- client-go 的 `remotecommand.NewSPDYExecutor(restConfig, "POST", execURL)` 可以直接用 `CreateRestConfig()` 产出的 config，无需额外认证处理

### 5.4 官方设计意图佐证

- Fleet 子 agent 的 systemPrompt 明确写：「**Scope:** Decline general Kubernetes administration tasks (e.g., "Delete this pod")... **Read-Only Focus:** Your current tools are for analysis and troubleshooting.」
- rancher-ai-agent 文档有 **Human Validation Tools** 机制：AIAgentConfig 的 `humanValidationTools` 字段可指定哪些工具执行前必须用户确认——这是给危险工具（如 exec）预留的闸门

---

## 6. readOnly 机制（澄清：与上述问题无关）

```go
// pkg/toolsets/core/tools.go（结构示意）
// Write 工具仅在非 readOnly 模式下注册：
//   createKubernetesResource / createKubernetesResourcePlan
//   createProject / createProjectPlan
//   patchKubernetesResource / patchKubernetesResourcePlan
//   provisioning 的 create*/scale* 系列
```

Helm values：
```yaml
mcp:
  readOnly: true   # 默认 false；true 时只注册 Read-only 工具
```

用户已设 `readOnly: false`，因此写工具都在——但 `createKubernetesResource` 对自定义 CR 仍然失败，因为瓶颈在 GVR 表，不在工具注册。

---

## 7. Fork 改造建议（实现路线）

### 7.1 目标 A：支持任意 CRD（推荐方案）

**方案 A1（最小改动）—— discovery 替换硬编码表**

改造 `pkg/converter/grv.go` + `pkg/client/client.go`：

1. 新增方法：通过 discovery client 拉取 `ServerPreferredResources()` 或 `APIResourceList`，构建 kind→GVR 动态映射（按集群缓存，注意不同下游集群 CRD 不同，缓存 key 要带 clusterID）
2. 修改 `GetResource/GetResources/Create/Patch` 的 GVR 解析逻辑：
   - 先查硬编码表（保持向后兼容，含前缀 quirk）
   - miss 则查 discovery 动态映射（kind 大小写不敏感；kind 冲突时要求传入 group，如 `VirtualMachine.harvesterhci.io` 或 `harvesterhci.io/VirtualMachine`）
3. 直接暴露 `GetResourceByGVR` 为新工具 `getKubernetesResourceByGVR(group, version, resource, namespace, name)`，绕过 kind 解析

参考代码模式（伪代码）：

```go
func (c *Client) resolveGVR(ctx context.Context, token, cluster, kind string) (schema.GroupVersionResource, error) {
    if gvr, ok := converter.K8sKindsToGVRs[strings.ToLower(kind)]; ok {
        return gvr, nil
    }
    // discovery fallback
    cli, err := c.CreateClientSet(ctx, token, cluster)
    lists, err := cli.Discovery().ServerPreferredResources()
    for _, list := range lists {
        gv, _ := schema.ParseGroupVersion(list.GroupVersion)
        for _, ar := range list.APIResources {
            if strings.EqualFold(ar.Kind, kind) {
                return schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: ar.Name}, nil
            }
        }
    }
    return schema.GroupVersionResource{}, fmt.Errorf("unknown kind: %s", kind)
}
```

**方案 A2（兼容性最好）—— 工具入参加可选 `apiVersion` 字段**

`getKubernetesResource`/`listKubernetesResources`/`patchKubernetesResource`/`createKubernetesResource` 的 params 增加 `apiVersion`（如 `harvesterhci.io/v1beta1`），传入时直接用 `schema.FromAPIVersionAndResource()`（或由 manifest 中的 apiVersion 解析，create 工具已经解析了 manifest，可直接从中取 GVR 而**不需要 kind 查表**——create_resource.go 里 kind 参数对 manifest 内的 apiVersion 是冗余的，这是个明显的设计漏洞）。

**方案 A3（锦上添花）—— 新增 CRD 发现工具**

`listAPIResources(cluster, group?)`：返回该集群所有 API group/version/resource/kind，让 LLM 可以先发现再操作。这在 TOOLS.md 里没有，对任意 CR 场景几乎是必需的。

### 7.2 目标 B：实现 pod exec

1. `go.mod` 无需新依赖（client-go 已包含 `tools/remotecommand`）
2. 新增 `pkg/toolsets/core/exec_pod.go`，参考 `inspect_pod.go` 的结构：

```go
type execPodParams struct {
    Name      string `json:"name"`
    Namespace string `json:"namespace"`
    Cluster   string `json:"cluster"`
    Container string `json:"container,omitempty"`
    Command   []string `json:"command"`   // 非交互式：直接执行并收 stdout/stderr
}

func (t *Tools) execPod(ctx context.Context, ...) {
    restConfig, _ := t.client.CreateRestConfig(token, clusterID)  // 复用现有方法
    execURL := restConfig.Host + fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/exec?container=%s&stdout=true&stderr=true",
        ns, name, container)
    executor, _ := remotecommand.NewSPDYExecutor(restConfig, "POST", parseURL(execURL))
    var stdout, stderr bytes.Buffer
    executor.StreamWithContext(ctx, remotecommand.StreamOptions{
        Stdin: nil,   // 非交互：不给 stdin
        Stdout: &stdout, Stderr: &stderr,
        Tty: false,   // TTY 在 MCP 单次请求模型下无意义
        Command: params.Command,
    })
    return stdout/stderr
}
```

3. 注册时**放进 Human Validation 名单**：在 rancher-ai-agent 侧 AIAgentConfig 配 `humanValidationTools: ["execPod"]`，执行前用户确认（官方机制，见第 5.4 节）
4. 可选防护：命令白名单/黑名单正则、禁止 shell 元字符、限制超时（如 30s）、限制输出大小（如 64KB，参考 inspect_pod 的 50 行日志截断思路）

### 7.3 注意事项

- **版本探测成本**：`ServerPreferredResources` 部分集群可能因聚合 API 不可达而报 `ErrGroupDiscoveryFailed`，要用 `discovery.IsGroupDiscoveryFailedError` 容忍并合并部分结果
- **缓存策略**：现有 `clusterIdsCache`/`clustersDisplayNameToIDCache` 是 `sync.Map` 全局缓存且不区分用户 token；GVR 动态映射也应按 clusterID 缓存 + TTL（CRD 会增删）
- **Rancher Steve vs 原生 API**：MCP 走的是 Rancher 的 `/k8s/clusters/<id>` 原生 apiserver 代理（不是 Steve `/v1` schema），所以 CR 操作就是标准 dynamic client 语义，没有 Steve 类型转换问题
- **RBAC 是现成的**：token 透传意味着 fork 后无需任何 RBAC 改动，用户 Rancher 权限不够时 API 自然 403，符合预期
- **TOOLS.md 是生成的**：改完工具记得 `make generate` 重新生成文档，CI（`verify-generated-docs.yml`）会校验
- **schema 测试**：`pkg/toolsets/core/*_test.go` 有 Gemini/OpenCode 的 input schema 校验回归测试（见 PR #136），新工具的 InputSchema 要写干净（nullable pointer 会产生非法 properties）

## 8. 附：当前完整工具清单（v0.36.4 TOOLS.md 摘要）

| Toolset | 工具 | 访问 |
|---------|------|------|
| fleet | analyzeFleetResources / getBundle / getGitRepo / listGitRepos | RO |
| provisioning | analyzeCluster / analyzeClusterMachines / getClusterMachine / listK3kClusters / listSupportedKubernetesVersions | RO |
| provisioning | createCustomCluster(Plan) / createImportedCluster(Plan) / createK3kCluster(Plan) / scaleClusterNodePool(Plan) | W |
| rancher | getClusterImages / getDeployment / getKubernetesResource / getNodeMetrics / getProject / getResourceUsage / getRoleTemplate / getUser / inspectPod / listClusterRoleTemplateBindings / listKubernetesResources / listProjectRoleTemplateBindings / listProjects / listRoleTemplates | RO |
| rancher | createKubernetesResource(Plan) / createProject(Plan) / patchKubernetesResource(Plan) | W |
| rancher,provisioning | listClusters | RO |

**所有工具都依赖 GVR 表（directly or indirectly），全部受第 4 节瓶颈影响。**

## 9. 参考链接

- 仓库：https://github.com/rancher/rancher-ai-mcp
- 工具文档：https://github.com/rancher/rancher-ai-mcp/blob/main/TOOLS.md
- Agent 仓库：https://github.com/rancher/rancher-ai-agent
- 官方文档（admin how-to，readOnly/Human Validation/Multi-agent）：https://documentation.suse.com/cloudnative/rancher-ai/latest/en/how-tos/how-to-admin.html
- Helm chart：`oci://registry.suse.com/rancher/charts/rancher-ai-agent`
