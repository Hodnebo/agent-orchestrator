package triage

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func finishWith(conv *fakeTriageConversation, text string) {
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderItemID: "final", Text: text}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, TurnState: domain.TurnStateCompleted}
}

func TestRunnerPublishesOnlyFinalDraftWithInitialAuthorization(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved only", true: "authorized"}[authorized], func(t *testing.T) {
			dir := t.TempDir()
			conv := fakeConversation()
			deps := configuredDeps()
			deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
			var publishCalls atomic.Int32
			deps.Publish = func(_ context.Context, run Run, save func(Publication) error) (Publication, error) {
				publishCalls.Add(1)
				store, _ := openRunStore(dir)
				saved, err := store.get(run.ID)
				if err != nil || saved.Draft == nil || saved.Draft.Title != "Blank session" || saved.State != "publishing" {
					return Publication{}, errors.New("publisher was called before draft was durable")
				}
				publication := Publication{State: "published", Marker: "stable-marker", IssueURL: "https://github.com/example/ao/issues/7"}
				if err := save(publication); err != nil {
					return publication, err
				}
				return publication, nil
			}
			c := serveForTest(t, dir, deps)
			c.send(startCommand("start", authorized))
			run := c.response("start").Run
			c.state("running")
			finishWith(conv, `{"title":"Blank session","body":"The session stayed blank after I opened it.","evidence":"Agent-collected evidence: no reproduction available."}`)
			if authorized {
				saved := *c.wait(func(response Response) bool {
					return response.Run != nil && response.Run.State == "needs_input" && response.Run.Publication.IssueURL != ""
				}).Run
				if saved.Publication.IssueURL == "" || publishCalls.Load() != 1 {
					t.Fatalf("publication failed: %#v", saved)
				}
			} else {
				c.state("needs_input")
				if publishCalls.Load() != 0 {
					t.Fatal("unauthorized report was published")
				}
				c.send(Command{ID: "publish", Type: "publish", RunID: run.ID})
				if response := c.response("publish"); response.Type != "error" || publishCalls.Load() != 0 {
					t.Fatalf("unauthorized retry accepted: %#v", response)
				}
			}
			cancelLiveConversation(t, c, conv, run.ID)
		})
	}
}

func TestRunnerMalformedFinalDoesNotInventDraftOrPublication(t *testing.T) {
	for _, text := range []string{
		"I collected the available evidence.",
		`{"title":"incomplete"}`,
		"```json\n{\"title\":\"Blank session\",\"body\":\"User report\"}\n```",
		`{"title":"Blank session","body":"User report"} {"extra":"value"}`,
	} {
		t.Run(text, func(t *testing.T) {
			conv := fakeConversation()
			deps := configuredDeps()
			deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
			deps.Publish = func(context.Context, Run, func(Publication) error) (Publication, error) {
				return Publication{}, errors.New("malformed draft reached publisher")
			}
			c := serveForTest(t, t.TempDir(), deps)
			c.send(startCommand("start", true))
			c.response("start")
			c.state("running")
			finishWith(conv, text)
			run := c.state("needs_input")
			if run.Draft != nil || run.Publication.State != "" || run.Error != "" {
				t.Fatalf("invented issue draft: %#v", run)
			}
			cancelLiveConversation(t, c, conv, run.ID)
		})
	}
}

func TestRunnerRestartKeepsPublicationUncertaintyAndRetriesSameIntent(t *testing.T) {
	dir := t.TempDir()
	store, err := openRunStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := Run{ID: "saved-run", Version: ProtocolVersion, CreatedAt: now, UpdatedAt: now,
		State: "publishing", Request: StartRequest{PublishAuthorized: true}, Messages: []Message{},
		Draft:       &Draft{Title: "Blank session", Body: "It stayed blank."},
		Publication: Publication{State: "issue_unknown", Marker: "stable-marker", Identity: "stable-identity", Title: "frozen title", Body: "frozen body", Error: "response lost"},
	}
	if err := store.save(run); err != nil {
		t.Fatal(err)
	}
	var starts atomic.Int32
	var publishes atomic.Int32
	deps := Dependencies{
		StartConversation: func(context.Context, Run) (ports.ChatConversation, error) {
			starts.Add(1)
			return nil, errors.New("must not replay provider prompt")
		},
		Publish: func(_ context.Context, saved Run, save func(Publication) error) (Publication, error) {
			publishes.Add(1)
			if saved.Publication != run.Publication {
				return Publication{}, errors.New("retry discarded previous publication identity")
			}
			if err := save(saved.Publication); err != nil {
				return Publication{}, err
			}
			return Publication{}, errors.New("outcome still unknown")
		},
	}
	c := serveForTest(t, dir, deps)
	c.send(Command{ID: "list", Type: "list"})
	response := c.response("list")
	if len(response.Runs) != 1 || response.Runs[0].State != "interrupted" || response.Runs[0].Publication != run.Publication {
		t.Fatalf("restart lost uncertain publication: %#v", response)
	}
	if starts.Load() != 0 || publishes.Load() != 0 {
		t.Fatal("restart launched a provider or automatically retried publication")
	}
	c.send(Command{ID: "publish", Type: "publish", RunID: run.ID})
	c.response("publish")
	final := c.state("report_saved")
	if final.Publication != run.Publication || !strings.Contains(final.Error, "unknown") || publishes.Load() != 1 || starts.Load() != 0 {
		t.Fatalf("retry lost publication: %#v", final)
	}
}

func TestRunnerPreservesCheckpointOnCancelledPublication(t *testing.T) {
	dir := t.TempDir()
	conv := fakeConversation()
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	publishing := make(chan struct{})
	deps.Publish = func(ctx context.Context, _ Run, save func(Publication) error) (Publication, error) {
		if err := save(Publication{State: "issue_unknown", Marker: "stable-marker"}); err != nil {
			return Publication{}, err
		}
		close(publishing)
		<-ctx.Done()
		return Publication{}, ctx.Err()
	}
	c := serveForTest(t, dir, deps)
	c.send(startCommand("start", true))
	run := c.response("start").Run
	c.state("running")
	finishWith(conv, `{"title":"Blank session","body":"The session stayed blank."}`)
	awaitSignal(t, publishing)
	c.send(Command{ID: "cancel", Type: "cancel", RunID: run.ID})
	_, final := c.responseAndRun("cancel", func(run Run) bool {
		return run.State == "interrupted" && run.Publication.State == "issue_unknown"
	})
	if final.Publication.State != "issue_unknown" || final.Publication.Marker != "stable-marker" {
		t.Fatalf("cancel erased uncertain publication: %#v", final)
	}
}
