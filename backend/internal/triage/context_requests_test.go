package triage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func contextTurn(t *testing.T, conv *fakeTriageConversation) ports.ChatUserMessage {
	t.Helper()
	select {
	case msg := <-conv.started:
		return msg
	case <-time.After(5 * time.Second):
		t.Fatal("context continuation was not sent")
		return ports.ChatUserMessage{}
	}
}

func TestContextRequestsContinuePrivatelyWithoutPublishingOrLeakingIntoRun(t *testing.T) {
	dir := t.TempDir()
	conv := fakeConversation()
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	deps.ReadContext = func(_ context.Context, origin Origin, request ContextRead) (Evidence, error) {
		if origin.SessionID != "affected" || request.Source != "workspace" || request.Path != "src/a.ts" {
			t.Errorf("wrong context binding: %#v %#v", origin, request)
		}
		return Evidence{Status: "collected", Source: "workspace:src/a.ts", Text: "private implementation detail", Kind: "private-context"}, nil
	}
	c := serveForTest(t, dir, deps)
	command := startCommand("start", false)
	command.Start.Origin.SessionID = "affected"
	c.send(command)
	run := c.response("start").Run
	c.state("running")
	first := contextTurn(t, conv)
	finishWith(conv, `{"read":[{"source":"workspace","path":"src/a.ts"}]}`)
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady}
	second := contextTurn(t, conv)
	if second.AuthoredByUser || !strings.Contains(second.Text, "private implementation detail") || first.ClientMessageID == second.ClientMessageID {
		t.Fatal("private context continuation lost data or reused a dispatch ID")
	}
	finishWith(conv, `{"title":"Blank window","body":"The window stayed blank.","evidence":"Agent-collected evidence: selected context checked."}`)
	c.state("needs_input")
	cancelLiveConversation(t, c, conv, run.ID)
	raw, err := os.ReadFile(filepath.Join(dir, "reports", "triage", "runs", run.ID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private implementation detail") || strings.Contains(string(raw), `\"read\"`) {
		t.Fatalf("private context leaked into saved/public projection: %s", raw)
	}
}

func TestContextRequestsHaveACumulativeLimit(t *testing.T) {
	conv := fakeConversation()
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	var reads atomic.Int32
	deps.ReadContext = func(context.Context, Origin, ContextRead) (Evidence, error) {
		reads.Add(1)
		return Evidence{Status: "unavailable", Text: "missing"}, nil
	}
	c := serveForTest(t, t.TempDir(), deps)
	c.send(startCommand("start", false))
	c.response("start")
	c.state("running")
	contextTurn(t, conv)
	for n := 0; n < maxContextReads; n++ {
		finishWith(conv, `{"read":[{"source":"workspace","path":"a.ts"}]}`)
		conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady}
		contextTurn(t, conv)
	}
	finishWith(conv, `{"read":[{"source":"workspace","path":"a.ts"}]}`)
	run := c.state("failed")
	if reads.Load() != maxContextReads || !strings.Contains(run.Error, "context limit") {
		t.Fatalf("unbounded reads: %d, %#v", reads.Load(), run)
	}
}

func TestContextContinuationWaitsForNativeReadyBarrier(t *testing.T) {
	conv := fakeConversation()
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	deps.ReadContext = func(context.Context, Origin, ContextRead) (Evidence, error) {
		return Evidence{Source: "workspace:a.ts", Kind: "private-context", Status: "collected", Text: "context"}, nil
	}
	c := serveForTest(t, t.TempDir(), deps)
	c.send(startCommand("start", false))
	c.response("start")
	c.state("running")
	contextTurn(t, conv)
	finishWith(conv, `{"read":[{"source":"workspace","path":"a.ts"}]}`)
	c.wait(func(response Response) bool { return response.Run != nil && len(response.Run.Evidence) > 0 })
	select {
	case <-conv.started:
		t.Fatal("next turn started before native Ready")
	default:
	}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady}
	contextTurn(t, conv)
}
