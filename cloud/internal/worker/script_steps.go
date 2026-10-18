package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/cloud/internal/activity"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
)

// Live Setup and Cleanup logs. Each step of a project's Setup or Cleanup run
// is reported on the session's event stream as a ScriptStepEventType event
// carrying the redacted tail of the step's output, so the person can follow a
// run and read it afterwards without opening the sandbox. The workspace.setup
// summary event is unchanged.

// ScriptStepEventType is the session event that reports one step of a Setup
// or Cleanup run.
const ScriptStepEventType = "workspace.script.step"

// Phases of ScriptStepEvent.Phase.
const (
	ScriptPhaseSetup   = "setup"
	ScriptPhaseCleanup = "cleanup"
)

// ScriptStepEvent.Status values beyond a finished step's SetupSucceeded,
// SetupFailed and SetupTimedOut.
const (
	ScriptStepRunning = "running"
	// ScriptStepSkipped is a step that never ran because an earlier one failed.
	ScriptStepSkipped = "skipped"
)

const (
	// ScriptStepOutputLimit bounds the output tail one event carries.
	ScriptStepOutputLimit = 16 << 10
	// ScriptStepCommandLimit bounds an event's command.
	ScriptStepCommandLimit = 300
	// scriptLiveTailLimit is the raw output kept per step. It is larger than
	// ScriptStepOutputLimit so a secret is redacted whole before the tail is
	// cut from the redacted text.
	scriptLiveTailLimit = 64 << 10
	// scriptStepEncodedLimit bounds an encoded event, so its escaped output
	// still fits the control plane's worker event body limit (24 KiB).
	scriptStepEncodedLimit = 20 << 10
	// scriptStepQueueLimit bounds the final events waiting to be sent.
	scriptStepQueueLimit = 256
	// scriptStepAttempts is how often a step's final event is sent before it
	// is dropped; a live update is sent once.
	scriptStepAttempts = 3
	// scriptStepSendTimeout bounds one send.
	scriptStepSendTimeout = 10 * time.Second
	// scriptStepFlushWait is how long a finished run waits for its events to
	// be sent before the worker goes on; the rest are sent in the background.
	scriptStepFlushWait = 2 * time.Second
)

// ErrScriptStepsUnsupported is what a publish function returns when the
// control plane refuses ScriptStepEventType (one from before live logs). The
// run then sends nothing more.
var ErrScriptStepsUnsupported = errors.New("the control plane does not accept " + ScriptStepEventType + " events")

// ScriptStepEvent is the payload of ScriptStepEventType. The newest event for
// a run's step carries everything needed to show it: a live update repeats
// the step's facts with the latest output tail.
type ScriptStepEvent struct {
	Phase string `json:"phase"`
	// RunID is the same for every step of one run of the whole list.
	RunID string `json:"runId"`
	// Step is 1-based, of Total.
	Step  int `json:"step"`
	Total int `json:"total"`
	// Command is the step's command on one line, redacted and bounded, and
	// Repository the repository folder it ran in ("" for the main one).
	Command    string `json:"command"`
	Repository string `json:"repository"`
	Status     string `json:"status"`
	// ExitCode and DurationMS are set once the step finished (not on a
	// running or skipped step). StartedAt (RFC 3339, UTC) is unset for a
	// skipped step and for a Cleanup step whose start was not reported.
	ExitCode   *int   `json:"exitCode,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	DurationMS *int64 `json:"durationMs,omitempty"`
	// Output is the redacted tail of the step's combined output, at most
	// ScriptStepOutputLimit bytes, without terminal escape sequences.
	// OutputTruncated is true when its beginning was dropped.
	Output          string `json:"output"`
	OutputTruncated bool   `json:"outputTruncated"`
	// LogPath is the file in the sandbox with the full output.
	LogPath string `json:"logPath,omitempty"`
	// The rest describe a failed or timed-out step, as SetupEvent does.
	User   string   `json:"user,omitempty"`
	Groups []string `json:"groups,omitempty"`
	Dir    string   `json:"dir,omitempty"`
	Hint   string   `json:"hint,omitempty"`
}

// finish records a finished step's exit status and duration.
func (e *ScriptStepEvent) finish(status string, exitCode int, duration time.Duration) {
	e.Status = status
	e.ExitCode = &exitCode
	milliseconds := duration.Milliseconds()
	e.DurationMS = &milliseconds
}

// ValidScriptStepEvent reports whether a ScriptStepEvent is well formed:
// a known phase and status, a step within its run, and an exit status and
// duration exactly when the step finished.
func ValidScriptStepEvent(event ScriptStepEvent) bool {
	if event.Phase != ScriptPhaseSetup && event.Phase != ScriptPhaseCleanup {
		return false
	}
	if strings.TrimSpace(event.RunID) == "" || len(event.RunID) > 128 ||
		event.Step < 1 || event.Total < event.Step || event.Total > 1000 {
		return false
	}
	if event.DurationMS != nil && *event.DurationMS < 0 {
		return false
	}
	if event.StartedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, event.StartedAt); err != nil {
			return false
		}
	}
	switch event.Status {
	case ScriptStepRunning, ScriptStepSkipped:
		return event.ExitCode == nil && event.DurationMS == nil
	case SetupSucceeded, SetupFailed, SetupTimedOut:
		return event.ExitCode != nil
	}
	return false
}

// SanitizeScriptStepEvent bounds and redacts an event a worker sent, on top
// of the worker's own redaction, so the control plane never stores more.
func SanitizeScriptStepEvent(event ScriptStepEvent) ScriptStepEvent {
	redactor := scriptRedactor{}
	event.Output, event.OutputTruncated = scriptOutputTail(redactor.redact(activity.Excerpt(event.Output, 0)), event.OutputTruncated)
	event.Command = boundScriptCommand(redactor.redact(strings.Join(strings.Fields(event.Command), " ")))
	event.Repository = activity.Truncate(event.Repository, 256)
	event.Dir = activity.Truncate(event.Dir, 1024)
	event.User = activity.Truncate(event.User, 128)
	event.LogPath = activity.Truncate(event.LogPath, 1024)
	event.Hint = activity.Truncate(event.Hint, 512)
	if len(event.Groups) > 32 {
		event.Groups = event.Groups[:32]
	}
	for index, group := range event.Groups {
		event.Groups[index] = activity.Truncate(group, 64)
	}
	return fitScriptStepEvent(event)
}

// secretNamePattern matches the names of environment variables whose values
// are masked in a step's output.
var secretNamePattern = regexp.MustCompile(`(?i)(TOKEN|SECRET|KEY|PASSWORD|PASS|CREDENTIAL|AUTH)`)

// workerTokenPattern matches an AO worker token (see TokenManager.Issue).
var workerTokenPattern = regexp.MustCompile(`\b` + tokenPrefix + `\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

// minSecretValueLength skips values too short to mask without blanking
// ordinary words ("1", "true").
const minSecretValueLength = 6

// scriptRedactor masks credentials in a step's output and command before they
// leave the sandbox: the values of secret-named environment variables and any
// extra secrets (the worker's own token) verbatim, then activity.Redact's
// patterns and AO worker tokens.
type scriptRedactor struct {
	values []string
}

func newScriptRedactor(environment []string, extra []string) scriptRedactor {
	seen := map[string]bool{}
	var values []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if len(value) >= minSecretValueLength && !seen[value] {
			seen[value] = true
			values = append(values, value)
		}
	}
	for _, entry := range environment {
		if name, value, ok := strings.Cut(entry, "="); ok && secretNamePattern.MatchString(name) {
			add(value)
		}
	}
	for _, value := range extra {
		add(value)
	}
	// The longest first, so a secret that contains another is masked whole.
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return scriptRedactor{values: values}
}

func (r scriptRedactor) redact(text string) string {
	for _, value := range r.values {
		text = strings.ReplaceAll(text, value, "[REDACTED]")
	}
	return workerTokenPattern.ReplaceAllString(activity.Redact(text), "[REDACTED]")
}

// scriptStepOutput turns a finished step's raw output tail into what an event
// carries: carriage-return overwrites (progress bars) collapsed to their last
// state, terminal escapes and control characters removed, credentials masked,
// and the tail of at most ScriptStepOutputLimit bytes. headDropped says the
// raw output already lost its beginning; its partial first line is dropped, so
// a secret cut in half there is never published. An unfinished last line (the
// step ended mid-line) is published, but its end is masked when it may be the
// start of a credential (maskTrailingFragment).
func (r scriptRedactor) scriptStepOutput(raw string, headDropped bool) (string, bool) {
	if headDropped {
		if index := strings.IndexByte(raw, '\n'); index >= 0 {
			raw = raw[index+1:]
		}
	}
	text := collapseCarriageReturns(raw)
	// Excerpt with no limit strips escapes and control characters and redacts.
	text = r.redact(activity.Excerpt(text, 0))
	if raw != "" && !strings.HasSuffix(raw, "\n") {
		text = r.maskTrailingFragment(text)
	}
	return scriptOutputTail(text, headDropped)
}

// liveStepOutput is scriptStepOutput for a step still running: only its
// finished lines are published. The line being written may be a credential
// printed part way ("DB_PASSWORD=supersecr"), which no pattern recognizes
// until it is whole, so it waits for its newline or the step's final event.
func (r scriptRedactor) liveStepOutput(raw string, headDropped bool) (string, bool) {
	return r.scriptStepOutput(raw[:strings.LastIndexByte(raw, '\n')+1], headDropped)
}

// minSecretFragmentLength is the shortest end of an output that is masked as
// the start of a known secret value.
const minSecretFragmentLength = 4

// tokenFragmentPattern matches the start of a well-known token shape at the
// end of an output, however short: what a step printed before it ended mid-way
// through a token the full patterns would mask.
var tokenFragmentPattern = regexp.MustCompile(`\b(?:gh[pousr]_|github_pat_|sk-|xox[abprs]?-?|AKIA|eyJ|npm_|AIza|glpat-|SG\.)[A-Za-z0-9_.-]*$`)

// maskTrailingFragment masks the end of an output that may be a credential
// cut short: the beginning of a known secret value (at least
// minSecretFragmentLength bytes of it), or of a well-known token shape.
func (r scriptRedactor) maskTrailingFragment(text string) string {
	for _, value := range r.values {
		for length := min(len(value)-1, len(text)); length >= minSecretFragmentLength; length-- {
			if strings.HasSuffix(text, value[:length]) {
				return text[:len(text)-length] + "[REDACTED]"
			}
		}
	}
	if location := tokenFragmentPattern.FindStringIndex(text); location != nil {
		return text[:location[0]] + "[REDACTED]"
	}
	return text
}

// scriptOutputTail keeps the last ScriptStepOutputLimit bytes of already
// redacted text on a line start when it can, on a rune start otherwise.
func scriptOutputTail(text string, truncated bool) (string, bool) {
	text = strings.TrimRight(strings.ToValidUTF8(text, ""), "\n")
	if len(text) <= ScriptStepOutputLimit {
		return text, truncated
	}
	tail := text[len(text)-ScriptStepOutputLimit:]
	if index := strings.IndexByte(tail, '\n'); index >= 0 && index < len(tail)-1 {
		tail = tail[index+1:]
	}
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		tail = tail[1:]
	}
	return tail, true
}

// collapseCarriageReturns keeps what a terminal would show of each line that
// redraws itself with carriage returns: the text after the last one.
func collapseCarriageReturns(text string) string {
	if !strings.Contains(text, "\r") {
		return text
	}
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		line = strings.TrimRight(line, "\r")
		if cut := strings.LastIndexByte(line, '\r'); cut >= 0 {
			line = line[cut+1:]
		}
		lines[index] = line
	}
	return strings.Join(lines, "\n")
}

// scriptStepCommand is a step's command for its events: as the person wrote
// it (without the cd AO wraps a repository step in), on one line, redacted
// before it is cut to ScriptStepCommandLimit bytes, and its repository folder.
func scriptStepCommand(stored string, redactor scriptRedactor) (command, repository string) {
	repository, command, _ = domain.ParseRepoScriptCommand(stored)
	return boundScriptCommand(redactor.redact(strings.Join(strings.Fields(command), " "))), repository
}

func boundScriptCommand(command string) string {
	if len(command) <= ScriptStepCommandLimit {
		return command
	}
	return strings.ToValidUTF8(command[:ScriptStepCommandLimit-3], "") + "..."
}

// fitScriptStepEvent drops more of the output's beginning while the encoded
// event is over scriptStepEncodedLimit (JSON escapes can grow it severalfold).
func fitScriptStepEvent(event ScriptStepEvent) ScriptStepEvent {
	bare := event
	bare.Output = ""
	overhead, err := json.Marshal(bare)
	if err != nil || len(overhead) >= scriptStepEncodedLimit {
		return event
	}
	for event.Output != "" {
		encoded, err := json.Marshal(event)
		if err != nil || len(encoded) <= scriptStepEncodedLimit {
			return event
		}
		// Keep the share of the output that fits at its current escaping
		// rate, and at least a little less each time.
		keep := len(event.Output) * (scriptStepEncodedLimit - len(overhead)) / (len(encoded) - len(overhead))
		keep = min(keep, len(event.Output)-1)
		output := event.Output[len(event.Output)-max(keep, 0):]
		for len(output) > 0 && !utf8.RuneStart(output[0]) {
			output = output[1:]
		}
		event.Output, event.OutputTruncated = output, true
	}
	return event
}

// liveTail keeps the last limit bytes of a running step's output for its live
// updates. It is written by the step's output copier and read by the watcher.
type liveTail struct {
	mu      sync.Mutex
	limit   int
	data    bytes.Buffer
	dropped bool
	version uint64
}

func (t *liveTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data.Write(p)
	if over := t.data.Len() - t.limit; over > 0 {
		t.data.Next(over)
		t.dropped = true
	}
	if len(p) > 0 {
		t.version++
	}
	return len(p), nil
}

// snapshot returns the kept output, whether its beginning was dropped, and a
// version that changes with every write.
func (t *liveTail) snapshot() (string, bool, uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.data.String(), t.dropped, t.version
}

// scriptStepPublisher sends one run's step events, in order, from its own
// goroutine, so a step never waits on the network. Live updates coalesce:
// only the newest waits to be sent, and one that fails is dropped. A step's
// final event is sent up to scriptStepAttempts times. Once the control plane
// refuses the event type, the run sends nothing more.
type scriptStepPublisher struct {
	ctx        context.Context
	publish    func(context.Context, ScriptStepEvent) error
	retryDelay time.Duration

	mu          sync.Mutex
	live        *ScriptStepEvent
	finals      []ScriptStepEvent
	closed      bool
	unsupported bool
	wake        chan struct{}
	done        chan struct{}
}

func newScriptStepPublisher(
	ctx context.Context,
	publish func(context.Context, ScriptStepEvent) error,
	retryDelay time.Duration,
) *scriptStepPublisher {
	publisher := &scriptStepPublisher{
		ctx: ctx, publish: publish, retryDelay: retryDelay,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	go publisher.run()
	return publisher
}

// update replaces any live update still waiting to be sent.
func (p *scriptStepPublisher) update(event ScriptStepEvent) {
	p.mu.Lock()
	if !p.closed && !p.unsupported {
		p.live = &event
	}
	p.mu.Unlock()
	p.signal()
}

// final queues a step's final event; a live update for the same step still
// waiting is dropped, as the final one supersedes it.
func (p *scriptStepPublisher) final(event ScriptStepEvent) {
	p.mu.Lock()
	if !p.closed && !p.unsupported {
		if p.live != nil && p.live.Step == event.Step {
			p.live = nil
		}
		if len(p.finals) < scriptStepQueueLimit {
			p.finals = append(p.finals, event)
		}
	}
	p.mu.Unlock()
	p.signal()
}

// close stops taking events and waits up to wait for the queued ones to be
// sent; any left are still sent in the background.
func (p *scriptStepPublisher) close(wait time.Duration) {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.signal()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
	}
}

func (p *scriptStepPublisher) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *scriptStepPublisher) run() {
	defer close(p.done)
	for {
		p.mu.Lock()
		if p.unsupported || p.ctx.Err() != nil {
			p.live, p.finals = nil, nil
			p.mu.Unlock()
			return
		}
		// Final events are older than the pending live update (which is
		// always for the step running now), so they go first.
		if len(p.finals) > 0 {
			event := p.finals[0]
			p.finals = p.finals[1:]
			p.mu.Unlock()
			p.send(event, scriptStepAttempts)
			continue
		}
		if p.live != nil {
			event := *p.live
			p.live = nil
			p.mu.Unlock()
			p.send(event, 1)
			continue
		}
		if p.closed {
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		select {
		case <-p.wake:
		case <-p.ctx.Done():
		}
	}
}

func (p *scriptStepPublisher) send(event ScriptStepEvent, attempts int) {
	for attempt := 1; attempt <= attempts; attempt++ {
		ctx, cancel := context.WithTimeout(p.ctx, scriptStepSendTimeout)
		err := p.publish(ctx, event)
		cancel()
		if err == nil {
			return
		}
		if errors.Is(err, ErrScriptStepsUnsupported) {
			p.mu.Lock()
			p.unsupported = true
			p.mu.Unlock()
			return
		}
		if attempt < attempts {
			select {
			case <-p.ctx.Done():
				return
			case <-time.After(p.retryDelay * time.Duration(attempt)):
			}
		}
	}
}

// liveUpdateInterval paces a running step's updates: about every second, and
// every five seconds once the step has run two minutes, so a long, chatty
// step does not fill the session's event stream.
func liveUpdateInterval(elapsed time.Duration) time.Duration {
	if elapsed >= 2*time.Minute {
		return 5 * time.Second
	}
	return time.Second
}

// watchStepOutput sends a live update whenever the step finished another line
// of output, paced by interval, until the returned stop is called.
func watchStepOutput(
	tail *liveTail,
	interval func(elapsed time.Duration) time.Duration,
	emit func(output string, truncated bool),
	redactor scriptRedactor,
) (stop func()) {
	quit, finished := make(chan struct{}), make(chan struct{})
	started := time.Now()
	go func() {
		defer close(finished)
		var sent uint64
		lastOutput, lastTruncated := "", false
		for {
			timer := time.NewTimer(interval(time.Since(started)))
			select {
			case <-quit:
				timer.Stop()
				return
			case <-timer.C:
			}
			raw, dropped, version := tail.snapshot()
			if version == sent {
				continue
			}
			sent = version
			output, truncated := redactor.liveStepOutput(raw, dropped)
			if output == "" || (output == lastOutput && truncated == lastTruncated) {
				// Nothing new is finished: the step is still writing a line.
				continue
			}
			lastOutput, lastTruncated = output, truncated
			emit(output, truncated)
		}
	}()
	return func() {
		close(quit)
		<-finished
	}
}

// scriptTimestamp formats a step's start time for its events.
func scriptTimestamp(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// setupStepEvents reports one Setup run's steps through a SetupRunner's
// Publish. Without Publish every method does nothing.
type setupStepEvents struct {
	publisher *scriptStepPublisher
	redactor  scriptRedactor
	interval  func(time.Duration) time.Duration
	runID     string
	total     int

	current   ScriptStepEvent
	started   time.Time
	live      *liveTail
	stopWatch func()
}

func (r SetupRunner) startStepEvents(ctx context.Context, total int) *setupStepEvents {
	if r.Publish == nil {
		return &setupStepEvents{}
	}
	var secrets []string
	if r.Secrets != nil {
		secrets = r.Secrets()
	}
	interval := liveUpdateInterval
	if r.UpdateInterval > 0 {
		interval = func(time.Duration) time.Duration { return r.UpdateInterval }
	}
	return &setupStepEvents{
		publisher: newScriptStepPublisher(ctx, r.Publish, time.Second),
		redactor:  newScriptRedactor(os.Environ(), secrets),
		interval:  interval,
		runID:     uuid.NewString(),
		total:     total,
	}
}

// start reports a step as running and returns the writer that feeds its live
// updates (nil without Publish).
func (s *setupStepEvents) start(step int, command, logPath string) io.Writer {
	if s.publisher == nil {
		return nil
	}
	s.stopWatching()
	s.started = time.Now()
	s.current = ScriptStepEvent{
		Phase: ScriptPhaseSetup, RunID: s.runID, Step: step, Total: s.total,
		Status: ScriptStepRunning, StartedAt: scriptTimestamp(s.started), LogPath: logPath,
	}
	s.current.Command, s.current.Repository = scriptStepCommand(command, s.redactor)
	s.publisher.update(fitScriptStepEvent(s.current))
	s.live = &liveTail{limit: scriptLiveTailLimit}
	running := s.current
	s.stopWatch = watchStepOutput(s.live, s.interval, func(output string, truncated bool) {
		update := running
		update.Output, update.OutputTruncated = output, truncated
		s.publisher.update(fitScriptStepEvent(update))
	}, s.redactor)
	return s.live
}

// finish reports the step start began with its outcome and final output.
func (s *setupStepEvents) finish(outcome SetupEvent) {
	if s.publisher == nil {
		return
	}
	s.stopWatching()
	event := s.current
	event.finish(outcome.Status, outcome.ExitCode, time.Since(s.started))
	raw, dropped, _ := s.live.snapshot()
	event.Output, event.OutputTruncated = s.redactor.scriptStepOutput(raw, dropped)
	if event.Output == "" && outcome.Status != SetupSucceeded {
		// A step that could not start says why instead.
		event.Output, _ = s.redactor.scriptStepOutput(outcome.Output, false)
	}
	if event.LogPath != "" {
		if _, err := os.Stat(event.LogPath); err != nil {
			event.LogPath = ""
		}
	}
	if outcome.Status != SetupSucceeded {
		event.User, event.Groups, event.Dir, event.Hint = outcome.User, outcome.Groups, outcome.Dir, outcome.Hint
	}
	s.publisher.final(fitScriptStepEvent(event))
}

// skip reports the steps after a failure, which never ran.
func (s *setupStepEvents) skip(commands []string) {
	if s.publisher == nil {
		return
	}
	for index, command := range commands {
		event := ScriptStepEvent{
			Phase: ScriptPhaseSetup, RunID: s.runID, Step: s.current.Step + 1 + index, Total: s.total,
			Status: ScriptStepSkipped,
		}
		event.Command, event.Repository = scriptStepCommand(command, s.redactor)
		s.publisher.final(event)
	}
}

func (s *setupStepEvents) close() {
	if s.publisher == nil {
		return
	}
	s.stopWatching()
	s.publisher.close(scriptStepFlushWait)
}

func (s *setupStepEvents) stopWatching() {
	if s.stopWatch != nil {
		s.stopWatch()
		s.stopWatch = nil
	}
}
