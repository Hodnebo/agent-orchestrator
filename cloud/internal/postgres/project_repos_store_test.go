package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func seedGitHubRepository(t *testing.T, admin *pgxpool.Pool, fullName string) int64 {
	t.Helper()
	ctx := context.Background()
	repositoryID := time.Now().UnixNano()
	url := "https://github.com/" + fullName
	if _, err := admin.Exec(ctx, `INSERT INTO ao_github_repositories
		(github_repository_id, github_owner_account_id, name, full_name, html_url, clone_url, visibility)
		VALUES ($1, 1, $2, $3, $4, $5, 'private')`, repositoryID, fullName[strings.Index(fullName, "/")+1:], fullName, url, url+".git"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM ao_github_repository_grants WHERE github_repository_id = $1`, repositoryID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM ao_github_repositories WHERE github_repository_id = $1`, repositoryID)
	})
	return repositoryID
}

func setProjectRepos(t *testing.T, admin *pgxpool.Pool, projectID, repositoryURL, config string) {
	t.Helper()
	if _, err := admin.Exec(context.Background(), `UPDATE ao_projects SET repository_url = $1, config = $2::jsonb WHERE id = $3`,
		repositoryURL, config, projectID); err != nil {
		t.Fatal(err)
	}
}

// A pull request on one of the project's additional repositories, from the
// session branch every checkout is on, belongs to the session.
func TestSessionForGitHubPullRequestHeadMatchesAnAdditionalRepository(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	extraName := "octo/extra-" + suffix
	extraID := seedGitHubRepository(t, admin, extraName)
	otherID := seedGitHubRepository(t, admin, "octo/other-"+suffix)
	if _, err := admin.Exec(ctx, `UPDATE ao_sessions SET branch = 'ao/s1' WHERE id = $1`, fixture.sessionID); err != nil {
		t.Fatal(err)
	}
	for _, config := range []string{
		fmt.Sprintf(`{"repos":[{"url":"https://github.com/%s.git/"}]}`, extraName),
		fmt.Sprintf(`{"coder":{"extraRepos":[{"url":"https://github.com/%s"}]}}`, strings.ToUpper(extraName)),
	} {
		setProjectRepos(t, admin, fixture.projectID, "https://github.com/octo/main-"+suffix, config)
		got, err := store.SessionForGitHubPullRequestHead(ctx, fixture.orgID, extraID, "ao/s1", "abc")
		if err != nil || got != fixture.sessionID {
			t.Fatalf("%s: matched %q %v", config, got, err)
		}
		if _, err := store.SessionForGitHubPullRequestHead(ctx, fixture.orgID, otherID, "ao/s1", "abc"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s: undeclared repository err = %v", config, err)
		}
	}
	// "repos" replaces the legacy list, even when empty.
	setProjectRepos(t, admin, fixture.projectID, "https://github.com/octo/main-"+suffix,
		fmt.Sprintf(`{"repos":[],"coder":{"extraRepos":[{"url":"https://github.com/%s"}]}}`, extraName))
	if _, err := store.SessionForGitHubPullRequestHead(ctx, fixture.orgID, extraID, "ao/s1", "abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleared repos err = %v", err)
	}
	// Values of the wrong shape never break the query.
	setProjectRepos(t, admin, fixture.projectID, "https://github.com/octo/main-"+suffix, `{"repos":"nope","coder":{"extraRepos":[1]}}`)
	if _, err := store.SessionForGitHubPullRequestHead(ctx, fixture.orgID, extraID, "ao/s1", "abc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("malformed repos err = %v", err)
	}
}

func TestGitHubRepositoryAccessReportsEachRepositorysInstallation(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	mainID := seedGitHubRepository(t, admin, "octo/main-"+suffix)
	extraID := seedGitHubRepository(t, admin, "octo/extra-"+suffix)
	foreignID := seedGitHubRepository(t, admin, "other/lib-"+suffix)
	seedGitHubRepository(t, admin, "octo/ungranted-"+suffix)
	installations := map[string]string{}
	for _, account := range []string{"octo", "other"} {
		var id string
		if err := admin.QueryRow(ctx, `INSERT INTO ao_github_installations
			(org_id, github_installation_id, github_account_id, account_login, account_type, repository_selection, installed_by_user_id)
			VALUES ($1, $2, $2, $3, 'Organization', 'selected', $4) RETURNING id::text`,
			fixture.orgID, time.Now().UnixNano(), account, fixture.userID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		installations[account] = id
	}
	for repositoryID, account := range map[int64]string{mainID: "octo", extraID: "octo", foreignID: "other"} {
		if _, err := admin.Exec(ctx, `INSERT INTO ao_github_repository_grants (org_id, installation_id, github_repository_id, repository_selection)
			VALUES ($1, $2, $3, 'selected')`, fixture.orgID, installations[account], repositoryID); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, id := range installations {
			_, _ = admin.Exec(context.Background(), `DELETE FROM ao_github_repository_grants WHERE installation_id = $1`, id)
			_, _ = admin.Exec(context.Background(), `DELETE FROM ao_github_installations WHERE id = $1`, id)
		}
	})

	access, err := store.GitHubRepositoryAccess(ctx, domain.Principal{UserID: fixture.userID, Provider: "local"}, fixture.orgID, mainID,
		[]string{"OCTO/extra-" + suffix, "other/lib-" + suffix, "octo/ungranted-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	if access.MainURL != "https://github.com/octo/main-"+suffix || access.MainInstallation != installations["octo"] {
		t.Fatalf("main = %q %q", access.MainURL, access.MainInstallation)
	}
	want := map[string]string{"octo/extra-" + suffix: installations["octo"], "other/lib-" + suffix: installations["other"]}
	if len(access.Installations) != 2 || access.Installations["octo/extra-"+suffix] != want["octo/extra-"+suffix] ||
		access.Installations["other/lib-"+suffix] != want["other/lib-"+suffix] {
		t.Fatalf("installations = %v, want %v", access.Installations, want)
	}
}

func TestUpdateProjectSettingsChecksRepositorySettingsAgainstTheMainRepository(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	setProjectRepos(t, admin, fixture.projectID, "https://github.com/octo/alice", `{"postCreate":["npm ci"]}`)

	project, err := store.UpdateProjectSettings(ctx, principal, fixture.orgID, fixture.projectID, domain.ProjectSettingsPatch{
		Config: json.RawMessage(`{"repos":[{"url":"https://www.github.com/octo/web.git"}],"postCreate":["npm ci",{"command":"make up","repository":"https://github.com/octo/web"}],"agentStartsIn":"parent"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(project.Config, &config); err != nil {
		t.Fatal(err)
	}
	if string(config["repos"]) != `[{"url": "https://github.com/octo/web"}]` && string(config["repos"]) != `[{"url":"https://github.com/octo/web"}]` {
		t.Fatalf("repos = %s", config["repos"])
	}
	for _, bad := range []string{
		`{"repos":[{"url":"https://github.com/octo/alice"}]}`,
		`{"postCreate":[{"command":"x","repository":"https://github.com/octo/elsewhere"}]}`,
		// Removing a repository a step still runs in is refused.
		`{"repos":[]}`,
	} {
		if _, err := store.UpdateProjectSettings(ctx, principal, fixture.orgID, fixture.projectID, domain.ProjectSettingsPatch{Config: json.RawMessage(bad)}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", bad, err)
		}
	}
}
