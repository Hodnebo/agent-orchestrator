package triage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

func (h *host) prepare(origin *Origin) (Run, func(), error) {
	if active := h.current(""); active != nil {
		if active.ctx.Err() == nil {
			return active.snapshot(), nil, nil
		}
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		select {
		case <-active.done:
		case <-h.ctx.Done():
			return Run{}, nil, h.ctx.Err()
		case <-deadline.C:
			return Run{}, nil, errors.New("the previous investigation is still stopping; try Report a Problem again shortly")
		}
	}
	var source Origin
	if origin != nil {
		source = *origin
	}
	now := h.deps.Now().UTC()
	run := Run{ID: uuid.NewString(), Version: ProtocolVersion, CreatedAt: now, UpdatedAt: now,
		Preparation: true, Request: StartRequest{Origin: source}, State: "collecting",
		Activity: "Collecting diagnostics while you choose an agent.", Messages: []Message{}}
	release, _, err := h.store.claimRun(run.ID)
	if err != nil {
		return Run{}, nil, err
	}
	if err := h.store.save(run); err != nil {
		release()
		return Run{}, nil, err
	}
	i := h.newInvestigation(run, release)
	return i.snapshot(), i.prepare, nil
}

func (i *investigation) activate(request StartRequest) (Run, func(), error) {
	if i.ctx.Err() != nil {
		return Run{}, nil, errors.New("the diagnostic preparation was cancelled; open Report a Problem again")
	}
	run := i.snapshot()
	if request.PreparationID != run.ID {
		return Run{}, nil, errors.New("the diagnostic preparation does not match this investigation")
	}
	if request.Origin.SessionID != run.Request.Origin.SessionID || request.Origin.HostID != run.Request.Origin.HostID || request.Origin.Surface != run.Request.Origin.Surface {
		return Run{}, nil, errors.New("the report context changed; open Report a Problem again")
	}
	request.Origin = run.Request.Origin
	var reserved bool
	if err := i.commit(func(run *Run) {
		if run.Execution == "session" {
			reserved = true
			return
		}
		run.Preparation = false
		run.Request = request
		if run.State == "prepared" {
			run.State = "starting"
			run.Activity = "Starting the selected agent."
		}
		if request.Observation != "" {
			run.Messages = append(run.Messages, Message{ID: newMessageID(len(run.Messages) + 1), Role: "user", Text: request.Observation, At: i.host.deps.Now().UTC()})
		}
	}); err != nil {
		return Run{}, nil, err
	}
	if reserved {
		return Run{}, nil, errors.New("this preparation is reserved for an ordinary session handoff")
	}
	select {
	case i.activation <- struct{}{}:
	default:
	}
	return i.snapshot(), nil, nil
}

func (i *investigation) prepare() {
	defer i.finish()
	if err := i.collectEvidence(); err != nil {
		i.host.preparationFinished(i.id, err)
		return
	}
	if err := i.commit(func(run *Run) {
		if run.Preparation {
			run.State = "prepared"
			run.Activity = "Diagnostics are ready. Choose an agent to investigate."
		}
	}); err != nil {
		i.host.preparationFinished(i.id, err)
		return
	}
	i.host.preparationFinished(i.id, nil)
	select {
	case <-i.ctx.Done():
		return
	case <-i.activation:
	}
	if i.snapshot().Execution == "session" {
		return
	}
	i.runProvider()
}

func (i *investigation) collectEvidence() error {
	origin := i.snapshot().Request.Origin
	var evidence []Evidence
	if i.host.deps.Collect != nil {
		collected, err := i.host.deps.Collect(i.ctx, origin)
		evidence = append(evidence, collected...)
		if err != nil && i.ctx.Err() == nil {
			evidence = append(evidence, Evidence{ID: "collection-error", Kind: "collection", Source: "local triage collector", CollectedAt: i.host.deps.Now().UTC(), Status: "unavailable", Text: err.Error()})
		}
	}
	if i.ctx.Err() != nil {
		return i.ctx.Err()
	}
	if localContextAllowed(origin) && origin.SessionID == "" && i.host.deps.ListSessions != nil {
		ctx, cancel := context.WithTimeout(i.ctx, 3*time.Second)
		candidates, err := i.host.deps.ListSessions(ctx)
		cancel()
		item := Evidence{ID: "session-candidates", Kind: "session-candidates", Source: "recent local session records", CollectedAt: i.host.deps.Now().UTC(), Status: "collected", Values: map[string]any{"sessions": candidates}}
		if err != nil {
			item.Status = "unavailable"
			item.Text = err.Error()
			item.Values = nil
		}
		evidence = append(evidence, item)
	}
	if err := i.commit(func(run *Run) { run.Evidence = append(run.Evidence, evidence...) }); err != nil {
		return err
	}
	if i.host.deps.ReadContext == nil || !localContextAllowed(origin) {
		return nil
	}
	sources := []string{"daemon-log"}
	if origin.SessionID != "" {
		sources = append(sources, "session-history", "provider-log", "session-hook-errors")
	}
	for _, source := range sources {
		if !advertisesContextSource(evidence, source) {
			continue
		}
		if i.ctx.Err() != nil {
			return i.ctx.Err()
		}
		ctx, cancel := context.WithTimeout(i.ctx, 3*time.Second)
		item, err := i.host.deps.ReadContext(ctx, origin, ContextRead{Source: source})
		cancel()
		if err != nil {
			item = contextReadError(ContextRead{Source: source}, err)
		}
		item = RedactEvidence([]Evidence{item})[0]
		raw, _ := json.Marshal(item)
		if len(raw) > maxContextBytes-i.contextBytes {
			break
		}
		i.contextBytes += len(raw)
		i.contextReads++
		i.privateContext = append(i.privateContext, item)
		if err := i.commit(func(run *Run) {
			run.Evidence = append(run.Evidence, Evidence{ID: "initial-context-" + source, Kind: item.Kind, Source: item.Source, CollectedAt: item.CollectedAt, Status: item.Status, Text: "Relevant private diagnostic context was checked. Its contents are not attached automatically."})
		}); err != nil {
			return err
		}
	}
	return nil
}

func advertisesContextSource(evidence []Evidence, source string) bool {
	for _, item := range evidence {
		switch sources := item.Values["sources"].(type) {
		case []string:
			for _, candidate := range sources {
				if candidate == source {
					return true
				}
			}
		case []any:
			for _, candidate := range sources {
				if candidate == source {
					return true
				}
			}
		}
	}
	return false
}
