package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workertransport"
)

// chatStartChecks are what startInterface asks of the workspace and the
// control plane; tests replace them.
type chatStartChecks struct {
	// firstStart is true on the session's first worker start in this
	// workspace (no persisted worker token). Only then may a session move off
	// Chat: a restarted or reconnecting worker leaves the interface alone.
	firstStart bool
	// harnessInstalled reports whether the harness binary is on PATH.
	harnessInstalled func(harness string) bool
	// claudeChatStartable is workerexec.ClaudeACPAdapterStartable.
	claudeChatStartable func() error
	// fallBack asks the control plane to move the session to the terminal
	// (POST /worker/interface/chat-start-fallback) and reports whether it did.
	fallBack func(ctx context.Context, code string) (bool, error)
}

// startInterface is the interface the worker starts its session in: the one
// the control plane committed, except that a Claude session committed to Chat
// in a workspace with no ACP adapter and no npm to install one starts in the
// terminal, with an interface notice, instead of in a Chat whose every turn
// would fail. The control plane decides: it moves only a session created in
// Chat that never started, and a session the person switched to Chat stays
// there.
//
// The check waits for nothing. It runs only once the harness itself is
// installed: a template that installs its tools in the background may add
// npm along with the harness, and until then the adapter's install is not
// known to be impossible. An install that starts and then fails is settled
// later, once the workspace is ready (settleClaudeChatStart).
func startInterface(ctx context.Context, logger *slog.Logger, launch worker.LaunchContext, checks chatStartChecks) string {
	committed := strings.TrimSpace(launch.Interface)
	if committed == "" {
		return workertransport.InterfaceTUI
	}
	if !checks.firstStart || committed != workertransport.InterfaceChat || launch.Harness != "claude-code" ||
		!checks.harnessInstalled(launch.Harness) {
		return committed
	}
	reason := checks.claudeChatStartable()
	if !errors.Is(reason, workerexec.ErrChatAdapterUnavailable) {
		return committed
	}
	moved, err := checks.fallBack(ctx, worker.ChatStartAdapterUnavailable)
	switch {
	case err != nil:
		logger.Warn("chat start: Claude Chat cannot run here and the session could not move to the terminal",
			"reason", reason, "error", err)
		return committed
	case !moved:
		logger.Warn("chat start: Claude Chat cannot run here; the session stays in Chat", "reason", reason)
		return committed
	}
	logger.Info("chat start: Claude Chat cannot run here; the session starts in the terminal", "reason", reason)
	return workertransport.InterfaceTUI
}

// claudeChatAdapterWait bounds how long a Claude session that starts in Chat
// waits, once its workspace is ready, for the Claude ACP adapter's install
// (workerexec allows the install four minutes). Past it the session stays in
// Chat and its first turn waits for the adapter itself.
const claudeChatAdapterWait = 5 * time.Minute

// chatAdapterSettle is what settleClaudeChatStart asks of the workspace, the
// control plane and the transport; tests replace them.
type chatAdapterSettle struct {
	// firstStart is chatStartChecks.firstStart.
	firstStart bool
	// ensure waits for the Claude ACP adapter (workerexec.EnsureClaudeACPAdapter).
	ensure func(ctx context.Context) error
	// fallBack is chatStartChecks.fallBack.
	fallBack func(ctx context.Context, code string) (bool, error)
	// switchToTUI replaces the Chat start with the terminal agent
	// (workertransport.Supervisor.FallBackToTUI).
	switchToTUI func(ctx context.Context) error
}

// settleClaudeChatStart runs once the workspace is ready and before the Chat
// controller may take a turn. startInterface settled the case it can see at
// once (no adapter and no npm); here the adapter's install has had its
// chance. When the install failed, the session moves to the terminal with the
// same notice, rather than every Chat turn failing on the missing adapter. It
// reports whether the session moved.
func settleClaudeChatStart(ctx context.Context, logger *slog.Logger, launch worker.LaunchContext, settle chatAdapterSettle) bool {
	if !settle.firstStart || launch.Interface != workertransport.InterfaceChat || launch.Harness != "claude-code" {
		return false
	}
	waitCtx, cancel := context.WithTimeout(ctx, claudeChatAdapterWait)
	reason := settle.ensure(waitCtx)
	cancel()
	if !errors.Is(reason, workerexec.ErrChatAdapterUnavailable) || ctx.Err() != nil {
		return false
	}
	moved, err := settle.fallBack(ctx, worker.ChatStartAdapterUnavailable)
	switch {
	case err != nil:
		logger.Warn("chat start: the Claude ACP adapter could not be installed and the session could not move to the terminal",
			"reason", reason, "error", err)
		return false
	case !moved:
		logger.Warn("chat start: the Claude ACP adapter could not be installed; the session stays in Chat", "reason", reason)
		return false
	}
	if err := settle.switchToTUI(ctx); err != nil {
		logger.Error("chat start: start the terminal agent after the Claude ACP adapter could not be installed", "error", err)
		return false
	}
	logger.Info("chat start: the Claude ACP adapter could not be installed; the session runs in the terminal", "reason", reason)
	return true
}

func (c *client) fallBackChatStart(ctx context.Context, code string) (bool, error) {
	var response worker.ChatStartFallbackResponse
	if err := c.do(ctx, "/worker/interface/chat-start-fallback", worker.ChatStartFallbackRequest{Code: code}, &response); err != nil {
		return false, err
	}
	return response.MovedToTUI, nil
}
