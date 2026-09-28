// Command baseline captures golden tool responses from a live Rancher MCP
// server, so that a tool-surface refactor (43 -> 7 tools) can be verified
// byte-for-byte against the pre-refactor behavior.
//
// Subcommands:
//
//	baseline discover     discover real parameters via read tools, write .baseline/discovered.json
//	baseline capture      run the case matrix in scripts/baseline/calls.json, write v1 golden files
//	baseline capture -v2  replay the same cases through their mapsTo mapping (merged surface), write v2 goldens
//	baseline compare      byte-compare the v1 and v2 .norm.json goldens of every shared case
//
// Configuration is loaded from .baseline/config.json ({"url": ..., "headers":
// {...}}). When absent it is generated from the "rancher" entry of the local
// Claude config (~/.claude.json), which is the same credential source the
// interactive MCP client uses. The whole .baseline/ directory is gitignored.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// repoRoot-relative paths
	baselineDir = ".baseline"
	// goldenDirV1 is the pre-refactor corpus: the 43 per-operation tools of the
	// old surface, captured with `capture` (no flag).
	goldenDirV1 = ".baseline/golden/v1"
	// goldenDirV2 is the post-refactor corpus: the same 43 cases replayed
	// through each case's mapsTo mapping onto the merged 7-tool surface,
	// captured with `capture -v2`.
	goldenDirV2     = ".baseline/golden/v2"
	callsFile       = "scripts/baseline/calls.json"
	configFile      = ".baseline/config.json"
	discoveredFile  = ".baseline/discovered.json"
	claudeConfigEnv = "CLAUDE_CONFIG_FILE"
	// total budget for one capture run
	captureTotalTimeout = 15 * time.Minute
	// per tool-call budget
	perCallTimeout = 60 * time.Second
	// placeholder prefix used inside calls.json params
	placeholderPrefix = "${discovered."
	// fake token used to capture the rejection path of Execute tools.
	// NEVER replace this with a real confirmationToken.
	invalidToken = "invalid-baseline-token"
)

// config is the on-disk connection configuration.
type config struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "baseline: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	v2 := false
	fs.BoolVar(&v2, "v2", false, "capture through each case's mapsTo mapping into "+goldenDirV2+" (merged surface)")
	switch cmd {
	case "discover":
		_ = fs.Parse(os.Args[2:])
		return discover()
	case "capture":
		_ = fs.Parse(os.Args[2:])
		return capture(v2)
	case "compare":
		_ = fs.Parse(os.Args[2:])
		return compare()
	default:
		return fmt.Errorf("unknown subcommand %q (want discover, capture or compare)", cmd)
	}
}

// loadConfig loads .baseline/config.json, generating it from the Claude
// config when missing.
func loadConfig() (*config, error) {
	raw, err := os.ReadFile(configFile)
	if err == nil {
		var cfg config
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", configFile, err)
		}
		if cfg.URL == "" || len(cfg.Headers) == 0 {
			return nil, fmt.Errorf("%s: url and headers must be set", configFile)
		}
		return &cfg, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("reading %s: %w", configFile, err)
	}

	// Generate from ~/.claude.json (or $CLAUDE_CONFIG_FILE).
	cfg, err := configFromClaude()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(baselineDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", baselineDir, err)
	}
	raw, err = json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(configFile, append(raw, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("writing %s: %w", configFile, err)
	}
	fmt.Printf("generated %s from Claude config\n", configFile)
	return cfg, nil
}

// configFromClaude extracts the rancher MCP server entry from the Claude
// config file. The token never appears in any committed file.
func configFromClaude() (*config, error) {
	path := os.Getenv(claudeConfigEnv)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving home dir: %w", err)
		}
		path = filepath.Join(home, ".claude.json")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
		Projects map[string]struct {
			MCPServers map[string]struct {
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"mcpServers"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if e, ok := doc.MCPServers["rancher"]; ok && e.URL != "" {
		return &config{URL: e.URL, Headers: e.Headers}, nil
	}
	// Fall back to any project-scoped rancher entry.
	for _, p := range doc.Projects {
		if e, ok := p.MCPServers["rancher"]; ok && e.URL != "" {
			return &config{URL: e.URL, Headers: e.Headers}, nil
		}
	}
	return nil, fmt.Errorf("no mcpServers.rancher entry with a url in %s", path)
}

// newSession connects a client session to the live server with the custom
// auth headers injected on every HTTP request.
func newSession(ctx context.Context, cfg *config) (*mcp.ClientSession, error) {
	rt := &headerTransport{headers: cfg.Headers}
	client := mcp.NewClient(&mcp.Implementation{Name: "baseline-capture", Version: "0.1"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint: cfg.URL,
		HTTPClient: &http.Client{
			Transport: rt,
			Timeout:   perCallTimeout,
		},
		// The capture only issues request/response calls.
		DisableStandaloneSSE: true,
		MaxRetries:           1,
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", cfg.URL, err)
	}
	return session, nil
}

// headerTransport injects the custom auth headers into every request.
type headerTransport struct {
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// callTool invokes a tool and returns the raw serialized CallToolResult plus
// an error if the transport or protocol failed. A tool-level error (result
// with IsError=true) is NOT an error here: error responses are part of the
// captured behavior.
func callTool(ctx context.Context, session *mcp.ClientSession, tool string, params map[string]any) (json.RawMessage, error) {
	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      tool,
		Arguments: params,
	})
	if err != nil {
		// Transport/protocol-level failure (timeout, HTTP error, ...).
		// Store it as a synthetic error result so the golden captures it.
		synthetic := map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
			"_baseline": map[string]any{
				"kind":    "transport-error",
				"tool":    tool,
				"message": err.Error(),
			},
		}
		raw, mErr := json.Marshal(synthetic)
		if mErr != nil {
			return nil, mErr
		}
		return raw, nil
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("serializing CallToolResult for %s: %w", tool, err)
	}
	return raw, nil
}

// ---------------------------------------------------------------------------
// discover
// ---------------------------------------------------------------------------

// discovered holds the real parameters found by the discover subcommand.
type discovered struct {
	Cluster            string `json:"cluster"`
	ClusterID          string `json:"clusterID"`
	Project            string `json:"project"`
	ProjectID          string `json:"projectID"`
	Workspace          string `json:"workspace"`
	GitRepo            string `json:"gitRepo"`
	DeploymentName     string `json:"deploymentName"`
	DeploymentNS       string `json:"deploymentNamespace"`
	PodName            string `json:"podName"`
	PodNamespace       string `json:"podNamespace"`
	RoleTemplate       string `json:"roleTemplate"`
	Username           string `json:"username"`
	UserID             string `json:"userID"`
	MachineName        string `json:"machineName"`
	NodePoolName       string `json:"nodePoolName"`
	NodePoolNamespace  string `json:"nodePoolNamespace"`
	BundleName         string `json:"bundleName"`
	K3sVersion         string `json:"k3sVersion"`
	Rke2Version        string `json:"rke2Version"`
	APIVersion         string `json:"apiVersion"`
	CustomResourceKind string `json:"customResourceKind"`
	CustomResourceNS   string `json:"customResourceNamespace"`
}

func discover() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	session, err := newSession(ctx, cfg)
	if err != nil {
		return err
	}
	defer session.Close()

	d := &discovered{}
	fmt.Println("== discovery: listClusters")
	raw, err := callTool(ctx, session, "listClusters", map[string]any{})
	if err != nil {
		return err
	}
	clusters, err := parseList(raw)
	if err != nil {
		return fmt.Errorf("listClusters: %w", err)
	}
	// Objects are K8s resources: name lives in metadata. The uiContext array
	// (kind/cluster/name) is a more compact source, but metadata is enough.
	// Prefer a non-local downstream cluster so discovery covers real
	// downstream behavior; fall back to local.
	for _, c := range clusters {
		name := clusterName(c)
		if name == "" {
			continue
		}
		if name != "local" {
			d.Cluster = name
			d.ClusterID = name
			break
		}
	}
	if d.Cluster == "" {
		for _, c := range clusters {
			if name := clusterName(c); name != "" {
				d.Cluster = name
				d.ClusterID = name
				break
			}
		}
	}
	if d.Cluster == "" {
		return fmt.Errorf("no cluster found via listClusters")
	}
	fmt.Printf("   management cluster=%s\n", d.Cluster)

	// The provisioning cluster (provisioning.cattle.io/cluster in
	// fleet-default) is the name that tools like analyzeCluster and
	// scaleClusterNodePool expect (it resolves via display-name lookup too).
	// Prefer it as the primary cluster param when one exists.
	fmt.Println("== discovery: provisioning clusters")
	raw, err = callTool(ctx, session, "listKubernetesResources", map[string]any{
		"kind": "cluster", "apiVersion": "provisioning.cattle.io/v1",
		"namespace": "fleet-default", "cluster": "local", "limit": 20, "offset": 0,
	})
	if err != nil {
		return err
	}
	provClusters, perr := parseList(raw)
	provNames := make([]string, 0, len(provClusters))
	if perr == nil {
		for _, pc := range provClusters {
			if name := clusterName(pc); name != "" {
				provNames = append(provNames, name)
			}
		}
	}
	if len(provNames) > 0 {
		// Prefer a cluster that has CAPI machines (Harvester-driven clusters
		// have none, which would leave machine/node-pool discovery empty).
		chosen := provNames[0]
		for _, name := range provNames {
			mraw, merr := callTool(ctx, session, "analyzeClusterMachines", map[string]any{"cluster": name})
			if merr != nil {
				continue
			}
			if mm, mperr := parseList(mraw); mperr == nil && len(mm) > 0 {
				chosen = name
				break
			}
		}
		d.ClusterID = d.Cluster
		d.Cluster = chosen
	}
	fmt.Printf("   cluster=%s managementID=%s\n", d.Cluster, d.ClusterID)

	fmt.Println("== discovery: listProjects")
	raw, err = callTool(ctx, session, "listProjects", map[string]any{"cluster": d.Cluster})
	if err != nil {
		return err
	}
	projects, err := parseList(raw)
	if err != nil {
		return fmt.Errorf("listProjects: %w", err)
	}
	for _, p := range projects {
		name := clusterName(p)
		if name == "" {
			continue
		}
		if name != "Default" && name != "System" {
			d.Project = name
			d.ProjectID = name
			break
		}
	}
	if d.Project == "" {
		for _, p := range projects {
			if name := clusterName(p); name != "" {
				d.Project = name
				d.ProjectID = name
				break
			}
		}
	}
	if d.Project == "" {
		return fmt.Errorf("no project found via listProjects")
	}
	fmt.Printf("   project=%s id=%s\n", d.Project, d.ProjectID)

	// Fleet workspace: try common values until one yields GitRepos or an OK response.
	fmt.Println("== discovery: listGitRepos (workspace probe)")
	d.Workspace = "fleet-default"
	// We still record the probe even if there are no repos; error is fine.
	for _, ws := range []string{"fleet-default", "fleet-local", "cattle-fleet-system"} {
		raw, err = callTool(ctx, session, "listGitRepos", map[string]any{"workspace": ws})
		if err != nil {
			return err
		}
		repos, perr := parseList(raw)
		isErr := strings.Contains(string(raw), `"isError":true`) || strings.Contains(string(raw), `"IsError":true`)
		if perr == nil && len(repos) > 0 {
			d.Workspace = ws
			if name := clusterName(repos[0]); name != "" {
				d.GitRepo = name
			}
			break
		}
		if !isErr && perr == nil {
			d.Workspace = ws
			break
		}
		if !isErr {
			d.Workspace = ws
			break
		}
	}
	fmt.Printf("   workspace=%s gitRepo=%s\n", d.Workspace, d.GitRepo)

	// A real deployment via listKubernetesResources (paginated, limit=5).
	// The chosen cluster may have no deployments (e.g. a Harvester compute
	// cluster); probe all known clusters until one yields a deployment.
	fmt.Println("== discovery: listKubernetesResources (deployment)")
	clusterCandidates := unique(append([]string{d.Cluster}, allClusterNames(clusters)...))
	for _, c := range clusterCandidates {
		if c == "" {
			continue
		}
		raw, err = callTool(ctx, session, "listKubernetesResources", map[string]any{
			"kind": "deployment", "limit": 5, "offset": 0,
			"namespace": "", "cluster": c,
		})
		if err != nil {
			return err
		}
		deployments, perr := parseList(raw)
		if perr != nil {
			continue
		}
		for _, dep := range deployments {
			md, _ := dep["metadata"].(map[string]any)
			if md == nil {
				continue
			}
			name, _ := md["name"].(string)
			ns, _ := md["namespace"].(string)
			if name != "" && ns != "" {
				d.DeploymentName = name
				d.DeploymentNS = ns
				break
			}
		}
		if d.DeploymentName != "" {
			break
		}
	}
	if d.DeploymentName == "" {
		return fmt.Errorf("no deployment found via listKubernetesResources")
	}
	fmt.Printf("   deployment=%s/%s\n", d.DeploymentNS, d.DeploymentName)

	// A real pod in the same way.
	fmt.Println("== discovery: listKubernetesResources (pod)")
	for _, c := range clusterCandidates {
		if c == "" {
			continue
		}
		raw, err = callTool(ctx, session, "listKubernetesResources", map[string]any{
			"kind": "pod", "limit": 5, "offset": 0,
			"namespace": "", "cluster": c,
		})
		if err != nil {
			return err
		}
		pods, perr := parseList(raw)
		if perr != nil {
			continue
		}
		for _, pod := range pods {
			md, _ := pod["metadata"].(map[string]any)
			if md == nil {
				continue
			}
			name, _ := md["name"].(string)
			ns, _ := md["namespace"].(string)
			if name != "" && ns != "" {
				d.PodName = name
				d.PodNamespace = ns
				break
			}
		}
		if d.PodName != "" {
			break
		}
	}
	if d.PodName == "" {
		return fmt.Errorf("no pod found via listKubernetesResources")
	}
	fmt.Printf("   pod=%s/%s\n", d.PodNamespace, d.PodName)

	// A real role template.
	fmt.Println("== discovery: listRoleTemplates")
	raw, err = callTool(ctx, session, "listRoleTemplates", map[string]any{})
	if err != nil {
		return err
	}
	roleTemplates, err := parseList(raw)
	if err != nil {
		return fmt.Errorf("listRoleTemplates: %w", err)
	}
	for _, rt := range roleTemplates {
		name := clusterName(rt)
		if name != "" {
			d.RoleTemplate = name
			break
		}
	}
	if d.RoleTemplate == "" {
		return fmt.Errorf("no role template found via listRoleTemplates")
	}
	fmt.Printf("   roleTemplate=%s\n", d.RoleTemplate)

	// Supported k8s versions (rke2 and k3s) — one entry each for synthetic plan params.
	fmt.Println("== discovery: listSupportedKubernetesVersions")
	raw, err = callTool(ctx, session, "listSupportedKubernetesVersions", map[string]any{"distribution": "rke2"})
	if err != nil {
		return err
	}
	rke2Versions := extractVersions(raw)
	if len(rke2Versions) > 0 {
		d.Rke2Version = rke2Versions[len(rke2Versions)-1]
	}
	fmt.Printf("   rke2 versions: %d, picked %q\n", len(rke2Versions), d.Rke2Version)

	// A username from CRTBs (best effort).
	fmt.Println("== discovery: listClusterRoleTemplateBindings")
	raw, err = callTool(ctx, session, "listClusterRoleTemplateBindings", map[string]any{"cluster": d.Cluster})
	if err != nil {
		return err
	}
	crtbs, perr := parseList(raw)
	if perr == nil {
		for _, crtb := range crtbs {
			un, _ := crtb["userName"].(string)
			// userName is a Rancher user ID (u-xxx); getUser takes a username,
			// so also probe userPrincipalName as a fallback display name.
			if un != "" {
				d.UserID = un
				if pn, _ := crtb["userPrincipalName"].(string); pn != "" {
					d.Username = pn
				} else {
					d.Username = un
				}
				break
			}
		}
	}
	fmt.Printf("   username=%s userID=%s\n", d.Username, d.UserID)

	// Machine + node pool (best effort; analyzeClusterMachines covers the happy path).
	fmt.Println("== discovery: analyzeClusterMachines")
	raw, err = callTool(ctx, session, "analyzeClusterMachines", map[string]any{"cluster": d.Cluster})
	if err != nil {
		return err
	}
	machines, perr := parseList(raw)
	_ = perr // the response may be a single object; best effort only
	if len(machines) > 0 {
		for _, m := range machines {
			md, _ := m["metadata"].(map[string]any)
			if md == nil {
				continue
			}
			name, _ := md["name"].(string)
			ns, _ := md["namespace"].(string)
			if name != "" && ns != "" {
				d.MachineName = name
				break
			}
		}
	}
	fmt.Printf("   machine=%s\n", d.MachineName)

	// analyzeCluster output contains the provisioning cluster with
	// spec.kubernetesVersion (used as the plan param for cluster creation) and
	// spec.rkeConfig.machinePools (node pool names).
	fmt.Println("== discovery: analyzeCluster")
	raw, err = callTool(ctx, session, "analyzeCluster", map[string]any{"cluster": d.Cluster})
	if err != nil {
		return err
	}
	nodePools := extractNodePools(raw)
	if len(nodePools) > 0 {
		np := nodePools[len(nodePools)-1]
		d.NodePoolName = np.name
		d.NodePoolNamespace = np.namespace
	}
	if v := extractKubernetesVersion(raw); v != "" {
		d.Rke2Version = v
	}
	fmt.Printf("   nodePool=%s/%s kubernetesVersion=%s\n", d.NodePoolNamespace, d.NodePoolName, d.Rke2Version)

	// A bundle name for getBundle (best effort via analyzeFleetResources).
	fmt.Println("== discovery: analyzeFleetResources")
	raw, err = callTool(ctx, session, "analyzeFleetResources", map[string]any{"workspace": d.Workspace})
	if err != nil {
		return err
	}
	bundles := extractBundleNames(raw)
	if len(bundles) > 0 {
		d.BundleName = bundles[len(bundles)-1]
	}
	fmt.Printf("   bundle=%s\n", d.BundleName)

	// Save.
	if err := os.MkdirAll(baselineDir, 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(discoveredFile, append(out, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("\ndiscovered parameters written to %s\n", discoveredFile)
	return nil
}

// clusterName returns the object name: K8s resources keep it under
// metadata.name, some Rancher APIs expose it at the top level.
func clusterName(c map[string]any) string {
	if md, ok := c["metadata"].(map[string]any); ok {
		if n, ok := md["name"].(string); ok {
			return n
		}
	}
	if n, ok := c["name"].(string); ok {
		return n
	}
	return ""
}

// allClusterNames returns the metadata.name of every cluster entry.
func allClusterNames(clusters []map[string]any) []string {
	out := make([]string, 0, len(clusters))
	for _, c := range clusters {
		if name := clusterName(c); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// unique de-duplicates a string slice preserving first-seen order.
func unique(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// parseList extracts the list of objects from a CallToolResult: it takes
// Content[0].Text, tries JSON, and returns either the []any (array case) or
// the value under a "data"/"items"/"resources" key, or nil if the response is
// an error.
func parseList(raw json.RawMessage) ([]map[string]any, error) {
	texts, err := contentTexts(raw)
	if err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return nil, errors.New("no content")
	}
	var doc any
	if err := json.Unmarshal([]byte(texts[0]), &doc); err != nil {
		return nil, fmt.Errorf("content is not JSON: %w", err)
	}
	return objectsFrom(doc), nil
}

func objectsFrom(doc any) []map[string]any {
	switch v := doc.(type) {
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		// Standard server envelope: {"llm": <data>, "uiContext": [...]}.
		// llm is either the object list directly, or (when paginated/annotated)
		// {"resources": [...], "note": "..."}.
		if llm, ok := v["llm"]; ok {
			return objectsFrom(llm)
		}
		// Plan responses: {"plan": [...], "confirmation": {...}}
		if arr, ok := v["plan"].([]any); ok {
			return objectsFrom(arr)
		}
		for _, key := range []string{"data", "items", "resources", "machines", "clusters"} {
			if arr, ok := v[key].([]any); ok {
				return objectsFrom(arr)
			}
		}
		if _, isErr := v["error"]; isErr {
			return nil
		}
		return []map[string]any{v}
	default:
		return nil
	}
}

// contentTexts returns the Text field of every TextContent in a serialized
// CallToolResult.
func contentTexts(raw json.RawMessage) ([]string, error) {
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	var texts []string
	for _, c := range res.Content {
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	return texts, nil
}

// extractVersions pulls a list of version-like strings out of a response.
func extractVersions(raw json.RawMessage) []string {
	texts, err := contentTexts(raw)
	if err != nil || len(texts) == 0 {
		return nil
	}
	var doc any
	if err := json.Unmarshal([]byte(texts[0]), &doc); err != nil {
		return nil
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for k, e := range t {
				switch k {
				case "version", "Versions", "versions":
					if s, ok := e.(string); ok && strings.HasPrefix(s, "v") {
						out = append(out, s)
					}
				default:
					walk(e)
				}
			}
		}
	}
	walk(doc)
	return out
}

type nodePool struct{ name, namespace string }

// extractNodePools finds node pools in an analyzeCluster response: the
// provisioning cluster object carries spec.rkeConfig.machinePools[] with a
// "name" field; the pool lives in the same namespace as the cluster.
func extractNodePools(raw json.RawMessage) []nodePool {
	texts, err := contentTexts(raw)
	if err != nil || len(texts) == 0 {
		return nil
	}
	var doc any
	if err := json.Unmarshal([]byte(texts[0]), &doc); err != nil {
		return nil
	}
	var out []nodePool
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			if kind, _ := t["kind"].(string); kind == "Cluster" {
				if spec, ok := t["spec"].(map[string]any); ok {
					if rke, ok := spec["rkeConfig"].(map[string]any); ok {
						if pools, ok := rke["machinePools"].([]any); ok {
							ns, _ := nestedString(t, "metadata", "namespace")
							for _, p := range pools {
								pm, ok := p.(map[string]any)
								if !ok {
									continue
								}
								if name, _ := pm["name"].(string); name != "" {
									out = append(out, nodePool{name: name, namespace: ns})
								}
							}
						}
					}
				}
			}
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(doc)
	return out
}

// extractKubernetesVersion finds the provisioning cluster's
// spec.kubernetesVersion in an analyzeCluster response.
func extractKubernetesVersion(raw json.RawMessage) string {
	texts, err := contentTexts(raw)
	if err != nil || len(texts) == 0 {
		return ""
	}
	var doc any
	if err := json.Unmarshal([]byte(texts[0]), &doc); err != nil {
		return ""
	}
	var found string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			if kind, _ := t["kind"].(string); kind == "Cluster" {
				if v, ok := nestedString(t, "spec", "kubernetesVersion"); ok && found == "" {
					found = v
				}
			}
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(doc)
	return found
}

// nestedString walks nested maps for a string value.
func nestedString(m map[string]any, path ...string) (string, bool) {
	cur := m
	for i, k := range path {
		v, ok := cur[k]
		if !ok {
			return "", false
		}
		if i == len(path)-1 {
			s, ok := v.(string)
			return s, ok
		}
		cur, ok = v.(map[string]any)
		if !ok {
			return "", false
		}
	}
	return "", false
}

// extractBundleNames finds bundle names in an analyzeFleetResources response.
func extractBundleNames(raw json.RawMessage) []string {
	texts, err := contentTexts(raw)
	if err != nil || len(texts) == 0 {
		return nil
	}
	var doc any
	if err := json.Unmarshal([]byte(texts[0]), &doc); err != nil {
		return nil
	}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, k := range []string{"name", "bundleName"} {
				if s, ok := t[k].(string); ok && s != "" {
					if _, dup := contains(out, s); !dup {
						out = append(out, s)
					}
				}
			}
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(doc)
	return out
}

func contains(list []string, s string) (int, bool) {
	for i, e := range list {
		if e == s {
			return i, true
		}
	}
	return -1, false
}

// ---------------------------------------------------------------------------
// capture
// ---------------------------------------------------------------------------

// callCase is one entry of scripts/baseline/calls.json.
type callCase struct {
	ID     string         `json:"id"`
	Tool   string         `json:"tool"`
	Params map[string]any `json:"params"`
	MapsTo struct {
		Tool   string         `json:"tool"`
		Params map[string]any `json:"params"`
	} `json:"mapsTo"`
}

// capture runs the case matrix. In v1 mode (v2=false) it calls the case's own
// tool/params (the old per-operation surface); in v2 mode it replays every case
// through its mapsTo mapping onto the merged 7-tool surface. The goldens of the
// two modes land in different directories so compare can diff them.
func capture(v2 bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), captureTotalTimeout)
	defer cancel()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	session, err := newSession(ctx, cfg)
	if err != nil {
		return err
	}
	defer session.Close()

	var disc discovered
	raw, err := os.ReadFile(discoveredFile)
	if err != nil {
		return fmt.Errorf("reading %s (run 'baseline discover' first): %w", discoveredFile, err)
	}
	if err := json.Unmarshal(raw, &disc); err != nil {
		return fmt.Errorf("parsing %s: %w", discoveredFile, err)
	}

	var cases []callCase
	raw, err = os.ReadFile(callsFile)
	if err != nil {
		return fmt.Errorf("reading %s: %w", callsFile, err)
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		return fmt.Errorf("parsing %s: %w", callsFile, err)
	}
	if len(cases) != 43 {
		return fmt.Errorf("%s: expected 43 cases, got %d", callsFile, len(cases))
	}

	// callCase + params for this run: v1 uses the case's own tool/params, v2
	// uses the mapsTo mapping onto the merged surface.
	type plannedCall struct {
		id     string
		tool   string
		params map[string]any
	}
	planned := make([]plannedCall, 0, len(cases))
	for _, c := range cases {
		pc := plannedCall{id: c.ID, tool: c.Tool, params: c.Params}
		if v2 {
			pc.tool = c.MapsTo.Tool
			pc.params = c.MapsTo.Params
		}
		if pc.tool == "" {
			// A blank mapsTo in v2 mode would silently retarget the call to the
			// empty tool name; fail loudly instead.
			where := "case"
			if v2 {
				where = "mapsTo"
			}
			return fmt.Errorf("case %s: no %s tool configured", c.ID, where)
		}
		planned = append(planned, pc)
	}

	outDir := goldenDirV1
	if v2 {
		outDir = goldenDirV2
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	var okCount, errCount int
	var failures []string
	start := time.Now()
	for i, pc := range planned {
		params, err := resolvePlaceholders(pc.params, &disc)
		if err != nil {
			return fmt.Errorf("case %s: %w", pc.id, err)
		}
		callCtx, callCancel := context.WithTimeout(ctx, perCallTimeout)
		raw, err := callTool(callCtx, session, pc.tool, params)
		callCancel()
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", pc.id, err))
			continue
		}

		rawPath := filepath.Join(outDir, pc.id+".raw.json")
		if err := os.WriteFile(rawPath, append(raw, '\n'), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", rawPath, err)
		}
		norm, err := normalize(raw)
		if err != nil {
			return fmt.Errorf("normalizing %s: %w", pc.id, err)
		}
		normPath := filepath.Join(outDir, pc.id+".norm.json")
		if err := os.WriteFile(normPath, append(norm, '\n'), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", normPath, err)
		}

		if isErrorResponse(raw) {
			errCount++
			fmt.Printf("[%2d/%d] %-36s ERROR-RESPONSE (%d bytes raw)\n", i+1, len(planned), pc.id, len(raw))
		} else {
			okCount++
			fmt.Printf("[%2d/%d] %-36s ok (%d bytes raw)\n", i+1, len(planned), pc.id, len(raw))
		}
	}
	fmt.Printf("\ncapture done in %s: %d ok, %d error responses, %d hard failures\n",
		time.Since(start).Round(time.Second), okCount, errCount, len(failures))
	for _, f := range failures {
		fmt.Printf("  FAILED: %s\n", f)
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d cases failed to capture", len(failures))
	}
	return nil
}

// ---------------------------------------------------------------------------
// compare
// ---------------------------------------------------------------------------

// knownDiffs are case ids whose v1 and v2 goldens are EXPECTED to differ; they
// are printed as "KNOWN-DIFF <id>" and do not count as failures. Every entry
// must carry the reason it is expected.
//
// This list is deliberately hardcoded rather than derived: a new diff appearing
// in any other case is a real regression signal and must stay visible.
var knownDiffs = map[string]string{
	// The old server ran without --enable-exec, so the old exec tools were not
	// registered and the call failed with a transport-level "unknown tool".
	// The merged surface always registers planChange, which refuses the execPod
	// operation at runtime with an explicit --enable-exec error.
	"execPodPlan": "old server ran without --enable-exec (unknown tool); merged surface returns the explicit --enable-exec error",
	"execPod":     "old server ran without --enable-exec (unknown tool); merged surface returns the explicit --enable-exec error",
	// These v1 goldens are TLS failures against the deployment's KDM endpoint
	// (self-signed cert), not tool behavior: the response depends on the
	// environment the server runs in, not on the refactor.
	"listSupportedKubernetesVersions": "v1 golden is an environment-dependent TLS error from the KDM endpoint",
	"createCustomClusterPlan":         "v1 golden is an environment-dependent TLS error from the KDM endpoint",
}

// compareResult summarizes one compare run.
type compareResult struct {
	Shared      int
	OK          int
	Diff        int
	KnownDiff   int
	OnlyV1      []string
	OnlyV2      []string
	Regressions []string
}

// compare byte-compares the .norm.json goldens of every case id present in both
// v1/ and v2/ and prints OK / DIFF / KNOWN-DIFF per case. It only reports; it
// never writes to the golden corpus.
func compare() error {
	// A missing corpus directory is the normal first-run mistake, so say what
	// to run instead of leaking a bare ENOENT.
	v1, err := normFiles(goldenDirV1)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s: no golden corpus yet (run 'baseline capture' first)", goldenDirV1)
		}
		return err
	}
	v2, err := normFiles(goldenDirV2)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s: no v2 corpus yet (run 'baseline capture -v2' first)", goldenDirV2)
		}
		return err
	}
	if len(v1) == 0 {
		return fmt.Errorf("%s: no .norm.json files (run 'baseline capture' first)", goldenDirV1)
	}
	if len(v2) == 0 {
		return fmt.Errorf("%s: no .norm.json files (run 'baseline capture -v2' first)", goldenDirV2)
	}

	res, err := compareGoldens(v1, v2, os.Stdout)
	if err != nil {
		return err
	}
	if res.Diff > 0 {
		return fmt.Errorf("%d unexpected diffs: %s", res.Diff, strings.Join(res.Regressions, ", "))
	}
	return nil
}

// compareGoldens diffs two case id -> norm-file-path maps, writing one line per
// shared case to w and returning the tally. Split out of compare so the diff
// policy (including the knownDiffs allowlist) is unit-testable without a golden
// corpus on disk.
func compareGoldens(v1, v2 map[string]string, w io.Writer) (compareResult, error) {
	var res compareResult

	ids := make([]string, 0, len(v1))
	for id := range v1 {
		if _, ok := v2[id]; ok {
			ids = append(ids, id)
		} else {
			res.OnlyV1 = append(res.OnlyV1, id)
		}
	}
	for id := range v2 {
		if _, ok := v1[id]; !ok {
			res.OnlyV2 = append(res.OnlyV2, id)
		}
	}
	sort.Strings(ids)
	sort.Strings(res.OnlyV1)
	sort.Strings(res.OnlyV2)
	res.Shared = len(ids)

	for _, id := range ids {
		same, err := sameFile(v1[id], v2[id])
		if err != nil {
			return res, fmt.Errorf("case %s: %w", id, err)
		}
		switch {
		case same:
			res.OK++
			fmt.Fprintf(w, "OK         %s\n", id)
		case knownDiffs[id] != "":
			res.KnownDiff++
			fmt.Fprintf(w, "KNOWN-DIFF %s (%s)\n", id, knownDiffs[id])
		default:
			res.Diff++
			res.Regressions = append(res.Regressions, id)
			fmt.Fprintf(w, "DIFF       %s\n", id)
		}
	}

	fmt.Fprintf(w, "\ncompare done: %d OK, %d DIFF, %d KNOWN-DIFF (of %d shared cases)\n",
		res.OK, res.Diff, res.KnownDiff, res.Shared)
	if len(res.OnlyV1) > 0 {
		fmt.Fprintf(w, "only in v1 (%d): %s\n", len(res.OnlyV1), strings.Join(res.OnlyV1, ", "))
	}
	if len(res.OnlyV2) > 0 {
		fmt.Fprintf(w, "only in v2 (%d): %s\n", len(res.OnlyV2), strings.Join(res.OnlyV2, ", "))
	}
	return res, nil
}

// normFiles maps case id -> path of its .norm.json golden in dir.
func normFiles(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".norm.json") {
			continue
		}
		out[strings.TrimSuffix(e.Name(), ".norm.json")] = filepath.Join(dir, e.Name())
	}
	return out, nil
}

// sameFile reports whether two files have identical bytes.
func sameFile(a, b string) (bool, error) {
	ba, err := os.ReadFile(a)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", a, err)
	}
	bb, err := os.ReadFile(b)
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", b, err)
	}
	return bytes.Equal(ba, bb), nil
}

// isErrorResponse reports whether the serialized CallToolResult is an error.
func isErrorResponse(raw json.RawMessage) bool {
	var res struct {
		IsError *bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return true
	}
	if res.IsError != nil {
		return *res.IsError
	}
	return false
}

// placeholderRe matches ${discovered.field} inside string values.
var placeholderRe = regexp.MustCompile(`\$\{discovered\.([A-Za-z0-9_]+)\}`)

// emptyFallbacks maps discovered fields that may legitimately be empty in the
// target environment (e.g. no GitRepos in Fleet) to a fixed probe value, so
// the call still exercises the tool's dispatch path and records a
// deterministic not-found response instead of an empty parameter.
var emptyFallbacks = map[string]string{
	"gitRepo":    "baseline-nonexistent-repo",
	"bundleName": "baseline-nonexistent-bundle",
}

// resolvePlaceholders substitutes ${discovered.xxx} placeholders in string
// values (including inside nested objects) with the discovered parameters.
func resolvePlaceholders(v any, d *discovered) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var discMap map[string]any
	dj, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(dj, &discMap); err != nil {
		return nil, err
	}
	resolved := placeholderRe.ReplaceAllFunc(b, func(m []byte) []byte {
		key := string(placeholderRe.FindSubmatch(m)[1])
		if val, ok := discMap[key]; ok {
			if s, ok := val.(string); ok {
				if s == "" {
					if fb, ok := emptyFallbacks[key]; ok {
						return []byte(fb)
					}
				}
				return []byte(s)
			}
			j, _ := json.Marshal(val)
			return j
		}
		return m
	})
	var out map[string]any
	if err := json.Unmarshal(resolved, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// normalization
// ---------------------------------------------------------------------------

// maskedFields are field names whose values are replaced with "<masked>".
var maskedFields = map[string]bool{
	"resourceVersion":   true,
	"creationTimestamp": true,
	"managedFields":     true,
	"uid":               true,
	"generation":        true,
	"confirmationToken": true,
	"expiresAt":         true,
	"ExpiresAt":         true,
	"token":             true,
	"nonce":             true,
}

// metricsContainers are keys whose subtrees contain volatile numeric leaves
// (CPU/memory usage, timestamps of metric samples). Every numeric leaf under
// them is masked.
var metricsContainers = map[string]bool{
	"metrics": true,
	"usage":   true,
	"Metrics": true,
	"Usage":   true,
}

// normalize extracts Content[].Text from the raw CallToolResult, parses each
// as JSON when possible, masks volatile fields, and re-serializes with sorted
// keys and 2-space indentation. Non-JSON text is kept verbatim. The envelope
// (isError, _meta) is preserved.
func normalize(raw json.RawMessage) ([]byte, error) {
	var res map[string]any
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("unmarshaling result: %w", err)
	}
	if content, ok := res["content"].([]any); ok {
		for i, c := range content {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			text, ok := cm["text"].(string)
			if !ok {
				continue
			}
			var parsed any
			if err := json.Unmarshal([]byte(text), &parsed); err == nil {
				maskVolatile(parsed, false)
				buf, err := marshalSorted(parsed)
				if err != nil {
					return nil, err
				}
				// NB: buf is []byte; assigning it directly would make the
				// outer json.Marshal base64-encode it. Convert to string.
				cm["text"] = string(buf)
			} else {
				// Non-JSON text: mask obvious secrets just in case, keep shape.
				cm["text"] = maskInlineSecrets(text)
			}
			content[i] = cm
		}
		res["content"] = content
	}
	return marshalSorted(res)
}

// maskVolatile walks a decoded JSON value and applies the masking rules:
//   - fields named in maskedFields are replaced entirely with "<masked>"
//   - under metrics/usage containers every numeric leaf becomes "<masked>"
//     (JSON numbers and quantity-formatted strings like "851950646n",
//     "11142728Ki"); structure and non-numeric fields are preserved
func maskVolatile(v any, inMetrics bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if maskedFields[k] {
				t[k] = "<masked>"
				continue
			}
			childMetrics := inMetrics || metricsContainers[k]
			if childMetrics && (isNumericLeaf(val) || isQuantityString(val)) {
				t[k] = "<masked>"
				continue
			}
			maskVolatile(val, childMetrics)
		}
	case []any:
		for _, e := range t {
			maskVolatile(e, inMetrics)
		}
	}
}

// quantityRe matches k8s quantity-formatted numbers, including the binary
// suffixes (e.g. "851950646n", "11142728Ki", "100m", "2Gi", "512Mi").
var quantityRe = regexp.MustCompile(`^-?\d+(\.\d+)?(n|u|m|k|M|G|T|P|E|Ki|Mi|Gi|Ti|Pi|Ei)?$`)

// isNumericLeaf reports whether v is a JSON number (int or float).
func isNumericLeaf(v any) bool {
	switch v.(type) {
	case float64, int, int64, json.Number:
		return true
	}
	return false
}

// isQuantityString reports whether v is a k8s quantity-formatted numeric
// string (e.g. "851950646n", "11142728Ki").
func isQuantityString(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	return quantityRe.MatchString(s)
}

// secretTokenRe matches token-shaped substrings in non-JSON text.
var secretTokenRe = regexp.MustCompile(`token-[A-Za-z0-9]+:[A-Za-z0-9]+`)

func maskInlineSecrets(text string) string {
	return secretTokenRe.ReplaceAllString(text, "<masked>")
}

// marshalSorted serializes with sorted keys and 2-space indentation,
// deterministically (map iteration order never leaks).
func marshalSorted(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sortJSON(v)); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// sortJSON round-trips v so that every map is a sorted-key representation,
// guaranteeing deterministic output from json.Encoder.
func sortJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, k := range keys {
			out[k] = sortJSON(t[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = sortJSON(e)
		}
		return out
	default:
		return v
	}
}
