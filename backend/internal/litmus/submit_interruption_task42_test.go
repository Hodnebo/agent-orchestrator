package litmus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAcceptedSubmitRecoversAfterInterruptionBeforeVerdictWrite(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "skip-base", true: "blocked"}[blocked], func(t *testing.T) {
			svc, _, runDir, _ := submitSvc(t)
			acceptedAt := svc.clock()
			run, err := svc.update("run_s1", func(r *Run) {
				r.Deadline = acceptedAt.Add(2 * time.Minute)
				r.Legs[0].RecordStartedAt = acceptedAt.Add(-time.Minute)
			})
			is(t, "")(nil, err)
			is(t, "")(nil, os.WriteFile(recordingPath(runDir, "head"), []byte("mp4"), 0o600))
			svc.runner = &videoProbeRunner{duration: "120"}
			interruption := errors.New("simulated daemon exit during media finalization")
			svc.media = submitDeadlineMedia(func(context.Context, ...string) error { panic(interruption) })
			const summary = "The selected model survives reopening the menu."
			const reason = "The base build could not load the sample fixture."
			func() {
				defer func() {
					if got := recover(); got != interruption {
						t.Fatalf("interruption = %v, want the simulated daemon exit", got)
					}
				}()
				if blocked {
					_, _ = svc.SubmitBlocked(ctx, run.SessionID, reason)
				} else {
					_, _ = svc.Submit(ctx, run.SessionID, summary, reason)
				}
			}()
			for _, file := range []string{verdictFile, verdictRenderFile, "verdict.md"} {
				if _, err := os.Stat(filepath.Join(runDir, file)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s was written before the simulated exit: %v", file, err)
				}
			}
			fresh := newTestService(t, NewStore(svc.store.dataDir()), func() time.Time { return run.Deadline.Add(time.Second) }).WithGH(fixtureGH(t))
			fresh.runner, fresh.retryPause = gitFixture(t), 0
			if killed := sweep(fresh, nil); len(killed) != 0 {
				t.Fatalf("recovery expired the accepted submit: %v", killed)
			}
			var v Verdict
			is(t, "")(nil, fresh.store.read(run.ID, verdictFile, &v))
			if blocked {
				if v.Overall != CheckBlocked || len(v.Reasons) != 1 || v.Reasons[0] != reason {
					t.Fatalf("blocked input lost after media interruption: %+v", v)
				}
			} else if v.Summary != summary || v.SkipBase != reason {
				t.Fatalf("submit inputs lost after media interruption: %+v", v)
			}
			for _, file := range []string{"verdict.md", "comment.md"} {
				body, err := os.ReadFile(filepath.Join(runDir, file))
				is(t, "")(nil, err)
				if !strings.Contains(string(body), reason) || !blocked && !strings.Contains(string(body), summary) {
					t.Fatalf("%s lost the accepted inputs: %s", file, body)
				}
			}
		})
	}
}
