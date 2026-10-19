package triage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/amp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty/ptyregistry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Exercise the real hidden PTY host with a controlled fake executable, never a
// user's installed provider or account.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "pty-host":
			os.Exit(conpty.RunHost(os.Args[2:], os.Stdout))
		case "triage-fake-harness":
			os.Exit(runFakeHarness())
		}
	}
	os.Exit(m.Run())
}

func runFakeHarness() int {
	var request harnessRequest
	if readSmallJSON(os.Getenv("AO_TRIAGE_REQUEST_FILE"), harnessRequestLimit, &request) != nil {
		return 2
	}
	receipt := map[string]any{"args": os.Args[2:], "cwd": filepath.Dir(os.Getenv("AO_TRIAGE_REQUEST_FILE")), "account": os.Getenv("FAKE_SELECTED_ACCOUNT"), "github": os.Getenv("GH_TOKEN"), "delegation": os.Getenv("AO_MCP_TOKEN"), "messages": request.Messages}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return 3
	}
	if err := hookutil.AtomicWriteFile(filepath.Join(filepath.Dir(os.Getenv("AO_TRIAGE_REQUEST_FILE")), request.DispatchID+"-receipt.json"), raw, 0o600); err != nil {
		return 3
	}
	if os.Getenv("FAKE_TRIAGE_AFTER_START") == "1" {
		fmt.Print("Type a message\n")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			return 4
		}
	}
	if os.Getenv("FAKE_TRIAGE_NATIVE_INPUT") == "1" {
		fmt.Print("\x1b[2J\x1b[HNATIVE_CONFIRM: Type the exact observation or press Enter to continue\n")
		text, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return 5
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(os.Getenv("AO_TRIAGE_REQUEST_FILE")), "native-input.txt"), []byte(text), 0o600); err != nil {
			return 6
		}
		fmt.Print("\x1b[2J\x1b[HWORKING\n")
	}
	if os.Getenv("FAKE_TRIAGE_WAIT") == "1" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	}
	response := harnessResponse{Version: 1, RunID: request.RunID, DispatchID: request.DispatchID, Nonce: request.Nonce}
	if os.Getenv("FAKE_TRIAGE_BAD_ID") == "1" {
		response.DispatchID = "other-dispatch"
	}
	switch request.DispatchID {
	case "dispatch-1":
		response.Text = `{"read":{"source":"selected-workspace","path":"main.go"}}`
	case "dispatch-2":
		response.Text = `{"question":"Which symptom did you see?","options":["Hang","Crash"]}`
	default:
		response.Text = `{"draft":{"title":"Observed outage","body":"The user observed a crash."}}`
	}
	if err := atomicPrivateJSON(os.Getenv("AO_TRIAGE_RESULT_FILE"), response); err != nil {
		return 7
	}
	// The provider remains alive after writing its result. The transport must
	// terminate this exact child before reporting Completed and Ready.
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

type fakeHarnessAgent struct {
	ports.Agent
	afterStart bool
}

func (a fakeHarnessAgent) GetLaunchCommand(_ context.Context, cfg ports.LaunchConfig) ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return []string{exe, "triage-fake-harness", "--model", cfg.Config.Model, "--effort", cfg.Config.Effort, "--permissions", string(cfg.Permissions), "--mode", cfg.Config.Mode}, nil
}
func (a fakeHarnessAgent) GetPromptDeliveryStrategy(context.Context, ports.LaunchConfig) (ports.PromptDeliveryStrategy, error) {
	if a.afterStart {
		return ports.PromptDeliveryAfterStart, nil
	}
	return ports.PromptDeliveryInCommand, nil
}
func (fakeHarnessAgent) DetectTerminalActivity(output string) (domain.ActivityState, bool) {
	if strings.Contains(output, "NATIVE_CONFIRM:") {
		return domain.ActivityWaitingInput, true
	}
	return domain.ActivityActive, true
}

func artifactFixture(t *testing.T, agent ports.Agent, extra map[string]string) (*artifactConversation, context.Context) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native detached PTY test requires macOS or Linux")
	}
	dataDir := t.TempDir()
	run := Run{ID: "artifact-run", Request: StartRequest{Selection: Selection{Harness: "fixture", Model: "configured-model", Effort: "xhigh", Permissions: ports.PermissionModeAuto}}}
	workspace, _, err := diagnosticWorkspace(dataDir, run)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	runtime, err := newHarnessRuntime(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ptyregistry.SetRunFilePath("") })
	env := diagnosticEnv(os.Environ(), map[string]string{"FAKE_SELECTED_ACCOUNT": "exact-configured-account", "GH_TOKEN": "", "AO_MCP_TOKEN": ""})
	for key, value := range extra {
		env[key] = value
	}
	conv := newArtifactConversation(ctx, run, workspace, filepath.Join(dataDir, "provider-state"), diagnosticInstructions(), env, agent, runtime)
	t.Cleanup(func() { _ = conv.Close() })
	return conv, ctx
}

func completeArtifactTurn(ctx context.Context, t *testing.T, conv *artifactConversation, text string) string {
	t.Helper()
	ref, err := conv.SendTurn(ctx, ports.ChatUserMessage{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if err := conv.StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatal(err)
	}
	var result string
	completed := false
	for {
		select {
		case <-ctx.Done():
			conv.mu.Lock()
			handle := conv.handle
			conv.mu.Unlock()
			output, _ := conv.runtime.GetOutput(context.Background(), handle, 40)
			entries, _ := os.ReadDir(conv.workspace)
			t.Fatalf("%v; terminal %q; artifacts %v", ctx.Err(), output, entries)
		case event := <-conv.Events():
			if event.Kind == ports.ChatEventError {
				t.Fatal(event.Err)
			}
			if event.Kind == ports.ChatEventMessageCompleted {
				result = event.Text
			}
			if event.Kind == ports.ChatEventTurnCompleted {
				completed = true
			}
			if event.Kind == ports.ChatEventControllerState && event.ControllerState == ports.ChatControllerReady {
				if !completed || result == "" {
					t.Fatal("ready preceded the completed artifact")
				}
				entries, complete, err := ptyregistry.Scan(ctx)
				if err != nil || !complete || len(entries) != 0 {
					t.Fatalf("provider still owned when Ready emitted: %#v %v", entries, err)
				}
				return result
			}
		}
	}
}

func TestArtifactHarnessRoundTripKeepsContextAndExactSettings(t *testing.T) {
	for _, afterStart := range []bool{false, true} {
		t.Run(fmt.Sprint(afterStart), func(t *testing.T) {
			extra := map[string]string{}
			if afterStart {
				extra["FAKE_TRIAGE_AFTER_START"] = "1"
			}
			conv, ctx := artifactFixture(t, fakeHarnessAgent{afterStart: afterStart}, extra)
			if got := completeArtifactTurn(ctx, t, conv, "Read the supplied context"); !strings.Contains(got, `"read"`) {
				t.Fatal(got)
			}
			if got := completeArtifactTurn(ctx, t, conv, "Private selected file chunk"); !strings.Contains(got, `"question"`) {
				t.Fatal(got)
			}
			if got := completeArtifactTurn(ctx, t, conv, "Crash"); !strings.Contains(got, `"draft"`) {
				t.Fatal(got)
			}
			var receipt struct {
				Args       []string         `json:"args"`
				Account    string           `json:"account"`
				Github     string           `json:"github"`
				Delegation string           `json:"delegation"`
				Messages   []harnessMessage `json:"messages"`
			}
			if err := readSmallJSON(filepath.Join(conv.workspace, "dispatch-3-receipt.json"), harnessArtifactLimit, &receipt); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(receipt.Args, []string{"--model", "configured-model", "--effort", "xhigh", "--permissions", "auto", "--mode", ""}) || receipt.Account != "exact-configured-account" || receipt.Github != "" || receipt.Delegation != "" {
				t.Fatalf("selected launch changed: %#v", receipt)
			}
			if len(receipt.Messages) != 5 || receipt.Messages[0].Text != "Read the supplied context" || receipt.Messages[2].Text != "Private selected file chunk" || receipt.Messages[4].Text != "Crash" {
				t.Fatalf("conversation context lost: %#v", receipt.Messages)
			}
			for _, entry := range []string{"dispatch-1-request.json", "dispatch-2-request.json", "dispatch-3-request.json"} {
				info, err := os.Stat(filepath.Join(conv.workspace, entry))
				if err != nil || info.Mode().Perm()&0o077 != 0 {
					t.Fatalf("request is not private: %v", err)
				}
			}
		})
	}
}

func TestArtifactHarnessUsesRegisteredAmpModeWithoutShellReconstruction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture shim is a POSIX shell script")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	shim := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' triage-fake-harness \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "amp"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	conv, ctx := artifactFixture(t, amp.New(), map[string]string{"FAKE_TRIAGE_AFTER_START": "1"})
	conv.selection = Selection{Harness: "amp", Mode: "high", Permissions: ports.PermissionModeDefault}
	completeArtifactTurn(ctx, t, conv, "Investigate")
	var receipt struct {
		Args []string `json:"args"`
	}
	if err := readSmallJSON(filepath.Join(conv.workspace, "dispatch-1-receipt.json"), harnessArtifactLimit, &receipt); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(receipt.Args, []string{"--mode", "high"}) {
		t.Fatalf("registered Amp arguments = %q", receipt.Args)
	}
}

func TestArtifactHarnessForwardsOnlyExplicitNativeInput(t *testing.T) {
	conv, ctx := artifactFixture(t, fakeHarnessAgent{}, map[string]string{"FAKE_TRIAGE_NATIVE_INPUT": "1"})
	ref, err := conv.SendTurn(ctx, ports.ChatUserMessage{Text: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conv.StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case event := <-conv.Events():
			if event.Kind == ports.ChatEventError {
				t.Fatal(event.Err)
			}
			if event.Kind != ports.ChatEventInputRequested {
				continue
			}
			if _, err := os.Stat(filepath.Join(conv.workspace, "native-input.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("native prompt was answered automatically")
			}
			if err := conv.ResolveInput(ctx, event.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: map[string]any{"action": "Approve"}}); !errors.Is(err, ports.ErrChatDecisionNotOffered) {
				t.Fatalf("invented approval accepted: %v", err)
			}
			if err := conv.ResolveInput(ctx, event.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: map[string]any{"action": "Send typed text", "text": "my exact input"}}); err != nil {
				t.Fatal(err)
			}
			for {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case final := <-conv.Events():
					if final.Kind == ports.ChatEventError {
						t.Fatal(final.Err)
					}
					if final.Kind == ports.ChatEventControllerState {
						raw, err := os.ReadFile(filepath.Join(conv.workspace, "native-input.txt"))
						if err != nil || strings.TrimSpace(string(raw)) != "my exact input" {
							t.Fatalf("forwarded native input = %q %v", raw, err)
						}
						return
					}
				}
			}
		}
	}
}

func TestArtifactHarnessCancellationStopsOwnedPTY(t *testing.T) {
	conv, ctx := artifactFixture(t, fakeHarnessAgent{}, map[string]string{"FAKE_TRIAGE_WAIT": "1"})
	ref, err := conv.SendTurn(ctx, ports.ChatUserMessage{Text: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conv.StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case event := <-conv.Events():
			if event.Kind == ports.ChatEventError {
				t.Fatal(event.Err)
			}
			if event.Kind == ports.ChatEventActivityStarted {
				if err := conv.Interrupt(ctx, ref.ProviderTurnID); err != nil {
					t.Fatal(err)
				}
				if err := conv.Close(); err != nil {
					t.Fatal(err)
				}
				entries, complete, err := ptyregistry.Scan(ctx)
				if err != nil || !complete || len(entries) != 0 {
					t.Fatalf("cancel left an owned provider: %#v %v", entries, err)
				}
				return
			}
		}
	}
}

func TestArtifactHarnessRejectsMismatchedResultIdentity(t *testing.T) {
	conv, ctx := artifactFixture(t, fakeHarnessAgent{}, map[string]string{"FAKE_TRIAGE_BAD_ID": "1"})
	ref, err := conv.SendTurn(ctx, ports.ChatUserMessage{Text: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conv.StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case event := <-conv.Events():
			if event.Kind == ports.ChatEventError {
				if !strings.Contains(event.Err.Error(), "identity") {
					t.Fatal(event.Err)
				}
				return
			}
			if event.Kind == ports.ChatEventTurnCompleted {
				t.Fatal("unrelated response completed this dispatch")
			}
		}
	}
}

func TestArtifactHarnessAcceptsBoundedContextLargerThanResponseLimit(t *testing.T) {
	conv, ctx := artifactFixture(t, fakeHarnessAgent{}, nil)
	text := strings.Repeat("private-context ", 24<<10)
	if got := completeArtifactTurn(ctx, t, conv, text); !strings.Contains(got, `"read"`) {
		t.Fatal(got)
	}
	info, err := os.Stat(filepath.Join(conv.workspace, "dispatch-1-request.json"))
	if err != nil || info.Size() <= harnessArtifactLimit || info.Size() > harnessRequestLimit {
		t.Fatalf("legitimate private context was not preserved: %v %v", info, err)
	}
}

type noDetectorHarness struct{ ports.Agent }

func TestArtifactHarnessNativeControlsWorkWithoutActivityDetector(t *testing.T) {
	conv, ctx := artifactFixture(t, noDetectorHarness{Agent: fakeHarnessAgent{}}, map[string]string{"FAKE_TRIAGE_NATIVE_INPUT": "1"})
	ref, err := conv.SendTurn(ctx, ports.ChatUserMessage{Text: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conv.StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case event := <-conv.Events():
			if event.Kind == ports.ChatEventError {
				t.Fatal(event.Err)
			}
			if event.Kind != ports.ChatEventActivityStarted {
				continue
			}
			if err := conv.RequestNativeInput(ctx); err != nil {
				t.Fatal(err)
			}
			if err := conv.RequestNativeInput(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case request := <-conv.Events():
				if request.Kind != ports.ChatEventInputRequested {
					t.Fatalf("manual native control = %#v", request)
				}
				select {
				case duplicate := <-conv.Events():
					t.Fatalf("duplicate native request: %#v", duplicate)
				default:
				}
				if err := conv.ResolveInput(ctx, request.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: map[string]any{"action": "Keep waiting"}}); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(conv.workspace, "native-input.txt")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("opening controls sent native input")
				}
				if err := conv.RequestNativeInput(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case next := <-conv.Events():
					if err := conv.ResolveInput(ctx, next.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: map[string]any{"action": "Send typed text", "text": "explicit native input"}}); err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				for {
					select {
					case final := <-conv.Events():
						if final.Kind == ports.ChatEventError {
							t.Fatal(final.Err)
						}
						if final.Kind == ports.ChatEventControllerState {
							return
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
}

type staleNativeInputRuntime struct {
	mu                 sync.Mutex
	workspace          string
	request            harnessRequest
	publishAfterAnswer bool
	answered           bool
	inputs             int
	destroyed          bool
}

func (r *staleNativeInputRuntime) Create(_ context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workspace = cfg.WorkspacePath
	if err := readSmallJSON(cfg.Env["AO_TRIAGE_REQUEST_FILE"], harnessRequestLimit, &r.request); err != nil {
		return ports.RuntimeHandle{}, err
	}
	return ports.RuntimeHandle{ID: string(cfg.SessionID)}, nil
}
func (r *staleNativeInputRuntime) Destroy(context.Context, ports.RuntimeHandle) error {
	r.mu.Lock()
	r.destroyed = true
	r.mu.Unlock()
	return nil
}
func (*staleNativeInputRuntime) IsAlive(context.Context, ports.RuntimeHandle) (bool, error) {
	return true, nil
}
func (r *staleNativeInputRuntime) GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	return r.GetStyledOutput(ctx, handle, lines)
}
func (r *staleNativeInputRuntime) GetStyledOutput(context.Context, ports.RuntimeHandle, int) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.publishAfterAnswer && r.answered {
		if err := r.publishLocked(); err != nil {
			return "", err
		}
	}
	// Deliberately retain the old prompt after its answer. Native rendering and
	// completion artifacts are independent observations, not one atomic event.
	return "NATIVE_CONFIRM: Type the exact observation", nil
}
func (r *staleNativeInputRuntime) SendMessage(context.Context, ports.RuntimeHandle, string) error {
	r.mu.Lock()
	r.answered = true
	r.inputs++
	r.mu.Unlock()
	return nil
}
func (r *staleNativeInputRuntime) publish() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.publishLocked()
}
func (r *staleNativeInputRuntime) publishLocked() error {
	response := harnessResponse{Version: 1, RunID: r.request.RunID, DispatchID: r.request.DispatchID, Nonce: r.request.Nonce, Text: "The diagnostic response is complete."}
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return hookutil.AtomicWriteFile(filepath.Join(r.workspace, r.request.ResultFile), raw, 0o600)
}

func staleNativeInputFixture(t *testing.T, publishAfterAnswer bool) (*artifactConversation, *staleNativeInputRuntime, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	dataDir := t.TempDir()
	run := Run{ID: "stale-screen", Request: StartRequest{Selection: Selection{Harness: "fixture"}}}
	workspace, _, err := diagnosticWorkspace(dataDir, run)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &staleNativeInputRuntime{publishAfterAnswer: publishAfterAnswer}
	conv := newArtifactConversation(ctx, run, workspace, dataDir, diagnosticInstructions(), nil, fakeHarnessAgent{}, runtime)
	t.Cleanup(func() { _ = conv.Close() })
	ref, err := conv.SendTurn(ctx, ports.ChatUserMessage{Text: "Investigate"})
	if err != nil {
		t.Fatal(err)
	}
	if err := conv.StartDeferredTurn(ref.ProviderTurnID); err != nil {
		t.Fatal(err)
	}
	return conv, runtime, ctx
}

func TestArtifactHarnessDoesNotRepeatAnsweredNativeScreen(t *testing.T) {
	conv, runtime, ctx := staleNativeInputFixture(t, true)
	answered := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case event := <-conv.Events():
			if event.Kind == ports.ChatEventError {
				t.Fatal(event.Err)
			}
			if event.Kind == ports.ChatEventInputRequested {
				if answered {
					t.Fatal("an unchanged answered screen requested native input again instead of reading the completed artifact")
				}
				if err := conv.ResolveInput(ctx, event.RequestID, ports.ChatInputResponse{Action: ports.ChatInputActionAccept, Content: map[string]any{"action": "Send typed text", "text": "exact user input"}}); err != nil {
					t.Fatal(err)
				}
				answered = true
			}
			if event.Kind == ports.ChatEventControllerState {
				runtime.mu.Lock()
				inputs, destroyed := runtime.inputs, runtime.destroyed
				runtime.mu.Unlock()
				if !answered || inputs != 1 || !destroyed {
					t.Fatalf("native dispatch completion = answered %v, inputs %d, destroyed %v", answered, inputs, destroyed)
				}
				return
			}
		}
	}
}

func TestArtifactHarnessReadsResultWhileNativeInputIsPending(t *testing.T) {
	conv, runtime, ctx := staleNativeInputFixture(t, false)
	published := false
	for {
		select {
		case <-ctx.Done():
			t.Fatal("a native screen prevented the completed artifact from being read")
		case event := <-conv.Events():
			if event.Kind == ports.ChatEventError {
				t.Fatal(event.Err)
			}
			if event.Kind == ports.ChatEventInputRequested && !published {
				if err := runtime.publish(); err != nil {
					t.Fatal(err)
				}
				published = true
			}
			if event.Kind == ports.ChatEventControllerState {
				runtime.mu.Lock()
				inputs := runtime.inputs
				runtime.mu.Unlock()
				if !published || inputs != 0 {
					t.Fatalf("completion depended on native input: published %v, inputs %d", published, inputs)
				}
				return
			}
		}
	}
}
