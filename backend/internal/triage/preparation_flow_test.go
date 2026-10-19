package triage

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPreparationFlowCollectsBeforeStartAndReusesPendingRun(t *testing.T) {
	origin := Origin{SessionID: "affected", Surface: "session", AppVersion: "original-build"}
	collecting := make(chan Origin, 1)
	finishCollection := make(chan struct{})
	providerStarts := make(chan Run, 1)
	var collections atomic.Int32
	conv := fakeConversation()
	deps := configuredDeps()
	deps.Collect = func(ctx context.Context, selected Origin) ([]Evidence, error) {
		collections.Add(1)
		collecting <- selected
		select {
		case <-finishCollection:
			return []Evidence{{ID: "prepared-snapshot", Source: "preparation collector", Status: "collected", Text: "evidence collected before provider start"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	deps.StartConversation = func(_ context.Context, run Run) (ports.ChatConversation, error) {
		providerStarts <- run
		return conv, nil
	}
	c := serveForTest(t, t.TempDir(), deps)
	c.send(Command{ID: "prepare", Type: "prepare", Origin: &origin})
	prepared := c.response("prepare")
	if prepared.Type != "run" || prepared.Run == nil || !prepared.Run.Preparation || prepared.Run.State != "collecting" {
		t.Fatalf("prepare did not return a collecting preparation: %#v", prepared)
	}
	select {
	case selected := <-collecting:
		if !reflect.DeepEqual(selected, origin) {
			t.Fatalf("collection used a different origin: %#v", selected)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prepare did not start collection")
	}
	select {
	case run := <-providerStarts:
		t.Fatalf("prepare started a provider before Start: %#v", run)
	default:
	}
	start := startCommand("activate", false)
	start.Start.PreparationID = prepared.Run.ID
	start.Start.Origin = origin
	start.Start.Origin.AppVersion = "later-desktop-snapshot"
	c.send(start)
	activated := c.response("activate")
	if activated.Type != "run" || activated.Run.ID != prepared.Run.ID || activated.Run.Preparation || activated.Run.State != "collecting" {
		t.Fatalf("Start replaced or waited synchronously for preparation: %#v", activated)
	}
	start.ID = "duplicate-start"
	c.send(start)
	if duplicate := c.response("duplicate-start"); duplicate.Run == nil || duplicate.Run.ID != prepared.Run.ID {
		t.Fatalf("repeated Start created another run: %#v", duplicate)
	}
	c.send(Command{ID: "collection-barrier", Type: "get", RunID: prepared.Run.ID})
	if current := c.response("collection-barrier"); current.Run == nil || current.Run.State != "collecting" {
		t.Fatalf("host could not serve requests during collection: %#v", current)
	}
	select {
	case run := <-providerStarts:
		t.Fatalf("provider started while preparation collection was pending: %#v", run)
	default:
	}
	close(finishCollection)
	select {
	case run := <-providerStarts:
		if run.ID != prepared.Run.ID || run.Preparation || !reflect.DeepEqual(run.Request.Origin, origin) || len(run.Evidence) != 1 || run.Evidence[0].ID != "prepared-snapshot" {
			t.Fatalf("provider did not receive the prepared run and frozen evidence: %#v", run)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not continue after collection finished")
	}
	if msg := contextTurn(t, conv); !strings.Contains(msg.Text, "evidence collected before provider start") {
		t.Fatal("first native turn omitted prepared evidence")
	}
	c.state("running")
	if collections.Load() != 1 || conv.turns.Load() != 1 {
		t.Fatalf("activation repeated collection or initial dispatch: collections=%d turns=%d", collections.Load(), conv.turns.Load())
	}
}

func TestPreparationFlowCancelBeforeStartReleasesOwnership(t *testing.T) {
	dir := t.TempDir()
	collecting, cancelled := make(chan struct{}), make(chan struct{})
	var collections, providerStarts atomic.Int32
	deps := configuredDeps()
	deps.Collect = func(ctx context.Context, _ Origin) ([]Evidence, error) {
		if collections.Add(1) == 1 {
			close(collecting)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		}
		return []Evidence{}, nil
	}
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) {
		providerStarts.Add(1)
		return fakeConversation(), nil
	}
	c := serveForTest(t, dir, deps)
	c.send(Command{ID: "prepare", Type: "prepare"})
	prepared := c.response("prepare")
	if prepared.Run == nil {
		t.Fatalf("prepare failed: %#v", prepared)
	}
	awaitSignal(t, collecting)
	// The removal event is a completion barrier for ownership release. A saved
	// cancelled update is emitted just before release and is insufficient alone.
	lockDir := filepath.Join(dir, "reports", "triage", "active.lock")
	entries, err := os.ReadDir(lockDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("preparation did not own exactly one active claim: %#v err=%v", entries, err)
	}
	claimPath := filepath.Join(lockDir, entries[0].Name())
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watcher.Close() })
	if err := watcher.Add(lockDir); err != nil {
		t.Fatal(err)
	}
	c.send(Command{ID: "cancel-preparation", Type: "cancel", RunID: prepared.Run.ID})
	c.responseAndState("cancel-preparation", "cancelled")
	awaitSignal(t, cancelled)
	deadline := time.After(5 * time.Second)
	released := false
	for !released {
		select {
		case event := <-watcher.Events:
			released = event.Name == claimPath && event.Has(fsnotify.Remove)
		case err := <-watcher.Errors:
			t.Fatalf("watch preparation ownership: %v", err)
		case <-deadline:
			t.Fatal("cancelled preparation retained run ownership")
		}
	}
	replacement := serveForTest(t, dir, deps)
	replacement.send(Command{ID: "prepare-again", Type: "prepare"})
	again := replacement.response("prepare-again")
	if again.Type != "run" || again.Run == nil || again.Run.ID == prepared.Run.ID || !again.Run.Preparation {
		t.Fatalf("cancellation did not release ownership for a fresh preparation: %#v", again)
	}
	replacement.state("prepared")
	if providerStarts.Load() != 0 {
		t.Fatal("a preflight cancellation or replacement started a provider")
	}
}

func TestPreparationFlowReopenWaitsForCancelledCollectionCleanup(t *testing.T) {
	collecting, cancelled, finishCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var collections atomic.Int32
	deps := configuredDeps()
	deps.Collect = func(ctx context.Context, _ Origin) ([]Evidence, error) {
		if collections.Add(1) == 1 {
			close(collecting)
			<-ctx.Done()
			close(cancelled)
			<-finishCleanup
			return nil, ctx.Err()
		}
		return nil, nil
	}
	c := serveForTest(t, t.TempDir(), deps)
	c.send(Command{ID: "prepare", Type: "prepare"})
	first := c.response("prepare")
	awaitSignal(t, collecting)
	c.send(Command{ID: "cancel", Type: "cancel", RunID: first.Run.ID})
	c.response("cancel")
	awaitSignal(t, cancelled)
	c.send(Command{ID: "reopen", Type: "prepare"})
	close(finishCleanup)
	reopened := c.response("reopen")
	if reopened.Type != "run" || reopened.Run == nil || reopened.Run.ID == first.Run.ID || !reopened.Run.Preparation {
		t.Fatalf("reopen adopted a cancelled preparation: %#v", reopened)
	}
	c.state("prepared")
}

func TestPreparationFlowRejectsMismatchedOriginAndPreparationID(t *testing.T) {
	origin := Origin{SessionID: "affected", HostID: "original-host", Surface: "session"}
	collecting, finishCollection := make(chan struct{}), make(chan struct{})
	var providerStarts atomic.Int32
	conv := fakeConversation()
	deps := configuredDeps()
	deps.Collect = func(ctx context.Context, _ Origin) ([]Evidence, error) {
		close(collecting)
		select {
		case <-finishCollection:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) {
		providerStarts.Add(1)
		return conv, nil
	}
	c := serveForTest(t, t.TempDir(), deps)
	c.send(Command{ID: "prepare", Type: "prepare", Origin: &origin})
	prepared := c.response("prepare")
	if prepared.Run == nil {
		t.Fatalf("prepare failed: %#v", prepared)
	}
	awaitSignal(t, collecting)
	for _, mismatch := range []struct {
		name   string
		change func(*StartRequest)
	}{
		{"preparation-id", func(request *StartRequest) { request.PreparationID = "different-preparation" }},
		{"session-id", func(request *StartRequest) { request.Origin.SessionID = "different-session" }},
		{"host-id", func(request *StartRequest) { request.Origin.HostID = "different-host" }},
		{"surface", func(request *StartRequest) { request.Origin.Surface = "settings" }},
	} {
		command := startCommand(mismatch.name, false)
		command.Start.PreparationID, command.Start.Origin = prepared.Run.ID, origin
		mismatch.change(command.Start)
		c.send(command)
		if rejected := c.response(mismatch.name); rejected.Type != "error" {
			t.Fatalf("mismatched %s was accepted: %#v", mismatch.name, rejected)
		}
	}
	c.send(Command{ID: "unchanged", Type: "get", RunID: prepared.Run.ID})
	unchanged := c.response("unchanged")
	if unchanged.Run == nil || !unchanged.Run.Preparation || !reflect.DeepEqual(unchanged.Run.Request.Origin, origin) || providerStarts.Load() != 0 {
		t.Fatalf("a rejected activation mutated the preparation: %#v", unchanged)
	}
	close(finishCollection)
	c.state("prepared")
	valid := startCommand("valid-start", false)
	valid.Start.PreparationID, valid.Start.Origin = prepared.Run.ID, origin
	c.send(valid)
	if started := c.response("valid-start"); started.Run == nil || started.Run.ID != prepared.Run.ID || started.Run.Preparation {
		t.Fatalf("rejected requests consumed the valid preparation: %#v", started)
	}
	contextTurn(t, conv)
	if providerStarts.Load() != 1 {
		t.Fatalf("valid preparation started %d providers", providerStarts.Load())
	}
}

func TestPreparationFlowOptionAnswerNeedsNoTextAndWaitsForReady(t *testing.T) {
	conv := fakeConversation()
	var nativeResponses atomic.Int32
	conv.resolveHook = func(string, ports.ChatDecision) error { nativeResponses.Add(1); return nil }
	conv.inputHook = func(string, ports.ChatInputResponse) error { nativeResponses.Add(1); return nil }
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	c := serveForTest(t, t.TempDir(), deps)
	c.send(startCommand("start", false))
	started := c.response("start")
	if started.Run == nil {
		t.Fatalf("start failed: %#v", started)
	}
	c.state("running")
	contextTurn(t, conv)
	finishWith(conv, `{"questions":[{"id":"symptom","prompt":"Which problem did you see?","options":["Terminal blank","Session missing"]}]}`)
	waiting := c.state("needs_input")
	if waiting.PendingInput == nil || waiting.PendingInput.Origin != "triage" || len(waiting.PendingInput.Questions) != 1 {
		t.Fatalf("final question JSON was not projected as engine input: %#v", waiting)
	}
	question := waiting.PendingInput.Questions[0]
	if !question.AllowText || !reflect.DeepEqual(question.Options, []string{"Terminal blank", "Session missing", questionOther, questionUnsure}) {
		t.Fatalf("option-first question omitted selections or escape choices: %#v", question)
	}
	c.send(Command{ID: "invalid-option", Type: "reply", RunID: waiting.ID, RequestID: waiting.PendingInput.ID, Answers: map[string][]string{"symptom": {"Unoffered choice"}}})
	if rejected := c.response("invalid-option"); rejected.Type != "error" {
		t.Fatalf("unoffered option was accepted: %#v", rejected)
	}
	c.send(Command{ID: "select-option", Type: "reply", RunID: waiting.ID, RequestID: waiting.PendingInput.ID, Answers: map[string][]string{"symptom": {"Terminal blank"}}})
	selected := c.response("select-option")
	if selected.Type != "run" || selected.Run == nil || selected.Run.State != "dispatching" || selected.Run.PendingInput != nil {
		t.Fatalf("selection-only reply was not accepted and queued: %#v", selected)
	}
	select {
	case message := <-conv.started:
		t.Fatalf("answer dispatched before native readiness: %#v", message)
	default:
	}
	if conv.turns.Load() != 1 || nativeResponses.Load() != 0 {
		t.Fatal("engine selection sent a turn or native request response before Ready")
	}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady, ProviderTurnID: "turn-1", ProviderConversationID: conv.ProviderConversationID()}
	answer := contextTurn(t, conv)
	if !answer.AuthoredByUser || !strings.Contains(answer.Text, "Which problem did you see?\nSelected: Terminal blank") || strings.Contains(answer.Text, "Optional human details:") {
		t.Fatalf("selection was not delivered as human input without required typing: %#v", answer)
	}
	c.wait(func(response Response) bool {
		return response.Run != nil && response.Run.State == "running" && response.Run.ProviderTurnID == "turn-2"
	})
	if conv.turns.Load() != 2 || nativeResponses.Load() != 0 {
		t.Fatalf("selection dispatched incorrectly: turns=%d native responses=%d", conv.turns.Load(), nativeResponses.Load())
	}
}
