package triage

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReportSearchReadsOpenClosedIssuesAndMergedPRs(t *testing.T) {
	f := newPublisherFixture(t)
	f.repo = "AOAgents/agent-orchestrator"
	queries := map[string]bool{}
	reads := 0
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/search/issues" {
			q := r.URL.Query().Get("q")
			queries[q] = true
			if !strings.HasPrefix(q, "repo:"+f.repo+" ") || !strings.Contains(q, "in:title,body") || strings.Contains(q, "ghp_") || strings.Contains(q, "repo:attacker") || strings.Contains(q, "synthetic-token") {
				t.Errorf("unsafe query %q", q)
			}
			item := f.issue("untrusted snippet", "not the full report")
			if strings.Contains(q, "is:pr") {
				item["number"] = 43
				item["html_url"] = "https://github.com/" + f.repo + "/pull/43"
			}
			f.json(w, 200, map[string]any{"total_count": 1, "items": []any{item}})
			return true
		}
		switch r.URL.Path {
		case "/repos/" + f.repo + "/issues/42":
			reads++
			issue := f.issue("Session window closes on launch", "Observed crash in 0.10.0. token=ghp_abcdefghijklmnopqrstuvwxyz123456")
			issue["state"] = "open"
			f.json(w, 200, issue)
			return true
		case "/repos/" + f.repo + "/pulls/43":
			reads++
			f.json(w, 200, map[string]any{
				"number": 43, "html_url": "https://github.com/" + f.repo + "/pull/43", "title": "Fix session launch crash",
				"body": "Fix landed after 0.10.0. synthetic-token", "state": "closed", "merged": true,
				"merge_commit_sha": strings.Repeat("a", 40), "merged_at": "2026-10-18T10:00:00Z",
			})
			return true
		}
		return false
	}
	result, err := f.publisher.SearchReports(context.Background(), "session crash ghp_abcdefghijklmnopqrstuvwxyz123456 synthetic-token repo:attacker/repo")
	if err != nil || result.Repository != f.repo || len(result.Candidates) != 2 || len(queries) != 4 || reads != 2 {
		t.Fatalf("result=%+v err=%v queries=%d reads=%d", result, err, len(queries), reads)
	}
	for _, kind := range []string{"issue", "pr"} {
		for _, state := range []string{"open", "closed"} {
			found := false
			for q := range queries {
				found = found || strings.Contains(q, "is:"+kind+" is:"+state)
			}
			if !found {
				t.Fatalf("missing %s %s search", state, kind)
			}
		}
	}
	issue, pr := result.Candidates[0], result.Candidates[1]
	if issue.Title == "untrusted snippet" || strings.Contains(issue.Body, "ghp_") || !pr.IsPullRequest || !pr.Merged || pr.MergeCommit != strings.Repeat("a", 40) || pr.MergedAt == "" || strings.Contains(pr.Body, "synthetic-token") {
		t.Fatalf("unverified or unsafe candidates: %+v", result.Candidates)
	}
	if _, posts := f.counts(); posts != 0 {
		t.Fatalf("search sent %d mutations", posts)
	}
}

func TestReportSearchStatesCapsAndBodyTruncationAreHonest(t *testing.T) {
	f := newPublisherFixture(t)
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/search/issues":
			f.json(w, 200, map[string]any{"total_count": 90, "incomplete_results": true, "items": []any{f.issue("snippet", "snippet")}})
			return true
		case "/repos/" + f.repo + "/issues/42":
			issue := f.issue("Full issue", strings.Repeat("界", reportCandidateBodyLimit))
			issue["state"] = "closed"
			f.json(w, 200, issue)
			return true
		}
		return false
	}
	result, err := f.publisher.SearchReports(context.Background(), "session crash")
	if err != nil || !result.Truncated || len(result.Warnings) == 0 || len(result.Candidates) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	candidate := result.Candidates[0]
	if !candidate.BodyTruncated || len(candidate.Body) > reportCandidateBodyLimit || !utf8.ValidString(candidate.Body) {
		t.Fatal("body truncation was invalid or hidden")
	}
}

func TestReportSearchRejectsUntrustedCandidates(t *testing.T) {
	for _, mode := range []string{"foreign search URL", "wrong report number", "foreign read URL", "wrong kind", "invalid state", "private canonical repo", "oversized page"} {
		t.Run(mode, func(t *testing.T) {
			f := newPublisherFixture(t)
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if mode == "private canonical repo" && r.URL.Path == "/repos/"+canonicalRepository {
					f.json(w, 200, map[string]any{"full_name": f.repo, "url": f.server.URL + "/repos/" + f.repo, "html_url": "https://github.com/" + f.repo, "private": true})
					return true
				}
				if r.URL.Path == "/search/issues" {
					issue := f.issue("snippet", "snippet")
					if mode == "foreign search URL" {
						issue["html_url"] = "https://github.com/attacker/repo/issues/42"
					}
					if mode == "wrong kind" {
						issue["html_url"] = "https://github.com/" + f.repo + "/pull/42"
					}
					items := []any{issue}
					if mode == "oversized page" {
						items = []any{issue, issue, issue, issue, issue, issue}
					}
					f.json(w, 200, map[string]any{"total_count": len(items), "items": items})
					return true
				}
				if r.URL.Path == "/repos/"+f.repo+"/issues/42" {
					issue := f.issue("Full report", "Observed failure")
					issue["state"] = "open"
					switch mode {
					case "wrong report number":
						issue["number"] = 88
					case "foreign read URL":
						issue["html_url"] = "https://github.com/attacker/repo/issues/42"
					case "invalid state":
						issue["state"] = "invented"
					}
					f.json(w, 200, issue)
					return true
				}
				return false
			}
			if _, err := f.publisher.SearchReports(context.Background(), "crash"); err == nil {
				t.Fatal("accepted invalid report")
			}
		})
	}
}

func TestReportSearchScrubsErrorsAndPreservesCancellation(t *testing.T) {
	f := newPublisherFixture(t)
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/search/issues" {
			f.json(w, 429, map[string]any{"message": "bad synthetic-token ghp_abcdefghijklmnopqrstuvwxyz123456 /Users/alice/private.log"})
			return true
		}
		return false
	}
	_, err := f.publisher.SearchReports(context.Background(), "crash")
	if err == nil || strings.Contains(err.Error(), "synthetic-token") || strings.Contains(err.Error(), "ghp_") || strings.Contains(err.Error(), "/Users/alice") {
		t.Fatalf("unsafe error %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = f.publisher.SearchReports(ctx, "crash")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	for _, query := range []string{"", "ghp_abcdefghijklmnopqrstuvwxyz123456", "/Users/alice/private.log"} {
		before, _ := f.counts()
		if _, err := f.publisher.SearchReports(context.Background(), query); err == nil {
			t.Fatalf("accepted empty/private search %q", query)
		}
		after, _ := f.counts()
		if after != before {
			t.Fatal("sent an empty/private query")
		}
	}
}
