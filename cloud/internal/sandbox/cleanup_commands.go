package sandbox

import (
	"context"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

// A Cleanup run's budget: the sum of its steps' time limits
// (domain.ScriptStepTimeout) and CleanupStepGrace for each, never below
// MinCleanupBudget (what every run had before per-step limits) nor above
// MaxCleanupBudget, so a deletion is delayed by a bounded time at most.
const (
	MinCleanupBudget = 10 * time.Minute
	MaxCleanupBudget = 2 * time.Hour
	// CleanupStepGrace covers timeout(1)'s kill grace (-k 5) and a step's
	// own start and end around its time limit.
	CleanupStepGrace = 10 * time.Second
)

// CleanupBudget is the budget of a Cleanup run that may run any of the given
// lists of commands (each command led by its time limit's marker when it sets
// one): the largest list's, bounded as above. With no commands it is
// MinCleanupBudget.
func CleanupBudget(lists ...[]string) time.Duration {
	budget := MinCleanupBudget
	for _, commands := range lists {
		var total time.Duration
		for _, command := range commands {
			plain, timeout := domain.SplitScriptStepTimeout(command)
			if strings.TrimSpace(plain) != "" {
				total += timeout + CleanupStepGrace
			}
		}
		budget = max(budget, total)
	}
	return min(budget, MaxCleanupBudget)
}

type cleanupCommandsKey struct{}

// WithCleanupCommands carries a session's current project Cleanup commands
// (Project settings > Scripts) to Provider.Delete. The reconciler reads them
// when it deletes the sandbox, so Cleanup uses the project's config at removal
// time, as a local project does, rather than the commands the worker was
// launched with. An empty list means the project has no Cleanup now.
func WithCleanupCommands(ctx context.Context, commands []string) context.Context {
	return context.WithValue(ctx, cleanupCommandsKey{}, append([]string{}, commands...))
}

// CleanupCommands returns the commands WithCleanupCommands carried, and false
// when the caller could not look them up (the provider then falls back to the
// commands the worker installed).
func CleanupCommands(ctx context.Context) ([]string, bool) {
	commands, ok := ctx.Value(cleanupCommandsKey{}).([]string)
	return commands, ok
}

type installedCleanupKey struct{}

// WithInstalledCleanupCommands carries the Cleanup commands the session's
// worker was last given (domain.InstalledCleanupCommands) to Provider.Delete:
// what the hook runs when the control plane cannot pass the project's current
// Cleanup in, so its output is read knowing the values they assign to
// secret-named variables.
func WithInstalledCleanupCommands(ctx context.Context, commands []string) context.Context {
	return context.WithValue(ctx, installedCleanupKey{}, append([]string{}, commands...))
}

// InstalledCleanupCommands returns the commands WithInstalledCleanupCommands
// carried, and false when none were stored.
func InstalledCleanupCommands(ctx context.Context) ([]string, bool) {
	commands, ok := ctx.Value(installedCleanupKey{}).([]string)
	return commands, ok
}

type skipCleanupKey struct{}

// WithoutCleanup tells Provider.Delete to delete at once without running or
// waiting for the project's Cleanup. The reconciler uses it for its last
// Delete before it gives up on a deletion, so a slow or restarted Cleanup can
// delay a deletion but never leave the workspace running.
func WithoutCleanup(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipCleanupKey{}, true)
}

// CleanupSkipped reports whether WithoutCleanup marked ctx.
func CleanupSkipped(ctx context.Context) bool {
	skip, _ := ctx.Value(skipCleanupKey{}).(bool)
	return skip
}

type cleanupOrgKey struct{}

// WithCleanupOrg names the organization whose sandbox Provider.Delete
// removes, so a provider can bound how many Cleanup runs one organization has
// at once.
func WithCleanupOrg(ctx context.Context, orgID string) context.Context {
	return context.WithValue(ctx, cleanupOrgKey{}, orgID)
}

// CleanupOrg returns the organization WithCleanupOrg named, or "".
func CleanupOrg(ctx context.Context) string {
	orgID, _ := ctx.Value(cleanupOrgKey{}).(string)
	return orgID
}

type cleanupNoResumeKey struct{}

// WithoutCleanupResume tells Provider.Delete not to resume a paused or
// stopped sandbox to run the project's Cleanup, with the reason, said to the
// user: the organization is over its plan, so its paused sandboxes stay
// paused. A running sandbox still runs its Cleanup.
func WithoutCleanupResume(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, cleanupNoResumeKey{}, reason)
}

// CleanupResumeRefused returns the reason WithoutCleanupResume gave, and
// whether it was given.
func CleanupResumeRefused(ctx context.Context) (string, bool) {
	reason, ok := ctx.Value(cleanupNoResumeKey{}).(string)
	return reason, ok
}
