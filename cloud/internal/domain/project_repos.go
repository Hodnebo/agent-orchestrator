package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// A cloud project works on one main repository (ao_projects.repository_url)
// plus any number of additional repositories, kept under the project's Config
// as {"repos": [{"url": ..., "branch": ...}]}. Every session clones them all
// side by side, on every sandbox provider, and the agent starts in the main
// repository with access to the others. Projects set up before "repos"
// existed carry them as the coder dev kit's {"coder": {"extraRepos": [...]}},
// which is still read when "repos" is absent.

// MaxProjectAdditionalRepos bounds a project's additional repositories.
const MaxProjectAdditionalRepos = 10

// ProjectAgentStartsInRepository (the default) starts the coding agent in the
// main repository's checkout; ProjectAgentStartsInParent starts it in the
// folder that holds every repository's checkout.
const (
	ProjectAgentStartsInRepository = "repository"
	ProjectAgentStartsInParent     = "parent"
)

// MaxProjectRulesBytes bounds each of a project's agentRules and
// orchestratorRules.
const MaxProjectRulesBytes = 16 << 10

// StandaloneRepositoryHost is the host of the placeholder repository URL a
// standalone project (one with no repository) stores in its repository_url:
// https://scratch.ao.local/<id>. A session of such a project starts in an empty,
// git-initialized folder instead of a checkout.
const StandaloneRepositoryHost = "scratch.ao.local"

// StandaloneProjectSource marks a standalone project's Config: {"source": "standalone"}.
const StandaloneProjectSource = "standalone"

// StandaloneRepositoryURL is the placeholder repository URL for a standalone
// project with the given id.
func StandaloneRepositoryURL(id string) string {
	return "https://" + StandaloneRepositoryHost + "/" + id
}

// IsStandaloneRepositoryURL reports whether raw is a standalone project's
// placeholder repository URL.
func IsStandaloneRepositoryURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Scheme == "https" &&
		strings.EqualFold(parsed.Hostname(), StandaloneRepositoryHost) &&
		parsed.Port() == "" && parsed.User == nil
}

// RepoDirName is the folder a repository is checked out into beside the main
// repository, derived from its URL: "https://github.com/owner/ChartSnip" gives
// "ChartSnip".
func RepoDirName(repoURL string) string {
	trimmed := strings.TrimSuffix(strings.Trim(repoURL, "/"), ".git")
	name := trimmed
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		name = trimmed[idx+1:]
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
	if name == "" || name == "." || name == ".." {
		return "repo"
	}
	return name
}

// reservedRepoDirNames are folders beside the main checkout that AO itself
// uses: the managed providers' main checkout and worker state.
var reservedRepoDirNames = []string{"repository", ".ao"}

// ProjectAdditionalRepos returns a project's additional repositories: its
// "repos", or the legacy coder dev kit "extraRepos" when "repos" is absent.
func ProjectAdditionalRepos(config json.RawMessage) []RepoRef {
	if len(config) == 0 {
		return nil
	}
	var envelope struct {
		Repos *[]RepoRef `json:"repos"`
	}
	if err := json.Unmarshal(config, &envelope); err == nil && envelope.Repos != nil {
		return nonEmptyRepos(*envelope.Repos)
	}
	if coder, ok := DecodeProjectCoderConfig(config); ok {
		return nonEmptyRepos(coder.ExtraRepos)
	}
	return nil
}

func nonEmptyRepos(repos []RepoRef) []RepoRef {
	kept := make([]RepoRef, 0, len(repos))
	for _, repo := range repos {
		if strings.TrimSpace(repo.URL) != "" {
			kept = append(kept, RepoRef{URL: strings.TrimSpace(repo.URL), Branch: strings.TrimSpace(repo.Branch)})
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

// CanonicalGitHubRepoURL returns https://github.com/<owner>/<name> for a GitHub
// repository URL, and the owner/name pair.
func CanonicalGitHubRepoURL(raw string) (canonical, fullName string, err error) {
	parsed, parseErr := url.Parse(strings.TrimSpace(raw))
	if parseErr != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		!(strings.EqualFold(parsed.Hostname(), "github.com") || strings.EqualFold(parsed.Hostname(), "www.github.com")) ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", fmt.Errorf("%q is not a GitHub repository URL", raw)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("%q is not a GitHub repository URL", raw)
	}
	owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
	if !validGitHubName(owner) || !validGitHubName(name) {
		return "", "", fmt.Errorf("%q is not a GitHub repository URL", raw)
	}
	return "https://github.com/" + owner + "/" + name, owner + "/" + name, nil
}

func validGitHubName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 100 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// NormalizeProjectRepos validates a project's additional repositories against
// its main repository and returns them canonicalized: each a GitHub repository,
// none the main repository or listed twice, and each checking out into its own
// folder beside the main one. A blank URL is dropped. mainURL may be a
// standalone placeholder, in which case no additional repository is allowed.
func NormalizeProjectRepos(mainURL string, repos []RepoRef) ([]RepoRef, error) {
	kept := nonEmptyRepos(repos)
	if len(kept) == 0 {
		return nil, nil
	}
	if IsStandaloneRepositoryURL(mainURL) {
		return nil, errors.New("a project without a repository cannot have additional repositories")
	}
	if len(kept) > MaxProjectAdditionalRepos {
		return nil, fmt.Errorf("at most %d additional repositories are allowed", MaxProjectAdditionalRepos)
	}
	mainCanonical, _, mainErr := CanonicalGitHubRepoURL(mainURL)
	dirs := map[string]string{}
	for _, reserved := range reservedRepoDirNames {
		dirs[strings.ToLower(reserved)] = reserved
	}
	if mainErr == nil {
		dirs[strings.ToLower(RepoDirName(mainCanonical))] = mainCanonical
	}
	seen := map[string]bool{}
	out := make([]RepoRef, 0, len(kept))
	for _, repo := range kept {
		canonical, _, err := CanonicalGitHubRepoURL(repo.URL)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(canonical)
		if mainErr == nil && key == strings.ToLower(mainCanonical) {
			return nil, fmt.Errorf("%s is already the project's main repository", canonical)
		}
		if seen[key] {
			return nil, fmt.Errorf("%s is listed twice", canonical)
		}
		seen[key] = true
		dir := RepoDirName(canonical)
		if other, taken := dirs[strings.ToLower(dir)]; taken {
			if other == "repository" || other == ".ao" {
				return nil, fmt.Errorf("%s would check out into the folder %q, which AO uses itself", canonical, dir)
			}
			return nil, fmt.Errorf("%s and %s would check out into the same folder %q", other, canonical, dir)
		}
		dirs[strings.ToLower(dir)] = canonical
		if len(repo.Branch) > 255 || strings.ContainsAny(repo.Branch, " \t\r\n~^:?*[\\") {
			return nil, fmt.Errorf("branch %q of %s is not a valid branch name", repo.Branch, canonical)
		}
		out = append(out, RepoRef{URL: canonical, Branch: repo.Branch})
	}
	return out, nil
}

// ProjectScriptStep is one Setup or Cleanup command. Repository, when set, is
// the canonical URL of the project repository the command runs in; empty runs
// it in the main repository. A step is stored as a plain string when it has
// no repository, so configs written before per-repository steps stay valid.
type ProjectScriptStep struct {
	Command    string `json:"command"`
	Repository string `json:"repository,omitempty"`
}

// UnmarshalJSON accepts a plain command string or {"command", "repository"}.
func (s *ProjectScriptStep) UnmarshalJSON(raw []byte) error {
	var command string
	if err := json.Unmarshal(raw, &command); err == nil {
		*s = ProjectScriptStep{Command: command}
		return nil
	}
	var object struct {
		Command    *string `json:"command"`
		Repository string  `json:"repository"`
	}
	if err := json.Unmarshal(raw, &object); err != nil || object.Command == nil {
		return errors.New("each command must be a string or an object with a command")
	}
	*s = ProjectScriptStep{Command: *object.Command, Repository: strings.TrimSpace(object.Repository)}
	return nil
}

// MarshalJSON writes a step without a repository as a plain string.
func (s ProjectScriptStep) MarshalJSON() ([]byte, error) {
	if s.Repository == "" {
		return json.Marshal(s.Command)
	}
	type step ProjectScriptStep
	return json.Marshal(step(s))
}

// ShellCommand is the command the worker runs from the main repository's
// checkout: the command itself, or, for a step in another repository, the
// command run in a subshell from that repository's folder beside the main one.
func (s ProjectScriptStep) ShellCommand(mainURL string) string {
	if s.Repository == "" || strings.EqualFold(s.Repository, mainURL) {
		return s.Command
	}
	dir := RepoDirName(s.Repository)
	return "(\ncd -- '../" + dir + "' || exit 1\n" + s.Command + "\n)"
}

// ProjectAgentStartsIn returns where a project's coding agent starts:
// ProjectAgentStartsInRepository (the default) or ProjectAgentStartsInParent.
func ProjectAgentStartsIn(config json.RawMessage) string {
	var envelope struct {
		AgentStartsIn string `json:"agentStartsIn"`
	}
	if len(config) > 0 && json.Unmarshal(config, &envelope) == nil &&
		strings.TrimSpace(envelope.AgentStartsIn) == ProjectAgentStartsInParent {
		return ProjectAgentStartsInParent
	}
	return ProjectAgentStartsInRepository
}

// ProjectWorkingDirectory returns the project's own working directory for a
// bring-your-own Coder workspace (for example ~/work/{repo}), which overrides
// the organization connection's. Empty keeps the connection's.
func ProjectWorkingDirectory(config json.RawMessage) string {
	var envelope struct {
		WorkingDirectory string `json:"workingDirectory"`
	}
	if len(config) == 0 || json.Unmarshal(config, &envelope) != nil {
		return ""
	}
	return strings.TrimSpace(envelope.WorkingDirectory)
}

// ValidateProjectRules checks agentRules or orchestratorRules text.
func ValidateProjectRules(key, rules string) error {
	if len(rules) > MaxProjectRulesBytes {
		return fmt.Errorf("%s must be at most 16 KB", key)
	}
	if strings.ContainsRune(rules, 0) || !utf8.ValidString(rules) {
		return fmt.Errorf("%s contains invalid characters", key)
	}
	return nil
}
