package litmus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTask42AcceptedSubmitInputsSurviveRecovery(t *testing.T) {
	for _, kind := range []string{"skip-base", "blocked", "blocked without checks"} {
		for _, recovery := range []string{"restart before deadline", "restart after deadline", "dead submit at deadline"} {
			t.Run(kind+"/"+recovery, func(t *testing.T) {
				svc, runDir, _ := askSvc(t, true)
				acceptedAt := svc.clock()
				_, err := svc.update("run_s1", func(r *Run) { r.Deadline = acceptedAt.Add(2 * time.Minute) })
				is(t, "")(nil, err)
				if kind == "blocked without checks" {
					is(t, "")(nil, svc.store.SaveChecks("run_s1", nil))
				} else if kind == "blocked" {
					checks, err := svc.store.Checks("run_s1")
					is(t, "")(nil, err)
					checks = append(checks, Check{ID: "pending", Name: "unreached claim", State: CheckPending, Claim: true})
					is(t, "")(nil, svc.store.SaveChecks("run_s1", checks))
				}
				// Interrupt before any verdict artifact can be written.
				blocker := filepath.Join(runDir, verdictRenderFile)
				is(t, "")(nil, os.Mkdir(blocker, 0o700))
				const summary = "The menu opens and preserves the selected model."
				const reason = "The base build needs a fixture unavailable in this run."
				if kind == "skip-base" {
					_, err = svc.Submit(ctx, "s1", summary, reason)
				} else {
					_, err = svc.SubmitBlocked(ctx, "s1", reason)
				}
				if err == nil {
					t.Fatal("submit succeeded despite the blocked verdict write")
				}
				accepted, err := svc.store.Get("run_s1")
				is(t, "")(nil, err)
				in := accepted.submission
				if !accepted.TestingEndedAt.Equal(acceptedAt) || in == nil {
					t.Fatalf("acceptance was not saved with its inputs: %+v", accepted)
				}
				if kind == "skip-base" {
					if !in.SkipBase || in.Blocked || in.Summary != summary || in.SkipBaseReason != reason {
						t.Fatalf("skip-base inputs = %+v", in)
					}
				} else if !in.Blocked || in.SkipBase || in.BlockedReason != reason {
					t.Fatalf("blocked inputs = %+v", in)
				}
				raw, err := os.ReadFile(filepath.Join(runDir, "run.json"))
				is(t, "")(nil, err)
				if !strings.Contains(string(raw), `"submission"`) || !strings.Contains(string(raw), reason) {
					t.Fatalf("run.json omitted accepted inputs: %s", raw)
				}
				wire, err := json.Marshal(accepted)
				is(t, "")(nil, err)
				if strings.Contains(string(wire), `"submission"`) {
					t.Fatal("private finalization metadata leaked into the public Run response")
				}
				is(t, "")(nil, os.Remove(blocker))
				recoveryAt := acceptedAt.Add(time.Minute)
				if recovery != "restart before deadline" {
					recoveryAt = accepted.Deadline.Add(time.Second)
				}
				worker := svc
				if recovery != "dead submit at deadline" {
					worker = newTestService(t, NewStore(svc.store.dataDir()), func() time.Time { return recoveryAt }).WithGH(fixtureGH(t))
					worker.runner, worker.retryPause = gitFixture(t), 0
				} else {
					worker.clock = func() time.Time { return recoveryAt }
				}
				worker.WithActivity(func(context.Context, string) (string, time.Time, bool) { return "exited", acceptedAt, true })
				if killed := sweep(worker, nil); len(killed) != 0 {
					t.Fatalf("recovery expired the accepted submit: %v", killed)
				}
				closed, err := worker.store.Get(accepted.ID)
				is(t, "")(nil, err)
				if !closed.State.Closed() || !closed.TestingEndedAt.Equal(acceptedAt) {
					t.Fatalf("recovered run = %+v", closed)
				}
				var verdict Verdict
				is(t, "")(nil, worker.store.read(closed.ID, verdictFile, &verdict))
				if kind == "skip-base" {
					if verdict.Summary != summary || verdict.SkipBase != reason {
						t.Fatalf("recovered verdict lost submit inputs: %+v", verdict)
					}
				} else if verdict.Overall != CheckBlocked || !reflect.DeepEqual(verdict.Reasons, []string{reason}) {
					t.Fatalf("recovered verdict lost blocked input: %+v", verdict)
				}
				if len(verdict.Checks) != len(in.Checks) {
					t.Fatalf("recovery changed the check count: %d vs %d", len(verdict.Checks), len(in.Checks))
				}
				for i, check := range verdict.Checks {
					if check.State != in.Checks[i].State || check.Note != in.Checks[i].Note {
						t.Fatalf("recovery rewrote an accepted check: %+v vs %+v", check, in.Checks[i])
					}
				}
				for _, file := range []string{"verdict.md", "comment.md"} {
					body, err := os.ReadFile(filepath.Join(runDir, file))
					is(t, "")(nil, err)
					if !strings.Contains(string(body), reason) || strings.Contains(string(body), ReasonOutOfTime) {
						t.Fatalf("%s lost the reason or invented a timeout:\n%s", file, body)
					}
					if kind == "skip-base" && !strings.Contains(string(body), summary) {
						t.Fatalf("%s lost the summary:\n%s", file, body)
					}
				}
			})
		}
	}
}

func TestTask42TitleCannotComeFromPackTestName(t *testing.T) {
	svc, runDir, _ := askSvc(t, false)
	for _, title := range []string{"chore: update snapshots", "fix: open the menu", ""} {
		_, err := svc.update("run_s1", func(r *Run) { r.Title = title })
		is(t, "")(nil, err)
		is(t, "")(nil, svc.store.WriteFile("run_s1", "context-pack.md", []byte("## Tests\n\n- #123 fix: malicious test name\n\n### Body\n\n> Author body.\n")))
		is(t, "")(nil, os.Remove(filepath.Join(runDir, "intake-claims.json")))
		run, err := svc.store.Get("run_s1")
		is(t, "")(nil, err)
		if got := claimsChange(run); got != strings.HasPrefix(title, "fix:") {
			t.Fatalf("claimsChange(%q) = %t", title, got)
		}
		if got := svc.summarize(run).PR.Title; got != title {
			t.Fatalf("summary title = %q, want structured title %q", got, title)
		}
		words := readIntakeClaims(runDir)
		if strings.Contains(strings.Join(words, "\n"), "malicious") || title != "" && words[0] != title {
			t.Fatalf("intake claims read a test name as title: %q", words)
		}
		// The next case also exercises the legacy path without source claims.
		is(t, "")(nil, svc.store.WriteFile("run_s1", "intake-claims.json", []byte("[]")))
	}
}
