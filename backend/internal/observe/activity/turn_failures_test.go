package activity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/lifecycle"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func rolloutRecord(at time.Time, payload string) string {
	return `{"timestamp":"` + at.UTC().Format("2006-01-02T15:04:05.000Z") + `","type":"event_msg","payload":` + payload + "}\n"
}

func failedTurn(at time.Time) string {
	return rolloutRecord(at, `{"type":"task_complete","turn_id":"t","last_agent_message":null,"error":{"message":"You've hit your usage limit.","codex_error_info":"usage_limit_exceeded"}}`)
}

func succeededTurn(at time.Time) string {
	return rolloutRecord(at, `{"type":"task_complete","turn_id":"t","last_agent_message":"done"}`)
}

func startedTurn(at time.Time) string {
	return rolloutRecord(at, `{"type":"task_started","turn_id":"t"}`)
}

type turnFailureHarness struct {
	ctx      context.Context
	store    *sqlite.Store
	manager  *lifecycle.Manager
	observer *TurnFailureObserver
	session  domain.SessionRecord
	rollout  string
}

// newTurnFailureHarness runs the real chain: a Codex UserPromptSubmit hook
// signal makes the session active and records its rollout path, then the
// observer reads that rollout through the Codex adapter and writes through
// the lifecycle manager into SQLite.
func newTurnFailureHarness(t *testing.T, promptAt time.Time) *turnFailureHarness {
	t.Helper()
	ctx := context.Background()
	store, err := sqlitetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspace := t.TempDir()
	createdAt := promptAt.Add(-time.Minute)
	if err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "turnfail", Path: workspace, RegisteredAt: createdAt}); err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSession(ctx, domain.SessionRecord{
		ProjectID: "turnfail",
		Kind:      domain.KindWorker,
		Harness:   domain.HarnessCodex,
		Activity:  domain.Activity{State: domain.ActivityIdle, LastActivityAt: createdAt},
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	session.Metadata.RuntimeHandleID = "handle-1"
	session.Metadata.RuntimeLaunchID = "launch-1"
	if err := store.UpdateSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "sessions", "2026", "10", "18")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(dir, "rollout-2026-10-18T10-00-00-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl")
	if err := os.WriteFile(rollout, []byte(`{"type":"session_meta","payload":{"source":"cli"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := lifecycle.New(store, nil)
	h := &turnFailureHarness{
		ctx: ctx, store: store, manager: manager, session: session, rollout: rollout,
		observer: NewTurnFailureObserver(store, manager, fakeAgents{domain.HarnessCodex: codex.New()},
			TurnFailureConfig{Logger: testLogger()}),
	}
	if err := manager.ApplyActivitySignal(ctx, session.ID, ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Timestamp: promptAt, Event: "user-prompt-submit",
		LaunchID: "launch-1", AgentSessionID: "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", TranscriptPath: rollout,
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *turnFailureHarness) appendRollout(t *testing.T, records ...string) {
	t.Helper()
	file, err := os.OpenFile(h.rollout, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(strings.Join(records, "")); err != nil {
		t.Fatal(err)
	}
}

func (h *turnFailureHarness) poll(t *testing.T) domain.SessionRecord {
	t.Helper()
	if err := h.observer.Poll(h.ctx); err != nil {
		t.Fatal(err)
	}
	got, ok, err := h.store.GetSession(h.ctx, h.session.ID)
	if err != nil || !ok {
		t.Fatalf("GetSession: ok=%v err=%v", ok, err)
	}
	return got
}

func TestTurnFailureObserverIdlesAFailedCodexTurn(t *testing.T) {
	promptAt := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Millisecond)
	h := newTurnFailureHarness(t, promptAt)
	if got := h.poll(t); got.Activity.State != domain.ActivityActive {
		t.Fatalf("running turn: state = %q, want active", got.Activity.State)
	}
	if got, _, _ := h.store.GetSession(h.ctx, h.session.ID); got.Metadata.NativeTranscriptPath != h.rollout {
		t.Fatalf("hook did not record the rollout path: %q", got.Metadata.NativeTranscriptPath)
	}

	failedAt := promptAt.Add(2 * time.Second)
	h.appendRollout(t, startedTurn(promptAt.Add(100*time.Millisecond)), failedTurn(failedAt))
	got := h.poll(t)
	if got.Activity.State != domain.ActivityIdle {
		t.Fatalf("failed turn: state = %q, want idle", got.Activity.State)
	}
	// Dated when the turn ended, so terminal input typed after the failure
	// still outranks it for a Chat switch's idle proof.
	if !got.Activity.LastActivityAt.Equal(failedAt) {
		t.Fatalf("idle LastActivityAt = %v, want the failure time %v", got.Activity.LastActivityAt, failedAt)
	}

	// The next prompt starts a new turn; the old failure is not replayed.
	nextPromptAt := failedAt.Add(5 * time.Second)
	h.appendRollout(t, startedTurn(nextPromptAt))
	if err := h.manager.ApplyActivitySignal(h.ctx, h.session.ID, ports.ActivitySignal{
		Valid: true, State: domain.ActivityActive, Timestamp: nextPromptAt, Event: "user-prompt-submit", LaunchID: "launch-1",
	}); err != nil {
		t.Fatal(err)
	}
	if got := h.poll(t); got.Activity.State != domain.ActivityActive {
		t.Fatalf("next turn: state = %q, want active", got.Activity.State)
	}
}

// A resumed conversation whose last turn failed before this prompt must not
// idle the new turn, even before Codex writes the new turn's start.
func TestTurnFailureObserverDoesNotReplayAnEarlierFailure(t *testing.T) {
	promptAt := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Millisecond)
	h := newTurnFailureHarness(t, promptAt)
	h.appendRollout(t, startedTurn(promptAt.Add(-time.Hour)), failedTurn(promptAt.Add(-time.Hour+time.Second)))
	if got := h.poll(t); got.Activity.State != domain.ActivityActive {
		t.Fatalf("state = %q, want active", got.Activity.State)
	}
}

func TestTurnFailureObserverLeavesSuccessfulTurnsToTheStopHook(t *testing.T) {
	promptAt := time.Now().UTC().Add(-10 * time.Second).Truncate(time.Millisecond)
	h := newTurnFailureHarness(t, promptAt)
	h.appendRollout(t, startedTurn(promptAt.Add(100*time.Millisecond)), succeededTurn(promptAt.Add(3*time.Second)))
	if got := h.poll(t); got.Activity.State != domain.ActivityActive {
		t.Fatalf("state = %q, want active (Codex's Stop hook reports a successful turn)", got.Activity.State)
	}
}

func TestTurnFailureObserverIgnoresChatSessions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	rollout := filepath.Join(t.TempDir(), "rollout-2026-10-18T10-00-00-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl")
	if err := os.WriteFile(rollout, []byte(startedTurn(now.Add(-2*time.Second))+failedTurn(now.Add(-time.Second))), 0o600); err != nil {
		t.Fatal(err)
	}
	session := activeSession(now, domain.HarnessCodex)
	session.Mode = domain.SessionModeChat
	session.Metadata.NativeTranscriptPath = rollout
	sink := &fakeSink{}
	observer := NewTurnFailureObserver(fakeSessions{rows: []domain.SessionRecord{session}}, sink,
		fakeAgents{domain.HarnessCodex: codex.New()}, TurnFailureConfig{Logger: testLogger()})
	if err := observer.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.signals) != 0 {
		t.Fatalf("Chat session got %d signals, want none: its controller reports turn ends", len(sink.signals))
	}
	// The same session in the terminal is reported.
	session.Mode = domain.SessionModeTUI
	observer = NewTurnFailureObserver(fakeSessions{rows: []domain.SessionRecord{session}}, sink,
		fakeAgents{domain.HarnessCodex: codex.New()}, TurnFailureConfig{Logger: testLogger()})
	if err := observer.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.signals) != 1 {
		t.Fatalf("terminal session got %d signals, want 1", len(sink.signals))
	}
}

// The transcript is re-read only when it changes, and a report the lifecycle
// did not act on is not re-sent until the session row moves.
func TestTurnFailureObserverReportsAFailureOnce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	dir := t.TempDir()
	rollout := filepath.Join(dir, "rollout-2026-10-18T10-00-00-0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b.jsonl")
	if err := os.WriteFile(rollout, []byte(startedTurn(now.Add(-2*time.Second))+failedTurn(now.Add(-time.Second))), 0o600); err != nil {
		t.Fatal(err)
	}
	session := activeSession(now, domain.HarnessCodex)
	session.Activity.LastActivityAt = now.Add(-3 * time.Second)
	session.Metadata.NativeTranscriptPath = rollout
	session.Revision = 7
	sink := &fakeSink{}
	observer := NewTurnFailureObserver(fakeSessions{rows: []domain.SessionRecord{session}}, sink,
		fakeAgents{domain.HarnessCodex: codex.New()}, TurnFailureConfig{Logger: testLogger()})
	for range 3 {
		if err := observer.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.signals) != 1 {
		t.Fatalf("signals = %d, want 1", len(sink.signals))
	}
	signal := sink.signals[0]
	if !signal.Valid || signal.State != domain.ActivityIdle || signal.Event != TurnFailureEvent ||
		signal.LaunchID != "launch-1" || signal.ExpectedHarness != domain.HarnessCodex ||
		signal.ExpectedRevision == nil || *signal.ExpectedRevision != 7 {
		t.Fatalf("signal = %#v", signal)
	}
}
