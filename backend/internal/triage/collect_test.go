package triage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/daemonmeta"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

const testDaemonPID = 12345

func testCollector(t *testing.T) (collectorDeps, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "running.json")
	return collectorDeps{
		now: func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) },
		paths: func() (config.Paths, error) {
			return config.Paths{DataDir: root, RunFilePath: path, DataDirSource: "test", RunFileSource: "test"}, nil
		},
		executable: func() (string, error) { return "/test/triage-host", nil },
		lookPath: func(name string) (string, error) {
			if name != "ao" {
				t.Errorf("unexpected PATH lookup: %s", name)
			}
			return "/test/path/ao", nil
		},
		identity: func(context.Context, int) (processIdentity, error) {
			return processIdentity{PID: testDaemonPID, Name: "ao", Start: "test-start"}, nil
		},
		machine: func(context.Context) (map[string]any, error) { return unknownMachine(), nil },
		client:  loopbackProbeClient,
	}, path
}

func writeCollectorRunFile(t *testing.T, path, serverURL string) {
	t.Helper()
	_, portString, err := net.SplitHostPort(strings.TrimPrefix(serverURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	if err := runfile.Write(path, runfile.Info{PID: testDaemonPID, Port: port, StartedAt: time.Date(2026, 10, 19, 11, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
}

func evidenceByID(t *testing.T, evidence []Evidence, id string) Evidence {
	t.Helper()
	for _, item := range evidence {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("missing evidence %s in %#v", id, evidence)
	return Evidence{}
}

func daemonProbeJSON(w http.ResponseWriter, path string) {
	status := "ok"
	if path == "/readyz" {
		status = "ready"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"service": daemonmeta.ServiceName, "pid": testDaemonPID, "status": status, "workingDirectory": "/Users/test-user/private", "apiKey": "test-response-secret"})
}

func TestCollectIndependentBoundedFactsAndRetainedOrigin(t *testing.T) {
	deps, path := testCollector(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Method != http.MethodGet || req.URL.Path != "/healthz" && req.URL.Path != "/readyz" {
			t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
		}
		daemonProbeJSON(w, req.URL.Path)
	}))
	defer server.Close()
	writeCollectorRunFile(t, path, server.URL)
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "ao.db"), []byte("not SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	origin := Origin{AppVersion: "desktop-version", BuildMode: "development", SessionID: "session-test", DaemonState: "starting", DaemonOutput: "failed token=output-secret at /Users/test-user/file", Machine: json.RawMessage(`{"system":{"cpuMeasured":false,"cpuPercent":0,"memoryTotalBytes":123},"apiKey":"origin-machine-secret"}`)}
	evidence, err := collect(context.Background(), filepath.Dir(path), origin, deps)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || evidenceByID(t, evidence, "daemon-healthz").Status != "collected" || evidenceByID(t, evidence, "daemon-readyz").Values["status"] != "ready" {
		t.Fatalf("unexpected probe results: calls=%d evidence=%#v", calls.Load(), evidence)
	}
	if evidenceByID(t, evidence, "origin").Values["appVersion"] != "desktop-version" || evidenceByID(t, evidence, "collector").Values["goVersion"] == "" {
		t.Fatal("desktop and collector build identities were not retained separately")
	}
	metrics := evidenceByID(t, evidence, "machine").Values
	if metrics["cpuMeasured"] != false || metrics["cpuPercent"] != nil || metrics["memoryTotalBytes"] != nil || metrics["load1"] != nil {
		t.Fatalf("unknown metrics became zeros: %#v", metrics)
	}
	snapshot := evidenceByID(t, evidence, "origin-machine").Values["snapshot"].(map[string]any)["system"].(map[string]any)
	if snapshot["cpuMeasured"] != false || snapshot["cpuPercent"] != json.Number("0") {
		t.Fatalf("origin measurement semantics changed: %#v", snapshot)
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"test-user", "output-secret", "origin-machine-secret", "test-response-secret"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("serialized evidence contains %q", secret)
		}
	}
}

func TestCollectKeepsCachedSessionFactsDuringDaemonOutage(t *testing.T) {
	deps, path := testCollector(t)
	deps.client = func(int) *http.Client { t.Fatal("missing daemon triggered a network request"); return nil }
	origin := Origin{SessionID: "affected", Session: json.RawMessage(`{"id":"affected","status":"working","cpuMeasured":false,"cpuPercent":0,"apiKey":"cached-secret"}`)}
	evidence, err := collect(context.Background(), filepath.Dir(path), origin, deps)
	if err != nil {
		t.Fatal(err)
	}
	item := evidenceByID(t, evidence, "session-origin")
	if item.Status != "collected" || !strings.Contains(item.Text, "not a fresh daemon read") {
		t.Fatalf("cached context lost its provenance: %#v", item)
	}
	snapshot := item.Values["snapshot"].(map[string]any)
	if snapshot["cpuMeasured"] != false || snapshot["cpuPercent"] != json.Number("0") {
		t.Fatal("cached measurement flags changed")
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "cached-secret") {
		t.Fatal("cached context leaked a credential")
	}
}

func TestCollectMissingCorruptAndHostileRunFilesNeverProbe(t *testing.T) {
	cases := map[string]string{
		"missing": "", "corrupt": "{", "external": `{"pid":12345,"port":80,"startedAt":"2026-10-19T11:00:00Z","url":"https://example.com"}`,
		"negative port":  `{"pid":12345,"port":-1,"startedAt":"2026-10-19T11:00:00Z"}`,
		"oversized port": `{"pid":12345,"port":65536,"startedAt":"2026-10-19T11:00:00Z"}`,
		"zero pid":       `{"pid":0,"port":3001,"startedAt":"2026-10-19T11:00:00Z"}`,
		"oversized pid":  `{"pid":2147483648,"port":3001,"startedAt":"2026-10-19T11:00:00Z"}`,
		"trailing JSON":  `{"pid":12345,"port":3001,"startedAt":"2026-10-19T11:00:00Z"} {}`,
		"oversized":      strings.Repeat("x", collectMaxBytes+1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			deps, path := testCollector(t)
			if raw != "" {
				if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			deps.identity = func(context.Context, int) (processIdentity, error) {
				t.Fatal("invalid run-file triggered identity lookup")
				return processIdentity{}, nil
			}
			deps.client = func(int) *http.Client { t.Fatal("invalid run-file triggered HTTP client"); return nil }
			evidence, err := collect(context.Background(), filepath.Dir(path), Origin{SessionID: "test-session"}, deps)
			if err != nil || evidenceByID(t, evidence, "daemon-healthz").Status != "unavailable" || evidenceByID(t, evidence, "session-origin").Status != "unavailable" {
				t.Fatalf("missing partial facts: err=%v evidence=%#v", err, evidence)
			}
			if name == "oversized" && evidenceByID(t, evidence, "run-file").Status != "truncated" {
				t.Fatal("oversized run-file was not marked truncated")
			}
		})
	}
}

func TestCollectRejectsSymlinkRunFile(t *testing.T) {
	deps, path := testCollector(t)
	target := filepath.Join(filepath.Dir(path), "target")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skip(err)
	}
	evidence, err := collect(context.Background(), filepath.Dir(path), Origin{}, deps)
	if err != nil || evidenceByID(t, evidence, "run-file").Status != "unavailable" {
		t.Fatalf("symlink accepted: %v %#v", err, evidence)
	}
}

func TestCollectRejectsUnverifiedAndChangedProcessIdentity(t *testing.T) {
	for _, name := range []string{"unrelated", "wrong pid", "missing start", "missing", "change before request", "change after request", "change at end"} {
		t.Run(name, func(t *testing.T) {
			deps, path := testCollector(t)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { requests.Add(1); daemonProbeJSON(w, req.URL.Path) }))
			defer server.Close()
			writeCollectorRunFile(t, path, server.URL)
			var identities atomic.Int32
			deps.identity = func(context.Context, int) (processIdentity, error) {
				n := identities.Add(1)
				identity := processIdentity{PID: testDaemonPID, Start: "same", Name: "ao"}
				switch name {
				case "unrelated":
					identity.Name = "other"
				case "wrong pid":
					identity.PID = 1
				case "missing start":
					identity.Start = ""
				case "missing":
					return processIdentity{}, errors.New("missing")
				case "change before request":
					if n >= 2 {
						identity.Start = "changed"
					}
				case "change after request":
					if n >= 3 {
						identity.Start = "changed"
					}
				case "change at end":
					if n >= 6 {
						identity.Start = "changed"
					}
				}
				return identity, nil
			}
			evidence, err := collect(context.Background(), filepath.Dir(path), Origin{}, deps)
			if err != nil || evidenceByID(t, evidence, "daemon-healthz").Status != "unavailable" || evidenceByID(t, evidence, "daemon-readyz").Status != "unavailable" {
				t.Fatalf("unsafe identity accepted: %v %#v", err, evidence)
			}
			if (name == "unrelated" || name == "wrong pid" || name == "missing start" || name == "missing" || name == "change before request") && requests.Load() != 0 {
				t.Fatalf("unverified identity received %d requests", requests.Load())
			}
		})
	}
}

func TestCollectProbeBoundary(t *testing.T) {
	for _, name := range []string{"redirect", "wrong service", "wrong pid", "oversized", "timeout", "run-file changed"} {
		t.Run(name, func(t *testing.T) {
			deps, path := testCollector(t)
			var redirected atomic.Int32
			external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { redirected.Add(1) }))
			defer external.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch name {
				case "redirect":
					http.Redirect(w, req, external.URL, http.StatusFound)
				case "wrong service":
					_, _ = fmt.Fprintf(w, `{"service":"other","pid":%d,"status":"ok"}`, testDaemonPID)
				case "wrong pid":
					_, _ = fmt.Fprintf(w, `{"service":%q,"pid":1,"status":"ok"}`, daemonmeta.ServiceName)
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat("x", collectMaxBytes+1)))
				case "timeout":
					<-req.Context().Done()
				case "run-file changed":
					_ = os.WriteFile(path, []byte("{}"), 0o600)
					daemonProbeJSON(w, req.URL.Path)
				}
			}))
			defer server.Close()
			writeCollectorRunFile(t, path, server.URL)
			ctx := context.Background()
			if name == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 40*time.Millisecond)
				defer cancel()
			}
			started := time.Now()
			evidence, _ := collect(ctx, filepath.Dir(path), Origin{}, deps)
			want := "unavailable"
			if name == "oversized" {
				want = "truncated"
			}
			if got := evidenceByID(t, evidence, "daemon-healthz").Status; got != want {
				t.Fatalf("health status = %q, want %q", got, want)
			}
			if redirected.Load() != 0 {
				t.Fatal("followed an HTTP redirect")
			}
			if name == "timeout" && time.Since(started) > time.Second {
				t.Fatal("snapshot ignored context deadline")
			}
		})
	}
}

func TestCollectIgnoresProxyAndBoundsSuppliedOutput(t *testing.T) {
	deps, path := testCollector(t)
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { proxyCalls.Add(1) }))
	defer proxy.Close()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, proxy.URL)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { daemonProbeJSON(w, req.URL.Path) }))
	defer server.Close()
	writeCollectorRunFile(t, path, server.URL)
	evidence, err := collect(context.Background(), filepath.Dir(path), Origin{DaemonOutput: strings.Repeat("x", collectMaxBytes+10), Machine: json.RawMessage(strings.Repeat("x", collectMaxBytes+1))}, deps)
	if err != nil || proxyCalls.Load() != 0 || evidenceByID(t, evidence, "daemon-readyz").Status != "collected" {
		t.Fatalf("proxy boundary failed: %v calls=%d", err, proxyCalls.Load())
	}
	output := evidenceByID(t, evidence, "daemon-output")
	if output.Status != "truncated" || len(output.Text) != collectMaxBytes || evidenceByID(t, evidence, "origin-machine").Status != "truncated" {
		t.Fatal("supplied output was not bounded")
	}
}

func TestCollectSanitizesErrorsAndReturnsPartialFacts(t *testing.T) {
	deps, path := testCollector(t)
	deps.executable = func() (string, error) { return "", errors.New("open /Users/private-user/bin token=error-secret") }
	deps.machine = func(context.Context) (map[string]any, error) {
		return unknownMachine(), errors.New("machine unavailable")
	}
	deps.lookPath = func(string) (string, error) { return "", errors.New("PATH lookup failed") }
	evidence, err := collect(context.Background(), filepath.Dir(path), Origin{}, deps)
	if err != nil || evidenceByID(t, evidence, "collector").Status != "collected" || evidenceByID(t, evidence, "machine").Values["cpuPercent"] != nil {
		t.Fatalf("partial snapshot lost: %v %#v", err, evidence)
	}
	data, _ := json.Marshal(evidence)
	if strings.Contains(string(data), "private-user") || strings.Contains(string(data), "error-secret") {
		t.Fatalf("error was not sanitized: %s", data)
	}
}

func TestCollectHangingIndependentReaderHonorsDeadline(t *testing.T) {
	deps, path := testCollector(t)
	release := make(chan struct{})
	defer close(release)
	deps.executable = func() (string, error) { <-release; return "", nil }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	evidence, err := collect(ctx, filepath.Dir(path), Origin{}, deps)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second || evidenceByID(t, evidence, "snapshot-limit").Status != "unavailable" {
		t.Fatalf("snapshot did not stop: err=%v time=%v", err, time.Since(started))
	}
}

func TestProbeTransportRejectsUnfrozenTarget(t *testing.T) {
	client := loopbackProbeClient(1234)
	transport := client.Transport.(*http.Transport)
	if _, err := transport.DialContext(context.Background(), "tcp", "192.0.2.1:1234"); err == nil {
		t.Fatal("transport accepted an external destination")
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "127.0.0.1:4321"); err == nil {
		t.Fatal("transport accepted a different loopback port")
	}
}
