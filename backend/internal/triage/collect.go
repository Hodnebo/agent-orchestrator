package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/daemonmeta"
	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

const (
	collectTimeout  = 10 * time.Second
	requestTimeout  = 2 * time.Second
	collectMaxBytes = 64 << 10
)

type processIdentity struct {
	PID        int
	Start      string
	Name       string
	Executable string
}

type collectorDeps struct {
	now        func() time.Time
	paths      func() (config.Paths, error)
	executable func() (string, error)
	lookPath   func(string) (string, error)
	identity   func(context.Context, int) (processIdentity, error)
	machine    func(context.Context) (map[string]any, error)
	client     func(int) *http.Client
}

// Collect gathers a small read-only snapshot without opening the normal AO
// database, invoking a subprocess, or starting or repairing the daemon. Missing
// facts are separate unavailable evidence, so a broken daemon remains useful.
func Collect(ctx context.Context, dataDir string, origin Origin) ([]Evidence, error) {
	return collect(ctx, dataDir, origin, collectorDeps{
		now: time.Now, paths: config.ResolvePaths, executable: os.Executable,
		lookPath: exec.LookPath, identity: readProcessIdentity,
		machine: readMachine, client: loopbackProbeClient,
	})
}

func collect(parent context.Context, dataDir string, origin Origin, deps collectorDeps) ([]Evidence, error) {
	ctx, cancel := context.WithTimeout(parent, collectTimeout)
	defer cancel()
	at := deps.now().UTC()
	var evidence []Evidence
	add := func(id, source, status, text string, values map[string]any) {
		evidence = append(evidence, Evidence{ID: id, Kind: id, Source: source, CollectedAt: at, Status: status, Text: text, Values: values})
	}
	add("collector", "triage host", "collected", "", buildFacts(at))
	executable, err := within(ctx, deps.executable)
	if err != nil {
		add("collector-executable", "os.Executable", "unavailable", err.Error(), nil)
	} else {
		add("collector-executable", "os.Executable", "collected", "", map[string]any{"path": executable})
	}
	add("origin", "initiating desktop action", "collected", "", map[string]any{
		"appVersion": origin.AppVersion, "buildMode": origin.BuildMode,
		"surface": origin.Surface, "sessionId": origin.SessionID,
		"daemonState": origin.DaemonState, "daemonCode": origin.DaemonCode,
	})
	if origin.DaemonOutput != "" {
		output, truncated := limitText(origin.DaemonOutput)
		status := "collected"
		if truncated {
			status = "truncated"
		}
		add("daemon-output", "output retained by initiating desktop action", status, output, nil)
	}
	if len(origin.Machine) != 0 {
		if len(origin.Machine) > collectMaxBytes {
			add("origin-machine", "initiating desktop action", "truncated", "Machine snapshot exceeded the 64 KiB limit and was omitted.", nil)
		} else {
			var value any
			if err := decodeJSON(origin.Machine, &value); err != nil {
				add("origin-machine", "initiating desktop action", "unavailable", err.Error(), nil)
			} else {
				add("origin-machine", "initiating desktop action", "collected", "Supplied snapshot; its measurement flags and sample time are retained.", map[string]any{"snapshot": value})
			}
		}
	}
	paths, pathErr := within(ctx, deps.paths)
	pathValues := map[string]any{"dataDir": paths.DataDir, "dataDirSource": paths.DataDirSource, "runFile": paths.RunFilePath, "runFileSource": paths.RunFileSource, "triageDataDir": dataDir}
	if pathErr != nil {
		add("paths", "configuration path resolvers", "unavailable", pathErr.Error(), pathValues)
	} else {
		add("paths", "configuration path resolvers", "collected", "Paths resolved without loading daemon configuration or reading SQLite.", pathValues)
	}
	aoPath, err := within(ctx, func() (string, error) { return deps.lookPath("ao") })
	if err != nil {
		add("path-ao", "PATH lookup without execution", "unavailable", err.Error(), nil)
	} else {
		add("path-ao", "PATH lookup without execution", "collected", "This may differ from the executable used by the desktop app.", map[string]any{"path": aoPath})
	}
	machine, err := within(ctx, func() (map[string]any, error) { return deps.machine(ctx) })
	if err != nil {
		add("machine", "independent operating-system snapshot", "unavailable", err.Error(), machine)
	} else {
		add("machine", "independent operating-system snapshot", "collected", "A single sample does not measure CPU utilization.", machine)
	}
	probeEvidence, daemonReady := collectDaemon(ctx, paths.RunFilePath, at, deps)
	evidence = append(evidence, probeEvidence...)
	if len(origin.Session) != 0 {
		var value any
		if len(origin.Session) > collectMaxBytes {
			add("session-origin", "initiating desktop's cached session", "truncated", "Cached session details exceeded the 64 KiB limit and were omitted.", nil)
		} else if err := decodeJSON(origin.Session, &value); err != nil {
			add("session-origin", "initiating desktop's cached session", "unavailable", err.Error(), nil)
		} else {
			add("session-origin", "initiating desktop's cached session", "collected", "Previously displayed session details, not a fresh daemon read. They may be stale.", map[string]any{"snapshot": value})
		}
	} else if origin.SessionID != "" {
		text := "Session records were not read by the independent collector."
		if !daemonReady {
			text = "Session records are unavailable because daemon readiness was not verified."
		}
		add("session-origin", "initiating session context", "unavailable", text, map[string]any{"sessionId": origin.SessionID})
	}
	if ctx.Err() != nil {
		add("snapshot-limit", "triage host", "unavailable", ctx.Err().Error(), nil)
	}
	return RedactEvidence(evidence), parent.Err()
}

func buildFacts(at time.Time) map[string]any {
	facts := map[string]any{"os": runtime.GOOS, "arch": runtime.GOARCH, "goVersion": runtime.Version(), "collectedAt": at.Format(time.RFC3339Nano)}
	if info, ok := debug.ReadBuildInfo(); ok {
		facts["moduleVersion"] = info.Main.Version
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				facts["vcsRevision"] = setting.Value
			case "vcs.time":
				facts["vcsTime"] = setting.Value
			case "vcs.modified":
				facts["vcsModified"] = setting.Value == "true"
			}
		}
	}
	return facts
}

func unknownMachine() map[string]any {
	return map[string]any{"cpuCount": runtime.NumCPU(), "cpuMeasured": false, "cpuPercent": nil, "memoryTotalBytes": nil, "memoryAvailableBytes": nil, "load1": nil}
}

func collectDaemon(ctx context.Context, path string, at time.Time, deps collectorDeps) ([]Evidence, bool) {
	item := func(id, status, text string, values map[string]any) Evidence {
		return Evidence{ID: id, Kind: id, Source: "existing daemon run-file and fixed loopback probes", CollectedAt: at, Status: status, Text: text, Values: values}
	}
	unavailable := func(reason string) ([]Evidence, bool) {
		return []Evidence{item("daemon-healthz", "unavailable", reason, nil), item("daemon-readyz", "unavailable", reason, nil)}, false
	}
	if path == "" {
		return unavailable("Run-file path is unavailable.")
	}
	raw, err := within(ctx, func() ([]byte, error) { return readBoundedFile(path) })
	if err != nil {
		status := "unavailable"
		if errors.Is(err, errSnapshotTooLarge) {
			status = "truncated"
		}
		out, ready := unavailable(err.Error())
		return append([]Evidence{item("run-file", status, err.Error(), nil)}, out...), ready
	}
	var info runfile.Info
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&info)
	if err == nil {
		err = requireEOF(decoder)
	}
	if err == nil && (info.PID <= 0 || int64(info.PID) >= 1<<31 || info.Port < 1 || info.Port > 65535 || info.StartedAt.IsZero() || info.StartedAt.After(at.Add(time.Minute))) {
		err = errors.New("invalid run-file PID, port, or start time")
	}
	if err != nil {
		out, ready := unavailable("Run-file could not be validated: " + err.Error())
		return append([]Evidence{item("run-file", "unavailable", err.Error(), nil)}, out...), ready
	}
	out := []Evidence{item("run-file", "collected", "Validated bounded run-file snapshot; its target is frozen for this collection.", map[string]any{"pid": info.PID, "port": info.Port, "startedAt": info.StartedAt.Format(time.RFC3339Nano), "owner": info.Owner})}
	identity, err := within(ctx, func() (processIdentity, error) { return deps.identity(ctx, info.PID) })
	if err == nil {
		err = validateProcess(identity, info.PID)
	}
	if err != nil {
		missing, ready := unavailable("Daemon process identity is unavailable: " + err.Error())
		return append(out, missing...), ready
	}
	client := deps.client(info.Port)
	client.Timeout = requestTimeout
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	ready := false
	healthOK := false
	for _, endpoint := range []string{"healthz", "readyz"} {
		before, identityErr := within(ctx, func() (processIdentity, error) { return deps.identity(ctx, info.PID) })
		if identityErr == nil && before != identity {
			identityErr = errors.New("daemon process identity changed")
		}
		if identityErr != nil {
			out = append(out, item("daemon-"+endpoint, "unavailable", identityErr.Error(), nil))
			continue
		}
		values, probeErr := readDaemonProbe(ctx, client, info.Port, endpoint, identity)
		status := "collected"
		text := ""
		if probeErr != nil {
			status = "unavailable"
			if errors.Is(probeErr, errSnapshotTooLarge) {
				status = "truncated"
			}
			text = probeErr.Error()
			values = nil
		}
		after, afterErr := within(ctx, func() (processIdentity, error) { return deps.identity(ctx, info.PID) })
		if afterErr != nil || after != identity {
			status, text, values = "unavailable", "Daemon process identity changed or became unavailable during the probe.", nil
		}
		out = append(out, item("daemon-"+endpoint, status, text, values))
		if endpoint == "healthz" && status == "collected" && values["status"] == "ok" {
			healthOK = true
		}
		if endpoint == "readyz" && healthOK && status == "collected" && values["status"] == "ready" {
			ready = true
		}
	}
	finalRaw, finalErr := within(ctx, func() ([]byte, error) { return readBoundedFile(path) })
	finalIdentity, finalIdentityErr := within(ctx, func() (processIdentity, error) { return deps.identity(ctx, info.PID) })
	if finalErr != nil || !bytes.Equal(raw, finalRaw) || finalIdentityErr != nil || finalIdentity != identity {
		for i := range out {
			if strings.HasPrefix(out[i].ID, "daemon-") {
				out[i].Status, out[i].Text, out[i].Values = "unavailable", "Run-file or daemon process identity changed or became unavailable during collection.", nil
			}
		}
		ready = false
	}
	return out, ready
}

func validateProcess(identity processIdentity, pid int) error {
	name := strings.ToLower(identity.Name)
	if identity.PID != pid || identity.Start == "" || (name != "ao" && name != "ao.exe") {
		return errors.New("recorded PID is not a verified AO process")
	}
	return nil
}

func loopbackProbeClient(port int) *http.Client {
	address := net.JoinHostPort(config.LoopbackHost, strconv.Itoa(port))
	dialer := &net.Dialer{Timeout: requestTimeout}
	transport := &http.Transport{
		Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			if network != "tcp" || target != address {
				return nil, errors.New("probe target is not the frozen loopback address")
			}
			return dialer.DialContext(ctx, "tcp4", address)
		},
	}
	return &http.Client{Transport: transport, Timeout: requestTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

func readDaemonProbe(ctx context.Context, client *http.Client, port int, endpoint string, identity processIdentity) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if endpoint != "healthz" && endpoint != "readyz" {
		return nil, errors.New("unsupported probe endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(config.LoopbackHost, strconv.Itoa(port))+"/"+endpoint, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d", endpoint, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, collectMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > collectMaxBytes {
		return nil, errSnapshotTooLarge
	}
	var value map[string]any
	if err := decodeJSON(raw, &value); err != nil {
		return nil, err
	}
	pid, ok := value["pid"].(json.Number)
	if value["service"] != daemonmeta.ServiceName || !ok || string(pid) != strconv.Itoa(identity.PID) {
		return nil, errors.New("probe service or PID did not match the verified daemon")
	}
	status, ok := value["status"].(string)
	if !ok || status == "" {
		return nil, errors.New("probe status was missing")
	}
	if identity.Executable != "" {
		if executable, ok := value["executablePath"].(string); ok && filepath.Clean(executable) != filepath.Clean(identity.Executable) {
			return nil, errors.New("probe executable did not match the verified process")
		}
	}
	return value, nil
}

var errSnapshotTooLarge = errors.New("snapshot exceeded the 64 KiB limit")

func readBoundedFile(path string) ([]byte, error) {
	stat, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, errors.New("run-file is not a regular file")
	}
	if stat.Size() > collectMaxBytes {
		return nil, errSnapshotTooLarge
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(stat, opened) {
		return nil, errors.New("run-file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, collectMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > collectMaxBytes {
		return nil, errSnapshotTooLarge
	}
	return raw, nil
}

func decodeJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("snapshot contains trailing JSON")
	}
	return nil
}

func limitText(text string) (string, bool) {
	if len(text) <= collectMaxBytes {
		return text, false
	}
	text = text[:collectMaxBytes]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text, true
}

func within[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := fn()
		done <- result{value, err}
	}()
	select {
	case result := <-done:
		return result.value, result.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
