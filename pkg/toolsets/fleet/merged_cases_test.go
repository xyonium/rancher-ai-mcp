package fleet

import "testing"

func TestMergedCaseKeys(t *testing.T) {
	tools := NewTools(nil)
	q := tools.QueryCases()
	for _, k := range []string{"gitRepo", "gitRepos", "bundle"} {
		if _, ok := q[k]; !ok {
			t.Errorf("missing query case %q", k)
		}
	}
	if got := q["gitRepo"].Required; len(got) != 2 || got[0] != "workspace" || got[1] != "name" {
		t.Errorf("gitRepo required = %v, want [workspace name]", got)
	}
	d := tools.DiagnoseCases()
	if len(d) != 1 {
		t.Fatalf("fleet owns exactly 1 diagnose case, got %d", len(d))
	}
	if got := d["fleet"].Required; len(got) != 1 || got[0] != "workspace" {
		t.Errorf("fleet required = %v, want [workspace]", got)
	}
}
