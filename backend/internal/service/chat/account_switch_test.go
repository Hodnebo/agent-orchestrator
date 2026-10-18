package chat_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	chatsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/chat"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/store"
)

// A move that stops the turn races the old agent's reply to the cancel, so the
// turn can settle as failed. Marking the switch makes it an interrupted turn
// with a notice of the account the conversation continues on.
func TestMarkAccountSwitchTurnsTheStoppedTurnIntoASwitch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "long work", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	running := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	// The move starts while the turn runs; the harness keeps its own clock.
	moveStarted := running.Turns[0].RequestedAt
	// The reply the old agent sent as it was stopped, with no usable state.
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateFailed
	})

	if err := h.svc.MarkAccountSwitch(ctx, testSession, "acct-b", moveStarted); err != nil {
		t.Fatalf("MarkAccountSwitch: %v", err)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateInterrupted
	})
	if snapshot.Turns[0].ErrorMessage != "" {
		t.Fatalf("stopped turn kept an error: %q", snapshot.Turns[0].ErrorMessage)
	}
	var notices int
	for _, activity := range snapshot.Activities {
		var detail map[string]string
		_ = json.Unmarshal(activity.Detail, &detail)
		if detail["event"] == chatsvc.AccountSwitchedEvent {
			notices++
			if detail["accountId"] != "acct-b" || activity.Kind != domain.ActivityKindSystem {
				t.Fatalf("notice = %+v detail %v", activity, detail)
			}
		}
	}
	if notices != 1 {
		t.Fatalf("account switch notices = %d, want 1", notices)
	}

	// Marking again, or for a move that began after the turn ended, adds nothing.
	if err := h.svc.MarkAccountSwitch(ctx, testSession, "acct-b", moveStarted); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.MarkAccountSwitch(ctx, testSession, "acct-c", moveStarted.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	snapshot = h.awaitSnapshot(t, func(store.ConversationSnapshot) bool { return true })
	notices = 0
	for _, activity := range snapshot.Activities {
		var detail map[string]string
		_ = json.Unmarshal(activity.Detail, &detail)
		if detail["event"] == chatsvc.AccountSwitchedEvent {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("account switch notices after repeats = %d, want 1", notices)
	}
}

// Messages queued behind the stopped turn are swept to interrupted by the stop
// and never ran. The notice still goes on the turn that ran, so the stopped
// turn stays the one Continue resumes.
func TestMarkAccountSwitchSkipsQueuedMessagesTheStopSwept(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "long work", ClientMessageID: "c1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "provider-turn-1"})
	running := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return len(s.Turns) == 1 && s.Turns[0].State == domain.TurnStateRunning
	})
	if _, err := h.svc.Send(ctx, testSession, ports.ChatUserMessage{Text: "queued", ClientMessageID: "c2"}); err != nil {
		t.Fatalf("queue a message: %v", err)
	}
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool { return len(s.Turns) == 2 })
	moveStarted := running.Turns[0].RequestedAt
	if err := h.st.CancelAllQueuedTurns(ctx, running.Conversation.ID, moveStarted.Add(time.Second)); err != nil {
		t.Fatalf("sweep the queue: %v", err)
	}
	h.conv.emit(ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: "provider-turn-1"})
	h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["long work"] == domain.TurnStateFailed
	})

	if err := h.svc.MarkAccountSwitch(ctx, testSession, "acct-b", moveStarted); err != nil {
		t.Fatalf("MarkAccountSwitch: %v", err)
	}
	snapshot := h.awaitSnapshot(t, func(s store.ConversationSnapshot) bool {
		return turnStateByText(t, s)["long work"] == domain.TurnStateInterrupted
	})
	if got := chatsvc.ContinuableTurnID(snapshot.Turns); got != running.Turns[0].ID {
		t.Fatalf("continuable turn = %q, want the stopped turn %q", got, running.Turns[0].ID)
	}
	notices := 0
	for _, activity := range snapshot.Activities {
		var detail map[string]string
		_ = json.Unmarshal(activity.Detail, &detail)
		if detail["event"] == chatsvc.AccountSwitchedEvent && activity.TurnID == running.Turns[0].ID {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("notices on the stopped turn = %d, want 1", notices)
	}
}
