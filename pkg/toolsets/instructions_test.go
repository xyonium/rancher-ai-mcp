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
