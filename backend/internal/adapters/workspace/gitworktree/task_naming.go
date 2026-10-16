package gitworktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// NameTaskWorkspace finalizes names before setup or agent processes can use the
// checkout. The caller holds its project workspace gate. Git never force-renames
// branches or overwrites destinations; the reservation also coordinates daemons.
func (w *Workspace) NameTaskWorkspace(ctx context.Context, info ports.WorkspaceProjectInfo, branch, name string) (ports.WorkspaceProjectInfo, error) {
	if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || strings.HasPrefix(name, "-") || len(name) > 50 {
		return info, fmt.Errorf("gitworktree: invalid task name %q", name)
	}
	oldPath, err := w.validateManagedPath(info.Root.Path)
	if err != nil {
		return info, err
	}
	if err := validatePathComponent("project id", string(info.Root.ProjectID)); err != nil {
		return info, err
	}
	parent := filepath.Join(w.managedRoot, string(info.Root.ProjectID))
	if filepath.Dir(oldPath) != parent {
		return info, fmt.Errorf("gitworktree: task path is outside its project")
	}
	repos := append([]ports.WorkspaceRepoInfo(nil), info.Worktrees...)
	if len(repos) == 0 {
		repo := info.Root.RepoPath
		if repo == "" {
			repo, err = w.repoPath(info.Root.ProjectID)
			if err != nil {
				return info, err
			}
		}
		repos = []ports.WorkspaceRepoInfo{{RepoPath: repo, Path: oldPath, Branch: info.Root.Branch}}
	}
	for _, repo := range repos {
		inside, err := pathWithin(oldPath, repo.Path)
		if err != nil || !inside {
			return info, fmt.Errorf("gitworktree: task repository path is outside its workspace: %s", repo.Path)
		}
	}
	if err := w.validateBranch(ctx, repos[0].RepoPath, branch); err != nil {
		return info, err
	}
	namespace := strings.TrimSuffix(branch, "/root")
	for suffix := 1; suffix <= 100; suffix++ {
		candidate, candidateNamespace := name, namespace
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", name, suffix)
			candidateNamespace = fmt.Sprintf("%s-%d", namespace, suffix)
		}
		path := filepath.Join(parent, candidate)
		if _, err := os.Lstat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return info, err
		}
		result, retry, renameErr := w.nameReservedTask(ctx, info, repos, candidateNamespace, path)
		if retry {
			continue
		}
		return result, renameErr
	}
	return info, fmt.Errorf("gitworktree: no unused task name for %q", name)
}

func (w *Workspace) nameReservedTask(ctx context.Context, info ports.WorkspaceProjectInfo, repos []ports.WorkspaceRepoInfo, namespace, path string) (ports.WorkspaceProjectInfo, bool, error) {
	branch := namespace + "/root"
	for _, repo := range repos {
		relative, err := filepath.Rel(info.Root.Path, repo.Path)
		if err != nil {
			return info, false, err
		}
		records, err := w.listRecords(ctx, repo.RepoPath)
		if err != nil {
			return info, false, err
		}
		if _, exists := findWorktree(records, filepath.Join(path, relative)); exists {
			return info, true, nil
		}
		out, err := w.run(ctx, w.binary, "-C", repo.RepoPath, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
		if err != nil {
			return info, false, err
		}
		for _, ref := range strings.Fields(string(out)) {
			if remote, ok := strings.CutPrefix(ref, "refs/remotes/"); ok {
				_, ref, _ = strings.Cut(remote, "/")
			} else {
				ref = strings.TrimPrefix(ref, "refs/heads/")
			}
			if ref == namespace || strings.HasPrefix(ref, namespace+"/") || strings.HasPrefix(namespace, ref+"/") {
				return info, true, nil
			}
		}
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return info, errors.Is(err, os.ErrExist), err
	}
	defer func() { _ = os.Remove(path) }() // removes only our empty reservation on failure
	oldPath := info.Root.Path
	// Roll back only before a successful move. After the move, returned paths
	// must describe reality even if a child registration needs repair.
	renamed := 0
	rollback := func(cause error) (ports.WorkspaceProjectInfo, bool, error) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		for i := renamed - 1; i >= 0; i-- {
			if _, err := w.run(cleanupCtx, w.binary, "-C", repos[i].Path, "branch", "-m", repos[i].Branch); err != nil {
				cause = errors.Join(cause, err)
				if len(info.Worktrees) > i {
					info.Worktrees[i].Branch = branch
				}
				if i == 0 {
					info.Root.Branch = branch
				}
			}
		}
		return info, false, cause
	}
	for _, repo := range repos {
		renamed++ // include a command that succeeds immediately before cancellation
		if _, err := w.run(ctx, w.binary, "-C", repo.Path, "branch", "-m", branch); err != nil {
			return rollback(err)
		}
	}
	if err := moveTaskDirectory(oldPath, path); err != nil {
		return rollback(err)
	}
	info.Root.Path, info.Root.Branch = path, branch
	// Once moved, finish registration repair even if the request was cancelled.
	repairCtx, cancelRepair := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelRepair()
	_, repairErr := w.run(repairCtx, w.binary, "-C", repos[0].RepoPath, "worktree", "repair", path)
	for i := range info.Worktrees {
		wt := &info.Worktrees[i]
		relative, err := filepath.Rel(oldPath, wt.Path)
		if err != nil {
			return info, false, err
		}
		wt.Path, wt.Branch = filepath.Join(path, relative), branch
		if i > 0 {
			_, err := w.run(repairCtx, w.binary, "-C", wt.RepoPath, "worktree", "repair", wt.Path)
			repairErr = errors.Join(repairErr, err)
		}
	}
	return info, false, repairErr
}
