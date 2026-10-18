// Package cleanuphook runs a session's project Cleanup before a provider
// deletes the session's sandbox, and reports it. The worker installs the
// Cleanup hook in its data directory (worker.WriteCleanupHook) when the
// session starts; the worker is gone by the time the sandbox is deleted, so
// the control plane runs that hook remotely, as its owner, with a time budget,
// and reports the outcome as the project_cleanup stage and each step as a
// workspace.script.step event (phase cleanup) on the session.
//
// Every provider shares the hook command, the run bookkeeping and the
// reporting here; each brings only how it runs a shell command in its sandbox
// (Coder's agent terminal, a Freestyle exec, a Docker exec).
package cleanuphook

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/activity"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/google/uuid"
)

const (
	// Stage is the project's Cleanup commands, run before AO deletes the
	// session's sandbox.
	Stage = "project_cleanup"
	// Marker precedes the hook command's last line, which carries the hook's
	// exit status or Skipped.
	Marker = "__AO_PROJECT_CLEANUP__"
	// Skipped is what the hook command reports when the worker never
	// installed a hook (the worker never started), or the hook may not run.
	Skipped = "skipped"
	// Budget bounds the whole Cleanup run, so the sandbox is still deleted
	// well within the reconciler's deletion deadline. Each command also has
	// its own 10-minute limit inside the hook.
	Budget = 10 * time.Minute
	// Margin is extra time AO waits beyond Budget for the command to start
	// and the result to arrive.
	Margin = 20 * time.Second
	// RecordTTL is how long a finished run is remembered, so a Delete the
	// reconciler re-issues while the sandbox winds down never runs the
	// commands a second time.
	RecordTTL = time.Hour
	// InlineWait is how long one Delete waits for a Cleanup run before
	// handing the sandbox back to the reconciler. Most Cleanup commands
	// finish within it, so the sandbox is deleted in the same pass and a
	// later tick is only needed for slow ones.
	InlineWait = 5 * time.Second
	// StaleAfter is added to a run's limit before a run that never reported
	// is treated as finished, so a lost or wedged run can never hold a
	// deletion indefinitely.
	StaleAfter = 30 * time.Second
	// PayloadLimit bounds the encoded Cleanup commands passed in the hook
	// command; larger Cleanup falls back to the commands the worker
	// installed. Coder's terminal carries the command in its request URL.
	PayloadLimit = 12 << 10
	// OutputLimit bounds the Cleanup output the hook command prints and AO
	// keeps: the hook's own tail (worker.CleanupHookOutputLimit) and its last
	// lines, split into the run's step events.
	OutputLimit = worker.CleanupHookOutputLimit + 1<<10
	// stageOutputLimit bounds the output excerpt on a failed run's stage.
	stageOutputLimit = 2048
)

// WorkspaceHook is the Cleanup hook's path in the /workspace layout the
// reconciler gives every provider but Coder (AO_DATA_DIR
// /workspace/.ao/worker).
const WorkspaceHook = "/workspace/.ao/worker/" + worker.CleanupHookFile

// Runner is how the hook command reaches the hook's owner, the user the
// worker ran as.
type Runner int

const (
	// AsLoginUser runs the hook command as an ordinary login user: a hook
	// that user owns runs directly, any other hook only as its owner through
	// sudo, never as root or as the login user (Coder's agent terminal; a
	// Docker exec as the container's user, where only its own hook runs).
	AsLoginUser Runner = iota
	// AsRoot runs the hook command as root, which runs the hook as its owner
	// through runuser and never as root (a Freestyle exec).
	AsRoot
)

// Stop says whether the hook command first stops the hook owner's processes.
type Stop int

const (
	// KeepOwnerProcesses runs the hook beside whatever the owner runs
	// (Coder: the login user may also run the Coder agent that carries the
	// hook command, so nothing is stopped there).
	KeepOwnerProcesses Stop = iota
	// StopOwnerProcesses ends every process of the hook's owner (the AO
	// worker, its agent and whatever they started) before the hook runs as
	// a fresh process, so neither acts during Cleanup: a Freestyle VM
	// resumed for its Cleanup resumes them too, and a Docker container runs
	// them until it is removed. Each gets SIGTERM, up to OwnerStopWait to
	// exit, then SIGKILL. PID 1 is spared: in a Docker container it is the
	// worker itself, and ending it would stop the container and the Cleanup
	// with it (with its agent gone and its session deleted it has nothing to
	// act on); in a VM it is root's init. Containers the Cleanup stops are
	// root's (dockerd), so they are untouched; a dev server the agent started
	// is stopped, which is what a Cleanup that stops it wants.
	StopOwnerProcesses
)

// OwnerStopWait is how long the owner's processes have to exit after SIGTERM.
const OwnerStopWait = 5 * time.Second

// stopOwnerScript defines ao_stop USER SIGNAL, which signals every process of
// USER but PID 1 and this shell and reports whether any was signalled. It
// reads /proc rather than using pkill, which slim images lack; signalling 0
// only checks a process is still there.
const stopOwnerScript = "ao_stop() {\n" +
	"  ao_uid=$(id -u \"$1\" 2>/dev/null) || return 1\n" +
	"  ao_found=1\n" +
	"  for ao_pid in /proc/[0-9]*; do\n" +
	"    ao_pid=${ao_pid#/proc/}\n" +
	"    case \"$ao_pid\" in 1|\"$$\") continue ;; esac\n" +
	"    [ \"$(stat -c %u \"/proc/$ao_pid\" 2>/dev/null)\" = \"$ao_uid\" ] || continue\n" +
	"    kill -\"$2\" \"$ao_pid\" 2>/dev/null && ao_found=0\n" +
	"  done\n" +
	"  return $ao_found\n" +
	"}\n"

// Options says how Command reaches the hook's owner.
type Options struct {
	Runner Runner
	Stop   Stop
	// Owner, with AsRoot, is the account the hook runs as: the user the
	// worker was provisioned to run as. Without it the hook's file owner is
	// used (never root).
	Owner string
}

// Command is the shell command that runs the worker-installed Cleanup hook
// (hook is a shell word naming it) as the user that owns it, under timeout(1)
// when the image has it, then prints the tail of its output and, after
// Marker, its exit status (or Skipped). With override (a CommandsPayload), it
// first replaces the commands script beside the hook with the project's
// current Cleanup, as that user, so the hook keeps its working directory,
// session environment and run-once lock; if that fails the installed
// commands run. With StopOwnerProcesses it ends the owner's processes first.
func Command(hook string, budget time.Duration, override string, options Options) string {
	seconds := strconv.Itoa(max(1, int(budget/time.Second)))
	command := "ao_hook=" + hook + "\n" +
		"ao_skip() { printf '\\n%s:%s\\n' " + Marker + " " + Skipped + "; exit 0; }\n" +
		"ao_owner=\n"
	asOwner := `sudo -n -H -u "$ao_owner"`
	switch {
	case options.Runner == AsRoot && options.Owner != "":
		// The hook runs as the account the worker was provisioned with,
		// whoever owns the file: an agent that swaps the hook for a link to
		// another account's program cannot borrow that account.
		asOwner = `runuser -u "$ao_owner" --`
		command += "if [ -f \"$ao_hook\" ] && [ -x \"$ao_hook\" ] && id -u " + shellQuote(options.Owner) + " >/dev/null 2>&1; then\n" +
			"  ao_owner=" + shellQuote(options.Owner) + "\n" +
			"  case \"$ao_owner\" in root|\"$(id -un)\") ao_skip ;; esac\n" +
			"  set -- " + asOwner + " \"$ao_hook\"\n" +
			"else ao_skip; fi\n"
	case options.Runner == AsRoot:
		asOwner = `runuser -u "$ao_owner" --`
		command += "if [ -f \"$ao_hook\" ] && [ -x \"$ao_hook\" ]; then\n" +
			"  ao_owner=$(stat -c %U \"$ao_hook\" 2>/dev/null) || ao_skip\n" +
			"  case \"$ao_owner\" in ''|root|\"$(id -un)\") ao_skip ;; esac\n" +
			"  set -- " + asOwner + " \"$ao_hook\"\n" +
			"else ao_skip; fi\n"
	default:
		// A hook the login user owns (the worker ran as it) runs directly;
		// any other hook runs only as its owner through sudo, never as root
		// or as the login user, so an agent that rewrites or chmods its hook
		// cannot borrow the login user's (sudo-capable) account.
		command += "if [ -O \"$ao_hook\" ] && [ -x \"$ao_hook\" ]; then set -- \"$ao_hook\"\n" +
			"elif sudo -n test -x \"$ao_hook\" 2>/dev/null; then\n" +
			"  ao_owner=$(sudo -n stat -c %U \"$ao_hook\" 2>/dev/null) || ao_skip\n" +
			"  case \"$ao_owner\" in ''|root|\"$(id -un)\") ao_skip ;; esac\n" +
			"  set -- " + asOwner + " \"$ao_hook\"\n" +
			"else ao_skip; fi\n"
	}
	if override != "" {
		write := shellQuote(`cat >"$1.tmp" && chmod 600 "$1.tmp" && mv "$1.tmp" "$1"`)
		command += "ao_commands=$(dirname \"$ao_hook\")/" + worker.CleanupCommandsFile + "\n" +
			"ao_payload=" + shellQuote(override) + "\n" +
			"if [ -n \"$ao_owner\" ]; then\n" +
			"  printf %s \"$ao_payload\" | base64 -d | gzip -dc | " + asOwner + " sh -c " + write + " _ \"$ao_commands\"\n" +
			"else\n" +
			"  printf %s \"$ao_payload\" | base64 -d | gzip -dc | sh -c " + write + " _ \"$ao_commands\"\n" +
			"fi || echo \"AO: could not install the project's current Cleanup; running the commands from the session's start\" >&2\n"
	}
	if options.Stop == StopOwnerProcesses {
		polls := strconv.Itoa(int(OwnerStopWait / (500 * time.Millisecond)))
		command += stopOwnerScript +
			// A hook the login user owns runs as that user (ao_owner unset).
			"ao_who=${ao_owner:-$(id -un)}\n" +
			"if ao_stop \"$ao_who\" TERM; then\n" +
			"  ao_wait=0\n" +
			"  while [ \"$ao_wait\" -lt " + polls + " ] && ao_stop \"$ao_who\" 0; do sleep 0.5; ao_wait=$((ao_wait + 1)); done\n" +
			"  ao_stop \"$ao_who\" KILL\n" +
			"fi\n"
	}
	return command +
		"if command -v timeout >/dev/null 2>&1; then\n" +
		"  ao_out=$(timeout -k 5 " + seconds + " \"$@\" 2>&1 </dev/null)\n" +
		"else\n" +
		"  ao_out=$(\"$@\" 2>&1 </dev/null)\n" +
		"fi\n" +
		"ao_code=$?\n" +
		"printf '%s\\n' \"$ao_out\" | tail -c " + strconv.Itoa(OutputLimit) + "\n" +
		"printf '\\n%s:%s\\n' " + Marker + " \"$ao_code\"\n"
}

// HookWord is the shell word for a hook path, quoted.
func HookWord(hook string) string {
	return shellQuote(path.Clean(hook))
}

// CommandsPayload is the gzip-compressed, base64-encoded Cleanup commands
// script for the project's current Cleanup, its step markers carrying nonce,
// and whether it fits PayloadLimit.
func CommandsPayload(commands []string, nonce string) (string, bool) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(worker.CleanupCommandsScript(commands, nonce))); err != nil {
		return "", false
	}
	if err := writer.Close(); err != nil {
		return "", false
	}
	payload := base64.StdEncoding.EncodeToString(compressed.Bytes())
	return payload, len(payload) <= PayloadLimit
}

// Plan is one Cleanup run's hook command and the run its steps are read as.
type Plan struct {
	// Override is the project's current Cleanup for Command, or "" to run
	// the commands the worker installed.
	Override string
	// Steps names the run and, with an override, its marker nonce and
	// commands.
	Steps worker.CleanupRun
}

// NewPlan plans a Cleanup run from the project's current Cleanup when the
// caller read it (sandbox.WithCleanupCommands). It reports false when nothing
// is to run: the project has no Cleanup now. A current Cleanup too large to
// pass is logged and the commands the worker installed run instead.
func NewPlan(ctx context.Context, logArgs ...any) (Plan, bool) {
	current, known := sandbox.CleanupCommands(ctx)
	if known && len(current) == 0 {
		return Plan{}, false
	}
	// The current Cleanup's commands script marks its steps with a nonce
	// made for this run, so its step events are read only from those markers.
	plan := Plan{Steps: worker.CleanupRun{RunID: uuid.NewString()}}
	if known {
		nonce := worker.NewScriptMarkerNonce()
		if payload, fits := CommandsPayload(current, nonce); fits {
			plan.Override, plan.Steps.Nonce, plan.Steps.Commands = payload, nonce, current
			plan.Steps.RunID = worker.CleanupRunID(nonce)
		} else {
			slog.WarnContext(ctx, "the project's current Cleanup is too large to pass to the sandbox; running the commands from the session's start",
				append(logArgs, "limit_bytes", PayloadLimit)...)
		}
	}
	return plan, true
}

// Split splits a hook command's output into what the hook printed and the
// status after Marker; ok is false when the marker never came.
func Split(output string) (printed, status string, ok bool) {
	index := strings.LastIndex(output, Marker+":")
	if index < 0 {
		return output, "", false
	}
	status, _, _ = strings.Cut(output[index+len(Marker)+1:], "\n")
	return output[:index], strings.TrimSpace(strings.TrimRight(status, "\r")), true
}

// ExitCode is a reported status as an exit code; -1 when it is not a number.
func ExitCode(status string) int {
	code, err := strconv.Atoi(strings.TrimSpace(status))
	if err != nil {
		return -1
	}
	return code
}

// Report reports a finished hook command. printed is what the hook printed
// before the status (at least its last OutputLimit bytes), status the status
// after Marker, and err why no status arrived. It appends the run's step
// events to the session (sandbox.RecordSessionEvent) and finishes the stage
// with attrs: a failed run with its output excerpt and hint, a run with no
// hook as skipped, and a run that never reported as an error. A run that
// never reported still appends its steps: those its partial output shows, or
// else each current step as skipped with the provider's error
// (ExecFailedReason), so the session shows why Cleanup did not run. Nothing
// it reports fails the deletion. name prefixes the stage's error.
func Report(
	ctx context.Context,
	finish func(error, map[string]string),
	attrs map[string]string,
	steps worker.CleanupRun,
	printed, status string,
	err error,
	name string,
) {
	exitCode := ExitCode(status)
	switch {
	case err == nil && status != Skipped:
		steps.ExitCode, steps.Duration = exitCode, time.Since(steps.StartedAt)
		for _, event := range worker.CleanupStepEvents(printed, steps) {
			sandbox.RecordSessionEvent(ctx, worker.ScriptStepEventType, event)
		}
	case err != nil && strings.Contains(printed, "@@ao-step-"):
		// The hook's output came back in part: its steps up to there.
		steps.ExitCode, steps.Duration = -1, time.Since(steps.StartedAt)
		for _, event := range worker.CleanupStepEvents(printed, steps) {
			sandbox.RecordSessionEvent(ctx, worker.ScriptStepEventType, event)
		}
	case err != nil:
		// The provider could not run the hook: each step says why, so the
		// session shows its Cleanup did not run.
		recordSkippedSteps(ctx, steps, ExecFailedReason(err))
	}
	switch {
	case err != nil:
		finish(fmt.Errorf("%s: project cleanup did not report: %w", name, err), attrs)
	case status == Skipped:
		attrs["result"] = Skipped
		finish(nil, attrs)
	case exitCode != 0:
		// The tail carries the failed step, its command, the user and groups
		// it ran as, and where its full output is (worker.CleanupCommandsScript).
		output := boundedOutput(printed)
		attrs["output"] = activity.Excerpt(output, stageOutputLimit)
		if hint := worker.ScriptFailureHint(output, exitCode, ""); hint != "" {
			attrs["hint"] = hint
		}
		finish(fmt.Errorf("%s: project cleanup exited %d", name, exitCode), attrs)
	default:
		finish(nil, attrs)
	}
}

// ReportNotRun reports a Cleanup that could not run at all: the stage as
// skipped, failed with err, and, when the run carries the project's current
// Cleanup, each of its steps as skipped on the session with reason (said to
// the user) as its hint. The deletion goes ahead.
func ReportNotRun(
	ctx context.Context,
	finish func(error, map[string]string),
	attrs map[string]string,
	steps worker.CleanupRun,
	reason string,
	err error,
) {
	recordSkippedSteps(ctx, steps, reason)
	attrs["result"] = Skipped
	finish(err, attrs)
}

// ExecFailedReason is what each step of a Cleanup the provider could not run
// says: the provider's error, redacted and short.
func ExecFailedReason(err error) string {
	detail := strings.Join(strings.Fields(worker.RedactScriptOutput(err.Error())), " ")
	return "AO could not run this Cleanup step in the sandbox before deleting it: " + activity.Truncate(detail, 300)
}

// recordSkippedSteps appends each of the run's current Cleanup steps as
// skipped, with reason as its hint; nothing when the run does not carry them.
func recordSkippedSteps(ctx context.Context, steps worker.CleanupRun, reason string) {
	total := len(steps.Commands)
	for index, stored := range steps.Commands {
		repository, command, _ := domain.ParseRepoScriptCommand(stored)
		sandbox.RecordSessionEvent(ctx, worker.ScriptStepEventType, worker.SanitizeScriptStepEvent(worker.ScriptStepEvent{
			Phase: worker.ScriptPhaseCleanup, RunID: steps.RunID, Step: index + 1, Total: total,
			Command: command, Repository: repository, Status: worker.ScriptStepSkipped, Hint: reason,
		}))
	}
}

// boundedOutput is the hook's output for the stage: redacted as its step
// events are (worker.RedactScriptOutput), without carriage returns or control
// characters, at most stageOutputLimit bytes.
func boundedOutput(output string) string {
	output = worker.RedactScriptOutput(strings.ReplaceAll(output, "\r", ""))
	output = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' || character >= ' ' {
			return character
		}
		return -1
	}, output)
	output = strings.TrimSpace(output)
	if len(output) > stageOutputLimit {
		output = output[len(output)-stageOutputLimit:]
	}
	return output
}

// Run is one sandbox's Cleanup run. It never fails a Delete.
type Run struct {
	done    chan struct{}
	started time.Time
	// limit is how long the run may take before Delete stops waiting for it.
	limit time.Duration
}

// Tracker remembers one provider's Cleanup runs in this control-plane
// process, by key (the sandbox, or for Coder the workspace build). The
// reconciler finishes a whole pass before starting the next, so Delete starts
// a run in the background and reports it unfinished instead of holding every
// other sandbox in the pass for up to Budget; a later tick's Delete waits for
// the same run. The hook itself runs the commands at most once per sandbox,
// so a run another replica starts only waits for and reports the first one.
//
// At most MaxRuns runs go at once for the provider, and MaxOrgRuns for one
// organization; the rest wait for a slot, up to QueueWait, and are then
// reported as not run, so a burst of deletions cannot flood the provider (or
// one organization take every slot) and a deletion is never held for long.
type Tracker struct {
	runs sync.Map // key -> *Run

	// MaxRuns, MaxOrgRuns and QueueWait override DefaultMaxRuns,
	// DefaultMaxOrgRuns and DefaultQueueWait when positive (tests).
	MaxRuns    int
	MaxOrgRuns int
	QueueWait  time.Duration

	mu      sync.Mutex
	running int
	byOrg   map[string]int
	freed   chan struct{} // closed and replaced when a slot frees
}

const (
	// DefaultMaxRuns bounds one provider's concurrent Cleanup runs in a
	// control-plane process.
	DefaultMaxRuns = 4
	// DefaultMaxOrgRuns bounds one organization's.
	DefaultMaxOrgRuns = 2
	// DefaultQueueWait is how long a run waits for a slot. With a run's own
	// limit (up to Budget, Margin and a resume) and StaleAfter it keeps a
	// deletion within the reconciler's deletion deadline (15 minutes).
	DefaultQueueWait = 2 * time.Minute
)

// QueueFullReason is what a Cleanup that waited too long for a slot reports
// on each of its steps.
const QueueFullReason = "AO was running too many Cleanups at once, so this Cleanup step did not run before the sandbox was deleted."

// Lookup returns the run recorded for key.
func (t *Tracker) Lookup(key string) (*Run, bool) {
	run, ok := t.runs.Load(key)
	if !ok {
		return nil, false
	}
	return run.(*Run), true
}

// Start runs fn in the background as key's run, once a slot is free for the
// provider and orgID ("" counts only against the provider), unless a run is
// already recorded for key; it returns the run recorded for key. A run that
// waits QueueWait without a slot calls skip with QueueFullReason instead.
// Delete waits for the run up to the queue wait, limit and StaleAfter.
func (t *Tracker) Start(key, orgID string, limit time.Duration, fn func(), skip func(reason string)) *Run {
	queueWait := t.QueueWait
	if queueWait <= 0 {
		queueWait = DefaultQueueWait
	}
	run := &Run{done: make(chan struct{}), started: time.Now(), limit: queueWait + limit}
	if existing, loaded := t.runs.LoadOrStore(key, run); loaded {
		return existing.(*Run)
	}
	t.prune()
	go func() {
		defer close(run.done)
		if !t.acquire(orgID, queueWait) {
			slog.Warn("project cleanup waited too long for a slot; deleting the sandbox without it",
				"key", key, "org_id", orgID, "queue_wait", queueWait)
			skip(QueueFullReason)
			return
		}
		defer t.release(orgID)
		fn()
	}()
	return run
}

// acquire takes a provider slot and one of orgID's, waiting up to wait.
func (t *Tracker) acquire(orgID string, wait time.Duration) bool {
	maxRuns, maxOrgRuns := t.MaxRuns, t.MaxOrgRuns
	if maxRuns <= 0 {
		maxRuns = DefaultMaxRuns
	}
	if maxOrgRuns <= 0 {
		maxOrgRuns = DefaultMaxOrgRuns
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		t.mu.Lock()
		if t.byOrg == nil {
			t.byOrg = map[string]int{}
		}
		if t.freed == nil {
			t.freed = make(chan struct{})
		}
		if t.running < maxRuns && (orgID == "" || t.byOrg[orgID] < maxOrgRuns) {
			t.running++
			if orgID != "" {
				t.byOrg[orgID]++
			}
			t.mu.Unlock()
			return true
		}
		freed := t.freed
		t.mu.Unlock()
		select {
		case <-freed:
		case <-timer.C:
			return false
		}
	}
}

func (t *Tracker) release(orgID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.running--
	if orgID != "" {
		if t.byOrg[orgID]--; t.byOrg[orgID] <= 0 {
			delete(t.byOrg, orgID)
		}
	}
	if t.freed != nil {
		close(t.freed)
	}
	t.freed = make(chan struct{})
}

// Skipper returns a Start skip function that reports the run as not run
// (ReportNotRun) under ctx's stage observer and session event recorder.
func Skipper(ctx context.Context, attrs map[string]string, steps worker.CleanupRun) func(reason string) {
	return func(reason string) {
		ReportNotRun(ctx, sandbox.BeginStage(ctx, Stage), attrs, steps, reason,
			fmt.Errorf("project cleanup did not run: %s", reason))
	}
}

func (t *Tracker) prune() {
	t.runs.Range(func(key, value any) bool {
		run := value.(*Run)
		select {
		case <-run.done:
			if time.Since(run.started) > RecordTTL {
				t.runs.Delete(key)
			}
		default:
		}
		return true
	})
}

// Await waits up to inline (InlineWait when not positive) for the run and
// reports whether Delete may proceed. A run past its own limit plus
// StaleAfter counts as finished, so Cleanup can delay a deletion but never
// block it.
func (r *Run) Await(ctx context.Context, inline time.Duration, logArgs ...any) bool {
	if inline <= 0 {
		inline = InlineWait
	}
	timer := time.NewTimer(inline)
	defer timer.Stop()
	select {
	case <-r.done:
		return true
	case <-ctx.Done():
	case <-timer.C:
	}
	if time.Since(r.started) > r.limit+StaleAfter {
		slog.WarnContext(ctx, "project cleanup never reported; deleting the sandbox anyway",
			append(logArgs, "started", r.started)...)
		return true
	}
	return false
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
