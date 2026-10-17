package litmus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

const (
	maxSocketPath = 103       // agent-browser's Unix preflight: the macOS sun_path limit
	outputCap     = 256 << 10 // per stream; the engine also caps itself via AGENT_BROWSER_MAX_OUTPUT
	// MaxDriveWait bounds an explicit agent-browser wait, before starting any process.
	MaxDriveWait = 5 * time.Minute
	// DriveBudget includes the longest wait, command overhead, setup and receipts.
	DriveBudget = MaxDriveWait + 2*time.Minute
)

// Tests shorten these or point the socket dir elsewhere.
var (
	cmdTimeout         = 30 * time.Second
	healthProbeTimeout = 2 * time.Second
	socketBase         = "/tmp" // not os.TempDir(): macOS temp paths would blow the socket path limit
)

var errTabDrift = errors.New("the app's tab was re-selected after a page it opened took focus, which clears element refs; take a new snapshot and use its refs")
var errLegDown = errors.New("the leg's app target is unavailable")

func isLegDown(err error) bool { return errors.Is(err, errLegDown) }

// A canceled client can leave the daemon executing with its command lock held.
// Stop the current Drive even if recovery succeeds: tester setup may be gone.
type engineInterruption struct{ error }

func (e *engineInterruption) Unwrap() error { return e.error }

type unsentAction struct{ error }

func (e *unsentAction) Error() string { return "the new action was NOT sent. " + e.error.Error() }
func (e *unsentAction) Unwrap() error { return e.error }

func actionNotSent(err error) error {
	var unsent *unsentAction
	if errors.As(err, &unsent) {
		return err
	}
	return &unsentAction{err}
}

func captureBlocked(err error) bool {
	var interrupted *engineInterruption
	return isLegDown(err) || isTabDrift(err) || errors.As(err, &interrupted)
}

const lostDaemonState = "Lost daemon-held state: tester setup may have been lost, including set emulation (color scheme, viewport/device, user agent, media, timezone, locale, geolocation, offline mode), headers and HTTP/proxy credentials, network routes/domain filters, init scripts, frame selection, input mode/held input, dialog handling, action policy and pending confirmations, element refs and snapshot history, console/errors/network history, in-progress record/trace/profiler/HAR captures, stream/inspect sessions, and pending WebMCP calls. Reapply tester setup, take a new snapshot, and restart any tester recording or trace before continuing."

// envAllow is the part of the daemon's environment an engine process may see (mirrors AO's allowlist).
var envAllow = []string{"path", "lang", "lc_all", "lc_ctype", "term", "colorterm", "no_color", "force_color"}

// EngineInfo is what run.json keeps about a leg's engine.
type EngineInfo struct {
	Binary    string `json:"binary,omitempty"`
	RunDir    string `json:"runDir"`
	Session   string `json:"session"`
	Namespace string `json:"namespace"`
	Endpoint  string `json:"endpoint"` // the target's remote-debugging URL
	SocketDir string `json:"socketDir"`
	AppOrigin string `json:"appOrigin,omitempty"`
}

// execFn runs one engine client process to completion and returns its stdout.
type execFn func(ctx context.Context, bin string, args, env []string) (string, error)

// Engine drives one launched leg through the bundled agent-browser. Every command is
// its own client process; the engine's per-session daemon lives on between them.
type Engine struct {
	EngineInfo
	exec          execFn
	connectPage   func(context.Context) (*pageCDP, error) // nil uses the leg's CDP endpoint
	onReattach    func(oldTab, newTab, appURL string)
	onRestart     func(string)
	leg           string
	diagnose      func(context.Context) (LegControl, error)
	onControl     func(LegControl) error
	abortAdvice   func() string                  // names the available legs when browser control is lost
	onInit        func()                         // marks the drive-log boundary for tester setup
	lostState     func() string                  // nil when there is no drive log
	identity      func() (daemonIdentity, error) // nil reads this session's PID file and process start time
	daemon        daemonIdentity                 // the process whose stream input and app binding were initialized
	pendingAction *actionRec                     // sent Drive command, before its receipt is appended to the log
	generation    uint64                         // increments when stream input is initialized
	mu            sync.Mutex                     // one action at a time per leg; Drive holds it across a whole receipt
	invokeMu      sync.Mutex                     // closes the invocation gate before teardown waits
	invocations   sync.WaitGroup                 // client processes that may still publish daemon runtime files
	last          string                         // the previous after-shot of this leg
	theme         string                         // Litmus's current prefers-color-scheme override; empty follows the system
	refs          map[string]refInfo             // the last snapshot's elements by @ref, so receipts can name what was acted on
	once          sync.Once                      // home dir and config.json
	ready         bool                           // stream input is disabled in the current daemon
	tab           string                         // the app's own tab: the engine's active tab follows pages the app opens, so it is re-selected when it moves
	ierr          error
	reset         bool            // the next invocation must initialize the daemon and app binding
	recovering    bool            // recovery commands must not recursively recover themselves
	restartNote   string          // retained until Drive can report state loss to a connected caller
	abort         func() error    // nil uses abortDaemon; tests model the persistent daemon
	life          context.Context // canceled by Close, which stops anything in flight
	stop          context.CancelFunc
}

func newEngine(info EngineInfo, run execFn) *Engine {
	life, stop := context.WithCancel(context.Background())
	return &Engine{EngineInfo: info, exec: run, life: life, stop: stop}
}

// NewEngine allocates the leg's engine: a short /tmp socket dir (the default temp dir
// is too long for a Unix socket on macOS) and a namespace of its own.
func NewEngine(binary, runDir, leg string, debugPort int, run execFn) (*Engine, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(b[:])
	info := EngineInfo{Binary: binary, RunDir: runDir, Session: leg + "-" + id[:6], Namespace: leg + "-" + id[:6],
		Endpoint: "http://127.0.0.1:" + strconv.Itoa(debugPort), SocketDir: filepath.Join(socketBase, "litmus-"+id)}
	if n := len(socketPath(info.SocketDir, info.Namespace)); n > maxSocketPath {
		return nil, fmt.Errorf("engine socket path is %d bytes, the limit is %d", n, maxSocketPath)
	}
	if err := os.Mkdir(info.SocketDir, 0o700); err != nil {
		return nil, err
	}
	return newEngine(info, run), nil
}

func socketPath(dir, ns string) string {
	return filepath.Join(dir, "namespaces", ns, "run", ns+".sock")
}

func (e *Engine) home() string { return filepath.Join(e.RunDir, "engine", e.Namespace) }

func (e *Engine) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); slices.Contains(envAllow, strings.ToLower(k)) {
			env = append(env, kv)
		}
	}
	h := e.home()
	return append(env, "HOME="+h, "USERPROFILE="+h, "XDG_CONFIG_HOME="+h, "XDG_CACHE_HOME="+h, "TEMP="+h, "TMP="+h, "TMPDIR="+h,
		"AO_LITMUS_CWD="+e.RunDir, "AGENT_BROWSER_DOWNLOAD_PATH="+filepath.Join(e.RunDir, "downloads"),
		"AGENT_BROWSER_CONFIG="+filepath.Join(h, "config.json"), "AGENT_BROWSER_CDP="+e.Endpoint,
		"AGENT_BROWSER_SOCKET_DIR="+e.SocketDir, "AGENT_BROWSER_SESSION="+e.Session, "AGENT_BROWSER_NAMESPACE="+e.Namespace,
		// The daemon owns tester setup for the whole leg; Close owns its lifetime.
		"AGENT_BROWSER_CONTENT_BOUNDARIES=1", "AGENT_BROWSER_MAX_OUTPUT=50000", "AGENT_BROWSER_IDLE_TIMEOUT_MS=0", "AGENT_BROWSER_AUTO_CONNECT=0")
}

func (e *Engine) invoke(ctx context.Context, args ...string) (string, error) {
	e.invokeMu.Lock()
	if err := e.life.Err(); err != nil {
		e.invokeMu.Unlock()
		return "", err
	}
	e.invocations.Add(1)
	e.invokeMu.Unlock()
	defer e.invocations.Done()
	return e.exec(ctx, e.Binary, append([]string{"--session", e.Session}, args...), e.env())
}

func (e *Engine) invokeTimed(ctx context.Context, phase string, args ...string) (string, error) {
	return e.invokeTimedBeforeSend(ctx, phase, nil, args...)
}

func (e *Engine) invokeTimedBeforeSend(ctx context.Context, phase string, beforeSend func(), args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := e.checkDaemon(); err != nil {
		if e.recovering {
			return "", err
		}
		return "", &engineInterruption{actionNotSent(err)}
	}
	started := time.Now()
	if beforeSend != nil {
		beforeSend()
	}
	out, err := e.invoke(ctx, args...)
	// A client may auto-spawn a daemon between the preflight and this reply.
	// Explicit close commands are the expected end of this daemon's lifetime.
	if !slices.Contains([]string{"close", "quit", "exit"}, args[0]) && e.life.Err() == nil {
		if identityErr := e.checkDaemon(); identityErr != nil {
			if !e.recovering && (phase == "command" || phase == "tab-list") {
				return out, &engineInterruption{fmt.Errorf("the action WAS sent and may have reached a replacement daemon. Its effect is uncertain. Look at the screen before retrying: %w", errors.Join(err, identityErr))}
			}
			return out, errors.Join(err, identityErr)
		}
	}
	if err != nil && ctx.Err() != nil && e.life.Err() == nil && !e.recovering {
		err = &engineInterruption{e.recoverInvocation(err)}
	}
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	fields := []any{"session", e.Session, "phase", phase, "command", command, "ms", time.Since(started).Milliseconds()}
	if err != nil {
		fields = append(fields, "error", err)
	}
	slog.Default().Info("litmus: engine command", fields...)
	return out, err
}

func (e *Engine) recoverInvocation(cause error) error {
	// `stream status` goes through the native DaemonState lock without changing
	// browser setup. This is a separate diagnosis, never a replay of the action.
	// A short tab-probe timeout alone is not evidence that the daemon is wedged.
	ctx, cancel := context.WithTimeout(e.life, cmdTimeout)
	_, healthErr := e.invoke(ctx, "stream", "status")
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	cancel()
	if healthErr == nil {
		return errors.Join(cause, e.checkDaemon())
	}
	if !timedOut || e.life.Err() != nil {
		return errors.Join(cause, fmt.Errorf("agent-browser recovery status: %w", healthErr))
	}
	abort := e.abort
	if abort == nil {
		abort = e.abortDaemon
	}
	if err := abort(); err != nil {
		advice := "The " + e.Session + " leg's browser control is unusable for the rest of this run."
		if e.abortAdvice != nil {
			advice = e.abortAdvice()
		}
		e.ierr = &engineInterruption{fmt.Errorf("abort unresponsive agent-browser daemon: %w. %s Mark checks you cannot finish skipped with a reason, and submit", err, advice)}
		return errors.Join(cause, e.ierr)
	}
	return e.restartDaemon(cause, "after the daemon recovery status timed out")
}

func (e *Engine) restartDaemon(cause error, reason string) error {
	lost := lostDaemonState
	if e.lostState != nil {
		lost = e.lostState()
	}
	e.pendingAction = nil
	e.reset, e.tab, e.refs, e.last = true, "", nil, ""
	e.daemon = daemonIdentity{}
	e.recovering = true
	_, restartErr := e.Run(e.life, cmdTimeout, "tab", "list")
	e.recovering = false
	message := "browser session was restarted " + reason + ". " + lost
	if restartErr != nil {
		message = "agent-browser " + reason + "; browser session restart failed: " + restartErr.Error() + ". " + lost
		// This request stops here. A later explicit request may initialize the
		// fresh daemon; do not cache a transient setup/probe failure forever.
		e.reset = true
	}
	e.restartNote = message
	if e.onRestart != nil {
		e.onRestart(message)
	}
	return fmt.Errorf("%w; %s", cause, message)
}

// Run executes one engine command with a timeout; the process is killed when it runs out.
func (e *Engine) Run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	return e.runBeforeSend(ctx, timeout, nil, args...)
}

// runBeforeSend executes before the command's client process is started, after
// setup and health probes have completed. It is used by Drive to record the
// action's send boundary without changing the public Run API.
func (e *Engine) runBeforeSend(ctx context.Context, timeout time.Duration, beforeSend func(), args ...string) (out string, err error) {
	defer func() {
		if err != nil && e.life.Err() == nil && (isLegDown(err) || e.ierr != nil) {
			var diagnosed *diagnosedControlError
			if e.ierr != nil && e.onControl != nil {
				control := LegControl{State: "lost", Cause: "browser_control_unusable", Detail: fmt.Sprintf("the %s leg's browser control is unusable: %v", e.leg, e.ierr)}
				control.PublicDetail = publicControlDetailFromCause(control.Cause, control.Detail)
				err = errors.Join(&diagnosedControlError{error: err, cause: errors.Join(errLegDown, err), leg: e.leg, detail: control.Detail}, e.onControl(control))
			} else if !errors.As(err, &diagnosed) {
				err = e.unavailable(ctx, err)
			}
		}
	}()
	if e.Binary == "" {
		return "", apierr.Unavailable("TESTING_ENGINE_MISSING", "agent-browser binary not found: set AO_AGENT_BROWSER_PATH, or place it at ../agent-browser/agent-browser beside the daemon")
	}
	if len(args) == 0 {
		return "", errors.New("agent-browser command is empty")
	}
	timeout, err = engineCommandBudget(timeout, args, e.RunDir)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("agent-browser %s: %w", args[0], err)
	}
	if e.reset {
		e.once, e.ierr, e.reset, e.ready = sync.Once{}, nil, false, false
		e.daemon, e.tab, e.refs, e.last = daemonIdentity{}, "", nil, ""
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(e.life, cancel)()
	e.once.Do(func() {
		if e.ierr = os.MkdirAll(e.home(), 0o700); e.ierr == nil {
			e.ierr = os.WriteFile(filepath.Join(e.home(), "config.json"), []byte("{}\n"), 0o600)
		}
	})
	if e.ierr != nil {
		return "", e.ierr
	}
	if !e.ready { // a fresh daemon streams by default; disable that input surface
		sctx, stopTimeout := context.WithTimeout(runCtx, cmdTimeout)
		_, setupErr := e.invokeTimed(sctx, "setup", "stream", "disable")
		stopTimeout()
		if setupErr != nil {
			return "", setupErr
		}
		identity, identityErr := e.daemonIdentity()
		if identityErr != nil {
			if e.recovering {
				return "", &engineInterruption{fmt.Errorf("identify recovery agent-browser daemon: %w", identityErr)}
			}
			return "", &engineInterruption{actionNotSent(fmt.Errorf("identify initialized agent-browser daemon: %w", identityErr))}
		}
		e.daemon = identity
		e.generation++
		if e.onInit != nil {
			e.onInit()
		}
		e.ready = true
	}
	helpOnly := args[0] == "tab" && slices.Contains(args[1:], "--help")
	if !helpOnly && e.tab == "" {
		if err := e.initializeTab(runCtx); err != nil {
			return "", err
		}
	}
	if !helpOnly && !(args[0] == "tab" && len(args) > 1 && args[1] == "list") { // tab list is itself the health probe
		if err := e.probeApp(runCtx, args); err != nil {
			return "", err
		}
	}
	phase := "command"
	if args[0] == "tab" && len(args) > 1 && args[1] == "list" {
		phase = "tab-list"
	}
	// Setup and health probes must not spend the command's own deadline.
	if err := runCtx.Err(); err != nil {
		return "", fmt.Errorf("agent-browser %s: %w", args[0], err)
	}
	// Once sent, the daemon keeps executing after its client disconnects. Keep
	// that client alive through the operation's own deadline, even if HTTP ends.
	cmdCtx, stopCommand := context.WithTimeout(context.WithoutCancel(runCtx), timeout)
	defer stopCommand()
	defer context.AfterFunc(e.life, stopCommand)()
	out, err = e.invokeTimedBeforeSend(cmdCtx, phase, beforeSend, args...)
	if err != nil {
		err = fmt.Errorf("agent-browser %s: %w", args[0], err)
	}
	return out, err
}

// engineCommandBudget mirrors wait's operation timeout: a numeric operand wins
// over --timeout, while condition waits use --timeout when supplied.
func engineCommandBudget(overhead time.Duration, args []string, runDir string) (time.Duration, error) {
	if len(args) < 2 || args[0] != "wait" {
		return overhead, nil
	}
	operands, _, err := browserOptions(args[1:], runDir)
	if err != nil {
		return 0, err
	}
	var explicit string
	timeoutSet := false
	var rest []string
	for i := 0; i < len(operands); i++ {
		if !timeoutSet && operands[i] == "--timeout" && i+1 < len(operands) {
			i++
			explicit, timeoutSet = operands[i], true
		} else {
			rest = append(rest, operands[i])
		}
	}
	condition := slices.ContainsFunc(rest, func(a string) bool {
		return slices.Contains([]string{"--url", "-u", "--load", "-l", "--fn", "-f", "--text", "-t", "--download", "-d"}, a)
	})
	if !condition && len(rest) > 0 {
		if _, err := strconv.ParseUint(strings.TrimPrefix(rest[0], "+"), 10, 64); err == nil || errors.Is(err, strconv.ErrRange) {
			explicit = rest[0]
		}
	}
	if explicit == "" {
		return overhead, nil
	}
	ms, err := strconv.ParseUint(strings.TrimPrefix(explicit, "+"), 10, 64)
	if errors.Is(err, strconv.ErrRange) || err == nil && ms > uint64(MaxDriveWait.Milliseconds()) {
		return 0, invalid("TESTING_ARGS_INVALID", fmt.Sprintf("wait maximum is %d ms (%s)", MaxDriveWait.Milliseconds(), MaxDriveWait))
	}
	if err != nil {
		return 0, invalid("TESTING_ARGS_INVALID", "wait timeout must be a nonnegative number of milliseconds")
	}
	return time.Duration(ms)*time.Millisecond + overhead, nil
}

var tabLine = regexp.MustCompile(`^(→)? *\[(t\d+)\] .* - (\S+)\s*$`)

type tabInfo struct {
	id     string
	url    string
	active bool
}

// parseTabs reads `tab list` ("→ [t1] title - url", the arrow on the active tab): the active tab's id
// and the first real (not about:) page's id.
func parseTabs(list string) (active, app string) {
	active, app, _ = parseTabsDetailed(list)
	return active, app
}

func parseTabsDetailed(list string) (active, app, appURL string) {
	for _, l := range strings.Split(list, "\n") {
		if m := tabLine.FindStringSubmatch(l); m != nil {
			if m[1] != "" {
				active = m[2]
			}
			if app == "" && !strings.HasPrefix(m[3], "about:") {
				app = m[2]
				appURL = m[3]
			}
		}
	}
	return active, app, appURL
}

func (e *Engine) initializeTab(ctx context.Context) error {
	if e.AppOrigin == "" {
		return fmt.Errorf("%w: this leg has no recorded app URL from launch readiness", errLegDown)
	}
	hctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	stop := context.AfterFunc(e.life, cancel)
	list, err := e.invokeTimed(hctx, "setup-tab-list", "tab", "list")
	stop()
	cancel()
	if err != nil {
		return e.unavailable(ctx, fmt.Errorf("agent-browser find the leg's app tab: %w", err))
	}
	entries := parseTabEntries(stripPageContentBoundary(list))
	var candidate *tabInfo
	for i := range entries {
		if appURLMatches(e.AppOrigin, entries[i].url) {
			candidate = &entries[i]
			break
		}
	}
	if candidate == nil {
		return fmt.Errorf("%w: no app tab matches origin %q", errLegDown, e.AppOrigin)
	}
	pctx, stopPin := context.WithTimeout(ctx, cmdTimeout)
	defer stopPin()
	if _, err := e.invokeTimed(pctx, "setup-pin", "tab", candidate.id, "--pin-tab"); err != nil {
		return e.unavailable(ctx, fmt.Errorf("agent-browser pin app tab: %w", err))
	}
	e.tab = candidate.id
	slog.Default().Info("litmus: app tab selected", "session", e.Session, "tabID", e.tab, "url", diagnosticURL(candidate.url), "appOrigin", e.AppOrigin)
	return nil
}

func appOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme == "file" {
		u.RawQuery = ""
		u.Fragment = ""
		u.Path = path.Dir(u.Path)
		if !strings.HasSuffix(u.Path, "/") {
			u.Path += "/"
		}
		return u.String()
	}
	if u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}

// appURLMatches compares a candidate tab to the app URL recorded before seed
// steps run. File pages have no host, so their path provides the stable binding.
func appURLMatches(expected, candidate string) bool {
	expectedURL, expectedErr := url.Parse(strings.TrimSpace(expected))
	candidateURL, candidateErr := url.Parse(strings.TrimSpace(candidate))
	if expectedErr != nil || candidateErr != nil || expectedURL.Scheme == "" || !strings.EqualFold(expectedURL.Scheme, candidateURL.Scheme) {
		return false
	}
	if strings.EqualFold(expectedURL.Scheme, "file") {
		if !strings.EqualFold(expectedURL.Host, candidateURL.Host) {
			return false
		}
		return pathHasPrefix(expectedURL.Path, candidateURL.Path)
	}
	return expectedURL.Host != "" && strings.EqualFold(expectedURL.Host, candidateURL.Host)
}

func pathHasPrefix(prefix, candidate string) bool {
	prefix, candidate = path.Clean(prefix), path.Clean(candidate)
	if prefix == "." || candidate == "." {
		return prefix == candidate
	}
	return candidate == prefix || strings.HasPrefix(candidate, strings.TrimSuffix(prefix, "/")+"/")
}

func isBlankURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(u.Scheme, "about") && strings.EqualFold(u.Opaque, "blank")
}

// diagnosticURL keeps target diagnostics useful without copying credentials,
// query tokens, or fragments into logs and run records.
func diagnosticURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(raw), "about:") {
		return strings.SplitN(raw, "?", 2)[0]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func hasElementRef(args []string) bool {
	return slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "@e") })
}

func (e *Engine) probeApp(ctx context.Context, args []string) error {
	hctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	stop := context.AfterFunc(e.life, cancel)
	list, err := e.invokeTimed(hctx, "tab-probe", "tab", "list")
	stop()
	cancel()
	if err != nil {
		return e.unavailable(ctx, fmt.Errorf("agent-browser tab probe failed: %w", err))
	}
	list = stripPageContentBoundary(list)
	active, _, _ := parseTabsDetailed(list)
	entries := parseTabEntries(list)
	for _, entry := range entries {
		slog.Default().Info("litmus: tab probe target", "session", e.Session, "tabID", entry.id, "active", entry.active, "url", diagnosticURL(entry.url))
	}
	pinned := tabByID(entries, e.tab)
	if pinned == nil || isBlankURL(pinned.url) {
		if err := e.reattachApp(ctx, entries); err != nil {
			return err
		}
		if hasElementRef(args) {
			return errTabDrift
		}
		return nil
	}
	if active != e.tab {
		sctx, stopSelect := context.WithTimeout(ctx, cmdTimeout)
		defer stopSelect()
		if _, err := e.invokeTimed(sctx, "tab-select", "tab", e.tab); err != nil {
			return e.unavailable(ctx, fmt.Errorf("agent-browser select app tab: %w", err))
		}
		if hasElementRef(args) {
			return errTabDrift
		}
	}
	slog.Default().Info("litmus: tab probe", "session", e.Session, "tabID", e.tab, "active", active, "appOrigin", e.AppOrigin)
	return nil
}

func parseTabEntries(list string) []tabInfo {
	var entries []tabInfo
	for _, l := range strings.Split(list, "\n") {
		if m := tabLine.FindStringSubmatch(l); m != nil {
			entries = append(entries, tabInfo{id: m[2], url: m[3], active: m[1] != ""})
		}
	}
	return entries
}

func tabByID(entries []tabInfo, id string) *tabInfo {
	for i := range entries {
		if entries[i].id == id {
			return &entries[i]
		}
	}
	return nil
}

func (e *Engine) reattachApp(ctx context.Context, entries []tabInfo) error {
	if pinned := tabByID(entries, e.tab); pinned != nil && !isBlankURL(pinned.url) {
		return fmt.Errorf("%w: pinned app tab %q still exists at a nonblank URL", errLegDown, e.tab)
	}
	if e.AppOrigin == "" {
		old := e.tab
		e.tab = ""
		return fmt.Errorf("%w: pinned app tab %q is missing and this leg has no recorded app URL", errLegDown, old)
	}
	var candidate *tabInfo
	for i := range entries {
		if appURLMatches(e.AppOrigin, entries[i].url) {
			candidate = &entries[i]
			break
		}
	}
	if candidate == nil {
		old := e.tab
		e.tab = ""
		return fmt.Errorf("%w: pinned app tab %q is missing; no app tab matches origin %q", errLegDown, old, e.AppOrigin)
	}
	old := e.tab
	e.tab = ""
	pctx, stopPin := context.WithTimeout(ctx, cmdTimeout)
	defer stopPin()
	if _, err := e.invokeTimed(pctx, "tab-reattach", "tab", candidate.id, "--pin-tab"); err != nil {
		return e.unavailable(ctx, fmt.Errorf("agent-browser pin replacement app tab: %w", err))
	}
	e.tab = candidate.id
	slog.Default().Info("litmus: app tab reattached", "session", e.Session, "oldTabID", old, "tabID", e.tab, "url", diagnosticURL(candidate.url))
	if e.onReattach != nil {
		e.onReattach(old, e.tab, candidate.url)
	}
	return nil
}

// abortDaemon terminates only this engine's owned automation process, without
// sending `close`, which could close the app target. Unlink its runtime files
// so the next client cannot connect to the canceled daemon's socket.
func (e *Engine) abortDaemon() error {
	if e.SocketDir == "" { // engines without a socket directory have no daemon
		return nil
	}
	if !strings.HasPrefix(filepath.Base(e.SocketDir), "litmus-") || filepath.Base(e.Namespace) != e.Namespace || filepath.Base(e.Session) != e.Session {
		return errors.New("invalid engine runtime directory")
	}
	runDir := filepath.Join(e.SocketDir, "namespaces", e.Namespace, "run")
	pidPath := filepath.Join(runDir, e.Session+".pid")
	sys := realSys()
	daemon, err := e.ownedDaemon(sys, pidPath)
	if err != nil {
		return err
	}
	if err := e.killDaemon(sys, daemon); err != nil {
		return err
	}
	return os.RemoveAll(runDir)
}

type ownedEngineDaemon struct {
	path  string
	data  []byte
	mtime time.Time
	daemonIdentity
}

// ownedDaemon records ownership before teardown. A
// successful process listing is required even when the daemon already exited.
func (e *Engine) ownedDaemon(sys sysOps, pidPath string) (ownedEngineDaemon, error) {
	d := ownedEngineDaemon{path: pidPath}
	var err error
	d.data, err = os.ReadFile(pidPath)
	if os.IsNotExist(err) {
		return d, errors.New("cannot verify owned agent-browser daemon: PID file is missing")
	}
	if err != nil {
		return d, err
	}
	d.pid, err = strconv.Atoi(strings.TrimSpace(string(d.data)))
	if err != nil || d.pid <= 0 {
		return d, errors.New("cannot verify owned agent-browser daemon PID")
	}
	info, err := os.Stat(pidPath)
	if err != nil {
		return d, err
	}
	d.mtime = info.ModTime()
	procs, err := sys.procs()
	if err != nil {
		return d, fmt.Errorf("inspect agent-browser daemon processes: %w", err)
	}
	if _, live := procs[d.pid]; !live {
		return d, nil
	}
	started, ok := sys.startTime(d.pid)
	if !ok || started.After(d.mtime) || !strings.Contains(procs[d.pid], filepath.Base(e.Binary)) {
		return d, errors.New("cannot verify owned agent-browser daemon PID and start time")
	}
	d.start = started
	return d, nil
}

// killDaemon checks identity again before killing, then verifies that the owned
// process exited. An unknown result leaves its PID file available for cleanup.
func (e *Engine) killDaemon(sys sysOps, d ownedEngineDaemon) error {
	for phase := 0; phase < 2; phase++ {
		procs, err := sys.procs()
		if err != nil {
			return fmt.Errorf("verify agent-browser daemon exit: %w", err)
		}
		if _, live := procs[d.pid]; !live {
			return nil
		}
		started, ok := sys.startTime(d.pid)
		if !ok {
			return errors.New("cannot verify agent-browser daemon start time during teardown")
		}
		if !d.start.IsZero() && !started.Equal(d.start) {
			return fmt.Errorf("agent-browser daemon PID %d was replaced during teardown", d.pid)
		}
		if d.start.IsZero() || !strings.Contains(procs[d.pid], filepath.Base(e.Binary)) {
			return errors.New("cannot verify owned agent-browser daemon during teardown")
		}
		if phase == 1 {
			return fmt.Errorf("agent-browser daemon PID %d is still running after teardown kill", d.pid)
		}
		sys.kill(d.pid)
	}
	return nil
}

func keepDaemonPID(d ownedEngineDaemon) error {
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(d.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write(d.data)
	return errors.Join(writeErr, f.Close(), os.Chtimes(d.path, d.mtime, d.mtime))
}

// Close stops in-flight clients and terminates the owned daemon directly. A
// close client can auto-spawn a replacement, so teardown never starts one.
func (e *Engine) Close(sys sysOps) error {
	e.stop()
	e.invokeMu.Lock()
	e.invokeMu.Unlock()
	e.invocations.Wait()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.SocketDir == "" || !strings.HasPrefix(filepath.Base(e.SocketDir), "litmus-") {
		return nil
	}
	pids, err := filepath.Glob(filepath.Join(e.SocketDir, "namespaces", "*", "run", "*.pid"))
	if err != nil || len(pids) > 0 && e.Binary == "" {
		slog.Default().Error("litmus: engine teardown unverified; keeping PID files", "path", e.SocketDir, "error", errors.Join(err, errors.New("cannot identify owned daemon runtime")))
		return errors.Join(err, errors.New("cannot identify owned daemon runtime"))
	}
	if len(pids) == 0 && e.daemon.pid != 0 {
		if sys.procs == nil {
			slog.Default().Error("litmus: engine teardown unverified; initialized daemon PID file is missing", "path", e.SocketDir, "pid", e.daemon.pid)
			return errors.New("cannot verify agent-browser exit: initialized daemon PID file is missing")
		}
		procs, err := sys.procs()
		if _, live := procs[e.daemon.pid]; err != nil || live {
			slog.Default().Error("litmus: engine teardown unverified; initialized daemon PID file is missing", "path", e.SocketDir, "pid", e.daemon.pid, "error", err)
			return errors.Join(err, errors.New("cannot verify agent-browser exit: initialized daemon PID file is missing"))
		}
	}
	if len(pids) > 0 && e.Binary != "" {
		var daemons []ownedEngineDaemon
		for _, pidPath := range pids {
			d, err := e.ownedDaemon(sys, pidPath)
			if err != nil {
				slog.Default().Error("litmus: engine teardown unverified; keeping PID file", "pidFile", pidPath, "pid", d.pid, "error", err)
				return err
			}
			daemons = append(daemons, d)
		}
		var teardownErr error
		for _, d := range daemons {
			if err := e.killDaemon(sys, d); err != nil {
				err = errors.Join(err, keepDaemonPID(d))
				slog.Default().Error("litmus: engine teardown unverified; keeping PID file", "pidFile", d.path, "pid", d.pid, "error", err)
				teardownErr = errors.Join(teardownErr, err)
			}
		}
		if teardownErr != nil {
			return teardownErr
		}
		current, err := filepath.Glob(filepath.Join(e.SocketDir, "namespaces", "*", "run", "*.pid"))
		if err == nil {
			for _, pidPath := range current {
				index := slices.IndexFunc(daemons, func(d ownedEngineDaemon) bool { return d.path == pidPath })
				if index < 0 {
					err = fmt.Errorf("new agent-browser PID file appeared during teardown: %s", pidPath)
					break
				}
				info, statErr := os.Stat(pidPath)
				data, readErr := os.ReadFile(pidPath)
				if statErr != nil || readErr != nil {
					err = errors.Join(statErr, readErr)
					break
				}
				if string(data) != string(daemons[index].data) || !info.ModTime().Equal(daemons[index].mtime) {
					err = fmt.Errorf("agent-browser PID file changed during teardown: %s", pidPath)
					break
				}
			}
		}
		if err != nil {
			slog.Default().Error("litmus: engine teardown unverified; keeping PID files", "path", e.SocketDir, "error", err)
			return err
		}
	}
	if err := os.RemoveAll(e.SocketDir); err != nil {
		slog.Default().Error("litmus: remove engine socket directory", "path", e.SocketDir, "error", err)
		return err
	}
	return nil
}

// capWriter keeps the first max bytes and discards the rest without failing the process.
type capWriter struct {
	buf []byte
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - len(w.buf); room > 0 {
		w.buf = append(w.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func realExec(ctx context.Context, bin string, args, env []string) (string, error) {
	cmd := process.CommandContext(ctx, bin, args...)
	// Default artifact names and project config discovery must stay in this run.
	for _, kv := range env {
		if dir, ok := strings.CutPrefix(kv, "AO_LITMUS_CWD="); ok {
			cmd.Dir = dir
			break
		}
	}
	out, errOut := &capWriter{max: outputCap}, &capWriter{max: outputCap}
	cmd.Env, cmd.Stdout, cmd.Stderr, cmd.WaitDelay = env, out, errOut, 2*time.Second // WaitDelay: a daemon holding our pipes cannot stall the kill
	err := cmd.Run()
	if cerr := ctx.Err(); cerr != nil {
		return string(out.buf), fmt.Errorf("killed: %w", cerr)
	}
	if err != nil {
		msg := strings.TrimSpace(string(errOut.buf))
		if msg == "" {
			msg = strings.TrimSpace(string(out.buf))
		}
		return string(out.buf), errors.New(err.Error() + ": " + lastLines([]byte(msg), 10))
	}
	return string(out.buf), nil
}

// resolveBinary finds agent-browser: AO_AGENT_BROWSER_PATH, else <daemon dir>/../agent-browser/agent-browser.
func resolveBinary(override, exe string) (string, error) {
	path := strings.TrimSpace(override)
	if path == "" {
		path = filepath.Join(filepath.Dir(exe), "..", "agent-browser", "agent-browser")
	}
	path, _ = filepath.Abs(path)
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return "", fmt.Errorf("agent-browser not found at %s", path)
	}
	return path, nil
}
