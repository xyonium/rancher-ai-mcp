package cmd

import (
	"strconv"
	"testing"

	"github.com/rancher/rancher-ai-mcp/pkg/confirm"
	"github.com/rancher/rancher-ai-mcp/pkg/toolsets"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeCmd(t *testing.T) {
	assert.NotNil(t, serveCmd)
	assert.Equal(t, "serve", serveCmd.Use)
	assert.Equal(t, "Start the MCP server", serveCmd.Short)
	assert.NotNil(t, serveCmd.RunE)
}

func TestRunServeCommand(t *testing.T) {
	// Create a new command instance to avoid modifying the global one
	testCmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP server",
		Long:  `Start the MCP server to handle requests from the Rancher AI agent`,
	}

	testCmd.Flags().IntVar(&port, "port", 9092, "Port to listen on")
	testCmd.Flags().BoolVar(&insecure, "insecure", false, "Skip TLS verification")

	// Verify flags exist and have correct defaults
	portFlag := testCmd.Flags().Lookup("port")
	require.NotNil(t, portFlag)
	assert.Equal(t, "9092", portFlag.DefValue)

	insecureFlag := testCmd.Flags().Lookup("insecure")
	require.NotNil(t, insecureFlag)
	assert.Equal(t, "false", insecureFlag.DefValue)
}

func TestServeFlagsRegistered(t *testing.T) {
	for name, def := range map[string]string{"allow-auto-write": "false", "enable-exec": "false"} {
		flag := serveCmd.Flags().Lookup(name)
		require.NotNil(t, flag, "flag --%s must be registered", name)
		assert.Equal(t, def, flag.DefValue)
	}
}

func TestBoolFlagOrEnv(t *testing.T) {
	const (
		flagName = "allow-auto-write"
		envName  = "MCP_ALLOW_AUTO_WRITE"
	)

	tests := []struct {
		name    string
		setFlag bool
		flagVal bool
		envVal  string
		want    bool
	}{
		{name: "no flag no env uses default", want: false},
		{name: "env true, flag not set", envVal: "true", want: true},
		{name: "env 1, flag not set", envVal: "1", want: true},
		{name: "env false, flag not set", envVal: "false", want: false},
		{name: "invalid env ignored", envVal: "not-a-bool", want: false},
		{name: "explicit flag false beats env true", setFlag: true, flagVal: false, envVal: "true", want: false},
		{name: "explicit flag true beats env false", setFlag: true, flagVal: true, envVal: "false", want: true},
		{name: "explicit flag true with no env", setFlag: true, flagVal: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envName, tt.envVal)

			cmd := &cobra.Command{Use: "test"}
			var flagVal bool
			cmd.Flags().BoolVar(&flagVal, flagName, false, "test flag")
			if tt.setFlag {
				require.NoError(t, cmd.Flags().Set(flagName, strconv.FormatBool(tt.flagVal)))
			}

			assert.Equal(t, tt.want, boolFlagOrEnv(cmd, flagName, envName, flagVal))
		})
	}
}

// TestServeConfigGate is the load-bearing wiring check: the configuration the
// server actually runs with must carry a real confirmation gate. Every write
// tool dereferences it, so a nil gate means panics (plan tools) or
// unconditional failure (execute tools) in production.
func TestServeConfigGate(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		cfg, err := serveConfig(readOnly, false, false)
		require.NoError(t, err)
		assert.NotNil(t, cfg.Gate, "serveConfig must always inject a confirmation gate (readOnly=%v)", readOnly)
		assert.Equal(t, readOnly, cfg.ReadOnly)
	}

	// The token gate on the injected gate must be live.
	cfg, err := serveConfig(false, true, true)
	require.NoError(t, err)
	require.NotNil(t, cfg.Gate)
	op := confirm.Operation{Tool: "createKubernetesResource", Cluster: "c-abc", Namespace: "default", Kind: "ConfigMap", Name: "cm"}
	tok, err := cfg.Gate.IssueToken(op)
	require.NoError(t, err)
	require.NoError(t, cfg.Gate.RequireToken(op, tok))
}

func TestServeConfigModes(t *testing.T) {
	cfg, err := serveConfig(true, false, false)
	require.NoError(t, err)
	assert.True(t, cfg.ReadOnly)
	assert.NotContains(t, toolsets.SafetyInstructions(cfg), "Write tool")

	cfg, err = serveConfig(false, true, true)
	require.NoError(t, err)
	assert.True(t, cfg.AutoWrite)
	assert.True(t, cfg.EnableExec)
	instr := toolsets.SafetyInstructions(cfg)
	assert.Contains(t, instr, "AUTO-WRITE mode")
	assert.Contains(t, instr, "execPod")
	assert.Contains(t, instr, "deleteKubernetesResource")
}
