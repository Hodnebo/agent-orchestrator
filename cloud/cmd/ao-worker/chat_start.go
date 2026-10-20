package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workertransport"
)

// chatStartChecks are what startInterface asks of the workspace and the
// control plane; tests replace them.
type chatStartChecks struct {
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
// known to be impossible.
func startInterface(ctx context.Context, logger *slog.Logger, launch worker.LaunchContext, checks chatStartChecks) string {
	committed := strings.TrimSpace(launch.Interface)
	if committed == "" {
		return workertransport.InterfaceTUI
	}
	if committed != workertransport.InterfaceChat || launch.Harness != "claude-code" ||
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

func (c *client) fallBackChatStart(ctx context.Context, code string) (bool, error) {
	var response worker.ChatStartFallbackResponse
	if err := c.do(ctx, "/worker/interface/chat-start-fallback", worker.ChatStartFallbackRequest{Code: code}, &response); err != nil {
		return false, err
	}
	return response.MovedToTUI, nil
}
