package triage

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	reportSearchBucketLimit  = 5
	reportCandidateBodyLimit = 12000
)

// ReportSearch contains API-read candidates for the selected agent to assess.
// A candidate is not a duplicate verdict, and a merged PR does not prove that
// its fix is present in the installed release.
type ReportSearch struct {
	Repository string            `json:"repository"`
	Query      string            `json:"query"`
	Candidates []ReportCandidate `json:"candidates"`
	Truncated  bool              `json:"truncated"`
	Warnings   []string          `json:"warnings,omitempty"`
}

// ReportCandidate keeps the report's observed state separate from fix claims.
type ReportCandidate struct {
	URL           string `json:"url"`
	Number        int    `json:"number"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	State         string `json:"state"`
	IsPullRequest bool   `json:"isPullRequest"`
	Merged        bool   `json:"merged"`
	MergeCommit   string `json:"mergeCommit,omitempty"`
	MergedAt      string `json:"mergedAt,omitempty"`
	ClosedAt      string `json:"closedAt,omitempty"`
	CreatedAt     string `json:"createdAt,omitempty"`
	UpdatedAt     string `json:"updatedAt,omitempty"`
	BodyTruncated bool   `json:"bodyTruncated,omitempty"`
}

// SearchReports uses the same trusted API and credential source as publication.
func SearchReports(ctx context.Context, dataDir, query string) (ReportSearch, error) {
	return NewPublisher(dataDir).SearchReports(ctx, query)
}

// SearchReports reads a bounded best-match page for each issue/PR state. It
// reports incomplete search coverage rather than claiming that no match exists.
func (p *Publisher) SearchReports(ctx context.Context, query string) (result ReportSearch, err error) {
	result.Query = reportSearchTerms(query)
	result.Candidates = []ReportCandidate{}
	if result.Query == "" {
		return result, errors.New("report search needs symptom words")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if p.TokenSource == nil {
		return result, errors.New("GitHub sign-in is required to search reports")
	}
	token, err := p.TokenSource(ctx)
	if err != nil {
		return result, safeGitHubError(err, "")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return result, errors.New("GitHub sign-in is required to search reports")
	}
	result.Query = reportSearchTerms(strings.ReplaceAll(query, token, redacted))
	if result.Query == "" {
		return result, errors.New("report search needs symptom words")
	}
	defer func() {
		if err != nil {
			err = safeGitHubError(err, token)
		}
	}()
	client, base, err := p.client()
	if err != nil {
		return result, err
	}
	api := publicationAPI{client: client, base: base, token: token}
	result.Repository, err = api.canonicalRepo(ctx)
	if err != nil {
		return result, err
	}
	seen := map[int]bool{}
	for _, kind := range []string{"issue", "pr"} {
		for _, state := range []string{"open", "closed"} {
			q := "repo:" + result.Repository + " is:" + kind + " is:" + state + " in:title,body " + quotedSearchTerms(result.Query)
			var page githubSearch
			if err := api.get(ctx, "/search/issues?per_page="+strconv.Itoa(reportSearchBucketLimit)+"&page=1&q="+url.QueryEscape(q), &page); err != nil {
				return result, fmt.Errorf("searching %s %s reports: %w", state, kind, err)
			}
			if page.TotalCount < 0 || len(page.Items) > reportSearchBucketLimit {
				return result, errors.New("GitHub returned an invalid report search page")
			}
			if page.Incomplete || page.TotalCount > len(page.Items) {
				result.Truncated = true
			}
			for _, item := range page.Items {
				isPR := kind == "pr"
				if seen[item.Number] {
					continue
				}
				candidate, err := api.readReport(ctx, result.Repository, item.HTMLURL, isPR)
				if err != nil {
					return result, err
				}
				if candidate.Number != item.Number {
					return result, errors.New("GitHub search returned an inconsistent report number")
				}
				seen[item.Number] = true
				candidate.Title = Redact(strings.ReplaceAll(candidate.Title, token, redacted))
				candidate.Body = Redact(strings.ReplaceAll(candidate.Body, token, redacted))
				if len(candidate.Body) > reportCandidateBodyLimit {
					candidate.Body = truncateUTF8(candidate.Body, reportCandidateBodyLimit)
					candidate.BodyTruncated = true
				}
				result.Candidates = append(result.Candidates, candidate)
			}
		}
	}
	if result.Truncated {
		result.Warnings = append(result.Warnings, "Search was capped or GitHub returned incomplete results. Refine the symptom query if these candidates do not explain the report.")
	}
	return result, nil
}

func (a publicationAPI) canonicalRepo(ctx context.Context) (string, error) {
	var repo struct {
		FullName string `json:"full_name"`
		URL      string `json:"url"`
		HTMLURL  string `json:"html_url"`
		Private  bool   `json:"private"`
	}
	if err := a.get(ctx, "/repos/"+canonicalRepository, &repo); err != nil {
		return "", err
	}
	if !validRepository(repo.FullName) || repo.Private || !a.validAPIURL(repo.URL, "/repos/"+repo.FullName) || !validRepositoryURL(repo.HTMLURL, repo.FullName) {
		return "", errors.New("GitHub returned an invalid canonical repository")
	}
	return repo.FullName, nil
}

func (a publicationAPI) readReport(ctx context.Context, repo, rawURL string, isPR bool) (ReportCandidate, error) {
	if !validReportURL(rawURL, repo, true, false) {
		return ReportCandidate{}, errors.New("GitHub returned an invalid report URL")
	}
	u, _ := url.Parse(rawURL)
	parts := strings.Split(u.Path, "/")
	number, _ := strconv.Atoi(parts[len(parts)-1])
	route := "issues"
	if isPR {
		route = "pulls"
		if parts[len(parts)-2] != "pull" {
			return ReportCandidate{}, errors.New("GitHub returned an inconsistent pull request URL")
		}
	} else if parts[len(parts)-2] != "issues" {
		return ReportCandidate{}, errors.New("GitHub returned an inconsistent issue URL")
	}
	var report struct {
		Number      int       `json:"number"`
		HTMLURL     string    `json:"html_url"`
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		State       string    `json:"state"`
		PullRequest *struct{} `json:"pull_request"`
		Merged      bool      `json:"merged"`
		MergeCommit string    `json:"merge_commit_sha"`
		MergedAt    string    `json:"merged_at"`
		ClosedAt    string    `json:"closed_at"`
		CreatedAt   string    `json:"created_at"`
		UpdatedAt   string    `json:"updated_at"`
	}
	if err := a.get(ctx, "/repos/"+repo+"/"+route+"/"+strconv.Itoa(number), &report); err != nil {
		return ReportCandidate{}, err
	}
	if report.Number != number || report.HTMLURL != rawURL || report.State != "open" && report.State != "closed" || strings.TrimSpace(report.Title) == "" || utf8.RuneCountInString(report.Title) > 256 || !utf8.ValidString(report.Title+report.Body) || !isPR && report.PullRequest != nil {
		return ReportCandidate{}, errors.New("GitHub returned inconsistent report details")
	}
	for _, timestamp := range []string{report.MergedAt, report.ClosedAt, report.CreatedAt, report.UpdatedAt} {
		if timestamp != "" {
			if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
				return ReportCandidate{}, errors.New("GitHub returned an invalid report timestamp")
			}
		}
	}
	if isPR && (report.Merged && report.State != "closed" || report.MergeCommit != "" && !regexp.MustCompile(`^(?:[a-fA-F0-9]{40}|[a-fA-F0-9]{64})$`).MatchString(report.MergeCommit)) {
		return ReportCandidate{}, errors.New("GitHub returned inconsistent pull request merge details")
	}
	return ReportCandidate{
		URL: report.HTMLURL, Number: number, Title: report.Title, Body: report.Body, State: report.State,
		IsPullRequest: isPR, Merged: isPR && report.Merged, MergeCommit: report.MergeCommit,
		MergedAt: report.MergedAt, ClosedAt: report.ClosedAt, CreatedAt: report.CreatedAt, UpdatedAt: report.UpdatedAt,
	}, nil
}

func reportSearchTerms(query string) string {
	query = Redact(query)
	for _, placeholder := range []string{redacted, "[private-path]", "[local-url]"} {
		query = strings.ReplaceAll(query, placeholder, " ")
	}
	var terms []string
	for _, word := range strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(word) < 2 {
			continue
		}
		terms = append(terms, truncateUTF8(word, 40))
		if len(terms) == 6 {
			break
		}
	}
	return strings.Join(terms, " ")
}

func quotedSearchTerms(terms string) string {
	return `"` + strings.Join(strings.Fields(terms), `" "`) + `"`
}

func truncateUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	text = text[:limit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}

type redactedGitHubError struct {
	message string
	cause   error
}

func (e redactedGitHubError) Error() string { return e.message }
func (e redactedGitHubError) Unwrap() error { return e.cause }

func safeGitHubError(err error, token string) error {
	message := err.Error()
	if token != "" {
		message = strings.ReplaceAll(message, token, redacted)
	}
	return redactedGitHubError{message: safePublishError(errors.New(message)), cause: err}
}
