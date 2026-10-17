package litmus

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionartifacts"
)

const pendingHomeReportSuffix = "Owner home changes unverified: teardown has not completed.\n"
const incompleteHomeReportSuffix = "Owner home changes unverified: teardown snapshot is incomplete.\n"
const homeChangesCaveat = "Any process on this Mac can cause these; Litmus does not attribute them."

const legHomeForksFile = "home-forks.txt"

var legacyVerdictRenderWarnings sync.Map

// HomeChanges reads the local report. It is never part of a public comment.
func (s *Store) HomeChanges(id string) (string, error) {
	path, err := s.path(id, homeChangesFile)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if _, snapshotErr := os.Stat(filepath.Join(filepath.Dir(path), homeSnapshotFile)); snapshotErr == nil {
			return homeChangesReport(pendingHomeReportSuffix), nil
		} else if !os.IsNotExist(snapshotErr) {
			return "", snapshotErr
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return homeChangesReport(string(data)), nil
}

func homeChangesReport(body string) string {
	if strings.HasPrefix(body, homeChangesCaveat+"\n") {
		return body
	}
	return homeChangesCaveat + "\n" + body
}

// A pending report has not had its teardown snapshot yet. Only a timed-out
// teardown scan carries the retry suffix. Permanent unreadable entries remain
// in the report as final evidence.
func homeReportNeedsRetry(body string) bool {
	return strings.HasSuffix(body, pendingHomeReportSuffix) || strings.HasSuffix(body, incompleteHomeReportSuffix)
}

// Called after intake claims its in-flight marker, outside the service mutex.
func (s *Service) snapshotIntakeHome(id string) error {
	path := filepath.Join(s.store.root, id, homeSnapshotFile)
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	before, err := s.snapshotHome()
	if err != nil {
		return fmt.Errorf("litmus: snapshot owner home: %w", err)
	}
	if err := s.store.put(id, homeSnapshotFile, before); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o400); err != nil {
		return err
	}
	if before.Incomplete {
		return s.store.WriteFile(id, homeChangesFile, []byte(homeChangesReport(diffHomeSnapshots(before, before)+"\n"+pendingHomeReportSuffix)))
	}
	return nil
}

// Stale cleanup has no live Service owner setting. The intake snapshot retains it.
func (s *Service) legHomeForksBeforeWipe(id, leg string) (string, error) {
	path := filepath.Join(s.store.legDir(id, leg), legHomeForksFile)
	previous, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	home := s.ownerHome
	if home == "" {
		var before homeSnapshot
		if err := s.store.read(id, homeSnapshotFile, &before); os.IsNotExist(err) {
			return "", nil // Older runs have no owner-home baseline.
		} else if err != nil {
			return "", err
		}
		for _, root := range before.Roots {
			if root.Required {
				home = root.Path
				break
			}
		}
	}
	report, err := legHomeForks(home, filepath.Join(s.store.root, id), leg)
	if err != nil {
		return "", err
	}
	lines := strings.FieldsFunc(string(previous)+"\n"+report, func(r rune) bool { return r == '\n' })
	slices.Sort(lines)
	report = strings.Join(slices.Compact(lines), "\n")
	// Keep evidence across a cleanup failure or daemon restart after HOME is gone.
	if report != "" && report != string(previous) {
		if err := writeAtomic(path, []byte(report)); err != nil {
			return "", err
		}
	}
	return report, nil
}

func (s *Service) finishHomeGuard(id string) error {
	if report, err := os.ReadFile(filepath.Join(s.store.root, id, homeChangesFile)); err == nil {
		if len(report) == 0 {
			return errors.New("litmus: owner home report is empty")
		}
		if !homeReportNeedsRetry(string(report)) {
			if strings.HasPrefix(string(report), homeChangesCaveat+"\n") {
				return nil
			}
			return s.store.WriteFile(id, homeChangesFile, []byte(homeChangesReport(string(report))))
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var before homeSnapshot
	err := s.store.read(id, homeSnapshotFile, &before)
	if os.IsNotExist(err) {
		// Runs from before homeguard cannot establish an owner-home baseline.
		return s.store.WriteFile(id, homeChangesFile, []byte(homeChangesReport("Owner home changes unverified: no intake snapshot.\n")))
	}
	if err != nil {
		return fmt.Errorf("litmus: read owner home snapshot: %w", err)
	}
	snapshot := s.snapshotHomeAfter
	if snapshot == nil {
		snapshot = scanHomeRoots
	}
	after, err := snapshot(before.Roots, before.RunsRoot)
	if err != nil {
		return fmt.Errorf("litmus: compare owner home snapshot: %w", err)
	}
	changes := diffHomeSnapshots(before, after)
	var capturedForks []string
	for _, leg := range []string{"head", "base"} {
		forks, err := s.legHomeForksBeforeWipe(id, leg)
		if err != nil {
			return fmt.Errorf("litmus: inspect leg home forks: %w", err)
		}
		capturedForks = append(capturedForks, forks)
	}
	changes = strings.Trim(strings.Join(append([]string{changes}, capturedForks...), "\n"), "\n")
	if changes == "" {
		changes = "No owner home changes detected.\n"
	}
	if after.TimedOut {
		changes = strings.TrimRight(changes, "\n") + "\n" + incompleteHomeReportSuffix
	}
	return s.store.WriteFile(id, homeChangesFile, []byte(homeChangesReport(changes)))
}

func homeChangesSection(runDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(runDir, homeChangesFile))
	if errors.Is(err, os.ErrNotExist) {
		if _, snapshotErr := os.Stat(filepath.Join(runDir, homeSnapshotFile)); snapshotErr == nil {
			return "## Owner home changes\n\n" + homeChangesCaveat + "\n\nUnverified: teardown has not completed.\n", nil
		} else if !os.IsNotExist(snapshotErr) {
			return "", snapshotErr
		}
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// Indentation keeps arbitrary filenames from becoming Markdown or links.
	body := strings.TrimPrefix(string(data), homeChangesCaveat+"\n")
	return "## Owner home changes\n\n" + homeChangesCaveat + "\n\n" + indentHomeChanges(body) + "\n", nil
}

func indentHomeChanges(text string) string {
	return "    " + strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n    ") + "\n"
}

// Cleanup can finish after a verdict was published. Regenerate the reports
// from their recorded inputs and the completed home report.
func (s *Service) refreshHomeChangesVerdict(id string) error {
	unlock := s.lockRun(id)
	defer unlock()
	return s.refreshHomeChangesVerdictLocked(id)
}

// Caller holds the run lock across the read, render and atomic writes.
func (s *Service) refreshHomeChangesVerdictLocked(id string) (refreshErr error) {
	defer func() {
		if refreshErr != nil {
			slog.Warn("litmus: could not refresh reports after teardown", "run", id, "error", refreshErr)
		}
	}()
	runDir := filepath.Join(s.store.root, id)
	run, err := s.store.Get(id)
	if err != nil {
		return err
	}
	var inputs verdictRenderInputs
	if err := s.store.read(id, verdictRenderFile, &inputs); err != nil {
		if os.IsNotExist(err) {
			legacyEvidence := false
			for _, name := range []string{verdictFile, "verdict.md"} {
				if info, evidenceErr := os.Stat(filepath.Join(runDir, name)); evidenceErr == nil {
					if info.Size() > 0 {
						legacyEvidence = true
						break
					}
				} else if !os.IsNotExist(evidenceErr) {
					return fmt.Errorf("check older verdict evidence: %w", evidenceErr)
				}
			}
			switch {
			case legacyEvidence:
				// verdict-render.json was added after older runs were written. Their
				// existing reports are still valid, but cannot be regenerated.
				key := filepath.Join(s.store.root, id)
				if _, loaded := legacyVerdictRenderWarnings.LoadOrStore(key, struct{}{}); !loaded {
					slog.Warn("litmus: leaving reports unchanged for older run without verdict render inputs", "run", id)
				}
				return nil
			case !legacyEvidence && run.State != RunSubmitted && run.State != RunAwaitingApproval:
				return nil // This run has not produced a verdict.
			}
		}
		return fmt.Errorf("read verdict render inputs: %w", err)
	}
	if inputs.Run.ID != id || inputs.At.IsZero() || inputs.Verdict.Overall == "" {
		return errors.New("litmus: verdict render inputs are incomplete")
	}
	report, err := os.ReadFile(filepath.Join(runDir, homeChangesFile))
	if err != nil {
		return fmt.Errorf("read owner home report: %w", err)
	}
	if len(report) == 0 {
		return errors.New("litmus: owner home report is empty")
	}
	md := inputs.render(runDir)
	if err := s.store.WriteFile(id, "verdict.md", []byte(md)); err != nil {
		return err
	}
	if run.SessionID != "" {
		path := filepath.Join(sessionartifacts.Dir(s.store.dataDir(), domain.SessionID(run.SessionID)), "verdict.md")
		if err := writeAtomic(path, []byte(md)); err != nil {
			return err
		}
	}
	var docs []commentDoc
	var media commentMedia
	if err := s.store.read(id, commentMediaFile, &media); err == nil {
		manifest, err := s.readCommentManifest(inputs.Run)
		if err != nil {
			return fmt.Errorf("read published comment inputs: %w", err)
		}
		if len(manifest) == 0 {
			return errors.New("litmus: published comment inputs are missing")
		}
		docs, err = s.commentDrafts(inputs.Run, inputs.Verdict, evidence{bugImages: media.BugImages, checkClips: media.CheckClips}, "")
		if err != nil {
			return fmt.Errorf("render published comment after teardown: %w", err)
		}
	} else if !os.IsNotExist(err) || run.CommentState == "posted" || run.CommentState == "updated" {
		return fmt.Errorf("read published comment media: %w", err)
	} else {
		docs, err = s.renderCommentDrafts(inputs.Run, inputs.Verdict, inputs.At)
		if err != nil {
			return fmt.Errorf("render comment after teardown: %w", err)
		}
	}
	if len(docs) == 0 {
		return errors.New("litmus: no comment rendered after teardown")
	}
	return s.store.WriteFile(id, "comment.md", []byte(docs[0].Body))
}
