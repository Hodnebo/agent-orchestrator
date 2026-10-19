package triage

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func followupInvestigation(t *testing.T) (*investigation, *fakeTriageConversation) {
	t.Helper()
	store, err := openRunStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	conv := fakeConversation()
	h := &host{ctx: ctx, cancel: cancel, store: store, out: io.Discard, deps: Dependencies{Now: time.Now}}
	run := Run{ID: "followup-run", Version: ProtocolVersion, State: "starting", Request: *startCommand("", false).Start,
		ProviderConversationID: conv.ProviderConversationID(), Messages: []Message{}}
	i := &investigation{host: h, id: run.ID, ctx: ctx, cancel: cancel, run: run, conv: conv,
		replies: make(chan Command, 1), release: func() {}, done: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-i.done:
		default:
			i.finish()
		}
	})
	if err := i.sendTurn("Investigate the observed problem.", false); err != nil {
		t.Fatal(err)
	}
	contextTurn(t, conv)
	return i, conv
}

func followupEvent(t *testing.T, i *investigation, event ports.ChatEvent) {
	t.Helper()
	if err := i.event(event); err != nil {
		t.Fatalf("live conversation event failed: %v", err)
	}
}

func completeFollowup(t *testing.T, i *investigation, text string) {
	t.Helper()
	followupEvent(t, i, ports.ChatEvent{Kind: ports.ChatEventMessageCompleted, ProviderItemID: i.snapshot().ProviderTurnID + "-answer", Text: text})
	followupEvent(t, i, ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, TurnState: domain.TurnStateCompleted})
}

func readyFollowup(t *testing.T, i *investigation) {
	t.Helper()
	followupEvent(t, i, ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady})
}

func assertConversationLive(t *testing.T, conv *fakeTriageConversation) {
	t.Helper()
	select {
	case <-conv.terminated:
		t.Fatal("the selected conversation was terminated")
	default:
	}
}

func cancelLiveConversation(t *testing.T, c *hostClient, conv *fakeTriageConversation, runID string) {
	t.Helper()
	assertConversationLive(t, conv)
	c.send(Command{ID: "stop-live-" + runID, Type: "cancel", RunID: runID})
	c.responseAndState("stop-live-"+runID, "interrupted")
	awaitSignal(t, conv.terminated)
}

func TestConversationQueuesOrdinaryMessageUntilReady(t *testing.T) {
	i, conv := followupInvestigation(t)
	text := "It happened after the laptop woke up."
	if err := i.reply(Command{Type: "reply", Text: text}); err != nil {
		t.Fatal(err)
	}
	if conv.turns.Load() != 1 || len(i.queuedHumanText) != 1 {
		t.Fatal("busy provider received an overlapping human turn")
	}
	completeFollowup(t, i, "I checked the available startup diagnostics.")
	if conv.turns.Load() != 1 {
		t.Fatal("turn completion was treated as the Ready barrier")
	}
	readyFollowup(t, i)
	if msg := contextTurn(t, conv); msg.Text != text || !msg.AuthoredByUser || msg.ClientMessageID != "followup-run/dispatch-2" {
		t.Fatalf("queued human message lost its identity: %#v", msg)
	}
	count := 0
	for _, msg := range i.snapshot().Messages {
		if msg.Role == "user" && msg.Text == text {
			count++
		}
	}
	if count != 1 || len(i.queuedHumanText) != 0 || i.queuedHumanBytes != 0 {
		t.Fatalf("message was duplicated or retained after dispatch: count=%d queue=%d bytes=%d", count, len(i.queuedHumanText), i.queuedHumanBytes)
	}
}

func TestConversationOrdinaryMessageDoesNotResolveNativeInput(t *testing.T) {
	for _, kind := range []ports.ChatEventKind{ports.ChatEventInputRequested, ports.ChatEventApprovalRequested} {
		t.Run(string(kind), func(t *testing.T) {
			i, conv := followupInvestigation(t)
			resolved := 0
			conv.inputHook = func(string, ports.ChatInputResponse) error { resolved++; return nil }
			conv.resolveHook = func(string, ports.ChatDecision) error { resolved++; return nil }
			followupEvent(t, i, ports.ChatEvent{Kind: kind, RequestID: "native-confirm", Input: &ports.ChatInputRequest{
				Schema: map[string]any{"properties": map[string]any{"choice": map[string]any{"type": "string", "enum": []any{"Approve", "Decline"}}}},
			}})
			if err := i.reply(Command{Type: "reply", Text: "Approve"}); err != nil {
				t.Fatal(err)
			}
			if run := i.snapshot(); resolved != 0 || run.State != "needs_input" || run.PendingInput == nil || run.PendingInput.ID != "native-confirm" || conv.turns.Load() != 1 {
				t.Fatalf("ordinary typing answered a native request: resolved=%d run=%#v", resolved, run)
			}
			if err := i.reply(Command{Type: "reply", RequestID: "native-confirm", Answers: map[string][]string{"choice": {"Decline"}}}); err != nil {
				t.Fatal(err)
			}
			if resolved != 1 || i.snapshot().PendingInput != nil || conv.turns.Load() != 1 {
				t.Fatal("explicit native answer did not retain the queued ordinary message")
			}
			completeFollowup(t, i, "The native request is resolved.")
			readyFollowup(t, i)
			if msg := contextTurn(t, conv); msg.Text != "Approve" || !msg.AuthoredByUser {
				t.Fatalf("ordinary text was not delivered as a later chat turn: %#v", msg)
			}
		})
	}
}

func TestConversationOrdinaryCompletedTextStaysConversational(t *testing.T) {
	for _, text := range []string{"I found no startup failure in the available logs.", "What did you expect to see?"} {
		t.Run(text, func(t *testing.T) {
			i, conv := followupInvestigation(t)
			completeFollowup(t, i, text)
			run := i.snapshot()
			if run.State != "needs_input" || run.PendingInput != nil || run.Draft != nil || run.Messages[len(run.Messages)-1].Text != text {
				t.Fatalf("ordinary prose was replaced with a report or generic choices: %#v", run)
			}
			assertConversationLive(t, conv)
			readyFollowup(t, i)
			if err := i.reply(Command{Type: "reply", Text: "I expected the terminal prompt."}); err != nil {
				t.Fatal(err)
			}
			if msg := contextTurn(t, conv); !msg.AuthoredByUser || msg.Text != "I expected the terminal prompt." {
				t.Fatalf("ordinary follow-up did not continue the same conversation: %#v", msg)
			}
		})
	}
}

func TestConversationSavedAndPublishedDraftStayLive(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(fmt.Sprint(authorized), func(t *testing.T) {
			i, conv := followupInvestigation(t)
			i.run.Request.PublishAuthorized = authorized
			published := 0
			i.host.deps.Publish = func(context.Context, Run, func(Publication) error) (Publication, error) {
				published++
				return Publication{State: PublicationPublished, IssueURL: "https://github.com/example/ao/issues/7"}, nil
			}
			completeFollowup(t, i, `{"title":"Blank session","body":"The session stayed blank."}`)
			run := i.snapshot()
			if run.State != "needs_input" || run.PendingInput != nil || run.Draft == nil || published != map[bool]int{false: 0, true: 1}[authorized] {
				t.Fatalf("draft did not leave the conversation open: %#v, publications=%d", run, published)
			}
			assertConversationLive(t, conv)
			readyFollowup(t, i)
			if err := i.reply(Command{Type: "reply", Text: "The session recovered after reopening it."}); err != nil {
				t.Fatal(err)
			}
			contextTurn(t, conv)
			completeFollowup(t, i, `{"title":"Blank session","body":"The session recovered after reopening it."}`)
			if authorized && (published != 1 || i.snapshot().Publication.IssueURL == "") {
				t.Fatal("a follow-up draft created another publication or lost the existing link")
			}
		})
	}
}

func TestConversationQueuedCorrectionPrecedesAutomaticPublication(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprintf("command-inbox=%v", buffered), func(t *testing.T) {
			i, conv := followupInvestigation(t)
			i.run.Request.PublishAuthorized = true
			var published []Draft
			i.host.deps.Publish = func(_ context.Context, run Run, _ func(Publication) error) (Publication, error) {
				published = append(published, *run.Draft)
				return Publication{State: PublicationPublished, IssueURL: "https://github.com/example/ao/issues/8"}, nil
			}
			correction := Command{ID: "correction", Type: "reply", Text: "Correction: the app opened; only the session was blank."}
			if buffered {
				i.replies <- correction
			} else if err := i.reply(correction); err != nil {
				t.Fatal(err)
			}
			completeFollowup(t, i, `{"title":"App did not open","body":"The app failed to open."}`)
			if len(published) != 0 || conv.turns.Load() != 1 || len(i.queuedHumanText) != 1 {
				t.Fatal("the old draft overtook a correction already accepted by the host")
			}
			readyFollowup(t, i)
			if msg := contextTurn(t, conv); msg.Text != correction.Text || !msg.AuthoredByUser {
				t.Fatalf("the selected agent did not receive the correction: %#v", msg)
			}
			if len(published) != 0 {
				t.Fatal("dispatching a correction published the old draft")
			}
			completeFollowup(t, i, `{"title":"Blank session","body":"The app opened, but its session was blank."}`)
			if len(published) != 1 || published[0].Title != "Blank session" || !strings.Contains(published[0].Body, "app opened") {
				t.Fatalf("publication did not wait for the selected agent's revised draft: %#v", published)
			}
		})
	}
}

func TestConversationHumanQueueIsBoundedAndClearedOnStop(t *testing.T) {
	i, conv := followupInvestigation(t)
	text := strings.Repeat("x", maxReplyBytes)
	for count := 0; count < maxQueuedHumanMessages; count++ {
		if err := i.reply(Command{Type: "reply", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if err := i.reply(Command{Type: "reply", Text: "one more"}); err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Fatalf("queue overflow was not reported honestly: %v", err)
	}
	if len(i.queuedHumanText) != maxQueuedHumanMessages || i.queuedHumanBytes != maxQueuedHumanBytes || len(i.snapshot().Messages) != maxQueuedHumanMessages || conv.turns.Load() != 1 {
		t.Fatal("the queue bound changed accepted text or dispatched overlapping turns")
	}
	i.finish()
	awaitSignal(t, conv.terminated)
	if len(i.queuedHumanText) != 0 || i.queuedHumanBytes != 0 {
		t.Fatal("stopping retained undelivered message buffers")
	}
}

func TestConversationExplicitPublicationRetryWaitsForReady(t *testing.T) {
	i, _ := followupInvestigation(t)
	i.run.Request.PublishAuthorized = true
	publications := 0
	i.host.deps.Publish = func(context.Context, Run, func(Publication) error) (Publication, error) {
		publications++
		return Publication{State: PublicationAuthRequired}, nil
	}
	completeFollowup(t, i, `{"title":"Blank session","body":"The session stayed blank."}`)
	if publications != 1 {
		t.Fatal("the initial authorized draft was not attempted")
	}
	if err := i.reply(Command{Type: "retry-publication"}); err != nil {
		t.Fatal(err)
	}
	if publications != 1 {
		t.Fatal("publication retry ignored the Ready barrier")
	}
	readyFollowup(t, i)
	if publications != 2 || i.publicationRetry || i.snapshot().State != "needs_input" {
		t.Fatal("explicit retry did not use the live conversation")
	}
}

func TestConversationAuthRetryAssessesWithSameLiveAgent(t *testing.T) {
	i, conv := followupInvestigation(t)
	i.run.Request.PublishAuthorized = true
	publications := 0
	var frozen Publication
	i.host.deps.Publish = func(_ context.Context, run Run, _ func(Publication) error) (Publication, error) {
		publications++
		if publications == 1 {
			var err error
			frozen, err = preparePublication(run)
			if err != nil {
				return Publication{}, err
			}
			frozen.State = PublicationAuthRequired
			return frozen, nil
		}
		if run.Publication.Title != frozen.Title || run.Publication.Body != frozen.Body || run.Publication.Marker != frozen.Marker {
			t.Fatal("live auth recovery replaced the frozen publication")
		}
		pub := run.Publication
		pub.State, pub.IssueURL = PublicationPublished, "https://github.com/example/ao/issues/9"
		return pub, nil
	}
	completeFollowup(t, i, `{"title":"Blank session","body":"The session stayed blank."}`)
	readyFollowup(t, i)
	searches := 0
	i.host.deps.SearchReports = func(context.Context, string) (ReportSearch, error) {
		searches++
		return ReportSearch{Repository: canonicalRepository, Candidates: []ReportCandidate{}}, nil
	}
	if err := i.reply(Command{Type: "retry-publication"}); err != nil {
		t.Fatal(err)
	}
	assessment := contextTurn(t, conv)
	if searches != 1 || publications != 1 || assessment.AuthoredByUser || !strings.Contains(assessment.Text, "Assess these existing issues") || conv.turns.Load() != 2 {
		t.Fatalf("retry bypassed selected-agent candidate assessment: %#v", assessment)
	}
	completeFollowup(t, i, `{"title":"Refined title","body":"Refined draft after sign-in."}`)
	if publications != 2 || i.snapshot().Publication.IssueURL == "" || i.snapshot().State != "needs_input" {
		t.Fatal("live reassessment failed to resume the saved publication")
	}
	assertConversationLive(t, conv)
}

func TestConversationCorrectionSupersedesQueuedPublicationRetry(t *testing.T) {
	i, conv := followupInvestigation(t)
	i.run.Request.PublishAuthorized = true
	publications := 0
	i.host.deps.Publish = func(context.Context, Run, func(Publication) error) (Publication, error) {
		publications++
		return Publication{State: PublicationAuthRequired}, nil
	}
	completeFollowup(t, i, `{"title":"Blank session","body":"The session stayed blank."}`)
	if err := i.reply(Command{Type: "retry-publication"}); err != nil {
		t.Fatal(err)
	}
	if err := i.reply(Command{Type: "reply", Text: "Wait, I selected a different session."}); err != nil {
		t.Fatal(err)
	}
	readyFollowup(t, i)
	if msg := contextTurn(t, conv); !msg.AuthoredByUser || !strings.Contains(msg.Text, "different session") {
		t.Fatalf("publication retry overtook the correction: %#v", msg)
	}
	completeFollowup(t, i, "I will check which session was affected.")
	readyFollowup(t, i)
	if publications != 1 || i.publicationRetry {
		t.Fatal("superseded retry published a draft before correction review")
	}
}
