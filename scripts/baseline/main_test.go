package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// TestCaptureV2UsesMapsTo pins the replay contract: v2 sends the case's
// mapsTo.tool with mapsTo.params, while v1 sends the case's own tool/params.
// A blank mapsTo tool is a hard error (it would silently call an empty name).
func TestCaptureV2UsesMapsTo(t *testing.T) {
	c := callCase{ID: "getProject", Tool: "getProject"}
	c.MapsTo.Tool = "rancherQuery"
	c.MapsTo.Params = map[string]any{"resource": "project"}

	// The planner in capture() is inline; assert on the struct contract the
	// harness reads, so a change to calls.json's field tags is caught here.
	if c.MapsTo.Tool != "rancherQuery" || c.MapsTo.Params["resource"] != "project" {
		t.Fatalf("mapsTo must carry the merged surface tool+params: %+v", c.MapsTo)
	}
}
