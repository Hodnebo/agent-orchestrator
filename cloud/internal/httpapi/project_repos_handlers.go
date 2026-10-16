package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

// projectRepositoryAccessStore reads the GitHub App grants behind a project's
// repositories (postgres.Store.GitHubRepositoryAccess).
type projectRepositoryAccessStore interface {
	GitHubRepositoryAccess(ctx context.Context, principal domain.Principal, orgID string, mainRepositoryID int64, fullNames []string) (domain.GitHubRepositoryAccess, error)
}

// projectRepoProblem is a project repository setting the request cannot use,
// rendered as a 422 with its code.
type projectRepoProblem struct {
	code    string
	message string
}

func (p *projectRepoProblem) write(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusUnprocessableEntity, p.code, p.message)
}

// normalizeNewProjectRepoConfig prepares a new project's multi-repo settings:
// it moves a legacy coder.extraRepos list to "repos" and validates and
// canonicalizes the settings against the main repository (see
// domain.NormalizeProjectRepoSettings).
func normalizeNewProjectRepoConfig(mainURL string, config json.RawMessage) (json.RawMessage, *projectRepoProblem) {
	adopted, err := domain.AdoptLegacyExtraRepos(config)
	if err != nil {
		return nil, &projectRepoProblem{"validation_error", err.Error()}
	}
	normalized, err := domain.NormalizeProjectRepoSettings(mainURL, adopted)
	if err != nil {
		return nil, &projectRepoProblem{"validation_error", err.Error()}
	}
	return normalized, nil
}

// additionalRepoFullNames returns owner/name for each additional repository of
// a (normalized) project config.
func additionalRepoFullNames(config json.RawMessage) []string {
	repos := domain.ProjectAdditionalRepos(config)
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		if _, fullName, err := domain.CanonicalGitHubRepoURL(repo.URL); err == nil {
			names = append(names, fullName)
		}
	}
	return names
}

// checkAdditionalRepoAccess requires every additional repository to be granted
// to the organization's GitHub App, through the same installation as the main
// repository: a session's checkout and push tokens are installation tokens, so
// a repository of another installation could never be cloned or pushed.
// mainInstallation is the main repository's installation, or "" when the
// project's repository access does not come from the App (a personal access
// token project), in which case only the grant is not required either.
func checkAdditionalRepoAccess(access domain.GitHubRepositoryAccess, mainInstallation string, fullNames []string) *projectRepoProblem {
	if mainInstallation == "" {
		return nil
	}
	for _, name := range fullNames {
		installation, granted := access.Installations[strings.ToLower(name)]
		if !granted {
			return &projectRepoProblem{"repository_not_accessible", fmt.Sprintf(
				"AO's GitHub App can't access %s. Give the App access to it in GitHub, then try again.", name)}
		}
		if installation != mainInstallation {
			return &projectRepoProblem{"repository_installation_mismatch", fmt.Sprintf(
				"%s is connected through a different GitHub App installation than the main repository. "+
					"All of a project's repositories must come from the same GitHub account or organization.", name)}
		}
	}
	return nil
}

// projectRepositoryAccess reads the grants for a project's repositories, or
// reports ok=false when the store cannot (a test double), in which case the
// grant check is skipped.
func (s *Server) projectRepositoryAccess(
	ctx context.Context, principal domain.Principal, orgID string, mainRepositoryID int64, fullNames []string,
) (domain.GitHubRepositoryAccess, bool, error) {
	store, ok := s.store.(projectRepositoryAccessStore)
	if !ok {
		return domain.GitHubRepositoryAccess{}, false, nil
	}
	access, err := store.GitHubRepositoryAccess(ctx, principal, orgID, mainRepositoryID, fullNames)
	return access, err == nil, err
}

// validatePatchedProjectRepos checks the additional repositories a settings
// patch sets against the project's main repository and its GitHub App grants.
// It reports whether the request may continue and writes the error when not.
func (s *Server) validatePatchedProjectRepos(w http.ResponseWriter, r *http.Request, orgID, projectID string, patch json.RawMessage) bool {
	var keys map[string]json.RawMessage
	if len(patch) == 0 || json.Unmarshal(patch, &keys) != nil {
		return true
	}
	rawRepos, setsRepos := keys["repos"]
	if !setsRepos {
		return true
	}
	project, err := s.store.GetProject(r.Context(), principalFrom(r), orgID, projectID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return false
	}
	normalized, err := domain.NormalizeProjectRepoSettings(project.RepositoryURL, json.RawMessage(`{"repos":`+string(rawRepos)+`}`))
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return false
	}
	names := additionalRepoFullNames(normalized)
	if len(names) == 0 || project.GitHubRepositoryID == nil {
		return true
	}
	access, ok, err := s.projectRepositoryAccess(r.Context(), principalFrom(r), orgID, *project.GitHubRepositoryID, names)
	if err != nil {
		s.writeStoreError(w, r, err)
		return false
	}
	if !ok {
		return true
	}
	if problem := checkAdditionalRepoAccess(access, access.MainInstallation, names); problem != nil {
		problem.write(w, r)
		return false
	}
	return true
}

// prepareGitHubProjectRepos validates the multi-repo settings of a project
// being created on a GitHub App repository: the main repository's URL comes
// from its grant, and every additional repository must be granted through the
// same installation. It returns the normalized config and whether the request
// may continue, writing the error when not.
func (s *Server) prepareGitHubProjectRepos(
	w http.ResponseWriter, r *http.Request, orgID string, githubRepositoryID int64, config json.RawMessage,
) (json.RawMessage, bool) {
	adopted, err := domain.AdoptLegacyExtraRepos(config)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "validation_error", err.Error())
		return nil, false
	}
	access, checked, err := s.projectRepositoryAccess(r.Context(), principalFrom(r), orgID, githubRepositoryID, additionalRepoFullNames(adopted))
	if err != nil {
		s.writeStoreError(w, r, err)
		return nil, false
	}
	normalized, problem := normalizeNewProjectRepoConfig(access.MainURL, adopted)
	if problem != nil {
		problem.write(w, r)
		return nil, false
	}
	if checked && access.MainURL != "" {
		if problem := checkAdditionalRepoAccess(access, access.MainInstallation, additionalRepoFullNames(normalized)); problem != nil {
			problem.write(w, r)
			return nil, false
		}
	}
	return normalized, true
}
