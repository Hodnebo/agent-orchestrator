package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A project's Setup (postCreate) and Cleanup (preRemove) scripts, the same
// project settings a local project uses (Project settings > Scripts). Setup
// commands run in order in the session's workspace after its checkout and
// before the coding agent starts; Cleanup commands run before the session's
// workspace is permanently deleted.
const (
	// ProjectScriptStepTimeout bounds each Setup or Cleanup command, as the
	// local daemon does.
	ProjectScriptStepTimeout = 10 * time.Minute
	// ProjectScriptOutputLimit bounds the command output kept for the worker
	// log and the session event, as the local daemon does.
	ProjectScriptOutputLimit = 8 << 10
	// setupDoneFile, in the worker data directory, records that the session's
	// Setup succeeded. Setup runs once per session, like a local workspace's
	// setup runs once when the workspace is created: a worker restart or a
	// workspace stop/start keeps the prepared checkout and does not run it
	// again.
	setupDoneFile = "setup.done"
	// setupAttemptedFile records that this session's Setup started running,
	// so a worker restart after a failed Setup runs it again (and keeps the
	// agent blocked) instead of treating the session as already set up.
	setupAttemptedFile = "setup.attempted"
	// CleanupHookFile and CleanupCommandsFile, in the worker data directory,
	// are what the control plane runs before it deletes the session's sandbox: the
	// hook enters the session's workspace with the session environment and
	// runs the Cleanup commands script beside it. The control plane rewrites
	// that script with the project's current Cleanup before running the hook,
	// as a local project reads its current config at removal time.
	CleanupHookFile     = "cleanup-hook.sh"
	CleanupCommandsFile = "cleanup-commands.sh"
	// CleanupHookOutputLimit bounds the output tail the Cleanup hook prints
	// for the control plane, which splits it into the run's step events.
	CleanupHookOutputLimit = 64 << 10
	// cleanupStateDir, in the worker data directory, makes Cleanup run at most
	// once per workspace: the first hook run creates it and records the
	// outcome there, and any later run (another control-plane replica, a
	// re-issued delete) waits for and reports that outcome instead.
	cleanupStateDir = "cleanup-state"
)

// Setup outcomes reported in SetupEvent.Status.
const (
	SetupSucceeded = "succeeded"
	SetupFailed    = "failed"
	SetupTimedOut  = "timed_out"
)

// SetupEventType is the worker event that reports a project's Setup outcome
// on the session's event stream.
const SetupEventType = "workspace.setup"

// SetupEvent is the payload of SetupEventType.
type SetupEvent struct {
	Status string `json:"status"`
	// Step is the 1-based command that failed or timed out; 0 on success.
	Step       int   `json:"step,omitempty"`
	ExitCode   int   `json:"exitCode"`
	DurationMS int64 `json:"durationMs"`
	// Output is the tail of the failed command's output (the last command's on
	// success).
	Output string `json:"output,omitempty"`
	// The rest describe a failed step, so it can be fixed without opening the
	// sandbox: its command (one line, bounded), the repository folder it ran
	// in ("" for the main repository), the directory, the Linux user and
	// groups it ran as, the file in the sandbox with its full output, and a
	// hint when the output shows a permission problem.
	Command    string   `json:"command,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Dir        string   `json:"dir,omitempty"`
	User       string   `json:"user,omitempty"`
	Groups     []string `json:"groups,omitempty"`
	LogPath    string   `json:"logPath,omitempty"`
	Hint       string   `json:"hint,omitempty"`
}

// Summary names the failed step and why, in the local daemon's words
// ("setup step N failed"), without the command's output.
func (e SetupEvent) Summary() string {
	cause := fmt.Sprintf("exit status %d", e.ExitCode)
	if e.Status == SetupTimedOut {
		cause = "timed out after " + ProjectScriptStepTimeout.String()
	}
	return fmt.Sprintf("Setup step %d failed: %s", e.Step, cause)
}

// Context is a failed step's command, who and where it ran, and its log, one
// fact per line, for the startup screen's details and the worker log.
func (e SetupEvent) Context() string {
	var lines []string
	if e.Command != "" {
		lines = append(lines, fmt.Sprintf("Step %d: %s", e.Step, e.Command))
	}
	if e.User != "" {
		ran := "Ran as " + e.User
		if len(e.Groups) > 0 {
			ran += " (groups: " + strings.Join(e.Groups, " ") + ")"
		}
		if e.Dir != "" {
			ran += " in " + e.Dir
		}
		lines = append(lines, ran)
	}
	if e.LogPath != "" {
		lines = append(lines, "Full output: "+e.LogPath)
	}
	return strings.Join(lines, "\n")
}

// Message is the startup error shown when Setup did not succeed: the summary
// followed by the command's output.
func (e SetupEvent) Message() string {
	message := e.Summary()
	if output := strings.TrimSpace(e.Output); output != "" {
		message += ": " + output
	}
	return message
}

// WorkspaceStartupEventType is the worker event that reports whether the
// session's workspace (checkout, working directory and Setup) is ready for
// its agent. A failure becomes the session's startup error, shown on the
// desktop's startup screen; a later ready report clears it.
const WorkspaceStartupEventType = "workspace.startup"

const (
	WorkspaceStartupFailed = "failed"
	WorkspaceStartupReady  = "ready"
)

// WorkspaceStartupEvent is the payload of WorkspaceStartupEventType. Code is
// one of domain.WorkspaceStartupErrorCodes on a failure. Detail is a bounded,
// redacted excerpt (the failed Setup command's output).
type WorkspaceStartupEvent struct {
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// SessionScriptEnvironment is the AO_* environment a Setup or Cleanup command
// sees.
type SessionScriptEnvironment struct {
	SessionID    string
	SessionKind  string
	Branch       string
	WorkspaceDir string
	// ReposRoot is the folder that holds the session's checkouts: the main
	// repository's and each additional repository's beside it.
	ReposRoot     string
	RepositoryURL string
	ProjectID     string
	ProjectName   string
}

// Map returns the environment as AO_* variables. AO_WORKTREE_PATH and
// AO_SOURCE_TREE_PATH are the variables a local project's scripts use. A
// cloud session has no separate source tree (its checkout is the only copy of
// the repository in the sandbox), so both name the session's workspace.
// AO_REPOS_ROOT is the folder that holds every checkout of a project with
// additional repositories, each in its own folder (see ExtraRepoPath).
func (e SessionScriptEnvironment) Map() map[string]string {
	reposRoot := e.ReposRoot
	if reposRoot == "" && e.WorkspaceDir != "" {
		reposRoot = filepath.Dir(e.WorkspaceDir)
	}
	return map[string]string{
		"AO_REPOS_ROOT":       reposRoot,
		"AO_SESSION_ID":       e.SessionID,
		"AO_SESSION_KIND":     e.SessionKind,
		"AO_SESSION_BRANCH":   e.Branch,
		"AO_WORKSPACE_DIR":    e.WorkspaceDir,
		"AO_WORKTREE_PATH":    e.WorkspaceDir,
		"AO_SOURCE_TREE_PATH": e.WorkspaceDir,
		"AO_REPOSITORY_URL":   e.RepositoryURL,
		"AO_PROJECT_ID":       e.ProjectID,
		"AO_PROJECT_NAME":     e.ProjectName,
	}
}

// SetupRunner runs a project's Setup commands once per session.
type SetupRunner struct {
	DataDir string
	// FirstStart is true on the session's first worker start (no worker
	// token persisted yet). Setup runs when the session's workspace is first
	// prepared, like a local workspace's: a session that started without Setup
	// (or under a worker from before Setup existed) never runs one added to
	// the project later inside its already-prepared workspace.
	FirstStart bool
	// StepTimeout overrides ProjectScriptStepTimeout for tests.
	StepTimeout time.Duration
	// Publish, when set, receives the run's ScriptStepEventType events: each
	// step's start, live updates of its output while it runs, its final
	// outcome, and the steps a failure skipped. It is called from its own
	// goroutine, so a slow or failing control plane never holds up a step;
	// it returns ErrScriptStepsUnsupported when the control plane refuses
	// the event type.
	Publish func(context.Context, ScriptStepEvent) error
	// Secrets returns credentials the step's environment does not name
	// (the worker's token), masked in the published output and commands.
	Secrets func() []string
	// UpdateInterval overrides liveUpdateInterval for tests.
	UpdateInterval time.Duration
	// FinalWait overrides scriptStepFinalWait for tests.
	FinalWait time.Duration
}

// Run runs commands in order in dir with `sh -c`, stopping at the first one
// that fails. ran is false when there is nothing to run or Setup already
// succeeded for this session. Only a success is recorded, so a failed Setup
// runs again when the session is reopened.
func (r SetupRunner) Run(
	ctx context.Context,
	commands []string,
	dir string,
	environment SessionScriptEnvironment,
) (event SetupEvent, ran bool) {
	commands = nonEmptyCommands(commands)
	marker := filepath.Join(r.DataDir, setupDoneFile)
	if _, err := os.Stat(marker); err == nil {
		return SetupEvent{}, false
	}
	attempted := filepath.Join(r.DataDir, setupAttemptedFile)
	_, attemptErr := os.Stat(attempted)
	if len(commands) == 0 || (!r.FirstStart && attemptErr != nil) {
		// Nothing to run, or a restarted worker whose session never started a
		// Setup: the workspace is already prepared, so record it as set up.
		r.writeMarker(marker)
		return SetupEvent{}, false
	}
	r.writeMarker(attempted)
	timeout := r.StepTimeout
	if timeout <= 0 {
		timeout = ProjectScriptStepTimeout
	}
	// Each run keeps only its own steps' logs.
	logs := filepath.Join(r.DataDir, setupLogsDir)
	_ = os.RemoveAll(logs)
	if err := os.MkdirAll(logs, 0o700); err != nil {
		logs = ""
	}
	redactor := r.redactor()
	steps := r.startStepEvents(ctx, len(commands), redactor)
	defer steps.close()
	started := time.Now()
	event = SetupEvent{Status: SetupSucceeded}
	for index, command := range commands {
		logPath := ""
		if logs != "" {
			logPath = filepath.Join(logs, "step-"+strconv.Itoa(index+1)+".log")
		}
		live := steps.start(index+1, command, logPath)
		step := runProjectScriptStep(ctx, command, timeout, dir, environment.Map(), logPath, live)
		// Only a failure carries output in the summary event: it is what the
		// startup screen shows to fix the step. Each step's own events carry
		// its redacted output (setupStepEvents).
		if step.Status != SetupSucceeded {
			// The summary (and the startup error made from it) carries the
			// output as the step's events do: carriage-return overwrites
			// collapsed, escapes stripped, and the values of the secret-named
			// environment variables and the worker's token masked before the
			// patterns run.
			step.Output, _ = redactor.scriptStepOutput(step.Output, false)
			step.Step = index + 1
			step.DurationMS = time.Since(started).Milliseconds()
			step.Command, step.Repository = scriptCommandSummary(command, redactor)
			step.Dir = dir
			if step.Repository != "" {
				step.Dir = filepath.Join(filepath.Dir(dir), step.Repository)
			}
			identity := CurrentScriptIdentity()
			step.User, step.Groups = identity.User, identity.Groups
			if logPath != "" {
				if _, err := os.Stat(logPath); err == nil {
					step.LogPath = logPath
				}
			}
			step.Hint = ScriptFailureHint(step.Output, step.ExitCode, step.User)
			steps.finish(step)
			steps.skip(commands[index+1:])
			return step, true
		}
		steps.finish(step)
	}
	event.DurationMS = time.Since(started).Milliseconds()
	r.writeMarker(marker)
	return event, true
}

func (r SetupRunner) writeMarker(path string) {
	if err := os.MkdirAll(r.DataDir, 0o700); err == nil {
		_ = os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
	}
}

func nonEmptyCommands(commands []string) []string {
	kept := make([]string, 0, len(commands))
	for _, command := range commands {
		if strings.TrimSpace(command) != "" {
			kept = append(kept, command)
		}
	}
	return kept
}

// runProjectScriptStep runs one command as `sh -c command` in its own process
// group, so a timeout also stops whatever the command started.
func runProjectScriptStep(
	ctx context.Context,
	command string,
	timeout time.Duration,
	dir string,
	environment map[string]string,
	logPath string,
	live io.Writer,
) SetupEvent {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	process := exec.CommandContext(runCtx, "sh", "-c", command)
	process.Dir = dir
	process.Env = mergeEnvironment(os.Environ(), environment)
	output := &tailBuffer{limit: ProjectScriptOutputLimit}
	sinks := []io.Writer{output}
	if live != nil {
		sinks = append(sinks, live)
	}
	if logPath != "" {
		if file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
			defer file.Close()
			sinks = append(sinks, &cappedFile{file: file, left: scriptLogLimit})
		}
	}
	var sink io.Writer = output
	if len(sinks) > 1 {
		sink = io.MultiWriter(sinks...)
	}
	process.Stdout = sink
	process.Stderr = sink
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	process.Cancel = func() error {
		if process.Process == nil {
			return nil
		}
		return syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
	}
	// A successful shell can leave a detached child holding its output pipes;
	// WaitDelay closes them without failing the step (as the local daemon does).
	process.WaitDelay = time.Second
	err := process.Run()
	if errors.Is(err, exec.ErrWaitDelay) && runCtx.Err() == nil {
		err = nil
	}
	event := SetupEvent{Status: SetupSucceeded, Output: strings.TrimSpace(output.String())}
	switch {
	case err == nil:
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		event.Status, event.ExitCode = SetupTimedOut, -1
	default:
		event.Status, event.ExitCode = SetupFailed, -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			event.ExitCode = exitErr.ExitCode()
		} else if event.Output == "" {
			event.Output = err.Error()
		}
	}
	return event
}

// mergeEnvironment overlays overrides on a KEY=VALUE environment.
func mergeEnvironment(base []string, overrides map[string]string) []string {
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[key]; !replaced {
			merged = append(merged, entry)
		}
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		merged = append(merged, key+"="+overrides[key])
	}
	return merged
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	limit   int
	data    bytes.Buffer
	dropped bool
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.data.Write(p)
	if over := b.data.Len() - b.limit; over > 0 {
		b.data.Next(over)
		b.dropped = true
	}
	return len(p), nil
}

// String is the kept output. Once its beginning was dropped, what it then
// starts with that may be cut short is dropped too (dropPartialHead): a secret
// cut in half there is not recognized by any redaction pattern.
func (b *tailBuffer) String() string {
	text := b.data.String()
	if b.dropped {
		return dropPartialHead(text)
	}
	return text
}

// CleanupCommandsScript is the shell script the Cleanup hook runs: each
// command with `sh -c` in order, each bounded by ProjectScriptStepTimeout when
// timeout(1) is available, stopping at the first failure with that command's
// status. With no commands it does nothing. Around each command it prints a
// start and an end marker line carrying nonce (NewScriptMarkerNonce), which
// the control plane parses into the run's step events
// (CleanupStepEvents); the commands, their order, environment and exit
// status are the same as without them.
func CleanupCommandsScript(commands []string, nonce string) string {
	seconds := strconv.Itoa(int(ProjectScriptStepTimeout / time.Second))
	commands = nonEmptyCommands(commands)
	total := strconv.Itoa(len(commands))
	marker := scriptMarkerPrefix + nonce + " "
	var steps strings.Builder
	steps.WriteString("#!/bin/sh\n# Written by AO: the project's Cleanup commands.\n")
	steps.WriteString("ao_timeout=\nif command -v timeout >/dev/null 2>&1; then ao_timeout=\"timeout -k 5 " + seconds + "\"; fi\n")
	if len(commands) > 0 {
		// Milliseconds since the epoch: from nanoseconds when date has %N (16
		// digits or more), else from seconds, else 0; never anything that
		// would stop the script.
		steps.WriteString("ao_ms() { ao_t=$(date +%s%N 2>/dev/null) || ao_t=\n" +
			"  case \"$ao_t\" in *[!0-9]*) ao_t= ;; ????????????????*) ao_t=${ao_t%??????} ;; *) ao_t= ;; esac\n" +
			"  [ -n \"$ao_t\" ] || ao_t=$(date +%s 2>/dev/null)000\n" +
			"  case \"$ao_t\" in ''|*[!0-9]*) ao_t=0 ;; esac\n" +
			"  echo \"$ao_t\"\n}\n")
		steps.WriteString("printf '%s\\n' " + shellQuote(marker+"log") + "\" $(dirname \"$0\")/" + cleanupStateDir + "/output\"\n")
	}
	for index, command := range commands {
		step := strconv.Itoa(index + 1)
		summary, repository := scriptCommandSummary(command, scriptRedactor{})
		if repository == "" {
			repository = scriptMarkerMainRepository
		}
		steps.WriteString("ao_started=$(ao_ms)\n")
		steps.WriteString("printf '%s %s %s\\n' " + shellQuote(marker+step+" "+total+" start") + " \"$ao_started\" " +
			shellQuote(repository+" "+summary) + "\n")
		steps.WriteString("$ao_timeout sh -c " + shellQuote(command) + " </dev/null\n")
		end := "echo \"" + marker + step + " " + total + " end $ao_code $(($(ao_ms) - $ao_started))\"\n"
		// Say which step, what it ran, and as whom and where, so a failure in
		// the logs needs no trip into the sandbox.
		steps.WriteString("ao_code=$?\nif [ \"$ao_code\" -ne 0 ]; then\n" +
			"  echo \"cleanup step " + step + " failed: exit status $ao_code\" >&2\n" +
			"  echo " + shellQuote("cleanup step "+step+": "+summary) + " >&2\n" +
			"  echo \"ran as $(id -un 2>/dev/null) (groups: $(id -Gn 2>/dev/null)) in $(pwd)\" >&2\n" +
			"  " + end +
			"  exit \"$ao_code\"\nfi\n" + end)
	}
	return steps.String()
}

// WriteCleanupHook installs the hook the control plane runs before it deletes
// the session's workspace. The hook enters dir with the session environment
// and runs the Cleanup commands script (CleanupCommandsScript). It is
// installed even when the project has no Cleanup yet, so Cleanup added to the
// project later still runs in the session's workspace with its environment.
func WriteCleanupHook(dataDir string, commands []string, dir string, environment SessionScriptEnvironment) error {
	hook := filepath.Join(dataDir, CleanupHookFile)
	commandsPath := filepath.Join(dataDir, CleanupCommandsFile)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(commandsPath, []byte(CleanupCommandsScript(commands, NewScriptMarkerNonce())), 0o600); err != nil {
		return err
	}
	state := filepath.Join(dataDir, cleanupStateDir)
	var content strings.Builder
	content.WriteString("#!/bin/sh\n# Written by the AO worker: runs the project's Cleanup commands once.\n")
	content.WriteString("ao_state=" + shellQuote(state) + "\n")
	content.WriteString("if ! mkdir \"$ao_state\" 2>/dev/null; then\n" +
		"  while [ ! -f \"$ao_state/status\" ]; do sleep 2; done\n" +
		"  cat \"$ao_state/output\" 2>/dev/null\n" +
		"  exit \"$(cat \"$ao_state/status\")\"\n" +
		"fi\n")
	content.WriteString("cd " + shellQuote(dir) + " >\"$ao_state/output\" 2>&1 || { echo 1 >\"$ao_state/status\"; cat \"$ao_state/output\"; exit 1; }\n")
	values := environment.Map()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		content.WriteString("export " + key + "=" + shellQuote(values[key]) + "\n")
	}
	content.WriteString("sh " + shellQuote(commandsPath) + " >>\"$ao_state/output\" 2>&1\n" +
		"ao_code=$?\n" +
		"tail -c " + strconv.Itoa(CleanupHookOutputLimit) + " \"$ao_state/output\"\n" +
		"if [ \"$ao_code\" -ne 0 ]; then echo \"full cleanup output: $ao_state/output\"; fi\n" +
		"echo \"$ao_code\" >\"$ao_state/status\"\n" +
		"exit \"$ao_code\"\n")
	return writeFileAtomic(hook, []byte(content.String()), 0o700)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	temporary := path + ".tmp-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(temporary, mode); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
