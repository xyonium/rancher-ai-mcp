package provisioning

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/rancher/rancher-ai-mcp/internal/middleware"
	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/response"
	"github.com/rancher/rancher-ai-mcp/pkg/utils"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type createImportedClusterParams struct {
	Name                     string `json:"name" jsonschema:"the name of the cluster to be created"`
	Description              string `json:"description,omitempty" jsonschema:"a short description added to the cluster"`
	VersionManagementSetting string `json:"VersionManagementSetting,omitempty" jsonschema:"specifies the version management setting for the cluster. Potential values are system-default, true, and false. If not specified, the global version management setting will be used"`

	ConfirmationToken string `json:"confirmationToken,omitempty" jsonschema:"REQUIRED (unless the server runs in auto-write mode): the single-use confirmationToken returned by createImportedClusterPlan for THIS exact operation. Never invent, reuse, or guess a token"`
}

// createImportedCluster creates an imported cluster. The execution is gated
// behind a single-use plan token plus a direct user confirmation, and the user
// approves the exact cluster object submitted.
func (t *Tools) createImportedCluster(ctx context.Context, toolReq *mcp.CallToolRequest, params createImportedClusterParams) (*mcp.CallToolResult, any, error) {
	log := utils.NewChildLogger(toolReq, map[string]string{
		"Name":                     params.Name,
		"Description":              params.Description,
		"versionManagementSetting": params.VersionManagementSetting,
	})

	log.Debug("Creating imported cluster")

	cluster, err := t.createImportedClusterObj(params)
	if err != nil {
		log.Error("failed to create imported cluster object", zap.Error(err))
		return nil, nil, fmt.Errorf("failed to create imported cluster object: %w", err)
	}

	clusterJSON, err := cluster.MarshalJSON()
	if err != nil {
		log.Error("failed to marshal cluster object to JSON", zap.Error(err))
		return nil, nil, fmt.Errorf("failed to marshal cluster object to JSON: %w", err)
	}

	// The token binds the exact cluster object submitted: the same JSON bytes are
	// shown to the user, hashed into the token and sent to the Rancher API.
	op := confirm.Operation{Tool: "createImportedCluster", Cluster: LocalCluster, Kind: "cluster", Name: params.Name, Payload: clusterJSON}
	summary := fmt.Sprintf("CREATE imported cluster %s in cluster %q with the following object:\n%s", params.Name, LocalCluster, clusterJSON)
	approved, err := t.cfg.Gate.Check(ctx, toolReq.Session, op, params.ConfirmationToken, summary, "", t.cfg.AutoWrite)
	if err != nil {
		return nil, nil, err
	}
	if !approved {
		return confirm.CancelledResult(), nil, nil
	}

	respBody, status, err := makeRancherRequest(ctx, t.client.RancherURL(), http.MethodPost, "v3/clusters", middleware.Token(ctx), clusterJSON)
	if err != nil {
		log.Error("failed to make request to Rancher API to create imported cluster", zap.Error(err))
		return nil, nil, fmt.Errorf("failed to make request to Rancher API to create imported cluster: %w", err)
	}

	if status != http.StatusCreated {
		log.Error("received non-success status code from Rancher API when creating imported cluster", zap.Int("statusCode", status), zap.ByteString("responseBody", respBody))
		return nil, nil, fmt.Errorf("received non-success status code %d from Rancher API when creating imported cluster: %s", status, string(respBody))
	}

	obj := make(map[string]any)
	err = json.Unmarshal(respBody, &obj)
	if err != nil {
		log.Error("failed to unmarshal response body from Rancher API after cluster creation", zap.Error(err))
		return nil, nil, fmt.Errorf("failed to unmarshal response body from Rancher API after cluster creation: %w", err)
	}

	log.Debug("Successfully created imported cluster")
	mcpResponse, err := response.CreateMcpResponse([]*unstructured.Unstructured{{Object: obj}}, LocalCluster)
	if err != nil {
		log.Error("failed to create mcp response", zap.Error(err))
		return nil, nil, err
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: mcpResponse}},
	}, nil, nil
}

func (t *Tools) createImportedClusterObj(params createImportedClusterParams) (*unstructured.Unstructured, error) {
	if params.VersionManagementSetting != "" && (params.VersionManagementSetting != "true" && params.VersionManagementSetting != "false" && params.VersionManagementSetting != "system-default") {
		return nil, fmt.Errorf("invalid value for VersionManagementSetting: %s. Valid values are 'system-default', 'true', and 'false'", params.VersionManagementSetting)
	}

	if params.VersionManagementSetting == "" {
		params.VersionManagementSetting = "system-default"
	}

	if params.Name == "" {
		return nil, fmt.Errorf("name is required")
	}

	cluster := &unstructured.Unstructured{
		Object: make(map[string]interface{}),
	}

	cluster.SetAnnotations(map[string]string{
		"rancher.io/imported-cluster-version-management": params.VersionManagementSetting,
	})

	if err := unstructured.SetNestedField(cluster.Object, "cluster", "type"); err != nil {
		return nil, fmt.Errorf("failed to create cluster object: %w", err)
	}

	if err := unstructured.SetNestedField(cluster.Object, params.Name, "name"); err != nil {
		return nil, fmt.Errorf("failed to create cluster object: %w", err)
	}

	if err := unstructured.SetNestedField(cluster.Object, params.Description, "description"); err != nil {
		return nil, fmt.Errorf("failed to create cluster object: %w", err)
	}

	return cluster, nil
}
