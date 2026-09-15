// Package toolconfig carries the server's tool registration and safety
// configuration to every toolset without import cycles.
package toolconfig

import "github.com/rancher/rancher-ai-mcp/pkg/confirm"

// Config controls which tools are registered and how mutating tools are gated.
type Config struct {
	// ReadOnly registers only read-only tools (highest precedence).
	ReadOnly bool
	// AutoWrite lets create/update-class tools execute without the
	// confirmation token and user confirmation. Delete and exec tools are
	// NEVER exempted. Enable only for trusted automation.
	AutoWrite bool
	// EnableExec registers the execPod tools (off by default).
	EnableExec bool
	// Gate is the confirmation gate shared by all mutating tools.
	Gate *confirm.Gate
}
