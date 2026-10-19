package triage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestFlowAssessesRelatedPRBeforeAutomaticPublication(t *testing.T) {
	dir := t.TempDir()
	conv := fakeConversation()
	f := newPublisherFixture(t)
	prURL := "https://github.com/" + canonicalRepository + "/pull/42"
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/search/issues":
			items := []any{}
			if strings.Contains(r.URL.Query().Get("q"), "is:pr is:closed") {
				items = append(items, map[string]any{"number": 42, "html_url": prURL})
			}
			f.json(w, 200, map[string]any{"total_count": len(items), "incomplete_results": false, "items": items})
			return true
		case "/repos/" + canonicalRepository + "/pulls/42":
			f.json(w, 200, map[string]any{"number": 42, "html_url": prURL, "title": "Fix blank session", "body": "Public candidate detail for semantic assessment.", "state": "closed", "merged": true, "merged_at": "2026-10-18T10:00:00Z", "merge_commit_sha": strings.Repeat("a", 40)})
			return true
		}
		return false
	}
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	deps.SearchReports, deps.Publish = f.publisher.SearchReports, f.publisher.Publish
	c := serveForTest(t, dir, deps)
	c.send(startCommand("start", true))
	run := c.response("start").Run
	c.state("running")
	contextTurn(t, conv)
	finishWith(conv, `{"title":"Blank session after opening","body":"The user opened a session and it remained blank."}`)
	c.wait(func(response Response) bool { return response.Run != nil && response.Run.SearchCompleted })
	if _, posts := f.counts(); posts != 0 {
		t.Fatal("published before the selected agent assessed the candidates")
	}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady}
	assessment := contextTurn(t, conv)
	if assessment.AuthoredByUser || !strings.Contains(assessment.Text, "Public candidate detail") || !strings.Contains(assessment.Text, "does not prove") {
		t.Fatalf("candidate details or release caveat missing: %s", assessment.Text)
	}
	finishWith(conv, `{"title":"Blank session after opening","body":"The user opened a session and it remained blank.","existingReportUrl":"`+prURL+`","existingReportReason":"This PR fixes the same blank-session symptom. Whether its fix shipped in this installed build is unverified."}`)
	published := *c.wait(func(response Response) bool {
		return response.Run != nil && response.Run.State == "needs_input" && response.Run.Publication.IssueURL != ""
	}).Run
	cancelLiveConversation(t, c, conv, run.ID)
	if published.Publication.State != PublicationDuplicate || published.Publication.IssueURL != prURL {
		t.Fatalf("did not return the matching PR: %#v", published.Publication)
	}
	if _, posts := f.counts(); posts != 0 {
		t.Fatal("a matched PR caused a public mutation")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "reports", "triage", "runs", run.ID, "manifest.json"))
	if err != nil || strings.Contains(string(raw), "Public candidate detail") {
		t.Fatalf("private search response entered the saved report: %v", err)
	}
}

func TestFlowNewIssueIsFiledAfterEmptyCandidateAssessment(t *testing.T) {
	f := newPublisherFixture(t)
	conv := fakeConversation()
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	deps.SearchReports, deps.Publish = f.publisher.SearchReports, f.publisher.Publish
	c := serveForTest(t, t.TempDir(), deps)
	c.send(startCommand("start", true))
	c.response("start")
	c.state("running")
	contextTurn(t, conv)
	draft := `{"title":"New blank session problem","body":"The user sees a blank session.","evidence":"Selected diagnostic fact only."}`
	finishWith(conv, draft)
	c.wait(func(response Response) bool { return response.Run != nil && response.Run.SearchCompleted })
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady}
	contextTurn(t, conv)
	finishWith(conv, draft)
	run := *c.wait(func(response Response) bool {
		return response.Run != nil && response.Run.State == "needs_input" && response.Run.Publication.IssueURL != ""
	}).Run
	cancelLiveConversation(t, c, conv, run.ID)
	if run.Publication.State != PublicationPublished || run.Publication.EvidenceURL == "" {
		t.Fatalf("report and selected evidence were not filed: %#v", run.Publication)
	}
	if _, posts := f.counts(); posts != 2 {
		t.Fatalf("expected one issue and one evidence comment, got %d mutations", posts)
	}
}

func TestFlowAuthRecoveryReassessesWithSameAgentAndFrozenPublication(t *testing.T) {
	dir := t.TempDir()
	store, err := openRunStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	run := testPublishRun()
	run.Version, run.CreatedAt, run.UpdatedAt, run.State = ProtocolVersion, time.Now().UTC(), time.Now().UTC(), "report_saved"
	run.Request.Selection = startCommand("", true).Start.Selection
	run.Publication, err = preparePublication(run)
	if err != nil {
		t.Fatal(err)
	}
	run.Publication.State = PublicationAuthRequired
	if err := store.save(run); err != nil {
		t.Fatal(err)
	}
	conv := fakeConversation()
	f := newPublisherFixture(t)
	var signedIn atomic.Bool
	deps := configuredDeps()
	deps.StartConversation = func(_ context.Context, selected Run) (ports.ChatConversation, error) {
		if selected.Request.Selection != run.Request.Selection || selected.ID != run.ID {
			return nil, errors.New("auth recovery changed the selected agent or report")
		}
		return conv, nil
	}
	deps.StoreGitHubCredential = func(_ context.Context, credential GitHubCredential) (GitHubIdentity, error) {
		if credential.AccessToken != "synthetic-private-credential" {
			return GitHubIdentity{}, errors.New("credential missing")
		}
		signedIn.Store(true)
		return GitHubIdentity{Login: "reporter", ID: 7}, nil
	}
	deps.SearchReports = func(ctx context.Context, query string) (ReportSearch, error) {
		if !signedIn.Load() {
			return ReportSearch{}, errors.New("not signed in")
		}
		return f.publisher.SearchReports(ctx, query)
	}
	deps.Publish = f.publisher.Publish
	c := serveForTest(t, dir, deps)
	c.send(Command{ID: "auth", Type: "github-auth-store", GitHubCredential: &GitHubCredential{AccessToken: "synthetic-private-credential"}})
	response := c.response("auth")
	raw, _ := json.Marshal(response)
	if response.Type != "github-auth-stored" || response.GitHubIdentity == nil || strings.Contains(string(raw), "synthetic-private-credential") {
		t.Fatalf("invalid auth response: %s", raw)
	}
	c.send(Command{ID: "retry", Type: "publish", RunID: run.ID})
	c.response("retry")
	c.state("running")
	if prompt := contextTurn(t, conv); !strings.Contains(prompt.Text, run.Draft.Body) {
		t.Fatal("reassessment lost the saved human report")
	}
	changedDraft := `{"title":"Changed title","body":"Changed body after sign-in"}`
	finishWith(conv, changedDraft)
	c.wait(func(response Response) bool { return response.Run != nil && response.Run.SearchCompleted })
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady}
	contextTurn(t, conv)
	finishWith(conv, changedDraft)
	final := *c.wait(func(response Response) bool {
		return response.Run != nil && response.Run.State == "needs_input" && response.Run.Publication.IssueURL != ""
	}).Run
	cancelLiveConversation(t, c, conv, run.ID)
	if final.Publication.Title != run.Publication.Title || final.Publication.Body != run.Publication.Body || final.Publication.Marker != run.Publication.Marker {
		t.Fatalf("auth recovery replaced frozen intent: %#v", final.Publication)
	}
}

func TestFlowGlobalSessionChoiceCollectsPrivateContextAutomatically(t *testing.T) {
	dir := t.TempDir()
	conv := fakeConversation()
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	deps.ListSessions = func(context.Context) ([]DiagnosticSession, error) {
		return []DiagnosticSession{{ID: "affected-session", Label: "Checkout review", Harness: "test", State: "working"}}, nil
	}
	deps.ValidateSession = func(_ context.Context, id string) error {
		if id != "affected-session" {
			return errors.New("session was not discovered")
		}
		return nil
	}
	deps.Collect = func(_ context.Context, origin Origin) ([]Evidence, error) {
		if origin.SessionID == "" {
			return nil, nil
		}
		return []Evidence{{ID: "retained", Kind: "session", Status: "collected", Values: map[string]any{"sources": []string{"session-history", "provider-log"}}}}, nil
	}
	var reads atomic.Int32
	deps.ReadContext = func(_ context.Context, origin Origin, read ContextRead) (Evidence, error) {
		if origin.SessionID != "affected-session" {
			return Evidence{}, errors.New("private context accessed before selection")
		}
		reads.Add(1)
		return Evidence{Status: "collected", Source: read.Source, Text: "Private retained conversation content."}, nil
	}
	c := serveForTest(t, dir, deps)
	c.send(startCommand("start", false))
	run := c.response("start").Run
	c.state("running")
	if prompt := contextTurn(t, conv); !strings.Contains(prompt.Text, "Checkout review") || reads.Load() != 0 {
		t.Fatal("global entry did not discover candidates before private reads")
	}
	finishWith(conv, `{"session":"affected-session"}`)
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady}
	if continuation := contextTurn(t, conv); !strings.Contains(continuation.Text, "Private retained conversation content") || continuation.AuthoredByUser || reads.Load() != 2 {
		t.Fatalf("selected context was not injected automatically: %s", continuation.Text)
	}
	finishWith(conv, `{"title":"Session stuck","body":"The selected session is stuck."}`)
	c.state("needs_input")
	cancelLiveConversation(t, c, conv, run.ID)
	raw, err := os.ReadFile(filepath.Join(dir, "reports", "triage", "runs", run.ID, "manifest.json"))
	if err != nil || strings.Contains(string(raw), "Private retained conversation content") {
		t.Fatalf("raw history entered public report projection: %v", err)
	}
}

func TestFlowNativeUncertaintyDeclinesEnumAndContinuesAfterReady(t *testing.T) {
	conv := fakeConversation()
	deps := configuredDeps()
	deps.StartConversation = func(context.Context, Run) (ports.ChatConversation, error) { return conv, nil }
	inputs := make(chan ports.ChatInputResponse, 1)
	conv.inputHook = func(_ string, reply ports.ChatInputResponse) error { inputs <- reply; return nil }
	c := serveForTest(t, t.TempDir(), deps)
	c.send(startCommand("start", false))
	run := c.response("start").Run
	c.state("running")
	contextTurn(t, conv)
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventInputRequested, RequestID: "native", Input: &ports.ChatInputRequest{Schema: map[string]any{"properties": map[string]any{"choice": map[string]any{"type": "string", "enum": []any{"Known option"}}}}}}
	c.state("needs_input")
	c.send(Command{ID: "answer", Type: "reply", RunID: run.ID, RequestID: "native", Text: "I'm not sure"})
	c.response("answer")
	if reply := <-inputs; reply.Action != ports.ChatInputActionDecline || len(reply.Content) != 0 {
		t.Fatalf("invented an accepted native enum answer: %#v", reply)
	}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventTurnCompleted, TurnState: domain.TurnStateCompleted}
	c.state("dispatching")
	select {
	case <-conv.started:
		t.Fatal("human uncertainty was sent before the native Ready barrier")
	default:
	}
	conv.events <- ports.ChatEvent{Kind: ports.ChatEventControllerState, ControllerState: ports.ChatControllerReady}
	if prompt := contextTurn(t, conv); !strings.Contains(prompt.Text, "I'm not sure") || prompt.AuthoredByUser {
		t.Fatalf("human uncertainty lost or mislabeled: %#v", prompt)
	}
}

func TestFlowNativeEnumValidationAndOptionalText(t *testing.T) {
	input := &ports.ChatInputRequest{Schema: map[string]any{"required": []any{"action"}, "properties": map[string]any{
		"action": map[string]any{"type": "string", "enum": []any{"Press Enter", "Send typed text"}},
		"text":   map[string]any{"type": "string"},
	}}}
	if _, err := inputContent(input, map[string][]string{"action": {"I'm not sure"}}); err == nil {
		t.Fatal("accepted an invented native enum")
	}
	if _, err := inputContent(input, map[string][]string{"text": {"typed text"}}); err == nil {
		t.Fatal("accepted missing required action")
	}
	if content, err := inputContent(input, map[string][]string{"action": {"Press Enter"}}); err != nil || content["action"] != "Press Enter" {
		t.Fatalf("optional text became mandatory: %v %#v", err, content)
	}
}

func TestFlowReopenReapsInterruptedProviderUnderClaimAndKeepsSavedReport(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleaned", true: "cleanup error retained"}[cleanupFails], func(t *testing.T) {
			dir := t.TempDir()
			store, err := openRunStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			run := Run{ID: "interrupted-agent", Version: ProtocolVersion, State: "running", CreatedAt: now, UpdatedAt: now,
				ProviderConversationID: "owned-native-provider", Publication: Publication{State: PublicationIssueUnknown, Marker: "frozen-intent"}}
			if err := store.save(run); err != nil {
				t.Fatal(err)
			}
			var cleaned atomic.Int32
			deps := Dependencies{RecoverConversation: func(ctx context.Context, saved Run) error {
				cleaned.Add(1)
				if saved.ID != run.ID || saved.ProviderConversationID != run.ProviderConversationID {
					return errors.New("recovery lost provider identity")
				}
				if _, ok := ctx.Deadline(); !ok {
					return errors.New("recovery has no timeout")
				}
				if release, _, err := store.claimRun("other-agent"); !errors.Is(err, errRunOwned) {
					if release != nil {
						release()
					}
					return errors.New("recovery did not hold global ownership")
				}
				if cleanupFails {
					return errors.New("owned provider did not stop")
				}
				return nil
			}}
			c := serveForTest(t, dir, deps)
			c.send(Command{ID: "get", Type: "get", RunID: run.ID})
			response := c.response("get")
			if response.Run == nil || response.Run.State != "interrupted" || response.Run.Publication != run.Publication || cleaned.Load() != 1 {
				t.Fatalf("reopen did not recover the saved report and provider: %#v", response)
			}
			if cleanupFails != strings.Contains(response.Run.Error, "Provider cleanup incomplete") {
				t.Fatalf("cleanup result was hidden or fabricated: %s", response.Run.Error)
			}
		})
	}
}
