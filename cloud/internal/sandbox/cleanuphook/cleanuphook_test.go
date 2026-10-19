package cleanuphook

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// rootShims puts stand-ins for stat and runuser first on PATH, so a hook
// command made for root runs as an ordinary test user: stat names owner as
// the hook's owner, and runuser prints the user it was asked for and runs
// the command.
func rootShims(t *testing.T, owner string) []string {
	t.Helper()
	bin := t.TempDir()
	shims := map[string]string{
		"stat":    "#!/bin/sh\necho " + shellQuote(owner) + "\n",
		"runuser": "#!/bin/sh\n[ \"$1\" = -u ] && [ \"$3\" = -- ] || { echo bad runuser call: \"$@\"; exit 90; }\necho \"runuser:$2\"\nshift 3\nexec \"$@\"\n",
	}
	for name, content := range shims {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func runHookCommand(t *testing.T, command string, environment []string) string {
	t.Helper()
	run := exec.Command("sh", "-c", command)
	run.Env = environment
	output, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("hook command: %v: %s", err, output)
	}
	return string(output)
}

// Run as root, the hook command runs the hook as its owner through runuser,
// never as root, and reports skipped when there is no hook.
func TestCommandAsRootRunsTheHookAsItsOwner(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	hook := filepath.Join(t.TempDir(), "worker", worker.CleanupHookFile)
	command := Command(HookWord(hook), 10*time.Second, "", Options{Runner: AsRoot})
	if strings.Contains(command, "sudo") || strings.Contains(command, "[ -O") {
		t.Fatalf("a root hook command must reach the owner only through runuser: %s", command)
	}
	if output := runHookCommand(t, command, rootShims(t, "ao-worker")); !strings.Contains(output, Marker+":"+Skipped) {
		t.Fatalf("no hook: %q", output)
	}
	if err := os.MkdirAll(filepath.Dir(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho stopping stack\nexit 4\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	output := runHookCommand(t, command, rootShims(t, "ao-worker"))
	if !strings.Contains(output, "runuser:ao-worker\nstopping stack") || !strings.Contains(output, Marker+":4") {
		t.Fatalf("hook output: %q", output)
	}
	for _, owner := range []string{"root", ""} {
		if output := runHookCommand(t, command, rootShims(t, owner)); !strings.Contains(output, Marker+":"+Skipped) ||
			strings.Contains(output, "stopping stack") {
			t.Fatalf("a hook owned by %q ran: %q", owner, output)
		}
	}
}

// With the project's current Cleanup, the root hook command installs it as
// the hook's owner and the installed hook runs it in the session's workspace.
func TestCommandAsRootRunsTheCurrentCleanup(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	for _, tool := range []string{"base64", "gzip"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is unavailable", tool)
		}
	}
	dataDir, workspace := t.TempDir(), t.TempDir()
	environment := worker.SessionScriptEnvironment{SessionID: "session-1", WorkspaceDir: workspace}
	if err := worker.WriteCleanupHook(dataDir, []string{"echo installed-cleanup"}, workspace, environment); err != nil {
		t.Fatal(err)
	}
	current := []string{`echo "current-cleanup $AO_SESSION_ID"`}
	nonce := worker.NewScriptMarkerNonce()
	payload, fits := CommandsPayload(current, nonce)
	if !fits {
		t.Fatal("a one-line Cleanup does not fit the payload limit")
	}
	command := Command(HookWord(filepath.Join(dataDir, worker.CleanupHookFile)), 10*time.Second, payload, Options{Runner: AsRoot})
	output := runHookCommand(t, command, rootShims(t, "ao-worker"))
	printed, status, ok := Split(output)
	if !ok || status != "0" || strings.Contains(printed, "installed-cleanup") {
		t.Fatalf("hook output = %q", output)
	}
	events := worker.CleanupStepEvents(printed, worker.CleanupRun{RunID: "r", Nonce: nonce, Commands: current})
	if len(events) != 1 || events[0].Status != worker.SetupSucceeded || events[0].Output != "current-cleanup session-1" {
		t.Fatalf("events = %+v", events)
	}
}

func TestSplitReadsTheStatusAfterTheMarker(t *testing.T) {
	t.Parallel()
	printed, status, ok := Split("tearing down\n\n" + Marker + ":3\n")
	if !ok || printed != "tearing down\n\n" || status != "3" || ExitCode(status) != 3 {
		t.Fatalf("split = %q %q %v", printed, status, ok)
	}
	if _, status, ok := Split("x\r\n" + Marker + ":" + Skipped + "\r\n"); !ok || status != Skipped || ExitCode(status) != -1 {
		t.Fatalf("skipped status = %q %v", status, ok)
	}
	if printed, _, ok := Split("cut off"); ok || printed != "cut off" {
		t.Fatalf("no marker = %q %v", printed, ok)
	}
}

func TestNewPlanFollowsTheProjectsCurrentCleanup(t *testing.T) {
	t.Parallel()
	if _, ok := NewPlan(sandbox.WithCleanupCommands(context.Background(), nil)); ok {
		t.Fatal("a project without Cleanup now planned a run")
	}
	plan, ok := NewPlan(context.Background())
	if !ok || plan.Override != "" || plan.Steps.RunID == "" || plan.Steps.Nonce != "" {
		t.Fatalf("installed-commands plan = %+v", plan)
	}
	plan, ok = NewPlan(sandbox.WithCleanupCommands(context.Background(), []string{"make down"}))
	if !ok || plan.Override == "" || plan.Steps.Nonce == "" || len(plan.Steps.Commands) != 1 {
		t.Fatalf("current-Cleanup plan = %+v", plan)
	}
	huge := make([]string, 20)
	for index := range huge {
		var step strings.Builder
		for range 256 {
			step.WriteString(worker.NewScriptMarkerNonce())
		}
		huge[index] = step.String()
	}
	plan, ok = NewPlan(sandbox.WithCleanupCommands(context.Background(), huge))
	if !ok || plan.Override != "" || plan.Steps.Commands != nil {
		t.Fatalf("an oversized Cleanup must fall back to the installed commands: override %d bytes", len(plan.Override))
	}
}

// Tracker runs a key's Cleanup once, Await holds a Delete only while the run
// is going, and a run that never reports stops holding it after its limit.
func TestTrackerRunsOnceAndNeverBlocksForever(t *testing.T) {
	t.Parallel()
	var tracker Tracker
	var calls atomic.Int32
	release := make(chan struct{})
	noSkip := func(reason string) { t.Errorf("skipped: %s", reason) }
	run := tracker.Start("vm-1", "org-1", time.Minute, func() { calls.Add(1); <-release }, noSkip)
	if again := tracker.Start("vm-1", "org-1", time.Minute, func() { calls.Add(1) }, noSkip); again != run {
		t.Fatal("a second Start for the key started another run")
	}
	if run.Await(context.Background(), 20*time.Millisecond) {
		t.Fatal("Await reported a running Cleanup finished")
	}
	close(release)
	if !run.Await(context.Background(), time.Second) || calls.Load() != 1 {
		t.Fatalf("finished run: calls = %d", calls.Load())
	}
	if found, ok := tracker.Lookup("vm-1"); !ok || found != run {
		t.Fatal("a finished run is forgotten before RecordTTL")
	}

	wedged := make(chan struct{})
	defer close(wedged)
	stale := tracker.Start("vm-2", "org-1", -time.Hour, func() { <-wedged }, noSkip)
	if !stale.Await(context.Background(), 10*time.Millisecond) {
		t.Fatal("a run past its limit still holds the deletion")
	}
}

type recorder struct {
	mu     sync.Mutex
	events []worker.ScriptStepEvent
	stages []sandbox.StageReport
}

func (r *recorder) context() context.Context {
	ctx := sandbox.WithStageObserver(context.Background(), func(_ context.Context, report sandbox.StageReport) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.stages = append(r.stages, report)
	})
	return sandbox.WithSessionEventRecorder(ctx, func(_ context.Context, eventType string, payload any) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if event, ok := payload.(worker.ScriptStepEvent); ok && eventType == worker.ScriptStepEventType {
			r.events = append(r.events, event)
		}
	})
}

// Report appends a run's steps (phase cleanup) and reports a failure with
// its output and hint; a run with no hook or no status appends nothing.
func TestReportAppendsStepsAndFinishesTheStage(t *testing.T) {
	t.Parallel()
	report := func(printed, status string, err error, steps worker.CleanupRun) *recorder {
		r := &recorder{}
		ctx := r.context()
		steps.StartedAt = time.Now()
		Report(ctx, sandbox.BeginStage(ctx, Stage), map[string]string{"provider_id": "vm-1"}, steps, printed, status, err, "test")
		return r
	}
	commands := []string{"make down", "docker compose down"}
	failed := report("@@ao-step-00000000000000aa 1 2 start 0 / make down\nstopped\n@@ao-step-00000000000000aa 1 2 end 0 10\n"+
		"@@ao-step-00000000000000aa 2 2 start 0 / docker compose down\nno such service\n"+
		"cleanup step 2 failed: exit status 3\n@@ao-step-00000000000000aa 2 2 end 3 20\n", "3", nil,
		worker.CleanupRun{RunID: "run-1", Nonce: "00000000000000aa", Commands: commands})
	if len(failed.events) != 2 || failed.events[0].Status != worker.SetupSucceeded || failed.events[1].Status != worker.SetupFailed {
		t.Fatalf("events = %+v", failed.events)
	}
	for _, event := range failed.events {
		if event.Phase != worker.ScriptPhaseCleanup || event.RunID != worker.CleanupRunID("00000000000000aa") || !worker.ValidScriptStepEvent(event) {
			t.Fatalf("event = %+v", event)
		}
	}
	if stage := failed.stages[0]; stage.Stage != Stage || stage.Err == nil || !strings.Contains(stage.Err.Error(), "exited 3") ||
		!strings.Contains(stage.Attrs["output"], "no such service") || stage.Attrs["provider_id"] != "vm-1" {
		t.Fatalf("stage = %+v", stage)
	}

	skipped := report("", Skipped, nil, worker.CleanupRun{RunID: "run-2"})
	if len(skipped.events) != 0 || skipped.stages[0].Err != nil || skipped.stages[0].Attrs["result"] != Skipped {
		t.Fatalf("skipped run: events %+v, stage %+v", skipped.events, skipped.stages[0])
	}
	// A run the hook command skipped with a reason says so on every step it
	// knows, whether the current Cleanup or the installed commands.
	for name, steps := range map[string]worker.CleanupRun{
		"current":   {RunID: "run-5", Commands: commands},
		"installed": {RunID: "run-5", KnownCommands: commands},
	} {
		noSudo := report("", Skipped+" "+skipNoSudo, nil, steps)
		if len(noSudo.events) != 2 || noSudo.stages[0].Err != nil || noSudo.stages[0].Attrs["result"] != Skipped ||
			noSudo.stages[0].Attrs["skip_reason"] != skipNoSudo {
			t.Fatalf("%s: skipped run with a reason: events %+v, stage %+v", name, noSudo.events, noSudo.stages[0])
		}
		for index, event := range noSudo.events {
			if event.Status != worker.ScriptStepSkipped || event.Command != commands[index] || event.Phase != worker.ScriptPhaseCleanup ||
				event.Hint != SkippedReason(skipNoSudo) || !strings.Contains(event.Hint, "sudo") || !worker.ValidScriptStepEvent(event) {
				t.Fatalf("%s: skipped step %d = %+v", name, index+1, event)
			}
		}
	}
	lost := report("partial", "", errors.New(`freestyle api returned 400: "timeout_ms cannot exceed 300000" token=abc123`),
		worker.CleanupRun{RunID: "run-3", Commands: commands})
	if lost.stages[0].Err == nil || !strings.Contains(lost.stages[0].Err.Error(), "did not report") || len(lost.events) != 2 {
		t.Fatalf("lost run: events %+v, stage %+v", lost.events, lost.stages[0])
	}
	for index, event := range lost.events {
		if event.Status != worker.ScriptStepSkipped || event.Command != commands[index] || event.RunID != "run-3" ||
			!strings.Contains(event.Hint, "could not run") || !strings.Contains(event.Hint, "timeout_ms cannot exceed 300000") ||
			strings.Contains(event.Hint, "abc123") || !worker.ValidScriptStepEvent(event) {
			t.Fatalf("lost run step %d = %+v", index+1, event)
		}
	}
	// Output that came back in part reports the steps it shows.
	cut := report("@@ao-step-00000000000000aa 1 2 start 0 / make down\nstopped\n@@ao-step-00000000000000aa 1 2 end 0 10\n"+
		"@@ao-step-00000000000000aa 2 2 start 0 / docker compose down\nstopp", "", errors.New("connection reset"),
		worker.CleanupRun{RunID: "run-4", Nonce: "00000000000000aa", Commands: commands})
	if len(cut.events) != 2 || cut.events[0].Status != worker.SetupSucceeded || cut.events[1].Status != worker.SetupFailed {
		t.Fatalf("cut run: events %+v", cut.events)
	}
}

// ReportNotRun reports each of the current Cleanup's steps as skipped, with
// the reason, and the stage as skipped and failed.
func TestReportNotRunSkipsEveryStep(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	ctx := r.context()
	steps := worker.CleanupRun{RunID: "run-1", Commands: []string{"make down", domain.RepoScriptCommand("api", "npm run clean")}}
	ReportNotRun(ctx, sandbox.BeginStage(ctx, Stage), map[string]string{}, steps, "The sandbox did not resume.", errors.New("resume failed"))
	if len(r.events) != 2 {
		t.Fatalf("events = %+v", r.events)
	}
	for index, event := range r.events {
		if !worker.ValidScriptStepEvent(event) || event.Status != worker.ScriptStepSkipped || event.Phase != worker.ScriptPhaseCleanup ||
			event.Step != index+1 || event.Total != 2 || event.Hint != "The sandbox did not resume." {
			t.Fatalf("event %d = %+v", index, event)
		}
	}
	if r.events[1].Repository != "api" || r.events[1].Command != "npm run clean" {
		t.Fatalf("repository step = %+v", r.events[1])
	}
	if stage := r.stages[0]; stage.Err == nil || stage.Attrs["result"] != Skipped {
		t.Fatalf("stage = %+v", stage)
	}
}

// A failed run's stage output and its step events are redacted alike.
func TestReportRedactsTheStageOutputLikeTheSteps(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	ctx := r.context()
	printed := "@@ao-step-00000000000000aa 1 1 start 0 / make down\nAPI_KEY: abc123\n{\\\"password\\\":\\\"hunter2\\\"}\n" +
		"cleanup step 1 failed: exit status 3\n@@ao-step-00000000000000aa 1 1 end 3 20\n"
	Report(ctx, sandbox.BeginStage(ctx, Stage), map[string]string{}, worker.CleanupRun{
		RunID: "run-1", Nonce: "00000000000000aa", StartedAt: time.Now(),
	}, printed, "3", nil, "test")
	stage := r.stages[0].Attrs["output"]
	if len(r.events) != 1 || strings.Contains(r.events[0].Output, "abc123") || strings.Contains(r.events[0].Output, "hunter2") ||
		strings.Contains(stage, "abc123") || strings.Contains(stage, "hunter2") || !strings.Contains(stage, "[REDACTED]") {
		t.Fatalf("stage output %q, events %+v", stage, r.events)
	}
}

// At most MaxRuns runs go at once for the provider and MaxOrgRuns for one
// organization; the rest wait for a slot, and one that waits past QueueWait
// is skipped with QueueFullReason. A waiting run holds its Delete.
func TestTrackerBoundsConcurrentRuns(t *testing.T) {
	t.Parallel()
	tracker := Tracker{MaxRuns: 3, MaxOrgRuns: 2, QueueWait: time.Hour}
	var mu sync.Mutex
	running, peak, peakOrg := map[string]int{}, 0, 0
	release := make(chan struct{})
	started := make(chan string, 10)
	start := func(key, org string) *Run {
		return tracker.Start(key, org, time.Minute, func() {
			mu.Lock()
			running[org]++
			total := 0
			for _, count := range running {
				total += count
			}
			peak, peakOrg = max(peak, total), max(peakOrg, running[org])
			mu.Unlock()
			started <- key
			<-release
			mu.Lock()
			running[org]--
			mu.Unlock()
		}, func(reason string) { t.Errorf("%s skipped: %s", key, reason) })
	}
	runs := []*Run{start("a1", "a"), start("a2", "a"), start("a3", "a"), start("b1", "b"), start("c1", "c")}
	seen := map[string]bool{}
	for range 3 {
		select {
		case key := <-started:
			seen[key] = true
		case <-time.After(5 * time.Second):
			t.Fatal("the first runs did not start")
		}
	}
	select {
	case key := <-started:
		t.Fatalf("%s started beyond the provider's 3 slots", key)
	case <-time.After(100 * time.Millisecond):
	}
	if seen["a3"] && seen["a1"] && seen["a2"] {
		t.Fatal("three runs of one organization went at once")
	}
	if runs[2].Await(context.Background(), 10*time.Millisecond) && !seen["a3"] {
		t.Fatal("a waiting run let its Delete go ahead")
	}
	close(release)
	for _, run := range runs {
		if !run.Await(context.Background(), 5*time.Second) {
			t.Fatal("a queued run never ran")
		}
	}
	if peak != 3 || peakOrg != 2 {
		t.Fatalf("peak runs = %d (want 3), peak for one organization = %d (want 2)", peak, peakOrg)
	}

	full := Tracker{MaxRuns: 1, QueueWait: 50 * time.Millisecond}
	hold := make(chan struct{})
	defer close(hold)
	holding := make(chan struct{})
	full.Start("x1", "", time.Minute, func() { close(holding); <-hold }, func(string) {})
	<-holding
	skipped := make(chan string, 1)
	queued := full.Start("x2", "", time.Minute, func() { t.Error("x2 ran without a slot") }, func(reason string) { skipped <- reason })
	select {
	case reason := <-skipped:
		if reason != QueueFullReason || !queued.Await(context.Background(), time.Second) {
			t.Fatalf("skip reason %q", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a run that waited past QueueWait was not skipped")
	}
}

// Skipper reports a run that never ran: each step skipped with the reason.
func TestSkipperReportsTheRunAsNotRun(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	Skipper(r.context(), map[string]string{"provider_id": "vm-1"}, worker.CleanupRun{RunID: "run-1", Commands: []string{"make down"}})(QueueFullReason)
	if len(r.events) != 1 || r.events[0].Status != worker.ScriptStepSkipped || r.events[0].Hint != QueueFullReason ||
		r.stages[0].Attrs["result"] != Skipped || r.stages[0].Err == nil {
		t.Fatalf("events %+v, stages %+v", r.events, r.stages)
	}
}

// Freestyle and Docker stop the hook owner's processes (the worker and its
// agent) before the hook runs; Coder, whose login user may run the Coder
// agent carrying the command, stops nothing.
func TestCommandStopsTheOwnersProcessesFirst(t *testing.T) {
	t.Parallel()
	for _, runner := range []Runner{AsRoot, AsLoginUser} {
		stopping := Command(HookWord("/workspace/.ao/worker/hook"), time.Minute, "", Options{Runner: runner, Stop: StopOwnerProcesses})
		stop, run := strings.Index(stopping, `ao_stop "$ao_who" TERM`), strings.Index(stopping, `ao_out=$(`)
		if stop < 0 || run < stop || !strings.Contains(stopping, `ao_stop "$ao_who" KILL`) ||
			!strings.Contains(stopping, `1|"$$") continue`) || !strings.Contains(stopping, "ao_who=${ao_owner:-$(id -un)}") {
			t.Fatalf("runner %d: the owner's processes must be stopped, sparing PID 1 and the command, before the hook runs:\n%s", runner, stopping)
		}
		if keeping := Command(HookWord("/workspace/.ao/worker/hook"), time.Minute, "", Options{Runner: runner}); strings.Contains(keeping, "ao_stop") {
			t.Fatalf("runner %d: KeepOwnerProcesses stops processes:\n%s", runner, keeping)
		}
	}
}

// The stop sends SIGTERM, waits, then SIGKILLs a process that ignores it, and
// touches only the owner's processes. Run where /proc lists processes, with id
// and stat shimmed so the owner's are only the two children started here.
func TestCommandStopEndsOnlyTheOwnersProcesses(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc to list processes")
	}
	polite := exec.Command("sleep", "60")
	stubborn := exec.Command("sh", "-c", `trap "" TERM; while :; do sleep 0.2; done`)
	bystander := exec.Command("sleep", "60")
	for _, process := range []*exec.Cmd{polite, stubborn, bystander} {
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = bystander.Process.Kill(); _ = bystander.Wait() })
	exited := func(process *exec.Cmd) chan struct{} {
		done := make(chan struct{})
		go func() { _ = process.Wait(); close(done) }()
		return done
	}
	politeDone, stubbornDone := exited(polite), exited(stubborn)
	bin := t.TempDir()
	owned := "/proc/" + strconv.Itoa(polite.Process.Pid) + "|/proc/" + strconv.Itoa(stubborn.Process.Pid)
	shims := map[string]string{
		"id":   "#!/bin/sh\ncase \"$1\" in -u) echo 4242 ;; -un) echo owner ;; *) exit 1 ;; esac\n",
		"stat": "#!/bin/sh\ncase \"$3\" in " + owned + ") echo 4242 ;; *) echo 0 ;; esac\n",
	}
	for name, content := range shims {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hook := filepath.Join(t.TempDir(), worker.CleanupHookFile)
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho hook ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	output := runHookCommand(t, Command(HookWord(hook), 10*time.Second, "", Options{Runner: AsLoginUser, Stop: StopOwnerProcesses}),
		append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH")))
	if !strings.Contains(output, "hook ran") || !strings.Contains(output, Marker+":0") {
		t.Fatalf("output = %q", output)
	}
	for name, done := range map[string]chan struct{}{"polite": politeDone, "stubborn": stubbornDone} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("the %s owner process is still running", name)
		}
	}
	if elapsed := time.Since(started); elapsed < OwnerStopWait-time.Second {
		t.Fatalf("the stubborn process was killed after %s, before its SIGTERM wait", elapsed)
	}
	if err := bystander.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("a process the owner does not own was signalled: %v", err)
	}
}

// Run as root with a pinned owner, the hook runs as that account whoever owns
// the file (an agent could point the hook at another account's program), and
// is skipped when the account does not exist.
func TestCommandAsRootRunsTheHookAsThePinnedOwner(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	hook := filepath.Join(t.TempDir(), worker.CleanupHookFile)
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho hook ran\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	command := Command(HookWord(hook), 10*time.Second, "", Options{Runner: AsRoot, Owner: "ao-worker"})
	if strings.Contains(command, "stat -c %U") {
		t.Fatalf("a pinned owner must not be read from the hook's file:\n%s", command)
	}
	// stat would name another account; id knows ao-worker.
	environment := rootShims(t, "postgres")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "id"), []byte("#!/bin/sh\ncase \"$1\" in -un) echo root ;; -u) [ \"$2\" = ao-worker ] && echo 1000 ;; esac\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for index, entry := range environment {
		if strings.HasPrefix(entry, "PATH=") {
			environment[index] = "PATH=" + bin + string(os.PathListSeparator) + strings.TrimPrefix(entry, "PATH=")
		}
	}
	if output := runHookCommand(t, command, environment); !strings.Contains(output, "runuser:ao-worker") || !strings.Contains(output, "hook ran") {
		t.Fatalf("output = %q", output)
	}
	missing := Command(HookWord(hook), 10*time.Second, "", Options{Runner: AsRoot, Owner: "no-such-user"})
	if output := runHookCommand(t, missing, environment); !strings.Contains(output, Marker+":"+Skipped) {
		t.Fatalf("missing account: output = %q", output)
	}
}

// The project's Cleanup changed after the session started, and the hook ran
// the commands the worker was given at launch (the current ones could not be
// passed in, or could not be read). One of those old commands sets a secret
// and prints it with nothing naming it: with the launch-time commands stored
// on the sandbox (domain.InstalledCleanupKey, carried by the reconciler as
// sandbox.WithInstalledCleanupCommands), the reported steps and the stage's
// output mask it.
func TestCleanupOfCommandsGivenAtLaunchMasksTheirSecrets(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	launched := []string{
		`export DEPLOY_TOKEN=FAKEVAL98x; printf 'token %s\n' "$DEPLOY_TOKEN"`,
		`printf '%s\n' "$DEPLOY_TOKEN"; exit 4`,
	}
	huge := make([]string, 20)
	for index := range huge {
		var step strings.Builder
		step.WriteString("echo ")
		for range 256 {
			step.WriteString(worker.NewScriptMarkerNonce())
		}
		huge[index] = step.String()
	}
	for name, ctx := range map[string]context.Context{
		// The project's current Cleanup is too large to pass to the sandbox.
		"current too large": sandbox.WithCleanupCommands(context.Background(), huge),
		// It could not be read.
		"current unknown": context.Background(),
	} {
		dataDir, workspace := t.TempDir(), t.TempDir()
		environment := worker.SessionScriptEnvironment{SessionID: "session-1", WorkspaceDir: workspace}
		if err := worker.WriteCleanupHook(dataDir, launched, workspace, environment); err != nil {
			t.Fatal(err)
		}
		stored, err := json.Marshal(map[string]any{domain.InstalledCleanupKey: launched, "providerFailureId": "build-1"})
		if err != nil {
			t.Fatal(err)
		}
		installed, ok := domain.InstalledCleanupCommands(stored)
		if !ok {
			t.Fatal("the stored commands were not read back")
		}
		plan, ok := NewPlan(sandbox.WithInstalledCleanupCommands(ctx, installed))
		if !ok || plan.Override != "" || strings.Join(plan.Steps.KnownCommands, "\n") != strings.Join(launched, "\n") {
			t.Fatalf("%s: plan = %+v", name, plan)
		}
		output := runHookCommand(t, Command(HookWord(filepath.Join(dataDir, worker.CleanupHookFile)), 10*time.Second, plan.Override, Options{}), os.Environ())
		printed, status, ok := Split(output)
		if !ok || status != "4" || !strings.Contains(printed, "FAKEVAL98x") {
			t.Fatalf("%s: the launch-time commands did not run: %q", name, output)
		}
		r := &recorder{}
		reportCtx := r.context()
		steps := plan.Steps
		steps.StartedAt = time.Now()
		Report(reportCtx, sandbox.BeginStage(reportCtx, Stage), map[string]string{}, steps, printed, status, nil, "test")
		if len(r.events) != 2 || r.events[1].Status != worker.SetupFailed {
			t.Fatalf("%s: events = %+v", name, r.events)
		}
		published := ""
		for _, event := range r.events {
			published += event.Command + "\n" + event.Output + "\n" + worker.SanitizeScriptStepEvent(event).Output + "\n"
		}
		for _, stage := range r.stages {
			published += stage.Attrs["output"] + "\n"
		}
		if strings.Contains(published, "FAKEVAL98x") || !strings.Contains(published, "token [REDACTED]") {
			t.Fatalf("%s: published %q", name, published)
		}
	}

	// Without the stored commands, the current Cleanup is the best guess
	// left; one the control plane installs for the run counts too.
	plan, _ := NewPlan(sandbox.WithCleanupCommands(context.Background(), []string{"make down"}))
	if strings.Join(plan.Steps.KnownCommands, "|") != "make down" || strings.Join(plan.Steps.Commands, "|") != "make down" {
		t.Fatalf("current-only plan = %+v", plan.Steps)
	}
	plan, _ = NewPlan(sandbox.WithInstalledCleanupCommands(sandbox.WithCleanupCommands(context.Background(), []string{"make down"}), launched))
	if plan.Override == "" || strings.Join(plan.Steps.Commands, "|") != "make down" || len(plan.Steps.KnownCommands) != 2 {
		t.Fatalf("current and stored plan = %+v", plan.Steps)
	}
}

// loginShims puts stand-ins for sudo and stat first on PATH. sudo needs -n,
// fails outright with noSudo, and runs the command as the test user, saying on
// stderr whom it was asked to run as. Asked to run test(1) as root, it fails,
// as uutils coreutils (Ubuntu 25.10 and later) does for a file only its owner
// may run. stat names owner as the owner of any file that exists.
func loginShims(t *testing.T, owner string, noSudo bool) []string {
	t.Helper()
	bin := t.TempDir()
	sudo := "#!/bin/sh\n[ \"$1\" = -n ] || { echo sudo without -n >&2; exit 91; }\nshift\n"
	if noSudo {
		sudo += "echo 'sudo: a password is required' >&2\nexit 1\n"
	}
	sudo += "user=root\nwhile :; do case \"$1\" in -H) shift ;; -u) user=$2; shift 2 ;; *) break ;; esac; done\n" +
		"if [ \"$user\" = root ] && [ \"$1\" = test ]; then exit 1; fi\n" +
		"[ \"$user\" = root ] || echo \"sudo-as:$user\" >&2\n" +
		"exec \"$@\"\n"
	shims := map[string]string{
		"sudo": sudo,
		"stat": "#!/bin/sh\n[ \"$1\" = -c ] && [ \"$2\" = %U ] || exit 92\n[ -e \"$3\" ] || exit 1\necho " + shellQuote(owner) + "\n",
	}
	for name, content := range shims {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Run as a login user that does not own the hook (Coder's agent terminal, the
// worker running as ao-worker), the hook command runs the hook as its owner
// through sudo, asking whether it can run as that owner rather than as root:
// prod's Ubuntu 26.04 Coder image skipped every Cleanup on a root test -x.
// Each reason it cannot run is reported after Skipped.
func TestCommandAsLoginUserRunsAnotherUsersHookAsItsOwner(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}
	if os.Geteuid() == 0 {
		t.Skip("needs a hook the test user does not own")
	}
	// A root-owned program stands in for the worker's hook.
	hook, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true is unavailable")
	}
	if hook, err = filepath.EvalSymlinks(hook); err != nil {
		t.Fatal(err)
	}
	command := Command(HookWord(hook), 10*time.Second, "", Options{Runner: AsLoginUser})
	if strings.Contains(command, "sudo -n test") {
		t.Fatalf("the hook command checks the hook as root:\n%s", command)
	}
	output := runHookCommand(t, command, loginShims(t, "ao-worker", false))
	if !strings.Contains(output, "sudo-as:ao-worker") || !strings.Contains(output, Marker+":0") {
		t.Fatalf("hook output: %q", output)
	}
	for name, test := range map[string]struct {
		hook, owner string
		noSudo      bool
		reason      string
	}{
		"no hook":      {hook: filepath.Join(t.TempDir(), worker.CleanupHookFile), owner: "ao-worker", reason: skipNoHook},
		"not runnable": {hook: "/etc/hosts", owner: "ao-worker", reason: skipNotRunnable},
		"root's hook":  {hook: hook, owner: "root", reason: skipOwner},
		"no account":   {hook: hook, owner: "UNKNOWN", reason: skipOwner},
		"no sudo":      {hook: hook, owner: "ao-worker", noSudo: true, reason: skipNoSudo},
	} {
		if _, err := os.Stat(test.hook); name == "not runnable" && err != nil {
			continue
		}
		command := Command(HookWord(test.hook), 10*time.Second, "", Options{Runner: AsLoginUser})
		output := runHookCommand(t, command, loginShims(t, test.owner, test.noSudo))
		_, status, ok := Split(output)
		if reason, skipped := SkipReason(status); !ok || !skipped || reason != test.reason || strings.Contains(output, "sudo-as:") {
			t.Fatalf("%s: output = %q, want skipped %s without running the hook", name, output, test.reason)
		}
	}
}

// SkipReason reads the reason after Skipped, and none from an exit status.
func TestSkipReasonReadsTheHookCommandsStatus(t *testing.T) {
	t.Parallel()
	for status, want := range map[string][2]string{
		Skipped:                    {"", "skipped"},
		Skipped + " " + skipNoHook: {skipNoHook, "skipped"},
		" skipped no_sudo \r":      {skipNoSudo, "skipped"},
		"0":                        {"", ""},
		"":                         {"", ""},
		"skippedx":                 {"", ""},
	} {
		reason, skipped := SkipReason(status)
		if reason != want[0] || skipped != (want[1] == "skipped") {
			t.Fatalf("SkipReason(%q) = %q, %v", status, reason, skipped)
		}
	}
	if SkippedReason("bogus") == "" || SkippedReason(skipNoHook) == SkippedReason("bogus") {
		t.Fatal("every reason needs words for the user")
	}
}

// SkipOnce reports a Cleanup that cannot run because the sandbox is not
// running: each step skipped with NotRunningReason, the stage as skipped and
// not failed, and only once for the sandbox.
func TestSkipOnceReportsANotRunningSandboxOnce(t *testing.T) {
	t.Parallel()
	r := &recorder{}
	ctx := r.context()
	var tracker Tracker
	steps := worker.CleanupRun{RunID: "run-1", Commands: []string{"make down", "docker compose down"}}
	for range 2 {
		tracker.SkipOnce(ctx, "vm-1", map[string]string{"provider_id": "vm-1"}, steps)
	}
	if len(r.events) != 2 || len(r.stages) != 1 {
		t.Fatalf("events = %+v, stages = %+v", r.events, r.stages)
	}
	for _, event := range r.events {
		if event.Status != worker.ScriptStepSkipped || event.Hint != NotRunningReason || !worker.ValidScriptStepEvent(event) {
			t.Fatalf("event = %+v", event)
		}
	}
	if stage := r.stages[0]; stage.Err != nil || stage.Attrs["result"] != Skipped {
		t.Fatalf("stage = %+v", stage)
	}
	if run, ok := tracker.Lookup("vm-1"); !ok || !run.Await(ctx, time.Millisecond) {
		t.Fatal("a re-issued Delete must find the finished record and proceed")
	}
}
