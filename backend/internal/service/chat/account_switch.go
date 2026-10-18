package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// AccountSwitchedEvent marks, on a turn an account move stopped, the account
// the conversation continues on.
const AccountSwitchedEvent = "account.switched"

// MarkAccountSwitch records that the session's latest turn stopped for an
// account move that began at since, rather than failing. Stopping the old
// agent races its reply to the cancel, so the turn may have settled as failed;
// it settles as interrupted, and a notice names the account the next message
// continues on. A turn that finished before the move is left alone.
func (s *Service) MarkAccountSwitch(ctx context.Context, id domain.SessionID, accountID string, since time.Time) error {
	reader, ok := s.store.(promptSuggestionTurnReader)
	if !ok {
		return nil
	}
	conversation, err := s.store.ConversationForSession(ctx, id)
	if errors.Is(err, domain.ErrNoConversation) {
		return nil
	}
	if err != nil {
		return err
	}
	turn, err := reader.LatestConversationTurn(ctx, conversation.ID)
	if errors.Is(err, domain.ErrNoConversationTurn) {
		return nil
	}
	if err != nil {
		return err
	}
	if turn.ProviderTurnID == "" || turn.CompletedAt == nil || turn.CompletedAt.Before(since) ||
		(turn.State != domain.TurnStateFailed && turn.State != domain.TurnStateInterrupted) {
		return nil
	}
	now := s.now()
	if err := s.store.SettleTurnByID(ctx, turn.ID, domain.TurnStateInterrupted, "", now); err != nil {
		return err
	}
	detail, err := json.Marshal(map[string]string{"event": AccountSwitchedEvent, "accountId": accountID})
	if err != nil {
		return fmt.Errorf("encode account switch: %w", err)
	}
	return s.store.UpsertActivity(ctx, conversation.ID, turn.ProviderTurnID, domain.ConversationActivity{
		ID:             s.newID(),
		Kind:           domain.ActivityKindSystem,
		Status:         domain.ActivityStatusCompleted,
		Summary:        "Switched account",
		Detail:         detail,
		ProviderItemID: "account-switch:" + turn.ID,
	}, now)
}
