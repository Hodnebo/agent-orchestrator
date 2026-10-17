package litmus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestDriveTimedWaitHasOperationAndOverheadBudget(t *testing.T) {
	for _, tc := range []struct {
		ms   int64
		args []string
	}{
		{28000, []string{"28000"}},
		{35000, []string{"35000"}},
		{35000, []string{"+35000", "--user-agent", "--load"}},
		{35000, []string{"--text", "Ready", "--timeout", "35000", "--timeout", "1"}},
		{MaxDriveWait.Milliseconds(), []string{strconv.FormatInt(MaxDriveWait.Milliseconds(), 10)}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, f, _, _ := driveSvc(t)
				run, _ := svc.store.Get("run_s1")
				e := svc.engineFor(run.ID, run.Legs[0])
				requested := time.Duration(tc.ms) * time.Millisecond
				var actionCtx context.Context
				e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
					if err := ctx.Err(); err != nil {
						return "", err
					}
					if slices.Equal(args[2:], []string{"stream", "disable"}) {
						<-time.After(10 * time.Second) // fake setup time must not consume the wait's budget
					}
					if args[2] == "wait" && args[3] != "--load" {
						actionCtx = ctx
						deadline, ok := ctx.Deadline()
						if !ok || time.Until(deadline) != requested+cmdTimeout {
							t.Fatalf("wait budget = %s, want %s plus %s overhead", time.Until(deadline), requested, cmdTimeout)
						}
						select {
						case <-time.After(requested):
						case <-ctx.Done():
							return "", ctx.Err()
						}
					}
					return f.exec(ctx, bin, args, env)
				}
				res, err := svc.Drive(context.Background(), "s1", "wait", tc.args)
				if err != nil {
					t.Fatal(err)
				}
				rec := res["receipt"].(actionRec)
				if actionCtx == nil || actionCtx.Err() == nil || rec.Reason != "" || len(rec.Warnings) != 0 || rec.After == "" {
					t.Fatalf("wait context %v, receipt %+v", actionCtx, rec)
				}
			})
		})
	}
}

func TestDriveWaitOverMaximumRefusedBeforeRunning(t *testing.T) {
	for _, args := range [][]string{
		{strconv.FormatInt(MaxDriveWait.Milliseconds()+1, 10)},
		{"--json", strconv.FormatInt(MaxDriveWait.Milliseconds()+1, 10)},
		{"18446744073709551616"}, // also reject overflow before converting to time.Duration
		{"--text", "Ready", "--timeout", strconv.FormatInt(MaxDriveWait.Milliseconds()+1, 10)},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			svc, f, r, _ := driveSvc(t)
			_, err := svc.Drive(context.Background(), "s1", "wait", args)
			if err == nil || !strings.Contains(err.Error(), "maximum") || !strings.Contains(err.Error(), strconv.FormatInt(MaxDriveWait.Milliseconds(), 10)) {
				t.Fatalf("over-limit wait error = %v", err)
			}
			if len(f.calls) != 0 || len(r.calls) != 0 {
				t.Fatalf("refused wait ran commands: engine %v, runner %v", f.calls, r.calls)
			}
		})
	}
}

func TestEngineSetupDeadlineIsNotCached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeEngine{}
		failSetup := true
		runner := func(ctx context.Context, bin string, args, env []string) (string, error) {
			if failSetup && slices.Equal(args[2:], []string{"stream", "disable"}) {
				failSetup = false
				<-ctx.Done()
				return "", ctx.Err()
			}
			return f.exec(ctx, bin, args, env)
		}
		e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Namespace: "s", Session: "s", AppOrigin: "http://app.test"}, runner)
		e.abort = func() error { return nil }
		if _, err := e.Run(context.Background(), time.Minute, "snapshot"); !errors.Is(err, context.DeadlineExceeded) || isLegDown(err) {
			t.Fatalf("setup timeout = %v", err)
		}
		if _, err := e.Run(context.Background(), time.Minute, "snapshot"); err != nil {
			t.Fatalf("following action: %v", err)
		}
	})
}

func TestDriveDeadlineDoesNotBlameAppOrBlockFollowingAction(t *testing.T) {
	for _, requestDeadline := range []time.Duration{0, time.Second} {
		t.Run(requestDeadline.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, f, _, _ := driveSvc(t)
				run, _ := svc.store.Get("run_s1")
				e := svc.engineFor(run.ID, run.Legs[0])
				busy, aborted, freshProbes := false, 0, 0
				var actionCtx context.Context
				e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
					if busy { // the persistent daemon retains its lock after the client is killed
						<-ctx.Done()
						return "", ctx.Err()
					}
					if args[2] == "hang" {
						busy, actionCtx = true, ctx
					}
					if actionCtx != nil && args[2] == "tab" {
						if ctx == actionCtx || ctx.Err() != nil {
							t.Fatalf("probe reused expired action context: %v", ctx.Err())
						}
						freshProbes++
					}
					return f.exec(ctx, bin, args, env)
				}
				e.abort = func() error { busy = false; aborted++; return nil }
				requestCtx := context.Background()
				if requestDeadline > 0 {
					var cancel context.CancelFunc
					requestCtx, cancel = context.WithTimeout(requestCtx, requestDeadline)
					defer cancel()
				}
				res, err := svc.Drive(requestCtx, "s1", "hang", nil)
				if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) || strings.Contains(err.Error(), errLegDown.Error()) {
					t.Fatalf("deadline error = %v", err)
				}
				rec := res["receipt"].(actionRec)
				if !strings.Contains(rec.Reason, context.DeadlineExceeded.Error()) || !strings.Contains(rec.Reason, "browser session was restarted") || strings.Contains(fmt.Sprint(rec), errLegDown.Error()) || len(rec.Warnings) > 0 || rec.After != "" {
					t.Fatalf("deadline receipt %+v", rec)
				}
				if aborted != 1 || freshProbes == 0 || busy {
					t.Fatalf("aborts %d, fresh probes %d, daemon busy %t", aborted, freshProbes, busy)
				}
				if requestCtx.Err() != nil {
					if _, err := svc.Drive(context.Background(), "s1", "snapshot", nil); err == nil || !strings.Contains(err.Error(), "browser session was restarted") {
						t.Fatalf("disconnected caller lost restart notice: %v", err)
					}
				}
				if _, err := svc.Drive(context.Background(), "s1", "snapshot", nil); err != nil {
					t.Fatalf("following action: %v", err)
				}
			})
		})
	}
}

func TestEngineProbeDeadlineIsNotLegDownOrCached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeEngine{}
		e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Namespace: "s", Session: "s", AppOrigin: "http://app.test"}, f.exec)
		aborted := 0
		e.abort = func() error { aborted++; return nil }
		if _, err := e.Run(context.Background(), time.Minute, "snapshot"); err != nil {
			t.Fatal(err)
		}
		failProbe := true
		e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
			if failProbe && slices.Equal(args[2:], []string{"tab", "list"}) {
				failProbe = false
				<-ctx.Done()
				return "", ctx.Err()
			}
			return f.exec(ctx, bin, args, env)
		}
		if _, err := e.Run(context.Background(), time.Minute, "snapshot"); !errors.Is(err, context.DeadlineExceeded) || isLegDown(err) {
			t.Fatalf("probe timeout = %v", err)
		}
		if _, err := e.Run(context.Background(), time.Minute, "snapshot"); err != nil {
			t.Fatalf("following action: %v", err)
		}
		if aborted != 0 {
			t.Fatalf("responsive daemon restarted %d times after a short probe timeout", aborted)
		}
	})
}

func TestEngineAbortFailureRemainsVisible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeEngine{}
		runner := func(ctx context.Context, bin string, args, env []string) (string, error) {
			if slices.Equal(args[2:], []string{"stream", "status"}) {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return f.exec(ctx, bin, args, env)
		}
		e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Namespace: "s", Session: "s", AppOrigin: "http://app.test"}, runner)
		e.abort = func() error { return errors.New("kill failed") }
		if _, err := e.Run(context.Background(), time.Second, "hang"); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "kill failed") || isLegDown(err) {
			t.Fatalf("abort failure = %v", err)
		}
		calls := len(f.calls)
		if _, err := e.Run(context.Background(), time.Second, "snapshot"); err == nil || !strings.Contains(err.Error(), "kill failed") || len(f.calls) != calls {
			t.Fatalf("failed abort allowed another command: %v, calls %v", err, f.calls)
		}
	})
}
