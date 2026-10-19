package claudeacp

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var _ ports.ChatDiagnosticDriver = (*checkpointDriver)(nil)

// DiagnosticPolicy describes the native SDK tool profile installed at launch.
// It does not claim OS filesystem or network isolation.
func (d *checkpointDriver) DiagnosticPolicy() ports.ChatDiagnosticPolicy {
	return ports.ChatDiagnosticPolicy{ReadOnly: true, DisableToolNetwork: true}
}

func (d *checkpointDriver) Start(ctx context.Context, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	if (cfg.ReadOnly || cfg.DisableNetwork) && len(cfg.MCPServers) != 0 {
		return nil, fmt.Errorf("%w: Claude diagnostics do not allow MCP servers", ports.ErrChatCapabilityUnavailable)
	}
	return d.ChatDriver.Start(ctx, cfg)
}

func (d *checkpointDriver) Resume(ctx context.Context, cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
	if cfg.ReadOnly && len(cfg.MCPServers) != 0 {
		return nil, fmt.Errorf("%w: Claude diagnostics do not allow MCP servers", ports.ErrChatCapabilityUnavailable)
	}
	return d.ChatDriver.Resume(ctx, cfg)
}
