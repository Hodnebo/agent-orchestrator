package coder

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/cleanuphook"
)

// Worker environment for a bring-your-own connection's working directory and
// secret-file protection (mirrored by the worker package's constants of the
// same names).
const (
	WorkingDirectoryEnv   = "AO_WORKER_WORKING_DIRECTORY"
	ProtectSecretFilesEnv = "AO_WORKER_PROTECT_SECRET_FILES"
	// workspaceNameEnv names the session's Coder workspace for the project's
	// scripts and the agent; a worker launched by AO does not otherwise see it.
	workspaceNameEnv = "CODER_WORKSPACE_NAME"
	// cleanupHookFile is the hook the worker installs in its data directory
	// (worker.CleanupHookFile) when the project has Cleanup commands.
	cleanupHookFile = "cleanup-hook.sh"
)

// The shared Cleanup run (cleanuphook), under the names this package's
// callers and tests use.
const (
	// StageCleanup is the project's Cleanup commands, run before AO deletes
	// the session's workspace.
	StageCleanup       = cleanuphook.Stage
	cleanupMarker      = cleanuphook.Marker
	cleanupSkipped     = cleanuphook.Skipped
	cleanupBudget      = cleanuphook.Budget
	cleanupMargin      = cleanuphook.Margin
	cleanupOutputLimit = cleanuphook.OutputLimit
)

// cleanupRuns tracks Cleanup per workspace build in this control-plane
// process (cleanuphook.Tracker).
var cleanupRuns cleanuphook.Tracker

// addLifecycleEnvironment adds the workspace name and the connection's working
// directory to a workspace-user worker's environment.
func (c *Client) addLifecycleEnvironment(environment map[string]string, workspaceName string) {
	if workspaceName != "" {
		environment[workspaceNameEnv] = workspaceName
	}
	if c.workingDirectory != "" {
		environment[WorkingDirectoryEnv] = c.workingDirectory
	}
}

// cleanupFinished reports whether Delete may remove the workspace. The first
// Delete of a running, reachable workspace starts the project's Cleanup
// commands (the hook the worker installed) through the agent PTY in the
// background and waits up to cleanuphook.InlineWait for it; while the run is still
// going Delete returns false and the reconciler re-issues it on a later tick.
// A workspace that is not running or reachable, or a
// client without a durable root, has nothing to clean up. Cleanup never
// blocks deletion: a failure or a run over cleanupBudget is only reported as
// a stage. Like local cleanup it runs only before permanent removal, never on
// stop or pause.
func (c *Client) cleanupFinished(ctx context.Context, id sandbox.ID) bool {
	if c.durableRoot == "" || sandbox.CleanupSkipped(ctx) {
		return true
	}
	var view workspace
	if err := c.do(ctx, http.MethodGet, "/api/v2/workspaces/"+url.PathEscape(string(id)), nil, &view); err != nil {
		return true
	}
	key := string(id) + "/" + view.LatestBuild.ID
	if existing, ok := cleanupRuns.Lookup(key); ok {
		return existing.Await(ctx, c.cleanupInline, "workspace_id", string(id))
	}
	// The project's current Cleanup, when the caller read it: an empty list
	// means the project has no Cleanup now, so nothing runs.
	if current, known := sandbox.CleanupCommands(ctx); known && len(current) == 0 {
		return true
	}
	agent, ok := c.selectAgent(view)
	if normalizeState(view.LatestBuild.Status) != sandbox.StateRunning || !ok || !agentReady(agent) {
		return true
	}
	ptyURL, err := url.Parse(c.baseURL + "/api/v2/workspaceagents/" + url.PathEscape(agent.ID) + "/pty")
	if err != nil {
		return true
	}
	plan, _ := cleanuphook.NewPlan(ctx, "workspace_id", string(id))
	wait := c.cleanupWait
	if wait <= 0 {
		wait = cleanupBudget
	}
	// Detached from the reconcile pass's cancellation, but keeping its stage
	// observer so the outcome is still logged against the session.
	runCtx := context.WithoutCancel(ctx)
	attrs := c.stageAttrs(string(id), agent.ID)
	run := cleanupRuns.Start(key, wait+cleanupMargin, func() {
		slog.InfoContext(ctx, "coder: running project cleanup before deleting the workspace",
			"workspace_id", string(id), "budget", wait)
		c.runCleanup(runCtx, ptyURL, wait, plan, attrs)
	})
	return run.Await(ctx, c.cleanupInline, "workspace_id", string(id))
}

// runCleanup runs the worker-installed Cleanup hook through the agent PTY,
// reports the outcome as StageCleanup, and appends the run's steps to the
// session's events (cleanuphook.Report).
func (c *Client) runCleanup(ctx context.Context, ptyURL *url.URL, wait time.Duration, plan cleanuphook.Plan, attrs map[string]string) {
	finish := sandbox.BeginStage(ctx, StageCleanup)
	steps := plan.Steps
	steps.StartedAt = time.Now()
	runCtx, cancel := context.WithTimeout(ctx, wait+cleanupMargin)
	defer cancel()
	attemptURL := *ptyURL
	query := attemptURL.Query()
	query.Set("width", "200")
	query.Set("height", "50")
	query.Set("command", "sh -c "+shellQuote(cleanupHookCommand(c.durableRoot, wait, plan.Override)))
	query.Set("backend_type", "buffered")
	attemptURL.RawQuery = query.Encode()
	// Opening the agent's terminal now and then ends at once with no output.
	// A quick failure like that is retried a few times; the hook's run-once
	// lock keeps a retry from running the commands twice.
	var result readyProbeResult
	var status string
	var err error
	for attempt := 1; ; attempt++ {
		started := time.Now()
		result, status, err = c.runMarkedCommandOnce(runCtx, &attemptURL, cleanupMarker, wait+cleanupMargin, cleanupOutputLimit)
		if err == nil || !errors.Is(err, errTerminalNotReady) || attempt == cleanupTerminalAttempts ||
			time.Since(started) > cleanupQuickFailure || runCtx.Err() != nil {
			break
		}
		select {
		case <-runCtx.Done():
		case <-time.After(cleanupTerminalRetryDelay):
		}
	}
	cleanuphook.Report(ctx, finish, attrs, steps, result.raw, status, err, "coder")
}

// A Cleanup whose terminal fails within cleanupQuickFailure is retried, up to
// cleanupTerminalAttempts in all, cleanupTerminalRetryDelay apart.
const (
	cleanupTerminalAttempts   = 3
	cleanupQuickFailure       = 5 * time.Second
	cleanupTerminalRetryDelay = 2 * time.Second
)

// cleanupCommandsPayload is cleanuphook.CommandsPayload.
func cleanupCommandsPayload(commands []string, nonce string) (string, bool) {
	return cleanuphook.CommandsPayload(commands, nonce)
}

// cleanupHookCommand runs the worker-installed Cleanup hook under the durable
// root as its owner from the agent terminal, which runs as the workspace
// login user (cleanuphook.Command).
func cleanupHookCommand(durableRoot string, budget time.Duration, override string) string {
	hook := shellQuote(path.Join(durableRoot, ".ao", "worker", cleanupHookFile))
	if sandbox.NormalizeCoderDurableRoot(durableRoot) == sandbox.CoderHomeDurableRoot {
		hook = `"$HOME"/.ao/worker/` + cleanupHookFile
	}
	return cleanuphook.Command(hook, budget, override, cleanuphook.AsLoginUser)
}
