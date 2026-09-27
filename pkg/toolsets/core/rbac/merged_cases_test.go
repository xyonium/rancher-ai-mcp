package rbac

import "testing"

func TestQueryCasesKeys(t *testing.T) {
	tools := NewTools(nil, true)
	cases := tools.QueryCases()
	want := []string{"user", "roleTemplate", "roleTemplates", "clusterRTBs", "projectRTBs"}
	if len(cases) != len(want) {
		t.Fatalf("rbac owns %d query cases, got %d", len(want), len(cases))
	}
	for _, k := range want {
		if _, ok := cases[k]; !ok {
			t.Errorf("missing query case %q", k)
		}
	}
	if got := cases["user"].Required; len(got) != 1 || got[0] != "name" {
		t.Errorf("user required = %v, want [name]", got)
	}
	if got := cases["gitRepo"]; got.Handler != nil {
		t.Error("rbac must not own fleet cases")
	}
}
