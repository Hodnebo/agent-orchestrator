//go:build darwin || linux

package unrealagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unreallabsai/unreal-agent/harness/inbox"
	"github.com/unreallabsai/unreal-agent/harness/llm"
	"github.com/unreallabsai/unreal-agent/harness/session"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore"
	"github.com/unreallabsai/unreal-agent/harness/sessionstore/localfile"
	"github.com/unreallabsai/unreal-agent/harness/tool"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeProviderClient struct {
	request chan llm.Request
}

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(value []byte) (int, error) { return write(value) }

func (client *fakeProviderClient) Respond(_ context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	client.request <- request
	return llm.Response{
		ID: "response-1",
		Output: []llm.Item{{
			ProviderID: "message-1",
			Type:       llm.ItemMessage,
			Data:       llm.Message{Role: llm.RoleAssistant, Text: "done"},
		}},
		Usage: llm.Usage{InputTokens: 3, OutputTokens: 1},
	}, nil
}

func (*fakeProviderClient) Close() error { return nil }

type controlledProviderClient struct {
	requests  chan llm.Request
	responses chan llm.Response
}

func (client *controlledProviderClient) Respond(ctx context.Context, request llm.Request, _ llm.RequestOptions) (llm.Response, error) {
	select {
	case client.requests <- request:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	select {
	case response := <-client.responses:
		return response, nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

func (*controlledProviderClient) Close() error { return nil }

func startControlledProvider(t *testing.T, cfg providerConfig) (*controlledProviderClient, io.WriteCloser, <-chan frame, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	client := &controlledProviderClient{
		requests: make(chan llm.Request, 1), responses: make(chan llm.Response, 1),
	}
	frames, done := make(chan frame, 64), make(chan error, 1)
	go func() {
		done <- runProvider(ctx, cfg, inputReader, outputWriter, client, "test-model")
		_ = outputWriter.Close()
	}()
	go func() {
		defer close(frames)
		decoder := json.NewDecoder(outputReader)
		for {
			var incoming frame
			if err := decoder.Decode(&incoming); err != nil {
				return
			}
			select {
			case frames <- incoming:
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = inputWriter.Close()
		_ = inputReader.Close()
		_ = outputReader.Close()
		_ = outputWriter.Close()
	})
	waitProviderFrame(t, frames, func(incoming frame) bool { return incoming.Type == "ready" })
	return client, inputWriter, frames, done
}

func waitProviderFrame(t *testing.T, frames <-chan frame, match func(frame) bool) frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		select {
		case incoming, ok := <-frames:
			if !ok {
				t.Fatal("provider stopped before the expected frame")
			}
			if match(incoming) {
				return incoming
			}
		case <-ctx.Done():
			t.Fatal("provider did not emit the expected frame")
		}
	}
}

func nextProviderRequest(t *testing.T, client *controlledProviderClient) llm.Request {
	t.Helper()
	select {
	case request := <-client.requests:
		return request
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not request a model response")
		return llm.Request{}
	}
}

func TestProviderReadOnlyHasNoToolsOrWorkspaceSkillsAfterRestart(t *testing.T) {
	cfg := providerConfig{
		AOSessionID: "ao-session", ProviderConversationID: "provider-session",
		DataDir: t.TempDir(), WorkspacePath: t.TempDir(), ReadOnly: true,
	}
	skillPath := filepath.Join(cfg.WorkspacePath, ".harness", "skills", "private", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("invalid private skill metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath, err := writeProviderConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		cfg, err = readProviderConfig(configPath)
		if err != nil {
			t.Fatal(err)
		}
		client, input, frames, done := startControlledProvider(t, cfg)
		turnID := fmt.Sprintf("turn-%d", attempt)
		if err := json.NewEncoder(input).Encode(command{
			Version: protocolVersion, Type: "turn", RequestID: turnID,
			ProviderTurnID: turnID, MessageID: turnID, Text: "investigate injected evidence",
		}); err != nil {
			t.Fatal(err)
		}
		request := nextProviderRequest(t, client)
		if len(request.Tools) != 0 || !requestContainsUserText(request, "investigate injected evidence") {
			t.Fatalf("read-only model request = %#v", request)
		}
		for _, item := range request.Input {
			if message, ok := item.Data.(llm.Message); ok && strings.Contains(message.Text, "invalid private skill metadata") {
				t.Fatal("read-only model request included workspace skill contents")
			}
		}
		client.responses <- llm.Response{ID: "response-" + turnID, Output: []llm.Item{{
			Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "facts gathered"},
		}}}
		waitProviderFrame(t, frames, func(incoming frame) bool {
			return incoming.Event != nil && incoming.Event.ProviderTurnID == turnID && incoming.Event.ControllerState == ports.ChatControllerReady
		})
		_ = input.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	// The same broken workspace skill still fails mutable mode. Read-only mode
	// succeeds because discovery is skipped, not because its errors are hidden.
	cfg.ReadOnly = false
	if err := runProvider(t.Context(), cfg, strings.NewReader(""), io.Discard, &fakeProviderClient{}, "test-model"); err == nil || !strings.Contains(err.Error(), "discover Unreal Agent skills") {
		t.Fatalf("mutable provider error = %v, want skill discovery failure", err)
	}
}

func TestProviderReadOnlyRejectsUnexpectedModelTools(t *testing.T) {
	for _, name := range []string{tool.BashName, tool.ViewImageName, tool.SkillUseName, "UnexpectedTool"} {
		t.Run(name, func(t *testing.T) {
			cfg := providerConfig{
				AOSessionID: "ao-session", ProviderConversationID: "provider-session",
				DataDir: t.TempDir(), WorkspacePath: t.TempDir(), ReadOnly: true,
			}
			client, input, frames, done := startControlledProvider(t, cfg)
			if err := json.NewEncoder(input).Encode(command{
				Version: protocolVersion, Type: "turn", RequestID: "request-1",
				ProviderTurnID: "turn-1", MessageID: "message-1", Text: "use the supplied facts",
			}); err != nil {
				t.Fatal(err)
			}
			if request := nextProviderRequest(t, client); len(request.Tools) != 0 {
				t.Fatalf("read-only provider offered tools: %v", request.Tools)
			}
			client.responses <- llm.Response{ID: "tool-response", Output: []llm.Item{{
				Type: llm.ItemToolCall, Data: llm.ToolCall{CallID: "unexpected-call", Name: name, Arguments: "{}"},
			}}}
			correction := nextProviderRequest(t, client)
			if len(correction.Tools) != 0 {
				t.Fatalf("correction enabled tools: %v", correction.Tools)
			}
			wantError := fmt.Sprintf("tool %q is not available", name)
			found := false
			for _, item := range correction.Input {
				result, ok := item.Data.(llm.ToolResult)
				if !ok || result.CallID != "unexpected-call" {
					continue
				}
				for _, output := range result.Output {
					if output.Kind == llm.ToolResultText && output.Value == wantError {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("model did not receive unavailable-tool rejection: %#v", correction.Input)
			}
			client.responses <- llm.Response{ID: "corrected-response", Output: []llm.Item{{
				Type: llm.ItemMessage, Data: llm.Message{Role: llm.RoleAssistant, Text: "only injected facts used"},
			}}}
			waitProviderFrame(t, frames, func(incoming frame) bool {
				return incoming.Event != nil && incoming.Event.ControllerState == ports.ChatControllerReady
			})
			_ = input.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			store, err := localfile.New(nativeSessionDirectory(cfg.DataDir))
			if err != nil {
				t.Fatal(err)
			}
			page, err := store.Items(t.Context(), session.ID(cfg.ProviderConversationID), sessionstore.BeforeFirst, 100)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := store.Resume(t.Context(), session.ID(cfg.ProviderConversationID))
			if err != nil || len(restored.Operations) != 0 {
				t.Fatalf("read-only retained operations = %#v, error = %v", restored.Operations, err)
			}
			found = false
			for _, item := range page.Items {
				if status, ok := item.Data.(sessionstore.ToolCallStatus); ok && status.CallID == "unexpected-call" {
					found = true
					if status.Status.Error != wantError || len(status.Operations) != 0 || len(status.Status.WaitingFor) != 0 {
						t.Fatalf("persisted read-only tool rejection = %#v", status)
					}
				}
			}
			if !found {
				t.Fatal("tool rejection was not persisted")
			}
		})
	}
}

func TestProviderRunsPersistentTurnProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	client := &fakeProviderClient{request: make(chan llm.Request, 1)}
	cfg := providerConfig{
		AOSessionID: "ao-session", ProviderConversationID: "provider-session",
		DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
	}
	done := make(chan error, 1)
	go func() {
		done <- runProvider(ctx, cfg, inputReader, outputWriter, client, "test-model")
		_ = outputWriter.Close()
	}()

	decoder := json.NewDecoder(outputReader)
	var incoming frame
	if err := decoder.Decode(&incoming); err != nil || incoming.Type != "ready" {
		t.Fatalf("ready frame = (%#v, %v)", incoming, err)
	}
	if err := json.NewEncoder(inputWriter).Encode(command{
		Version: protocolVersion, Type: "turn", RequestID: "request-1",
		ProviderTurnID: "turn-1", MessageID: "message-1", Text: "hello",
	}); err != nil {
		t.Fatal(err)
	}

	seen := map[ports.ChatEventKind]bool{}
	for !seen[ports.ChatEventTurnCompleted] {
		incoming = frame{}
		if err := decoder.Decode(&incoming); err != nil {
			t.Fatal(err)
		}
		if incoming.Type == "event" && incoming.Event != nil {
			seen[incoming.Event.Kind] = true
			if incoming.EventID == "" {
				t.Fatalf("event %#v has no durable id", incoming.Event)
			}
		}
	}
	for _, kind := range []ports.ChatEventKind{
		ports.ChatEventTurnStarted,
		ports.ChatEventMessageDelta,
		ports.ChatEventMessageCompleted,
		ports.ChatEventUsage,
		ports.ChatEventTurnCompleted,
	} {
		if !seen[kind] {
			t.Errorf("missing %s event", kind)
		}
	}
	select {
	case request := <-client.request:
		if request.Model.ID != "test-model" || !requestContainsUserText(request, "hello") {
			t.Errorf("model request = %#v", request)
		}
	default:
		t.Error("model was not called")
	}
	if err := json.NewEncoder(inputWriter).Encode(command{
		Version: protocolVersion, Type: "turn", RequestID: "request-2",
		ProviderTurnID: "turn-1", MessageID: "message-1", Text: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	for incoming.RequestID != "request-2" {
		incoming = frame{}
		if err := decoder.Decode(&incoming); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case duplicate := <-client.request:
		t.Fatalf("duplicate message called model again: %#v", duplicate)
	case <-time.After(100 * time.Millisecond):
	}

	_ = inputWriter.Close()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(cfg.DataDir, "agent-runtime", string(domain.HarnessUnreal), "sessions", "*"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("persisted Unreal session files = (%v, %v)", matches, err)
	}
}

func requestContainsUserText(request llm.Request, text string) bool {
	for _, item := range request.Input {
		if item.Type == llm.ItemMessage {
			message := item.Data.(llm.Message)
			if message.Role == llm.RoleUser && message.Text == text {
				return true
			}
		}
	}
	return false
}

func TestEventJournalReplaysUntilAcknowledged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	firstOutput := new(bytes.Buffer)
	journal, err := newEventJournal(path, firstOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.emit(wireEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "turn-1"}); err != nil {
		t.Fatal(err)
	}
	var persisted frame
	if err := json.NewDecoder(firstOutput).Decode(&persisted); err != nil {
		t.Fatal(err)
	}

	replayOutput := new(bytes.Buffer)
	restarted, err := newEventJournal(path, replayOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.replay(); err != nil {
		t.Fatal(err)
	}
	var replayed frame
	if err := json.NewDecoder(replayOutput).Decode(&replayed); err != nil || replayed.EventID != persisted.EventID {
		t.Fatalf("replayed frame = (%#v, %v), want event %q", replayed, err, persisted.EventID)
	}
	if err := restarted.ack(replayed.EventID); err != nil {
		t.Fatal(err)
	}

	afterAck := new(bytes.Buffer)
	restarted, err = newEventJournal(path, afterAck)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.replay(); err != nil {
		t.Fatal(err)
	}
	if afterAck.Len() != 0 {
		t.Fatalf("acknowledged event replayed: %s", afterAck.String())
	}
}

func TestEventJournalRunsCompletionBeforePublishingEvent(t *testing.T) {
	completed := false
	journal, err := newEventJournal(filepath.Join(t.TempDir(), "events.json"), writerFunc(func(value []byte) (int, error) {
		if !completed {
			t.Error("event published before durable completion callback")
		}
		return len(value), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.emitAfterPersist(wireEvent{Kind: ports.ChatEventTurnCompleted}, func() error {
		completed = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRecoveryStateIsScopedToProviderConversation(t *testing.T) {
	dataDir := t.TempDir()
	firstConfig := providerConfig{
		AOSessionID: "ao-session", ProviderConversationID: "provider-one",
		DataDir: dataDir, WorkspacePath: t.TempDir(),
	}
	secondConfig := firstConfig
	secondConfig.ProviderConversationID = "provider-two"

	firstRuntime := &providerRuntime{
		cfg: firstConfig,
		active: activeTurnState{
			ProviderTurnID: "stale-turn", MessageID: "stale-message",
		},
	}
	if err := firstRuntime.writeActiveTurn(); err != nil {
		t.Fatal(err)
	}
	secondRuntime := &providerRuntime{cfg: secondConfig}
	active, err := secondRuntime.readActiveTurn()
	if err != nil {
		t.Fatal(err)
	}
	if active.ProviderTurnID != "" || active.MessageID != "" {
		t.Fatalf("new provider conversation inherited active turn %#v", active)
	}

	firstJournal, err := newEventJournal(providerStatePath(firstConfig, "journals"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstJournal.emit(wireEvent{Kind: ports.ChatEventTurnStarted, ProviderTurnID: "stale-turn"}); err != nil {
		t.Fatal(err)
	}
	secondOutput := new(bytes.Buffer)
	secondJournal, err := newEventJournal(providerStatePath(secondConfig, "journals"), secondOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondJournal.replay(); err != nil {
		t.Fatal(err)
	}
	if secondOutput.Len() != 0 {
		t.Fatalf("new provider conversation replayed stale frames: %s", secondOutput.String())
	}
}

func TestInterruptPreservesCoordinatorFailureForProviderLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inputs, err := inbox.New(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	coordinatorErr := errors.New("coordinator failed")
	done := make(chan error, 1)
	done <- coordinatorErr
	runtime := &providerRuntime{
		ctx: ctx,
		active: activeTurnState{
			ProviderTurnID: "turn-1", MessageID: "message-1",
		},
		inputs: inputs,
		done:   done,
	}

	if err := runtime.interrupt("turn-1"); !errors.Is(err, coordinatorErr) {
		t.Fatalf("interrupt error = %v, want %v", err, coordinatorErr)
	}
	select {
	case got := <-done:
		if !errors.Is(got, coordinatorErr) {
			t.Fatalf("preserved coordinator error = %v, want %v", got, coordinatorErr)
		}
	default:
		t.Fatal("interrupt consumed the coordinator failure")
	}
}

func TestProviderUsageSurvivesRestart(t *testing.T) {
	cfg := providerConfig{
		AOSessionID: "ao-session", ProviderConversationID: "provider-session",
		DataDir: t.TempDir(), WorkspacePath: t.TempDir(),
	}
	first := &providerRuntime{cfg: cfg}
	usage, err := first.recordUsage(llm.Usage{InputTokens: 3, OutputTokens: 2, CachedInputTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 3 || usage.OutputTokens != 2 || usage.CachedTokens != 1 || usage.TotalTokens != 5 {
		t.Fatalf("first usage = %#v", usage)
	}

	restarted := &providerRuntime{cfg: cfg}
	restarted.usage, err = restarted.readUsage()
	if err != nil {
		t.Fatal(err)
	}
	usage, err = restarted.recordUsage(llm.Usage{InputTokens: 4, OutputTokens: 1, CachedInputTokens: 2})
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 3 || usage.CachedTokens != 3 || usage.TotalTokens != 10 || !usage.TotalsKnown {
		t.Fatalf("restarted cumulative usage = %#v", usage)
	}
}
