package litmus

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEngineCloseKeepsPIDWhenTeardownIsUnverified(t *testing.T) {
	for _, scenario := range []string{"ps before teardown", "ps before kill", "ps after kill", "kill failed", "reused PID", "PID reused before kill", "PID reused after kill", "unknown start", "kill removed PID"} {
		t.Run(scenario, func(t *testing.T) {
			socketDir := filepath.Join(t.TempDir(), "litmus-owned")
			pidPath := filepath.Join(socketDir, "namespaces", "head", "run", "head.pid")
			if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(pidPath, []byte("41\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(pidPath)
			if err != nil {
				t.Fatal(err)
			}
			started := info.ModTime().Add(-time.Hour)
			if scenario == "reused PID" {
				started = info.ModTime().Add(time.Hour)
			}
			probes, kills, closes := 0, 0, 0
			sys := sysOps{
				startTime: func(int) (time.Time, bool) { return started, scenario != "unknown start" },
				procs: func() (map[int]string, error) {
					probes++
					if scenario == "PID reused before kill" && probes == 2 || scenario == "PID reused after kill" && probes == 3 {
						started = started.Add(time.Minute)
					}
					if scenario == "ps before teardown" || scenario == "ps before kill" && probes == 2 || scenario == "ps after kill" && probes == 3 || scenario == "kill removed PID" && probes == 3 {
						return nil, errors.New("ps unavailable")
					}
					return map[int]string{41: "/fake/agent-browser"}, nil
				},
				kill: func(int) {
					kills++
					if scenario == "kill removed PID" {
						if err := os.RemoveAll(filepath.Dir(pidPath)); err != nil {
							t.Fatal(err)
						}
					}
				},
			}
			e := newEngine(EngineInfo{Binary: "/fake/agent-browser", SocketDir: socketDir, Session: "head", Namespace: "head"}, func(context.Context, string, []string, []string) (string, error) {
				closes++
				return "", nil
			})
			var logs bytes.Buffer
			logger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(logger)
			e.Close(sys)
			data, err := os.ReadFile(pidPath)
			if err != nil || string(data) != "41\n" {
				t.Fatalf("lost PID file: data=%q error=%v", data, err)
			}
			kept, err := os.Stat(pidPath)
			if err != nil || !kept.ModTime().Equal(info.ModTime()) {
				t.Fatalf("PID ownership timestamp changed: %v, %v", kept, err)
			}
			if !strings.Contains(logs.String(), "teardown unverified; keeping PID file") || !strings.Contains(logs.String(), pidPath) {
				t.Fatalf("teardown failure not logged: %s", &logs)
			}
			if closes != 0 {
				t.Fatalf("teardown started a daemon client: %d", closes)
			}
			if scenario == "ps before teardown" || scenario == "reused PID" || scenario == "PID reused before kill" || scenario == "unknown start" {
				if kills != 0 || closes != 0 {
					t.Fatalf("acted on an unverified daemon: closes=%d kills=%d", closes, kills)
				}
			}
			if strings.HasPrefix(scenario, "ps ") && !strings.Contains(logs.String(), "ps unavailable") {
				t.Fatalf("ps error hidden: %s", &logs)
			}
		})
	}
}

func TestEnginePostSendIdentityFailureExplainsUncertainEffect(t *testing.T) {
	for _, scenario := range []string{"replaced", "unverifiable", "recovery identity fails"} {
		t.Run(scenario, func(t *testing.T) {
			f := &fakeEngine{}
			e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "head", Namespace: "head", AppOrigin: "http://app.test"}, f.exec)
			identity, _ := fakeDaemonIdentity()
			var identityErr error
			e.identity = func() (daemonIdentity, error) { return identity, identityErr }
			if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil {
				t.Fatal(err)
			}
			f.hook = func(cmd []string) {
				if cmd[0] == "click" {
					if scenario == "unverifiable" {
						identityErr = errors.New("ps unavailable")
					} else {
						identity.pid++
					}
				}
				if e.recovering && scenario == "recovery identity fails" && cmd[0] == "stream" {
					identityErr = errors.New("recovery PID unreadable")
				}
			}
			sent := 0
			_, err := e.runBeforeSend(context.Background(), time.Second, func() { sent++ }, "click", "@e1")
			if sent != 1 || f.find("click") == nil || err == nil {
				t.Fatalf("sent=%d error=%v calls=%v", sent, err, f.calls)
			}
			for _, want := range []string{"WAS sent", "replacement daemon", "effect is uncertain", "Look at the screen before retrying"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("missing %q: %v", want, err)
				}
			}
			if strings.Contains(err.Error(), "NOT sent") {
				t.Fatalf("sent action marked unsent: %v", err)
			}
		})
	}
}

func TestEngineCloseKeepsReplacementPIDFile(t *testing.T) {
	for _, change := range []string{"contents", "new file", "mtime"} {
		t.Run(change, func(t *testing.T) {
			socketDir := filepath.Join(t.TempDir(), "litmus-owned")
			pidPath := filepath.Join(socketDir, "namespaces", "head", "run", "head.pid")
			if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(pidPath, []byte("41"), 0o600); err != nil {
				t.Fatal(err)
			}
			started := time.Now().Add(-time.Hour)
			live := true
			sys := sysOps{
				procs: func() (map[int]string, error) {
					if live {
						return map[int]string{41: "agent-browser"}, nil
					}
					return map[int]string{}, nil
				},
				startTime: func(int) (time.Time, bool) { return started, true },
				kill: func(int) {
					live = false
					var err error
					switch change {
					case "contents":
						err = os.WriteFile(pidPath, []byte("42"), 0o600)
					case "new file":
						err = os.WriteFile(filepath.Join(filepath.Dir(pidPath), "replacement.pid"), []byte("42"), 0o600)
					case "mtime":
						err = os.Chtimes(pidPath, time.Now(), time.Now().Add(time.Minute))
					}
					if err != nil {
						t.Fatal(err)
					}
				},
			}
			e := newEngine(EngineInfo{Binary: "agent-browser", SocketDir: socketDir, Namespace: "head", Session: "head"}, nil)
			e.Close(sys)
			if _, err := os.Stat(pidPath); err != nil {
				t.Fatalf("removed PID file after replacement: %v", err)
			}
		})
	}
}

func TestEngineCloseWaitsForClientPublishingPID(t *testing.T) {
	socketDir := filepath.Join(t.TempDir(), "litmus-owned")
	pidPath := filepath.Join(socketDir, "namespaces", "head", "run", "head.pid")
	started := time.Now().Add(-time.Hour)
	entered := make(chan struct{})
	runDone := make(chan error, 1)
	live := false
	e := newEngine(EngineInfo{Binary: "agent-browser", SocketDir: socketDir, Namespace: "head", Session: "head", RunDir: t.TempDir(), AppOrigin: "http://app.test"}, func(ctx context.Context, _ string, _ []string, _ []string) (string, error) {
		close(entered)
		<-ctx.Done()
		if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(pidPath, []byte("41"), 0o600); err != nil {
			return "", err
		}
		live = true
		return "", ctx.Err()
	})
	go func() { _, err := e.Run(context.Background(), time.Second, "snapshot"); runDone <- err }()
	<-entered
	sys := sysOps{
		procs: func() (map[int]string, error) {
			if live {
				return map[int]string{41: "agent-browser"}, nil
			}
			return map[int]string{}, nil
		},
		startTime: func(int) (time.Time, bool) { return started, true },
		kill:      func(int) { live = false },
	}
	e.Close(sys)
	if err := <-runDone; err == nil {
		t.Fatal("in-flight request completed without cancellation")
	}
	if live {
		t.Fatal("client published an orphan daemon during teardown")
	}
	if _, err := os.Stat(socketDir); !os.IsNotExist(err) {
		t.Fatalf("runtime left after verified client shutdown: %v", err)
	}
}

func TestEngineCloseKeepsInitializedRuntimeWithMissingPID(t *testing.T) {
	e := newEngine(EngineInfo{SocketDir: filepath.Join(t.TempDir(), "litmus-owned")}, nil)
	e.daemon = daemonIdentity{pid: 41, start: time.Now().Add(-time.Hour)}
	if err := os.MkdirAll(e.SocketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sys := sysOps{procs: func() (map[int]string, error) { return map[int]string{41: "agent-browser"}, nil }}
	e.Close(sys)
	if _, err := os.Stat(e.SocketDir); err != nil {
		t.Fatalf("removed initialized runtime without PID ownership proof: %v", err)
	}
}

func TestEngineCloseRemovesRuntimeOnlyAfterOwnedProcessExited(t *testing.T) {
	for _, scenario := range []string{"kill exits", "already exited"} {
		t.Run(scenario, func(t *testing.T) {
			socketDir := filepath.Join(t.TempDir(), "litmus-owned")
			pidPath := filepath.Join(socketDir, "namespaces", "head", "run", "head.pid")
			if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(pidPath, []byte("41"), 0o600); err != nil {
				t.Fatal(err)
			}
			started := time.Now().Add(-time.Hour)
			live := scenario != "already exited"
			kills, closes := 0, 0
			sys := sysOps{
				procs: func() (map[int]string, error) {
					if live {
						return map[int]string{41: "agent-browser"}, nil
					}
					return map[int]string{}, nil
				},
				startTime: func(int) (time.Time, bool) { return started, live },
				kill:      func(int) { kills++; live = false },
			}
			e := newEngine(EngineInfo{Binary: "agent-browser", SocketDir: socketDir, Namespace: "head", Session: "head"}, func(context.Context, string, []string, []string) (string, error) {
				closes++
				return "", nil
			})
			e.Close(sys)
			if _, err := os.Stat(socketDir); !os.IsNotExist(err) {
				t.Fatalf("runtime retained after verified exit: %v", err)
			}
			wantKills := 0
			if scenario == "kill exits" {
				wantKills = 1
			}
			wantCloses := 0
			if kills != wantKills || closes != wantCloses {
				t.Fatalf("kills=%d closes=%d, want %d/%d", kills, closes, wantKills, wantCloses)
			}
		})
	}
}

func TestEngineCallbacksAreStableDuringConcurrentLookup(t *testing.T) {
	svc, _, _, _ := driveSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			e.mu.Lock()
			_ = e.restartDaemon(errors.New("test restart"), "during test")
			e.onReattach("old", "new", "http://app.test")
			e.mu.Unlock()
		}
	}()
	for i := 0; i < 100; i++ {
		if got := svc.engineFor(run.ID, run.Legs[0]); got != e {
			t.Fatal("engine changed")
		}
	}
	wg.Wait()
}

func TestAbortAdviceNamesOnlyAvailableLegs(t *testing.T) {
	svc, _, _, _ := driveSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	e := svc.engineFor(run.ID, run.Legs[0])
	if got := e.abortAdvice(); !strings.Contains(got, "The head leg") || strings.Contains(got, "base") || strings.Contains(got, "other leg") {
		t.Fatalf("single-leg advice: %s", got)
	}
	run.Legs = append(run.Legs, Leg{Name: "base", State: "running", Engine: &EngineInfo{}})
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	if got := e.abortAdvice(); !strings.Contains(got, "The head leg") || !strings.Contains(got, "on the base leg") || strings.Contains(got, "other leg") {
		t.Fatalf("two-leg advice: %s", got)
	}
}

func TestLoggedRestartNoticeIncludesOtherLostState(t *testing.T) {
	got := loggedDaemonState(t.TempDir(), "head", 0, 1, nil)
	for _, want := range []string{"frame selection", "network HAR", "profiler", "dialog state", "Other daemon-held setup may also have been lost"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
}

func TestActionNotSentIsOnlyAddedOnce(t *testing.T) {
	cause := errors.New("daemon lost")
	err := actionNotSent(&engineInterruption{actionNotSent(cause)})
	if strings.Count(err.Error(), "NOT sent") != 1 || !errors.Is(err, cause) || !captureBlocked(err) {
		t.Fatalf("unsent error=%v", err)
	}
}
