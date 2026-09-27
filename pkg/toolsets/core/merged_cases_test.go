package core

import (
	"testing"

	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

func testCfg() toolconfig.Config { return toolconfig.Config{ReadOnly: true} }

func TestQueryCasesKeys(t *testing.T) {
	tools := NewTools(nil, testCfg()) // reuse the cfg helper used by existing core tests; if none, toolconfig.Config{ReadOnly: true}
	cases := tools.QueryCases()
	for _, want := range []string{"clusters", "clusterImages"} {
		if _, ok := cases[want]; !ok {
			t.Errorf("missing query case %q", want)
		}
	}
	if len(cases) != 2 {
		t.Errorf("core owns exactly 2 query cases, got %d", len(cases))
	}
}

func TestDiagnoseCasesKeys(t *testing.T) {
	tools := NewTools(nil, testCfg())
	cases := tools.DiagnoseCases()
	for _, want := range []string{"nodes", "deployment", "pod"} {
		if _, ok := cases[want]; !ok {
			t.Errorf("missing diagnose case %q", want)
		}
	}
	if len(cases) != 3 {
		t.Errorf("core owns exactly 3 diagnose cases, got %d", len(cases))
	}
	req := cases["deployment"].Required
	if len(req) != 3 || req[0] != "cluster" || req[1] != "namespace" || req[2] != "name" {
		t.Errorf("deployment required = %v, want [cluster namespace name]", req)
	}
}

var _ = dispatch.QueryParams{} // keep import if unused above
