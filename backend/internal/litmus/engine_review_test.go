package litmus

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type engineReviewHTTPTransport struct{ body string }

func (f engineReviewHTTPTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	status, body := http.StatusOK, f.body
	if req.URL.Path != "/json/list" {
		status, body = http.StatusNotFound, "not found"
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

func useEngineReviewTargets(t *testing.T, body string) {
	t.Helper()
	old := httpClient
	httpClient = &http.Client{Transport: engineReviewHTTPTransport{body: body}}
	t.Cleanup(func() { httpClient = old })
}

func TestEngineInitializesFromRecordedAppOrigin(t *testing.T) {
	f := &fakeEngine{out: map[string]string{
		"tab": "→ [t1] OAuth - https://accounts.example/login\n  [t2] App - http://app.test/#/home\n",
	}}
	e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s", AppOrigin: "http://app.test"}, f.exec)

	if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil {
		t.Fatal(err)
	}
	if !hasEngineReviewCall(f.calls, "tab", "t2", "--pin-tab") {
		t.Fatalf("did not pin t2: %v", f.calls)
	}
	if hasEngineReviewCall(f.calls, "tab", "t1", "--pin-tab") {
		t.Fatalf("pinned unrelated tab: %v", f.calls)
	}
	if e.AppOrigin != "http://app.test" {
		t.Fatalf("app origin changed to %q", e.AppOrigin)
	}
}

func TestEngineKeepsPinnedTabDuringOAuthNavigation(t *testing.T) {
	f := &fakeEngine{out: map[string]string{
		"tab": "→ [t1] App - http://app.test/\n  [t2] about:blank - about:blank\n",
	}}
	e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s", AppOrigin: "http://app.test"}, f.exec)
	if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	f.out["tab"] = "→ [t1] Sign in - https://accounts.example/authorize\n  [t2] about:blank - about:blank\n"

	if _, err := e.Run(context.Background(), time.Second, "click", "@e1"); err != nil {
		t.Fatal(err)
	}
	if e.tab != "t1" {
		t.Fatalf("pinned tab changed to %q", e.tab)
	}
	if got := f.find("tab"); !reflect.DeepEqual(got, []string{"tab", "list"}) {
		t.Fatalf("unexpected tab command %v, want a probe only", got)
	}
	if got := f.find("click"); !reflect.DeepEqual(got, []string{"click", "@e1"}) {
		t.Fatalf("OAuth click was not sent to pinned target: %v", got)
	}
}

func TestEngineReattachesOnlyWhenPinnedTabIsGoneOrBlank(t *testing.T) {
	for _, row := range []struct {
		name       string
		list       string
		wantTab    string
		wantDrift  bool
		wantDown   bool
		wantPinTab string
	}{
		{
			name:    "blank pinned tab",
			list:    "→ [t1] Blank - about:blank\n  [t2] App - http://app.test/\n",
			wantTab: "t2", wantDrift: true, wantPinTab: "t2",
		},
		{
			name:    "missing pinned tab",
			list:    "→ [t2] App - http://app.test/\n",
			wantTab: "t2", wantDrift: true, wantPinTab: "t2",
		},
		{
			name:     "missing app target",
			list:     "→ [t2] Other - https://other.test/\n",
			wantDown: true,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := &fakeEngine{out: map[string]string{
				"tab": "→ [t1] App - http://app.test/\n",
			}}
			e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s", AppOrigin: "http://app.test"}, f.exec)
			if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil {
				t.Fatal(err)
			}
			f.calls = nil
			f.out["tab"] = row.list
			_, err := e.Run(context.Background(), time.Second, "click", "@e1")
			if row.wantDrift && (err == nil || !strings.Contains(err.Error(), "re-selected")) {
				t.Fatalf("reattach err = %v, want stale-ref error", err)
			}
			if row.wantDown && !isLegDown(err) {
				t.Fatalf("missing app err = %v, want leg-down", err)
			}
			if row.wantTab != "" && e.tab != row.wantTab {
				t.Fatalf("pinned tab = %q, want %q", e.tab, row.wantTab)
			}
			if row.wantDown && e.tab != "" {
				t.Fatalf("missing app kept tab %q", e.tab)
			}
			if row.wantPinTab != "" {
				if !hasEngineReviewCall(f.calls, "tab", row.wantPinTab, "--pin-tab") {
					t.Fatalf("pin call for %s missing: %v", row.wantPinTab, f.calls)
				}
			} else if got := f.find("click"); got != nil {
				t.Fatalf("click reached a leg with no app target: %v", got)
			}
		})
	}
}

func TestEngineMatchesFileAppByPathPrefix(t *testing.T) {
	f := &fakeEngine{out: map[string]string{
		"tab": "→ [t1] Neighbor - file:///tmp/app-copy/index.html\n  [t2] App - file:///tmp/app/index.html#/home\n",
	}}
	e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s", AppOrigin: "file:///tmp/app/"}, f.exec)
	if _, err := e.Run(context.Background(), time.Second, "snapshot"); err != nil {
		t.Fatal(err)
	}
	if !hasEngineReviewCall(f.calls, "tab", "t2", "--pin-tab") {
		t.Fatalf("did not pin t2: %v", f.calls)
	}
	if !appURLMatches("file:///tmp/app/", "file:///tmp/app/nested/page.html") || appURLMatches("file:///tmp/app/", "file:///tmp/application/page.html") {
		t.Fatal("file URL matching did not respect a path-segment prefix")
	}
}

func TestAppOriginMatchesSiblingFilePage(t *testing.T) {
	origin := appOrigin("file:///a/index.html?theme=dark#/home")
	if origin != "file:///a/" || !appURLMatches(origin, "file:///a/settings.html") || appURLMatches(origin, "file:///app/settings.html") {
		t.Fatalf("file app origin %q did not match its directory", origin)
	}
}

func hasEngineReviewCall(calls [][]string, want ...string) bool {
	for _, call := range calls {
		if reflect.DeepEqual(call, want) {
			return true
		}
	}
	return false
}

func TestEngineLegacyBindingDoesNotGuessFirstPage(t *testing.T) {
	f := &fakeEngine{out: map[string]string{"tab": "→ [t1] App - http://app.test/\n"}}
	e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s"}, f.exec)
	if _, err := e.Run(context.Background(), time.Second, "snapshot"); !isLegDown(err) {
		t.Fatalf("legacy engine error = %v, want leg-down", err)
	}
	if f.find("snapshot") != nil || f.find("tab") != nil {
		t.Fatalf("legacy engine guessed a tab: %v", f.calls)
	}
}

func TestEngineHealthTimeoutAppliesOnlyToTabListProbe(t *testing.T) {
	oldHealth, oldCommand := healthProbeTimeout, cmdTimeout
	healthProbeTimeout, cmdTimeout = 40*time.Millisecond, time.Second
	t.Cleanup(func() { healthProbeTimeout, cmdTimeout = oldHealth, oldCommand })
	var remaining = map[string]time.Duration{}
	var listCalls int
	run := func(ctx context.Context, _ string, args, _ []string) (string, error) {
		command := strings.Join(args[2:], " ")
		if deadline, ok := ctx.Deadline(); ok {
			remaining[command] = time.Until(deadline)
		}
		switch {
		case command == "tab list":
			listCalls++
			return "→ [t1] App - http://app.test/\n", nil
		case command == "tab t1 --pin-tab":
			return "", nil
		case command == "snapshot":
			return "snapshot", nil
		case command == "stream disable":
			return "", nil
		default:
			return "", errors.New("unexpected command: " + command)
		}
	}
	e := newEngine(EngineInfo{Binary: "fake", RunDir: t.TempDir(), Session: "s", Namespace: "s", AppOrigin: "http://app.test"}, run)
	if _, err := e.Run(context.Background(), 800*time.Millisecond, "snapshot"); err != nil {
		t.Fatal(err)
	}
	if listCalls != 2 {
		t.Fatalf("tab-list probes = %d, want setup plus health probe", listCalls)
	}
	if remaining["stream disable"] <= healthProbeTimeout || remaining["tab t1 --pin-tab"] <= healthProbeTimeout || remaining["snapshot"] <= healthProbeTimeout {
		t.Fatalf("non-probe command got health timeout: deadlines %v", remaining)
	}
	if remaining["tab list"] > healthProbeTimeout {
		t.Fatalf("tab-list probe deadline %s exceeds health timeout %s", remaining["tab list"], healthProbeTimeout)
	}
}

func TestReadyCheckCapturesOnlyAnUnambiguousPageURL(t *testing.T) {
	body := `[{"type":"background_page","url":"chrome-extension://id/background.html"},{"type":"page","url":"file:///tmp/ao/app/index.html#/home"}]`
	useEngineReviewTargets(t, body)
	rc := readyCheck{debug: "http://engine-review.test", ready: Ready{PageTargets: 1}}
	if got, err := rc.appURL(context.Background()); err != nil || got != "file:///tmp/ao/app/index.html#/home" {
		t.Fatalf("ready app URL = %q, %v", got, err)
	}
	rc.ready.PageTargets = 2
	useEngineReviewTargets(t, `[{"type":"page","url":"file:///tmp/ao/app/index.html#/home"},{"type":"page","url":"about:blank"}]`)
	if got, err := rc.appURL(context.Background()); err == nil || got != "" || !strings.Contains(err.Error(), "unambiguously") {
		t.Fatalf("ambiguous ready URL = %q, %v", got, err)
	}
}

func TestAttachEnginePreservesReadinessAppOrigin(t *testing.T) {
	svc, _ := newSvc(t)
	svc.binary = "fake"
	svc.engExec = func(context.Context, string, []string, []string) (string, error) { return "", nil }
	leg, err := svc.attachEngine(context.Background(), "run_engine-origin", Leg{
		Name: "head", DebugPort: 9400, Engine: &EngineInfo{AppOrigin: "file:///tmp/ao/app/index.html"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.engines["run_engine-origin/head"].Close(sysOps{})
	if leg.Engine == nil || leg.Engine.AppOrigin != "file:///tmp/ao/app/index.html" {
		t.Fatalf("attached engine origin = %+v", leg.Engine)
	}
}

func TestCDPTargetKeepsPinnedOAuthTarget(t *testing.T) {
	useEngineReviewTargets(t, `[{"id":"target","type":"page","url":"https://accounts.example/authorize","webSocketDebuggerUrl":"ws://target"}]`)
	e := newEngine(EngineInfo{Endpoint: "http://engine-review.test", AppOrigin: "http://app.test"}, nil)
	got, err := e.targetURL(context.Background(), "target")
	if err != nil || got != "ws://target" {
		t.Fatalf("OAuth target URL = %q, %v", got, err)
	}
}
