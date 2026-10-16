package sessionmanager

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SwitchAgentAccount moves a session's agent to accountID. The binding changes
// first, so the account applies at the agent's next start. The agent is then
// stopped and resumed in the same native conversation under the new account;
// an exited agent is left exited. A turn in progress is refused unless
// stopTurn says the caller accepts losing it, as when the turn is only waiting
// for the old account's limit to reset.
func (m *Manager) SwitchAgentAccount(ctx context.Context, id domain.SessionID, accountID string, stopTurn bool) (domain.SessionRecord, error) {
	if m.agentAccounts == nil {
		return domain.SessionRecord{}, fmt.Errorf("switch account %s: %w", id, ports.ErrAgentAccountUnknown)
	}
	rec, err := m.getRecord(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, fmt.Errorf("switch account %s: %w", id, err)
	}
	if rec.IsTerminated {
		return domain.SessionRecord{}, fmt.Errorf("switch account %s: %w", id, ErrTerminated)
	}
	switch rec.Activity.State {
	case domain.ActivityActive, domain.ActivityBlocked:
		if stopTurn {
			break
		}
		return domain.SessionRecord{}, fmt.Errorf("switch account %s: %w", id, ports.ErrAgentAccountSessionBusy)
	}
	if err := m.agentAccounts.BindSession(ctx, id, rec.Harness, accountID); err != nil {
		return domain.SessionRecord{}, fmt.Errorf("switch account %s: %w", id, err)
	}
	if rec.Activity.State == domain.ActivityExited {
		return rec, nil
	}
	if _, err := m.ExitAgent(ctx, id); err != nil {
		return domain.SessionRecord{}, err
	}
	res, err := m.ResumeAgentWithMode(ctx, id)
	if err != nil {
		return domain.SessionRecord{}, err
	}
	return res.Session, nil
}
