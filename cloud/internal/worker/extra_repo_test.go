package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedExtraRepoSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	gitRun(t, src, "init", "--initial-branch=main")
	gitRun(t, src, "config", "user.name", "AO Test")
	gitRun(t, src, "config", "user.email", "ao@example.test")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, src, "add", "README.md")
	gitRun(t, src, "commit", "-m", "init")
	return src
}

func TestPrepareExtraRepoChecksOutTheSessionBranchAndReviewBase(t *testing.T) {
	src := seedExtraRepoSource(t)
	dest := filepath.Join(t.TempDir(), "extra")
	repo := RepoRef{URL: "file://" + src}
	if err := PrepareExtraRepo(context.Background(), ExecGitRunner{}, dest, repo, "ao/abc123", "", t.TempDir()); err != nil {
		t.Fatalf("PrepareExtraRepo: %v", err)
	}
	if branch := gitOutput(t, dest, "rev-parse", "--abbrev-ref", "HEAD"); branch != "ao/abc123" {
		t.Fatalf("checked out %q, want the session branch", branch)
	}
	if base := gitOutput(t, dest, "rev-parse", WorkspaceReviewBaseRef); base != gitOutput(t, src, "rev-parse", "HEAD") {
		t.Fatalf("review base %q is not the cloned commit", base)
	}
}

// A worker restart finds the checkout already there: it fetches instead of
// failing to clone, and keeps the agent's commits on the session branch.
func TestPrepareExtraRepoReusesAnExistingCheckout(t *testing.T) {
	src := seedExtraRepoSource(t)
	dest := filepath.Join(t.TempDir(), "extra")
	repo := RepoRef{URL: "file://" + src}
	ctx := context.Background()
	if err := PrepareExtraRepo(ctx, ExecGitRunner{}, dest, repo, "ao/abc123", "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dest, "config", "user.name", "AO Test")
	gitRun(t, dest, "config", "user.email", "ao@example.test")
	if err := os.WriteFile(filepath.Join(dest, "work.txt"), []byte("agent work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dest, "add", "work.txt")
	gitRun(t, dest, "commit", "-m", "agent work")
	agentCommit := gitOutput(t, dest, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(src, "upstream.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, src, "add", "upstream.txt")
	gitRun(t, src, "commit", "-m", "upstream")

	if err := PrepareExtraRepo(ctx, ExecGitRunner{}, dest, repo, "ao/abc123", "", t.TempDir()); err != nil {
		t.Fatalf("second PrepareExtraRepo: %v", err)
	}
	if head := gitOutput(t, dest, "rev-parse", "HEAD"); head != agentCommit {
		t.Fatalf("restart moved the session branch to %s, want the agent's commit %s", head, agentCommit)
	}
	if fetched := gitOutput(t, dest, "rev-parse", "origin/main"); fetched != gitOutput(t, src, "rev-parse", "HEAD") {
		t.Fatalf("restart did not fetch: origin/main = %s", fetched)
	}
}

func TestPrepareExtraRepoLeavesOtherFoldersAlone(t *testing.T) {
	ctx := context.Background()
	t.Run("plain folder", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "extra")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, "notes.txt"), []byte("mine"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := PrepareExtraRepo(ctx, ExecGitRunner{}, dest, RepoRef{URL: "file://" + seedExtraRepoSource(t)}, "ao/x", "", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "not a Git checkout") {
			t.Fatalf("err = %v, want a not-a-checkout refusal", err)
		}
		if _, statErr := os.Stat(filepath.Join(dest, "notes.txt")); statErr != nil {
			t.Fatalf("the folder's file was touched: %v", statErr)
		}
	})
	t.Run("other repository", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "extra")
		if err := PrepareExtraRepo(ctx, ExecGitRunner{}, dest, RepoRef{URL: "file://" + seedExtraRepoSource(t)}, "ao/x", "", t.TempDir()); err != nil {
			t.Fatal(err)
		}
		err := PrepareExtraRepo(ctx, ExecGitRunner{}, dest, RepoRef{URL: "file://" + seedExtraRepoSource(t)}, "ao/x", "", t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "different repository") {
			t.Fatalf("err = %v, want a different-repository refusal", err)
		}
	})
}

func TestSessionRepoCheckoutsAndSelectors(t *testing.T) {
	workspace := "/home/dev/work/alice"
	checkouts := SessionRepoCheckouts(workspace, "https://github.com/acme/alice", []RepoRef{
		{URL: "https://github.com/acme/voice-calling-ai"}, {URL: ""}, {URL: "https://github.com/Acme/Julian.git", Branch: "dev"},
	})
	if len(checkouts) != 3 || !checkouts[0].Main || checkouts[0].FullName != "acme/alice" {
		t.Fatalf("checkouts = %+v", checkouts)
	}
	if checkouts[2].Path != "/home/dev/work/Julian" || checkouts[2].FullName != "acme/julian" || checkouts[2].Branch != "dev" {
		t.Fatalf("Julian checkout = %+v", checkouts[2])
	}
	for selector, want := range map[string]string{
		"": "alice", "acme/voice-calling-ai": "voice-calling-ai", "ACME/julian": "Julian",
		"julian": "Julian", "https://github.com/acme/alice.git": "alice",
	} {
		got, err := ResolveRepoCheckout(checkouts, selector)
		if err != nil || got.Name != want {
			t.Errorf("ResolveRepoCheckout(%q) = %+v, %v; want %s", selector, got, err, want)
		}
	}
	if _, err := ResolveRepoCheckout(checkouts, "acme/other"); err != ErrUnknownRepository {
		t.Fatalf("unknown selector err = %v", err)
	}
	standalone := SessionRepoCheckouts("/workspace/repository", "https://scratch.ao.local/p1", []RepoRef{{URL: "https://github.com/acme/x"}})
	if len(standalone) != 1 {
		t.Fatalf("standalone checkouts = %+v, want only the workspace", standalone)
	}
}
