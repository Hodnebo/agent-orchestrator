package worker

import (
	"path/filepath"

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
