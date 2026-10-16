package workerexec

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// A session of a project with additional repositories has one checkout per
// repository, side by side (worker.SessionRepoCheckouts). The coding agent
// starts in the main repository's checkout, or, when the project chose
// domain.ProjectAgentStartsInParent, in the folder holding them all, and is
// given access to every checkout either way.

// agentDirectory is where the coding agent starts: the main checkout, or its
// parent folder for a project that starts agents there. A reviewer always
// reviews from the main checkout, and a standalone session has only its own
// folder.
func agentDirectory(launch worker.LaunchContext, workspace string) string {
	if launch.Kind == "reviewer" || worker.IsScratchRepositoryURL(launch.RepositoryURL) ||
		strings.TrimSpace(launch.AgentStartsIn) != domain.ProjectAgentStartsInParent {
		return workspace
	}
	return filepath.Dir(workspace)
}

// additionalDirectories are the checkouts outside the agent's starting folder
// that it must be allowed to read and edit: the additional repositories, when
// it starts in the main checkout. Starting in the parent folder already covers
// every checkout. Only checkouts present on disk are listed, so a repository
// that failed to clone does not break the agent's configuration.
func additionalDirectories(launch worker.LaunchContext, workspace string) []string {
	if launch.Kind == "reviewer" || agentDirectory(launch, workspace) != workspace {
		return nil
	}
	var dirs []string
	for _, checkout := range worker.SessionRepoCheckouts(workspace, launch.RepositoryURL, launch.ExtraRepos)[1:] {
		if info, err := os.Stat(checkout.Path); err == nil && info.IsDir() {
			dirs = append(dirs, checkout.Path)
		}
	}
	return dirs
}

// repositoriesPromptNote tells the agent which repositories the session has,
// where each is checked out, where it started, and how to open a pull
// request on each. Paths come from worker.SessionRepoCheckouts, the same
// paths the worker cloned to.
func repositoriesPromptNote(launch worker.LaunchContext, workspace string) string {
	checkouts := worker.SessionRepoCheckouts(workspace, launch.RepositoryURL, launch.ExtraRepos)
	started := agentDirectory(launch, workspace)
	if len(checkouts) < 2 && started == workspace {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Repositories\n\n")
	if started == workspace {
		fmt.Fprintf(&b, "Your working directory is the main repository's checkout, %s. The project's other repositories are checked out beside it, and you can read, edit and commit in them directly:\n\n", workspace)
	} else {
		fmt.Fprintf(&b, "Your working directory is %s, the folder that holds every repository of this project, each checked out in its own folder. Change into a repository's folder before running its git, build or test commands, and follow that repository's own instructions (AGENTS.md, CLAUDE.md):\n\n", started)
	}
	for _, checkout := range checkouts {
		role := ""
		if checkout.Main {
			role = " (main repository)"
		} else if checkout.Branch != "" {
			role = fmt.Sprintf(" (cloned at %s)", checkout.Branch)
		}
		relative := checkout.Path
		if rel, err := filepath.Rel(started, checkout.Path); err == nil {
			relative = rel
		}
		fmt.Fprintf(&b, "- %s%s: %s (relative to your working directory: %s)\n", checkout.Name, role, checkout.Path, relative)
	}
	if len(checkouts) > 1 {
		branch := strings.TrimSpace(launch.Branch)
		if branch != "" {
			fmt.Fprintf(&b, "\nEvery repository is checked out on this session's branch, %s. ", branch)
		} else {
			b.WriteString("\n")
		}
		b.WriteString("Commit in each repository you change and open one pull request per repository with AO_PULL_REQUEST_HELP, passing \"repository\" for any repository other than the main one; mention the related pull requests in each description. A repository missing from disk could not be cloned (for example it is outside this session's GitHub access); continue with the others.")
	}
	return b.String()
}

// claudeAdditionalDirectoriesKey records, in Claude's settings, which
// permissions.additionalDirectories entries AO added, so a later launch with a
// different set of repositories removes exactly those and keeps the rest.
const claudeAdditionalDirectoriesKey = "aoManagedAdditionalDirectories"

// applyClaudeAdditionalDirectories sets AO's entries in a Claude settings
// object's permissions.additionalDirectories to dirs.
func applyClaudeAdditionalDirectories(settings map[string]any, dirs []string) {
	permissions, _ := settings["permissions"].(map[string]any)
	if permissions == nil {
		if len(dirs) == 0 {
			delete(settings, claudeAdditionalDirectoriesKey)
			return
		}
		permissions = map[string]any{}
		settings["permissions"] = permissions
	}
	managed := map[string]bool{}
	if previous, ok := settings[claudeAdditionalDirectoriesKey].([]any); ok {
		for _, entry := range previous {
			if dir, ok := entry.(string); ok {
				managed[dir] = true
			}
		}
	}
	kept := []any{}
	seen := map[string]bool{}
	if existing, ok := permissions["additionalDirectories"].([]any); ok {
		for _, entry := range existing {
			dir, ok := entry.(string)
			if !ok || managed[dir] || seen[dir] {
				continue
			}
			seen[dir] = true
			kept = append(kept, dir)
		}
	}
	added := []any{}
	for _, dir := range dirs {
		if !seen[dir] {
			seen[dir] = true
			kept = append(kept, dir)
			added = append(added, dir)
		}
	}
	if len(kept) == 0 {
		delete(permissions, "additionalDirectories")
	} else {
		permissions["additionalDirectories"] = kept
	}
	if len(added) == 0 {
		delete(settings, claudeAdditionalDirectoriesKey)
	} else {
		settings[claudeAdditionalDirectoriesKey] = added
	}
}

// codexWritableRootsArgs lets a Codex terminal write the additional checkouts
// in its workspace-write sandbox. It uses a -c override rather than --add-dir
// because -c is accepted in the same position by a fresh launch and a resume.
func codexWritableRootsArgs(dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		quoted = append(quoted, strconv.Quote(dir))
	}
	return []string{"-c", "sandbox_workspace_write.writable_roots=[" + strings.Join(quoted, ",") + "]"}
}
