package provisioning

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/dispatch"
	provisioningV1 "github.com/rancher/rancher/pkg/apis/provisioning.cattle.io/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
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

// TestConverterIdentityFieldMapping pins the identity-bearing fields of every
// provisioning converter field-by-field. The converters were proven
// mutation-sensitive by review, so each struct is compared whole: a transposed
// or dropped assignment (Namespace↔TargetCluster, CNI↔Version, ...) fails.
func TestConverterIdentityFieldMapping(t *testing.T) {
	t.Run("custom cluster", func(t *testing.T) {
		got := customClusterParams(dispatch.ChangeParams{
			Name: "custom", Description: "desc", CNI: "calico", Version: "v1.33.1+rke2r1", Distribution: "rke2",
		})
		want := createCustomClusterParams{
			Name: "custom", Description: "desc", CNI: "calico", Version: "v1.33.1+rke2r1", Distribution: "rke2",
		}
		if got != want {
			t.Errorf("customClusterParams = %+v, want %+v", got, want)
		}
	})

	t.Run("imported cluster", func(t *testing.T) {
		got := importedClusterParams(dispatch.ChangeParams{
			Name: "imported", Description: "desc", VersionManagementSetting: "false",
		})
		want := createImportedClusterParams{
			Name: "imported", Description: "desc", VersionManagementSetting: "false",
		}
		if got != want {
			t.Errorf("importedClusterParams = %+v, want %+v", got, want)
		}
	})

	t.Run("k3k cluster identity", func(t *testing.T) {
		// Namespace and TargetCluster are distinct fields that a transposition
		// would swap; Name/Version are the other identity strings.
		got := k3kClusterParams(dispatch.ChangeParams{
			Name: "k3k", Namespace: "k3k-ns", TargetCluster: "down",
			Version: "v1.33.1-k3s1", Mode: "virtual", Servers: 3, Agents: 2,
		})
		want := createK3kClusterParams{
			Name: "k3k", Namespace: "k3k-ns", TargetCluster: "down",
			Version: "v1.33.1-k3s1", Mode: "virtual", Servers: 3, Agents: 2,
		}
		if got != want {
			t.Errorf("k3kClusterParams = %+v, want %+v", got, want)
		}
	})

	t.Run("scale scalars", func(t *testing.T) {
		got := scaleParams(dispatch.ChangeParams{
			Cluster: "c", Namespace: "ns", NodePoolName: "pool",
			DesiredSize: 7, AmountToAdd: 2, AmountToSubtract: 1,
		})
		want := scaleNodePoolParameters{
			Cluster: "c", Namespace: "ns", NodePoolName: "pool",
			DesiredSize: 7, AmountToAdd: 2, AmountToSubtract: 1,
		}
		if got != want {
			t.Errorf("scaleParams = %+v, want %+v", got, want)
		}
	})
}

// TestProvisioningPhaseInversion pins that each provisioning operation maps to
// the plan handler in the plan table and the execute handler in the execute
// table — never the other way round. The two phases are observationally
// distinct: the plan handler issues a token and patches nothing, the execute
// handler consumes that token and patches the cluster once.
func TestProvisioningPhaseInversion(t *testing.T) {
	tools, dyn := newScalingCaseTools(t)
	req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: "scaleClusterNodePool"}}

	// The plan case returns a plan and touches nothing.
	res, _, err := tools.PlanCases()["scaleClusterNodePool"].Handler(
		middleware.WithToken(context.Background(), testToken), req,
		dispatch.ChangeParams{Operation: "scaleClusterNodePool", Cluster: "test-cluster", Namespace: "fleet-default", NodePoolName: "test-nodepool", DesiredSize: 3})
	require.NoError(t, err)
	assert.True(t, isPlanResponse(t, res.Content[0].(*mcp.TextContent).Text),
		"the plan case must return a plan carrying a confirmationToken")
	assert.Zero(t, countPatches(dyn), "planning a scale must not patch the cluster")

	// The execute case, given the token the plan mints for exactly this
	// operation, executes — it must not return a second plan.
	token := issueScaleToken(t, tools, tools.cfg.Gate, req,
		scaleNodePoolParameters{Cluster: "test-cluster", Namespace: "fleet-default", NodePoolName: "test-nodepool", DesiredSize: 3})
	res, _, err = tools.ExecuteCases()["scaleClusterNodePool"].Handler(
		middleware.WithToken(context.Background(), testToken), req,
		dispatch.ChangeParams{Operation: "scaleClusterNodePool", Cluster: "test-cluster", Namespace: "fleet-default", NodePoolName: "test-nodepool", DesiredSize: 3, ConfirmationToken: token})
	require.NoError(t, err)
	assert.False(t, isPlanResponse(t, res.Content[0].(*mcp.TextContent).Text),
		"the execute case must execute, not return another plan")
	assert.Equal(t, 1, countPatches(dyn), "the execute case must apply the scale exactly once")
}

// newScalingCaseTools builds Tools over a fake dynamic client holding one
// scalable provisioning cluster with one worker node pool, plus a real
// confirmation gate.
func newScalingCaseTools(t *testing.T) (*Tools, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(provisioningSchemes(), provisioningCustomListKinds(),
		newProvisioningClusterWithRKEConfig("test-cluster", "fleet-default", "c-m-abc123", []provisioningV1.RKEMachinePool{
			{WorkerRole: true, Name: "test-nodepool", Quantity: ptr.To[int32](1)},
		}))
	c := &client.Client{
		ClientSetCreator: func(*rest.Config) (kubernetes.Interface, error) { return newFakeClientSet(), nil },
		DynClientCreator: func(*rest.Config) (dynamic.Interface, error) { return dyn, nil },
	}
	gate := fakeGates(t, approveElicit)
	return NewTools(c, toolconfig.Config{Gate: gate}), dyn
}

// isPlanResponse reports whether a response is a plan carrying a confirmation
// block.
func isPlanResponse(t *testing.T, text string) bool {
	t.Helper()
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(text), &parsed))
	_, isPlan := parsed["plan"]
	_, hasConfirmation := parsed["confirmation"]
	return isPlan && hasConfirmation
}
