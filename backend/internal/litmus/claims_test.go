package litmus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

func TestClaimInPRNormalization(t *testing.T) {
	words := []string{"Model menu", "Clicking **Other models** keeps the menu open.\nUse `Menu` to pick a model:\nIt's “ready” — always.\n[Read the guide](https://example.com/guide).\nTwo words"}
	for _, sentence := range []string{
		"Clicking Other models keeps the menu open.",
		"Clicking `Other models` keeps the menu open.",
		"CLICKING OTHER MODELS KEEPS THE MENU OPEN",
		"Clicking Other models keeps the menu open:",
		"keeps \n the\tmenu open!", "Use Menu to pick a model", `It's "ready" - always`,
		"Read the guide", "[Read the guide](https://example.com/guide)", "Model menu", "Two words",
	} {
		if !claimInPR(sentence, words) {
			t.Errorf("refused PR words %q", sentence)
		}
	}
	for _, sentence := range []string{"", "menu open", "Other models", "Reviewers say focus never moves", "Clicking Other models opens a different menu"} {
		if claimInPR(sentence, words) {
			t.Errorf("accepted nonclaim %q", sentence)
		}
	}
}

func TestCheckReclassifyOnlyBeforeEnding(t *testing.T) {
	choices := []CheckClassification{
		{Purpose: "claim", ClaimSentence: "The row now stops that click."},
		{Purpose: "risk", Risk: "moving focus could break the composer"},
		{Purpose: "explore"},
	}
	for _, state := range append([]CheckState{CheckPending, CheckRunning}, outcomes...) {
		t.Run(string(state), func(t *testing.T) {
			svc, _ := newSvc(t)
			run, err := svc.Intake(ctx, "s1", prURL)
			is(t, "")(nil, err)
			gh := svc.gh.(*fakeGH)
			calls := len(gh.calls)
			for _, from := range choices {
				for _, to := range choices {
					c := Check{ID: "c1", State: state, Purpose: from.Purpose, Claim: from.Purpose == "claim", ClaimSentence: from.ClaimSentence, Risk: from.Risk}
					is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{c}))
					path := filepath.Join(svc.store.root, run.ID, "checks.json")
					before, err := os.ReadFile(path)
					is(t, "")(nil, err)
					got, err := svc.CheckReclassify(ctx, "s1", c.ID, to, "correct the planned purpose")
					if state == CheckPending || state == CheckRunning {
						is(t, "")(got, err)
						if got.State != state || got.Purpose != to.Purpose || got.ClaimSentence != to.ClaimSentence || got.Risk != to.Risk || len(got.Reclassifications) != 1 {
							t.Fatalf("reclassification lost state, purpose or audit: %+v", got)
						}
						continue
					}
					is(t, "TESTING_CHECK_STATE")(nil, err)
					var api *apierr.Error
					want := fmt.Sprintf("c1 ended %s; coverage is fixed once a check ends. Add a new check with --claim and run it.", state)
					if !errors.As(err, &api) || api.Kind != apierr.KindConflict || err.Error() != want {
						t.Fatalf("ended error = %v; want conflict %q", err, want)
					}
					after, err := os.ReadFile(path)
					is(t, "")(nil, err)
					if !bytes.Equal(before, after) {
						t.Fatalf("refused %s -> %s changed checks.json", from.Purpose, to.Purpose)
					}
				}
			}
			if state != CheckPending && state != CheckRunning {
				is(t, "TESTING_CHECK_STATE")(svc.CheckReclassify(ctx, "s1", "c1", CheckClassification{}, ""))
			}
			if len(gh.calls) != calls {
				t.Fatal("reclassification fetched PR text unnecessarily")
			}
		})
	}
}

func TestRun253EndedRiskChecksCannotBecomeClaimCoverage(t *testing.T) {
	svc, _, runDir, _ := submitSvc(t)
	checks, err := svc.store.Checks("run_s1")
	is(t, "")(nil, err)
	checks[0].State, checks[2].State = CheckSkipped, CheckSkipped
	checks[0].Note, checks[2].Note = "no reachable precondition", "no reachable precondition"
	checks[0].Receipts, checks[2].Receipts = nil, nil
	checks = append(checks[:3], Check{ID: "r1", Purpose: "risk", State: CheckPass}, Check{ID: "r2", Purpose: "risk", State: CheckPass}, Check{ID: "r3", Purpose: "risk", State: CheckPass}, Check{ID: "r4", Purpose: "risk", State: CheckPass})
	for i := 3; i < len(checks); i++ {
		checks[i].Name, checks[i].Risk = fmt.Sprint("risk ", i), "focus changes could break another user path"
		checks[i].Receipts = []Receipt{{Seq: i + 1, AfterPath: evidenceFile(t)}}
	}
	is(t, "")(nil, svc.store.SaveChecks("run_s1", checks))
	before, err := os.ReadFile(filepath.Join(runDir, "checks.json"))
	is(t, "")(nil, err)
	for _, c := range checks[3:] {
		is(t, "TESTING_CHECK_STATE")(svc.CheckReclassify(ctx, "s1", c.ID, CheckClassification{Purpose: "claim", ClaimSentence: "The row now stops that click."}, "this also covers the headline"))
	}
	after, err := os.ReadFile(filepath.Join(runDir, "checks.json"))
	is(t, "")(nil, err)
	if !bytes.Equal(before, after) {
		t.Fatal("refused risk relabels changed the ledger")
	}
	v, err := svc.Submit(ctx, "s1", "", "")
	is(t, "")(v, err)
	if v.Overall != CheckUntested {
		t.Fatalf("all claims skipped, four risks passed: %s", v.Overall)
	}
}

func TestClaimOperationsRejectContextAndShortFragments(t *testing.T) {
	for _, verb := range []string{"add", "start", "reclassify"} {
		t.Run(verb, func(t *testing.T) {
			svc, _ := newSvc(t)
			gh := svc.gh.(*fakeGH)
			var pr pullRequest
			is(t, "")(nil, json.Unmarshal(gh.out["pr view"], &pr))
			pr.Comments = []remark{{Body: "The author's later comment promises faster loading"}}
			pr.Comments[0].Author.Login = pr.Author.Login
			pr.Reviews = []remark{{Body: "The reviewer promises stable keyboard focus"}}
			pr.Reviews[0].Author.Login = "reviewer"
			raw, err := json.Marshal(pr)
			is(t, "")(nil, err)
			gh.out["pr view"] = raw
			run, err := svc.Intake(ctx, "s1", prURL)
			is(t, "")(nil, err)
			words := readIntakeClaims(filepath.Join(svc.store.root, run.ID))
			for _, sentence := range []string{pr.Reviews[0].Body, pr.Comments[0].Body, "Other models", "a linked issue promises stable keyboard focus"} {
				choice := CheckClassification{Purpose: "claim", ClaimSentence: sentence}
				c := Check{ID: "c1", Name: "focus", State: CheckPending, Purpose: "claim", Claim: true, ClaimSentence: sentence}
				if verb == "reclassify" {
					c.Purpose, c.Claim, c.ClaimSentence = "explore", false, ""
				}
				is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{c}))
				path := filepath.Join(svc.store.root, run.ID, "checks.json")
				before, err := os.ReadFile(path)
				is(t, "")(nil, err)
				switch verb {
				case "add":
					_, err = svc.CheckAdd(ctx, "s1", "new focus check", "menu stays open", "", "", "", choice)
				case "start":
					_, err = svc.CheckStart(ctx, "s1", c.ID, choice)
				case "reclassify":
					_, err = svc.CheckReclassify(ctx, "s1", c.ID, choice, "this is a PR claim")
				}
				is(t, "TESTING_CHECK_CLAIM_SENTENCE")(nil, err)
				var api *apierr.Error
				if !errors.As(err, &api) || api.Kind != apierr.KindInvalid || !reflect.DeepEqual(api.Details["intakeClaims"], words) {
					t.Fatalf("missing author's source lines: %v", err)
				}
				for _, source := range words {
					if !strings.Contains(err.Error(), source) {
						t.Fatalf("claim error omitted source %q: %v", source, err)
					}
				}
				after, readErr := os.ReadFile(path)
				is(t, "")(nil, readErr)
				if !bytes.Equal(before, after) {
					t.Fatal("refused claim changed checks.json")
				}
			}
			pack, err := os.ReadFile(filepath.Join(svc.store.root, run.ID, "context-pack.md"))
			is(t, "")(nil, err)
			for _, text := range []string{"### Comments and reviews (context, not PR claims)", "## 6. Recipe"} {
				if !strings.Contains(string(pack), text) {
					t.Errorf("context pack missing %q", text)
				}
			}
			if strings.Contains(string(pack), "## 7.") {
				t.Errorf("context pack retained the removed section 7:\n%s", pack)
			}
		})
	}
}

type claimFetch struct {
	t       *testing.T
	svc     *Service
	raw     []byte
	err     error
	calls   int
	started chan struct{}
	release chan struct{}
}

func (f *claimFetch) Run(ctx context.Context, args ...string) ([]byte, error) {
	f.calls++
	if len(args) < 2 || args[0] != "pr" || args[1] != "view" {
		f.t.Fatalf("unexpected claim fetch: %v", args)
	}
	if !f.svc.mu.TryLock() {
		f.t.Fatal("claim refresh holds service lock")
	}
	f.svc.mu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 10*time.Second || time.Until(deadline) <= 0 {
		f.t.Fatalf("refresh timeout = %v, %t", deadline, ok)
	}
	if f.started != nil {
		close(f.started)
		<-f.release
	}
	return f.raw, f.err
}

func TestClaimRefreshEditedBodyAndRateLimit(t *testing.T) {
	for _, verb := range []string{"add", "start", "reclassify"} {
		t.Run(verb, func(t *testing.T) {
			svc, now := newSvc(t)
			run, err := svc.Intake(ctx, "s1", prURL)
			is(t, "")(nil, err)
			original := readIntakeClaims(filepath.Join(svc.store.root, run.ID))
			var pr pullRequest
			is(t, "")(nil, json.Unmarshal(svc.gh.(*fakeGH).out["pr view"], &pr))
			pr.Title = "The edited title promises stable focus"
			pr.Body = "The edited body keeps the menu open."
			raw, err := json.Marshal(pr)
			is(t, "")(nil, err)
			fetch := &claimFetch{t: t, svc: svc, raw: raw}
			svc.gh = fetch
			choice := CheckClassification{Purpose: "claim", ClaimSentence: pr.Body}
			is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{{ID: "c1", Name: "edited claim", State: CheckPending, Purpose: "claim", Claim: true, ClaimSentence: pr.Body}}))
			switch verb {
			case "add":
				_, err = svc.CheckAdd(ctx, "s1", "new check", "menu stays open", "", "", "", choice)
			case "start":
				_, err = svc.CheckStart(ctx, "s1", "c1", choice)
			case "reclassify":
				_, err = svc.CheckReclassify(ctx, "s1", "c1", choice, "quote the edited body")
			}
			is(t, "")(nil, err)
			words := readIntakeClaims(filepath.Join(svc.store.root, run.ID))
			want := append(append([]string{}, original...), pr.Title, pr.Body)
			if fetch.calls != 1 || !reflect.DeepEqual(words, want) {
				t.Fatalf("refresh calls = %d, source text = %q; want %q", fetch.calls, words, want)
			}
			miss := CheckClassification{Purpose: "claim", ClaimSentence: "Another edit keeps keyboard focus stable."}
			pr.Body = miss.ClaimSentence
			fetch.raw, err = json.Marshal(pr)
			is(t, "")(nil, err)
			*now = now.Add(59 * time.Second)
			is(t, "TESTING_CHECK_CLAIM_SENTENCE")(svc.CheckAdd(ctx, "s1", "second edit", "focus stays stable", "", "", "", miss))
			if fetch.calls != 1 {
				t.Fatal("refreshed within 60 seconds")
			}
			*now = now.Add(time.Second)
			is(t, "")(svc.CheckAdd(ctx, "s1", "second edit", "focus stays stable", "", "", "", miss))
			if fetch.calls != 2 {
				t.Fatalf("refresh calls after 60 seconds = %d", fetch.calls)
			}
			if !claimInPR(original[0], readIntakeClaims(filepath.Join(svc.store.root, run.ID))) {
				t.Fatal("refresh discarded intake words")
			}
		})
	}
}

func TestClaimRefreshFailureIsReturnedAndRateLimited(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(nil, err)
	fetch := &claimFetch{t: t, svc: svc, err: errors.New("gh auth login required")}
	svc.gh = fetch
	path := filepath.Join(svc.store.root, run.ID, "intake-claims.json")
	before, err := os.ReadFile(path)
	is(t, "")(nil, err)
	choice := CheckClassification{Purpose: "claim", ClaimSentence: "A new promise was added."}
	_, err = svc.CheckAdd(ctx, "s1", "new promise", "works", "", "", "", choice)
	is(t, "TESTING_CHECK_CLAIM_SENTENCE")(nil, err)
	if !strings.Contains(err.Error(), "PR text refresh failed:") || !strings.Contains(err.Error(), "gh is not authenticated") {
		t.Fatalf("claim error hid refresh failure: %v", err)
	}
	is(t, "TESTING_CHECK_CLAIM_SENTENCE")(svc.CheckAdd(ctx, "s1", "new promise", "works", "", "", "", choice))
	after, err := os.ReadFile(path)
	is(t, "")(nil, err)
	if fetch.calls != 1 || !bytes.Equal(before, after) {
		t.Fatal("failed refresh changed source text or escaped rate limit")
	}
}

func TestClaimRefreshRechecksLedgerAfterFetch(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(nil, err)
	var pr pullRequest
	is(t, "")(nil, json.Unmarshal(svc.gh.(*fakeGH).out["pr view"], &pr))
	pr.Body = "The edited body keeps the menu open."
	raw, err := json.Marshal(pr)
	is(t, "")(nil, err)
	fetch := &claimFetch{t: t, svc: svc, raw: raw, started: make(chan struct{}), release: make(chan struct{})}
	svc.gh = fetch
	is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{{ID: "c1", Name: "menu", State: CheckRunning, Purpose: "explore"}}))
	done := make(chan error, 1)
	go func() {
		_, err := svc.CheckReclassify(ctx, "s1", "c1", CheckClassification{Purpose: "claim", ClaimSentence: pr.Body}, "quote the edited body")
		done <- err
	}()
	<-fetch.started
	is(t, "")(svc.CheckEnd(ctx, "s1", "c1", CheckSkipped, "fixture unavailable", false, 0))
	path := filepath.Join(svc.store.root, run.ID, "checks.json")
	before, err := os.ReadFile(path)
	is(t, "")(nil, err)
	close(fetch.release)
	is(t, "TESTING_CHECK_STATE")(nil, <-done)
	after, err := os.ReadFile(path)
	is(t, "")(nil, err)
	if !bytes.Equal(before, after) {
		t.Fatal("claim fetch overwrote a check that ended while fetching")
	}
}

func TestClaimAbsentIntakeTextAndVerdictRelabelHistory(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(nil, err)
	is(t, "")(nil, svc.store.WriteFile(run.ID, "intake-claims.json", []byte("null")))
	claim := CheckClassification{Purpose: "claim", ClaimSentence: "This cannot be checked against intake."}
	c, err := svc.CheckAdd(ctx, "s1", "unavailable text", "works", "", "", "", claim)
	is(t, "")(nil, err)
	c, err = svc.CheckReclassify(ctx, "s1", c.ID, CheckClassification{Purpose: "explore"}, "explore the user path")
	is(t, "")(nil, err)
	c, err = svc.CheckReclassify(ctx, "s1", c.ID, claim, "return to the author's sentence")
	is(t, "")(nil, err)
	is(t, "")(svc.CheckStart(ctx, "s1", c.ID, claim))
	v := Verdict{Checks: []Check{c}}
	md := renderVerdict(run, v, true, nil, filepath.Join(svc.store.root, run.ID), run.CreatedAt, nil)
	for _, line := range []string{"claim sentences not checked", "reclassified claim -> explore: explore the user path", "reclassified explore -> claim: return to the author's sentence"} {
		if strings.Count(md, line) != 1 {
			t.Errorf("verdict missing one line %q:\n%s", line, md)
		}
	}
	comment := renderComment(run, v, evidence{}, run.CreatedAt)
	if strings.Contains(comment, "reclassified") || strings.Contains(comment, "claim sentences not checked") {
		t.Fatal("private verdict details changed comment")
	}
}
