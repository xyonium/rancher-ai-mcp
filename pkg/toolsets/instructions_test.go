package toolsets

import (
	"strings"
	"testing"

	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testWriteTools are the mutating tools the strict instructions are built around.
var testWriteTools = []string{
	"createKubernetesResource",
	"patchKubernetesResource",
	"deleteKubernetesResource",
	"createProject",
	"createCustomCluster",
	"createImportedCluster",
	"createK3kCluster",
	"scaleClusterNodePool",
}

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

func TestInstructionsStrictInventory(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{})

	// Every always-registered write tool is announced.
	for _, name := range testWriteTools {
		assert.Contains(t, s, name, "strict instructions must list %s", name)
	}

	// The seven-step protocol is rendered verbatim, one numbered rule each.
	for _, rule := range []string{"1. Tools marked as Write", "2. NEVER call a Write tool",
		"3. ALWAYS call the corresponding Plan tool", "4. After the user approves the plan",
		"5. Approval NEVER carries over", "6. If the user declines", "7. Prefer read-only tools"} {
		assert.Contains(t, s, rule, "strict instructions must contain the rule starting with %q", rule)
	}
	assert.NotContains(t, s, "AUTO-WRITE")
}

// TestInstructionsRuleOneWellFormed guards the mechanical shape of rule 1 in
// every mode: the inventory is opened and closed exactly once, and names each
// registered write tool exactly once, so a template that drops or duplicates a
// paren or an entry cannot ship.
func TestInstructionsRuleOneWellFormed(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  toolconfig.Config
	}{
		{"strict", toolconfig.Config{}},
		{"strict+exec", toolconfig.Config{EnableExec: true}},
		{"autowrite", toolconfig.Config{AutoWrite: true}},
		{"autowrite+exec", toolconfig.Config{AutoWrite: true, EnableExec: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := SafetyInstructions(tc.cfg)
			assert.NotContains(t, s, "))", "rule 1 must not close the inventory twice")

			start := strings.Index(s, "1. Tools marked as Write (")
			require.GreaterOrEqual(t, start, 0, "rule 1 must be present")
			rest := s[start:]

			// Rule 1 ends at the first blank line after the inventory.
			end := strings.Index(rest, "\n\n")
			require.GreaterOrEqual(t, end, 0)
			rule1 := rest[:end]
			assert.Equal(t, 1, strings.Count(rule1, "("), "rule 1 must open the inventory once")
			assert.Equal(t, 1, strings.Count(rule1, ")"), "rule 1 must close the inventory once")

			// The inventory closes at the end of its own wrapped line, before
			// the MODIFY explanation that follows within rule 1.
			inventory := rule1[:strings.Index(rule1, ")")]
			after := rule1[strings.Index(rule1, ")")+1:]
			assert.True(t, strings.HasPrefix(after, "\n"), "the inventory close paren must end its line")
			names := append([]string{}, testWriteTools...)
			if tc.cfg.EnableExec {
				names = append(names, "execPod")
			}
			for _, name := range names {
				assert.Equal(t, 1, strings.Count(inventory, name),
					"tool %s must appear exactly once in rule 1's inventory", name)
			}
		})
	}
}

func TestInstructionsEnableExecOnlyListsExecPod(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{EnableExec: true})
	assert.Contains(t, s, "execPod")
	for _, name := range testWriteTools {
		assert.Contains(t, s, name)
	}
}

func TestInstructionsReadOnly(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{ReadOnly: true})

	assert.Contains(t, s, "read-only")
	assert.NotContains(t, s, "Write tool")
	assert.NotContains(t, s, "AUTO-WRITE")
	assert.NotContains(t, s, "confirmationToken")

	// No mutating tool may be named in read-only mode.
	for _, name := range append(testWriteTools, "execPod", "execPodPlan") {
		assert.NotContains(t, s, name, "read-only instructions must not mention %s", name)
	}
}

func TestInstructionsReadOnlyWinsOverAutoWrite(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{ReadOnly: true, AutoWrite: true, EnableExec: true})
	assert.NotContains(t, s, "AUTO-WRITE")
	assert.NotContains(t, s, "execPod")
}

func TestInstructionsAutoWriteStructure(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{AutoWrite: true})

	// The auto-write paragraphs are rendered verbatim.
	assert.Contains(t, s, "2. The server is running in AUTO-WRITE mode: create/update-class tools")
	assert.Contains(t, s, "execute immediately when you call them")
	assert.Contains(t, s, "deleteKubernetesResource STILL REQUIRES the full protocol in")

	// The strict rules 5-7 follow, renumbered 4-6.
	assert.Contains(t, s, "1. Tools marked as Write")
	assert.Contains(t, s, "4. Approval NEVER carries over. Every operation that still requires")
	assert.Contains(t, s, "5. If the user declines")
	assert.Contains(t, s, "6. Prefer read-only tools")

	// The replaced strict rules are gone.
	assert.NotContains(t, s, "2. NEVER call a Write tool")
	assert.NotContains(t, s, "3. ALWAYS call the corresponding Plan tool")
	assert.NotContains(t, s, "4. After the user approves the plan")
}

func TestInstructionsAutoWriteToolListStaysConditional(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{AutoWrite: true})

	// The auto-write create/update list never mentions execPod; the delete/exec
	// protocol rule does not either when exec is disabled.
	start := strings.Index(s, "2. The server is running in AUTO-WRITE mode")
	require.GreaterOrEqual(t, start, 0)
	end := strings.Index(s[start:], "3. deleteKubernetesResource")
	require.GreaterOrEqual(t, end, 0)
	assert.NotContains(t, s[start:start+end], "execPod")
	assert.NotContains(t, s, "execPod")
	assert.NotContains(t, s, "createK3kClusterPlan")

	// With exec enabled, delete/exec are named together.
	s = SafetyInstructions(toolconfig.Config{AutoWrite: true, EnableExec: true})
	assert.Contains(t, s, "deleteKubernetesResource and execPod STILL REQUIRE")
}

func TestInstructionsReadOnlyMentionsNoWriteFlag(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{ReadOnly: true})
	assert.NotContains(t, s, "--allow-auto-write")
	assert.Contains(t, s, "--read-only")
}

// specStrictInstructions is the strict-mode safety block from the design spec
// (docs/superpowers/specs/2026-09-15-cr-support-and-safe-mutations-design.md §5.2),
// reproduced byte-for-byte, including the em-dashes, the quoting and the hard
// line wraps. It is the verbatim contract this package must keep emitting.
const specStrictInstructions = `SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:

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
   are never for exploration.`

// TestInstructionsStrictGolden pins the strict text to the spec byte-for-byte.
// The --enable-exec inventory matches the spec's nine-tool block exactly, so
// that configuration must equal the golden literal with nothing normalised.
func TestInstructionsStrictGolden(t *testing.T) {
	got := SafetyInstructions(toolconfig.Config{EnableExec: true})
	if got != specStrictInstructions {
		assert.Equal(t, specStrictInstructions, got, "strict text with --enable-exec must match spec §5.2 byte-for-byte")
	}

	// The default configuration registers the same tools minus execPod, so its
	// rendering is the golden literal with that one inventory entry removed.
	// The spec hard-wraps the nine-tool list across three lines; dropping one
	// tool reflows the tail, so the normalisation is expressed on the rendered
	// tool list only (rule 1), leaving rules 2-7 under the byte-exact check.
	want := strings.Replace(specStrictInstructions,
		"   createImportedCluster, createK3kCluster, scaleClusterNodePool, execPod)",
		"   createImportedCluster, createK3kCluster, scaleClusterNodePool)", 1)
	got = SafetyInstructions(toolconfig.Config{})
	if got != want {
		assert.Equal(t, want, got, "default strict text must match spec §5.2 minus the execPod inventory entry")
	}
}
