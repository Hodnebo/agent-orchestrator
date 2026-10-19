package codexappserver

import "github.com/aoagents/agent-orchestrator/backend/internal/ports"

var _ ports.ChatDiagnosticDriver = (*Driver)(nil)

// DiagnosticPolicy reports Codex's native workspace sandbox and sandboxed tool
// network controls. It makes no claim about inherited MCP servers, apps or hooks.
func (*Driver) DiagnosticPolicy() ports.ChatDiagnosticPolicy {
	return ports.ChatDiagnosticPolicy{ReadOnly: true, DisableToolNetwork: true}
}
