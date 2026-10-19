package triage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	scmgithub "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/github"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/githubpat"
)

// Publication states distinguish saved intent, uncertain requests and known results.
const (
	PublicationPrepared        = "prepared"
	PublicationIssuePending    = "issue_pending"
	PublicationIssueUnknown    = "issue_unknown"
	PublicationIssueFailed     = "issue_failed"
	PublicationEvidencePending = "evidence_pending"
	PublicationEvidenceUnknown = "evidence_unknown"
	PublicationEvidenceFailed  = "evidence_failed"
	PublicationPublished       = "published"
	PublicationDuplicate       = "duplicate"
	PublicationAuthRequired    = "auth_required"
	PublicationBlocked         = "blocked"

	canonicalRepository = "OrchestratorInc/agent-orchestrator"
	maxGitHubBody       = 60000
	maxGitHubResponse   = 2 << 20
)

// Publisher is the only triage component that receives a GitHub credential.
// The agent supplies a draft; this helper freezes and publishes it. Tests may
// replace these dependencies without invoking native gh or using real tokens.
type Publisher struct {
	HTTPClient  *http.Client
	BaseURL     string
	TokenSource func(context.Context) (string, error)
}

// NewPublisher uses AO's existing GitHub login without giving it to the agent.
func NewPublisher(dataDir string) *Publisher {
	stored := githubpat.New(dataDir)
	gh := &scmgithub.GHTokenSource{}
	return &Publisher{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		BaseURL:    "https://api.github.com",
		TokenSource: func(ctx context.Context) (string, error) {
			token, err := stored.GetToken(ctx)
			if err == nil || !os.IsNotExist(err) {
				return token, err
			}
			if token, err := (scmgithub.EnvTokenSource{EnvVars: []string{"AO_GITHUB_TOKEN", "GH_TOKEN"}}).Token(ctx); err == nil || !errors.Is(err, scmgithub.ErrNoToken) {
				return token, err
			}
			cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return gh.Token(cmdCtx)
		},
	}
}

// Publish persists the frozen intent before each mutation. A pending operation
// from an interrupted process is treated as unknown, even if the caller never
// received an error. Empty search results cannot disprove an accepted POST.
func (p *Publisher) Publish(ctx context.Context, run Run, save func(Publication) error) (Publication, error) {
	pub := run.Publication
	if !run.Request.PublishAuthorized {
		return pub, errors.New("GitHub publication was not authorized")
	}
	if run.Draft == nil || strings.TrimSpace(run.Draft.Title) == "" || strings.TrimSpace(run.Draft.Body) == "" || run.ID == "" {
		return pub, errors.New("a report title, body, and run ID are required")
	}
	if save == nil {
		return pub, errors.New("publication requires durable storage")
	}
	prepared, err := preparePublication(run)
	if err != nil {
		return pub, err
	}
	persist := func() error { return save(pub) }
	var token string
	fail := func(state string, err error) (Publication, error) {
		// Authentication and read failures must not erase an uncertain POST.
		switch pub.State {
		case PublicationIssuePending, PublicationIssueUnknown:
			state = PublicationIssueUnknown
		case PublicationEvidencePending, PublicationEvidenceUnknown:
			state = PublicationEvidenceUnknown
		}
		pub.State, pub.Error = state, safeGitHubError(err, token).Error()
		if serr := persist(); serr != nil {
			return pub, fmt.Errorf("saving publication: %w", serr)
		}
		return pub, errors.New(pub.Error)
	}
	if pub.Marker == "" {
		if pub.State != "" || pub.IssueURL != "" || pub.EvidenceURL != "" {
			return pub, errors.New("stored publication has no report marker; its outcome must be checked before sending again")
		}
		pub = prepared
		if err := persist(); err != nil {
			return pub, fmt.Errorf("saving prepared report: %w", err)
		}
	}
	if err := validateFrozenPublication(run.ID, pub); err != nil {
		return fail(PublicationBlocked, err)
	}
	if pub.State == PublicationPublished || pub.State == PublicationDuplicate {
		if !validReportURL(pub.IssueURL, pub.Repository, pub.State == PublicationDuplicate, false) || (pub.EvidenceURL != "" && !validReportURL(pub.EvidenceURL, pub.Repository, false, true)) {
			return fail(PublicationBlocked, errors.New("stored GitHub result URL is invalid"))
		}
		return pub, nil
	}
	if err := ctx.Err(); err != nil {
		return fail(PublicationBlocked, err)
	}
	if p.TokenSource == nil {
		return fail(PublicationAuthRequired, errors.New("no GitHub credential source is configured"))
	}
	token, err = p.TokenSource(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		if err == nil {
			err = scmgithub.ErrNoToken
		}
		state := PublicationBlocked
		if errors.Is(err, scmgithub.ErrNoToken) || errors.Is(err, githubpat.ErrInvalidCredentials) || os.IsNotExist(err) {
			state = PublicationAuthRequired
		}
		return fail(state, err)
	}
	if strings.ContainsAny(token, "\r\n") || (len(token) >= 8 && strings.Contains(pub.Title+pub.Body+pub.Evidence+pub.ExistingReportURL+pub.ExistingReportReason, token)) {
		return fail(PublicationBlocked, errors.New("report contains credential material; remove it before publication"))
	}
	client, base, err := p.client()
	if err != nil {
		return fail(PublicationBlocked, err)
	}
	api := publicationAPI{client: client, base: base, token: token}
	repo, err := api.canonicalRepo(ctx)
	if err != nil {
		return fail(publicationFailureState(err), err)
	}
	if pub.Repository != "" && !strings.EqualFold(pub.Repository, repo) {
		return fail(PublicationBlocked, errors.New("canonical GitHub repository changed after the report was prepared"))
	}
	pub.Repository = repo
	var user githubUser
	if err := api.get(ctx, "/user", &user); err != nil {
		return fail(publicationFailureState(err), err)
	}
	identity := user.identity()
	if identity == "" {
		return fail(PublicationBlocked, errors.New("GitHub returned an invalid user identity"))
	}
	if pub.Identity != "" && pub.Identity != identity {
		return fail(PublicationBlocked, errors.New("GitHub account changed after the report was prepared"))
	}
	pub.Identity = identity
	if err := persist(); err != nil {
		return pub, fmt.Errorf("saving GitHub identity: %w", err)
	}

	if pub.IssueURL == "" {
		if pub.State == PublicationIssueUnknown || pub.State == PublicationIssuePending {
			issue, err := api.reconcileIssue(ctx, pub)
			if err != nil {
				return fail(PublicationIssueUnknown, err)
			}
			pub.IssueURL, pub.State, pub.Error = issue.HTMLURL, PublicationEvidenceFailed, ""
			if err := persist(); err != nil {
				return pub, fmt.Errorf("saving recovered issue: %w", err)
			}
		} else {
			if pub.ExistingReportURL != "" {
				if !validReportURL(pub.ExistingReportURL, pub.Repository, true, false) {
					return fail(PublicationBlocked, errors.New("selected report does not belong to the canonical GitHub repository"))
				}
				isPR := strings.Contains(pub.ExistingReportURL, "/pull/")
				candidate, err := api.readReport(ctx, pub.Repository, pub.ExistingReportURL, isPR)
				if err != nil {
					return fail(publicationFailureState(err), fmt.Errorf("verifying selected report: %w", err))
				}
				pub.State, pub.IssueURL, pub.Error = PublicationDuplicate, candidate.URL, ""
				if err := persist(); err != nil {
					return pub, fmt.Errorf("saving related report result: %w", err)
				}
				return pub, nil
			}
			duplicate, err := api.findDuplicate(ctx, pub)
			if err != nil {
				return fail(publicationFailureState(err), fmt.Errorf("duplicate search: %w", err))
			}
			if duplicate != "" {
				pub.State, pub.IssueURL, pub.Error = PublicationDuplicate, duplicate, ""
				if err := persist(); err != nil {
					return pub, fmt.Errorf("saving duplicate result: %w", err)
				}
				return pub, nil
			}
			if err := ctx.Err(); err != nil {
				return fail(PublicationBlocked, err)
			}
			pub.State, pub.Error = PublicationIssuePending, ""
			if err := persist(); err != nil {
				return pub, fmt.Errorf("saving issue intent: %w", err)
			}
			var issue githubIssue
			err = api.post(ctx, "/repos/"+pub.Repository+"/issues", map[string]string{"title": pub.Title, "body": pub.Body}, &issue)
			if err != nil {
				if definiteRejection(err) {
					pub.State = PublicationIssueFailed
					state := PublicationIssueFailed
					if publicationFailureState(err) == PublicationAuthRequired {
						state = PublicationAuthRequired
					}
					return fail(state, err)
				}
				return fail(PublicationIssueUnknown, fmt.Errorf("issue publication outcome is unknown: %w", err))
			}
			if !validReportURL(issue.HTMLURL, pub.Repository, false, false) || issue.Number <= 0 || !strings.HasSuffix(issue.HTMLURL, "/"+strconv.Itoa(issue.Number)) {
				return fail(PublicationIssueUnknown, errors.New("issue publication outcome is unknown: GitHub returned an invalid issue URL"))
			}
			pub.IssueURL, pub.State = issue.HTMLURL, PublicationEvidenceFailed
			if err := persist(); err != nil {
				return pub, fmt.Errorf("saving created issue: %w", err)
			}
		}
	}
	if !validReportURL(pub.IssueURL, pub.Repository, false, false) {
		return fail(PublicationBlocked, errors.New("stored GitHub issue URL is invalid"))
	}
	if pub.Evidence == "" || pub.EvidenceURL != "" {
		if pub.EvidenceURL != "" && !validReportURL(pub.EvidenceURL, pub.Repository, false, true) {
			return fail(PublicationBlocked, errors.New("stored GitHub evidence URL is invalid"))
		}
		pub.State, pub.Error = PublicationPublished, ""
		if err := persist(); err != nil {
			return pub, fmt.Errorf("saving publication result: %w", err)
		}
		return pub, nil
	}
	issueNumber := pub.IssueURL[strings.LastIndex(pub.IssueURL, "/")+1:]
	if pub.State == PublicationEvidenceUnknown || pub.State == PublicationEvidencePending {
		comment, err := api.reconcileEvidence(ctx, pub, issueNumber)
		if err != nil {
			return fail(PublicationEvidenceUnknown, err)
		}
		pub.EvidenceURL, pub.State, pub.Error = comment.HTMLURL, PublicationPublished, ""
		if err := persist(); err != nil {
			return pub, fmt.Errorf("saving recovered evidence: %w", err)
		}
		return pub, nil
	}
	if err := ctx.Err(); err != nil {
		return fail(PublicationEvidenceFailed, err)
	}
	pub.State, pub.Error = PublicationEvidencePending, ""
	if err := persist(); err != nil {
		return pub, fmt.Errorf("saving evidence intent: %w", err)
	}
	var comment githubComment
	err = api.post(ctx, "/repos/"+pub.Repository+"/issues/"+issueNumber+"/comments", map[string]string{"body": pub.Evidence}, &comment)
	if err != nil {
		if definiteRejection(err) {
			pub.State = PublicationEvidenceFailed
			return fail(PublicationEvidenceFailed, err)
		}
		return fail(PublicationEvidenceUnknown, fmt.Errorf("evidence publication outcome is unknown: %w", err))
	}
	if !validReportURL(comment.HTMLURL, pub.Repository, false, true) || !strings.HasPrefix(comment.HTMLURL, pub.IssueURL+"#issuecomment-") {
		return fail(PublicationEvidenceUnknown, errors.New("evidence publication outcome is unknown: GitHub returned an invalid comment URL"))
	}
	pub.EvidenceURL, pub.State, pub.Error = comment.HTMLURL, PublicationPublished, ""
	if err := persist(); err != nil {
		return pub, fmt.Errorf("saving evidence result: %w", err)
	}
	return pub, nil
}

func reportMarker(runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return "<!-- ao-triage-report-" + hex.EncodeToString(sum[:16]) + " -->"
}

func evidenceMarker(marker string) string {
	return strings.Replace(marker, "ao-triage-report-", "ao-triage-evidence-", 1)
}

func preparePublication(run Run) (Publication, error) {
	pub := Publication{State: PublicationPrepared, Marker: reportMarker(run.ID)}
	pub.ExistingReportURL = strings.TrimSpace(run.Draft.ExistingReportURL)
	pub.ExistingReportReason = strings.TrimSpace(Redact(run.Draft.ExistingReportReason))
	pub.Title = strings.TrimSpace(Redact(run.Draft.Title))
	pub.Body = strings.TrimSpace(Redact(run.Draft.Body)) + "\n\n" + pub.Marker
	if strings.TrimSpace(run.Draft.Evidence) != "" {
		pub.Evidence = "### AO-collected evidence\n\n" + strings.TrimSpace(Redact(run.Draft.Evidence)) + "\n\n" + evidenceMarker(pub.Marker)
	}
	if err := validateFrozenPublication(run.ID, pub); err != nil {
		return pub, err
	}
	return pub, nil
}

// RefreshPublicationAssessment applies the selected agent's completed search
// after sign-in recovery. Only the related-report decision may change. A
// pending or uncertain POST must always be reconciled instead.
func RefreshPublicationAssessment(run Run) (Publication, error) {
	pub := run.Publication
	if pub.State != PublicationAuthRequired || pub.IssueURL != "" || pub.EvidenceURL != "" || run.Draft == nil {
		return pub, errors.New("report assessment can only resume before publication after GitHub sign-in")
	}
	if err := validateFrozenPublication(run.ID, pub); err != nil {
		return pub, err
	}
	prepared, err := preparePublication(run)
	if err != nil {
		return pub, err
	}
	pub.ExistingReportURL, pub.ExistingReportReason = prepared.ExistingReportURL, prepared.ExistingReportReason
	return pub, nil
}

var suspiciousPublication = regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-(?:proj-)?[A-Za-z0-9_-]{20,})\b|"(?:access_token|refresh_token|client_secret|private_key|OPENAI_API_KEY)"\s*:\s*"[^"\[<]{8,}"`)

func validateFrozenPublication(runID string, pub Publication) error {
	switch pub.State {
	case PublicationPrepared, PublicationIssuePending, PublicationIssueUnknown, PublicationIssueFailed, PublicationAuthRequired, PublicationBlocked:
	case PublicationEvidencePending, PublicationEvidenceUnknown, PublicationEvidenceFailed, PublicationPublished, PublicationDuplicate:
		if pub.IssueURL == "" {
			return errors.New("stored publication has no issue result; its outcome must be checked before sending again")
		}
	default:
		return errors.New("stored publication state is invalid")
	}
	if pub.Marker != reportMarker(runID) || strings.TrimSpace(pub.Title) == "" || strings.TrimSpace(pub.Body) == pub.Marker || !strings.HasSuffix(pub.Body, "\n\n"+pub.Marker) {
		return errors.New("prepared report is invalid")
	}
	if utf8.RuneCountInString(pub.Title) > 256 || strings.ContainsAny(pub.Title, "\r\n") || len(pub.Body) > maxGitHubBody || len(pub.Evidence) > maxGitHubBody {
		return errors.New("report exceeds GitHub title or body limits")
	}
	for _, text := range []string{pub.Title, pub.Body, pub.Evidence, pub.ExistingReportReason} {
		if !utf8.ValidString(text) || suspiciousPublication.MatchString(text) || strings.ContainsRune(text, '\x00') {
			return errors.New("report contains suspicious credential or private file content; review the prepared report before publication")
		}
	}
	if pub.ExistingReportURL != "" {
		u, err := url.Parse(pub.ExistingReportURL)
		if err != nil {
			return errors.New("selected GitHub report URL is invalid")
		}
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(parts) != 4 || !validReportURL(pub.ExistingReportURL, strings.Join(parts[:2], "/"), true, false) {
			return errors.New("selected GitHub report URL is invalid")
		}
		if len(strings.Fields(pub.ExistingReportReason)) < 4 || len(pub.ExistingReportReason) < 20 || len(pub.ExistingReportReason) > 2000 || !utf8.ValidString(pub.ExistingReportReason) || suspiciousPublication.MatchString(pub.ExistingReportReason) {
			return errors.New("selected report requires a useful explanation of the matching symptoms and affected build")
		}
	} else if pub.ExistingReportReason != "" {
		return errors.New("related report explanation requires a GitHub report URL")
	}
	if pub.Evidence != "" && (!strings.HasPrefix(pub.Evidence, "### AO-collected evidence\n\n") || !strings.HasSuffix(pub.Evidence, "\n\n"+evidenceMarker(pub.Marker))) {
		return errors.New("prepared evidence is invalid")
	}
	return nil
}

func safePublishError(err error) string {
	text := Redact(err.Error())
	if len(text) > 1000 {
		text = text[:1000]
	}
	return text
}

type publicationAPI struct {
	client *http.Client
	base   *url.URL
	token  string
}
type githubUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

func (u githubUser) identity() string {
	if u.ID <= 0 || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`).MatchString(u.Login) {
		return ""
	}
	return u.Login + "#" + strconv.FormatInt(u.ID, 10)
}

type githubIssue struct {
	Number      int             `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	HTMLURL     string          `json:"html_url"`
	User        githubUser      `json:"user"`
	PullRequest json.RawMessage `json:"pull_request"`
}
type githubComment struct {
	Body    string     `json:"body"`
	HTMLURL string     `json:"html_url"`
	User    githubUser `json:"user"`
}
type githubSearch struct {
	TotalCount int           `json:"total_count"`
	Incomplete bool          `json:"incomplete_results"`
	Items      []githubIssue `json:"items"`
}
type publicationHTTPError struct {
	Status      int
	Message     string
	RateLimited bool
}

func (e *publicationHTTPError) Error() string {
	if e.RateLimited {
		return fmt.Sprintf("GitHub rate limit, HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("GitHub HTTP %d: %s", e.Status, e.Message)
}
func definiteRejection(err error) bool {
	var e *publicationHTTPError
	return errors.As(err, &e) && e.Status >= 400 && e.Status < 500 && e.Status != http.StatusRequestTimeout
}
func publicationFailureState(err error) string {
	var e *publicationHTTPError
	if errors.As(err, &e) && e.Status == http.StatusUnauthorized {
		return PublicationAuthRequired
	}
	return PublicationBlocked
}

func (p *Publisher) client() (*http.Client, *url.URL, error) {
	base, err := url.Parse(strings.TrimRight(p.BaseURL, "/"))
	if err != nil || base == nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, nil, errors.New("invalid GitHub API base URL")
	}
	client := http.DefaultClient
	if p.HTTPClient != nil {
		client = p.HTTPClient
	}
	restrictedClient := *client
	previous := restrictedClient.CheckRedirect
	restrictedClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !sameOrigin(req.URL, base) {
			return errors.New("GitHub redirect changed trusted origin")
		}
		if len(via) >= 10 {
			return errors.New("too many GitHub redirects")
		}
		if previous != nil {
			return previous(req, via)
		}
		return nil
	}
	return &restrictedClient, base, nil
}
func sameOrigin(a, b *url.URL) bool {
	return a.User == nil && a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host)
}
func (a publicationAPI) validAPIURL(raw, path string) bool {
	u, err := url.Parse(raw)
	return err == nil && sameOrigin(u, a.base) && u.Path == path && u.RawQuery == "" && u.Fragment == ""
}
func (a publicationAPI) get(ctx context.Context, path string, out any) error {
	return a.request(ctx, http.MethodGet, path, nil, out)
}
func (a publicationAPI) post(ctx context.Context, path string, in, out any) error {
	return a.request(ctx, http.MethodPost, path, in, out)
}
func (a publicationAPI) request(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader = http.NoBody
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base.String()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		// Avoid a URL-bearing transport error, which may contain local proxy
		// credentials. The cause remains available through errors.Is.
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return fmt.Errorf("GitHub request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubResponse+1))
	if err != nil {
		return fmt.Errorf("reading GitHub response: %w", err)
	}
	if len(data) > maxGitHubResponse {
		return errors.New("GitHub response exceeded size limit")
	}
	expected := http.StatusOK
	if method == http.MethodPost {
		expected = http.StatusCreated
	}
	if resp.StatusCode != expected {
		var apiError struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &apiError)
		message := strings.ReplaceAll(apiError.Message, a.token, "[REDACTED]")
		return &publicationHTTPError{Status: resp.StatusCode, Message: safePublishError(errors.New(message)), RateLimited: resp.StatusCode == http.StatusTooManyRequests || resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != ""}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding GitHub response: %w", err)
	}
	return nil
}

func validRepository(repo string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(repo)
}
func validRepositoryURL(raw, repo string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && strings.EqualFold(u.Path, "/"+repo) && u.RawQuery == "" && u.Fragment == ""
}
func validReportURL(raw, repo string, allowPR, comment bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.RawPath != "" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(strings.Join(parts[:2], "/"), repo) || (parts[2] != "issues" && (!allowPR || parts[2] != "pull")) {
		return false
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 || strconv.Itoa(number) != parts[3] {
		return false
	}
	if comment {
		id := strings.TrimPrefix(u.Fragment, "issuecomment-")
		n, err := strconv.ParseInt(id, 10, 64)
		return strings.HasPrefix(u.Fragment, "issuecomment-") && err == nil && n > 0
	}
	return u.Fragment == ""
}

var priorReportMarker = regexp.MustCompile(`\s*<!-- ao-triage-report-[a-f0-9]{32} -->\s*$`)

func normalizedReport(text string) string {
	text = priorReportMarker.ReplaceAllString(text, "")
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}
func searchTerms(title string) string {
	var terms []string
	for _, part := range strings.FieldsFunc(title, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(part) >= 2 {
			terms = append(terms, part)
		}
		if len(terms) == 8 {
			break
		}
	}
	return strings.Join(terms, " ")
}
func (a publicationAPI) search(ctx context.Context, query string) ([]githubIssue, error) {
	var result githubSearch
	if err := a.get(ctx, "/search/issues?per_page=30&q="+url.QueryEscape(query), &result); err != nil {
		return nil, err
	}
	if result.Incomplete || result.TotalCount > len(result.Items) || len(result.Items) > 30 {
		return nil, errors.New("GitHub search was truncated or incomplete; duplicate status is unverified")
	}
	return result.Items, nil
}
func (a publicationAPI) readIssue(ctx context.Context, pub Publication, item githubIssue) (githubIssue, error) {
	if item.Number <= 0 || !validReportURL(item.HTMLURL, pub.Repository, true, false) || !strings.HasSuffix(item.HTMLURL, "/"+strconv.Itoa(item.Number)) {
		return githubIssue{}, errors.New("GitHub search returned an invalid report URL")
	}
	var issue githubIssue
	if err := a.get(ctx, "/repos/"+pub.Repository+"/issues/"+strconv.Itoa(item.Number), &issue); err != nil {
		return githubIssue{}, err
	}
	if issue.Number != item.Number || !validReportURL(issue.HTMLURL, pub.Repository, true, false) || issue.HTMLURL != item.HTMLURL {
		return githubIssue{}, errors.New("GitHub returned an invalid report URL")
	}
	return issue, nil
}
func (a publicationAPI) findDuplicate(ctx context.Context, pub Publication) (string, error) {
	terms := searchTerms(pub.Title)
	if terms == "" {
		return "", errors.New("report title has no searchable words")
	}
	items, err := a.search(ctx, "repo:"+pub.Repository+" in:title "+terms)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		issue, err := a.readIssue(ctx, pub, item)
		if err != nil {
			return "", err
		}
		body := strings.TrimSuffix(pub.Body, "\n\n"+pub.Marker)
		// Search rank and a similar title are evidence to read a candidate,
		// not proof of a duplicate. Only exact normalized observations match.
		if normalizedReport(issue.Title) == normalizedReport(pub.Title) && normalizedReport(issue.Body) == normalizedReport(body) {
			return issue.HTMLURL, nil
		}
	}
	return "", nil
}
func (a publicationAPI) reconcileIssue(ctx context.Context, pub Publication) (githubIssue, error) {
	marker := strings.TrimSuffix(strings.TrimPrefix(pub.Marker, "<!-- "), " -->")
	items, err := a.search(ctx, "repo:"+pub.Repository+" in:body \""+marker+"\"")
	if err != nil {
		return githubIssue{}, fmt.Errorf("issue outcome is unknown; marker search failed: %w", err)
	}
	var found *githubIssue
	for _, item := range items {
		issue, err := a.readIssue(ctx, pub, item)
		if err != nil {
			return githubIssue{}, err
		}
		if strings.Contains(issue.Body, pub.Marker) && issue.User.identity() == pub.Identity && validReportURL(issue.HTMLURL, pub.Repository, false, false) {
			if found != nil {
				return githubIssue{}, errors.New("issue outcome is unknown; multiple reports contain this marker")
			}
			found = &issue
		}
	}
	if found == nil {
		return githubIssue{}, errors.New("issue outcome is unknown; the report marker is not visible yet. No second issue was sent")
	}
	return *found, nil
}
func (a publicationAPI) reconcileEvidence(ctx context.Context, pub Publication, number string) (githubComment, error) {
	// Read comments directly, avoiding the search index for evidence recovery.
	for page := 1; page <= 10; page++ {
		var comments []githubComment
		if err := a.get(ctx, "/repos/"+pub.Repository+"/issues/"+number+"/comments?per_page=100&page="+strconv.Itoa(page), &comments); err != nil {
			return githubComment{}, fmt.Errorf("evidence outcome is unknown; reading comments: %w", err)
		}
		var found *githubComment
		for _, comment := range comments {
			if strings.Contains(comment.Body, evidenceMarker(pub.Marker)) && comment.User.identity() == pub.Identity {
				if !validReportURL(comment.HTMLURL, pub.Repository, false, true) || !strings.HasPrefix(comment.HTMLURL, pub.IssueURL+"#issuecomment-") {
					return githubComment{}, errors.New("GitHub returned an invalid evidence URL")
				}
				if found != nil {
					return githubComment{}, errors.New("evidence outcome is unknown; multiple comments contain this marker")
				}
				found = &comment
			}
		}
		if found != nil {
			return *found, nil
		}
		if len(comments) < 100 {
			return githubComment{}, errors.New("evidence outcome is unknown; its marker is not visible yet. No second comment was sent")
		}
	}
	return githubComment{}, errors.New("evidence outcome is unknown; comment listing was truncated")
}
