package dispatch

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// The enum sources of truth. The schemas are built from these lists, and the
// merged registration test asserts the runtime case maps have exactly these
// keys.
var (
	QueryResources = []string{
		"project", "projects", "resourceUsage", "clusters",
		"user", "roleTemplate", "roleTemplates", "clusterRTBs", "projectRTBs",
		"gitRepo", "gitRepos", "bundle",
		"clusterImages", "k3kClusters", "clusterMachine", "supportedVersions",
	}
	DiagnoseTargets  = []string{"cluster", "machines", "nodes", "fleet", "deployment", "pod"}
	ChangeOperations = []string{
		"createKubernetesResource", "patchKubernetesResource", "deleteKubernetesResource",
		"scaleClusterNodePool", "execPod",
		"createProject", "moveNamespace", "createCustomCluster", "createImportedCluster", "createK3kCluster",
	}
)

// forcePlainType collapses a nullable multi-type property (["null","array"])
// into a single type so strict agent clients (Gemini/Vertex) accept the schema.
// Same trick as the existing patchResourceInputSchema.
func forcePlainType(s *jsonschema.Schema, prop, typ string) {
	if p, ok := s.Properties[prop]; ok {
		p.Type = typ
		p.Types = nil
	}
}

func enumOf(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

func mustSchema(s *jsonschema.Schema, err error) *jsonschema.Schema {
	if err != nil {
		panic(fmt.Errorf("dispatch: building input schema: %w", err))
	}
	return s
}

// permissivePatchItemsSchema returns the items schema for the patch parameter.
// json.RawMessage infers items as integer(0-255); a JSON patch is an array of
// objects, so the items schema is replaced with a permissive one — the
// handler's patchList() validates the real shape.
//
// The schema must serialize to a plain JSON object, never a boolean: an empty
// &jsonschema.Schema{} marshals as the boolean subschema `true`, which is
// valid JSON Schema 2020-12 but rejected by strict OpenAPI-style validators
// used by some OpenAI-compatible upstreams (Volcano Engine Ark error 11133,
// codebuddyCN error 400001). {"type":["null","object"]} is semantically
// equivalent (accepts any item, null included to match jsonschema-go's
// nullable-inference convention) while staying object-shaped.
func permissivePatchItemsSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"null", "object"}}
}

// QueryInputSchema builds the rancherQuery input schema.
func QueryInputSchema() *jsonschema.Schema {
	s := mustSchema(jsonschema.For[QueryParams](nil))
	s.Properties["resource"].Enum = enumOf(QueryResources)
	forcePlainType(s, "clusters", "array")
	return s
}

// DiagnoseInputSchema builds the diagnose input schema.
func DiagnoseInputSchema() *jsonschema.Schema {
	s := mustSchema(jsonschema.For[DiagnoseParams](nil))
	s.Properties["target"].Enum = enumOf(DiagnoseTargets)
	return s
}

// PlanInputSchema builds the planChange input schema (no token required).
func PlanInputSchema() *jsonschema.Schema {
	s := mustSchema(jsonschema.For[ChangeParams](nil))
	s.Properties["operation"].Enum = enumOf(ChangeOperations)
	forcePlainType(s, "command", "array")
	forcePlainType(s, "patch", "array")
	s.Properties["patch"].Items = permissivePatchItemsSchema()
	return s
}

// ExecuteInputSchema builds the executeChange input schema. requireToken
// mirrors the server's auto-write mode: when writes are auto-approved there is
// no token to pass, so the schema must not demand one.
func ExecuteInputSchema(requireToken bool) *jsonschema.Schema {
	s := mustSchema(jsonschema.For[ChangeParams](nil))
	s.Properties["operation"].Enum = enumOf(ChangeOperations)
	forcePlainType(s, "command", "array")
	forcePlainType(s, "patch", "array")
	s.Properties["patch"].Items = permissivePatchItemsSchema()
	if requireToken {
		s.Required = append(s.Required, "confirmationToken")
	}
	return s
}
