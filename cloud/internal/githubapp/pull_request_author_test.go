package githubapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// authorStubStore records who a pull request was recorded as authored by and
// returns a stored GitHub user connection for the session's creator.
type authorStubStore struct {
	checkoutStubStore
	connection     *domain.GitHubUserConnection
	recordedAuthor string
}

func (s *authorStubStore) GitHubUserConnection(_ context.Context, userID string) (domain.GitHubUserConnection, error) {
	if s.connection == nil || s.connection.UserID != userID {
		return domain.GitHubUserConnection{}, postgres.ErrNotFound
	}
	return *s.connection, nil
}

func (s *authorStubStore) CreatePullRequestRecord(
	_ context.Context, _, _ string, _, repository, author string, number int, url, head, base, _, _ string, _, _, _ int,
) (domain.PullRequest, error) {
	s.recordedAuthor = author
	return domain.PullRequest{Repository: repository, Author: author, Number: number, URL: url, SourceBranch: head, TargetBranch: base}, nil
}

func userConnectionFor(t *testing.T, userID, token string) *domain.GitHubUserConnection {
	t.Helper()
	ciphertext, nonce, err := Encrypt(make([]byte, 32), []byte(token), githubUserTokenAssociatedData(userID, 42, "access"))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour)
	return &domain.GitHubUserConnection{
		UserID: userID, GitHubUserID: 42, GitHubLogin: "octocat",
		AccessTokenCiphertext: ciphertext, AccessTokenNonce: nonce, AccessTokenExpiresAt: &expires,
	}
}

// authorPullRequestServer opens pull requests as whoever the bearer token
// belongs to, refusing the user token when userRefuses is set.
func authorPullRequestServer(t *testing.T, backend *installationRepositoryServer, userRefuses bool, bearers *[]string) *httptest.Server {
	t.Helper()
	inner := backend.handler(t)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/pulls") {
			inner(w, r)
			return
		}
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		*bearers = append(*bearers, bearer)
		login := "ao-app[bot]"
		if bearer == "ghu_user" {
			if userRefuses {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
				return
			}
			login = "octocat"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number": 7, "html_url": "https://github.com/octo/app/pull/7",
			"head": map[string]any{"sha": "abc", "ref": "ao/s1"}, "base": map[string]any{"ref": "main"},
			"user": map[string]any{"login": login}, "state": "open", "title": "t",
		})
	}))
}

func TestRaisePullRequestOpensItAsTheSessionsGitHubUser(t *testing.T) {
	for _, tc := range []struct {
		name        string
		connected   bool
		userRefuses bool
		wantAuthor  string
		wantBearers int
	}{
		{name: "connected user is the author", connected: true, wantAuthor: "octocat", wantBearers: 1},
		{name: "no GitHub connection falls back to the App", wantAuthor: "ao-app[bot]", wantBearers: 1},
		{name: "GitHub refusing the user falls back to the App", connected: true, userRefuses: true, wantAuthor: "ao-app[bot]", wantBearers: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &installationRepositoryServer{repos: []Repository{{ID: 1, FullName: "octo/app"}}}
			var bearers []string
			server := authorPullRequestServer(t, backend, tc.userRefuses, &bearers)
			defer server.Close()
			primary := primaryContext()
			primary.SessionUserID = "user-1"
			store := &authorStubStore{checkoutStubStore: checkoutStubStore{primary: primary}}
			if tc.connected {
				store.connection = userConnectionFor(t, "user-1", "ghu_user")
			}
			svc := newCheckoutTestService(t, store, newAppTestClient(t, server.URL, server.Client()))
			pr, err := svc.RaisePullRequest(context.Background(), "org-1", "sess-1", domain.RaisePullRequest{Title: "t", HeadBranch: "ao/s1"})
			if err != nil {
				t.Fatalf("RaisePullRequest: %v", err)
			}
			if pr.Author != tc.wantAuthor || store.recordedAuthor != tc.wantAuthor {
				t.Fatalf("author = %q recorded %q, want %q", pr.Author, store.recordedAuthor, tc.wantAuthor)
			}
			if len(bearers) != tc.wantBearers {
				t.Fatalf("pull request calls = %v", bearers)
			}
			if tc.connected && bearers[0] != "ghu_user" {
				t.Fatalf("first call used %q, want the user token", bearers[0])
			}
		})
	}
}

func TestIsAppAccountMatchesBothLoginForms(t *testing.T) {
	svc := newCheckoutTestService(t, &checkoutStubStore{}, newAppTestClient(t, "https://api.example.com", http.DefaultClient))
	for login, want := range map[string]bool{
		"ao-test[bot]": true, "AO-Test": true, " ao-test ": true,
		"octocat": false, "ao-test-other[bot]": false, "": false,
	} {
		if got := svc.IsAppAccount(login); got != want {
			t.Errorf("IsAppAccount(%q) = %v, want %v", login, got, want)
		}
	}
	var none *Service
	if none.IsAppAccount("ao-test") {
		t.Error("a nil service matched an App account")
	}
}
