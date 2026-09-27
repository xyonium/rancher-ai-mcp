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

// phasePlan and phaseExecute are the two change phases, used by caseMapFor.
const (
	phasePlan    = "plan"
	phaseExecute = "execute"
)

// caseMapFor selects the dispatch table of a change phase. This is the single
// place where planChange and executeChange pick their cases, so the binding is
// one reviewed line of code instead of two literal arguments buried in
// Register — and a test can pin it. Returns nil for an unknown phase: Register
// only ever passes the constants above, and a nil map would surface as an
// "unknown operation" error rather than silently planning.
func caseMapFor(phase string, m caseMaps) map[string]dispatch.Case[dispatch.ChangeParams] {
	switch phase {
	case phasePlan:
		return m.plan
	case phaseExecute:
		return m.execute
	default:
		return nil
	}
}

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
			return dispatch.Dispatch(ctx, req, "operation", p.Operation, p, caseMapFor(phasePlan, cases))
		},
	)

	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "executeChange",
		Meta:        map[string]any{toolsSetAnn: "merged"},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr.To(true), IdempotentHint: false, OpenWorldHint: ptr.To(false)},
		InputSchema: dispatch.ExecuteInputSchema(!cfg.AutoWrite),
		Description: toolconfig.SecurityProtocol(cfg, `SECURITY: This tool CHANGES cluster state or EXECUTES a command in a pod. Protocol, no exceptions: (1) Call planChange first with the same operation and parameters and show the user the complete returned plan. (2) Obtain the user's EXPLICIT approval for THIS EXACT change. (3) Call this tool with the confirmationToken from the plan response. The server then asks the USER DIRECTLY to confirm — you cannot and MUST NOT answer on their behalf. Approval never carries over to any other call; never execute proactively or in batches. The deleteKubernetesResource and execPod operations ALWAYS require this protocol, even in auto-write mode; deleteKubernetesResource additionally asks the user to type the resource name.`) + `

Executes one change selected by operation (same values and required parameters as planChange). The confirmationToken binds the exact operation and parameters: reusing a token across operations or parameters fails.`},
		func(ctx context.Context, req *mcp.CallToolRequest, p dispatch.ChangeParams) (*mcp.CallToolResult, any, error) {
			return dispatch.Dispatch(ctx, req, "operation", p.Operation, p, caseMapFor(phaseExecute, cases))
		},
	)
}
