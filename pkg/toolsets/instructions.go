package toolsets

import (
	"fmt"
	"strings"

	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
)

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

// allWriteOperations lists every mutating operation announced for cfg. execPod
// exists only with --enable-exec; nothing in this list is available in
// read-only mode, where planChange/executeChange are not registered at all.
func allWriteOperations(cfg toolconfig.Config) []string {
	names := append([]string{}, writeOperations...)
	if cfg.EnableExec {
		names = append(names, "execPod")
	}
	return names
}

// toolListWidth is the column budget the write operation inventory is wrapped
// into.
const toolListWidth = 77

// renderToolList renders the write operation inventory as rule 1 of the safety
// instructions: firstPrefix opens the first line (the "1. The planChange and
// executeChange tools (operation: " of the rule), the inventory continues on
// 3-space indented continuation lines, and the list closes with ")". The prefix
// and the closing paren both count against toolListWidth, which is what makes
// the rendered rule match the spec's hard-wrapped template exactly for both the
// default and the --enable-exec inventories.
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
   read-only tools ONLY (rancherQuery, diagnose, getKubernetesResource,
   listKubernetesResources, listAPIResources). planChange and executeChange
   are unregistered here, so no operation can change any state. Do not look
   for, invent or attempt any such operation.
2. Use the read-only tools to observe, inspect, list and explain cluster state.
   Prefer read-only tools whenever they can answer the question.
3. If the user asks for a change, tell them this server runs in read-only mode
   and cannot perform it.`
}

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

func autoWriteInstructions(cfg toolconfig.Config) string {
	ops := renderToolList(allWriteOperations(cfg), "1. The planChange and executeChange tools (operation: ")

	// The delete/exec protocol rule names exactly the operations that are
	// exempt from auto-write and available in this configuration.
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

	var b strings.Builder
	b.WriteString("SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:\n\n")
	b.WriteString("This server is running in auto-write mode (--allow-auto-write): the operator\n" +
		"explicitly allowed create/update-class operations to execute without\n" +
		"per-operation user confirmation. delete and exec operations are NOT exempt.\n\n")
	fmt.Fprintf(&b, `%s
   MODIFY cluster state or EXECUTE commands inside pods. They are DANGEROUS.

2. The server is running in AUTO-WRITE mode: create/update-class operations
   (operation: createKubernetesResource, patchKubernetesResource,
   createProject, createCustomCluster, createImportedCluster, createK3kCluster,
   scaleClusterNodePool) execute immediately when you call them. Even so,
   only call them when the user has asked for the operation.
%s

4. Approval NEVER carries over. Every operation that still requires
   confirmation needs its own fresh plan and its own explicit user approval.
   NEVER batch, chain, loop, or automate them. NEVER execute "proactively"
   or "to be safe".

5. If the user declines or cancels, DO NOT retry. Report that nothing was
   executed. Never pressure the user into approving.

6. Prefer read-only tools (rancherQuery, diagnose, getKubernetesResource,
   listKubernetesResources, listAPIResources) whenever they can answer the
   question. planChange/executeChange are never for exploration.`, ops, guardRule)

	return b.String()
}
