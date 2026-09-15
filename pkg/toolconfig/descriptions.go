package toolconfig

// AutoWriteProtocol is the single source of the SECURITY preamble carried by
// every create/patch-class execute tool when the server runs with
// --allow-auto-write. In that mode the confirmation gate deliberately bypasses
// both the single-use confirmationToken and the server-initiated user
// confirmation, so the default plan-token preamble would be false. This text
// declares the automation mode instead (spec §5.1: "描述如实声明自动化模式").
//
// It must keep stating that deleteKubernetesResource and execPod are NOT
// exempt: the literal `false` bypass argument in delete_resource.go and
// exec_pod.go keeps them fully gated in every mode.
const AutoWriteProtocol = `SECURITY: AUTO-WRITE MODE: this server executes this tool IMMEDIATELY when you call it — there is NO confirmationToken to pass and NO server-initiated user confirmation to wait for. Call this tool ONLY when the user has explicitly asked for this exact operation; never proactively, never in batches. deleteKubernetesResource and execPod are NOT exempt: even in auto-write mode they ALWAYS require the full plan + user-confirmation protocol.`

// SecurityProtocol returns the SECURITY preamble of a create/patch-class
// execute-tool description. In the default mode it returns defaultProtocol
// unchanged: the plan-token paragraph is literally true there, because the gate
// requires the confirmationToken and asks the user directly.
//
// When cfg.AutoWrite is set it returns AutoWriteProtocol instead, so the
// description never promises a confirmation the server will not perform.
// Plan tools and the delete/exec tools must NOT be routed through this helper:
// plan tools still mint tokens in every mode, and delete/exec always require
// the full protocol.
func SecurityProtocol(cfg Config, defaultProtocol string) string {
	if cfg.AutoWrite {
		return AutoWriteProtocol
	}
	return defaultProtocol
}
