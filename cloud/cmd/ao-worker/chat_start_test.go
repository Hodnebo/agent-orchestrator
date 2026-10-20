package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/aoagents/agent-orchestrator/cloud/internal/workerexec"
)

func TestStartInterface(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	unavailable := func() error { return workerexec.ErrChatAdapterUnavailable }
	cases := []struct {
		name      string
		launch    worker.LaunchContext
		installed bool
		startable func() error
		moved     bool
		moveErr   error
		want      string
		wantAsked bool
	}{
		{name: "no interface is the terminal", launch: worker.LaunchContext{Harness: "claude-code"}, want: "tui"},
		{name: "terminal stays", launch: worker.LaunchContext{Harness: "claude-code", Interface: "tui"}, installed: true, startable: unavailable, want: "tui"},
		{name: "codex chat needs no adapter", launch: worker.LaunchContext{Harness: "codex", Interface: "chat"}, installed: true, startable: unavailable, want: "chat"},
		{name: "claude chat with the adapter", launch: worker.LaunchContext{Harness: "claude-code", Interface: "chat"}, installed: true, startable: func() error { return nil }, want: "chat"},
		{name: "claude not installed yet", launch: worker.LaunchContext{Harness: "claude-code", Interface: "chat"}, startable: unavailable, want: "chat"},
		{name: "no adapter and no npm moves to the terminal", launch: worker.LaunchContext{Harness: "claude-code", Interface: "chat"}, installed: true, startable: unavailable, moved: true, want: "tui", wantAsked: true},
		{name: "a session switched to chat stays", launch: worker.LaunchContext{Harness: "claude-code", Interface: "chat"}, installed: true, startable: unavailable, want: "chat", wantAsked: true},
		{name: "an older control plane keeps chat", launch: worker.LaunchContext{Harness: "claude-code", Interface: "chat"}, installed: true, startable: unavailable, moveErr: errors.New("404"), want: "chat", wantAsked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asked := false
			got := startInterface(context.Background(), logger, tc.launch, chatStartChecks{
				harnessInstalled: func(string) bool { return tc.installed },
				claudeChatStartable: func() error {
					if tc.startable == nil {
						t.Fatal("adapter checked")
					}
					return tc.startable()
				},
				fallBack: func(_ context.Context, code string) (bool, error) {
					asked = true
					if code != worker.ChatStartAdapterUnavailable {
						t.Errorf("fallback code = %q", code)
					}
					return tc.moved, tc.moveErr
				},
			})
			if got != tc.want || asked != tc.wantAsked {
				t.Fatalf("startInterface = %q (asked control plane %v), want %q (%v)", got, asked, tc.want, tc.wantAsked)
			}
		})
	}
}
