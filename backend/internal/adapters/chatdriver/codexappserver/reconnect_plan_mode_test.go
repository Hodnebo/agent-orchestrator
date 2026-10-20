package codexappserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCodexReconnectStatesPlanModeOnTheNextTurn(t *testing.T) {
	d, srv := newTestDriver(t)
	srv.replyError("initialize", -32600, "Already initialized")
	proc, err := d.spawn(context.Background(), "codex", "/tmp/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.persistent = true
	d.connectHost = func(_ context.Context, _ persistenthost.Config) (*persistenthost.Transport, error) {
		return &persistenthost.Transport{Stdin: proc.stdin, Stdout: proc.stdout, Reconnected: true, NextRequestID: 41}, nil
	}
	resumed, err := d.Resume(context.Background(), ports.ChatResumeConfig{
		SessionID: "plan-reconnect", ProviderConversationID: "thread-1", DataDir: t.TempDir(), WorkspacePath: t.TempDir(), Model: "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resumed.Close() }()
	if _, err := resumed.SendTurn(context.Background(), ports.ChatUserMessage{Text: "Implement the plan", Settings: ports.ChatTurnSettings{Model: "gpt-test", Plan: false}}); err != nil {
		t.Fatal(err)
	}
	request := srv.awaitFrame(func(f frame) bool { return f.Method == "turn/start" })
	var params struct {
		CollaborationMode *struct {
			Mode string `json:"mode"`
		} `json:"collaborationMode"`
	}
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.CollaborationMode == nil || params.CollaborationMode.Mode != "default" {
		t.Fatalf("Plan=false after reconnect did not clear sticky plan mode: collaborationMode=%s", request.Params)
	}
}
