#!/usr/bin/env bash
# Helm post-renderer: appends fork flags to the mcp Deployment rendered by the
# stock rancher-ai-agent chart. Usage:
#   helm upgrade -i rancher-ai-agent oci://registry.suse.com/rancher/charts/rancher-ai-agent \
#     -n cattle-ai-agent-system -f my-values.yaml --post-renderer deploy/postrenderer/patch-args.sh
#
# Helm 3 ONLY: Helm >= 4 rejects the executable-path --post-renderer form with
# "plugin: ... Type:postrenderer/v1 not found" and requires the post-renderer
# packaged as a postrenderer/v1 plugin. Use the image-level ENV path instead.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cat > "${DIR}/rendered.yaml"
kubectl kustomize "${DIR}" 2>/dev/null || kustomize build "${DIR}"
