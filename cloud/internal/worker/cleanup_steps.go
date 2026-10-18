package worker

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A Cleanup run's step events. The control plane runs the Cleanup hook
// remotely and only gets its output, so the commands script prints a marker
// line around each command (CleanupCommandsScript) and CleanupStepEvents
// splits the output back into steps:
//
//	@@ao-step-<nonce> log <path>
//	@@ao-step-<nonce> <n> <total> start <epochMs> <repository or /> <command>
//	@@ao-step-<nonce> <n> <total> end <exitCode> <durationMs>
//
// The nonce is random per script, so a command that prints something that
// looks like a marker is read as output.

const (
	scriptMarkerPrefix = "@@ao-step-"
	// scriptMarkerMainRepository stands for the main repository in a start
	// marker; a repository folder never contains a slash.
	scriptMarkerMainRepository = "/"
)

var (
	scriptMarkerPattern = regexp.MustCompile(scriptMarkerPrefix + `([0-9a-f]{16}) `)
	cleanupRanAsPattern = regexp.MustCompile(`(?m)^ran as (\S+) \(groups: ([^)]*)\) in (.+)$`)
	cleanupLogPattern   = regexp.MustCompile(`(?m)^full cleanup output: (\S.*)$`)
)

// NewScriptMarkerNonce returns a random nonce for a commands script's markers.
func NewScriptMarkerNonce() string {
	var random [8]byte
	_, _ = rand.Read(random[:])
	return hex.EncodeToString(random[:])
}

// CleanupRun describes one run of the Cleanup hook for CleanupStepEvents.
type CleanupRun struct {
	RunID string
	// Nonce is the marker nonce of the commands script the control plane
	// installed for this run, and Commands that script's commands; both are
	// empty when the hook ran the commands the worker installed, whose nonce
	// is then read from the output.
	Nonce    string
	Commands []string
	// ExitCode is the hook's exit status, and StartedAt and Duration when the
	// run started and how long it took.
	ExitCode  int
	StartedAt time.Time
	Duration  time.Duration
}

type cleanupStep struct {
	event       ScriptStepEvent
	startedMs   int64
	output      strings.Builder
	headDropped bool
	finished    bool
}

// CleanupStepEvents splits a Cleanup hook's output into one final event per
// step of the run, ordered by step: the steps that ran with their redacted
// output tails, and the ones a failure skipped. Output that lost its
// beginning (the hook prints only a tail) still reports every step: one whose
// start is no longer shown succeeded, as a later one ran. A hook without
// markers (a commands script from before them) is reported as a single step
// with the run's exit status and output. Nothing is reported for a silent,
// successful run without markers (no Cleanup commands).
func CleanupStepEvents(output string, run CleanupRun) []ScriptStepEvent {
	nonce, own := run.Nonce, run.Nonce != ""
	output = strings.ToValidUTF8(output, "")
	if nonce == "" || !strings.Contains(output, scriptMarkerPrefix+nonce+" ") {
		match := scriptMarkerPattern.FindStringSubmatch(output)
		if match == nil {
			return cleanupEventsWithoutMarkers(output, run)
		}
		nonce, own = match[1], match[1] == run.Nonce
	}
	prefix := scriptMarkerPrefix + nonce + " "
	redactor := scriptRedactor{}
	steps := map[int]*cleanupStep{}
	var current *cleanupStep
	var preamble, trailer strings.Builder
	logPath, total, lastFinished := "", 0, 0
	sawLog := false
	write := func(text string) {
		switch {
		case current != nil:
			current.output.WriteString(text + "\n")
		case lastFinished == 0:
			preamble.WriteString(text + "\n")
		default:
			trailer.WriteString(text + "\n")
		}
	}
	finish := func(step *cleanupStep, exitCode int, duration time.Duration) {
		status := SetupSucceeded
		switch {
		case (exitCode == 124 || exitCode == 137) && duration >= ProjectScriptStepTimeout-time.Second:
			status, exitCode = SetupTimedOut, -1
		case exitCode != 0:
			status = SetupFailed
		}
		step.event.finish(status, exitCode, duration)
		step.finished = true
		lastFinished = step.event.Step
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		index := strings.Index(line, prefix)
		if index < 0 {
			write(line)
			continue
		}
		if before := line[:index]; before != "" {
			write(before)
		}
		marker := line[index+len(prefix):]
		if path, ok := strings.CutPrefix(marker, "log "); ok {
			logPath, sawLog = strings.TrimSpace(path), true
			continue
		}
		fields := strings.SplitN(marker, " ", 4)
		if len(fields) < 3 {
			write(line)
			continue
		}
		step, stepErr := strconv.Atoi(fields[0])
		count, countErr := strconv.Atoi(fields[1])
		if stepErr != nil || countErr != nil || step < 1 || count < step || count > 1000 {
			write(line)
			continue
		}
		rest := ""
		if len(fields) == 4 {
			rest = fields[3]
		}
		total = max(total, count)
		switch fields[2] {
		case "start":
			if current != nil && !current.finished {
				// A start without the previous step's end: that step stopped
				// the script without reporting.
				finish(current, -1, 0)
			}
			parts := strings.SplitN(rest, " ", 3)
			current = &cleanupStep{event: ScriptStepEvent{Step: step}}
			if milliseconds, err := strconv.ParseInt(parts[0], 10, 64); err == nil && milliseconds > 0 {
				current.startedMs = milliseconds
			}
			if len(parts) > 1 && parts[1] != scriptMarkerMainRepository {
				current.event.Repository = parts[1]
			}
			if len(parts) > 2 {
				current.event.Command = boundScriptCommand(redactor.redact(parts[2]))
			}
			steps[step] = current
		case "end":
			parts := strings.Fields(rest)
			exitCode, duration := -1, time.Duration(0)
			if len(parts) > 0 {
				if code, err := strconv.Atoi(parts[0]); err == nil {
					exitCode = code
				}
			}
			if len(parts) > 1 {
				if milliseconds, err := strconv.ParseInt(parts[1], 10, 64); err == nil && milliseconds >= 0 {
					duration = time.Duration(milliseconds) * time.Millisecond
				}
			}
			if current == nil || current.event.Step != step || current.finished {
				// Its start is no longer in the output: what came before
				// this marker is the end of its output.
				current = &cleanupStep{event: ScriptStepEvent{Step: step}, headDropped: true}
				current.output.WriteString(preamble.String())
				preamble.Reset()
				steps[step] = current
			}
			finish(current, exitCode, duration)
			current = nil
		default:
			write(line)
		}
	}
	if total == 0 {
		return cleanupEventsWithoutMarkers(output, run)
	}
	if current != nil && !current.finished {
		// The hook stopped during this step: its budget ran out, or it failed
		// without reporting.
		ended := run.StartedAt.Add(run.Duration)
		duration := run.Duration
		if current.startedMs > 0 {
			duration = max(0, ended.Sub(time.UnixMilli(current.startedMs)))
		}
		switch code := run.ExitCode; code {
		case 124, 137:
			current.event.finish(SetupTimedOut, -1, duration)
		case 0:
			current.event.finish(SetupFailed, -1, duration)
		default:
			current.event.finish(SetupFailed, code, duration)
		}
		current.finished = true
		lastFinished = current.event.Step
	}
	if logPath == "" {
		if match := cleanupLogPattern.FindStringSubmatch(trailer.String()); match != nil {
			logPath = strings.TrimSpace(match[1])
		}
	}
	firstSeen := total + 1
	for step := range steps {
		firstSeen = min(firstSeen, step)
	}
	events := make([]ScriptStepEvent, 0, total)
	for number := 1; number <= total; number++ {
		event := ScriptStepEvent{Step: number}
		step, seen := steps[number]
		switch {
		case seen:
			event = step.event
			if step.startedMs > 0 {
				event.StartedAt = scriptTimestamp(time.UnixMilli(step.startedMs))
			}
			event.Output, event.OutputTruncated = redactor.scriptStepOutput(step.output.String(), step.headDropped)
			if event.Status != SetupSucceeded {
				cleanupFailureContext(&event, event.Output)
			}
		case number < firstSeen:
			// Before the first step the output still shows; a later step ran,
			// so this one succeeded.
			zero := 0
			event.Status, event.ExitCode, event.OutputTruncated = SetupSucceeded, &zero, true
		default:
			// After the step that stopped the run (or past what the output
			// shows): never ran.
			event.Status = ScriptStepSkipped
		}
		if event.Command == "" && own && len(run.Commands) == total {
			event.Command, event.Repository = scriptStepCommand(run.Commands[number-1], redactor)
		}
		event.Phase, event.RunID, event.Total = ScriptPhaseCleanup, run.RunID, total
		if event.Status != ScriptStepSkipped && (sawLog || logPath != "") {
			event.LogPath = logPath
		}
		events = append(events, fitScriptStepEvent(event))
	}
	return events
}

// cleanupEventsWithoutMarkers reports a run whose output has no markers as
// one step: the run's exit status and output tail.
func cleanupEventsWithoutMarkers(output string, run CleanupRun) []ScriptStepEvent {
	if run.ExitCode == 0 && strings.TrimSpace(output) == "" {
		return nil
	}
	redactor := scriptRedactor{}
	event := ScriptStepEvent{
		Phase: ScriptPhaseCleanup, RunID: run.RunID, Step: 1, Total: 1,
		StartedAt: scriptTimestamp(run.StartedAt),
	}
	switch {
	case run.ExitCode == 0:
		event.finish(SetupSucceeded, 0, run.Duration)
	case run.ExitCode == 124 && run.Duration >= ProjectScriptStepTimeout:
		event.finish(SetupTimedOut, -1, run.Duration)
	default:
		event.finish(SetupFailed, run.ExitCode, run.Duration)
	}
	event.Output, event.OutputTruncated = redactor.scriptStepOutput(output, false)
	if match := cleanupLogPattern.FindStringSubmatch(event.Output); match != nil {
		event.LogPath = strings.TrimSpace(match[1])
	}
	if event.Status != SetupSucceeded {
		cleanupFailureContext(&event, event.Output)
	}
	return []ScriptStepEvent{fitScriptStepEvent(event)}
}

// cleanupFailureContext fills a failed Cleanup step's user, groups and
// directory from the "ran as" line the commands script prints, and its hint.
func cleanupFailureContext(event *ScriptStepEvent, output string) {
	if match := cleanupRanAsPattern.FindStringSubmatch(output); match != nil {
		event.User, event.Groups, event.Dir = match[1], strings.Fields(match[2]), strings.TrimSpace(match[3])
	}
	exitCode := -1
	if event.ExitCode != nil {
		exitCode = *event.ExitCode
	}
	event.Hint = ScriptFailureHint(output, exitCode, event.User)
}
