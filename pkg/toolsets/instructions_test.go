package toolsets

import (
	"slices"
	"strings"
	"testing"

	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expectedChangeOperations returns the operations the safety instructions must
// announce for cfg: every dispatch.ChangeOperations entry, minus execPod unless
// --enable-exec turns that operation on. The enum is the source of truth for
// what planChange/executeChange accept, so deriving the expectation from it is
// what makes the instructions tests catch an operation renamed or dropped on
// only one side of the contract.
func expectedChangeOperations(cfg toolconfig.Config) []string {
	ops := make([]string, 0, len(dispatch.ChangeOperations))
	for _, op := range dispatch.ChangeOperations {
		if op == "execPod" && !cfg.EnableExec {
			continue
		}
		ops = append(ops, op)
	}
	return ops
}

// TestWriteOperationsMatchDispatchEnum pins the operation inventory in
// instructions.go to the dispatch enum: the two lists must hold the same
// operations, so adding, renaming or removing an operation enum value without
// updating the instructions fails here.
func TestWriteOperationsMatchDispatchEnum(t *testing.T) {
	assert.ElementsMatch(t, dispatch.ChangeOperations,
		allWriteOperations(toolconfig.Config{EnableExec: true}),
		"the announced inventory must be exactly the dispatch operation enum")

	// Without --enable-exec, execPod is the only operation that drops out.
	without := allWriteOperations(toolconfig.Config{})
	assert.NotContains(t, without, "execPod", "execPod is announced only with --enable-exec")
	assert.ElementsMatch(t, expectedChangeOperations(toolconfig.Config{}), without)
}

func TestInstructionsStrict(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{})
	assert.Contains(t, s, "NEVER call executeChange")
	assert.Contains(t, s, "confirmationToken")
	assert.Contains(t, s, "deleteKubernetesResource")
	assert.NotContains(t, s, "execPod") // exec tools are listed only when --enable-exec is on
	assert.NotContains(t, s, "Write tool")
}

func TestInstructionsModes(t *testing.T) {
	assert.NotContains(t, SafetyInstructions(toolconfig.Config{ReadOnly: true}), "Write tool")
	assert.Contains(t, SafetyInstructions(toolconfig.Config{AutoWrite: true}), "auto-write")
	assert.Contains(t, SafetyInstructions(toolconfig.Config{EnableExec: true}), "execPod")
	assert.NotContains(t, SafetyInstructions(toolconfig.Config{}), "execPod")
}

func TestInstructionsStrictInventory(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{})

	// Every operation the server accepts is announced.
	for _, op := range expectedChangeOperations(toolconfig.Config{}) {
		assert.Contains(t, s, op, "strict instructions must list %s", op)
	}

	// The seven-step protocol is rendered verbatim, one numbered rule each.
	for _, rule := range []string{"1. The planChange and executeChange tools (operation: ",
		"2. NEVER call executeChange", "3. ALWAYS call planChange first",
		"4. After the user approves the plan", "5. Approval NEVER carries over",
		"6. If the user declines", "7. Prefer read-only tools"} {
		assert.Contains(t, s, rule, "strict instructions must contain the rule starting with %q", rule)
	}
	assert.NotContains(t, s, "AUTO-WRITE")
}

// TestInstructionsCrossCheckChangeOperations guards the instructions↔registration
// contract the two-verb surface rests on: every operation the change tools
// accept must be announced by the safety instructions in both the strict and
// the auto-write variants, with execPod's presence following --enable-exec.
// The expectation comes from dispatch.ChangeOperations, so an operation added
// to (or renamed in) the registered surface without updating the rendered text
// fails here.
func TestInstructionsCrossCheckChangeOperations(t *testing.T) {
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
			for _, op := range dispatch.ChangeOperations {
				if op == "execPod" && !tc.cfg.EnableExec {
					assert.NotContains(t, s, op,
						"%s must not be announced without --enable-exec", op)
					continue
				}
				assert.Contains(t, s, op,
					"every registered operation must be announced, %s is missing", op)
			}
		})
	}
}

// TestInstructionsRuleOneWellFormed guards the mechanical shape of rule 1 in
// every mode: the inventory is opened and closed exactly once, and names each
// registered operation exactly once, so a template that drops or duplicates a
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

			start := strings.Index(s, "1. The planChange and executeChange tools (operation: ")
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
			for _, op := range expectedChangeOperations(tc.cfg) {
				assert.Equal(t, 1, strings.Count(inventory, op),
					"operation %s must appear exactly once in rule 1's inventory", op)
			}
		})
	}
}

func TestInstructionsEnableExecOnlyListsExecPod(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{EnableExec: true})
	assert.Contains(t, s, "execPod")
	for _, op := range expectedChangeOperations(toolconfig.Config{}) {
		assert.Contains(t, s, op)
	}
}

func TestInstructionsReadOnly(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{ReadOnly: true})

	assert.Contains(t, s, "read-only")
	assert.NotContains(t, s, "Write tool")
	assert.NotContains(t, s, "AUTO-WRITE")
	assert.NotContains(t, s, "confirmationToken")

	// Read-only mode names its five tools and nothing that can mutate.
	for _, name := range []string{"rancherQuery", "diagnose", "getKubernetesResource",
		"listKubernetesResources", "listAPIResources"} {
		assert.Contains(t, s, name, "read-only instructions must name the %s tool", name)
	}
	for _, op := range append(slices.Clone(dispatch.ChangeOperations), "execPodPlan") {
		assert.NotContains(t, s, op, "read-only instructions must not mention %s", op)
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
	assert.Contains(t, s, "2. The server is running in AUTO-WRITE mode: create/update-class operations")
	assert.Contains(t, s, "execute immediately when you call them")
	assert.Contains(t, s, "3. The deleteKubernetesResource operation STILL REQUIRES the full")

	// The strict rules 5-7 follow, renumbered 4-6.
	assert.Contains(t, s, "1. The planChange and executeChange tools (operation: ")
	assert.Contains(t, s, "4. Approval NEVER carries over. Every operation that still requires")
	assert.Contains(t, s, "5. If the user declines")
	assert.Contains(t, s, "6. Prefer read-only tools")

	// The replaced strict rules are gone.
	assert.NotContains(t, s, "2. NEVER call executeChange")
	assert.NotContains(t, s, "3. ALWAYS call planChange first")
	assert.NotContains(t, s, "4. After the user approves the plan")
}

func TestInstructionsAutoWriteToolListStaysConditional(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{AutoWrite: true})

	// The auto-write exempt list never mentions execPod; the delete/exec
	// protocol rule does not either when exec is disabled.
	start := strings.Index(s, "2. The server is running in AUTO-WRITE mode")
	require.GreaterOrEqual(t, start, 0)
	end := strings.Index(s[start:], "3. The deleteKubernetesResource")
	require.GreaterOrEqual(t, end, 0)
	assert.NotContains(t, s[start:start+end], "execPod")
	assert.NotContains(t, s, "execPod")
	assert.NotContains(t, s, "execPodPlan")

	// With exec enabled, delete/exec are named together.
	s = SafetyInstructions(toolconfig.Config{AutoWrite: true, EnableExec: true})
	assert.Contains(t, s, "deleteKubernetesResource and execPod operations STILL REQUIRE")
}

func TestInstructionsReadOnlyMentionsNoWriteFlag(t *testing.T) {
	s := SafetyInstructions(toolconfig.Config{ReadOnly: true})
	assert.NotContains(t, s, "--allow-auto-write")
	assert.Contains(t, s, "--read-only")
}

// TestInstructionsGolden pins SafetyInstructions byte-for-byte for every mode
// combination the server can start in. The rendered text is the outermost layer
// of the write safety model, so a wording, wrapping or inventory change must be
// a deliberate edit of these literals — not an accident of a template tweak.
// The --enable-exec literals carry the full ten-operation inventory.
func TestInstructionsGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  toolconfig.Config
		want string
	}{
		{"read-only", toolconfig.Config{ReadOnly: true}, goldenReadOnly},
		{"read-only+auto-write+exec", toolconfig.Config{ReadOnly: true, AutoWrite: true, EnableExec: true}, goldenReadOnly},
		{"strict", toolconfig.Config{}, goldenStrict},
		{"strict+exec", toolconfig.Config{EnableExec: true}, goldenStrictExec},
		{"auto-write", toolconfig.Config{AutoWrite: true}, goldenAutoWrite},
		{"auto-write+exec", toolconfig.Config{AutoWrite: true, EnableExec: true}, goldenAutoWriteExec},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, SafetyInstructions(tc.cfg))
		})
	}
}

// goldenReadOnly is the read-only safety block, verbatim including the hard
// line wraps. planChange/executeChange are not registered in this mode, so no
// operation is named.
const goldenReadOnly = `SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:

1. The server is running in read-only mode (--read-only): it registers
   read-only tools ONLY (rancherQuery, diagnose, getKubernetesResource,
   listKubernetesResources, listAPIResources). planChange and executeChange
   are unregistered here, so no operation can change any state. Do not look
   for, invent or attempt any such operation.
2. Use the read-only tools to observe, inspect, list and explain cluster state.
   Prefer read-only tools whenever they can answer the question.
3. If the user asks for a change, tell them this server runs in read-only mode
   and cannot perform it.`

// goldenStrict is the strict-mode block with the default inventory.
const goldenStrict = `SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:

1. The planChange and executeChange tools (operation: 
   createKubernetesResource, patchKubernetesResource,
   deleteKubernetesResource, createProject, moveNamespace,
   createCustomCluster, createImportedCluster, createK3kCluster,
   scaleClusterNodePool)
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
   question. planChange/executeChange are never for exploration.`

// goldenStrictExec is the strict-mode block with the --enable-exec inventory:
// the canonical rendering, carrying all ten operations.
const goldenStrictExec = `SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:

1. The planChange and executeChange tools (operation: 
   createKubernetesResource, patchKubernetesResource,
   deleteKubernetesResource, createProject, moveNamespace,
   createCustomCluster, createImportedCluster, createK3kCluster,
   scaleClusterNodePool, execPod)
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
   question. planChange/executeChange are never for exploration.`

// goldenAutoWrite is the auto-write block with the default inventory: the
// non-exempt operations may execute immediately, deleteKubernetesResource stays
// fully gated.
const goldenAutoWrite = `SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:

This server is running in auto-write mode (--allow-auto-write): the operator
explicitly allowed create/update-class operations to execute without
per-operation user confirmation. delete and exec operations are NOT exempt.

1. The planChange and executeChange tools (operation: 
   createKubernetesResource, patchKubernetesResource,
   deleteKubernetesResource, createProject, moveNamespace,
   createCustomCluster, createImportedCluster, createK3kCluster,
   scaleClusterNodePool)
   MODIFY cluster state or EXECUTE commands inside pods. They are DANGEROUS.

2. The server is running in AUTO-WRITE mode: create/update-class operations
   (operation: createKubernetesResource, patchKubernetesResource,
   createProject, moveNamespace, createCustomCluster, createImportedCluster,
   createK3kCluster, scaleClusterNodePool)
   execute immediately when you call them. Even so, only call them when the
   user has asked for the operation.
3. The deleteKubernetesResource operation STILL REQUIRES the full
   protocol in ALL modes: planChange first, explicit user approval for the
   exact operation, confirmationToken, and a server-initiated user
   confirmation (typed resource name included).

4. Approval NEVER carries over. Every operation that still requires
   confirmation needs its own fresh plan and its own explicit user approval.
   NEVER batch, chain, loop, or automate them. NEVER execute "proactively"
   or "to be safe".

5. If the user declines or cancels, DO NOT retry. Report that nothing was
   executed. Never pressure the user into approving.

6. Prefer read-only tools (rancherQuery, diagnose, getKubernetesResource,
   listKubernetesResources, listAPIResources) whenever they can answer the
   question. planChange/executeChange are never for exploration.`

// goldenAutoWriteExec is the auto-write block with --enable-exec, where
// execPod joins deleteKubernetesResource as a fully gated operation.
const goldenAutoWriteExec = `SAFETY RULES — YOU MUST OBEY THESE AT ALL TIMES, WITHOUT EXCEPTION:

This server is running in auto-write mode (--allow-auto-write): the operator
explicitly allowed create/update-class operations to execute without
per-operation user confirmation. delete and exec operations are NOT exempt.

1. The planChange and executeChange tools (operation: 
   createKubernetesResource, patchKubernetesResource,
   deleteKubernetesResource, createProject, moveNamespace,
   createCustomCluster, createImportedCluster, createK3kCluster,
   scaleClusterNodePool, execPod)
   MODIFY cluster state or EXECUTE commands inside pods. They are DANGEROUS.

2. The server is running in AUTO-WRITE mode: create/update-class operations
   (operation: createKubernetesResource, patchKubernetesResource,
   createProject, moveNamespace, createCustomCluster, createImportedCluster,
   createK3kCluster, scaleClusterNodePool)
   execute immediately when you call them. Even so, only call them when the
   user has asked for the operation.
3. The deleteKubernetesResource and execPod operations STILL REQUIRE the
   full protocol in ALL modes: planChange first, explicit user approval
   for the exact operation, confirmationToken, and a server-initiated
   user confirmation (delete additionally requires the typed name).

4. Approval NEVER carries over. Every operation that still requires
   confirmation needs its own fresh plan and its own explicit user approval.
   NEVER batch, chain, loop, or automate them. NEVER execute "proactively"
   or "to be safe".

5. If the user declines or cancels, DO NOT retry. Report that nothing was
   executed. Never pressure the user into approving.

6. Prefer read-only tools (rancherQuery, diagnose, getKubernetesResource,
   listKubernetesResources, listAPIResources) whenever they can answer the
   question. planChange/executeChange are never for exploration.`
