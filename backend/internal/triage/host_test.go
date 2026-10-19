package triage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type hostClient struct {
	t         *testing.T
	input     *io.PipeWriter
	responses chan Response
	done      chan error
	cancel    context.CancelFunc
}

func serveForTest(t *testing.T, dir string, deps Dependencies) *hostClient {
	t.Helper()
	in, writer := io.Pipe()
	reader, out := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	c := &hostClient{t: t, input: writer, responses: make(chan Response, 256), done: make(chan error, 1), cancel: cancel}
	go func() {
		defer close(c.responses)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		for scanner.Scan() {
			var response Response
			if json.Unmarshal(scanner.Bytes(), &response) == nil {
				c.responses <- response
			}
		}
	}()
	go func() {
		err := Serve(ctx, in, out, Options{DataDir: dir, Deps: deps})
		_ = out.Close()
		_ = in.Close()
		c.done <- err
	}()
	t.Cleanup(func() {
		_ = writer.Close()
		cancel()
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
			t.Error("triage host did not stop")
		}
		_ = reader.Close()
	})
	return c
}

func (c *hostClient) send(cmd Command) {
	c.t.Helper()
	if err := json.NewEncoder(c.input).Encode(cmd); err != nil {
		c.t.Fatal(err)
	}
}

func (c *hostClient) wait(predicate func(Response) bool) Response {
	c.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case response, ok := <-c.responses:
			if !ok {
				c.t.Fatal("triage host ended before expected response")
			}
			if response.Version != ProtocolVersion {
				c.t.Fatalf("unexpected response version: %d", response.Version)
			}
			if predicate(response) {
				return response
			}
		case <-deadline:
			c.t.Fatal("timed out waiting for triage response")
		}
	}
}

func (c *hostClient) response(id string) Response {
	return c.wait(func(response Response) bool { return response.ID == id })
}

func (c *hostClient) state(state string) Run {
	return *c.wait(func(response Response) bool { return response.Run != nil && response.Run.State == state }).Run
}

func (c *hostClient) responseAndState(id, state string) (Response, Run) {
	return c.responseAndRun(id, func(run Run) bool { return run.State == state })
}

func (c *hostClient) responseAndRun(id string, matches func(Run) bool) (Response, Run) {
	c.t.Helper()
	var acknowledged *Response
	var settled *Run
	c.wait(func(response Response) bool {
		if response.ID == id {
			if response.Type == "error" {
				c.t.Fatalf("%s failed: %s", id, response.Error)
			}
			acknowledged = &response
		}
		if response.Run != nil && matches(*response.Run) {
			settled = response.Run
		}
		return acknowledged != nil && settled != nil
	})
	return *acknowledged, *settled
}

func TestHostClientRetainsTerminalUpdateBeforeCancelAcknowledgement(t *testing.T) {
	c := &hostClient{t: t, responses: make(chan Response, 2)}
	c.responses <- Response{Version: ProtocolVersion, Type: "run", Run: &Run{State: "interrupted"}}
	c.responses <- Response{Version: ProtocolVersion, ID: "cancel", Type: "run", Run: &Run{State: "starting"}}
	acknowledged, settled := c.responseAndState("cancel", "interrupted")
	if acknowledged.ID != "cancel" || settled.State != "interrupted" {
		t.Fatalf("lost interleaved cancellation result: %#v, %#v", acknowledged, settled)
	}
}

func configuredDeps() Dependencies {
	return Dependencies{Catalog: func(context.Context) ([]Choice, error) {
		return []Choice{{Harness: "test", Available: true,
			Models:   []ModelChoice{{ID: "chosen-model", Efforts: []string{"xhigh"}}},
			Accounts: []Account{{ID: "chosen-account"}}}}, nil
	}}
}

func startCommand(id string, authorized bool) Command {
	return Command{ID: id, Type: "start", Start: &StartRequest{
		Selection:   Selection{Harness: "test", Model: "chosen-model", Effort: "xhigh", AccountID: "chosen-account"},
		Observation: "The session stays blank after opening it.", PublishAuthorized: authorized,
	}}
}

type fakeTriageConversation struct {
	events        chan ports.ChatEvent
	started       chan ports.ChatUserMessage
	terminated    chan struct{}
	terminateOnce sync.Once
	turns         atomic.Int32
	closeCalls    atomic.Int32
	sendHook      func(ports.ChatUserMessage) error
	deferredHook  func(string) error
	ackHook       func(string) error
	resolveHook   func(string, ports.ChatDecision) error
	inputHook     func(string, ports.ChatInputResponse) error
}

func fakeConversation() *fakeTriageConversation {
	return &fakeTriageConversation{events: make(chan ports.ChatEvent, 32), started: make(chan ports.ChatUserMessage, 4), terminated: make(chan struct{})}
}

func (c *fakeTriageConversation) ProviderConversationID() string { return "provider-conversation" }
func (c *fakeTriageConversation) Capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{ports.ChatCapabilityStreaming: true, ports.ChatCapabilityApprovals: true, ports.ChatCapabilityInterrupt: true, ports.ChatCapabilityResume: true}
}
func (c *fakeTriageConversation) SendTurn(_ context.Context, msg ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	if c.sendHook != nil {
		if err := c.sendHook(msg); err != nil {
			return ports.ChatTurnRef{}, err
		}
	}
	turn := c.turns.Add(1)
	c.started <- msg
	return ports.ChatTurnRef{ProviderTurnID: "turn-" + string('0'+turn)}, nil
}
func (c *fakeTriageConversation) Interrupt(context.Context, string) error { return nil }
func (c *fakeTriageConversation) ResolveRequest(_ context.Context, id string, decision ports.ChatDecision) error {
	if c.resolveHook != nil {
		return c.resolveHook(id, decision)
	}
	return nil
}
func (c *fakeTriageConversation) ResolveInput(_ context.Context, id string, response ports.ChatInputResponse) error {
	if c.inputHook != nil {
		return c.inputHook(id, response)
	}
	return nil
}
func (c *fakeTriageConversation) Events() <-chan ports.ChatEvent { return c.events }
func (c *fakeTriageConversation) Close() error                   { c.closeCalls.Add(1); return nil }
func (c *fakeTriageConversation) Terminate() error {
	c.terminateOnce.Do(func() { close(c.terminated) })
	return nil
}
func (c *fakeTriageConversation) StartDeferredTurn(id string) error {
	if c.deferredHook != nil {
		return c.deferredHook(id)
	}
	return nil
}
func (c *fakeTriageConversation) DiscardDeferredTurn(string) {}
func (c *fakeTriageConversation) AcknowledgeProviderEvent(_ context.Context, id string) error {
	if c.ackHook != nil {
		return c.ackHook(id)
	}
	return nil
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for provider signal")
	}
}

func TestHostReadOnlyCommandsNeedNoDaemonConfigOrProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AO_RUN_FILE", filepath.Join(dir, "does-not-exist", "running.json"))
	c := serveForTest(t, dir, Dependencies{})
	for _, kind := range []string{"catalog", "list", "preference"} {
		c.send(Command{ID: kind, Type: kind})
		if response := c.response(kind); response.Type == "error" {
			t.Fatalf("%s failed: %s", kind, response.Error)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "running.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only triage touched daemon run file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only triage created main config: %v", err)
	}
}

func TestHostMalformedAndOversizedCommands(t *testing.T) {
	c := serveForTest(t, t.TempDir(), Dependencies{})
	if _, err := io.WriteString(c.input, "not-json\n{\"id\":\"unknown\",\"type\":\"wat\"}\n{\"id\":\"extra\",\"type\":\"list\",\"secret\":true}\n"); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		response := c.wait(func(response Response) bool { return response.Type == "error" })
		if response.Error == "" {
			t.Fatal("malformed command returned no error")
		}
	}
	c.send(Command{ID: "long-reply", Type: "start", Start: &StartRequest{Observation: strings.Repeat("x", maxReplyBytes+1)}})
	if response := c.response("long-reply"); response.Type != "error" {
		t.Fatal("oversized observation was accepted")
	}
	if _, err := io.WriteString(c.input, strings.Repeat("x", maxCommandBytes+4)+"\n"); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	c.wait(func(response Response) bool {
		return response.Type == "error" && strings.Contains(response.Error, "1 MiB")
	})
}

func TestHostConfigureAndPreferenceUseExactConfiguredCatalog(t *testing.T) {
	deps := configuredDeps()
	configured := make(chan []Choice, 1)
	deps.Configure = func(_ context.Context, choices []Choice) error { configured <- choices; return nil }
	c := serveForTest(t, t.TempDir(), deps)
	c.send(Command{ID: "configure", Type: "configure", Choices: []Choice{{Harness: "test", Models: []ModelChoice{{ID: "chosen-model"}}}}})
	response := c.response("configure")
	if response.Type != "configured" || len(response.Choices) != 1 {
		t.Fatalf("unexpected configure response: %#v", response)
	}
	if choices := <-configured; len(choices) != 1 || choices[0].Harness != "test" {
		t.Fatal("configuration did not reach dependency")
	}
	for _, selection := range []Selection{
		{Harness: "test"},
		{Harness: "test", Model: "unlisted"},
		{Harness: "test", Model: "chosen-model", Effort: "unlisted"},
		{Harness: "test", Model: "chosen-model", AccountID: "unlisted"},
		{Harness: "other", Model: "chosen-model"},
	} {
		c.send(Command{ID: "bad", Type: "preference", Selection: &selection})
		if response := c.response("bad"); response.Type != "error" {
			t.Fatalf("invalid selection accepted: %#v", selection)
		}
	}
	valid := startCommand("", false).Start.Selection
	c.send(Command{ID: "set", Type: "preference", Selection: &valid})
	if response := c.response("set"); response.Type != "selection" || *response.Selection != valid {
		t.Fatalf("selection was changed: %#v", response)
	}
	c.send(Command{ID: "get", Type: "preference"})
	if response := c.response("get"); response.Selection == nil || *response.Selection != valid {
		t.Fatalf("selection was not saved: %#v", response)
	}
}

func TestSelectionConfiguredDefaultMustBeExplicitAndAvailable(t *testing.T) {
	selection := Selection{Harness: "test"}
	choices := []Choice{{Harness: "test", Available: true, Models: []ModelChoice{{ID: "other"}}}}
	if err := validateSelection(selection, choices); err == nil {
		t.Fatal("empty model silently selected a model")
	}
	choices[0].Models = append(choices[0].Models, ModelChoice{ID: "", Label: "Configured default"})
	if err := validateSelection(selection, choices); err != nil {
		t.Fatal(err)
	}
	choices[0].Available = false
	if err := validateSelection(selection, choices); err == nil {
		t.Fatal("unavailable harness accepted")
	}
}

func TestHostDispatchAndAcknowledgementFollowDurableSave(t *testing.T) {
	dir := t.TempDir()
	store, err := openRunStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	conv := fakeConversation()
	deps := configuredDeps()
	deps.Collect = func(context.Context, Origin) ([]Evidence, error) {
		return []Evidence{{ID: "probe", Source: "read-only probe", Status: "observed", Text: "token=ghp_abcdefghijklmnopqrstuvwx"}}, nil
	}
	var runID string
	deps.StartConversation = func(_ context.Context, run Run) (ports.ChatConversation, error) { runID = run.ID; return conv, nil }
	conv.sendHook = func(msg ports.ChatUserMessage) error {
		run, err := store.get(runID)
		if err != nil || run.State != "dispatching" || len(run.Evidence) != 1 {
			return errors.New("provider send preceded durable intent and collection")
		}
		if strings.Contains(msg.Text, "ghp_abcdefghijklmnopqrstuvwx") {
			return errors.New("provider prompt contains an unredacted token")
		}
		return nil
	}
	deferred := make(chan struct{})
	conv.deferredHook = func(turnID string) error {
		run, err := store.get(runID)
		if err != nil || run.ProviderTurnID != turnID || run.State != "running" {
			return errors.New("deferred turn started before durable correlation")
		}
		if turnID == "turn-1" {
			close(deferred)
		}
		return nil
	}
	acked := make(chan struct{})
	conv.ackHook = func(id string) error {
		run, err := store.get(runID)
		if err != nil || len(run.Messages) != 2 || run.Messages[1].Text != "What did you expect?" {
			return errors.New("provider event acknowledged before durable message")
		}
		close(acked)
		return nil
	}
	c := serveForTest(t, dir, deps)
	c.send(startCommand("start", false))
	c.response("start")
	awaitSignal(t, deferred)
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventReasoningDelta, Delta: "private reasoning must never be stored"}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventCommandOutputDelta, Delta: "private tool output must never be stored"}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventMessageDelta, ProviderItemID: "answer", Delta: "What did you expect?", ProviderEventID: "native-event"}
	awaitSignal(t, acked)
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, TurnState: domain.TurnStateCompleted}
	run := c.state("needs_input")
	if len(run.Messages) != 2 || run.PendingInput != nil {
		t.Fatalf("private events entered transcript or ordinary prose became a form: %#v", run)
	}
	raw, _ := json.Marshal(run)
	if strings.Contains(string(raw), "private reasoning") || strings.Contains(string(raw), "private tool") || strings.Contains(string(raw), "ghp_abcdefghijklmnopqrstuvwx") {
		t.Fatal("private content entered saved run")
	}
	c.send(Command{ID: "reply", Type: "reply", RunID: run.ID, Text: "I expected the terminal prompt."})
	if response := c.response("reply"); response.Type != "run" || response.Run.State != "dispatching" {
		t.Fatalf("ordinary reply was not queued: %#v", response)
	}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady}
	if next := c.state("running"); next.ProviderTurnID != "turn-2" {
		t.Fatalf("ordinary reply was not sent after native Ready: %#v", next)
	}
}

func TestHostNativeInputAndWriteApprovalRejection(t *testing.T) {
	conv := fakeConversation()
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	decisions := make(chan ports.ChatDecision, 2)
	conv.resolveHook = func(_ string, decision ports.ChatDecision) error { decisions <- decision; return nil }
	inputs := make(chan ports.ChatInputResponse, 1)
	conv.inputHook = func(_ string, response ports.ChatInputResponse) error { inputs <- response; return nil }
	c := serveForTest(t, t.TempDir(), deps)
	c.send(startCommand("start", false))
	run := *c.response("start").Run
	c.state("running")
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventApprovalRequested, RequestID: "write", Decisions: []ports.ChatDecisionOption{
		{ID: "allow", Kind: ports.ChatDecisionAllowOnce}, {ID: "deny", Kind: ports.ChatDecisionRejectOnce},
	}}
	select {
	case decision := <-decisions:
		if decision.ID != "deny" {
			t.Fatalf("write approval was allowed: %#v", decision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval was not rejected")
	}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventInputRequested, RequestID: "form", Input: &ports.ChatInputRequest{
		Mode: ports.ChatInputModeForm, Message: "Tell me what happened.", Schema: map[string]any{"properties": map[string]any{
			"frequency": map[string]any{"type": "integer", "description": "How many times?"},
		}},
	}}
	pending := c.state("needs_input")
	if pending.PendingInput == nil || pending.PendingInput.ID != "form" {
		t.Fatal("typed user input was not projected")
	}
	c.send(Command{ID: "stale", Type: "reply", RunID: run.ID, RequestID: "old", Answers: map[string][]string{"frequency": {"2"}}})
	if response := c.response("stale"); response.Type != "error" {
		t.Fatal("stale request ID accepted")
	}
	c.send(Command{ID: "answer", Type: "reply", RunID: run.ID, RequestID: "form", Answers: map[string][]string{"frequency": {"2"}}})
	if response := c.response("answer"); response.Type != "run" {
		t.Fatalf("answer failed: %#v", response)
	}
	if response := <-inputs; response.Action != ports.ChatInputActionAccept || response.Content["frequency"] != int64(2) {
		t.Fatalf("input was not typed: %#v", response)
	}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventApprovalRequested, RequestID: "native", Detail: []byte(`{"method":"item/tool/requestUserInput","rawCommand":"private command","questions":[{"id":"where","prompt":"Where did this happen?","options":[{"label":"Board"}]}]}`)}
	pending = c.state("needs_input")
	c.send(Command{ID: "native-answer", Type: "reply", RunID: run.ID, RequestID: pending.PendingInput.ID, Answers: map[string][]string{"where": {"Board"}}})
	if response := c.response("native-answer"); response.Type != "run" {
		t.Fatalf("native answer failed: %#v", response)
	}
	decision := <-decisions
	if string(decision.Raw) != `{"answers":{"where":{"answers":["Board"]}}}` {
		t.Fatalf("incorrect native question answer: %s", decision.Raw)
	}
}

func TestHostOneOwnerAndDuplicateStart(t *testing.T) {
	dir := t.TempDir()
	collecting := make(chan struct{})
	deps := configuredDeps()
	deps.Collect = func(ctx context.Context, _ Origin) ([]Evidence, error) {
		close(collecting)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	first := serveForTest(t, dir, deps)
	first.send(startCommand("first", false))
	run := first.response("first").Run
	awaitSignal(t, collecting)
	first.send(startCommand("again", false))
	if duplicate := first.response("again").Run; duplicate.ID != run.ID {
		t.Fatal("repeated start created a duplicate")
	}
	other := serveForTest(t, dir, configuredDeps())
	other.send(startCommand("other", false))
	if response := other.response("other"); response.Run == nil || response.Run.ID != run.ID {
		t.Fatalf("second owner created another investigation: %#v", response)
	}
	first.send(Command{ID: "cancel", Type: "cancel", RunID: run.ID})
	first.responseAndState("cancel", "interrupted")
}

func TestHostCancellationDuringStartTerminatesLateProvider(t *testing.T) {
	conv := fakeConversation()
	starting := make(chan struct{})
	deps := configuredDeps()
	deps.StartConversation = func(ctx context.Context, _ Run) (ports.ChatConversation, error) {
		close(starting)
		<-ctx.Done()
		return conv, ctx.Err()
	}
	c := serveForTest(t, t.TempDir(), deps)
	c.send(startCommand("start", false))
	run := c.response("start").Run
	awaitSignal(t, starting)
	c.send(Command{ID: "cancel", Type: "cancel", RunID: run.ID})
	c.responseAndState("cancel", "interrupted")
	awaitSignal(t, conv.terminated)
	if conv.turns.Load() != 0 || conv.closeCalls.Load() != 0 {
		t.Fatal("cancelled startup sent a turn or detached instead of terminating")
	}
}

func TestHostEOFStopsOwnedProviderAndKeepsReport(t *testing.T) {
	dir := t.TempDir()
	conv := fakeConversation()
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	c := serveForTest(t, dir, deps)
	c.send(startCommand("start", false))
	run := c.response("start").Run
	c.state("running")
	_ = c.input.Close()
	awaitSignal(t, conv.terminated)
	c.state("interrupted")
	store, _ := openRunStore(dir)
	saved, err := store.get(run.ID)
	if err != nil || saved.State != "interrupted" {
		t.Fatalf("EOF lost run: %#v, %v", saved, err)
	}
	if conv.closeCalls.Load() != 0 {
		t.Fatal("host used detach instead of provider termination")
	}
}

func TestHostCancelCanStopProviderDuringDispatch(t *testing.T) {
	conv := fakeConversation()
	sending := make(chan struct{})
	conv.sendHook = func(ports.ChatUserMessage) error {
		close(sending)
		<-conv.terminated
		return context.Canceled
	}
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	c := serveForTest(t, t.TempDir(), deps)
	c.send(startCommand("start", false))
	run := c.response("start").Run
	awaitSignal(t, sending)
	c.send(Command{ID: "cancel", Type: "cancel", RunID: run.ID})
	c.responseAndState("cancel", "interrupted")
	awaitSignal(t, conv.terminated)
}
