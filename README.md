## MCP Server for Rancher
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/rancher/rancher-ai-mcp/badge)](https://scorecard.dev/viewer/?uri=github.com/rancher/rancher-ai-mcp)

The MCP server allows the [Rancher AI agent](https://github.com/rancher-sandbox/rancher-ai-agent) to securely retrieve or update Kubernetes and Rancher resources across local and downstream clusters. It expects the Rancher token in a header, which the agent will always provide for authentication.

## Overview

This Model Context Protocol (MCP) server provides a secure bridge between the Rancher AI agent and Kubernetes clusters, enabling AI-powered cluster management through a standardized tool interface. The server runs as a Kubernetes deployment within the Rancher environment and exposes tools for resource inspection, modification, and cluster operations.

## Architecture

### Package Structure

- **`cmd/`** - CLI commands and server initialization
  - `serve.go` - HTTP/TLS server setup with dynamic listener support
  - `root.go` - Root command configuration

- **`pkg/client/`** - Kubernetes client abstraction
  - Dynamic client wrapper with cluster ID resolution
  - Rancher API integration for cluster management
  - Support for both local and downstream cluster operations

- **`pkg/toolsets/`** - Tool registration and organization
  - `toolsets.go` - Central registry for tool collections
  - `merged/` - The only registration surface: the 7 tools exposed by the server
  - `core/`, `fleet/`, `provisioning/` - Per-domain operation handlers dispatched by the merged tools

- **`pkg/response/`** - Response formatting utilities
  - Structured text and content generation for MCP responses

- **`pkg/converter/`** - Data transformation utilities
  - Group/Version/Resource (GVR) conversion helpers

### Multi-Agent Architecture

The server is designed with a modular toolset architecture to support a **multi-agent system**. Each toolset contains a collection of related tools that serve a specific agent or domain within the Rancher AI ecosystem.

**Current Toolsets:**
- **`merged`** - The consolidated surface: `rancherQuery`, `diagnose`, `planChange` and `executeChange`. Each one dispatches an enum parameter to the per-domain handlers.
- **`rancher`** - The K8s-generic tools: `getKubernetesResource`, `listKubernetesResources`, `listAPIResources`.

This architecture allows different AI agents to access only the tools they need, improving security, maintainability, and scalability. 

### TLS & Security

The server supports two modes:

1. **TLS Mode (Production)**: Uses Rancher's dynamic listener with auto-generated certificates
   - Certificates stored as Kubernetes secrets
   - Automatic cert rotation and renewal
   - Client certificate authentication support
   - TLS 1.2+ with secure cipher suites

2. **Insecure Mode (Development)**: Plain HTTP for local testing
   - Enabled via `--insecure` flag or `INSECURE_SKIP_TLS=true`

### Available Tools

Each tool is exposed through the MCP protocol and can be invoked by the Rancher AI agent. The full, up-to-date list of tools grouped by toolset is maintained in [TOOLS.md](TOOLS.md), which is generated from the tool definitions.

To regenerate it after adding or changing tools, run:

```bash
go generate ./...
# or
make generate
```

## Configuration

### Command-line Flags

```bash
--port <int>              Port to listen on (default: 9092)
--insecure                Skip TLS verification (default: false)
```

## Safety Model: Mandatory User Confirmation for Write Operations

Every tool that modifies cluster state or executes commands is gated by the
server, not the client:

1. The agent must call `planChange` first, with the operation and parameters it
   intends to execute. The plan response contains a single-use, 10-minute
   `confirmationToken` bound to the exact operation and parameters
   (HMAC-signed; any parameter change invalidates it).
2. The agent then calls `executeChange` with the same operation and parameters
   plus the token. The server asks the USER directly to confirm via MCP
   elicitation. The `deleteKubernetesResource` operation additionally requires
   the user to type the exact resource name.
3. If the client does not support elicitation, `executeChange` fails closed.
   The `deleteKubernetesResource` and `execPod` operations are never exempted,
   not even in auto-write mode.

### Flags and environment variables

| Flag | Env | Default | Effect |
|------|-----|---------|--------|
| `--read-only` | — | false | register only read-only tools (5 of 7; `planChange`/`executeChange` unregistered) |
| `--allow-auto-write` | `MCP_ALLOW_AUTO_WRITE` | false | create/update-class operations skip the token and confirmation (delete/exec unaffected). For trusted automation only |
| `--enable-exec` | `MCP_ENABLE_EXEC` | false | allow the `execPod` operation of `planChange`/`executeChange` (refused at runtime without it) |

## Deploying this fork with the stock rancher-ai-agent Helm chart

The stock chart hardcodes the MCP container args and has no extraArgs passthrough,
so the supported delivery paths are:

1. **Image-level ENV (recommended, no chart changes).** The GitHub Action builds
   two variants of the same commit — pick the tag by safety posture:

   | Tag | `MCP_ALLOW_AUTO_WRITE` | Behavior |
   |-----|----------------------|----------|
   | `latest`, `<sha>`, `vX.Y.Z` | `false` | every change requires plan-token + user confirmation |
   | `auto`, `<sha>-auto`, `vX.Y.Z-auto` | `true` | create/update-class operations execute without confirmation; delete/exec still always gated |

   The repository Actions variable `MCP_ENABLE_EXEC` (default unset = `false`) is baked into BOTH
   image tags, including `latest`: setting it to `true` enables the `execPod` operation of
   `planChange`/`executeChange` in the safe tag too. Exec stays fully confirmation-gated in every
   mode either way.

   `latest` and `auto` are floating tags, and the chart's default `imagePullPolicy: IfNotPresent`
   means a node-local cached image is not refreshed on upgrade. For reproducible upgrades, pin an
   immutable tag instead: `<sha>` / `<sha>-auto` (or `vX.Y.Z` / `vX.Y.Z-auto`).

   Then point the chart at the image:

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
       tag: latest                      # safety-gated; use "auto" (or "<sha>-auto") for the auto-write variant
   # imagePullSecrets:                    # only if the GHCR package is private
   #   - name: ghcr-pull-secret
   ```

   `helm upgrade -i rancher-ai-agent oci://registry.suse.com/rancher/charts/rancher-ai-agent -n cattle-ai-agent-system -f my-values.yaml`

2. **Post-renderer (no image rebuild):** `helm upgrade ... --post-renderer deploy/postrenderer/patch-args.sh`.

   The shipped `deploy/postrenderer/kustomization.yaml` is a **no-op passthrough**: it appends no
   flags, so the rendered deployment is exactly what the chart produced. Enabling either flag
   requires editing that file — uncomment the `patches:` block and the entries you want.

   > **WARNING:** `--allow-auto-write` lets create/update-class tools execute without
   > per-operation user confirmation. Enable it only for trusted automation.

   **Helm 4 users:** the executable-path `--post-renderer <script>` form works on **Helm 3 only**.
   Helm ≥ 4 rejects it (`plugin: ... Type:postrenderer/v1 not found`) and requires the
   post-renderer packaged as a `postrenderer/v1` plugin. Prefer the image-level ENV path
   (option 1) there.
