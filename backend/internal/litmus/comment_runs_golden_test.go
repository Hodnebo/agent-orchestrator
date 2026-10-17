package litmus

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type commentRunFixture struct {
	run      Run
	checks   []Check
	root     string
	evidence string
}

func loadCommentRunFixture(t *testing.T, name string) commentRunFixture {
	t.Helper()
	root := filepath.Join("testdata", "comment-runs", name)
	var fixture commentRunFixture
	readJSONFixture(t, filepath.Join(root, "run.json"), &fixture.run)
	readJSONFixture(t, filepath.Join(root, "checks.json"), &fixture.checks)

	fixture.root = root
	fixture.evidence = t.TempDir()
	if err := copyDirFiles(filepath.Join(root, "receipts"), fixture.evidence); err != nil {
		t.Fatal(err)
	}
	for i := range fixture.checks {
		for j := range fixture.checks[i].Receipts {
			rebaseReceipt(t, &fixture.checks[i].Receipts[j], fixture.evidence)
		}
	}
	return fixture
}

func readJSONFixture(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func rebaseReceipt(t *testing.T, receipt *Receipt, evidence string) {
	t.Helper()
	rebase := func(path string) string {
		if path == "" {
			return ""
		}
		return filepath.Join(evidence, filepath.Base(path))
	}
	receipt.BeforePath = rebase(receipt.BeforePath)
	receipt.AfterPath = rebase(receipt.AfterPath)
	for i := range receipt.ThemeShots {
		receipt.ThemeShots[i].Path = rebase(receipt.ThemeShots[i].Path)
	}
}

func copyDirFiles(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		in, err := os.Open(filepath.Join(src, entry.Name()))
		if err != nil {
			return err
		}
		out, err := os.Create(filepath.Join(dst, entry.Name()))
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeInErr := in.Close()
		closeOutErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeInErr != nil {
			return closeInErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
	}
	return nil
}

func draftCommentFixture(t *testing.T, fixture commentRunFixture) []commentDoc {
	t.Helper()
	now := fixture.run.CreatedAt.Add(30 * time.Minute)
	svc := newTestService(t, NewStore(t.TempDir()), func() time.Time { return now })
	if err := svc.store.Save(fixture.run); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SaveChecks(fixture.run.ID, fixture.checks); err != nil {
		t.Fatal(err)
	}
	for _, check := range fixture.checks {
		if check.onLeg() != "head" {
			continue
		}
		clip := checkClipPath(filepath.Join(svc.store.root, fixture.run.ID), check.ID)
		if err := os.MkdirAll(filepath.Dir(clip), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(clip, []byte("clip fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	overall, reasons := derive(fixture.checks)
	docs, err := svc.draftComments(fixture.run, Verdict{Overall: overall, Checks: fixture.checks, Reasons: reasons})
	if err != nil {
		t.Fatal(err)
	}
	return docs
}

func TestCommentRunGoldens(t *testing.T) {
	for _, name := range []string{"run243", "run244"} {
		t.Run(name, func(t *testing.T) {
			fixture := loadCommentRunFixture(t, name)
			docs := draftCommentFixture(t, fixture)
			if len(docs) != 1 {
				t.Fatalf("draft count = %d, want 1", len(docs))
			}
			goldenPath := filepath.Join(fixture.root, "golden.md")
			if os.Getenv("UPDATE_COMMENT_GOLDENS") == "1" {
				if err := os.WriteFile(goldenPath, []byte(docs[0].Body), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v; rerun with UPDATE_COMMENT_GOLDENS=1", goldenPath, err)
			}
			if got := docs[0].Body; got != string(want) {
				t.Fatalf("generated Markdown differs from %s:\n%s", goldenPath, goldenDiff(string(want), got))
			}
		})
	}
}

func TestCommentRun246BugIncludesOriginalProblemLine(t *testing.T) {
	fixture := loadCommentRunFixture(t, "run246")
	docs := draftCommentFixture(t, fixture)
	if len(docs) < 1 {
		t.Fatal("run246 produced no bug drafts")
	}
	const line = "Main reproduces the original problem in 3 claim checks that pass on the PR build."
	if got := originalProblemLine(fixture.run, fixture.checks); got != line {
		t.Fatalf("run246 original problem line = %q, want %q", got, line)
	}
	if !strings.Contains(docs[0].Body, line) {
		t.Fatalf("bug 1 omitted original problem line %q:\n%s", line, docs[0].Body)
	}
}

func goldenDiff(want, got string) string {
	return fmt.Sprintf("want:\n%s\ngot:\n%s", want, got)
}
