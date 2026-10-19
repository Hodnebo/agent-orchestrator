package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/hookutil"
	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/terminalui"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty/ptyregistry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const harnessArtifactLimit = 256 << 10
const harnessRequestLimit = 4 << 20

const artifactTransportInstructions = `
This request uses private file transport. Messages contain the conversation context.
Write one final JSON object to the resultFile named in this request, relative to this private workspace.
The object must contain version=1, the exact runId, dispatchId and nonce from this request, and text containing your complete response to the latest turn.
Write a temporary file and rename it atomically. You may also update progressFile with the same identity fields and text describing the current check.
These named files are transport exceptions to the investigation's read-only rule. Do not change the affected project.
Never put a draft or question only in terminal output. Do not spawn another agent.
`

type harnessRuntime interface {
	ports.Runtime
	ports.StyledTerminalOutputReader
	SendMessage(context.Context, ports.RuntimeHandle, string) error
}

// terminalHarnesses uses the same registered adapters as normal worker launch.
// No executable, argv, shell command or environment comes from the chooser.
func terminalHarnesses() map[string]ports.Agent {
	out := map[string]ports.Agent{}
	for _, item := range agentregistry.Harnessed() {
		out[string(item.Harness)] = item.Agent
	}
	return out
}

type harnessRequest struct {
	Version      int              `json:"version"`
	RunID        string           `json:"runId"`
	DispatchID   string           `json:"dispatchId"`
	Nonce        string           `json:"nonce"`
	ResultFile   string           `json:"resultFile"`
	ProgressFile string           `json:"progressFile"`
	Instructions string           `json:"instructions"`
	Messages     []harnessMessage `json:"messages"`
}

type harnessMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Artifact text is the same private read/question/draft turn contract used by
// native Chat. Terminal output is never interpreted as a completed response.
type harnessResponse struct {
	Version    int    `json:"version"`
	RunID      string `json:"runId"`
	DispatchID string `json:"dispatchId"`
	Nonce      string `json:"nonce"`
	Text       string `json:"text"`
}

type artifactConversation struct {
	ctx                                     context.Context
	cancel                                  context.CancelFunc
	runID, workspace, dataDir, instructions string
	selection                               Selection
	env                                     map[string]string
	agent                                   ports.Agent
	runtime                                 harnessRuntime
	mu                                      sync.Mutex
	messages                                []harnessMessage
	sequence                                int
	active                                  string
	activeCancel                            context.CancelFunc
	pending                                 *harnessRequest
	handle                                  ports.RuntimeHandle
	nativeRequest                           string
	nativeReply                             chan struct{}
	nativeScreen                            string
	answeredNativeScreen                    string
	events                                  chan ports.ChatEvent
	workers                                 sync.WaitGroup
	closed                                  bool
	closeOnce                               sync.Once
}

func newArtifactConversation(ctx context.Context, run Run, workspace, dataDir, instructions string, env map[string]string, agent ports.Agent, runtime harnessRuntime) *artifactConversation {
	ctx, cancel := context.WithCancel(ctx)
	return &artifactConversation{ctx: ctx, cancel: cancel, runID: run.ID, workspace: workspace,
		dataDir: dataDir, instructions: instructions, selection: run.Request.Selection, env: env,
		agent: agent, runtime: runtime, events: make(chan ports.ChatEvent, 64)}
}

func (c *artifactConversation) ProviderConversationID() string { return c.runID }
func (c *artifactConversation) Capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{ports.ChatCapabilityStreaming: true, ports.ChatCapabilityInterrupt: true, ports.ChatCapabilityInteractive: true}
}
func (c *artifactConversation) Events() <-chan ports.ChatEvent { return c.events }

func (c *artifactConversation) SendTurn(ctx context.Context, msg ports.ChatUserMessage) (ports.ChatTurnRef, error) {
	if err := ctx.Err(); err != nil {
		return ports.ChatTurnRef{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.ctx.Err() != nil {
		return ports.ChatTurnRef{}, errors.New("diagnostic agent is closed")
	}
	if c.active != "" {
		return ports.ChatTurnRef{}, errors.New("diagnostic agent already has an active turn")
	}
	if len(msg.Content) != 0 {
		return ports.ChatTurnRef{}, errors.New("terminal diagnostic transport accepts text context only")
	}
	if msg.Settings.Model != "" && msg.Settings.Model != c.selection.Model || msg.Settings.Effort != "" && msg.Settings.Effort != c.selection.Effort || msg.Settings.Approval != "" && msg.Settings.Approval != c.selection.Permissions {
		return ports.ChatTurnRef{}, errors.New("diagnostic agent settings cannot change during an investigation")
	}
	c.sequence++
	id := fmt.Sprintf("dispatch-%d", c.sequence)
	identity, err := uuid.NewRandom()
	if err != nil {
		return ports.ChatTurnRef{}, err
	}
	nonce := identity.String()
	messages := append(append([]harnessMessage(nil), c.messages...), harnessMessage{Role: "user", Text: msg.Text})
	request := &harnessRequest{Version: 1, RunID: c.runID, DispatchID: id, Nonce: nonce,
		ResultFile: id + "-result.json", ProgressFile: id + "-progress.json", Instructions: c.instructions + artifactTransportInstructions, Messages: messages}
	raw, err := json.Marshal(request)
	if err != nil || len(raw) > harnessRequestLimit {
		return ports.ChatTurnRef{}, errors.New("private diagnostic conversation exceeds the context limit")
	}
	// The engine already filters private turns before dispatch. Preserve that
	// bounded context directly, rather than applying the public report's JSON
	// projection repeatedly to the entire accumulated conversation.
	if err := hookutil.AtomicWriteFile(filepath.Join(c.workspace, id+"-request.json"), raw, 0o600); err != nil {
		return ports.ChatTurnRef{}, errors.New("cannot save the private diagnostic request")
	}
	c.pending, c.active = request, id
	c.messages = messages
	return ports.ChatTurnRef{ProviderTurnID: id}, nil
}

// Starting after the engine has committed dispatch identity prevents fast file
// results from racing the durable turn binding.
func (c *artifactConversation) StartDeferredTurn(id string) error {
	c.mu.Lock()
	if c.closed || c.active != id || c.pending == nil {
		c.mu.Unlock()
		return errors.New("diagnostic dispatch is not pending")
	}
	request := c.pending
	c.pending = nil
	ctx, cancel := context.WithCancel(c.ctx)
	c.activeCancel = cancel
	c.workers.Add(1)
	c.mu.Unlock()
	go func() { defer c.workers.Done(); defer cancel(); c.dispatch(ctx, request) }()
	return nil
}

func (c *artifactConversation) DiscardDeferredTurn(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == id && c.pending != nil {
		c.pending = nil
		c.active = ""
	}
}

func (c *artifactConversation) emit(ctx context.Context, event ports.ChatEvent) bool {
	event.ProviderConversationID = c.runID
	select {
	case c.events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *artifactConversation) dispatch(ctx context.Context, request *harnessRequest) {
	text, err := c.execute(ctx, request)
	c.mu.Lock()
	if err == nil {
		c.messages = append(c.messages, harnessMessage{Role: "assistant", Text: text})
	}
	if c.active == request.DispatchID {
		c.active = ""
		c.activeCancel = nil
		c.handle = ports.RuntimeHandle{}
		if c.nativeReply != nil {
			close(c.nativeReply)
			c.nativeReply = nil
			c.nativeRequest = ""
		}
		c.nativeScreen, c.answeredNativeScreen = "", ""
	}
	c.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		c.emit(ctx, ports.ChatEvent{Kind: ports.ChatEventError, ProviderTurnID: request.DispatchID, Err: err})
		return
	}
	if !c.emit(ctx, ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderTurnID: request.DispatchID, ProviderItemID: request.DispatchID + "-result", Text: text}) {
		return
	}
	if !c.emit(ctx, ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, ProviderTurnID: request.DispatchID, TurnState: domain.TurnStateCompleted}) {
		return
	}
	c.emit(ctx, ports.ChatEvent{Kind: ports.ChatEventControllerState, ProviderTurnID: request.DispatchID, ControllerState: ports.ChatControllerReady})
}

func (c *artifactConversation) execute(ctx context.Context, request *harnessRequest) (text string, resultErr error) {
	requestPath := filepath.Join(c.workspace, request.DispatchID+"-request.json")
	bootstrap := fmt.Sprintf("Read the private diagnostic request %q in this workspace. Follow its instructions and respond through its resultFile. The named transport files are the only files you may write. Do not spawn another agent.", filepath.Base(requestPath))
	cfg := ports.LaunchConfig{Config: domain.AgentConfig{Model: c.selection.Model, Effort: c.selection.Effort, Mode: c.selection.Mode, Permissions: c.selection.Permissions},
		DataDir: c.dataDir, Kind: domain.KindWorker, Permissions: c.selection.Permissions, Prompt: bootstrap, SessionID: c.runID + "-" + request.DispatchID, WorkspacePath: c.workspace, Env: c.env}
	if c.selection.Harness == "amp" && cfg.Config.Mode == "" {
		cfg.Config.Mode, cfg.Config.Model = cfg.Config.Model, ""
	}
	// Aider has no model read-file tool. Its existing --read launch contract
	// supplies the request as read-only model context.
	if c.selection.Harness == "aider" {
		cfg.SystemPromptFile = requestPath
	}
	if preparer, ok := c.agent.(interface {
		PrepareStandaloneWorkspace(context.Context, ports.WorkspaceHookConfig) error
	}); ok {
		if err := preparer.PrepareStandaloneWorkspace(ctx, ports.WorkspaceHookConfig{Config: cfg.Config, DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath, SessionID: cfg.SessionID, Env: cfg.Env}); err != nil {
			return "", fmt.Errorf("prepare standalone agent: %w", err)
		}
	}
	argv, err := c.agent.GetLaunchCommand(ctx, cfg)
	if err != nil {
		return "", err
	}
	strategy, err := c.agent.GetPromptDeliveryStrategy(ctx, cfg)
	if err != nil {
		return "", err
	}
	env := make(map[string]string, len(c.env)+4)
	for key, value := range c.env {
		env[key] = value
	}
	env["AO_TRIAGE_REQUEST_FILE"] = requestPath
	env["AO_TRIAGE_RESULT_FILE"] = filepath.Join(c.workspace, request.ResultFile)
	env["AO_TRIAGE_DISPATCH_ID"] = request.DispatchID
	if augmenter, ok := c.agent.(interface {
		AugmentRuntimeEnv(map[string]string, string)
	}); ok && c.selection.Harness == "autohand" {
		augmenter.AugmentRuntimeEnv(env, c.dataDir)
	}
	handle, err := c.runtime.Create(ctx, ports.RuntimeConfig{SessionID: domain.SessionID("triage-" + cfg.SessionID), WorkspacePath: c.workspace, Argv: argv, Env: env, ExitOnCommandCompletion: true})
	if err != nil {
		var effect ports.RuntimeEffectError
		if errors.As(err, &effect) && effect.PossibleHandle().ID != "" {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = errors.Join(err, c.runtime.Destroy(cleanupCtx, effect.PossibleHandle()))
			cancel()
		}
		return "", fmt.Errorf("start selected diagnostic agent: %w", err)
	}
	c.mu.Lock()
	c.handle = handle
	c.mu.Unlock()
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, c.runtime.Destroy(stopCtx, handle))
	}()
	if strategy == ports.PromptDeliveryAfterStart {
		if err := c.deliver(ctx, handle, cfg, bootstrap); err != nil {
			return "", err
		}
	} else if strategy != ports.PromptDeliveryInCommand {
		return "", errors.New("selected agent did not provide a standalone prompt delivery strategy")
	}
	c.emit(ctx, ports.ChatEvent{Kind: ports.ChatEventActivityStarted, ProviderTurnID: request.DispatchID, Summary: "The selected agent is reading diagnostic evidence."})
	root, err := os.OpenRoot(c.workspace)
	if err != nil {
		return "", errors.New("private diagnostic workspace is unavailable")
	}
	defer func() { _ = root.Close() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	lastProgress := ""
	for {
		result, found, err := readHarnessArtifact(root, request.ResultFile, request)
		if err != nil {
			return "", err
		}
		if found {
			return result.Text, nil
		}
		progress, found, err := readHarnessArtifact(root, request.ProgressFile, request)
		if err != nil {
			return "", err
		}
		if found && progress.Text != lastProgress {
			lastProgress = progress.Text
			c.emit(ctx, ports.ChatEvent{Kind: ports.ChatEventActivityStarted, ProviderTurnID: request.DispatchID, Summary: progress.Text})
		}
		if detector, ok := c.agent.(ports.TerminalActivityDetector); ok {
			output, readErr := c.runtime.GetStyledOutput(ctx, handle, 40)
			if readErr == nil {
				if state, known := detector.DetectTerminalActivity(output); known {
					if state == domain.ActivityWaitingInput {
						// A rendered input boundary can outlive the provider's answer
						// or result. Keep reading the authoritative artifacts while
						// the human has the native controls open.
						if _, err := c.beginNativeInput(ctx, request.DispatchID, output, false); err != nil {
							return "", err
						}
					} else {
						c.clearAnsweredNativeScreen()
					}
				}
			}
		}
		if child, ok := c.runtime.(ports.RuntimeChildInspector); ok {
			alive, probeErr := child.IsChildAlive(ctx, handle)
			if probeErr == nil && !alive {
				// Re-read after the authoritative exit, since the final atomic
				// rename can race the previous observation.
				result, found, err = readHarnessArtifact(root, request.ResultFile, request)
				if err != nil {
					return "", err
				}
				if found {
					return result.Text, nil
				}
				return "", errors.New("selected agent exited without a structured diagnostic response")
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func readHarnessArtifact(root *os.Root, name string, request *harnessRequest) (harnessResponse, bool, error) {
	var result harnessResponse
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return result, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > harnessArtifactLimit {
		return result, false, errors.New("diagnostic agent wrote an invalid response artifact")
	}
	file, err := root.Open(name)
	if err != nil {
		return result, false, errors.New("diagnostic response artifact is unavailable")
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, harnessArtifactLimit+1))
	if decoder.Decode(&result) != nil {
		return result, false, errors.New("diagnostic agent wrote malformed response JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || result.Version != 1 || result.RunID != request.RunID || result.DispatchID != request.DispatchID || result.Nonce != request.Nonce || strings.TrimSpace(result.Text) == "" {
		return result, false, errors.New("diagnostic response identity or content is invalid")
	}
	return result, true, nil
}

func (c *artifactConversation) deliver(ctx context.Context, handle ports.RuntimeHandle, cfg ports.LaunchConfig, prompt string) error {
	if err := c.waitForPrompt(ctx, handle, cfg); err != nil {
		return err
	}
	if selector, ok := c.agent.(ports.AgentAfterStartCommand); ok {
		command, err := selector.AfterStartCommand(ctx, cfg)
		if err != nil {
			return err
		}
		if command != "" {
			if err := c.runtime.SendMessage(ctx, handle, command); err != nil {
				return err
			}
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				output, err := c.runtime.GetStyledOutput(ctx, handle, 100)
				if err != nil {
					return err
				}
				if selector.AfterStartCommandApplied(cfg, output) {
					break
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-deadline.C:
					return errors.New("selected native model was not confirmed")
				case <-ticker.C:
				}
			}
		}
	}
	if builder, ok := c.agent.(ports.AgentAfterStartPromptBuilder); ok {
		var err error
		prompt, err = builder.BuildAfterStartPrompt(ctx, cfg)
		if err != nil {
			return err
		}
	}
	return c.runtime.SendMessage(ctx, handle, prompt)
}

func (c *artifactConversation) waitForPrompt(ctx context.Context, handle ports.RuntimeHandle, cfg ports.LaunchConfig) error {
	provider, ok := c.agent.(ports.AgentPromptReadinessProvider)
	if !ok {
		return nil
	}
	hints, err := provider.PromptReadinessHints(ctx, cfg)
	if err != nil {
		return err
	}
	if hints.InitialDelay > 0 {
		timer := time.NewTimer(hints.InitialDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if hints.Timeout <= 0 {
		return nil
	}
	if hints.PollInterval <= 0 {
		hints.PollInterval = 100 * time.Millisecond
	}
	if hints.Lines <= 0 {
		hints.Lines = 80
	}
	deadline := time.NewTimer(hints.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(hints.PollInterval)
	defer ticker.Stop()
	for {
		output, err := c.runtime.GetStyledOutput(ctx, handle, hints.Lines)
		if err != nil {
			return err
		}
		if detector, ok := c.agent.(ports.TerminalActivityDetector); ok {
			if state, known := detector.DetectTerminalActivity(output); known {
				if state == domain.ActivityWaitingInput {
					c.mu.Lock()
					id := c.active
					c.mu.Unlock()
					if err := c.requestNativeInput(ctx, id, output); err != nil {
						return err
					}
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-ticker.C:
					}
					continue
				}
				if state == domain.ActivityIdle {
					c.clearAnsweredNativeScreen()
					return nil
				}
				c.clearAnsweredNativeScreen()
			}
		}
		for _, pattern := range hints.Patterns {
			if strings.Contains(output, pattern) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return nil
		case <-ticker.C:
		}
	}
}

func (c *artifactConversation) Interrupt(ctx context.Context, id string) error {
	c.mu.Lock()
	cancel := c.activeCancel
	active := c.active
	c.mu.Unlock()
	if active == "" || id != "" && id != active {
		return ports.ErrChatNoActiveTurn
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if cancel != nil {
		cancel()
	}
	return nil
}
func (c *artifactConversation) ResolveRequest(context.Context, string, ports.ChatDecision) error {
	return ports.ErrChatRequestNotPending
}

// The adapter identifies a native input boundary, but supplies no machine
// approval semantics. Show its rendered screen and forward only a key the human
// explicitly chooses. Terminal text never becomes an invented approval.
func (c *artifactConversation) requestNativeInput(ctx context.Context, dispatchID, screen string) error {
	reply, err := c.beginNativeInput(ctx, dispatchID, screen, false)
	if err != nil {
		return err
	}
	if reply == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-reply:
		return nil
	}
}

// RequestNativeInput lets the main workspace expose the selected agent's native
// interface even when its adapter cannot reliably identify blocked screens.
// It only offers explicit key forwarding; opening the controls sends no input.
func (c *artifactConversation) RequestNativeInput(ctx context.Context) error {
	c.mu.Lock()
	handle, id := c.handle, c.active
	c.mu.Unlock()
	if handle.ID == "" || id == "" {
		return ports.ErrChatNoActiveTurn
	}
	screen, err := c.runtime.GetStyledOutput(ctx, handle, 40)
	if err != nil {
		return err
	}
	_, err = c.beginNativeInput(ctx, id, screen, true)
	return err
}

func (c *artifactConversation) clearAnsweredNativeScreen() {
	c.mu.Lock()
	c.answeredNativeScreen = ""
	c.mu.Unlock()
}

func (c *artifactConversation) beginNativeInput(ctx context.Context, dispatchID, screen string, manual bool) (<-chan struct{}, error) {
	c.mu.Lock()
	if c.nativeReply != nil {
		reply := c.nativeReply
		c.mu.Unlock()
		return reply, nil
	}
	if c.active != dispatchID || c.closed || c.ctx.Err() != nil {
		c.mu.Unlock()
		return nil, ports.ErrChatNoActiveTurn
	}
	// Native rendering does not acknowledge a keypress atomically. The same
	// answered screen must not create another automatic question while its
	// result is being written. A human can explicitly reopen it at any time.
	if !manual && c.answeredNativeScreen != "" && screen == c.answeredNativeScreen {
		c.mu.Unlock()
		return nil, nil
	}
	requestID := dispatchID + "-native-" + uuid.NewString()
	reply := make(chan struct{})
	c.nativeRequest, c.nativeReply, c.nativeScreen = requestID, reply, screen
	plain := terminalui.PlainTerminalText(screen)
	if len(plain) > 16<<10 {
		plain = plain[len(plain)-(16<<10):]
	}
	input := &ports.ChatInputRequest{Mode: ports.ChatInputModeForm, Message: "The selected agent needs input in its native interface.", Schema: map[string]any{
		"type": "object", "properties": map[string]any{
			"action": map[string]any{"type": "string", "description": "The selected agent's current screen:\n\n" + plain + "\n\nChoose a key to send to this screen. Review the screen before pressing Enter.", "enum": []any{"Keep waiting", "Press Enter", "Press Escape", "Move up", "Move down", "Send typed text"}},
			"text":   map[string]any{"type": "string", "description": "Optional exact text, used only for Send typed text."},
		}, "required": []any{"action"},
	}}
	event := ports.ChatEvent{Kind: ports.ChatEventInputRequested, ProviderConversationID: c.runID, ProviderTurnID: dispatchID, RequestID: requestID, Input: input}
	select {
	case c.events <- event:
		c.mu.Unlock()
		return reply, nil
	default:
		c.nativeRequest, c.nativeReply = "", nil
		c.nativeScreen = ""
		close(reply)
		c.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("diagnostic native input event queue is full")
	}
}

func (c *artifactConversation) ResolveInput(ctx context.Context, id string, response ports.ChatInputResponse) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nativeRequest == "" || id != c.nativeRequest {
		return ports.ErrChatRequestNotPending
	}
	if response.Action != ports.ChatInputActionAccept {
		return errors.New("cancel the investigation to dismiss native input")
	}
	action, _ := response.Content["action"].(string)
	var err error
	// Keep waiting dismisses the screen without sending a native command.
	if action == "Send typed text" {
		text, _ := response.Content["text"].(string)
		if text == "" || len(text) > 4096 {
			return errors.New("enter the exact native input to send")
		}
		for _, r := range text {
			if unicode.IsControl(r) {
				return errors.New("native text input cannot contain control keys")
			}
		}
		err = c.runtime.SendMessage(ctx, c.handle, text)
	} else if action != "Keep waiting" {
		keys := map[string]string{"Press Enter": "\r", "Press Escape": "\x1b", "Move up": "\x1b[A", "Move down": "\x1b[B"}
		key, ok := keys[action]
		if !ok {
			return ports.ErrChatDecisionNotOffered
		}
		runtime, ok := c.runtime.(interface {
			SendInput(context.Context, ports.RuntimeHandle, string) error
		})
		if !ok {
			return errors.New("native key input is unavailable")
		}
		err = runtime.SendInput(ctx, c.handle, key)
	}
	if err != nil {
		return err
	}
	c.answeredNativeScreen = c.nativeScreen
	c.nativeScreen = ""
	close(c.nativeReply)
	c.nativeReply = nil
	c.nativeRequest = ""
	return nil
}
func (c *artifactConversation) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.cancel()
		c.workers.Wait()
		close(c.events)
	})
	return nil
}
func (c *artifactConversation) Terminate() error { return c.Close() }

// A separate registry prevents normal daemon reconciliation from adopting or
// deleting these private terminals. On helper restart, reap only this registry's
// owned diagnostic handles before starting another selected provider.
func newHarnessRuntime(ctx context.Context, dataDir string) (harnessRuntime, error) {
	root := filepath.Join(dataDir, "reports", "triage", "provider-hosts", "terminals")
	if err := privateDir(root); err != nil {
		return nil, err
	}
	runtime := conpty.New(conpty.Options{RunFilePath: filepath.Join(root, "running.json")})
	entries, complete, err := ptyregistry.Scan(ctx)
	if err != nil || !complete {
		return nil, errors.New("private diagnostic terminal ownership is unavailable")
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.SessionID, "triage-") {
			return nil, errors.New("unexpected private diagnostic terminal ownership")
		}
		if err := runtime.Destroy(ctx, ports.RuntimeHandle{ID: entry.SessionID}); err != nil {
			return nil, fmt.Errorf("stop interrupted diagnostic terminal: %w", err)
		}
	}
	return runtime, nil
}
