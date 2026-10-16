package worker

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

// ExtraRepoDirName derives a safe local directory name from a repo URL, e.g.
// "https://github.com/owner/ChartSnip" -> "ChartSnip" (domain.RepoDirName, which
// the control plane also uses to validate a project's repositories).
func ExtraRepoDirName(repoURL string) string {
	return domain.RepoDirName(repoURL)
}

// ExtraRepoPath returns where an additional dev-kit repo is checked out: a
// sibling of the primary workspace checkout. Cloning beside the primary repo
// (rather than in a worker-internal data dir) lets the agent, whose working
// directory is the primary repo, reach it at ../<name>. The launcher and the
// worker both compute the path here so the on-disk location and the path shown
// to the agent always agree.
func ExtraRepoPath(workspace, repoURL string) string {
	return filepath.Join(filepath.Dir(workspace), ExtraRepoDirName(repoURL))
}

// RepoCheckout is one repository checkout of a session: the main repository's
// or one of the project's additional repositories beside it.
type RepoCheckout struct {
	// Name is the checkout's folder name.
	Name string
	// Path is the checkout's absolute path.
	Path string
	// URL is the repository's URL as the project lists it.
	URL string
	// FullName is the repository's lower-case GitHub owner/name, or empty when
	// the URL is not a GitHub repository URL.
	FullName string
	// Branch is the branch an additional repository was cloned at; empty for
	// its default branch and for the main repository.
	Branch string
	// Main marks the main repository's checkout.
	Main bool
}

// SessionRepoCheckouts lists a session's checkouts, the main repository's
// first. A standalone session (no repository) lists only its workspace.
func SessionRepoCheckouts(workspace, mainURL string, extras []RepoRef) []RepoCheckout {
	main := RepoCheckout{Name: filepath.Base(workspace), Path: workspace, URL: mainURL, Main: true}
	if identity, err := githubRepositoryIdentity(mainURL); err == nil {
		main.FullName = identity
	}
	checkouts := []RepoCheckout{main}
	if IsScratchRepositoryURL(mainURL) {
		return checkouts
	}
	for _, repo := range extras {
		if strings.TrimSpace(repo.URL) == "" {
			continue
		}
		checkout := RepoCheckout{
			Name: ExtraRepoDirName(repo.URL), Path: ExtraRepoPath(workspace, repo.URL),
			URL: repo.URL, Branch: strings.TrimSpace(repo.Branch),
		}
		if identity, err := githubRepositoryIdentity(repo.URL); err == nil {
			checkout.FullName = identity
		}
		checkouts = append(checkouts, checkout)
	}
	return checkouts
}

// ErrUnknownRepository means a repository selector names none of the session's
// repositories.
var ErrUnknownRepository = errors.New("repository is not one of this session's repositories")

// ResolveRepoCheckout picks the checkout a selector names: an empty selector
// is the main repository; otherwise the selector is a repository's GitHub
// owner/name, URL or folder name, compared without case.
func ResolveRepoCheckout(checkouts []RepoCheckout, selector string) (RepoCheckout, error) {
	selector = strings.TrimSpace(selector)
	if len(checkouts) == 0 {
		return RepoCheckout{}, ErrUnknownRepository
	}
	if selector == "" {
		return checkouts[0], nil
	}
	fullName := strings.ToLower(strings.TrimSuffix(strings.Trim(selector, "/"), ".git"))
	if identity, err := githubRepositoryIdentity(selector); err == nil {
		fullName = identity
	}
	for _, checkout := range checkouts {
		if (checkout.FullName != "" && checkout.FullName == fullName) || strings.EqualFold(checkout.Name, selector) {
			return checkout, nil
		}
	}
	return RepoCheckout{}, ErrUnknownRepository
}
