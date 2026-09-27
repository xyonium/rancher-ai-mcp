# Tool Consolidation Design: 43 → 7 Tools

Date: 2026-09-27
Status: Approved (brainstorming session, approach A selected)

## Context

The server currently registers **43 MCP tools**. Two pressures motivate consolidation:

1. **Hard provider limits** — several LLM providers cap the tool list (e.g. at 32); the full-mode list of 43 is rejected or truncated.
2. **Context/token cost** — every tool schema is injected into the prompt. The ~150-word SECURITY protocol paragraph is duplicated across 18 write tools (9 plan/execute pairs), costing thousands of tokens before the conversation starts.

Analysis of the current 43 tools:

| Category | Count | Notes |
|---|---|---|
| Plan/execute write pairs | 18 | 9 operations × 2 tools each, SECURITY text duplicated per pair |
| Rancher API read wrappers | 16 | Flat params (≤3 string fields), all thin wrappers over the same client |
| K8s-generic tools | 3 | `getKubernetesResource`, `listKubernetesResources`, `listAPIResources` — already kubectl-style, keep as-is |
| Diagnostics | 6 | Two shapes: `(cluster, namespace?)` and `(name, namespace, cluster)` |

Key enabler: the confirmation gate (`pkg/confirm.Gate`) is already generic — a token binds `Operation{Tool, Cluster, Namespace, Kind, Name, PayloadHash}` where `Tool` is an opaque string. Consolidating plan/execute pairs into generic verbs requires **zero changes to the safety mechanism**; `Tool` simply carries the operation name instead of the MCP tool name.

## Goals

- Full mode: **7 tools**. Read-only mode: **5 tools**.
- Preserve every existing capability (no feature drops).
- Preserve the safety protocol end-to-end: plan-token binding, single-use tokens, server-initiated user confirmation, typed-name confirmation for delete, `--enable-exec` gating, `--allow-auto-write` exemptions (create/update class only).
- No backward-compatibility layer for old tool names (no downstream consumers depend on them).

## Non-goals

- Changing the `confirm.Gate` token mechanism.
- Rewriting existing tool handler logic (dispatch reuses current methods).
- Dynamic tool-list negotiation (`tools/list_changed`) — rejected: poor client support and hides operations from client-side permission UIs.
- `oneOf`/discriminated-union schemas — rejected: inconsistent provider support; enums are universally supported.

## Final Tool Surface

### Unchanged (3)

| Tool | Notes |
|---|---|
| `getKubernetesResource` | Already generic over any K8s kind |
| `listKubernetesResources` | Already generic |
| `listAPIResources` | Discovery companion for the two above |

### `rancherQuery` (replaces 16 read tools)

Single-object and list reads over Rancher APIs.

| Param | Type | Used by |
|---|---|---|
| `resource` (required) | enum | All. See matrix below. |
| `cluster` | string | Most resources |
| `name` | string | Single-object identifier: project name, username, role template, git repo, bundle, machine |
| `namespace` | string | `resourceUsage` |
| `workspace` | string | `gitRepo`, `gitRepos`, `bundle` |
| `project` | string | `resourceUsage` (optional), `projectRTBs` (projectID) |
| `user`, `group` | string | RTB filters |
| `clusters` | string[] | `clusterImages`, `k3kClusters` |
| `distribution` | enum(`rke2`,`k3s`) | `supportedVersions` |

`resource` enum → required params (validation matrix):

| resource | required | optional |
|---|---|---|
| `project` | cluster, name | |
| `projects` | cluster | |
| `resourceUsage` | cluster | project, namespace |
| `clusters` | — | |
| `user` | name | |
| `roleTemplate` | name | |
| `roleTemplates` | — | |
| `clusterRTBs` | cluster | user, group |
| `projectRTBs` | cluster | user, group, project |
| `gitRepo` | workspace, name | |
| `gitRepos` | workspace | |
| `bundle` | workspace, name | |
| `clusterImages` | — | clusters |
| `k3kClusters` | — | clusters |
| `clusterMachine` | cluster, name | |
| `supportedVersions` | distribution | |

### `diagnose` (replaces 6 diagnostic tools)

| Param | Type | Used by |
|---|---|---|
| `target` (required) | enum | `cluster`, `machines`, `nodes`, `fleet`, `deployment`, `pod` |
| `cluster` | string | cluster, machines, nodes, deployment, pod |
| `namespace` | string | cluster, machines (optional); deployment, pod (required) |
| `name` | string | deployment, pod |
| `workspace` | string | fleet |

### `planChange` (replaces 9 plan tools)

Plans a mutation; returns the planned object(s) plus a single-use `confirmationToken`.

| Param | Type | Used by |
|---|---|---|
| `operation` (required) | enum | See matrix below |
| `cluster` | string | Most operations |
| `namespace` | string | K8s ops, k3k, execPod, scaleClusterNodePool |
| `name` | string | Resource/project/cluster/pod/node-pool name |
| `kind`, `apiVersion` | string | K8s create/patch/delete |
| `manifest` | string | createKubernetesResource |
| `patch` | JSON array | patchKubernetesResource |
| `command` | string[] | execPod |
| `container` | string | execPod |
| `CNI`, `version`, `distribution`, `description`, `versionManagementSetting` | string | Cluster provisioning |
| `targetCluster`, `mode`, `servers`, `agents`, `persistence`, `serverLimit`, `workerLimit`, `sync` | various | createK3kCluster |
| `cpuLimit`, `cpuReservation`, `memoryLimit`, `memoryReservation`, `displayName` | various | createProject |
| `desiredSize`, `amountToAdd`, `amountToSubtract` | int | scaleClusterNodePool |

`operation` enum (initial set; `moveNamespace` added when upstream merges):

`createKubernetesResource`, `patchKubernetesResource`, `deleteKubernetesResource`, `scaleClusterNodePool`, `execPod`, `createProject`, `createCustomCluster`, `createImportedCluster`, `createK3kCluster`

Nested-object params from current tools (e.g. K3k `persistence`, `serverLimit`, `workerLimit`, `sync`) remain nested objects in the merged schema — only the tool surface flattens, not the data shapes.

### `executeChange` (replaces 9 execute tools)

Same params as `planChange` plus `confirmationToken` (required). Validates the token against the exact operation, then runs the server-initiated user confirmation, then executes.

## Architecture

```
pkg/toolsets/merged/           ← NEW package; the only registration surface
├── tools.go        AddTools(): registers the 7 tools (5 when read-only)
├── query.go        rancherQuery dispatch + validation matrix
├── diagnose.go     diagnose dispatch + validation matrix
├── plan.go         planChange dispatch + validation matrix
├── execute.go      executeChange dispatch + confirmation-token flow
└── schemas.go      hand-written InputSchema for the 4 merged tools
```

- Existing handler methods in `pkg/toolsets/core`, `.../fleet`, `.../provisioning` are **unchanged** and keep their unit tests. The merged package holds references to the existing `Tools` structs and maps merged params → typed params → existing method.
- The per-toolset `AddTools` methods are deleted; `toolsets.AddAllTools` delegates to the merged package.
- Hand-written `InputSchema` follows the existing precedent (`patchResourceInputSchema`, `execPodInputSchema`) — struct-tag schema generation does not support enums in the current SDK (go-sdk v1.7.0 / jsonschema-go v0.4.3).
- Merged tools keep the `toolset` meta annotation so `internal/toolsdoc` (which boots the server in-memory) regenerates `TOOLS.md` unchanged in mechanism.

## Security Protocol Flow

1. `planChange(operation=X, ...)` → existing plan handler → `Gate.IssueToken(Operation{Tool: "X", ...})` — the token still binds the exact operation and payload hash; cross-operation replay is impossible because `Tool` no longer matches.
2. `executeChange(operation=X, ..., confirmationToken)` → `Gate.Check(ctx, session, op, token, summary, typedName, bypass)` — unchanged.
3. Auto-write exemption becomes a table: `map[operation]bool` — `deleteKubernetesResource` and `execPod` are never exempt; other create/update-class operations are exempt only under `--allow-auto-write`.
4. `execPod` as an operation returns a clear error unless `--enable-exec` is set. In read-only mode `planChange`/`executeChange` are not registered at all.
5. `response.Confirmation.Note` text updates to "call executeChange with operation=X and this confirmationToken".
6. `toolsets.SafetyInstructions` is rewritten for the two-verb protocol (plan → user approval → execute), carrying the protocol once instead of 20× in tool descriptions.

## Error Handling

| Case | Behavior |
|---|---|
| Unknown `resource`/`target`/`operation` | Error listing valid enum values (schema enum is the first line of defense) |
| Missing required param for the given enum value | Error naming the missing fields, e.g. `resource=gitRepo requires: workspace, name` — model self-corrects |
| Token expired / mismatched / replayed | Existing `ErrTokenInvalid` family; message directs the agent to re-run `planChange` |
| Unknown K8s kind | Existing discovery hint pointing at `listAPIResources` |
| `operation=execPod` without `--enable-exec` | Explicit error naming the flag |

## Testing

- Existing handler tests: untouched.
- New: dispatch tests (every enum value reaches the right handler), validation-matrix tests (missing/extra params), registration invariants (full mode = 7 tools, read-only = 5, `--enable-exec` changes operation availability not tool count), token cross-operation replay rejection (token from `planChange(createProject)` rejected by `executeChange(patchKubernetesResource, ...)`).
- `TOOLS.md` regenerated via `go generate ./...`; existing golden-test mechanism applies.
- `cmd/serve_test.go` and registration-count tests updated for the new surface.

## Upstream Merge Strategy (after consolidation)

Upstream `rancher/rancher-ai-mcp` is 5 commits ahead (fork is 25 ahead). Merge after consolidation lands:

| Commit | Content | Action |
|---|---|---|
| `1ee464d` | golang.org/x/crypto v0.54→v0.56 [SECURITY] | Take |
| `7c7530b` | wrangler v3.7.1→v3.7.2 | Take |
| `aa2fa2a` + `47ab6c8` | `moveNamespace` tool + project-namespace refactor | Take handler logic; expose as a new `operation` enum value in planChange/executeChange wrapped in the confirmation gate (upstream version is an ungated write). Upstream's tool-registration code is dropped, which dissolves the only expected conflict (`projects/tools.go`) |
| `b0ea921` | OSSF scorecard CI + badge | Skip (low value on a fork) |

## Migration Notes

- Old tool names disappear. `README.md`, `TOOLS.md`, and deploy docs are regenerated/updated in the same change.
- Client-side permission rules keyed to old tool names need rewriting to the 7 new names (acceptable: no downstream dependencies).
