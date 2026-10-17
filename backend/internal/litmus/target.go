package litmus

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	// A loaded Mac took 2m04s to answer /readyz (#5848, 2026-10-14).
	readyLimit  = 300 * time.Second
	seedTimeout = 60 * time.Second
	// DefaultLaunchBudget matches the default recipe's two seed steps and
	// Launch's extra seed timeout.
	DefaultLaunchBudget = readyLimit + 3*seedTimeout
	// RecipeValidateBudget caps validation that prepares and launches a commit.
	RecipeValidateBudget = 10 * time.Minute
	legStopBudget        = 10 * time.Second
	// PruneBudget is the existing shared-clone pruning and private-state removal allowance.
	PruneBudget = 2 * time.Minute
	// TeardownBudget covers two 10s death confirmations, shared-clone pruning,
	// private install removal and the independently capped 20s home snapshot.
	TeardownBudget = 2*legStopBudget + PruneBudget + homeSnapshotTimeout
)

func launchBudget(seedSteps int) time.Duration {
	return readyLimit + time.Duration(seedSteps+1)*seedTimeout
}

// Tests shorten these.
var (
	pollEvery = 500 * time.Millisecond
	portWait  = legStopBudget
)

// Proc is a process and when it started, so a recycled pid is never mistaken for it.
type Proc struct {
	PID   int       `json:"pid"`
	Start time.Time `json:"start,omitzero"`
}

// Owned is owned.json in a leg dir: everything needed to find and kill the leg's processes later.
type Owned struct {
	RunID      string `json:"runId"`
	Leg        string `json:"leg"`
	PGID       Proc   `json:"pgid"` // the npm process, leader of the target's process group
	Electron   Proc   `json:"electron,omitzero"`
	Daemon     Proc   `json:"daemon,omitzero"` // runs in its own group, so the pgid kill misses it
	DaemonPort int    `json:"daemonPort"`
	DebugPort  int    `json:"debugPort"`
	State      string `json:"state"` // running | cleaned
}

func readOwned(dir string) (o Owned, err error) {
	data, err := os.ReadFile(filepath.Join(dir, "owned.json"))
	if err == nil {
		err = json.Unmarshal(data, &o)
	}
	return o, err
}

func writeOwned(dir string, o Owned) error {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "owned.json"), data)
}

// sysOps is everything that touches live processes, so tests can fake it.
type sysOps struct {
	startTime func(pid int) (time.Time, bool) // false when no such process
	procs     func() (map[int]string, error)  // pid -> full command line
	groupPIDs func(pgid int) ([]int, error)
	killGroup func(pgid int)
	kill      func(pid int)
	rmBrowser func(pid int) // the target's browser-runtime socket dirs
	portBusy  func(port int) (bool, error)
	freePorts func(n int) ([]int, error)
}

func realSys() sysOps {
	return sysOps{
		startTime: func(pid int) (time.Time, bool) {
			out, err := process.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
			t, perr := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(strings.Fields(string(out)), " "), time.Local)
			return t, err == nil && perr == nil
		},
		procs: func() (map[int]string, error) {
			out, err := process.Command("ps", "-axww", "-o", "pid=,command=").Output()
			if err != nil {
				return nil, err
			}
			m := map[int]string{}
			for _, line := range strings.Split(string(out), "\n") {
				pid, cmd, _ := strings.Cut(strings.TrimSpace(line), " ")
				if n, err := strconv.Atoi(pid); err == nil {
					m[n] = strings.TrimSpace(cmd)
				}
			}
			return m, nil
		},
		killGroup: sigkillGroup,
		freePorts: freePorts,
		groupPIDs: func(pgid int) ([]int, error) {
			out, err := process.Command("ps", "-ax", "-o", "pid=,pgid=").Output()
			if err != nil {
				return nil, err
			}
			var pids []int
			for _, line := range strings.Split(string(out), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[1] == strconv.Itoa(pgid) {
					pid, err := strconv.Atoi(fields[0])
					if err != nil {
						return nil, err
					}
					pids = append(pids, pid)
				}
			}
			return pids, nil
		},
		kill: sigkill,
		rmBrowser: func(pid int) {
			for _, base := range []string{"/tmp", os.TempDir()} {
				dirs, _ := filepath.Glob(filepath.Join(base, fmt.Sprintf("ao-brd-%d-*", pid)))
				for _, d := range dirs {
					_ = os.RemoveAll(d)
				}
			}
		},
		portBusy: func(port int) (bool, error) {
			c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
			if err == nil {
				_ = c.Close()
			}
			if err == nil {
				return true, nil
			}
			if errors.Is(err, syscall.ECONNREFUSED) {
				return false, nil
			}
			return false, err
		},
	}
}

// laps records how long each phase took, for the validate output and owned.json.
type laps struct {
	at  time.Time
	out []string
}

func newLaps() *laps { return &laps{at: time.Now()} }

func (l *laps) lap(name string) {
	now := time.Now()
	l.out = append(l.out, fmt.Sprintf("%s %.1fs", name, now.Sub(l.at).Seconds()))
	l.at = now
}

// freePorts returns n distinct free loopback TCP ports (all held open until every one is chosen).
func freePorts(n int) ([]int, error) {
	var ports []int
	held := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range held {
			_ = l.Close()
		}
	}()
	for range n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		held = append(held, l)
		addr, ok := l.Addr().(*net.TCPAddr)
		if !ok {
			return nil, fmt.Errorf("listener address is %T, not a TCP address", l.Addr())
		}
		ports = append(ports, addr.Port)
	}
	return ports, nil
}

// runNumber is the run's session number, which names the seeded project and chat: run_ao-9 is 9. A session id that
// does not end in digits is used whole.
func runNumber(runID string) string {
	id := strings.TrimPrefix(runID, "run_")
	if i := strings.LastIndexFunc(id, func(r rune) bool { return r < '0' || r > '9' }); i < len(id)-1 {
		return id[i+1:]
	}
	return id
}

func tmuxSocket(runID, leg string) string { return "litmus-" + runID + "-" + leg }

func hasTmuxSocket(argv, socket string) bool {
	fields := strings.Fields(argv)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "-L" && fields[i+1] == socket {
			return true
		}
	}
	return false
}

// legEnv is the target's whole environment. The isolation block comes last, so a
// recipe cannot point a target back at the real AO; inherited AO_* never gets through.
func legEnv(r Recipe, runID string, pr int, leg, legDir, root, nodeDir string, daemonPort, debugPort int) []string {
	env := baseEnv(nodeDir)
	for _, k := range slices.Sorted(maps.Keys(r.Env)) {
		env = append(env, k+"="+r.Env[k])
	}
	if pr > 0 { // the window label script names the window "Litmus: PR build #N" from this
		env = append(env, "LITMUS_PR="+strconv.Itoa(pr))
	}
	// The label script rides into the Electron main process on NODE_OPTIONS, after whatever the recipe set there.
	nodeOptions := "--require " + filepath.Join(legDir, windowLabelScriptName)
	if prior := envValue(env, "NODE_OPTIONS"); prior != "" {
		nodeOptions = prior + " " + nodeOptions
	}
	env = append(env, "NODE_OPTIONS="+nodeOptions, "LITMUS_ICON="+filepath.Join(legDir, windowLabelIconName), "LITMUS_ROOT="+root, "LITMUS_LEG="+leg, "LITMUS_RUN_ID="+runID, "LITMUS_RUN_NUM="+runNumber(runID), "LITMUS_DAEMON_PORT="+strconv.Itoa(daemonPort), "LITMUS_DEBUG_PORT="+strconv.Itoa(debugPort))
	if r.Isolation {
		env = append(env,
			"AO_DEV_ELECTRON_DIR="+filepath.Join(legDir, "electron"),
			"AO_DEV_API_TARGET=http://127.0.0.1:"+strconv.Itoa(daemonPort),
			"AO_PORT="+strconv.Itoa(daemonPort),
			"AO_RUN_FILE="+filepath.Join(legDir, "running.json"),
			"AO_DATA_DIR="+filepath.Join(legDir, "data"),
			"AO_TMUX_SOCKET_NAME="+tmuxSocket(runID, leg),
			"AO_APP_RUN_ID="+runID+"-"+leg,
			"AO_TELEMETRY_REMOTE=off")
	}
	return env
}

func envValue(env []string, key string) (v string) {
	for _, kv := range env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val // the last one wins, as in exec
		}
	}
	return v
}

// readyCheck is where to look for a launched target's signs of life.
type readyCheck struct {
	daemon, debug string // base URLs
	ready         Ready
}

var httpClient = &http.Client{Timeout: 2 * time.Second}

func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// check passes only when, in order: the daemon answers, the debug port answers, and
// /json/list holds exactly the expected number of page targets. It names the first stage that fails.
func (c readyCheck) check(ctx context.Context) error {
	if err := getJSON(ctx, c.daemon+c.ready.DaemonPath, nil); err != nil {
		return fmt.Errorf("daemon %s: %w", c.ready.DaemonPath, err)
	}
	if err := getJSON(ctx, c.debug+c.ready.DebugPath, nil); err != nil {
		return fmt.Errorf("debug port %s: %w", c.ready.DebugPath, err)
	}
	var targets []struct{ Type string }
	if err := getJSON(ctx, c.debug+"/json/list", &targets); err != nil {
		return fmt.Errorf("debug port /json/list: %w", err)
	}
	pages := 0
	for _, t := range targets {
		if t.Type == "page" {
			pages++
		}
	}
	if pages != c.ready.PageTargets {
		return fmt.Errorf("%d page targets in /json/list, want %d", pages, c.ready.PageTargets)
	}
	return nil
}

// appURL returns the only page target at readiness. Seed steps may open more
// pages, so this must run before the recipe is seeded and only works when the
// ready target count makes the app page unambiguous.
func (c readyCheck) appURL(ctx context.Context) (string, error) {
	var targets []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := getJSON(ctx, c.debug+"/json/list", &targets); err != nil {
		return "", fmt.Errorf("debug port /json/list: %w", err)
	}
	var pages []string
	for _, target := range targets {
		if target.Type == "page" {
			pages = append(pages, target.URL)
		}
	}
	if len(pages) != c.ready.PageTargets {
		return "", fmt.Errorf("%d page targets in /json/list, want %d", len(pages), c.ready.PageTargets)
	}
	if len(pages) != 1 {
		return "", fmt.Errorf("%d page targets are ready; cannot identify the app URL unambiguously", len(pages))
	}
	if strings.TrimSpace(pages[0]) == "" {
		return "", errors.New("the ready page target has no URL")
	}
	if isBlankURL(pages[0]) {
		return "", errors.New("the ready page target is still about:blank")
	}
	return pages[0], nil
}

// waitReady polls for readiness and a non-blank app URL until the limit runs out or the target exits.
func waitReady(ctx context.Context, c readyCheck, limit time.Duration, exited <-chan struct{}) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for {
		err := c.check(ctx)
		if err == nil {
			var appURL string
			appURL, err = c.appURL(ctx)
			if err == nil {
				return appURL, nil
			}
		}
		select {
		case <-exited:
			return "", fmt.Errorf("target exited before it was ready (last check: %w)", err)
		case <-ctx.Done():
			return "", fmt.Errorf("target not ready after %s: %w", limit, err)
		case <-time.After(pollEvery):
		}
	}
}

// occlusionFlags keep Chromium painting a window that is hidden or covered; without them CDP screenshots and
// the screencast never return for it (Playwright's Electron launcher sets the same two).
var occlusionFlags = []string{"--disable-backgrounding-occluded-windows", "--disable-renderer-backgrounding"}

// winOcclusion is the Windows-only third flag: Windows computes occlusion on its own.
const winOcclusion = "--disable-features=CalculateNativeWinOcclusion"

// runArgs expands the recipe's run args against env. For an Electron target the occlusion flags follow the
// --remote-debugging-port argument; one the recipe already lists is not repeated, and a recipe that sets
// --disable-features keeps it (on Windows that means no CalculateNativeWinOcclusion).
func runArgs(r Recipe, env []string, goos string) []string {
	args := make([]string, len(r.Run.Args))
	for i, a := range r.Run.Args {
		args[i] = os.Expand(a, func(k string) string { return envValue(env, k) })
	}
	i := slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, "--remote-debugging-port=") })
	if r.Kind != "electron" || i < 0 {
		return args
	}
	listed := func(flag string) bool {
		return slices.ContainsFunc(args, func(a string) bool { return a == flag || strings.HasPrefix(a, flag+"=") })
	}
	var add []string
	for _, f := range occlusionFlags {
		if !listed(f) {
			add = append(add, f)
		}
	}
	if goos == "windows" && !listed("--disable-features") {
		add = append(add, winOcclusion)
	}
	return slices.Insert(args, i+1, add...)
}

// startLeg runs the recipe's run command in its own process group, waits until the
// target is ready, then seeds it. Any failure kills the leg again before returning, except a failed seed step
// when seedGap is given: the leg then stays up and usable, and *seedGap says what is missing so the caller can
// record it as a gap.
func (s *Service) startLeg(ctx context.Context, runID string, pr int, r Recipe, leg Leg, w *laps, seedGap *string, keepState bool, reason string) (Leg, error) {
	m := readManifest(leg.Worktree)
	if m.SHA == "" {
		return leg, conflict("TESTING_NOT_PREPARED", "leg "+leg.Name+" has no build; run `ao testing prepare`")
	}
	dir := s.store.legDir(runID, leg.Name)
	ports, err := s.sys.freePorts(2)
	if err != nil {
		return leg, err
	}
	nodeDir := ""
	if m.NodePath != "" {
		nodeDir = filepath.Dir(m.NodePath)
	}
	env := legEnv(r, runID, pr, leg.Name, dir, leg.Worktree, nodeDir, ports[0], ports[1])
	env, err = prepareLegEnv(env, dir, s.ownerHome)
	if err != nil {
		return leg, fmt.Errorf("litmus: prepare %s shell setup: %w", leg.Name, err)
	}
	for _, sub := range []string{"electron", "data", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
			return leg, err
		}
	}
	if err := writeWindowLabel(dir); err != nil {
		return leg, err
	}
	logPath := filepath.Join(dir, "logs", "dev.log")
	logf, err := openLaunchLog(filepath.Dir(logPath), s.clock(), s.scrub(reason))
	if err != nil {
		return leg, err
	}
	args := append([]string{"-c", `exec "$0" "$@"`, r.Run.Cmd}, runArgs(r, env, runtime.GOOS)...) // sh finds npm on the env's PATH (the pinned node first), then becomes it
	cmd := process.Command("sh", args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = filepath.Join(leg.Worktree, r.Run.Dir), env, logf, logf
	ownGroup(cmd)
	err = cmd.Start()
	_ = logf.Close()
	if err != nil {
		return leg, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	start, _ := s.sys.startTime(cmd.Process.Pid)
	o := Owned{RunID: runID, Leg: leg.Name, PGID: Proc{cmd.Process.Pid, start}, DaemonPort: ports[0], DebugPort: ports[1], State: "running"}
	fail := func(err error) (Leg, error) {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TeardownBudget)
		defer cancel()
		var cleanupErr error
		if keepState {
			cleanupErr = killLeg(cleanupCtx, s.sys, dir)
		} else {
			cleanupErr = s.cleanLeg(cleanupCtx, runID, leg.Name)
		}
		if cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("cleanup target: %w", cleanupErr))
		}
		if !keepState { // relaunch keeps the private AO's registered worktrees
			if pruneErr := s.pruneClone(cleanupCtx, leg.Worktree); pruneErr != nil {
				err = errors.Join(err, fmt.Errorf("prune target worktree: %w", pruneErr))
			}
		}
		leg.State = "failed"
		return leg, fmt.Errorf("%s (logs in %s): %w", clip(err.Error(), 400), filepath.Dir(logPath), err)
	}
	if err := writeOwned(dir, o); err != nil {
		sigkillGroup(cmd.Process.Pid)
		return leg, err
	}

	rc := readyCheck{daemon: "http://127.0.0.1:" + strconv.Itoa(ports[0]), debug: "http://127.0.0.1:" + strconv.Itoa(ports[1]), ready: r.Ready}
	appURL, err := waitReady(ctx, rc, readyLimit, exited)
	if err != nil {
		return fail(err)
	}
	if origin := appOrigin(appURL); origin == "" {
		return fail(fmt.Errorf("identify app URL at readiness: unsupported URL %q", diagnosticURL(appURL)))
	} else {
		leg.Engine = &EngineInfo{AppOrigin: origin}
	}
	w.lap("ready")
	o.Electron, err = s.electronProc("--remote-debugging-port=" + strconv.Itoa(ports[1]))
	if err != nil {
		return fail(err)
	}
	var rf struct{ PID int }
	if data, err := os.ReadFile(filepath.Join(dir, "running.json")); err == nil && json.Unmarshal(data, &rf) == nil && rf.PID > 0 {
		o.Daemon.PID = rf.PID
		o.Daemon.Start, _ = s.sys.startTime(rf.PID)
	}
	if err := writeOwned(dir, o); err != nil {
		return fail(err)
	}
	if keepState {
		seedLog, err := os.OpenFile(filepath.Join(dir, "logs", "seed.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			_, err = fmt.Fprintln(seedLog, "relaunch: data dir kept, seed not re-run")
			err = errors.Join(err, seedLog.Close())
		}
		if err != nil {
			return fail(fmt.Errorf("log skipped seed: %w", err))
		}
	} else if err := s.seed(ctx, r.Seed, env, leg.Worktree, dir); err != nil {
		gap, ok := seedGapOf(ctx, exited, leg.Name, err)
		if seedGap == nil || !ok {
			return fail(err)
		}
		*seedGap = gap
	}
	w.lap("seed")
	leg.State, leg.PID, leg.PIDStart = "running", o.PGID.PID, start
	leg.DataDir, leg.RunFile = filepath.Join(dir, "data"), filepath.Join(dir, "running.json")
	leg.DaemonPort, leg.DebugPort = ports[0], ports[1]
	return leg, nil
}

// electronProc finds the Electron browser process of a leg: the one whose argv carries flag and no --type= (renderer,
// GPU and utility helpers inherit the debugging flag but are never the process to kill), lowest pid first. An
// Electron binary wins over the npm and node wrappers that were given the same flag.
func (s *Service) electronProc(flag string) (Proc, error) {
	procs, err := s.sys.procs()
	if err != nil {
		return Proc{}, fmt.Errorf("list processes for Electron: %w", err)
	}
	var best int
	for _, pid := range slices.Sorted(maps.Keys(procs)) {
		args := strings.Fields(procs[pid])
		if !slices.Contains(args, flag) || slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "--type=") }) {
			continue
		}
		if isElectron := strings.Contains(procs[pid], "Electron"); best == 0 || (isElectron && !strings.Contains(procs[best], "Electron")) {
			best = pid
		}
	}
	p := Proc{PID: best}
	if best > 0 {
		p.Start, _ = s.sys.startTime(best)
	}
	return p, nil
}

// seedError is a seed step that failed: which one, and what it said.
type seedError struct {
	step        int
	cmd, reason string
}

func (e *seedError) Error() string {
	return fmt.Sprintf("seed step %d (%s): %s", e.step, e.cmd, e.reason)
}

// seedGapOf turns a failed seed step into a gap only while the target is still running within its launch budget.
func seedGapOf(ctx context.Context, exited <-chan struct{}, leg string, err error) (string, bool) {
	if ctx.Err() != nil {
		return "", false
	}
	select {
	case <-exited:
		return "", false
	default:
	}
	var se *seedError
	if !errors.As(err, &se) {
		return "", false
	}
	build := map[string]string{"head": "PR", "base": "main"}[leg]
	return fmt.Sprintf("The %s build started without its sample content (seed step %d failed).", build, se.step), true
}

// seed runs the recipe's seed steps in order, each under its own timeout, waiting for its waitFor.
func (s *Service) seed(ctx context.Context, steps []SeedStep, env []string, root, legDir string) error {
	logf, err := os.OpenFile(filepath.Join(legDir, "logs", "seed.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logf.Close() }()
	for i, st := range steps {
		sctx, cancel := context.WithTimeout(ctx, seedTimeout)
		out, err := s.sh(sctx, root, env, st.Cmd)
		_, _ = fmt.Fprintf(logf, "$ %s\n%s\n", st.Cmd, out)
		detail := strings.TrimSpace(string(out)) // what the step itself said; its waitFor has nothing but its error
		if err == nil && st.WaitFor != "" {
			if err = poll(sctx, func() error { _, err := s.sh(sctx, root, env, st.WaitFor); return err }); err != nil {
				detail = ""
			}
		}
		cancel()
		if err != nil {
			if _, logErr := fmt.Fprintf(logf, "seed step %d failed: %v\n", i+1, err); logErr != nil {
				slog.Warn("litmus: could not write seed failure diagnostic", "step", i+1, "error", err, "logError", logErr)
			}
			return &seedError{step: i + 1, cmd: clip(st.Cmd, 80), reason: clip(cmp.Or(detail, err.Error()), 300)}
		}
	}
	return nil
}

// poll retries fn every pollEvery until it succeeds or ctx ends; it then returns fn's last error.
func poll(ctx context.Context, fn func() error) error {
	for {
		err := fn()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(pollEvery):
		}
	}
}

// Launch starts the base or head target privately and seeds it. Base and head may be launched at once.
func (s *Service) Launch(ctx context.Context, sessionID, leg string) (Run, error) {
	if leg != "base" && leg != "head" {
		return Run{}, invalid("TESTING_LEG_INVALID", "leg must be base or head")
	}
	run, done, err := s.begin(sessionID, "launch "+leg)
	if err != nil {
		return Run{}, err
	}
	defer done()
	i := slices.IndexFunc(run.Legs, func(l Leg) bool { return l.Name == leg })
	if i < 0 {
		return Run{}, conflict("TESTING_NOT_PREPARED", "run `ao testing prepare` before launching "+leg)
	}
	if run.Legs[i].State == "running" {
		return Run{}, conflict("TESTING_LEG_RUNNING", leg+" is already running; use: ao testing relaunch "+leg)
	}
	owned, ownershipErr := readOwned(s.store.legDir(run.ID, leg))
	if ownershipErr == nil && owned.State != "cleaned" {
		return Run{}, conflict("TESTING_LEG_RUNNING", leg+" still has uncleaned target ownership; use: ao testing relaunch "+leg)
	}
	if ownershipErr != nil && (!os.IsNotExist(ownershipErr) || run.Legs[i].PID > 0) {
		return Run{}, launchError(leg, fmt.Errorf("cannot verify prior leg ownership: %w", ownershipErr))
	}
	recipe, err := s.store.LoadRecipe(run.Repo)
	if err != nil {
		return Run{}, err
	}
	if err := s.resetTeardown(run.ID); err != nil {
		return Run{}, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), launchBudget(len(recipe.Seed)))
	defer cancel()
	var seedGap string
	started, lerr := s.startLeg(ctx, run.ID, run.PRNumber, recipe, run.Legs[i], newLaps(), &seedGap, false, "launch")
	if lerr == nil {
		started, lerr = s.attachEngine(ctx, run.ID, started)
		if lerr != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TeardownBudget)
			lerr = errors.Join(lerr, s.cleanLeg(cleanupCtx, run.ID, leg))
			cancel()
		}
	}
	if lerr == nil { // each leg records its own window, so a video never shows the other build
		started = s.startRecording(ctx, run.ID, started)
	}
	if lerr == nil && started.Control == nil {
		started.Control = &LegControl{State: "ok"}
	}
	seedGap = s.scrub(seedGap)
	out, err := s.update(run.ID, func(cur *Run) {
		if j := slices.IndexFunc(cur.Legs, func(l Leg) bool { return l.Name == leg }); j >= 0 {
			if cur.Legs[j].Control != nil {
				started.Control = cur.Legs[j].Control
			}
			cur.Legs[j] = started
		}
		if lerr == nil && !cur.State.Closed() {
			cur.State = RunRunning
		}
		if seedGap != "" && !slices.Contains(cur.Gaps, seedGap) {
			cur.Gaps = append(cur.Gaps, seedGap)
		}
	})
	if lerr != nil {
		return Run{}, launchError(leg, lerr)
	}
	return out, err
}

// launchError gives a failure that is not already an API error (a seed or readiness failure) a 409 that says why;
// a bare error would reach the tester as an opaque 500 and it could not report a reason.
func launchError(leg string, err error) error {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return err
	}
	return conflict("TESTING_LAUNCH_FAILED", "launch "+leg+" failed: "+err.Error())
}

// killLeg stops only this leg's owned processes. It leaves ownership and profile
// files intact until every owned PID has exited and both ports are free.
func killLeg(ctx context.Context, sys sysOps, dir string) error {
	o, err := readOwned(dir)
	if errors.Is(err, fs.ErrNotExist) || err == nil && o.State == "cleaned" {
		return nil
	}
	if err != nil {
		return err
	}
	procs, err := sys.procs()
	if err != nil {
		return fmt.Errorf("list processes for cleanup: %w", err)
	}
	for _, p := range []Proc{o.PGID, o.Daemon, o.Electron} {
		if p.PID <= 0 || !p.Start.IsZero() {
			continue
		}
		_, known := sys.startTime(p.PID)
		_, listed := procs[p.PID]
		if known || listed {
			return fmt.Errorf("cannot verify owned pid %d: start time is missing", p.PID)
		}
	}
	tracked := map[int]Proc{}
	track := func(pid int) {
		start, _ := sys.startTime(pid)
		tracked[pid] = Proc{PID: pid, Start: start}
	}
	if o.PGID.PID > 0 {
		leaderStart, known := sys.startTime(o.PGID.PID)
		_, listed := procs[o.PGID.PID]
		if !known && listed {
			return fmt.Errorf("cannot verify leg process group %d: leader start time is unknown", o.PGID.PID)
		}
		if !known || !o.PGID.Start.IsZero() && leaderStart.Equal(o.PGID.Start) {
			pids, err := sys.groupPIDs(o.PGID.PID)
			if err != nil {
				return fmt.Errorf("list leg process group: %w", err)
			}
			authorized := known
			for _, pid := range pids {
				if strings.Contains(procs[pid], dir+string(filepath.Separator)) {
					authorized = true
				}
				for _, owned := range []Proc{o.Electron, o.Daemon} {
					t, ok := sys.startTime(pid)
					if owned.PID == pid && !owned.Start.IsZero() && ok && t.Equal(owned.Start) {
						authorized = true
					}
				}
			}
			if !authorized && len(pids) > 0 {
				return fmt.Errorf("cannot verify leaderless leg process group %d ownership", o.PGID.PID)
			}
			if authorized {
				for _, pid := range pids {
					track(pid)
				}
				if known {
					tracked[o.PGID.PID] = o.PGID
				}
				sys.killGroup(o.PGID.PID)
			}
		}
	}

	reap := func(p Proc) {
		if p.PID <= 0 {
			return
		}
		t, ok := sys.startTime(p.PID)
		if !ok {
			// A process-list entry with no start time is not confirmed dead.
			if _, live := procs[p.PID]; live {
				tracked[p.PID] = p
			} else {
				sys.rmBrowser(p.PID)
			}
		} else if !p.Start.IsZero() && t.Equal(p.Start) {
			tracked[p.PID] = p
			sys.kill(p.PID)
			sys.rmBrowser(p.PID)
		}
	}
	reap(o.Daemon)
	reap(o.Electron)
	tmux := tmuxSocket(o.RunID, o.Leg)
	for _, pid := range slices.Sorted(maps.Keys(procs)) {
		if pid != os.Getpid() && (strings.Contains(procs[pid], dir+string(filepath.Separator)) || hasTmuxSocket(procs[pid], tmux)) {
			if _, killed := tracked[pid]; !killed {
				track(pid)
				sys.kill(pid)
				sys.rmBrowser(pid)
			}
		}
	}
	deadline := time.NewTimer(portWait)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		live, err := sys.procs()
		if err != nil {
			return fmt.Errorf("confirm leg process death: %w", err)
		}
		var alive, busy []int
		for pid, p := range tracked {
			t, known := sys.startTime(pid)
			_, listed := live[pid]
			if known && (p.Start.IsZero() || t.Equal(p.Start)) || listed && !known {
				alive = append(alive, pid)
			}
		}
		for pid, argv := range live {
			if pid != os.Getpid() && (strings.Contains(argv, dir+string(filepath.Separator)) || hasTmuxSocket(argv, tmux)) && !slices.Contains(alive, pid) {
				alive = append(alive, pid)
			}
		}
		for _, port := range []int{o.DaemonPort, o.DebugPort} {
			if port <= 0 {
				continue
			}
			inUse, err := sys.portBusy(port)
			if err != nil {
				return fmt.Errorf("confirm leg port %d free: %w", port, err)
			}
			if inUse {
				busy = append(busy, port)
			}
		}
		if len(alive) == 0 && len(busy) == 0 {
			return nil
		}
		slices.Sort(alive)
		select {
		case <-ctx.Done():
			return fmt.Errorf("leg death unconfirmed: pids %v alive, ports %v still in use: %w", alive, busy, ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("leg death unconfirmed after %s: pids %v alive, ports %v still in use", portWait, alive, busy)
		case <-tick.C:
		}
	}
}

// wipeLeg runs only after killLeg confirmed death. Logs and ownership remain.
func wipeLeg(dir string) error {
	o, err := readOwned(dir)
	if errors.Is(err, fs.ErrNotExist) || err == nil && o.State == "cleaned" {
		return nil
	}
	if err != nil {
		return err
	}
	for _, sub := range []string{"electron", "data", "home"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			return err
		}
	}
	o.State = "cleaned"
	return writeOwned(dir, o)
}

// All cleanup paths confirm death before deleting profile and app data.
func cleanLeg(ctx context.Context, sys sysOps, dir string, beforeWipe ...func() error) error {
	if err := killLeg(ctx, sys, dir); err != nil {
		return err
	}
	for _, inspect := range beforeWipe {
		if err := inspect(); err != nil {
			return err
		}
	}
	return wipeLeg(dir)
}

// Every service path that deletes a leg home captures forks before deletion.
func (s *Service) cleanLeg(ctx context.Context, id, leg string) error {
	dir := s.store.legDir(id, leg)
	if err := cleanLeg(ctx, s.sys, dir, func() error {
		_, err := s.legHomeForksBeforeWipe(id, leg)
		return err
	}); err != nil {
		return err
	}
	var errs []error
	for _, sub := range []string{"agents", "lib", "bin", "shell", "home", "uv", "pipx"} {
		if err := os.RemoveAll(filepath.Join(dir, sub)); err != nil {
			errs = append(errs, fmt.Errorf("remove %s state for %s: %w", sub, leg, err))
		}
	}
	return errors.Join(errs...)
}

// teardown stops a run's engines and targets, removes their private state (the worktrees their private AO
// made go with it), prunes the shared clones those worktrees were registered in and checks the ports are free.
func (s *Service) teardown(ctx context.Context, run Run) error {
	unlock := s.lockRun(run.ID)
	defer unlock()
	current, err := s.store.Get(run.ID)
	if err != nil {
		return err
	}
	if current.TeardownComplete {
		if err := s.finishHomeGuard(run.ID); err != nil {
			return err
		}
		return s.refreshHomeChangesVerdictLocked(run.ID)
	}
	run = current
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TeardownBudget)
	defer cancel()
	if s.closeEngine != nil {
		s.closeEngine(run.ID)
	}
	errs := make([]error, 0, 8+len(run.Legs))
	stopped := true
	cleaned := map[string]bool{}
	for _, leg := range []string{"head", "base"} {
		if err := s.cleanLeg(ctx, run.ID, leg); err != nil {
			stopped = false
			errs = append(errs, err)
			continue
		}
		cleaned[leg] = true
	}
	for _, l := range run.Legs {
		errs = append(errs, s.pruneClone(ctx, l.Worktree))
	}
	complete := errors.Join(errs...) == nil
	if len(run.Legs) > 0 || complete {
		_, err := s.update(run.ID, func(cur *Run) {
			cur.TeardownComplete = complete
			for i := range cur.Legs {
				if cleaned[cur.Legs[i].Name] {
					cur.Legs[i].State = "cleaned"
				}
			}
		})
		errs = append(errs, err)
	}
	if stopped {
		if err := s.finishHomeGuard(run.ID); err != nil {
			errs = append(errs, err)
		} else {
			errs = append(errs, s.refreshHomeChangesVerdictLocked(run.ID))
		}
	}
	return errors.Join(errs...)
}

// An open run can launch again after stale cleanup. Invalidate its previous
// completion before spawning so a later close stops the new target and scans home again.
func (s *Service) resetTeardown(id string) error {
	unlock := s.lockRun(id)
	defer unlock()
	path := filepath.Join(s.store.root, id, homeChangesFile)
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && !strings.HasSuffix(string(data), pendingHomeReportSuffix) && !strings.HasSuffix(string(data), incompleteHomeReportSuffix) {
		if err := s.store.WriteFile(id, homeChangesFile, append(data, []byte("\n"+pendingHomeReportSuffix)...)); err != nil {
			return err
		}
	}
	_, err = s.update(id, func(cur *Run) { cur.TeardownComplete = false })
	return err
}

// Cleanup stops both legs, deletes their private state and closes the run. It
// works on any run, including a submitted or expired one.
func (s *Service) Cleanup(ctx context.Context, sessionID string) (Run, error) {
	id, err := runID(sessionID)
	if err != nil {
		return Run{}, err
	}
	run, err := s.store.Get(id)
	if errors.Is(err, fs.ErrNotExist) {
		return Run{}, apierr.NotFound("TESTING_RUN_NOT_FOUND", "no testing run for this session; start with `ao testing intake <pr-url>`")
	}
	if err != nil {
		return Run{}, err
	}
	s.warnOwnerLogin(ctx, sessionID)
	s.mu.Lock()
	done, err := s.markBusy(run.ID, "cleanup")
	s.mu.Unlock()
	if err != nil {
		return Run{}, err
	}
	defer done()
	run = s.stopRecording(ctx, run) // while the engines are still alive
	failed := s.teardown(ctx, run)
	s.warnOwnerLogin(ctx, sessionID)
	out, err := s.update(run.ID, func(cur *Run) {
		if failed == nil && !cur.State.Closed() {
			cur.State = RunCleaned
		}
	})
	if err == nil && out.State.Closed() && s.sweepAfterClose != nil {
		s.sweepAfterClose()
	}
	return out, errors.Join(failed, err)
}

// SweepStale stops targets left behind by an earlier daemon and finishes pending
// home reports, including closed runs that never launched. Also runs after a run closes.
func SweepStale(dataDir string, log *slog.Logger) { sweepStale(dataDir, realSys(), log) }

func sweepStale(dataDir string, sys sysOps, log *slog.Logger) {
	guard := &Service{store: NewStore(dataDir), sys: sys, runner: execRunner{}, clock: time.Now, busy: map[string]busyVerb{}}
	guard.sweepStale(log)
}

func (s *Service) sweepStale(log *slog.Logger) {
	s.staleSweepMu.Lock()
	defer s.staleSweepMu.Unlock()
	store := s.store
	entries, err := os.ReadDir(store.root)
	if err != nil && !os.IsNotExist(err) {
		log.Warn("litmus: could not scan leftover targets", "error", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		done, ok := s.beginStaleSweep(entry.Name())
		if !ok {
			continue
		}
		unlock := s.lockRun(entry.Name())
		stopped, complete := false, true
		guard := s
		ctx, cancel := context.WithTimeout(context.Background(), TeardownBudget)
		cleaned := map[string]bool{}
		for _, leg := range []string{"head", "base"} {
			dir := store.legDir(entry.Name(), leg)
			info, err := os.Lstat(dir)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || !info.IsDir() {
				complete = false
				log.Warn("litmus: cannot verify leftover leg directory", "dir", dir, "error", err)
				continue
			}
			o, err := readOwned(dir)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				complete = false
				log.Warn("litmus: cannot read leftover target ownership", "dir", dir, "error", err)
				continue
			}
			run, runErr := store.Get(entry.Name())
			if runErr == nil && run.TeardownComplete {
				cleaned[leg] = true
				continue
			}
			if err := guard.cleanLeg(ctx, entry.Name(), leg); err != nil {
				complete = false
				log.Warn("litmus: could not stop a leftover target", "run", o.RunID, "leg", o.Leg, "error", err)
				continue
			}
			stopped = true
			cleaned[leg] = true
			if o.State != "cleaned" {
				log.Info("litmus: stopped a leftover target", "run", o.RunID, "leg", o.Leg)
			}
		}
		run, runErr := store.Get(entry.Name())
		reportReady := stopped
		if runErr == nil {
			if run.State.Closed() || run.TeardownComplete {
				reportReady = true // A blocked submission may never have launched a leg.
			}
			for _, leg := range run.Legs {
				if !cleaned[leg.Name] && leg.State != "cleaned" && !(leg.State == "prepared" && leg.PID == 0) {
					complete = false
					log.Warn("litmus: cannot confirm leftover leg cleanup without ownership", "run", run.ID, "leg", leg.Name)
				}
			}
		}
		if stopped {
			if runErr == nil {
				for i := range run.Legs {
					if err := guard.pruneClone(ctx, run.Legs[i].Worktree); err != nil {
						complete = false
						log.Warn("litmus: could not prune leftover worktrees", "run", run.ID, "leg", run.Legs[i].Name, "error", err)
					}
				}
				_, err := guard.update(run.ID, func(cur *Run) {
					cur.TeardownComplete = complete
					for i := range cur.Legs {
						if cleaned[cur.Legs[i].Name] {
							cur.Legs[i].State = "cleaned"
						}
					}
				})
				if err != nil {
					log.Warn("litmus: could not save stale cleanup leg states", "run", run.ID, "error", err)
				}
			} else if !os.IsNotExist(runErr) {
				log.Warn("litmus: could not read stale cleanup run", "run", entry.Name(), "error", runErr)
			}
		}
		if reportReady && complete {
			if err := guard.finishHomeGuard(entry.Name()); err != nil {
				log.Warn("litmus: could not compare owner home after stale cleanup", "run", entry.Name(), "error", err)
			} else if err := guard.refreshHomeChangesVerdictLocked(entry.Name()); err != nil {
				log.Warn("litmus: could not refresh owner home verdict after stale cleanup", "run", entry.Name(), "error", err)
			}
		}
		cancel()
		unlock()
		done()
	}
}

// Reserve cleanup under the same mutex as launches, so adoption cannot race the sweep.
func (s *Service) beginStaleSweep(id string) (func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, err := s.store.Get(id); err == nil {
		// Completed teardown only needs a pending or timed-out home report
		// retried. Do not reserve a sweep that would block an owner's post with
		// no work to do.
		if run.TeardownComplete {
			report, err := os.ReadFile(filepath.Join(s.store.root, id, homeChangesFile))
			if err == nil && len(report) > 0 && !homeReportNeedsRetry(string(report)) {
				return nil, false
			}
		}
		// This daemon still owns engines/recorders until its teardown finishes.
		if s.ownedRuns[id] && (!run.State.Closed() || !run.TeardownComplete) {
			return nil, false
		}
	}
	done, err := s.markBusy(id, "sweep")
	return done, err == nil
}

// RecipeValidate parses a recipe file. With a "@<sha>" suffix on the path (see
// RecipeRef) it also prepares, launches, seeds and cleans up a head leg at that
// SHA, and reports the timings in the returned recipe.
func (s *Service) RecipeValidate(ctx context.Context, ref string) (Recipe, error) {
	path, sha := SplitRecipeRef(ref)
	data, err := os.ReadFile(path)
	if err != nil {
		return Recipe{}, invalid("TESTING_RECIPE_UNREADABLE", err.Error())
	}
	r, err := ParseRecipe(data)
	if err != nil {
		return Recipe{}, invalid("TESTING_RECIPE_INVALID", err.Error())
	}
	if sha == "" {
		return r, nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), RecipeValidateBudget)
	defer cancel()
	id, w := fmt.Sprintf("validate_%d", time.Now().Unix()), newLaps()
	s.mu.Lock()
	done, err := s.markBusy(id, "launch head")
	s.mu.Unlock()
	if err != nil {
		return Recipe{}, err
	}
	defer done()
	leg, err := s.prepareLeg(ctx, cmp.Or(r.Repo, DefaultRepo), r, "head", sha)
	w.lap("prepare")
	if err == nil {
		_, err = s.startLeg(ctx, id, 0, r, leg, w, nil, false, "recipe validation")
	}
	if err == nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TeardownBudget)
		err = s.cleanLeg(cleanupCtx, id, "head")
		cancel()
		w.lap("cleanup")
	}
	if err != nil {
		return Recipe{}, fmt.Errorf("%w (timings: %s)", err, strings.Join(w.out, ", "))
	}
	r.Timings = w.out
	return r, nil
}
