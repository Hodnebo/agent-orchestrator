package litmus

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// EnvRun is set in a testing session's environment to the id of the run it owns.
const EnvRun = "AO_LITMUS_RUN"

// SweepInterval is how often the daemon looks for runs past their deadline.
const SweepInterval = 15 * time.Second

// Testers may use the owner's GitHub login in repos they create, following the
// skill's rules. Litmus posts results on the PR under test. The deny list keeps
// Claude Code away from commands that affect the owner's AO and earlier verdicts.
// Prefix rules stop mistakes, but do not match commands inside `sh -c`.
// Codex has no per-command rules in AO's launch and follows the skill alone.
var (
	allowedTools = []string{"Bash", "Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Grep", "Glob", "LS",
		"WebFetch", "WebSearch", "Agent", "Task", "TodoWrite"}
	disallowedTools = []string{"Bash(ao testing start:*)", "Bash(ao testing post:*)", "Bash(ao testing discard:*)", "Bash(ao testing prune:*)", "Bash(ao testing rerun:*)",
		"Bash(ao spawn:*)", "Bash(ao session:*)", "Bash(ao send:*)", "Bash(ao pr:*)",
		"Bash(ao review:*)", "Bash(ao project:*)", "Bash(ao orchestrator:*)", "Bash(ao automation:*)", "Bash(ao cue:*)",
		"Bash(ao stop:*)", "Bash(ao start:*)", "Bash(ao import:*)", "Bash(ao remote-host:*)", "Bash(ao browser:*)",
		"Bash(ao preview:*)", "Bash(ao render:*)", "Bash(ao dev:*)",
		"Read(**/litmus/runs/run_*/comment.md)", "Read(**/litmus/runs/run_*/verdict.md)"}
)

// TesterModel and TesterEffort are what a tester runs on when the project's worker settings name no model or effort.
// TesterModel is a Claude alias: a Codex tester without a model runs on Codex's own default.
const (
	TesterModel  = "sonnet"
	TesterEffort = "high"
)

// RefuseTester refuses a verb only the user may run (start, post, discard, prune, rerun) when the request names a tester's own session:
// one that has a run or a spawn marker. The CLI sends AO_SESSION_ID, so this holds for any harness, whatever its tool
// rules. A request that names no session is the user's.
func (s *Service) RefuseTester(sessionID, verb string) error {
	id := RunID(sessionID)
	if id == "" {
		return nil
	}
	_, spawnedBy := s.store.spawnedSession(sessionID)
	if _, gerr := s.store.Get(id); gerr == nil || spawnedBy {
		return apierr.Forbidden("TESTING_TESTER_FORBIDDEN", fmt.Sprintf("`ao testing %s` is for the user: a Litmus tester cannot start runs, post comments or prune; end the run with `ao testing submit`", verb))
	}
	return nil
}

// ToolPolicy returns copies of the allow and deny lists for a testing session.
func ToolPolicy() (allowed, disallowed []string) {
	return slices.Clone(allowedTools), slices.Clone(disallowedTools)
}

// RunID is the id of the one run a session owns.
func RunID(sessionID string) string {
	id, _ := runID(sessionID)
	return id
}

// RunDir is a testing session's working directory: the directory its run keeps
// context-pack.md, checks.json, receipts/ and verdict.md in.
func RunDir(dataDir, sessionID string) string {
	return filepath.Join(NewStore(dataDir).root, RunID(sessionID))
}

// IsRunDir reports whether path is a run directory under dataDir. Session
// workspaces are the only paths that can be one, so it identifies testing sessions.
func IsRunDir(dataDir, path string) bool {
	path = filepath.Clean(path)
	return path != "." && filepath.Dir(path) == NewStore(dataDir).root && strings.HasPrefix(filepath.Base(path), "run_")
}

// reapedFile marks a run whose session has been killed, so a daemon restart
// neither forgets an expiry nor repeats it.
const reapedFile = "reaped"

// SweepExpired closes stopped runs and runs past their deadline. A live run with checks, or a stopped run whose accepted
// submit is saved, is finished with what exists, as a submit would (see closeAtDeadline). Any other run flips to expired
// (so every verb refuses), the session is stopped through kill,
// the engines close and the legs are cleaned. A run is marked reaped only once all of that has
// worked and no long verb is still in flight, so anything else is retried on
// the next tick. State is on disk, so a restarted daemon picks up runs the
// previous one never got to.
func (s *Service) SweepExpired(ctx context.Context, kill func(ctx context.Context, sessionID string) error, log *slog.Logger) {
	runs, err := s.store.List()
	if err != nil {
		log.Warn("litmus: could not list runs for the deadline sweep", "error", err)
		return
	}
	if s.expireUnstarted(ctx, log) {
		if runs, err = s.store.List(); err != nil {
			log.Warn("litmus: could not list runs for the deadline sweep", "error", err)
			return
		}
	}
	for _, run := range runs {
		if run.State == RunSubmitted || run.State == RunCleaned || run.State == RunAwaitingApproval || s.reaped(run.ID) {
			continue
		}
		stopped := false
		if s.activity != nil {
			state, _, found := s.activity(ctx, run.SessionID)
			stopped = found && state == "exited" && run.State != RunExpired && !s.inFlight(run.ID)
		}
		acceptedSubmit := !run.TestingEndedAt.IsZero()
		if !stopped && run.State != RunExpired && !s.clock().After(run.Deadline) {
			continue
		}
		// A durable testing end means Submit passed validation. Resume finalization
		// even when the tester's session exited before the run deadline.
		if (!stopped || acceptedSubmit) && s.closeAtDeadline(ctx, run, log) {
			continue
		}
		expire := false
		if _, err := s.update(run.ID, func(r *Run) {
			// Acceptance and expiry share the service mutex. A submit that
			// began after the sweep's snapshot must keep its result.
			if _, submitting := s.busy[r.ID+" submit"]; submitting {
				return
			}
			expire = !r.State.Closed() || r.State == RunExpired
			if !r.State.Closed() {
				r.State = RunExpired
				if stopped {
					r.Error = ReasonStopped
				}
			}
		}); err != nil {
			log.Warn("litmus: could not expire a run", "run", run.ID, "error", err)
			continue
		}
		if !expire {
			continue
		}
		done := true
		if err := kill(ctx, run.SessionID); err != nil {
			log.Warn("litmus: could not stop the session of an expired run", "run", run.ID, "error", err)
			done = false
		}
		if s.inFlight(run.ID) { // a launch or prepare would write its leg after the cleanup below
			done = false
		} else if _, err := s.Cleanup(ctx, run.SessionID); err != nil {
			log.Warn("litmus: cleanup of an expired run failed", "run", run.ID, "error", err)
			done = false
		}
		if !done {
			continue
		}
		if err := s.store.WriteFile(run.ID, reapedFile, []byte(s.clock().UTC().Format(time.RFC3339))); err != nil {
			log.Warn("litmus: could not mark a run reaped", "run", run.ID, "error", err)
			continue
		}
		log.Info("litmus: run expired, session stopped", "run", run.ID, "reason", run.Error)
	}
}

// closeAtDeadline finishes a run that ran out of time with checks: every check without a result becomes skipped
// ("ran out of time"), the verdict is derived from what exists, verdict.md and comment.md are written, and the run
// follows its comment mode as a submit does. It reports whether the sweep is done with the run for this tick: it was
// closed, or finalization is unfinished and the next tick tries again. False sends the run down the plain expiry.
// A failed accepted submit keeps its saved checks and testing end for the existing sweep to resume. A run with no
// intake or a prior stopped reason has nothing to salvage.
func (s *Service) closeAtDeadline(ctx context.Context, run Run, log *slog.Logger) bool {
	acceptedSubmit := !run.TestingEndedAt.IsZero()
	s.mu.Lock()
	_, submitting := s.busy[run.ID+" submit"]
	s.mu.Unlock()
	if submitting {
		return true
	}
	var checks []Check
	var err error
	if run.submission != nil {
		checks = run.submission.Checks
	} else {
		checks, err = s.store.Checks(run.ID)
	}
	if err != nil {
		log.Warn("litmus: could not read checks at the deadline", "run", run.ID, "error", err)
		return acceptedSubmit
	}
	if run.submission == nil && (len(checks) == 0 || run.Error == ReasonNoIntake || run.Error == ReasonStopped) {
		return false
	}
	if s.inFlight(run.ID) {
		return true
	}
	s.mu.Lock()
	done, err := s.markBusy(run.ID, "submit")
	if err != nil {
		s.mu.Unlock()
		return true // a submit or launch began in between
	}
	defer done()
	run, err = s.store.Get(run.ID)
	if err != nil {
		s.mu.Unlock()
		log.Warn("litmus: could not read a run at its deadline", "error", err)
		return acceptedSubmit
	}
	if (run.State.Closed() && run.State != RunExpired) || s.reaped(run.ID) {
		s.mu.Unlock()
		return true
	}
	if run.submission != nil {
		s.mu.Unlock()
		in := run.submission
		if _, err := s.closeRunWithLinkMode(ctx, run, in.Checks, in.Summary, in.BlockedReason, in.SkipBaseReason, in.Deadline); err != nil {
			log.Warn("litmus: could not resume an accepted submit", "run", run.ID, "error", err)
		}
		return true
	}
	checks, err = s.store.Checks(run.ID)
	if err != nil {
		s.mu.Unlock()
		log.Warn("litmus: could not read checks at the deadline", "run", run.ID, "error", err)
		return acceptedSubmit
	}
	if len(checks) == 0 || run.Error == ReasonNoIntake || run.Error == ReasonStopped {
		s.mu.Unlock()
		return false
	}
	for i := range checks {
		if !slices.Contains(outcomes, checks[i].State) {
			checks[i].State, checks[i].Note = CheckSkipped, ReasonOutOfTime
		}
	}
	err = s.store.SaveChecks(run.ID, checks)
	s.mu.Unlock()
	if err != nil {
		log.Warn("litmus: could not save the checks of a run at its deadline", "run", run.ID, "error", err)
		return acceptedSubmit
	}
	why := "" // the head checks the tester reached but did not repeat on the base say so
	if _, notRun := baseCoverage(checks); len(notRun) > 0 {
		why = ReasonOutOfTime
	}
	if _, err := s.closeRunAtDeadline(ctx, run, skipOnBase(checks, why), "", "", why); err != nil {
		log.Warn("litmus: could not close a run at its deadline", "run", run.ID, "error", err)
		return acceptedSubmit
	}
	log.Info("litmus: run closed at its deadline", "run", run.ID)
	return true
}

// expireUnstarted expires the run of every tester that has not completed intake (no run.json, or one still in intake
// with no checks) once its session has been exited or idle for IdleBudget, or SpawnBudget has passed since it was
// spawned. The run keeps the reason, and the loop in SweepExpired then stops the session. It reports whether any run
// changed.
func (s *Service) expireUnstarted(ctx context.Context, log *slog.Logger) (changed bool) {
	spawns, err := s.store.spawnedSessions()
	if err != nil {
		log.Warn("litmus: could not list started testers for the deadline sweep", "error", err)
		return false
	}
	for sid, sp := range spawns {
		id, err := runID(sid)
		if err != nil {
			continue
		}
		run, err := s.store.Get(id)
		missing := errors.Is(err, fs.ErrNotExist)
		if err != nil && !missing {
			continue // unreadable: a half-written file is not a verdict
		}
		if !missing {
			checks, cerr := s.store.Checks(id)
			if cerr != nil || run.State != RunIntake || len(checks) > 0 {
				if cerr == nil {
					s.store.clearSpawned(sid) // intake is done; the run's own deadline rules from here
				}
				continue
			}
		}
		if s.inFlight(id) || !s.gaveUp(ctx, sid, sp) { // an intake or prepare in flight is a tester at work
			continue
		}
		s.mu.Lock()
		cur, err := s.store.Get(id)
		if missing = errors.Is(err, fs.ErrNotExist); missing {
			cur = Run{ID: id, SessionID: sid, ProjectID: s.store.sessionProject(sid), Repo: sp.Repo, PRNumber: sp.PR, CreatedAt: sp.At, Deadline: sp.At.Add(RunBudget)}
		} else if err != nil || cur.State != RunIntake { // intake got in between
			s.mu.Unlock()
			continue
		}
		cur.State, cur.Error = RunExpired, ReasonNoIntake
		err = s.store.Save(cur)
		s.mu.Unlock()
		if err != nil {
			log.Warn("litmus: could not expire a tester that never ran intake", "run", id, "error", err)
			continue
		}
		s.store.clearSpawned(sid)
		changed = true
	}
	return changed
}

// gaveUp reports whether a spawned tester is past SpawnBudget, or its session is exited or has been idle for IdleBudget.
func (s *Service) gaveUp(ctx context.Context, sessionID string, sp spawned) bool {
	now := s.clock()
	if now.Sub(sp.At) > SpawnBudget {
		return true
	}
	if s.activity == nil {
		return false
	}
	state, since, found := s.activity(ctx, sessionID)
	return found && (state == "exited" || state == "idle" && now.Sub(since) > IdleBudget)
}

// anyInFlight reports whether a long verb of any run is still running.
func (s *Service) anyInFlight() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.busy) > 0
}

// inFlight reports whether a long verb of the run is still running.
func (s *Service) inFlight(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.busy {
		if strings.HasPrefix(key, runID+" ") {
			return true
		}
	}
	return false
}

func (s *Service) reaped(id string) bool {
	path, err := s.store.path(id, reapedFile)
	if err != nil {
		return true
	}
	_, err = os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
