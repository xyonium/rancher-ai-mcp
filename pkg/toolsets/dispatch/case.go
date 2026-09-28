package dispatch

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CallHandler is the uniform shape of an enum-dispatched tool call.
type CallHandler[P any] func(ctx context.Context, req *mcp.CallToolRequest, p P) (*mcp.CallToolResult, any, error)

// Case binds one enum value to its required fields and its handler.
type Case[P any] struct {
	// Required lists json field names that must be non-zero for this case.
	Required []string
	Handler  CallHandler[P]
}

// MergeMaps unions case maps and panics on duplicate keys: a duplicate enum
// value would silently shadow one handler, which must be a build-time failure.
func MergeMaps[P any](maps ...map[string]Case[P]) map[string]Case[P] {
	out := make(map[string]Case[P])
	for _, m := range maps {
		for k, v := range m {
			if _, dup := out[k]; dup {
				panic(fmt.Sprintf("dispatch: duplicate case key %q", k))
			}
			out[k] = v
		}
	}
	return out
}

// Dispatch validates the discriminator value and required fields, then runs
// the case handler.
func Dispatch[P any](ctx context.Context, req *mcp.CallToolRequest, discriminator, key string, p P, cases map[string]Case[P]) (*mcp.CallToolResult, any, error) {
	c, ok := cases[key]
	if !ok {
		keys := make([]string, 0, len(cases))
		for k := range cases {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return nil, nil, fmt.Errorf("unknown %s %q; valid values: %s", discriminator, key, strings.Join(keys, ", "))
	}
	if err := Validate(discriminator, key, p, c.Required); err != nil {
		return nil, nil, err
	}
	return c.Handler(ctx, req, p)
}
