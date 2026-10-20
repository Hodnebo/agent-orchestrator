package workertransport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

// setupSwitchHarness is a worker transport whose workspace is still being
// prepared (checkout and Setup), with a terminal agent command that records
// each start in a marker file, as a task run from the command line would.
type setupSwitchHarness struct {
	supervisor *Supervisor
	marker     string
	builds     atomic.Int32
	ready      atomic.Int32
	cleanups   atomic.Int32
	// failBuilds fails that many agent command builds, as a credential that
	// could not be read does.
	failBuilds atomic.Int32
	failuresMu sync.Mutex
	failures   []error
	chatReady  chan struct{}
	cancel     context.CancelFunc
	done       chan error
}

func newSetupSwitchHarness(t *testing.T, initial string) *setupSwitchHarness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h := &setupSwitchHarness{marker: filepath.Join(t.TempDir(), "agent-starts"), cancel: cancel, done: make(chan error, 1), chatReady: make(chan struct{})}
	started := make(chan error, 1)
	h.supervisor = &Supervisor{
		Control:   &supervisorControlStub{agentTerminal: "00000000-0000-0000-0000-000000000061"},
		Workspace: t.TempDir(), Started: started,
		InitialInterface: initial, ChatWorkspaceReady: h.chatReady,
		ChatRunner:  blockingChatRunner{started: make(chan struct{})},
		StartupTask: true,
		AgentCommandFactory: func(context.Context, string, string, string) (workerexec.Command, error) {
			if h.failBuilds.Add(-1) >= 0 {
				return workerexec.Command{}, errors.New("load coding-agent credential: control plane unavailable")
			}
			h.failBuilds.Store(0)
			h.builds.Add(1)
			return workerexec.Command{
				Path: "/bin/sh", Args: []string{"-c", "echo started >> '" + h.marker + "'; sleep 30"},
				Cleanup: func() { h.cleanups.Add(1) },
			}, nil
		},
		AgentReady: func(context.Context) { h.ready.Add(1) },
		AgentStartFailed: func(_ context.Context, err error) {
			h.failuresMu.Lock()
			h.failures = append(h.failures, err)
			h.failuresMu.Unlock()
		},
		agentStartRetryDelay: 10 * time.Millisecond,
	}
	h.supervisor.HoldAgentInputUntilWorkspaceReady()
	go func() { h.done <- h.supervisor.Run(ctx) }()
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Error("transport did not stop")
		}
	})
	return h
}

// switchTo runs a drain switch's stop and start on the serial loop, as the
// control plane's interface commands do.
func (h *setupSwitchHarness) switchTo(t *testing.T, target string) {
	t.Helper()
	err := h.supervisor.runOnLoop(context.Background(), func(ctx context.Context) error {
		if err := h.supervisor.stopInterface(ctx, "drain"); err != nil {
			return err
		}
		return h.supervisor.startInterface(ctx, interfacePayload{TargetInterface: target})
	})
	if err != nil {
		t.Fatalf("switch to %s: %v", target, err)
	}
}

func (h *setupSwitchHarness) agentStarts() int {
	data, err := os.ReadFile(h.marker)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "started")
}

func (h *setupSwitchHarness) waitForAgentStarts(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.agentStarts() < want && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// Long enough for a second, duplicate start to show.
	time.Sleep(200 * time.Millisecond)
	if got := h.agentStarts(); got != want {
		t.Fatalf("terminal agent started %d times, want %d", got, want)
	}
}

// Staging session 7760b66b: a switch from Chat to the terminal while the
// project's Setup ran started the terminal agent (and its task) at once, a
// minute before Setup finished, and published no agent.ready. The switch
// commits, but the agent starts only once the workspace is ready, once.
func TestChatToTerminalDuringSetupStartsTheAgentOnceTheWorkspaceIsReady(t *testing.T) {
	h := newSetupSwitchHarness(t, InterfaceChat)
	h.switchTo(t, InterfaceTUI)
	if got := h.supervisor.Interface(); got != InterfaceTUI {
		t.Fatalf("interface after the switch = %q, want tui", got)
	}
	time.Sleep(300 * time.Millisecond)
	if got := h.agentStarts(); got != 0 {
		t.Fatalf("terminal agent started %d times while Setup ran", got)
	}
	if h.ready.Load() != 0 || h.supervisor.controllerReady(InterfaceTUI).Ready {
		t.Fatal("the terminal agent reported ready while Setup ran")
	}
	// Terminal input sent meanwhile is kept for the agent, not refused.
	if err := h.supervisor.writeTerminal(worker.TerminalCommand{
		TerminalID: "00000000-0000-0000-0000-000000000061", Data: []byte("early"),
	}); err != nil {
		t.Fatalf("input during Setup: %v", err)
	}

	h.supervisor.MarkWorkspaceReady()
	h.waitForAgentStarts(t, 1)
	if got := h.builds.Load(); got != 1 {
		t.Fatalf("agent command built %d times, want 1", got)
	}
	if got := h.ready.Load(); got != 1 {
		t.Fatalf("agent.ready reported %d times, want 1", got)
	}
	if !h.supervisor.controllerReady(InterfaceTUI).Ready {
		t.Fatal("the terminal agent is not ready after Setup")
	}
}

// A failed Setup keeps the workspace unready: the terminal agent a switch
// committed does not start until a retried Setup succeeds.
func TestChatToTerminalAfterFailedSetupStartsOnlyWhenTheRetrySucceeds(t *testing.T) {
	h := newSetupSwitchHarness(t, InterfaceChat)
	// The failed start opens the Chat controller (with its startup error).
	close(h.chatReady)
	h.switchTo(t, InterfaceTUI)
	time.Sleep(300 * time.Millisecond)
	if got := h.agentStarts(); got != 0 {
		t.Fatalf("terminal agent started %d times after a failed Setup", got)
	}
	h.supervisor.MarkWorkspaceReady() // the retry succeeded
	h.waitForAgentStarts(t, 1)
}

// A switch back to Chat before the workspace is ready cancels the terminal
// start: the agent never starts beside the Chat controller.
func TestSwitchBackToChatDuringSetupCancelsTheTerminalStart(t *testing.T) {
	h := newSetupSwitchHarness(t, InterfaceChat)
	h.supervisor.StartupTask = false
	h.switchTo(t, InterfaceTUI)
	h.switchTo(t, InterfaceChat)
	h.supervisor.MarkWorkspaceReady()
	time.Sleep(300 * time.Millisecond)
	if got := h.agentStarts(); got != 0 {
		t.Fatalf("terminal agent started %d times after the switch back to Chat", got)
	}
	if h.supervisor.Interface() != InterfaceChat || h.ready.Load() != 0 {
		t.Fatalf("interface = %q, agent.ready = %d", h.supervisor.Interface(), h.ready.Load())
	}
}

// The Chat start fallback (Claude ACP adapter not ready) moves to the
// terminal on the same path; made before the workspace is marked ready, it
// waits for it too.
func TestChatStartFallbackBeforeTheWorkspaceIsReadyWaitsForIt(t *testing.T) {
	h := newSetupSwitchHarness(t, InterfaceChat)
	if err := h.supervisor.FallBackToTUI(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := h.agentStarts(); got != 0 {
		t.Fatalf("terminal agent started %d times before the workspace was ready", got)
	}
	h.supervisor.MarkWorkspaceReady()
	h.waitForAgentStarts(t, 1)
	if got := h.ready.Load(); got != 1 {
		t.Fatalf("agent.ready reported %d times, want 1", got)
	}
}

// A switch to the terminal after Setup starts the agent at once and reports
// agent.ready, as a terminal session's own start does.
func TestChatToTerminalAfterSetupReportsAgentReady(t *testing.T) {
	h := newSetupSwitchHarness(t, InterfaceChat)
	h.supervisor.MarkWorkspaceReady()
	h.switchTo(t, InterfaceTUI)
	h.waitForAgentStarts(t, 1)
	if got := h.ready.Load(); got != 1 {
		t.Fatalf("agent.ready reported %d times, want 1", got)
	}
}

// A terminal session's agent runs its task from its command line once the
// workspace is ready. Until it starts, a drain to Chat waits for it rather
// than dropping the task; a stop-now switch to Chat during Setup leaves the
// session in Chat and the terminal agent never starts beside it.
func TestTerminalSessionSwitchedToChatDuringSetupDoesNotStartItsAgent(t *testing.T) {
	h := newSetupSwitchHarness(t, InterfaceTUI)
	inspection, err := h.supervisor.inspectInterface()
	if err != nil {
		t.Fatal(err)
	}
	if inspection.(interfaceInspectResult).Idle {
		t.Fatal("a drain treats the terminal as idle before its task ran")
	}
	err = h.supervisor.runOnLoop(context.Background(), func(ctx context.Context) error {
		if err := h.supervisor.stopInterface(ctx, "interrupt"); err != nil {
			return err
		}
		return h.supervisor.startInterface(ctx, interfacePayload{TargetInterface: InterfaceChat})
	})
	if err != nil {
		t.Fatal(err)
	}
	h.supervisor.MarkWorkspaceReady()
	command, err := h.supervisor.AgentCommandFactory(context.Background(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	started, err := h.supervisor.StartStartupAgent(context.Background(), command)
	if err != nil || started {
		t.Fatalf("startup agent start = (%v, %v), want skipped", started, err)
	}
	if h.cleanups.Load() != 1 {
		t.Fatal("the skipped agent command was not cleaned up")
	}
	time.Sleep(200 * time.Millisecond)
	if got := h.agentStarts(); got != 0 {
		t.Fatalf("terminal agent started %d times beside the Chat controller", got)
	}
}

// Without a switch the terminal session's own agent starts as before, and a
// drain then follows the terminal's own idle proof.
func TestTerminalSessionStartsItsAgentOnceTheWorkspaceIsReady(t *testing.T) {
	h := newSetupSwitchHarness(t, InterfaceTUI)
	h.supervisor.MarkWorkspaceReady()
	command, err := h.supervisor.AgentCommandFactory(context.Background(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	started, err := h.supervisor.StartStartupAgent(context.Background(), command)
	if err != nil || !started {
		t.Fatalf("startup agent start = (%v, %v), want started", started, err)
	}
	h.waitForAgentStarts(t, 1)
	if h.supervisor.startupTaskWaiting() {
		t.Fatal("the started agent's task still holds a drain")
	}
}

// blockingAgentTerminalControl holds EnsureAgentTerminal until release closes.
type blockingAgentTerminalControl struct {
	supervisorControlStub
	entered chan struct{}
	release chan struct{}
}

func (c *blockingAgentTerminalControl) EnsureAgentTerminal(ctx context.Context) (worker.AgentTerminalResponse, error) {
	close(c.entered)
	<-c.release
	return c.supervisorControlStub.EnsureAgentTerminal(ctx)
}

// StartStartupAgent returns when its context ends while the loop may still
// run its start; reading the result then must not race the loop writing it
// (run with -race).
func TestStartStartupAgentResultDoesNotRaceTheLoopWhenItsContextEnds(t *testing.T) {
	control := &blockingAgentTerminalControl{
		supervisorControlStub: supervisorControlStub{agentTerminal: "00000000-0000-0000-0000-000000000062"},
		entered:               make(chan struct{}), release: make(chan struct{}),
	}
	s := &Supervisor{
		Control: control, Workspace: t.TempDir(),
		terminals: make(map[string]*terminalProcess), iface: InterfaceTransition{current: InterfaceTUI},
	}
	defer s.closeAllTerminals()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() {
		started, _ := s.StartStartupAgent(ctx, workerexec.Command{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
		result <- started
	}()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for !s.runLoopCommands(context.Background()) {
			time.Sleep(time.Millisecond)
		}
	}()
	<-control.entered
	close(control.release)
	cancel()
	<-result
	<-loopDone
}

// A second switch to the terminal during Setup (terminal -> Chat -> terminal,
// or Chat -> terminal -> Chat -> terminal) reserves the agent terminal again:
// its input is kept for the agent, not refused as a terminal still switching.
func TestSecondSwitchToTerminalDuringSetupTakesTerminalInput(t *testing.T) {
	for _, test := range []struct {
		name     string
		initial  string
		switches []string
	}{
		{"terminal, Chat, terminal", InterfaceTUI, []string{InterfaceChat, InterfaceTUI}},
		{"Chat, terminal, Chat, terminal", InterfaceChat, []string{InterfaceTUI, InterfaceChat, InterfaceTUI}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newSetupSwitchHarness(t, test.initial)
			for _, target := range test.switches {
				err := h.supervisor.runOnLoop(context.Background(), func(ctx context.Context) error {
					if err := h.supervisor.stopInterface(ctx, "interrupt"); err != nil {
						return err
					}
					return h.supervisor.startInterface(ctx, interfacePayload{TargetInterface: target})
				})
				if err != nil {
					t.Fatalf("switch to %s: %v", target, err)
				}
			}
			if err := h.supervisor.writeTerminal(worker.TerminalCommand{
				TerminalID: "00000000-0000-0000-0000-000000000061", Data: []byte("early"),
			}); err != nil {
				t.Fatalf("input to the reserved terminal: %v", err)
			}
			h.supervisor.MarkWorkspaceReady()
			h.waitForAgentStarts(t, 1)
		})
	}
}

// reported is what AgentStartFailed reported: an error for a failure, nil
// for one that no longer stands.
func (h *setupSwitchHarness) reported() []error {
	h.failuresMu.Lock()
	defer h.failuresMu.Unlock()
	return append([]error(nil), h.failures...)
}

func (h *setupSwitchHarness) waitForReports(t *testing.T, want int) []error {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.reported()) < want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	reports := h.reported()
	if len(reports) != want {
		t.Fatalf("AgentStartFailed reported %v, want %d reports", reports, want)
	}
	return reports
}

// retry is the session's Retry: the workspace.checkout request, on the loop.
func (h *setupSwitchHarness) retry(t *testing.T) {
	t.Helper()
	if err := h.supervisor.runOnLoop(context.Background(), func(ctx context.Context) error {
		h.supervisor.handle(ctx, nil, &worker.TransportRequest{ID: "retry", Kind: "workspace.checkout"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A terminal start after Setup that fails once (the credential could not be
// read) is tried again shortly and starts the agent, once, with nothing
// reported.
func TestFailedTerminalStartAfterSetupIsTriedAgain(t *testing.T) {
	h := newSetupSwitchHarness(t, InterfaceChat)
	h.switchTo(t, InterfaceTUI)
	h.failBuilds.Store(1)
	h.supervisor.MarkWorkspaceReady()
	h.waitForAgentStarts(t, 1)
	if reports := h.reported(); len(reports) != 0 {
		t.Fatalf("a start that worked on its second attempt reported %v", reports)
	}
	if got := h.ready.Load(); got != 1 {
		t.Fatalf("agent.ready reported %d times, want 1", got)
	}
}

// A terminal start after Setup that keeps failing is reported, so the
// session shows a startup error instead of a dead terminal. Its Retry starts
// the agent and its task once, and clears the error.
func TestLastingTerminalStartFailureIsReportedAndRetryStartsTheAgentOnce(t *testing.T) {
	h := newSetupSwitchHarness(t, InterfaceChat)
	h.switchTo(t, InterfaceTUI)
	h.failBuilds.Store(agentStartAttempts)
	h.supervisor.MarkWorkspaceReady()
	reports := h.waitForReports(t, 1)
	if reports[0] == nil || !strings.Contains(reports[0].Error(), "credential") {
		t.Fatalf("reported %v, want the credential failure", reports[0])
	}
	if h.agentStarts() != 0 || h.supervisor.controllerReady(InterfaceTUI).Ready || h.ready.Load() != 0 {
		t.Fatal("the terminal agent reads as started after its start failed")
	}
	// Input sent to the dead terminal meanwhile is kept for the agent.
	if err := h.supervisor.writeTerminal(worker.TerminalCommand{
		TerminalID: "00000000-0000-0000-0000-000000000061", Data: []byte("x"),
	}); err != nil {
		t.Fatalf("input while the start failed: %v", err)
	}

	h.retry(t)
	h.waitForAgentStarts(t, 1)
	if reports := h.waitForReports(t, 2); reports[1] != nil {
		t.Fatalf("after the retry reported %v, want the failure cleared", reports[1])
	}
	if got := h.ready.Load(); got != 1 {
		t.Fatalf("agent.ready reported %d times, want 1", got)
	}
	// A second Retry with nothing failed is the workspace's own.
	h.retry(t)
	h.waitForAgentStarts(t, 1)
}

// A switch back to Chat while the terminal start waits for a retry cancels it
// and clears the reported failure: the session's task runs in Chat instead.
func TestSwitchToChatAfterAFailedTerminalStartClearsTheFailure(t *testing.T) {
	h := newSetupSwitchHarness(t, InterfaceChat)
	h.switchTo(t, InterfaceTUI)
	h.failBuilds.Store(agentStartAttempts)
	h.supervisor.MarkWorkspaceReady()
	h.waitForReports(t, 1)
	err := h.supervisor.runOnLoop(context.Background(), func(ctx context.Context) error {
		if err := h.supervisor.stopInterface(ctx, "interrupt"); err != nil {
			return err
		}
		return h.supervisor.startInterface(ctx, interfacePayload{TargetInterface: InterfaceChat})
	})
	if err != nil {
		t.Fatal(err)
	}
	if reports := h.waitForReports(t, 2); reports[1] != nil {
		t.Fatalf("after the switch to Chat reported %v, want the failure cleared", reports[1])
	}
	h.retry(t)
	time.Sleep(200 * time.Millisecond)
	if got := h.agentStarts(); got != 0 {
		t.Fatalf("terminal agent started %d times after the switch to Chat", got)
	}
}

func (h *setupSwitchHarness) inspect(t *testing.T) interfaceInspectResult {
	t.Helper()
	inspection, err := h.supervisor.inspectInterface()
	if err != nil {
		t.Fatal(err)
	}
	return inspection.(interfaceInspectResult)
}

// While the terminal's task waits for a startup that failed (its Setup, or
// the terminal agent's own start) and nothing runs, a drain to Chat cannot
// prove the terminal idle but sees no work: it waits a bounded time and then
// fails, leaving the task in the terminal, instead of waiting for a retry
// that may never come. While Setup runs, the drain waits for the task.
func TestDrainDuringAFailedStartupIsBounded(t *testing.T) {
	t.Run("setup failed", func(t *testing.T) {
		h := newSetupSwitchHarness(t, InterfaceTUI)
		if got := h.inspect(t); got.Idle || got.QuiescenceUnverified {
			t.Fatalf("inspect while Setup runs = %+v, want busy", got)
		}
		h.supervisor.SetWorkspaceStartupFailed(true)
		if got := h.inspect(t); got.Idle || !got.QuiescenceUnverified {
			t.Fatalf("inspect after Setup failed = %+v, want quiescence unverified", got)
		}
		h.supervisor.SetWorkspaceStartupFailed(false) // the retry runs Setup again
		if got := h.inspect(t); got.Idle || got.QuiescenceUnverified {
			t.Fatalf("inspect while the retry runs = %+v, want busy", got)
		}
	})
	t.Run("terminal agent start failed", func(t *testing.T) {
		h := newSetupSwitchHarness(t, InterfaceChat)
		h.switchTo(t, InterfaceTUI)
		h.failBuilds.Store(agentStartAttempts)
		h.supervisor.MarkWorkspaceReady()
		h.waitForReports(t, 1)
		if got := h.inspect(t); got.Idle || !got.QuiescenceUnverified {
			t.Fatalf("inspect after the start failed = %+v, want quiescence unverified", got)
		}
		h.retry(t)
		h.waitForAgentStarts(t, 1)
		if h.supervisor.startupFailureWaiting() {
			t.Fatal("the started agent still reads as a failed startup")
		}
	})
}
