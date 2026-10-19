package triage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/service/githubpat"
)

type publisherRoundTripper func(*http.Request) (*http.Response, error)

func (fn publisherRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

type publisherFixture struct {
	t         *testing.T
	server    *httptest.Server
	publisher *Publisher
	repo      string
	mu        sync.Mutex
	requests  []string
	posts     int
	saved     []Publication
	hook      func(http.ResponseWriter, *http.Request) bool
}

func newPublisherFixture(t *testing.T) *publisherFixture {
	t.Helper()
	f := &publisherFixture{t: t, repo: canonicalRepository}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		if r.Method == http.MethodPost {
			f.posts++
		}
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("missing synthetic bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		if f.hook != nil && f.hook(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/repos/"+canonicalRepository:
			f.json(w, 200, map[string]any{"full_name": f.repo, "url": f.server.URL + "/repos/" + f.repo, "html_url": "https://github.com/" + f.repo, "private": false})
		case r.URL.Path == "/user":
			f.json(w, 200, map[string]any{"login": "reporter", "id": 7})
		case r.URL.Path == "/search/issues":
			f.json(w, 200, map[string]any{"total_count": 0, "incomplete_results": false, "items": []any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/"+f.repo+"/issues":
			f.json(w, 201, f.issue("", ""))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			f.json(w, 201, map[string]any{"html_url": f.issueURL() + "#issuecomment-44"})
		default:
			f.json(w, 404, map[string]any{"message": "unexpected route"})
		}
	}))
	t.Cleanup(f.server.Close)
	f.publisher = &Publisher{HTTPClient: f.server.Client(), BaseURL: f.server.URL, TokenSource: func(context.Context) (string, error) { return "synthetic-token", nil }}
	return f
}
func (f *publisherFixture) json(w http.ResponseWriter, status int, value any) {
	f.t.Helper()
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		f.t.Error(err)
	}
}
func (f *publisherFixture) issueURL() string { return "https://github.com/" + f.repo + "/issues/42" }
func (f *publisherFixture) issue(title, body string) map[string]any {
	return map[string]any{"number": 42, "title": title, "body": body, "html_url": f.issueURL(), "user": map[string]any{"login": "reporter", "id": 7}}
}
func (f *publisherFixture) save(pub Publication) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, pub)
	return nil
}
func (f *publisherFixture) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests), f.posts
}
func testPublishRun() Run {
	return Run{ID: "report-run", Request: StartRequest{PublishAuthorized: true}, Draft: &Draft{Title: "Desktop crashes when opening a session", Body: "I opened a session and the desktop closed.", Evidence: "Observed exit status 1."}}
}

func TestPublisherRequiresAuthorizationAndValidDraftBeforeAnyRequests(t *testing.T) {
	for _, kind := range []string{"unauthorized", "empty title", "empty body", "missing draft", "missing save", "private key"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublisherFixture(t)
			run := testPublishRun()
			save := f.save
			switch kind {
			case "unauthorized":
				run.Request.PublishAuthorized = false
			case "empty title":
				run.Draft.Title = " "
			case "empty body":
				run.Draft.Body = " "
			case "missing draft":
				run.Draft = nil
			case "missing save":
				save = nil
			case "private key":
				run.Draft.Evidence = "-----BEGIN AO TEST PRIVATE KEY-----\nprivate file contents"
			}
			if _, err := f.publisher.Publish(context.Background(), run, save); err == nil {
				t.Fatal("expected rejection")
			}
			if requests, _ := f.counts(); requests != 0 {
				t.Fatalf("sent %d requests", requests)
			}
		})
	}
}

func TestPublisherResolvesCanonicalRenameAndFreezesBeforePosting(t *testing.T) {
	f := newPublisherFixture(t)
	f.repo = "AOAgents/agent-orchestrator"
	run := testPublishRun()
	run.Draft.Title = `Desktop crash repo:someone/else is:pr`
	run.Draft.Body += " Token: ghp_abcdefghijklmnopqrstuvwxyz123456"
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/search/issues" {
			q := r.URL.Query().Get("q")
			if !strings.HasPrefix(q, "repo:"+f.repo+" in:title ") || strings.Contains(q, "repo:someone") || strings.Contains(q, "is:pr") || strings.Contains(q, "is:open") {
				t.Errorf("unsafe query %q", q)
			}
		}
		if r.Method == http.MethodPost {
			f.mu.Lock()
			saved := f.saved[len(f.saved)-1]
			f.mu.Unlock()
			if saved.Identity != "reporter#7" || saved.Repository != f.repo {
				t.Errorf("intent has no frozen identity/repo: %+v", saved)
			}
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			if strings.HasSuffix(r.URL.Path, "/comments") {
				if saved.State != PublicationEvidencePending || payload["body"] != saved.Evidence || saved.IssueURL == "" || !strings.HasPrefix(payload["body"], "### AO-collected evidence") {
					t.Error("evidence was not frozen separately")
				}
			} else {
				if saved.State != PublicationIssuePending || payload["title"] != saved.Title || payload["body"] != saved.Body || strings.Contains(saved.Body, "ghp_") || strings.Contains(saved.Body, run.Draft.Evidence) {
					t.Error("report was not sanitized and frozen before POST")
				}
			}
		}
		return false
	}
	pub, err := f.publisher.Publish(context.Background(), run, f.save)
	if err != nil || pub.State != PublicationPublished || pub.IssueURL != f.issueURL() || pub.EvidenceURL == "" {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	f.mu.Lock()
	first := f.requests[0]
	f.mu.Unlock()
	if first != "GET /repos/"+canonicalRepository {
		t.Fatalf("resolution started at %s", first)
	}
}

func TestPublisherReadsDuplicateAndReturnsWithoutComment(t *testing.T) {
	f := newPublisherFixture(t)
	run := testPublishRun()
	reads := 0
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/search/issues":
			f.json(w, 200, map[string]any{"total_count": 1, "items": []any{f.issue(run.Draft.Title, "search snippets are not trusted")}})
			return true
		case "/repos/" + f.repo + "/issues/42":
			reads++
			f.json(w, 200, f.issue(run.Draft.Title, run.Draft.Body))
			return true
		}
		return false
	}
	pub, err := f.publisher.Publish(context.Background(), run, f.save)
	if err != nil || pub.State != PublicationDuplicate || pub.IssueURL != f.issueURL() || reads != 1 {
		t.Fatalf("pub=%+v err=%v reads=%d", pub, err, reads)
	}
	if _, posts := f.counts(); posts != 0 {
		t.Fatalf("posted %d mutations for duplicate", posts)
	}
}

func TestPublisherVerifiesSelectedIssueOrMergedFixWithoutPosting(t *testing.T) {
	for _, isPR := range []bool{false, true} {
		t.Run(map[bool]string{false: "issue", true: "merged fix"}[isPR], func(t *testing.T) {
			f := newPublisherFixture(t)
			run := testPublishRun()
			run.Draft.ExistingReportURL = f.issueURL()
			run.Draft.ExistingReportReason = "Same session-launch crash on the affected 0.10.0 build. A merged fix is not proof that this build includes it."
			route := "/issues/42"
			if isPR {
				run.Draft.ExistingReportURL = "https://github.com/" + f.repo + "/pull/42"
				route = "/pulls/42"
			}
			reads := 0
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/search/issues" {
					t.Error("selected candidate should be verified directly")
				}
				if r.URL.Path == "/repos/"+f.repo+route {
					reads++
					issue := f.issue("A differently worded report", "Related symptom on the installed build")
					issue["html_url"], issue["state"] = run.Draft.ExistingReportURL, "closed"
					if isPR {
						issue["merged"], issue["merge_commit_sha"] = true, strings.Repeat("a", 40)
					}
					f.json(w, 200, issue)
					return true
				}
				return false
			}
			pub, err := f.publisher.Publish(context.Background(), run, f.save)
			if err != nil || pub.State != PublicationDuplicate || pub.IssueURL != run.Draft.ExistingReportURL || pub.ExistingReportReason != run.Draft.ExistingReportReason || reads != 1 {
				t.Fatalf("pub=%+v err=%v reads=%d", pub, err, reads)
			}
			if _, posts := f.counts(); posts != 0 {
				t.Fatalf("selected report caused %d mutations", posts)
			}
		})
	}
}

func TestPublisherRejectsInvalidSelectedReportsWithoutPosting(t *testing.T) {
	for _, mode := range []string{"no reason", "foreign repo", "foreign URL", "API mismatch", "missing report", "wrong account"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublisherFixture(t)
			run := testPublishRun()
			run.Draft.ExistingReportURL = f.issueURL()
			run.Draft.ExistingReportReason = "The same session launch crash affects this installed build."
			switch mode {
			case "no reason":
				run.Draft.ExistingReportReason = "duplicate"
			case "foreign repo":
				run.Draft.ExistingReportURL = "https://github.com/another/repo/issues/42"
			case "foreign URL":
				run.Draft.ExistingReportURL = "https://example.invalid/issues/42"
			case "wrong account":
				prepared, err := preparePublication(run)
				if err != nil {
					t.Fatal(err)
				}
				prepared.Identity = "someoneelse#8"
				run.Publication = prepared
			}
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/repos/"+f.repo+"/issues/42" && mode == "API mismatch" {
					issue := f.issue("Matching report", "Observed failure")
					issue["state"], issue["number"] = "open", 88
					f.json(w, 200, issue)
					return true
				}
				return false
			}
			pub, err := f.publisher.Publish(context.Background(), run, f.save)
			if err == nil || pub.IssueURL != "" {
				t.Fatalf("pub=%+v err=%v", pub, err)
			}
			if _, posts := f.counts(); posts != 0 {
				t.Fatalf("invalid candidate caused %d mutations", posts)
			}
		})
	}
}

func TestPublisherFreezesSelectedReportAcrossAuthRetry(t *testing.T) {
	f := newPublisherFixture(t)
	run := testPublishRun()
	run.Draft.ExistingReportURL = f.issueURL()
	run.Draft.ExistingReportReason = "The same session crash affects the installed 0.10.0 build."
	f.publisher.TokenSource = func(context.Context) (string, error) { return "", os.ErrNotExist }
	pub, err := f.publisher.Publish(context.Background(), run, f.save)
	if err == nil || pub.State != PublicationAuthRequired {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	run.Publication = pub
	run.Draft.ExistingReportURL = "https://github.com/" + f.repo + "/issues/99"
	run.Draft.ExistingReportReason = "A changed draft must not replace the selected frozen report."
	f.publisher.TokenSource = func(context.Context) (string, error) { return "synthetic-token", nil }
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/repos/"+f.repo+"/issues/42" {
			issue := f.issue("Existing crash report", "Observed failure")
			issue["state"] = "open"
			f.json(w, 200, issue)
			return true
		}
		return false
	}
	pub, err = f.publisher.Publish(context.Background(), run, f.save)
	if err != nil || pub.State != PublicationDuplicate || pub.IssueURL != f.issueURL() || pub.ExistingReportURL != f.issueURL() {
		t.Fatalf("changed frozen selection: pub=%+v err=%v", pub, err)
	}
}

func TestPublisherUnknownPOSTReconcilesBeforeSelectedReport(t *testing.T) {
	f := newPublisherFixture(t)
	run := testPublishRun()
	prepared, err := preparePublication(run)
	if err != nil {
		t.Fatal(err)
	}
	prepared.State = PublicationIssueUnknown
	run.Publication = prepared
	run.Draft.ExistingReportURL = f.issueURL()
	run.Draft.ExistingReportReason = "The same session crash affects this installed version."
	pub, err := f.publisher.Publish(context.Background(), run, f.save)
	if err == nil || pub.State != PublicationIssueUnknown || pub.IssueURL != "" || !strings.Contains(pub.Error, "No second issue") {
		t.Fatalf("bypassed uncertain POST: pub=%+v err=%v", pub, err)
	}
	if _, posts := f.counts(); posts != 0 {
		t.Fatalf("uncertain intent caused %d more mutations", posts)
	}
}

func TestRefreshPublicationAssessmentChangesOnlyDecisionAfterSignIn(t *testing.T) {
	run := testPublishRun()
	prepared, err := preparePublication(run)
	if err != nil {
		t.Fatal(err)
	}
	prepared.State, prepared.Identity, prepared.Repository = PublicationAuthRequired, "reporter#7", canonicalRepository
	run.Publication = prepared
	run.Draft.Title, run.Draft.Body, run.Draft.Evidence = "Changed title", "Changed body", "Changed evidence"
	run.Draft.ExistingReportURL = "https://github.com/" + canonicalRepository + "/pull/42"
	run.Draft.ExistingReportReason = "This merged fix matches the installed build's session crash; release availability remains unverified."
	updated, err := RefreshPublicationAssessment(run)
	if err != nil || updated.ExistingReportURL != run.Draft.ExistingReportURL || updated.ExistingReportReason != run.Draft.ExistingReportReason {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	updated.ExistingReportURL, updated.ExistingReportReason = "", ""
	if !reflect.DeepEqual(updated, prepared) {
		t.Fatal("assessment changed frozen publication content or identity")
	}
	for _, state := range []string{PublicationPrepared, PublicationIssuePending, PublicationIssueUnknown, PublicationIssueFailed, PublicationEvidencePending, PublicationPublished} {
		run.Publication.State = state
		if _, err := RefreshPublicationAssessment(run); err == nil {
			t.Fatalf("allowed assessment change in %s", state)
		}
	}
	run.Publication.State, run.Publication.IssueURL = PublicationAuthRequired, "https://github.com/"+canonicalRepository+"/issues/42"
	if _, err := RefreshPublicationAssessment(run); err == nil {
		t.Fatal("allowed assessment change after an issue result")
	}
}

func TestPublisherUncertainIssueDoesNotRetryAfterRestartAndEmptySearch(t *testing.T) {
	f := newPublisherFixture(t)
	run := testPublishRun()
	run.Draft.Evidence = ""
	transport := f.server.Client().Transport
	f.publisher.HTTPClient = &http.Client{Transport: publisherRoundTripper(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		if err == nil && r.Method == http.MethodPost {
			_ = response.Body.Close()
			return nil, io.ErrUnexpectedEOF
		}
		return response, err
	})}
	pub, err := f.publisher.Publish(context.Background(), run, f.save)
	if err == nil || pub.State != PublicationIssueUnknown {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	// Simulate a process restart from the durably stored pending/unknown state.
	run.Publication = pub
	f.publisher.HTTPClient = f.server.Client()
	pub, err = f.publisher.Publish(context.Background(), run, f.save)
	if err == nil || pub.State != PublicationIssueUnknown || !strings.Contains(pub.Error, "No second issue") {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	if _, posts := f.counts(); posts != 1 {
		t.Fatalf("created %d issues after empty marker search", posts)
	}
	// Once GitHub indexes the original marker, recovery returns the original.
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		issue := f.issue(pub.Title, pub.Body)
		switch r.URL.Path {
		case "/search/issues":
			f.json(w, 200, map[string]any{"total_count": 1, "items": []any{issue}})
			return true
		case "/repos/" + f.repo + "/issues/42":
			f.json(w, 200, issue)
			return true
		}
		return false
	}
	run.Publication = pub
	pub, err = f.publisher.Publish(context.Background(), run, f.save)
	if err != nil || pub.State != PublicationPublished || pub.IssueURL != f.issueURL() {
		t.Fatalf("recovery=%+v err=%v", pub, err)
	}
	if _, posts := f.counts(); posts != 1 {
		t.Fatalf("recovery posted %d times", posts)
	}
}

func TestPublisherIssueAndCommentRecoverIndependently(t *testing.T) {
	f := newPublisherFixture(t)
	run := testPublishRun()
	transport := f.server.Client().Transport
	f.publisher.HTTPClient = &http.Client{Transport: publisherRoundTripper(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		if err == nil && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
			_ = response.Body.Close()
			return nil, io.ErrUnexpectedEOF
		}
		return response, err
	})}
	pub, err := f.publisher.Publish(context.Background(), run, f.save)
	if err == nil || pub.State != PublicationEvidenceUnknown || pub.IssueURL == "" {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	f.publisher.HTTPClient = f.server.Client()
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments") {
			f.json(w, 200, []any{map[string]any{"body": pub.Evidence, "html_url": f.issueURL() + "#issuecomment-44", "user": map[string]any{"login": "reporter", "id": 7}}})
			return true
		}
		return false
	}
	run.Publication = pub
	pub, err = f.publisher.Publish(context.Background(), run, f.save)
	if err != nil || pub.State != PublicationPublished || pub.EvidenceURL == "" {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	if _, posts := f.counts(); posts != 2 {
		t.Fatalf("recovery posted %d times", posts)
	}
}

func TestPublisherDefiniteCommentFailureCanRetryWithoutSecondIssue(t *testing.T) {
	f := newPublisherFixture(t)
	run := testPublishRun()
	rejected := false
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if !rejected && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments") {
			rejected = true
			f.json(w, 422, map[string]any{"message": "validation failed"})
			return true
		}
		return false
	}
	pub, err := f.publisher.Publish(context.Background(), run, f.save)
	if err == nil || pub.State != PublicationEvidenceFailed || pub.IssueURL == "" {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	run.Publication = pub
	run.Draft.Title = "Changed title must not replace the frozen report"
	pub, err = f.publisher.Publish(context.Background(), run, f.save)
	if err != nil || pub.State != PublicationPublished || pub.Title == run.Draft.Title {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	issues := 0
	for _, request := range f.requests {
		if request == "POST /repos/"+f.repo+"/issues" {
			issues++
		}
	}
	if issues != 1 {
		t.Fatalf("created %d issues", issues)
	}
}

func TestPublisherCancellationPreservesUnknownMutation(t *testing.T) {
	f := newPublisherFixture(t)
	run := testPublishRun()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport := f.server.Client().Transport
	f.publisher.HTTPClient = &http.Client{Transport: publisherRoundTripper(func(r *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(r)
		if err == nil && r.Method == http.MethodPost {
			_ = response.Body.Close()
			cancel()
			return nil, context.Canceled
		}
		return response, err
	})}
	pub, err := f.publisher.Publish(ctx, run, f.save)
	if err == nil || pub.State != PublicationIssueUnknown {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	if _, posts := f.counts(); posts != 1 {
		t.Fatalf("posts=%d", posts)
	}
}

func TestPublisherClassifiesAuthRateLimitAndNetworkFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		want   string
	}{{"credentials", 401, PublicationAuthRequired}, {"forbidden", 403, PublicationBlocked}, {"rate limit", 429, PublicationBlocked}, {"offline server", 503, PublicationBlocked}} {
		t.Run(test.name, func(t *testing.T) {
			f := newPublisherFixture(t)
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/user" {
					f.json(w, test.status, map[string]any{"message": "synthetic failure"})
					return true
				}
				return false
			}
			pub, err := f.publisher.Publish(context.Background(), testPublishRun(), f.save)
			if err == nil || pub.State != test.want || pub.Title == "" || !strings.Contains(pub.Error, "synthetic failure") {
				t.Fatalf("pub=%+v err=%v", pub, err)
			}
			if _, posts := f.counts(); posts != 0 {
				t.Fatalf("posts=%d", posts)
			}
		})
	}
	t.Run("network", func(t *testing.T) {
		f := newPublisherFixture(t)
		f.publisher.HTTPClient = &http.Client{Transport: publisherRoundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("synthetic network unavailable") })}
		pub, err := f.publisher.Publish(context.Background(), testPublishRun(), f.save)
		if err == nil || pub.State != PublicationBlocked || !strings.Contains(pub.Error, "synthetic network unavailable") {
			t.Fatalf("pub=%+v err=%v", pub, err)
		}
	})
}

func TestPublisherBlocksSearchFailureAndTruncation(t *testing.T) {
	for _, mode := range []string{"failed", "truncated", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublisherFixture(t)
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/search/issues" {
					return false
				}
				if mode == "failed" {
					f.json(w, 503, map[string]any{"message": "search unavailable"})
				} else {
					f.json(w, 200, map[string]any{"total_count": 31, "incomplete_results": mode == "incomplete", "items": []any{}})
				}
				return true
			}
			pub, err := f.publisher.Publish(context.Background(), testPublishRun(), f.save)
			if err == nil || !strings.Contains(pub.Error, "duplicate search") {
				t.Fatalf("pub=%+v err=%v", pub, err)
			}
			if _, posts := f.counts(); posts != 0 {
				t.Fatalf("posts=%d", posts)
			}
		})
	}
}

func TestPublisherRejectsForeignOriginsAndForeignResultURLs(t *testing.T) {
	for _, mode := range []string{"redirect", "canonical", "issue", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublisherFixture(t)
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				switch mode {
				case "redirect":
					if r.URL.Path == "/user" {
						http.Redirect(w, r, "https://example.invalid/user", http.StatusFound)
						return true
					}
				case "canonical":
					if r.URL.Path == "/repos/"+canonicalRepository {
						f.json(w, 200, map[string]any{"full_name": f.repo, "url": "https://evil.invalid/repos/" + f.repo, "html_url": "https://github.com/" + f.repo})
						return true
					}
				case "issue":
					if r.Method == http.MethodPost {
						f.json(w, 201, map[string]any{"number": 42, "html_url": "https://evil.invalid/" + f.repo + "/issues/42"})
						return true
					}
				case "duplicate":
					if r.URL.Path == "/search/issues" {
						issue := f.issue("", "")
						issue["html_url"] = "https://github.com/another/repo/issues/42"
						f.json(w, 200, map[string]any{"total_count": 1, "items": []any{issue}})
						return true
					}
				}
				return false
			}
			pub, err := f.publisher.Publish(context.Background(), testPublishRun(), f.save)
			if err == nil || pub.IssueURL != "" {
				t.Fatalf("pub=%+v err=%v", pub, err)
			}
			if mode == "issue" && pub.State != PublicationIssueUnknown {
				t.Fatalf("state=%s", pub.State)
			}
		})
	}
}

func TestPublisherSaveFailurePreventsMutation(t *testing.T) {
	f := newPublisherFixture(t)
	pub, err := f.publisher.Publish(context.Background(), testPublishRun(), func(pub Publication) error {
		if pub.State == PublicationIssuePending {
			return errors.New("synthetic disk failure")
		}
		return f.save(pub)
	})
	if err == nil || pub.State != PublicationIssuePending {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	if _, posts := f.counts(); posts != 0 {
		t.Fatalf("posts=%d", posts)
	}
}

func TestPublisherDefiniteIssueRejectionRetriesFrozenIntent(t *testing.T) {
	f := newPublisherFixture(t)
	run := testPublishRun()
	run.Draft.Evidence = ""
	rejected := false
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if !rejected && r.Method == http.MethodPost {
			rejected = true
			f.json(w, 422, map[string]any{"message": "validation failed"})
			return true
		}
		return false
	}
	pub, err := f.publisher.Publish(context.Background(), run, f.save)
	if err == nil || pub.State != PublicationIssueFailed || pub.IssueURL != "" {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	original := pub
	run.Publication = pub
	run.Draft.Title = "A later title"
	run.Draft.Body = "Later observations"
	pub, err = f.publisher.Publish(context.Background(), run, f.save)
	if err != nil || pub.State != PublicationPublished || pub.Title != original.Title || pub.Body != original.Body || pub.Marker != original.Marker {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	if _, posts := f.counts(); posts != 2 {
		t.Fatalf("POST attempts=%d", posts)
	}
}

func TestPublisherMissingTokenRetainsPreparedReport(t *testing.T) {
	f := newPublisherFixture(t)
	f.publisher.TokenSource = func(context.Context) (string, error) { return "", githubpat.ErrInvalidCredentials }
	pub, err := f.publisher.Publish(context.Background(), testPublishRun(), f.save)
	if err == nil || pub.State != PublicationAuthRequired || pub.Title == "" || pub.Body == "" || pub.Marker == "" || pub.Evidence == "" {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	if requests, _ := f.counts(); requests != 0 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestPublisherScrubsExactCredentialFromSourceFailure(t *testing.T) {
	f := newPublisherFixture(t)
	f.publisher.TokenSource = func(context.Context) (string, error) {
		return "synthetic-token", errors.New("source failed for synthetic-token")
	}
	pub, err := f.publisher.Publish(context.Background(), testPublishRun(), f.save)
	if err == nil || strings.Contains(pub.Error, "synthetic-token") || strings.Contains(err.Error(), "synthetic-token") {
		t.Fatalf("credential escaped error: %v", err)
	}
	if requests, _ := f.counts(); requests != 0 {
		t.Fatal("requested GitHub after credential source failure")
	}
}

func TestPublisherPendingIssuePreservesUnknownWhenCredentialsFail(t *testing.T) {
	f := newPublisherFixture(t)
	run := testPublishRun()
	pub, err := preparePublication(run)
	if err != nil {
		t.Fatal(err)
	}
	pub.State, pub.Repository, pub.Identity = PublicationIssuePending, canonicalRepository, "reporter#7"
	run.Publication = pub
	f.publisher.TokenSource = func(context.Context) (string, error) { return "", githubpat.ErrInvalidCredentials }
	pub, err = f.publisher.Publish(context.Background(), run, f.save)
	if err == nil || pub.State != PublicationIssueUnknown {
		t.Fatalf("pub=%+v err=%v", pub, err)
	}
	if requests, _ := f.counts(); requests != 0 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestPublisherRejectsInvalidDraftEvenWithFrozenIntent(t *testing.T) {
	f := newPublisherFixture(t)
	run := testPublishRun()
	pub, err := preparePublication(run)
	if err != nil {
		t.Fatal(err)
	}
	run.Publication = pub
	run.Draft.Title = strings.Repeat("x", 257)
	if _, err := f.publisher.Publish(context.Background(), run, f.save); err == nil {
		t.Fatal("invalid draft accepted")
	}
	if requests, _ := f.counts(); requests != 0 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestPublisherRejectsIncompleteUncertainStateWithoutNewPOST(t *testing.T) {
	for _, mode := range []string{"missing marker", "missing issue"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublisherFixture(t)
			run := testPublishRun()
			pub, err := preparePublication(run)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "missing marker" {
				pub.State, pub.Marker = PublicationIssueUnknown, ""
			} else {
				pub.State = PublicationEvidenceUnknown
			}
			run.Publication = pub
			if _, err := f.publisher.Publish(context.Background(), run, f.save); err == nil {
				t.Fatal("incomplete uncertainty was discarded")
			}
			if requests, _ := f.counts(); requests != 0 {
				t.Fatalf("requests=%d", requests)
			}
		})
	}
}
