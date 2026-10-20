package lifecycle

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestStopFromAnOldThreadDoesNotAnswerTheCurrentTurn(t *testing.T) {
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	store := &turnRecordingStore{fakeStore: newFakeStore()}
	store.sessions["s-1"] = domain.SessionRecord{ID: "s-1", Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: at},
		Metadata: domain.SessionMetadata{RuntimeLaunchID: "launch", AgentSessionID: "new-thread", AgentSessionIDLaunchID: "launch", NativeIdentityObservedAt: at}}
	manager := New(store, nil)
	if err := manager.ApplyActivitySignal(context.Background(), "s-1", ports.ActivitySignal{
		Event: "user-prompt-submit", Valid: true, State: domain.ActivityActive, LaunchID: "launch",
		AgentSessionID: "new-thread", Timestamp: at.Add(time.Second), LatestUserPrompt: "implement the new task",
		TurnUserRequest: "implement the new task", ProviderTurnID: "new-turn",
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyActivitySignal(context.Background(), "s-1", ports.ActivitySignal{
		Event: "stop", Valid: true, State: domain.ActivityIdle, LaunchID: "launch",
		AgentSessionID: "old-thread", Timestamp: at.Add(-time.Second), LatestAssistantUpdate: "old task complete",
		FinalAssistantMessage: "old task complete", ProviderTurnID: "old-turn",
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"request:codex:new-thread:implement the new task"}
	if !reflect.DeepEqual(store.calls, want) {
		t.Fatalf("rejected old-thread stop answered the current thread: got %q; want %q", store.calls, want)
	}
}

func TestStopFromAnOldThreadLeavesTheStoredTerminalHistoryAlone(t *testing.T) {
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	ctx := context.Background()
	store := sqlitetest.MustOpen(t)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "terminal-review", Path: t.TempDir(), RegisteredAt: at}); err != nil {
		t.Fatal(err)
	}
	rec, err := store.CreateSession(ctx, domain.SessionRecord{ProjectID: "terminal-review", Kind: domain.KindWorker,
		Harness: domain.HarnessCodex, Mode: domain.SessionModeTUI, CreatedAt: at, UpdatedAt: at,
		Activity: domain.Activity{State: domain.ActivityIdle, LastActivityAt: at},
		Metadata: domain.SessionMetadata{RuntimeLaunchID: "launch", AgentSessionID: "new-thread", AgentSessionIDLaunchID: "launch", NativeIdentityObservedAt: at}})
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, nil)
	if err := manager.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Event: "user-prompt-submit", Valid: true, State: domain.ActivityActive, LaunchID: "launch",
		AgentSessionID: "new-thread", Timestamp: at.Add(time.Second), LatestUserPrompt: "implement the new task",
		TurnUserRequest: "implement the new task", ProviderTurnID: "new-turn",
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ApplyActivitySignal(ctx, rec.ID, ports.ActivitySignal{
		Event: "stop", Valid: true, State: domain.ActivityIdle, LaunchID: "launch",
		AgentSessionID: "old-thread", Timestamp: at.Add(-time.Second), LatestAssistantUpdate: "old task complete",
		FinalAssistantMessage: "old task complete", ProviderTurnID: "old-turn",
	}); err != nil {
		t.Fatal(err)
	}
	turns, err := store.ListTerminalTurns(ctx, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].UserRequest != "implement the new task" || turns[0].AgentResponse != "" {
		t.Fatalf("rejected stop corrupted handoff history: %+v", turns)
	}
}
