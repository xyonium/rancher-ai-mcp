package toolsets

import (
	"fmt"
	"strings"

	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
)

// writeTools lists every mutating tool this server can register, in the order
// the instruction templates reference them.
var writeTools = []string{
	"createKubernetesResource",
	"patchKubernetesResource",
	"deleteKubernetesResource",
	"createProject",
	"createCustomCluster",
	"createImportedCluster",
	"createK3kCluster",
	"scaleClusterNodePool",
}

// SafetyInstructions returns the server-level instructions sent to the MCP
// client at handshake. They are the outermost layer of the write safety model,
// so they always describe the tools that are really registered.
func SafetyInstructions(cfg toolconfig.Config) string {
	switch {
	case cfg.ReadOnly:
		return readOnlyInstructions()
	case cfg.AutoWrite:
		return autoWriteInstructions(cfg)
	default:
		return strictInstructions(cfg)
	}
}

// allWriteTools lists every write tool registered for cfg. execPod exists only
// with --enable-exec; nothing in this list is registered in read-only mode.
func allWriteTools(cfg toolconfig.Config) []string {
	names := append([]string{}, writeTools...)
	if cfg.EnableExec {
		names = append(names, "execPod")
	}
	return names
}

// toolListWidth is the column budget the write tool inventory is wrapped into.
const toolListWidth = 77

// renderToolList renders the write tool inventory as rule 1 of the safety
// instructions: firstPrefix opens the first line (the "1. Tools marked as
// Write (" of the rule), the inventory continues on 3-space indented
// continuation lines, and the list closes with ")". The prefix and the closing
// paren both count against toolListWidth, which is what makes the rendered
// rule match the spec's hard-wrapped template exactly for both the default and
// the --enable-exec inventories.
func renderToolList(names []string, firstPrefix string) string {
	var b strings.Builder
	line := firstPrefix
	lineHasItems := false
	for i, name := range names {
		item := name + ","
		if i == len(names)-1 {
			item = name + ")"
		}
		sep := ""
		if lineHasItems {
			sep = " "
		}
		if len(line)+len(sep)+len(item) > toolListWidth {
			b.WriteString(line)
			b.WriteString("\n")
			line, lineHasItems = "   ", false
			sep = ""
		}
		line += sep + item
		lineHasItems = true
	}
	b.WriteString(line)
	return b.String()
}

func readOnlyInstructions() string {
	return `SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:

1. The server is running in read-only mode (--read-only): it registers
   read-only tools ONLY. Every tool that creates, modifies, deletes or
   executes anything is unregistered here, so no operation can change any
   state. Do not look for, invent or attempt any such operation.
2. Use the read-only tools to observe, inspect, list and explain cluster state.
   Prefer read-only tools whenever they can answer the question.
3. If the user asks for a change, tell them this server runs in read-only mode
   and cannot perform it.`
}

func strictInstructions(cfg toolconfig.Config) string {
	tools := renderToolList(allWriteTools(cfg), "1. Tools marked as Write (")

	var b strings.Builder
	b.WriteString("SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:\n\n")
	fmt.Fprintf(&b, `%s
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
   are never for exploration.`, tools)

	return b.String()
}

func autoWriteInstructions(cfg toolconfig.Config) string {
	tools := renderToolList(allWriteTools(cfg), "1. Tools marked as Write (")

	// The delete/exec protocol rule names exactly the tools that are exempt
	// from auto-write and actually registered.
	guardRule := "3. deleteKubernetesResource STILL REQUIRES the full protocol in\n" +
		"   ALL modes: Plan tool first, explicit user approval for the exact\n" +
		"   operation, confirmationToken, and a server-initiated user confirmation."
	if cfg.EnableExec {
		guardRule = "3. deleteKubernetesResource and execPod STILL REQUIRE the full protocol in\n" +
			"   ALL modes: Plan tool first, explicit user approval for the exact\n" +
			"   operation, confirmationToken, and a server-initiated user confirmation."
	}

	var b strings.Builder
	b.WriteString("SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:\n\n")
	b.WriteString("This server is running in auto-write mode (--allow-auto-write): the operator\n" +
		"explicitly allowed create/update-class tools to execute without per-operation\n" +
		"user confirmation. delete and exec tools are NOT exempt.\n\n")
	fmt.Fprintf(&b, `%s
   MODIFY cluster state or EXECUTE commands inside pods. They are DANGEROUS.

2. The server is running in AUTO-WRITE mode: create/update-class tools
   (createKubernetesResource, patchKubernetesResource, createProject,
   createCustomCluster, createImportedCluster, createK3kCluster,
   scaleClusterNodePool) execute immediately when you call them. Even so,
   only call them when the user has asked for the operation.
%s

4. Approval NEVER carries over. Every operation that still requires
   confirmation needs its own fresh plan and its own explicit user approval.
   NEVER batch, chain, loop, or automate them. NEVER execute a Write
   "proactively" or "to be safe".

5. If the user declines or cancels, DO NOT retry. Report that nothing was
   executed. Never pressure the user into approving.

6. Prefer read-only tools whenever they can answer the question. Write tools
   are never for exploration.`, tools, guardRule)

	return b.String()
}
