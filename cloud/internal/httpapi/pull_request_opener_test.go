package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/go-chi/chi/v5"
)

const (
	openerOrchestratorSession = "22222222-2222-2222-2222-222222222222"
	openerTeammateSession     = "33333333-3333-3333-3333-333333333333"
)

type openerPRStore struct {
	Store
	openerCalls map[string]int
}

// An orchestrator's list holds its own pull requests and those of every other
// session in the project, a teammate's among them.
func (openerPRStore) ListPullRequestsBySession(context.Context, domain.Principal, string, string) ([]domain.PullRequest, error) {
	return []domain.PullRequest{
		{ID: "pr-1", SessionID: openerOrchestratorSession, Provider: "github", Repository: "octo/app", Number: 1, Author: "ao-by-untrivial[bot]", State: contract.PRStateMerged},
		{ID: "pr-2", SessionID: openerOrchestratorSession, Provider: "github", Repository: "octo/lib", Number: 2, Author: "octocat", State: contract.PRStateMerged},
		{ID: "pr-3", SessionID: openerOrchestratorSession, Provider: "github", Repository: "octo/web", Number: 3, Author: "aoagentsdev123[bot]", State: contract.PRStateMerged},
		{ID: "pr-4", SessionID: openerTeammateSession, Provider: "github", Repository: "octo/app", Number: 4, Author: "ao-by-untrivial[bot]", State: contract.PRStateMerged},
		{ID: "pr-5", SessionID: openerTeammateSession, Provider: "github", Repository: "octo/app", Number: 5, Author: "ao-by-untrivial[bot]", State: contract.PRStateMerged},
	}, nil
}

func (openerPRStore) PullRequestSnapshot(context.Context, string, string) (domain.PullRequestSnapshot, error) {
	return domain.PullRequestSnapshot{}, nil
}

func (s *openerPRStore) SessionPullRequestOpener(_ context.Context, _, sessionID string) (domain.PullRequestOpener, error) {
	s.openerCalls[sessionID]++
	if sessionID == openerTeammateSession {
		return domain.PullRequestOpener{Name: "Grace Hopper", GitHubLogin: "grace"}, nil
	}
	return domain.PullRequestOpener{Name: "Ada Lovelace", GitHubLogin: "ada", AvatarURL: "https://avatars.example/ada"}, nil
}

func TestListSessionPullRequestsNamesThePersonBehindAppOpenedPullRequests(t *testing.T) {
	store := &openerPRStore{openerCalls: make(map[string]int)}
	server := New(Options{Store: store})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("orgId", "11111111-1111-1111-1111-111111111111")
	route.URLParams.Add("sessionId", openerOrchestratorSession)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	server.listSessionPullRequests(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var result struct {
		PullRequests []struct {
			Author   string `json:"author"`
			OpenedBy *struct {
				Name        string `json:"name"`
				GitHubLogin string `json:"githubLogin"`
				AvatarURL   string `json:"avatarUrl"`
			} `json:"openedBy"`
		} `json:"pullRequests"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.PullRequests) != 5 {
		t.Fatalf("pull requests = %+v", result.PullRequests)
	}
	for _, index := range []int{0, 2} {
		got := result.PullRequests[index]
		if got.OpenedBy == nil || got.OpenedBy.Name != "Ada Lovelace" || got.OpenedBy.GitHubLogin != "ada" || got.OpenedBy.AvatarURL == "" {
			t.Fatalf("pull request %d openedBy = %+v", index, got.OpenedBy)
		}
		if got.Author == "" {
			t.Fatalf("pull request %d lost its GitHub author", index)
		}
	}
	if result.PullRequests[1].OpenedBy != nil {
		t.Fatalf("a person's own pull request got openedBy %+v", result.PullRequests[1].OpenedBy)
	}
	// A teammate's pull request names the teammate, not the orchestrator's person.
	for _, index := range []int{3, 4} {
		if got := result.PullRequests[index].OpenedBy; got == nil || got.GitHubLogin != "grace" {
			t.Fatalf("teammate pull request %d openedBy = %+v, want grace", index, got)
		}
	}
	if store.openerCalls[openerOrchestratorSession] != 1 || store.openerCalls[openerTeammateSession] != 1 {
		t.Fatalf("opener lookups = %v, want once per session per request", store.openerCalls)
	}
}
