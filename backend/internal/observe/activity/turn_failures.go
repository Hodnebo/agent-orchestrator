package activity

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// DefaultTurnFailureTick is how often active terminal transcripts are checked
// for a turn that ended on an error. A transcript is re-read only when it
// changed, so the short tick costs a stat per active session.
const DefaultTurnFailureTick = 2 * time.Second

// TurnFailureEvent is the activity event a transcript-proven failed turn
// reports, matching Claude Code's StopFailure hook.
const TurnFailureEvent = "stop-failure"

// turnFailureHarnesses are the harnesses whose terminal turn can end on a
// provider error without firing a hook. Codex runs its Stop hook only after a
// successful turn; Claude Code reports such a turn through StopFailure.
var turnFailureHarnesses = []domain.AgentHarness{domain.HarnessCodex}

// turnFailureSource lists the sessions worth checking: live terminal sessions
// of one harness that are in a turn and have a native transcript path.
type turnFailureSource interface {
	ListTurnFailureCandidates(ctx context.Context, harness domain.AgentHarness) ([]domain.TurnFailureCandidate, error)
}

// TurnFailureConfig controls failed-turn polling.
type TurnFailureConfig struct {
	Tick   time.Duration
	Logger *slog.Logger
}

// TurnFailureObserver marks a terminal session idle when its harness's
// transcript proves the current turn ended on an error. Some harnesses (Codex)
// fire no hook at all for such a turn, so the hook-driven activity stayed
// active: the session read "working" and Finish-then-switch waited for a stop
// that never came. The 30 second stale-activity observer only recovers that
// after two minutes, and only when the screen positively shows an idle prompt.
type TurnFailureObserver struct {
	sessions turnFailureSource
	sink     activitySink
	agents   ports.AgentResolver
	tick     time.Duration
	logger   *slog.Logger
	watches  map[domain.SessionID]*turnFailureWatch
}

// turnFailureWatch caches one session's transcript read. The transcript is
// read again only when its path, size or modification time changes.
type turnFailureWatch struct {
	path    string
	size    int64
	modTime time.Time
	failure ports.TurnFailure
	failed  bool
	// sentRevision and sentAt record the last report, so a failure the
	// lifecycle suppressed (a running subagent keeps the parent active) is
	// not re-sent every tick. A newer session revision retries it.
	sent         bool
	sentRevision int64
	sentAt       time.Time
}

// NewTurnFailureObserver builds a failed-turn observer.
func NewTurnFailureObserver(sessions turnFailureSource, sink activitySink, agents ports.AgentResolver, cfg TurnFailureConfig) *TurnFailureObserver {
	o := &TurnFailureObserver{
		sessions: sessions,
		sink:     sink,
		agents:   agents,
		tick:     cfg.Tick,
		logger:   cfg.Logger,
		watches:  make(map[domain.SessionID]*turnFailureWatch),
	}
	if o.tick <= 0 {
		o.tick = DefaultTurnFailureTick
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return o
}

// Start runs polling until the context is canceled.
func (o *TurnFailureObserver) Start(ctx context.Context) <-chan struct{} {
	return observe.StartPollLoop(ctx, o.tick, o.Poll, o.logger, "turn failure observer")
}

// Poll performs one pass. It is not safe for concurrent use. The store
// returns only live terminal sessions in a turn (active, or waiting_input:
// Codex reports an approval prompt as waiting_input and fires no hook when the
// approved turn continues, so a turn that then fails would otherwise stay
// "needs input"). Chat sessions report turn ends through their controller, and
// an idle session has nothing to recover.
func (o *TurnFailureObserver) Poll(ctx context.Context) error {
	watched := make(map[domain.SessionID]bool, len(o.watches))
	for _, harness := range turnFailureHarnesses {
		detector, ok := o.detector(harness)
		if !ok {
			continue
		}
		sessions, err := o.sessions.ListTurnFailureCandidates(ctx, harness)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			watched[session.ID] = true
			o.reconcile(ctx, session, detector)
		}
	}
	for id := range o.watches {
		if !watched[id] {
			delete(o.watches, id)
		}
	}
	return nil
}

// detector returns the harness's failed-turn capability.
func (o *TurnFailureObserver) detector(harness domain.AgentHarness) (ports.TranscriptTurnFailureDetector, bool) {
	if o.agents == nil {
		return nil, false
	}
	agent, ok := o.agents.Agent(harness)
	if !ok {
		return nil, false
	}
	detector, ok := agent.(ports.TranscriptTurnFailureDetector)
	return detector, ok
}

func (o *TurnFailureObserver) reconcile(ctx context.Context, session domain.TurnFailureCandidate, detector ports.TranscriptTurnFailureDetector) {
	path := session.NativeTranscriptPath
	info, err := os.Stat(path)
	if err != nil {
		delete(o.watches, session.ID)
		return
	}
	watch := o.watches[session.ID]
	if watch == nil || watch.path != path || watch.size != info.Size() || !watch.modTime.Equal(info.ModTime()) {
		failure, failed, err := detector.LastTurnFailure(ctx, path)
		if err != nil {
			o.logger.Debug("turn failure observer: transcript unreadable", "session", session.ID, "err", err)
			return
		}
		if watch == nil || watch.path != path {
			watch = &turnFailureWatch{}
			o.watches[session.ID] = watch
		}
		watch.path, watch.size, watch.modTime = path, info.Size(), info.ModTime()
		watch.failure, watch.failed = failure, failed
	}
	if !watch.failed {
		return
	}
	// The current state was entered by a hook (a prompt, or an approval
	// request) at LastActivityAt. A failure recorded before it belongs to an
	// earlier turn: replaying it would idle a turn that is still running.
	if session.Activity.LastActivityAt.IsZero() || watch.failure.At.Before(session.Activity.LastActivityAt) {
		return
	}
	if watch.sent && watch.sentRevision == session.Revision && watch.sentAt.Equal(watch.failure.At) {
		return
	}
	revision := session.Revision
	err = o.sink.ApplyActivitySignal(ctx, session.ID, ports.ActivitySignal{
		Valid: true,
		State: domain.ActivityIdle,
		// Dated when the turn ended, not when this poll saw it: terminal input
		// typed after the failure is newer than this idle, so a Chat switch
		// still waits for the turn that input started.
		Timestamp:        watch.failure.At,
		ExpectedRevision: &revision,
		ExpectedHarness:  session.Harness,
		Event:            TurnFailureEvent,
		LaunchID:         session.RuntimeLaunchID,
	})
	if err != nil {
		o.logger.Error("turn failure observer: reconciliation failed", "session", session.ID, "err", err)
		return
	}
	watch.sent, watch.sentRevision, watch.sentAt = true, revision, watch.failure.At
	o.logger.Info("agent turn ended on an error; marking the session idle",
		"session", session.ID, "harness", session.Harness, "error_message", watch.failure.Message)
}
