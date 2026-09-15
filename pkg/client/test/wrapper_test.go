package test

import (
	"testing"

	"github.com/rancher/rancher-ai-mcp/pkg/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWrapClientCreateRestConfigValidatesToken guards the wrapper used by the
// core toolset tests: like its sibling methods, CreateRestConfig must reject a
// mismatching token before any cluster config is built.
func TestWrapClientCreateRestConfigValidatesToken(t *testing.T) {
	t.Setenv("RANCHER_URL", "https://rancher.example.com")
	c, err := client.NewClient(true, "")
	require.NoError(t, err)
	wrapped := WrapClient(c, "expected-token")

	cfg, err := wrapped.CreateRestConfig("expected-token", "local")
	require.NoError(t, err)
	assert.Equal(t, "expected-token", cfg.BearerToken)
	assert.Contains(t, cfg.Host, "/k8s/clusters/local")

	_, err = wrapped.CreateRestConfig("wrong-token", "local")
	assert.ErrorContains(t, err, "invalid token")
}
