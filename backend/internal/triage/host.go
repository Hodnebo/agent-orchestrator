package triage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	maxCommandBytes = 1 << 20
	maxReplyBytes   = 8 << 10
)

type host struct {
	ctx         context.Context
	cancel      context.CancelFunc
	deps        Dependencies
	store       *runStore
	out         io.Writer
	outMu       sync.Mutex
	outErr      error
	mu          sync.Mutex
	active      *investigation
	handoffMu   sync.Mutex
	preparation *preparationResult
	work        sync.WaitGroup
}

// Serve runs the private newline-JSON support protocol. It owns only the
// conversations it starts and never discovers or starts the AO daemon.
func Serve(ctx context.Context, in io.Reader, out io.Writer, opts Options) error {
	store, err := openRunStore(opts.DataDir)
	if err != nil {
		return err
	}
	if err := store.recoverInterrupted(ctx, opts.Deps.RecoverConversation); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	h := &host{ctx: ctx, cancel: cancel, deps: opts.Deps, store: store, out: out}
	if h.deps.Now == nil {
		h.deps.Now = time.Now
	}
	defer cancel()
	defer h.shutdown()
	if closer, ok := in.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	type inputLine struct {
		data []byte
		err  error
	}
	lines := make(chan inputLine)
	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 64<<10), maxCommandBytes+1)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- inputLine{data: line}:
			case <-ctx.Done():
				return
			}
		}
		select {
		case lines <- inputLine{err: scanner.Err()}:
		case <-ctx.Done():
		}
		close(lines)
	}()
	for {
		select {
		case <-ctx.Done():
			return h.outputError()
		case line, ok := <-lines:
			if !ok {
				return h.outputError()
			}
			if line.data == nil {
				if line.err != nil {
					h.emit(Response{Type: "error", Error: "triage command input failed or exceeded 1 MiB"})
					return line.err
				}
				return h.outputError()
			}
			var cmd Command
			decoder := json.NewDecoder(bytes.NewReader(line.data))
			decoder.DisallowUnknownFields()
			err := decoder.Decode(&cmd)
			if err == nil {
				var extra any
				if decoder.Decode(&extra) != io.EOF {
					err = errors.New("expected one command per line")
				}
			}
			if err != nil || cmd.Type == "" || len(line.data) > maxCommandBytes {
				h.emit(Response{ID: cmd.ID, Type: "error", Error: "invalid triage command"})
				continue
			}
			if h.command(cmd) {
				return h.outputError()
			}
		}
	}
}

func (h *host) outputError() error {
	h.outMu.Lock()
	defer h.outMu.Unlock()
	return h.outErr
}

func (h *host) emit(response Response) {
	response.Version = ProtocolVersion
	raw, err := sanitizeJSON(response)
	if err == nil {
		var compact bytes.Buffer
		err = json.Compact(&compact, raw)
		raw = append(compact.Bytes(), '\n')
	}
	if err == nil {
		h.outMu.Lock()
		if h.outErr == nil {
			var written int
			written, h.outErr = h.out.Write(raw)
			if h.outErr == nil && written != len(raw) {
				h.outErr = io.ErrShortWrite
			}
		}
		err = h.outErr
		h.outMu.Unlock()
	}
	if err != nil {
		h.outMu.Lock()
		if h.outErr == nil {
			h.outErr = err
		}
		h.outMu.Unlock()
		h.cancel()
	}
}

func (h *host) fail(id string, err error) {
	h.emit(Response{ID: id, Type: "error", Error: err.Error()})
}

func (h *host) catalog() ([]Choice, error) {
	if h.deps.Catalog == nil {
		return []Choice{}, nil
	}
	return h.deps.Catalog(h.ctx)
}

func validateSelection(selection Selection, choices []Choice) error {
	if !selection.Permissions.Valid() {
		return errors.New("invalid configured permission mode")
	}
	for _, choice := range choices {
		if choice.Harness != selection.Harness {
			continue
		}
		if !choice.Available {
			return fmt.Errorf("selected harness is unavailable: %s", choice.UnavailableReason)
		}
		if choice.Permissions != "" && choice.Permissions != selection.Permissions {
			return errors.New("permission mode does not match the configured agent")
		}
		if choice.Mode != "" && choice.Mode != selection.Mode {
			return errors.New("agent mode does not match the configured agent")
		}
		var model *ModelChoice
		for index := range choice.Models {
			if choice.Models[index].ID == selection.Model {
				model = &choice.Models[index]
				break
			}
		}
		if model == nil {
			return errors.New("model is not in the configured catalog")
		}
		if selection.Effort != "" {
			found := false
			for _, effort := range model.Efforts {
				found = found || effort == selection.Effort
			}
			if !found {
				return errors.New("effort is not in the configured catalog")
			}
		}
		if selection.AccountID != "" {
			found := false
			for _, account := range choice.Accounts {
				found = found || account.ID == selection.AccountID
			}
			if !found {
				return errors.New("account is not in the configured catalog")
			}
		}
		return nil
	}
	return errors.New("harness is not configured for triage")
}

func (h *host) command(cmd Command) bool {
	switch cmd.Type {
	case "catalog":
		choices, err := h.catalog()
		if err != nil {
			h.fail(cmd.ID, err)
		} else {
			h.emit(Response{ID: cmd.ID, Type: "catalog", Choices: choices})
		}
	case "configure":
		if h.deps.Configure == nil {
			h.fail(cmd.ID, errors.New("triage configuration is unavailable"))
			break
		}
		if err := h.deps.Configure(h.ctx, cmd.Choices); err != nil {
			h.fail(cmd.ID, err)
			break
		}
		choices, err := h.catalog()
		if err != nil {
			h.fail(cmd.ID, err)
		} else {
			h.emit(Response{ID: cmd.ID, Type: "configured", Choices: choices})
		}
	case "preference":
		if cmd.Selection != nil {
			choices, err := h.catalog()
			if err == nil {
				err = validateSelection(*cmd.Selection, choices)
			}
			if err == nil {
				err = atomicPrivateJSON(filepath.Join(h.store.root, "selection.json"), cmd.Selection)
			}
			if err != nil {
				h.fail(cmd.ID, err)
				break
			}
		}
		selection, err := h.store.selection()
		if err != nil {
			h.fail(cmd.ID, err)
		} else {
			h.emit(Response{ID: cmd.ID, Type: "selection", Selection: selection})
		}
	case "list":
		runs, err := h.store.list()
		if err != nil {
			h.fail(cmd.ID, err)
		} else {
			h.emit(Response{ID: cmd.ID, Type: "runs", Runs: runs})
		}
	case "get":
		run, err := h.store.get(cmd.RunID)
		if err != nil {
			h.fail(cmd.ID, err)
		} else {
			h.emit(Response{ID: cmd.ID, Type: "run", Run: &run})
		}
	case "prepare":
		run, launch, err := h.prepare(cmd.Origin)
		if err != nil {
			h.fail(cmd.ID, err)
		} else {
			h.emit(Response{ID: cmd.ID, Type: "run", Run: &run})
			if launch != nil {
				go launch()
			}
		}
	case "github-auth-store":
		if cmd.GitHubCredential == nil || h.deps.StoreGitHubCredential == nil {
			h.fail(cmd.ID, errors.New("GitHub sign-in storage is unavailable"))
			break
		}
		identity, err := h.deps.StoreGitHubCredential(h.ctx, *cmd.GitHubCredential)
		if err != nil {
			h.fail(cmd.ID, err)
		} else {
			h.emit(Response{ID: cmd.ID, Type: "github-auth-stored", GitHubIdentity: &identity})
		}
	case "start":
		run, launch, err := h.start(cmd.Start)
		if err != nil {
			h.fail(cmd.ID, err)
		} else {
			h.emit(Response{ID: cmd.ID, Type: "run", Run: &run})
			if launch != nil {
				go launch()
			}
		}
	case "handoff":
		// Collection may still be running. Keep the command loop available for
		// cancellation while the durable launch payload is being prepared.
		h.work.Add(1)
		go func() {
			defer h.work.Done()
			run, launch, err := h.sessionHandoff(cmd.Start)
			if err != nil {
				h.fail(cmd.ID, err)
			} else {
				h.emit(Response{ID: cmd.ID, Type: "session-launch", Run: &run, Launch: &launch})
			}
		}()
	case "session-linked":
		h.work.Add(1)
		go func() {
			defer h.work.Done()
			run, err := h.store.linkSession(h.ctx, cmd.RunID, cmd.SessionID, h.deps.Now)
			if err != nil {
				h.fail(cmd.ID, err)
			} else {
				h.emit(Response{ID: cmd.ID, Type: "run", Run: &run})
			}
		}()
	case "session-create-intent":
		run, err := h.store.sessionCreateIntent(cmd.RunID, h.deps.Now)
		if err != nil {
			h.fail(cmd.ID, err)
		} else {
			h.emit(Response{ID: cmd.ID, Type: "run", Run: &run})
		}
	case "reply", "native-input":
		i := h.current(cmd.RunID)
		if i == nil {
			h.fail(cmd.ID, errors.New("triage run is not active in this host"))
			break
		}
		if cmd.Type == "reply" {
			if err := validateReply(cmd); err != nil {
				h.fail(cmd.ID, err)
				break
			}
		}
		select {
		case i.replies <- cmd:
		default:
			h.fail(cmd.ID, errors.New("a reply is already being sent"))
		}
	case "cancel":
		i := h.current(cmd.RunID)
		if i == nil {
			h.fail(cmd.ID, errors.New("triage run is not active in this host"))
			break
		}
		i.stop()
		run := i.snapshot()
		h.emit(Response{ID: cmd.ID, Type: "run", Run: &run})
	case "publish":
		if !validRunID(cmd.RunID) {
			h.fail(cmd.ID, errors.New("publication requires a triage run ID"))
			break
		}
		if active := h.current(cmd.RunID); active != nil {
			run := active.snapshot()
			if !run.Request.PublishAuthorized || run.Draft == nil {
				h.fail(cmd.ID, errors.New("an authorized saved report draft is required"))
				break
			}
			cmd.Type = "retry-publication"
			select {
			case active.replies <- cmd:
			default:
				h.fail(cmd.ID, errors.New("a report action is already being sent"))
			}
			break
		}
		run, launch, err := h.retryPublication(cmd.RunID)
		if err != nil {
			h.fail(cmd.ID, err)
		} else {
			h.emit(Response{ID: cmd.ID, Type: "run", Run: &run})
			if launch != nil {
				h.work.Add(1)
				go func() { defer h.work.Done(); launch() }()
			}
		}
	case "quit":
		h.emit(Response{ID: cmd.ID, Type: "quit"})
		return true
	default:
		h.fail(cmd.ID, errors.New("unknown triage command"))
	}
	return false
}

func validateReply(cmd Command) error {
	raw, err := json.Marshal(cmd.Answers)
	if err != nil || len(cmd.Text)+len(raw) > maxReplyBytes {
		return errors.New("triage reply exceeds 8 KiB")
	}
	if strings.TrimSpace(cmd.Text) == "" && len(cmd.Answers) == 0 {
		return errors.New("a reply is required")
	}
	return nil
}

func (h *host) current(id string) *investigation {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active == nil || id != "" && h.active.id != id {
		return nil
	}
	select {
	case <-h.active.done:
		return nil
	default:
		return h.active
	}
}

func (h *host) start(request *StartRequest) (Run, func(), error) {
	if request == nil {
		return Run{}, nil, errors.New("triage start request is required")
	}
	if len(request.Observation) > maxReplyBytes {
		return Run{}, nil, errors.New("triage observation exceeds 8 KiB")
	}
	if request.PreparationID != "" {
		if saved, err := h.store.get(request.PreparationID); err == nil && saved.Execution == "session" {
			return h.adoptUnsentSession(*request)
		}
	}
	choices, err := h.catalog()
	if err != nil {
		return Run{}, nil, err
	}
	for _, choice := range choices {
		if choice.Harness == request.Harness {
			if request.Permissions == "" {
				request.Permissions = choice.Permissions
			}
			if request.Mode == "" {
				request.Mode = choice.Mode
			}
			break
		}
	}
	if err := validateSelection(request.Selection, choices); err != nil {
		return Run{}, nil, err
	}
	if active := h.current(""); active != nil {
		if active.snapshot().Execution == "session" {
			return Run{}, nil, errors.New("this preparation is reserved for an ordinary session handoff")
		}
		if active.snapshot().Preparation {
			return active.activate(*request)
		}
		return active.snapshot(), nil, nil
	}
	if request.PreparationID != "" {
		return Run{}, nil, errors.New("the diagnostic preparation is no longer active; open Report a Problem again")
	}
	now := h.deps.Now().UTC()
	run := Run{ID: uuid.NewString(), Version: ProtocolVersion, CreatedAt: now, UpdatedAt: now,
		Request: *request, State: "collecting", Activity: "Collecting relevant local evidence.", Messages: []Message{}}
	if strings.TrimSpace(request.Observation) != "" {
		run.Messages = append(run.Messages, Message{ID: newMessageID(1), Role: "user", Text: request.Observation, At: now})
	}
	release, owner, err := h.store.claimRun(run.ID)
	if errors.Is(err, errRunOwned) && owner != nil && owner.RunID != "" {
		existing, readErr := h.store.get(owner.RunID)
		return existing, nil, readErr
	}
	if err != nil {
		return Run{}, nil, err
	}
	if err := h.store.save(run); err != nil {
		release()
		return Run{}, nil, err
	}
	run, err = h.store.get(run.ID)
	if err != nil {
		release()
		return Run{}, nil, err
	}
	i := h.newInvestigation(run, release)
	return i.snapshot(), i.investigate, nil
}

func (h *host) newInvestigation(run Run, release func()) *investigation {
	ctx, cancel := context.WithCancel(h.ctx)
	i := &investigation{host: h, id: run.ID, ctx: ctx, cancel: cancel, run: run,
		replies: make(chan Command, 1), activation: make(chan struct{}, 1), done: make(chan struct{}), release: release}
	h.mu.Lock()
	h.active = i
	if run.Preparation {
		h.preparation = &preparationResult{id: run.ID, ready: make(chan struct{})}
	}
	h.mu.Unlock()
	return i
}

func (h *host) retryPublication(id string) (Run, func(), error) {
	if active := h.current(""); active != nil {
		if active.id == id {
			run := active.snapshot()
			if !run.Request.PublishAuthorized {
				return Run{}, nil, errors.New("publication was not authorized for this report")
			}
			if run.Draft == nil {
				return Run{}, nil, errors.New("this report has no completed issue draft")
			}
			return run, nil, nil
		}
		return Run{}, nil, errRunOwned
	}
	run, err := h.store.get(id)
	if err != nil {
		return Run{}, nil, err
	}
	if run.Execution == "session" {
		return h.retrySessionPublication(run)
	}
	if !run.Request.PublishAuthorized {
		return Run{}, nil, errors.New("publication was not authorized for this report")
	}
	if run.Draft == nil {
		return Run{}, nil, errors.New("this report has no completed issue draft")
	}
	release, _, err := h.store.claimRun(id)
	if err != nil {
		return Run{}, nil, err
	}
	i := h.newInvestigation(run, release)
	if run.Publication.State == PublicationAuthRequired && !run.SearchCompleted && run.Publication.IssueURL == "" && h.deps.SearchReports != nil {
		return i.snapshot(), i.investigate, nil
	}
	return i.snapshot(), func() { defer i.finish(); i.publish() }, nil
}

func (h *host) shutdown() {
	h.cancel()
	if i := h.current(""); i != nil {
		i.stop()
		<-i.done
	}
	h.work.Wait()
}
