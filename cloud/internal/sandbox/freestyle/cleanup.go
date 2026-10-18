package freestyle

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/cleanuphook"
)

const (
	// cleanupResumeWait bounds how long a Cleanup run waits for a paused or
	// stopped VM to run again before it deletes the VM without Cleanup. A
	// paused VM restores its memory in about a second.
	cleanupResumeWait = time.Minute
	cleanupResumePoll = 500 * time.Millisecond
)

// workerUser is the account the worker runs as in a Freestyle VM (the
// reconciler's default worker user, which the snapshot layout provides); the
// Cleanup hook runs as it.
const workerUser = "ao-worker"

// cleanupRuns tracks Cleanup per VM in this control-plane process.
var cleanupRuns cleanuphook.Tracker

// cleanupFinished reports whether Delete may remove the VM. The first Delete
// of a VM starts the project's Cleanup commands (the hook the worker
// installed) through an exec in the background and waits up to
// cleanuphook.InlineWait for it; while the run is still going Delete returns
// false and the reconciler re-issues it on a later tick. Like Coder's, the
// run is skipped for a project with no Cleanup now, a VM that cannot be read,
// and a Delete marked sandbox.WithoutCleanup, and never blocks deletion: a
// failure or a run over its budget is only reported. It runs only before
// permanent removal, never on pause or on a Recreate.
//
// Unlike a stopped Coder workspace, whose compute is gone, a paused VM keeps
// its memory and disk and resumes in about a second, so a paused (or
// stopped) VM is resumed for its Cleanup; one that does not resume is
// deleted with its Cleanup reported as skipped. A VM of an organization over
// its plan (sandbox.WithoutCleanupResume) is not resumed: its Cleanup is
// reported as skipped with the reason.
func (c *Client) cleanupFinished(ctx context.Context, id sandbox.ID) bool {
	if sandbox.CleanupSkipped(ctx) {
		return true
	}
	key := string(id)
	if run, ok := cleanupRuns.Lookup(key); ok {
		return run.Await(ctx, c.cleanupInline, "provider_id", key)
	}
	if current, known := sandbox.CleanupCommands(ctx); known && len(current) == 0 {
		return true
	}
	environment, err := c.Get(ctx, id)
	if err != nil {
		return true
	}
	resume := false
	switch environment.State {
	case sandbox.StateRunning:
	case sandbox.StatePaused, sandbox.StateStopped:
		resume = true
	default:
		// Starting or pausing: not a VM the hook can run in now.
		return true
	}
	plan, _ := cleanuphook.NewPlan(ctx, "provider_id", key)
	// Detached from the reconcile pass's cancellation, but keeping its stage
	// observer and session event recorder.
	runCtx := context.WithoutCancel(ctx)
	attrs := map[string]string{"provider_id": key}
	skip := cleanuphook.Skipper(runCtx, attrs, plan.Steps)
	if reason, refused := sandbox.CleanupResumeRefused(ctx); resume && refused {
		// The organization is over its plan: its paused VM stays paused, and
		// the Cleanup is reported as not run, once.
		attrs["resumed"] = "false"
		run := cleanupRuns.Start(key, sandbox.CleanupOrg(ctx), 0, func() { skip(reason) }, skip)
		return run.Await(ctx, c.cleanupInline, "provider_id", key)
	}
	wait := c.cleanupWait
	if wait <= 0 {
		wait = cleanuphook.Budget
	}
	resumeWait := c.cleanupResumeWait
	if resumeWait <= 0 {
		resumeWait = cleanupResumeWait
	}
	limit := wait + cleanuphook.Margin
	if resume {
		limit += resumeWait
	}
	run := cleanupRuns.Start(key, sandbox.CleanupOrg(ctx), limit, func() {
		c.log.InfoContext(ctx, "freestyle: running project cleanup before deleting the VM",
			"provider_id", key, "budget", wait, "resume", resume)
		c.runCleanup(runCtx, id, resume, resumeWait, wait, plan, attrs)
	}, skip)
	return run.Await(ctx, c.cleanupInline, "provider_id", key)
}

// runCleanup resumes the VM when asked, runs the worker-installed Cleanup hook
// as its owner in one exec, reports the outcome as cleanuphook.Stage, and
// appends the run's steps to the session's events.
func (c *Client) runCleanup(
	ctx context.Context,
	id sandbox.ID,
	resume bool,
	resumeWait, wait time.Duration,
	plan cleanuphook.Plan,
	attrs map[string]string,
) {
	finish := sandbox.BeginStage(ctx, cleanuphook.Stage)
	steps := plan.Steps
	if resume {
		attrs["resumed"] = "true"
		if err := c.resumeForCleanup(ctx, id, resumeWait); err != nil {
			cleanuphook.ReportNotRun(ctx, finish, attrs, steps,
				"The sandbox was paused and did not resume, so this Cleanup step did not run before the sandbox was deleted.",
				fmt.Errorf("freestyle: resume the paused VM for project cleanup: %w", err))
			return
		}
	}
	steps.StartedAt = time.Now()
	runCtx, cancel := context.WithTimeout(ctx, wait+cleanuphook.Margin)
	defer cancel()
	command := cleanuphook.Command(cleanuphook.HookWord(cleanuphook.WorkspaceHook), wait, plan.Override, cleanuphook.Options{
		Runner: cleanuphook.AsRoot, Stop: cleanuphook.StopOwnerProcesses, Owner: workerUser,
	})
	printed, status, err := c.execCleanupHook(runCtx, id, command, wait+cleanuphook.Margin)
	cleanuphook.Report(ctx, finish, attrs, steps, printed, status, err, "freestyle")
}

// resumeForCleanup starts a paused or stopped VM and waits up to wait for it
// to run.
func (c *Client) resumeForCleanup(ctx context.Context, id sandbox.ID, wait time.Duration) error {
	if err := c.Start(ctx, id); err != nil {
		return err
	}
	deadline := time.Now().Add(wait)
	for {
		environment, err := c.Get(ctx, id)
		if err != nil {
			return err
		}
		if environment.State == sandbox.StateRunning {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("VM %s is still %s %s after its start", id, environment.State, wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(cleanupResumePoll):
		}
	}
}

// execCleanupHook runs the hook command as root in one exec (the command runs
// the hook as its owner) and returns what the hook printed and its status.
// The exec waits for the whole run, so it is bounded by ctx and timeout
// rather than the client's per-call timeout.
func (c *Client) execCleanupHook(ctx context.Context, id sandbox.ID, command string, timeout time.Duration) (string, string, error) {
	long := *c.http
	long.Timeout = 0
	var result execResult
	if err := c.doWith(ctx, &long, http.MethodPost, "/v5/vms/"+url.PathEscape(string(id))+"/exec-await", execRequest{
		Command:   command,
		LinuxUser: "root",
		TimeoutMs: int(timeout / time.Millisecond),
	}, &result); err != nil {
		return "", "", err
	}
	printed, status, ok := cleanuphook.Split(result.Stdout)
	if !ok {
		exit := "without a status"
		if result.StatusCode != nil {
			exit = fmt.Sprintf("with status %d", *result.StatusCode)
		}
		return printed, "", fmt.Errorf("the hook command ended %s before reporting: %s",
			exit, truncate(strings.TrimSpace(result.Stderr), 512))
	}
	return printed, status, nil
}
