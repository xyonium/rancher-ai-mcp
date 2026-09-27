package projects

import (
	"iter"
	"maps"
	"slices"
	"testing"

	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
)

// testCfg builds the config the merged-case tests need: a fake gate, so the
// plan/execute handlers a case table registers have a usable confirmation gate.
func testCfg(t *testing.T) toolconfig.Config {
	t.Helper()
	return toolconfig.Config{Gate: fakeGates(t, nil)}
}

func TestMergedCaseKeys(t *testing.T) {
	tools := NewTools(nil, testCfg(t))
	if got := maps.Keys(tools.QueryCases()); !equalKeys(got, "project", "projects", "resourceUsage") {
		t.Errorf("query cases = %v", got)
	}
	if got := maps.Keys(tools.PlanCases()); !equalKeys(got, "createProject") {
		t.Errorf("plan cases = %v", got)
	}
	if got := maps.Keys(tools.ExecuteCases()); !equalKeys(got, "createProject") {
		t.Errorf("execute cases = %v", got)
	}
	if !slices.Equal(tools.PlanCases()["createProject"].Required, []string{"cluster", "name"}) {
		t.Errorf("createProject required = %v", tools.PlanCases()["createProject"].Required)
	}
}

func equalKeys(got iter.Seq[string], want ...string) bool {
	var g []string
	for k := range got {
		g = append(g, k)
	}
	slices.Sort(g)
	slices.Sort(want)
	return slices.Equal(g, want)
}
