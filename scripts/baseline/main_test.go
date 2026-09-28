package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolconfig"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets/merged"
)

// writeNorm writes a .norm.json golden named <id>.norm.json under dir.
func writeNorm(t *testing.T, dir, id, body string) string {
	t.Helper()
	path := filepath.Join(dir, id+".norm.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestCompareGoldensIdentical(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeNorm(t, a, "getProject", `{"ok":true}`)
	writeNorm(t, b, "getProject", `{"ok":true}`)

	var sb strings.Builder
	res, err := compareGoldens(
		map[string]string{"getProject": filepath.Join(a, "getProject.norm.json")},
		map[string]string{"getProject": filepath.Join(b, "getProject.norm.json")},
		&sb,
	)
	if err != nil {
		t.Fatalf("compareGoldens: %v", err)
	}
	if res.OK != 1 || res.Diff != 0 || res.KnownDiff != 0 || res.Shared != 1 {
		t.Fatalf("unexpected tally: %+v", res)
	}
	if !strings.Contains(sb.String(), "OK         getProject") {
		t.Fatalf("missing OK line:\n%s", sb.String())
	}
}

func TestCompareGoldensDetectsDiff(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeNorm(t, a, "getProject", `{"ok":true}`)
	writeNorm(t, b, "getProject", `{"ok":false}`)

	var sb strings.Builder
	res, err := compareGoldens(
		map[string]string{"getProject": filepath.Join(a, "getProject.norm.json")},
		map[string]string{"getProject": filepath.Join(b, "getProject.norm.json")},
		&sb,
	)
	if err != nil {
		t.Fatalf("compareGoldens: %v", err)
	}
	if res.Diff != 1 || res.OK != 0 {
		t.Fatalf("unexpected tally: %+v", res)
	}
	if len(res.Regressions) != 1 || res.Regressions[0] != "getProject" {
		t.Fatalf("unexpected regressions: %+v", res.Regressions)
	}
}

// TestCompareGoldensKnownDiffsExempt pins the two classes the brief calls out as
// expected: the execPod cases (old server had no --enable-exec) and the
// env-dependent KDM/TLS cases. An exempt case that differs must be reported as
// KNOWN-DIFF and must NOT fail the run; every other case must NOT be exempt.
func TestCompareGoldensKnownDiffsExempt(t *testing.T) {
	for _, id := range []string{"execPodPlan", "execPod", "listSupportedKubernetesVersions", "createCustomClusterPlan"} {
		if knownDiffs[id] == "" {
			t.Fatalf("%s must be in the knownDiffs allowlist", id)
		}
		a, b := t.TempDir(), t.TempDir()
		writeNorm(t, a, id, `{"v1":true}`)
		writeNorm(t, b, id, `{"v2":true}`)

		var sb strings.Builder
		res, err := compareGoldens(
			map[string]string{id: filepath.Join(a, id+".norm.json")},
			map[string]string{id: filepath.Join(b, id+".norm.json")},
			&sb,
		)
		if err != nil {
			t.Fatalf("compareGoldens(%s): %v", id, err)
		}
		if res.KnownDiff != 1 || res.Diff != 0 {
			t.Fatalf("%s: expected a single KNOWN-DIFF, got %+v", id, res)
		}
		if !strings.Contains(sb.String(), "KNOWN-DIFF "+id) {
			t.Fatalf("%s: missing KNOWN-DIFF line:\n%s", id, sb.String())
		}
	}

	// A diff in a case outside the allowlist stays a regression.
	if knownDiffs["getProject"] != "" {
		t.Fatal("getProject must not be exempt from diffing")
	}
}

func TestCompareGoldensReconcilesCorpora(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeNorm(t, a, "shared", `{"v1":true}`)
	writeNorm(t, b, "shared", `{"v1":true}`)
	writeNorm(t, a, "v1only", `{"v1":true}`)
	writeNorm(t, b, "v2only", `{"v2":true}`)

	var sb strings.Builder
	res, err := compareGoldens(
		map[string]string{
			"shared": filepath.Join(a, "shared.norm.json"),
			"v1only": filepath.Join(a, "v1only.norm.json"),
		},
		map[string]string{
			"shared": filepath.Join(b, "shared.norm.json"),
			"v2only": filepath.Join(b, "v2only.norm.json"),
		},
		&sb,
	)
	if err != nil {
		t.Fatalf("compareGoldens: %v", err)
	}
	if res.Shared != 1 {
		t.Fatalf("shared must count only cases present in both corpora: %+v", res)
	}
	if len(res.OnlyV1) != 1 || res.OnlyV1[0] != "v1only" {
		t.Fatalf("unexpected onlyV1: %+v", res.OnlyV1)
	}
	if len(res.OnlyV2) != 1 || res.OnlyV2[0] != "v2only" {
		t.Fatalf("unexpected onlyV2: %+v", res.OnlyV2)
	}
}

// TestCaptureV2UsesMapsTo pins the replay contract end to end: a
// calls.json-shaped document decodes into callCase, planCalls picks
// mapsTo.tool/mapsTo.params in v2 mode and the case's own tool/params in v1
// mode, and a blank tool is a hard error in both modes.
func TestCaptureV2UsesMapsTo(t *testing.T) {
	const doc = `[
	  {"id":"getProject","tool":"getProject",
	   "params":{"cluster":"c1","name":"p1"},
	   "mapsTo":{"tool":"rancherQuery","params":{"resource":"project","cluster":"c1","name":"p1"}}}
	]`
	var cases []callCase
	if err := json.Unmarshal([]byte(doc), &cases); err != nil {
		t.Fatalf("decoding calls.json shape must work: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("expected 1 case, got %d", len(cases))
	}
	if cases[0].MapsTo.Tool != "rancherQuery" || cases[0].MapsTo.Params["resource"] != "project" {
		t.Fatalf("mapsTo did not decode: %+v", cases[0].MapsTo)
	}

	// v1: the case's own tool and params, untouched.
	v1, err := planCalls(cases, false)
	if err != nil {
		t.Fatalf("planCalls(v1): %v", err)
	}
	if v1[0].tool != "getProject" || v1[0].params["name"] != "p1" {
		t.Fatalf("v1 must call the case's own tool/params: %+v", v1[0])
	}

	// v2: every case is retargeted to its mapping.
	v2, err := planCalls(cases, true)
	if err != nil {
		t.Fatalf("planCalls(v2): %v", err)
	}
	if v2[0].tool != "rancherQuery" || v2[0].params["resource"] != "project" {
		t.Fatalf("v2 must call mapsTo: %+v", v2[0])
	}

	// A blank mapsTo.tool is refused in v2 (it would silently call the empty
	// tool name) but tolerated in v1, where the case's own tool is what runs.
	blank := []callCase{{ID: "noMapping", Tool: "listClusters"}}
	if _, err := planCalls(blank, true); err == nil {
		t.Fatal("v2 with a blank mapsTo.tool must be a hard error")
	}
	if _, err := planCalls(blank, false); err != nil {
		t.Fatalf("v1 must not require a mapping: %v", err)
	}
}

// ---------------------------------------------------------------------------
// F1 regression: the plan-note rewording of 26eb2a0 must be allowlisted
// ---------------------------------------------------------------------------

// createProjectPlanV1Norm is the real .baseline/golden/v1/createProjectPlan
// .norm.json content, embedded so this regression runs in CI where the
// gitignored corpus is absent. It carries the pre-rewrite note naming the
// deleted per-operation tool.
const createProjectPlanV1Norm = `{
  "content": [
    {
      "text": "{\n  \"confirmation\": {\n    \"confirmationToken\": \"\\u003cmasked\\u003e\",\n    \"expiresAt\": \"\\u003cmasked\\u003e\",\n    \"note\": \"Show this plan to the user. Only after their explicit approval, call createProject with this confirmationToken. The token is single-use and expires in 10 minutes.\"\n  },\n  \"plan\": [\n    {\n      \"payload\": {\n        \"apiVersion\": \"management.cattle.io/v3\",\n        \"kind\": \"Project\",\n        \"metadata\": {\n          \"name\": \"baseline-probe-never-create\",\n          \"namespace\": \"scrke2\"\n        },\n        \"spec\": {\n          \"clusterName\": \"scrke2\",\n          \"containerDefaultResourceLimit\": {},\n          \"description\": \"baseline capture synthetic project plan (never executed)\"\n        }\n      },\n      \"resource\": {\n        \"cluster\": \"scrke2\",\n        \"kind\": \"Project\",\n        \"name\": \"baseline-probe-never-create\",\n        \"namespace\": \"scrke2\"\n      },\n      \"type\": \"create\"\n    }\n  ]\n}",
      "type": "text"
    }
  ]
}`

// TestCompareGoldensAllowsPlanNoteRewrite is the F1 regression: recording the
// real createProjectPlan v1 golden against a v2 golden that differs ONLY in the
// plan-note rewording (the change made in 26eb2a0) must report KNOWN-DIFF, not
// a regression. Before the seven plan cases were added to knownDiffs this
// failed with `DIFF createProjectPlan`, which is exactly the false alarm the
// post-deploy gate would have raised on a correct refactor.
func TestCompareGoldensAllowsPlanNoteRewrite(t *testing.T) {
	const oldNote = "call createProject with this confirmationToken"
	const newNote = "call executeChange with operation=createProject and this confirmationToken"
	if !strings.Contains(createProjectPlanV1Norm, oldNote) {
		t.Fatalf("fixture must carry the pre-rewrite note %q", oldNote)
	}
	// The v2 note is the v1 note with only the tool-naming clause replaced.
	rewritten := strings.Replace(createProjectPlanV1Norm, oldNote, newNote, 1)
	if rewritten == createProjectPlanV1Norm {
		t.Fatal("the note rewrite must actually change the v2 golden")
	}

	dir := t.TempDir()
	v1Path := filepath.Join(dir, "createProjectPlan.norm.json")
	v2Path := filepath.Join(dir, "createProjectPlan.v2.norm.json")
	if err := os.WriteFile(v1Path, []byte(createProjectPlanV1Norm), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v2Path, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	res, err := compareGoldens(
		map[string]string{"createProjectPlan": v1Path},
		map[string]string{"createProjectPlan": v2Path},
		&sb,
	)
	if err != nil {
		t.Fatalf("compareGoldens: %v", err)
	}
	if res.KnownDiff != 1 || res.Diff != 0 {
		t.Fatalf("the plan-note rewrite must be allowlisted (got %+v):\n%s", res, sb.String())
	}
	if len(res.Regressions) != 0 {
		t.Fatalf("a note-only rewrite must never count as a regression: %+v", res.Regressions)
	}
}

// TestPlanNoteCasesAllowlisted pins that exactly the seven plan cases whose v1
// golden carries a confirmation.note are allowlisted, and that the two plan
// cases without a note are exempted for their own documented reasons instead.
func TestPlanNoteCasesAllowlisted(t *testing.T) {
	withNote := []string{
		"createKubernetesResourcePlan", "patchKubernetesResourcePlan",
		"deleteKubernetesResourcePlan", "createProjectPlan",
		"createImportedClusterPlan", "createK3kClusterPlan",
		"scaleClusterNodePoolPlan",
	}
	for _, id := range withNote {
		if knownDiffs[id] == "" {
			t.Errorf("%s carries a v1 confirmation.note and must be allowlisted", id)
		}
		if !strings.Contains(knownDiffs[id], "note") {
			t.Errorf("%s must be exempted for the note rewording, got %q", id, knownDiffs[id])
		}
	}
	// These two mint no note; they must not claim the note reason.
	for _, id := range []string{"createCustomClusterPlan", "execPodPlan"} {
		if strings.Contains(knownDiffs[id], "note names") {
			t.Errorf("%s has no v1 note; its exemption reason must not cite one", id)
		}
	}
}

// TestDiscoverToolsAreRegistered pins the tool names discover() calls to the
// live merged surface: registration is the authoritative tool list, so a
// future rename or removal fails here instead of at live-discovery time.
func TestDiscoverToolsAreRegistered(t *testing.T) {
	gate, err := confirm.NewGate()
	if err != nil {
		t.Fatal(err)
	}
	cfg := toolconfig.Config{Gate: gate}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	merged.Register(&client.Client{}, server, cfg) // registration never calls the client

	ct, st := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	mc := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "v0"}, nil)
	sess, err := mc.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	res, err := sess.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	registered := map[string]bool{}
	for _, tool := range res.Tools {
		registered[tool.Name] = true
	}
	for _, name := range []string{toolRancherQuery, toolDiagnose, toolListK8s} {
		if !registered[name] {
			t.Errorf("discover() calls tool %q, which is not in the registered surface", name)
		}
	}
}
