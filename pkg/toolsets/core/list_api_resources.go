package core

import (
	"context"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type listAPIResourcesParams struct {
	Cluster string `json:"cluster" jsonschema:"the name of the Kubernetes cluster"`
	Group   string `json:"group,omitempty" jsonschema:"optional exact API group filter (e.g. harvesterhci.io). Empty returns all groups"`
	Kind    string `json:"kind,omitempty" jsonschema:"optional kind filter, case-insensitive exact match (e.g. VirtualMachine)"`
}

type apiResourceInfo struct {
	Group      string `json:"group"`
	Version    string `json:"version"`
	Kind       string `json:"kind"`
	Resource   string `json:"resource"`
	Namespaced bool   `json:"namespaced"`
}

// listAPIResources returns every API resource type served by the cluster so
// the agent can discover custom resources before operating on them.
func (t *Tools) listAPIResources(ctx context.Context, toolReq *mcp.CallToolRequest, params listAPIResourcesParams) (*mcp.CallToolResult, any, error) {
	zap.L().Debug("listAPIResources called", zap.String("cluster", params.Cluster))

	lists, err := t.client.ListAPIResources(ctx, middleware.Token(ctx), params.Cluster)
	if err != nil {
		return nil, nil, err
	}

	rows := make([]apiResourceInfo, 0)
	for _, list := range lists {
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}
		if params.Group != "" && gv.Group != params.Group {
			continue
		}
		for _, ar := range list.APIResources {
			if strings.Contains(ar.Name, "/") { // skip subresources
				continue
			}
			if params.Kind != "" && !strings.EqualFold(ar.Kind, params.Kind) {
				continue
			}
			rows = append(rows, apiResourceInfo{Group: gv.Group, Version: gv.Version, Kind: ar.Kind, Resource: ar.Name, Namespaced: ar.Namespaced})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Group != rows[j].Group {
			return rows[i].Group < rows[j].Group
		}
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].Version < rows[j].Version
	})

	mcpResponse, err := response.CreateMcpResponseAny(rows)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}}}, nil, nil
}
