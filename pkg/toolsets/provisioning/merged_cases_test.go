package provisioning

import (
	"testing"

	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
)

// testCfg builds the config the merged-case tests need: a fake gate, so the
// plan/execute handlers a case table registers have a usable confirmation gate.
func testCfg(t *testing.T) toolconfig.Config {
	t.Helper()
	return toolconfig.Config{Gate: fakeGates(t, nil)}
}

func TestMergedCaseKeys(t *testing.T) {
	tools := NewTools(nil, testCfg(t))
	if got := len(tools.QueryCases()); got != 3 {
		t.Errorf("provisioning owns 3 query cases, got %d", got)
	}
	for _, k := range []string{"clusterMachine", "k3kClusters", "supportedVersions"} {
		if _, ok := tools.QueryCases()[k]; !ok {
			t.Errorf("missing query case %q", k)
		}
	}
	if got := len(tools.DiagnoseCases()); got != 2 {
		t.Errorf("provisioning owns 2 diagnose cases, got %d", got)
	}
	plan := tools.PlanCases()
	exec := tools.ExecuteCases()
	for _, k := range []string{"createCustomCluster", "createImportedCluster", "createK3kCluster", "scaleClusterNodePool"} {
		if _, ok := plan[k]; !ok {
			t.Errorf("missing plan case %q", k)
		}
		if _, ok := exec[k]; !ok {
			t.Errorf("missing execute case %q", k)
		}
	}
	if got := plan["createCustomCluster"].Required; len(got) != 4 {
		t.Errorf("createCustomCluster required = %v, want 4 entries [name CNI version distribution]", got)
	}
	if got := plan["scaleClusterNodePool"].Required; len(got) != 3 || got[0] != "cluster" || got[1] != "namespace" || got[2] != "nodePoolName" {
		t.Errorf("scaleClusterNodePool required = %v", got)
	}
}

// TestConvertersForwardConfirmationToken guards the security invariant of the
// merged change tools: the single-use token must reach the gated handler
// verbatim, never dropped or rewritten in translation.
func TestConvertersForwardConfirmationToken(t *testing.T) {
	const token = "single-use-token"
	p := dispatch.ChangeParams{
		Name: "n", Cluster: "c", Namespace: "ns", ConfirmationToken: token,
	}
	if got := customClusterParams(p).ConfirmationToken; got != token {
		t.Errorf("customClusterParams token = %q, want %q", got, token)
	}
	if got := importedClusterParams(p).ConfirmationToken; got != token {
		t.Errorf("importedClusterParams token = %q, want %q", got, token)
	}
	if got := k3kClusterParams(p).ConfirmationToken; got != token {
		t.Errorf("k3kClusterParams token = %q, want %q", got, token)
	}
	if got := scaleParams(p).ConfirmationToken; got != token {
		t.Errorf("scaleParams token = %q, want %q", got, token)
	}
}

// TestK3kClusterParamsNestedMapping checks the flat merged params land in the
// nested k3k struct fields field-by-field.
func TestK3kClusterParamsNestedMapping(t *testing.T) {
	got := k3kClusterParams(dispatch.ChangeParams{
		Name: "k3k", Namespace: "ns", TargetCluster: "down", Version: "v1.33.1-k3s1",
		Mode: "shared", Servers: 3, Agents: 2,
		Sync:        dispatch.K3kSync{PriorityClasses: true, Ingresses: true},
		Persistence: dispatch.K3kPersistence{Type: "pvc", StorageClassName: "sc", StorageRequest: "5Gi"},
		ServerLimit: dispatch.K3kLimits{CPU: "1", Memory: "2Gi"},
		WorkerLimit: dispatch.K3kLimits{CPU: "500m", Memory: "512Mi"},
	})

	if got.Persistence != (PersistenceConfig{Type: "pvc", StorageClassName: "sc", StorageRequest: "5Gi"}) {
		t.Errorf("persistence = %+v", got.Persistence)
	}
	if got.Sync != (SyncConfig{PriorityClasses: true, Ingresses: true}) {
		t.Errorf("sync = %+v", got.Sync)
	}
	if got.ServerLimit != (ResourceLimits{CPU: "1", Memory: "2Gi"}) {
		t.Errorf("serverLimit = %+v", got.ServerLimit)
	}
	if got.WorkerLimit != (ResourceLimits{CPU: "500m", Memory: "512Mi"}) {
		t.Errorf("workerLimit = %+v", got.WorkerLimit)
	}
	if got.Servers != 3 || got.Agents != 2 || got.Mode != "shared" {
		t.Errorf("scalar fields = %+v", got)
	}
}

// TestScaleParamsMapping checks the scale converter copies every scalar field.
func TestScaleParamsMapping(t *testing.T) {
	got := scaleParams(dispatch.ChangeParams{
		Cluster: "c", Namespace: "ns", NodePoolName: "pool",
		DesiredSize: 5, AmountToAdd: 1, AmountToSubtract: 0,
	})
	want := scaleNodePoolParameters{
		Cluster: "c", Namespace: "ns", NodePoolName: "pool",
		DesiredSize: 5, AmountToAdd: 1, AmountToSubtract: 0,
	}
	if got != want {
		t.Errorf("scaleParams = %+v, want %+v", got, want)
	}
}
