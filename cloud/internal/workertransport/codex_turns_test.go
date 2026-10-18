package workertransport

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

const (
	codexCLIMeta     = `{"timestamp":"2026-10-18T06:00:00Z","type":"session_meta","payload":{"id":"t1","originator":"codex-tui","source":"cli"}}` + "\n"
	codexChatMeta    = `{"timestamp":"2026-10-18T06:00:00Z","type":"session_meta","payload":{"id":"t2","originator":"agent-orchestrator","source":"vscode"}}` + "\n"
	codexTurnStarted = `{"timestamp":"2026-10-18T06:00:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"u1"}}` + "\n"
	// Recorded from Codex 0.150 when its sign-in expired mid-session.
	codexTurnFailed = `{"timestamp":"2026-10-18T06:00:02Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"u1","last_agent_message":null,"error":{"message":"Your access token could not be refreshed because your refresh token was already used. Please log out and sign in again.","codex_error_info":"unauthorized"}}}` + "\n"
	codexTurnDone   = `{"timestamp":"2026-10-18T06:00:02Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"u1","last_agent_message":"done"}}` + "\n"
)

func writeRollout(t *testing.T, codexHome, name, content string) string {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "10", "18")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendRollout(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func TestCodexTurnFailedRecord(t *testing.T) {
	if message, failed := codexTurnFailedRecord([]byte(codexTurnFailed)); !failed || message == "" {
		t.Fatalf("failed turn = %q/%v, want reported with its message", message, failed)
	}
	for _, line := range []string{
		codexTurnDone,
		codexTurnStarted,
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"u1","error":null}}`,
		`{"type":"response_item","payload":{"type":"task_complete","error":{"message":"x"}}}`,
		`not json`,
	} {
		if _, failed := codexTurnFailedRecord([]byte(line)); failed {
			t.Fatalf("record %s reported as a failed turn", line)
		}
	}
}

func TestCodexRolloutTailReportsOnlyNewFailedTurns(t *testing.T) {
	home := t.TempDir()
	// A failure from before the terminal started is history, not this turn.
	path := writeRollout(t, home, "rollout-a.jsonl", codexCLIMeta+codexTurnStarted+codexTurnFailed)
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	tail := newCodexRolloutTail(home, time.Now())
	if failures := tail.poll(); len(failures) != 0 {
		t.Fatalf("history reported as failures: %v", failures)
	}
	appendRollout(t, path, codexTurnStarted+codexTurnDone)
	if failures := tail.poll(); len(failures) != 0 {
		t.Fatalf("a successful turn was reported: %v", failures)
	}
	// A record split across polls is read once it is complete.
	appendRollout(t, path, codexTurnFailed[:40])
	if failures := tail.poll(); len(failures) != 0 {
		t.Fatalf("a partial record was reported: %v", failures)
	}
	appendRollout(t, path, codexTurnFailed[40:])
	if failures := tail.poll(); len(failures) != 1 {
		t.Fatalf("failures = %v, want the failed turn", failures)
	}
}

// A resumed conversation (codex resume, or a Codex adopted across a worker
// restart) appends to the rollout that already holds its earlier turns. Its
// old failure is history even though the file changes after the watcher
// starts.
func TestCodexRolloutTailSkipsAResumedRolloutsHistory(t *testing.T) {
	home := t.TempDir()
	path := writeRollout(t, home, "rollout-a.jsonl", codexCLIMeta+codexTurnStarted+codexTurnFailed)
	tail := newCodexRolloutTail(home, time.Now().Add(-time.Second))
	appendRollout(t, path, codexTurnStarted)
	if failures := tail.poll(); len(failures) != 0 {
		t.Fatalf("a resumed rollout's old failure was reported mid-turn: %v", failures)
	}
	appendRollout(t, path, codexTurnFailed)
	if failures := tail.poll(); len(failures) != 1 {
		t.Fatalf("failures = %v, want the resumed turn's failure", failures)
	}
}

func TestCodexRolloutTailIgnoresChatRollouts(t *testing.T) {
	home := t.TempDir()
	tail := newCodexRolloutTail(home, time.Now().Add(-time.Second))
	writeRollout(t, home, "rollout-chat.jsonl", codexChatMeta+codexTurnStarted+codexTurnFailed)
	if failures := tail.poll(); len(failures) != 0 {
		t.Fatalf("a Chat app-server rollout was reported: %v", failures)
	}
	writeRollout(t, home, "rollout-tui.jsonl", codexCLIMeta+codexTurnStarted+codexTurnFailed)
	tail.scans = 0
	if failures := tail.poll(); len(failures) != 1 {
		t.Fatalf("failures = %v, want the terminal rollout's failed turn", failures)
	}
}

type codexActivityControl struct {
	supervisorControlStub
	mu       sync.Mutex
	activity []worker.ActivityEvent
	got      chan struct{}
}

func (c *codexActivityControl) PublishActivity(_ context.Context, event worker.ActivityEvent) error {
	c.mu.Lock()
	c.activity = append(c.activity, event)
	c.mu.Unlock()
	select {
	case c.got <- struct{}{}:
	default:
	}
	return nil
}

// A Codex terminal turn that ends on an error fires no Stop hook. The worker
// must still report the agent idle and write the terminal stop proof, or a
// switch to Chat waits forever for the turn to finish.
func TestCodexTerminalTurnFailureMarksAgentIdle(t *testing.T) {
	previous := codexTurnPollInterval
	codexTurnPollInterval = 20 * time.Millisecond
	defer func() { codexTurnPollInterval = previous }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	home, dataDir := t.TempDir(), t.TempDir()
	terminalID := "00000000-0000-0000-0000-000000000061"
	control := &codexActivityControl{got: make(chan struct{}, 1)}
	supervisor := &Supervisor{
		Control: control, Workspace: t.TempDir(), DataDir: dataDir, Harness: "codex",
		AgentTerminalID: terminalID,
		AgentCommand: workerexec.Command{
			Path: "/bin/sh", Args: []string{"-c", "cat"},
			Env: map[string]string{"CODEX_HOME": home},
		},
		terminals: make(map[string]*terminalProcess),
	}
	supervisor.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	defer supervisor.closeAllTerminals()
	if err := supervisor.openTerminal(ctx, worker.TerminalCommand{TerminalID: terminalID, Kind: "agent"}); err != nil {
		t.Fatal(err)
	}
	// The user typed the prompt that failed.
	supervisor.mu.Lock()
	supervisor.lastTUIInputAt = time.Now()
	supervisor.mu.Unlock()
	if idle, _ := supervisor.tuiSourceIdle(); idle {
		t.Fatal("terminal reported idle before the turn ended")
	}
	time.Sleep(10 * time.Millisecond)
	writeRollout(t, home, "rollout-tui.jsonl", codexCLIMeta+codexTurnStarted+codexTurnFailed)
	select {
	case <-control.got:
	case <-time.After(5 * time.Second):
		t.Fatal("the failed Codex turn was not reported")
	}
	control.mu.Lock()
	event := control.activity[0]
	control.mu.Unlock()
	if event.Harness != "codex" || event.Event != "stop-failure" ||
		event.State != contract.ActivityIdle || event.SourceInterface != "tui" {
		t.Fatalf("activity = %+v, want codex stop-failure idle from the terminal", event)
	}
	if !worker.ValidActivityEvent(event) {
		t.Fatalf("the control plane would reject %+v", event)
	}
	if idle, unverified := supervisor.tuiSourceIdle(); !idle || unverified {
		t.Fatalf("terminal idle = %v (unverified %v), want verified idle after the failed turn", idle, unverified)
	}
}
