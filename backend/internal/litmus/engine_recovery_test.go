package litmus

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

func TestEngineTabLookupDistinguishesCommandFailureFromMissingApp(t *testing.T) {
	for _, missingApp := range []bool{false, true} {
		t.Run(map[bool]string{false: "command failed", true: "app missing"}[missingApp], func(t *testing.T) {
			f := &fakeEngine{fail: map[string]error{}, out: map[string]string{}}
			want := "agent-browser find the leg's app tab: page unavailable"
			if missingApp {
				f.out["tab"] = "→ [t1] Other - http://other.test/\n"
				want = "the leg's app target is unavailable: no app tab matches origin"
			} else {
				f.fail["tab"] = errors.New("page unavailable")
			}
			e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Namespace: "s", Session: "s", AppOrigin: "http://app.test"}, f.exec)
			if _, err := e.Run(context.Background(), time.Second, "snapshot"); err == nil || !strings.HasPrefix(err.Error(), want) || isLegDown(err) != missingApp {
				t.Fatalf("lookup cause misreported: %v, missing app=%t", err, missingApp)
			}
		})
	}
}

func TestEngineAbortRequiresOwnedDaemonPID(t *testing.T) {
	e, err := NewEngine("fake", t.TempDir(), "head", 9400, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(e.SocketDir); e.stop() })
	if err := e.abortDaemon(); err == nil || !strings.Contains(err.Error(), "PID file is missing") {
		t.Fatalf("unverified daemon stop reported as success: %v", err)
	}
}

func TestDriveDisconnectedWaitKeepsItsOperationAndDaemonState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, f, _, _ := driveSvc(t)
		run, _ := svc.store.Get("run_s1")
		e := svc.engineFor(run.ID, run.Legs[0])
		state := "dark with route and recording"
		e.abort = func() error { state = "lost"; return nil }
		e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
			if slices.Equal(args[2:], []string{"wait", "35000"}) {
				select {
				case <-time.After(35 * time.Second):
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return f.exec(ctx, bin, args, env)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := svc.Drive(ctx, "s1", "wait", []string{"35000"}); err != nil {
			t.Fatal(err)
		}
		if ctx.Err() == nil || state != "dark with route and recording" || hasEngineReviewCall(f.calls, "stream", "status") {
			t.Fatalf("request=%v state=%q calls=%v", ctx.Err(), state, f.calls)
		}
	})
}

func TestEngineRecoveryChecksResponsivenessBeforeRestart(t *testing.T) {
	for _, diagnosis := range []string{"responsive", "transport error", "wedged"} {
		t.Run(diagnosis, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &fakeEngine{}
				e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Namespace: "s", Session: "s", AppOrigin: "http://app.test"}, f.exec)
				aborts, diagnoses := 0, 0
				e.abort = func() error { aborts++; return nil }
				e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
					if slices.Equal(args[2:], []string{"stream", "status"}) {
						diagnoses++
						switch diagnosis {
						case "transport error":
							return "", errors.New("socket read failed")
						case "wedged":
							<-ctx.Done()
							return "", ctx.Err()
						}
					}
					return f.exec(ctx, bin, args, env)
				}
				_, err := e.Run(context.Background(), time.Second, "hang")
				if !errors.Is(err, context.DeadlineExceeded) || diagnoses != 1 {
					t.Fatalf("error=%v diagnoses=%d", err, diagnoses)
				}
				if diagnosis == "wedged" {
					if aborts != 1 || !strings.Contains(err.Error(), "browser session was restarted") {
						t.Fatalf("aborts=%d error=%v", aborts, err)
					}
				} else if aborts != 0 || strings.Contains(err.Error(), "was restarted") {
					t.Fatalf("healthy/unproven daemon restarted: aborts=%d error=%v", aborts, err)
				}
				if diagnosis == "transport error" && !strings.Contains(err.Error(), "socket read failed") {
					t.Fatalf("diagnostic failure hidden: %v", err)
				}
			})
		})
	}
}

func TestEngineIncompleteRestartIsReportedAndInitializedOnNextRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeEngine{}
		e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Namespace: "s", Session: "s", AppOrigin: "http://app.test"}, f.exec)
		aborts, setupAttempts := 0, 0
		e.abort = func() error { aborts++; return nil }
		e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
			if slices.Equal(args[2:], []string{"stream", "status"}) {
				<-ctx.Done()
				return "", ctx.Err()
			}
			if aborts > 0 && slices.Equal(args[2:], []string{"stream", "disable"}) {
				setupAttempts++
				if setupAttempts == 1 {
					<-ctx.Done() // recovery must neither recurse nor retry
					return "", ctx.Err()
				}
			}
			return f.exec(ctx, bin, args, env)
		}
		_, err := e.Run(context.Background(), time.Second, "hang")
		if err == nil || !strings.Contains(err.Error(), "restart failed") || strings.Contains(err.Error(), "was restarted") || !strings.Contains(err.Error(), "Lost daemon-held state") || aborts != 1 {
			t.Fatalf("error=%v aborts=%d", err, aborts)
		}
		if setupAttempts != 1 {
			t.Fatalf("recovery retried setup %d times", setupAttempts)
		}
		if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil || setupAttempts != 2 || !e.ready || e.tab != "t1" {
			t.Fatalf("explicit request could not initialize daemon: error=%v setup=%d ready=%t tab=%q", err, setupAttempts, e.ready, e.tab)
		}
	})
}

func TestEngineReattachTimeoutPreservesRecoveredBinding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fakeEngine{out: map[string]string{}}
		e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Namespace: "s", Session: "s", AppOrigin: "http://app.test"}, f.exec)
		if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil {
			t.Fatal(err)
		}
		f.out["tab"] = "→ [t2] App - http://app.test/\n"
		busy, aborted := false, false
		e.abort = func() error { busy, aborted = false, true; return nil }
		e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
			if !aborted && slices.Equal(args[2:], []string{"tab", "t2", "--pin-tab"}) {
				busy = true
			}
			if busy {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return f.exec(ctx, bin, args, env)
		}
		if _, err := e.Run(context.Background(), time.Second, "snapshot"); err == nil || !strings.Contains(err.Error(), "was restarted") || e.tab != "t2" || !e.ready {
			t.Fatalf("recovered binding overwritten: error=%v tab=%q ready=%t", err, e.tab, e.ready)
		}
	})
}

func TestDriveBatchStopsAfterRestartWithoutBail(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, f, _, _ := driveSvc(t)
		run, _ := svc.store.Get("run_s1")
		e := svc.engineFor(run.ID, run.Legs[0])
		e.last = "before.png"
		busy, aborted := false, false
		e.abort = func() error { busy, aborted = false, true; return nil }
		e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
			if !aborted && args[2] == "fill" {
				busy = true
			}
			if busy {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return f.exec(ctx, bin, args, env)
		}
		res, err := svc.Drive(context.Background(), "s1", "batch", []string{`fill #name value`, `press Enter`})
		if err == nil || !strings.Contains(err.Error(), "browser session was restarted") || hasEngineReviewCall(f.calls, "press", "Enter") || f.find("screenshot") != nil || len(res["results"].([]map[string]any)) != 1 {
			t.Fatalf("batch continued after state loss: error=%v calls=%v result=%v", err, f.calls, res)
		}
	})
}

func TestDriveBatchRestartNoticeSurvivesHTTPEnvelope(t *testing.T) {
	for _, phase := range []string{"after earlier child error", "batch receipt", "covered click snapshot"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, f, _, _ := driveSvc(t)
				run, _ := svc.store.Get("run_s1")
				e := svc.engineFor(run.ID, run.Legs[0])
				e.last = "before.png"
				busy, aborted := false, false
				clickSent := false
				e.abort = func() error { busy, aborted = false, true; return nil }
				e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
					cmd := args[2:]
					if !aborted {
						switch phase {
						case "after earlier child error":
							if cmd[0] == "eval" {
								return "", errors.New("earlier eval failed")
							}
							busy = busy || cmd[0] == "fill"
						case "batch receipt":
							busy = busy || cmd[0] == "wait"
						case "covered click snapshot":
							if cmd[0] == "click" {
								clickSent = true
								return "", errors.New("element is covered by another element")
							}
							busy = busy || clickSent && cmd[0] == "snapshot"
						}
					}
					if busy {
						<-ctx.Done()
						return "", ctx.Err()
					}
					return f.exec(ctx, bin, args, env)
				}
				children := []string{`eval 1`, `fill #name value`, `press Enter`}
				if phase == "batch receipt" {
					children = []string{`snapshot -i`}
				} else if phase == "covered click snapshot" {
					children = []string{`click #button`, `press Enter`}
				}
				_, err := svc.Drive(context.Background(), "s1", "batch", children)
				if err == nil {
					t.Fatal("batch lost restart failure")
				}
				response := httptest.NewRecorder()
				envelope.WriteError(response, httptest.NewRequest(http.MethodPost, "/testing/drive", nil), err)
				var body envelope.APIError
				if decodeErr := json.Unmarshal(response.Body.Bytes(), &body); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				if response.Code != http.StatusServiceUnavailable || !strings.Contains(body.Message, "browser session was restarted") || !strings.Contains(body.Message, "Lost daemon-held state") || hasEngineReviewCall(f.calls, "press", "Enter") {
					t.Fatalf("restart hidden or batch continued: HTTP=%d body=%s calls=%v", response.Code, response.Body, f.calls)
				}
				if phase == "after earlier child error" && !strings.Contains(body.Message, "earlier eval failed") {
					t.Fatalf("earlier error hidden: %s", response.Body)
				}
			})
		})
	}
}

func TestDriveRestartRestoresBindingAndReportsLostStateAtEveryPhase(t *testing.T) {
	for _, phase := range []string{"setup", "probe", "before-shot", "command", "load", "after-shot", "errors", "console", "snapshot"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				svc, f, _, runDir := driveSvc(t)
				run, _ := svc.store.Get("run_s1")
				e := svc.engineFor(run.ID, run.Legs[0])
				e.last, e.refs = "old.png", map[string]refInfo{"@e1": {name: "old ref"}}
				if phase == "before-shot" {
					e.last = ""
				}
				busy, fired, streamDisabled, pinned := false, false, false, false
				aborts, actions, shots := 0, 0, 0
				e.abort = func() error {
					busy, streamDisabled, pinned = false, false, false
					aborts++
					return nil
				}
				e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
					cmd := args[2:]
					if cmd[0] == "fill" {
						actions++
					}
					if cmd[0] == "screenshot" {
						shots++
					}
					match := phase == "setup" && slices.Equal(cmd, []string{"stream", "disable"}) ||
						phase == "probe" && slices.Equal(cmd, []string{"tab", "list"}) && pinned ||
						phase == "before-shot" && cmd[0] == "screenshot" ||
						phase == "command" && cmd[0] == "fill" ||
						phase == "load" && cmd[0] == "wait" ||
						phase == "after-shot" && cmd[0] == "screenshot" || cmd[0] == phase
					if !fired && match {
						fired, busy = true, true
					}
					if busy {
						<-ctx.Done()
						return "", ctx.Err()
					}
					if slices.Equal(cmd, []string{"stream", "disable"}) {
						streamDisabled = true
					}
					if slices.Equal(cmd, []string{"tab", "t1", "--pin-tab"}) {
						if !streamDisabled {
							t.Fatal("app bound before stream input was disabled")
						}
						pinned = true
					}
					return f.exec(ctx, bin, args, env)
				}
				res, err := svc.Drive(context.Background(), "s1", "fill", []string{"#name", "value"})
				if err == nil || !strings.Contains(err.Error(), "browser session was restarted") || aborts != 1 || !pinned || !streamDisabled || e.tab != "t1" || !e.ready || e.refs != nil || e.last != "" {
					t.Fatalf("phase=%s error=%v aborts=%d pinned=%t streamDisabled=%t tab=%q ready=%t refs=%v last=%q", phase, err, aborts, pinned, streamDisabled, e.tab, e.ready, e.refs, e.last)
				}
				if !strings.Contains(err.Error(), "No successful set, route, record, or trace setup remains") || strings.Contains(err.Error(), "credentials") {
					t.Fatalf("restart reported unrecorded tester setup: %v", err)
				}
				wantActions := 1
				if slices.Contains([]string{"setup", "probe", "before-shot"}, phase) {
					wantActions = 0
				}
				if actions != wantActions || shots > 1 {
					t.Fatalf("action replay/capture after restart: actions=%d shots=%d result=%v", actions, shots, res)
				}
				log, readErr := os.ReadFile(filepath.Join(runDir, "actions.jsonl"))
				if readErr != nil || !strings.Contains(string(log), `"action":"browser-restart"`) || !strings.Contains(string(log), "Lost daemon-held state") {
					t.Fatalf("restart not persisted: %s, %v", log, readErr)
				}
				if _, err := svc.Drive(context.Background(), "s1", "snapshot", nil); err != nil {
					t.Fatalf("following snapshot: %v", err)
				}
			})
		})
	}
}

func TestThemePairUsesOwnCDPSessionAfterDaemonRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, f, _, _ := driveSvc(t)
		run, _ := svc.store.Get("run_s1")
		e := svc.engineFor(run.ID, run.Legs[0])
		e.abort = func() error { return nil }
		e.exec = func(ctx context.Context, bin string, args, env []string) (string, error) {
			if slices.Equal(args[2:], []string{"stream", "status"}) {
				<-ctx.Done()
				return "", ctx.Err()
			}
			return f.exec(ctx, bin, args, env)
		}
		if _, err := e.Run(context.Background(), time.Second, "hang"); err == nil || !strings.Contains(err.Error(), "was restarted") {
			t.Fatalf("restart: %v", err)
		}
		page, _, active := fakeThemePageWithPreference(t, false, "")
		request := page.request
		page.request = func(ctx context.Context, method string, params, out any) error {
			if method == "Page.captureScreenshot" {
				im := image.NewNRGBA(image.Rect(0, 0, 2, 2))
				value := uint8(10)
				if active() == "light" {
					value = 240
				}
				im.SetNRGBA(0, 0, color.NRGBA{R: value, A: 255})
				data, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(pngBytes(im))})
				return json.Unmarshal(data, out)
			}
			return request(ctx, method, params, out)
		}
		e.connectPage = func(context.Context) (*pageCDP, error) { return page, nil }
		e.exec = func(context.Context, string, []string, []string) (string, error) {
			t.Fatal("theme capture depended on the restarted automation daemon")
			return "", nil
		}
		shots, note := svc.themePair(context.Background(), e, "c1")
		if len(shots) != 2 || note != "" {
			t.Fatalf("shots=%v note=%q", shots, note)
		}
		for _, shot := range shots {
			file, err := os.Open(shot.Path)
			if err != nil {
				t.Fatal(err)
			}
			im, err := png.Decode(file)
			_ = file.Close()
			if err != nil {
				t.Fatal(err)
			}
			r, _, _, _ := im.At(0, 0).RGBA()
			want := uint32(10 * 257)
			if shot.Theme == "light" {
				want = 240 * 257
			}
			if r != want {
				t.Errorf("%s shot pixel=%d want=%d", shot.Theme, r, want)
			}
		}
	})
}
