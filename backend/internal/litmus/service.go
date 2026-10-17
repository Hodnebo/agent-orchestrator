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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

var outcomes = []CheckState{CheckPass, CheckFail, CheckPartial, CheckAbsent, CheckSkipped}

// endGrace is how long a tester has after its run closes to print its last line before its session is stopped.
// Tests shorten it.
var endGrace = 10 * time.Second

// maxCheckNoteChars keeps tester notes bounded on disk while leaving enough
// room for a short observation that public comments can keep whole.
const maxCheckNoteChars = 600

const doneReportBudget = 30 * time.Second

// Service owns the run lifecycle and the check ledger. Every verb is keyed by
// the calling AO session, which owns at most one run.
type Service struct {
	mu              sync.Mutex
	store           *Store
	clock           func() time.Time
	gh              GH
	resolve         RepoResolver
	runner          Runner
	sys             sysOps
	nodeDirs        func(major string) []string
	closeEngine     func(runID string)  // cleanup calls it before killing anything
	busy            map[string]busyVerb // long verbs in flight, by run and verb
	runLocks        map[string]*sync.Mutex
	staleSweepMu    sync.Mutex
	sweepAfterClose func()
	ownedRuns       map[string]bool    // runs adopted by this daemon, including idle live runs
	engines         map[string]*Engine // by run/leg
	recorderMu      sync.Mutex
	recorders       map[string]*screencast // by run/leg, separate from action locks
	newRecorder     func(context.Context, string, time.Time, func() time.Time, func() time.Duration, func(context.Context) (string, error), func(error)) *screencast
	seqs            map[string]int // receipts handed out, by run
	engExec         execFn
	media           Media                                              // ffmpeg, behind an interface so tests never run it
	spawn           Spawner                                            // launches testing workers; nil until the daemon wires it
	report          Reporter                                           // posts the session's `done` report after a submit; nil until the daemon wires it
	kill            Killer                                             // stops the tester's session after a submit; nil until the daemon wires it
	activity        SessionActivity                                    // reads a tester's activity for the sweep; nil until the daemon wires it
	binary          string                                             // the agent-browser binary; empty when it was not found (reported on first use)
	login           string                                             // the gh token's login, read by the first comment post
	evidenceMu      sync.Mutex                                         // one publish at a time: runs of a repo share its evidence clone
	prepareBudget   time.Duration                                      // longest a `prepare` verb may run; tests shorten it
	keepRuns        int                                                // retention after every run: finished run dirs kept
	keepCheckouts   int                                                // retention after every run: finished runs whose checkouts are kept
	spawnMarkerWait time.Duration                                      // longest intake waits for Start to finish saving a spawned session's modes
	projectRepo     func(ctx context.Context, projectID string) string // the project's own owner/repo; nil reads none
	projectHarness  func(ctx context.Context, projectID string) string // the project's worker harness; nil reads none

	mirrorMu     map[string]*sync.Mutex   // one at a time per mirror: a fetch and its pin, a clone, or prune's cleanup
	checkoutMu   map[string]chan struct{} // one preparation at a time per checkout
	retryPause   time.Duration            // before intake asks gh again about a PR it did not find; tests zero it
	ownerHome    string                   // owner-visible home captured when the service was built
	ownerEnv     []string                 // owner environment captured when the service was built
	ownerHomeErr error                    // error from resolving ownerHome at construction time
	claimRefresh map[string]time.Time     // last PR-text refresh attempt per run, guarded by mu

	snapshotHome      func() (homeSnapshot, error) // read-only owner state, injected in fixture tests
	snapshotHomeAfter func([]homeScanRoot, string) (homeSnapshot, error)
}

// New builds a service over store. A nil clock uses time.Now.
func New(store *Store, clock func() time.Time) *Service {
	if clock == nil {
		clock = time.Now
	}
	ownerHome, ownerHomeErr := os.UserHomeDir()
	ownerEnv := os.Environ()
	s := &Service{store: store, clock: clock, gh: execGH{}, runner: execRunner{}, sys: realSys(), nodeDirs: defaultNodeDirs, busy: map[string]busyVerb{},
		engines: map[string]*Engine{}, seqs: map[string]int{}, engExec: realExec, spawnMarkerWait: 10 * time.Second,
		media: execMedia{}, prepareBudget: PrepareBudget, keepRuns: DefaultKeepRuns, keepCheckouts: DefaultKeepCheckouts, mirrorMu: map[string]*sync.Mutex{}, checkoutMu: map[string]chan struct{}{}, retryPause: 2 * time.Second,
		ownerHome: ownerHome, ownerEnv: slices.Clone(ownerEnv), ownerHomeErr: ownerHomeErr}
	s.closeEngine = s.closeEngines
	s.sweepAfterClose = func() { go s.sweepStale(slog.Default()) }
	s.snapshotHome = func() (homeSnapshot, error) {
		if s.ownerHomeErr != nil {
			return homeSnapshot{}, s.ownerHomeErr
		}
		return snapshotOwnerHome(s.ownerHome, s.ownerEnv, store.root)
	}
	if exe, err := os.Executable(); err == nil {
		s.binary, _ = resolveBinary(os.Getenv("AO_AGENT_BROWSER_PATH"), exe)
	}
	return s
}

// WithOwnerHome freezes the owner-visible home and environment used by home snapshots.
// Tests use it to keep filesystem scans inside their fixture directories.
func (s *Service) WithOwnerHome(home string, env []string) *Service {
	s.ownerHome = home
	s.ownerEnv = slices.Clone(env)
	s.ownerHomeErr = nil
	return s
}

// autoPrune is the retention pass a closed run ends with. It writes the summary, and any skip lines, to the run's
// comment.log when something was deleted or skipped, and never fails the run.
func (s *Service) autoPrune(ctx context.Context, run Run) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), PruneBudget)
	defer cancel()
	rep, err := s.prune(ctx, s.keepRuns, s.keepCheckouts, false, run.ID) // this run's dir is kept whatever keepRuns says
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.commentLogf(run, "prune failed: %v", err)
		return
	}
	skips := slices.DeleteFunc(slices.Clone(rep.Lines), func(l string) bool { return !strings.HasPrefix(l, "skipped") })
	if len(rep.Checkouts)+len(rep.Runs) == 0 && len(skips) == 0 {
		return // nothing deleted and nothing skipped: nothing to say
	}
	for _, l := range skips { // a skipped path or checkout is why less was deleted than expected
		s.commentLogf(run, "%s", l)
	}
	s.commentLogf(run, "%s", rep.Summary())
}

// WithGH swaps the gh runner, so tests never reach the network.
func (s *Service) WithGH(gh GH) *Service {
	s.gh = gh
	return s
}

// WithRunner swaps the command runner, so tests never run external commands.
func (s *Service) WithRunner(runner Runner) *Service {
	s.runner = runner
	return s
}

// WithRepos sets how owner/repo is mapped to the local checkout that clones are made from.
func (s *Service) WithRepos(resolve RepoResolver) *Service {
	s.resolve = resolve
	return s
}

// WithProjectRepo sets how a project's own GitHub repo (lowercase owner/repo, "" when it has none) is looked up, so the
// runs list can tell the UI which repo a bare PR number means.
func (s *Service) WithProjectRepo(repo func(ctx context.Context, projectID string) string) *Service {
	s.projectRepo = repo
	return s
}

// WithProjectHarness sets how a project's worker harness ("" when it pins none) is looked up, for a start that names no
// harness and whose project has no Litmus harness setting.
func (s *Service) WithProjectHarness(harness func(ctx context.Context, projectID string) string) *Service {
	s.projectHarness = harness
	return s
}

// Reporter posts the worker report `done` for a session, as `ao report --done` would.
type Reporter func(ctx context.Context, sessionID, note string) error

// WithReporter sets how a submitted run reports its session done.
func (s *Service) WithReporter(r Reporter) *Service {
	s.report = r
	return s
}

// Killer stops a tester's AO session. The deadline sweep and a finished run end the tester through the same one.
type Killer func(ctx context.Context, sessionID string) error

// WithKiller sets how a submitted run's tester is stopped, endGrace after its done report.
func (s *Service) WithKiller(k Killer) *Service {
	s.kill = k
	return s
}

// SessionActivity reads a session's activity state ("idle", "exited", ...) and when it last changed. found is false
// when the session is unknown.
type SessionActivity func(ctx context.Context, sessionID string) (state string, since time.Time, found bool)

// WithActivity sets how the sweep detects stopped testers and testers that never ran intake. Without it only the
// run deadline and time since spawn apply.
func (s *Service) WithActivity(a SessionActivity) *Service {
	s.activity = a
	return s
}

func invalid(code, msg string) error  { return apierr.Invalid(code, msg, nil) }
func conflict(code, msg string) error { return apierr.Conflict(code, msg, nil) }

// runID names the one run a session may own, so a second intake cannot reset
// the run's clock and every verb finds its run without a scan.
func runID(sessionID string) (string, error) {
	if strings.TrimSpace(sessionID) == "" {
		return "", invalid("SESSION_ID_REQUIRED", "sessionId is required")
	}
	return "run_" + sessionID, nil
}

// Status returns the run and its checks.
func (s *Service) Status(ctx context.Context, sessionID string) (RunStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.load(sessionID)
	if err != nil {
		return RunStatus{}, err
	}
	checks, err := s.store.Checks(run.ID)
	var flying []InFlight // what is still running for the run, so a poller can tell a long build from a stuck one
	for k, fl := range s.busy {
		if verb, ours := strings.CutPrefix(k, run.ID+" "); ours {
			flying = append(flying, InFlight{Verb: verb, Seconds: s.clock().Sub(fl.since).Round(time.Second).Seconds()})
		}
	}
	slices.SortFunc(flying, func(a, b InFlight) int { return strings.Compare(a.Verb, b.Verb) })
	openClaims, hints := testingStatusGuidance(run, checks, s.clock())
	return RunStatus{OwnerLogins: s.ownerLoginStatus(ctx, run), Run: run, Checks: append([]Check{}, checks...), InFlight: flying, OpenClaims: openClaims, Hints: hints}, err
}

func testingStatusGuidance(run Run, checks []Check, now time.Time) ([]Check, []TestingHint) {
	active := verdictChecks(checks)
	var openClaims []Check
	for _, effect := range active {
		c := effect.Check
		if c.onLeg() != "head" || checkPurpose(c) != "claim" || effect.supersededBy != "" {
			continue
		}
		if !slices.Contains(outcomes, c.State) || baseResult(checks, c) == "" {
			openClaims = append(openClaims, c)
		}
	}

	var hints []TestingHint
	if run.State.Closed() || now.After(run.Deadline) {
		return openClaims, hints
	}
	if !run.State.Closed() && run.Deadline.Sub(now) <= baseGuardLeft && len(openClaims) > 0 {
		claims := make([]string, len(openClaims))
		for i, c := range openClaims {
			claims[i] = c.ID + " " + strconv.Quote(c.Name)
		}
		hints = append(hints, TestingHint{Text: "Finish now: open claim checks " + strings.Join(claims, ", ") + ". Main twins are optional from here."})
	}

	for _, effect := range active {
		c := effect.Check
		if c.onLeg() != "head" || !slices.Contains(outcomes, c.State) {
			continue
		}
		if _, err := pickLeg(run, "base"); err == nil && replayAdmissibility(checks, c) == nil {
			hints = append(hints, TestingHint{CheckID: c.ID, Text: "On main: ao testing replay " + c.ID + " --leg base"})
		}
		if c.State != CheckFail && c.State != CheckPartial {
			continue
		}
		var candidates []string
		for _, candidate := range active {
			target := candidate.Check
			if err := validateSameBug(&c, checks, target.ID, c.State); err != nil {
				continue
			}
			candidates = append(candidates, target.ID+" "+strconv.Quote(target.Name)+" ("+string(target.State)+")")
		}
		if len(candidates) > 0 {
			hints = append(hints, TestingHint{CheckID: c.ID, Text: "Same bug as an earlier check? Re-end with --result " + string(c.State) + " --same-bug <id>: " + strings.Join(candidates, ", ")})
		}
	}
	return openClaims, hints
}

// CheckAdd appends a pending check with the observable the worker expects. leg is
// "base", "head" or empty (the check acts on whichever leg the worker drives).
func (s *Service) CheckAdd(ctx context.Context, sessionID, name, expect, leg, replaces, reason string, classification ...CheckClassification) (Check, error) {
	if leg != "" && leg != "base" && leg != "head" {
		return Check{}, invalid("TESTING_LEG_INVALID", "leg must be base or head")
	}
	name, expect = strings.TrimSpace(name), strings.TrimSpace(expect)
	replaces, reason = strings.TrimSpace(replaces), strings.TrimSpace(reason)
	if (replaces == "") != (reason == "") {
		return Check{}, invalid("TESTING_CHECK_REPLACEMENT", "--replaces and --reason must be given together")
	}
	if name == "" || expect == "" {
		return Check{}, invalid("TESTING_CHECK_INVALID", "a check needs a name and an expected observable")
	}
	if len(classification) > 1 {
		return Check{}, invalid("TESTING_CHECK_PURPOSE_REQUIRED", "choose claim with its PR sentence, risk with what the diff could break, or explore")
	}
	choiceProvided := len(classification) == 1
	var choice CheckClassification
	if choiceProvided {
		var err error
		choice, err = checkClassification(classification)
		if err != nil {
			return Check{}, err
		}
	}
	change := func() (Check, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		run, err := s.load(sessionID)
		if err != nil {
			return Check{}, err
		}
		checks, err := s.store.Checks(run.ID)
		if err != nil {
			return Check{}, err
		}
		candidate := Check{Name: name, Leg: leg}
		if twinIndex := checkTwinIndex(checks, candidate); candidate.onLeg() == "base" && twinIndex >= 0 && !choiceProvided {
			choice = checkClassificationFor(checks[twinIndex])
			choiceProvided = true
		}
		if !choiceProvided {
			return Check{}, invalid("TESTING_CHECK_PURPOSE_REQUIRED", "choose claim with its PR sentence, risk with what the diff could break, or explore")
		}
		choice, err = checkClassification([]CheckClassification{choice})
		if err != nil {
			return Check{}, err
		}
		replaced := -1
		if replaces != "" {
			replaced = slices.IndexFunc(checks, func(c Check) bool { return c.ID == replaces })
			if replaced < 0 {
				return Check{}, apierr.NotFound("TESTING_CHECK_NOT_FOUND", "no check "+strconv.Quote(replaces))
			}
			old := checks[replaced]
			if old.onLeg() != (Check{Leg: leg}).onLeg() {
				return Check{}, invalid("TESTING_CHECK_REPLACEMENT", "replacement must be on the same leg")
			}
			if old.ReplacedBy != "" {
				return Check{}, conflict("TESTING_CHECK_REPLACEMENT", "check "+old.ID+" was already replaced by "+old.ReplacedBy)
			}
			if slices.ContainsFunc(checks, func(c Check) bool {
				return c.Replaces == old.ID && (c.State == CheckPending || c.State == CheckRunning)
			}) {
				return Check{}, conflict("TESTING_CHECK_REPLACEMENT", "check "+old.ID+" already has a replacement in progress")
			}
			if old.State != CheckFail && old.State != CheckPartial {
				return Check{}, conflict("TESTING_CHECK_REPLACEMENT", "only an ended fail or partial check can be replaced")
			}
			if replacementPurpose(old) != "claim" {
				return Check{}, conflict("TESTING_CHECK_REPLACEMENT", "only a claim check can be replaced")
			}
			if choice.Purpose != "claim" || strings.TrimSpace(choice.ClaimSentence) != strings.TrimSpace(old.ClaimSentence) {
				return Check{}, conflict("TESTING_CHECK_REPLACEMENT", "a replacement must keep the original claim and PR sentence")
			}
			words, err := claimWords(filepath.Join(s.store.root, run.ID))
			if err != nil {
				return Check{}, err
			}
			if !claimInPR(reason, words) {
				return Check{}, invalid("TESTING_CHECK_REPLACE_REASON", "--reason must quote the PR author's title or body")
			}
		}
		for i, c := range checks {
			if i != replaced && !replacementRetired(checks, c) && c.onLeg() == (Check{Leg: leg}).onLeg() && strings.EqualFold(c.Name, name) {
				return Check{}, conflict("TESTING_CHECK_DUPLICATE", "check "+strconv.Quote(name)+" already exists on the "+c.onLeg()+" leg")
			}
		}
		check := Check{ID: "c" + strconv.Itoa(len(checks)+1), Name: name, Expect: expect, State: CheckPending, Leg: leg, Purpose: choice.Purpose, Claim: choice.Purpose == "claim", ClaimSentence: choice.ClaimSentence, Risk: choice.Risk, Replaces: replaces, ReplaceReason: reason}
		if err := validateTwinClassification(checks, check); err != nil {
			return Check{}, err
		}
		if err := s.checkClaim(sessionID, choice); err != nil {
			return Check{}, err
		}
		if replaced >= 0 {
			// Keep the source sentence with the old row for older report readers.
			// ReplacedBy is set only when this new check ends with a usable result.
			checks[replaced].ReplaceReason = reason
		}
		return check, s.store.SaveChecks(run.ID, append(checks, check))
	}
	if choiceProvided {
		return s.withClaimWords(ctx, sessionID, choice, change)
	}
	return change()
}

// checkTwinIndex finds the active opposite-leg check with the same name. A
// pending twin counts too, since both checks need the same coverage before
// either one is run.
func checkTwinIndex(checks []Check, check Check) int {
	active := activeReplacementChecks(checks)
	for _, candidate := range active {
		if candidate.ID == check.ID || candidate.onLeg() == check.onLeg() || !strings.EqualFold(candidate.Name, check.Name) {
			continue
		}
		return slices.IndexFunc(checks, func(stored Check) bool { return stored.ID == candidate.ID })
	}
	return -1
}

// validateTwinClassification is shared by add, start, reclassify and replay.
// It compares the class and claim sentence; risk notes may differ by leg.
func validateTwinClassification(checks []Check, check Check) error {
	i := checkTwinIndex(checks, check)
	if i < 0 {
		return nil
	}
	twin := checks[i]
	checkChoice, twinChoice := checkClassificationFor(check), checkClassificationFor(twin)
	if sameTwinClassification(checkChoice, twinChoice) {
		return nil
	}
	head := check
	if check.onLeg() == "base" {
		head = twin
	}
	want := checkClassificationFor(head)
	if checkChoice.Purpose != twinChoice.Purpose {
		return conflict("TESTING_CHECK_TWIN_CLASS", fmt.Sprintf("main's twin must test what head check %s tests (%s)", head.ID, want.Purpose))
	}
	if checkChoice.ClaimSentence != twinChoice.ClaimSentence {
		return conflict("TESTING_CHECK_TWIN_CLASS", fmt.Sprintf("main's twin must use head check %s's claim sentence %s", head.ID, strconv.Quote(want.ClaimSentence)))
	}
	return nil
}

func sameTwinClassification(a, b CheckClassification) bool {
	return a.Purpose == b.Purpose && a.ClaimSentence == b.ClaimSentence
}

func checkClassificationFor(c Check) CheckClassification {
	purpose := c.Purpose
	if purpose == "" && c.Claim {
		purpose = "claim"
	}
	return CheckClassification{Purpose: purpose, ClaimSentence: c.ClaimSentence, Risk: c.Risk}
}

// checkClassification rejects an omitted or ambiguous coverage choice.
func checkClassification(choices []CheckClassification) (CheckClassification, error) {
	if len(choices) != 1 {
		return CheckClassification{}, invalid("TESTING_CHECK_PURPOSE_REQUIRED", "choose claim with its PR sentence, risk with what the diff could break, or explore")
	}
	c := choices[0]
	c.ClaimSentence = strings.TrimSpace(c.ClaimSentence)
	c.Risk = strings.TrimSpace(c.Risk)
	if c.Purpose != "claim" && c.Purpose != "explore" && c.Purpose != "risk" {
		return c, invalid("TESTING_CHECK_PURPOSE_REQUIRED", "choose claim with its PR sentence, risk with what the diff could break, or explore")
	}
	if (c.Purpose == "claim") != (c.ClaimSentence != "") {
		return c, invalid("TESTING_CHECK_CLAIM_SENTENCE", "only claim takes a PR sentence, and it is required")
	}
	if (c.Purpose == "risk") != (c.Risk != "") {
		return c, invalid("TESTING_CHECK_RISK", "only risk takes what the diff could break, and it is required")
	}
	return c, nil
}

func replacementClassification(checks []Check, c Check, choice CheckClassification) error {
	if strings.TrimSpace(c.Replaces) == "" {
		return nil
	}
	old, found := checkByID(checks, c.Replaces)
	if !found || replacementPurpose(old) != "claim" || choice.Purpose != "claim" || strings.TrimSpace(choice.ClaimSentence) != strings.TrimSpace(old.ClaimSentence) {
		return conflict("TESTING_CHECK_REPLACEMENT", "a replacement must remain a claim with the original PR sentence")
	}
	return nil
}

// CheckStart opens a pending check, inheriting its saved coverage unless a matching choice is supplied.
func (s *Service) CheckStart(ctx context.Context, sessionID, id string, classification ...CheckClassification) (Check, error) {
	if len(classification) > 1 {
		return Check{}, invalid("TESTING_CHECK_PURPOSE_REQUIRED", "choose claim with its PR sentence, risk with what the diff could break, or explore")
	}
	provided := len(classification) == 1
	var requested CheckClassification
	if provided {
		var err error
		requested, err = checkClassification(classification)
		if err != nil {
			return Check{}, err
		}
	}
	change := func() (Check, error) {
		return s.updateCheck(sessionID, id, func(c *Check, all []Check) error {
			choice := checkClassificationFor(*c)
			if choice.Purpose == "" {
				if !provided {
					return invalid("TESTING_CHECK_PURPOSE_REQUIRED", "choose claim with its PR sentence, risk with what the diff could break, or explore")
				}
				choice = requested
			} else if provided && requested != choice {
				return conflict("TESTING_CHECK_STATE", fmt.Sprintf("check %s is %s; use check reclassify with a reason before changing coverage", c.ID, choice.Purpose))
			}
			if err := replacementClassification(all, *c, choice); err != nil {
				return err
			}
			for _, other := range all {
				if other.State == CheckRunning {
					return conflict("TESTING_CHECK_STATE", "check "+other.ID+" is still running; end it first")
				}
			}
			if c.State != CheckPending {
				return conflict("TESTING_CHECK_STATE", fmt.Sprintf("check %s is %s; only a pending check can start", c.ID, c.State))
			}
			candidate := *c
			candidate.Purpose, candidate.Claim = choice.Purpose, choice.Purpose == "claim"
			candidate.ClaimSentence, candidate.Risk = choice.ClaimSentence, choice.Risk
			if err := validateTwinClassification(all, candidate); err != nil {
				return err
			}
			if err := s.checkClaim(sessionID, choice); err != nil {
				return err
			}
			c.State, c.Purpose, c.Claim, c.ClaimSentence = CheckRunning, choice.Purpose, choice.Purpose == "claim", choice.ClaimSentence
			c.Risk = choice.Risk
			return nil
		})
	}
	if provided {
		return s.withClaimWords(ctx, sessionID, requested, change)
	}
	return change()
}

// CheckReclassify changes pending or running coverage and records who changed it and why.
func (s *Service) CheckReclassify(ctx context.Context, sessionID, id string, classification CheckClassification, reason string) (Check, error) {
	choice, classErr := checkClassification([]CheckClassification{classification})
	reason = strings.TrimSpace(reason)
	return s.withClaimWords(ctx, sessionID, choice, func() (Check, error) {
		return s.updateCheck(sessionID, id, func(c *Check, all []Check) error {
			if c.State != CheckPending && c.State != CheckRunning {
				return conflict("TESTING_CHECK_STATE", fmt.Sprintf("%s ended %s; coverage is fixed once a check ends. Add a new check with --claim and run it.", c.ID, c.State))
			}
			if classErr != nil {
				return classErr
			}
			if reason == "" {
				return invalid("TESTING_REASON_REQUIRED", "reclassify needs a reason")
			}
			if utf8.RuneCountInString(reason) > maxReasonChars {
				return invalid("TESTING_REASON_TOO_LONG", "shorten the reclassification reason")
			}
			if err := replacementClassification(all, *c, choice); err != nil {
				return err
			}
			twinIndex := checkTwinIndex(all, *c)
			if twinIndex >= 0 && all[twinIndex].State != CheckPending && all[twinIndex].State != CheckRunning {
				return conflict("TESTING_CHECK_TWIN_STATE", fmt.Sprintf("cannot reclassify %s because twin check %s already ended %s; add a new check to preserve its saved evidence", c.ID, all[twinIndex].ID, all[twinIndex].State))
			}
			if twinIndex >= 0 {
				if err := replacementClassification(all, all[twinIndex], choice); err != nil {
					return err
				}
			}
			if err := s.checkClaim(sessionID, choice); err != nil {
				return err
			}
			reclassifiedAt, scrubbedReason := s.clock(), s.scrub(reason)
			c.Reclassifications = append(c.Reclassifications, CheckReclassification{Who: sessionID, At: reclassifiedAt, Reason: scrubbedReason, From: checkClassificationFor(*c), To: choice})
			c.Purpose, c.Claim, c.ClaimSentence = choice.Purpose, choice.Purpose == "claim", choice.ClaimSentence
			c.Risk = choice.Risk
			if twinIndex >= 0 {
				twin := &all[twinIndex]
				from := checkClassificationFor(*twin)
				if !sameTwinClassification(from, choice) {
					twinChoice := choice
					if choice.Purpose == "risk" && from.Purpose == "risk" && strings.TrimSpace(from.Risk) != "" {
						twinChoice.Risk = from.Risk
					}
					twin.Reclassifications = append(twin.Reclassifications, CheckReclassification{Who: sessionID, At: reclassifiedAt, Reason: scrubbedReason, From: from, To: twinChoice})
					twin.Purpose, twin.Claim, twin.ClaimSentence, twin.Risk = twinChoice.Purpose, twinChoice.Purpose == "claim", twinChoice.ClaimSentence, twinChoice.Risk
				}
				if err := validateTwinClassification(all, *c); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// CheckEnd closes a running check with its outcome, or corrects an ended check's note without changing its outcome.
func (s *Service) CheckEnd(ctx context.Context, sessionID, id string, result CheckState, note string, sameFault bool, shot int, attribution ...CheckAttribution) (Check, error) {
	s.warnOwnerLogin(ctx, sessionID)
	c, err := s.checkEnd(ctx, sessionID, id, result, note, sameFault, shot, attribution...)
	s.warnOwnerLogin(ctx, sessionID)
	return c, err
}

func (s *Service) checkEnd(ctx context.Context, sessionID, id string, result CheckState, note string, sameFault bool, shot int, attribution ...CheckAttribution) (Check, error) {
	if !slices.Contains(outcomes, result) {
		return Check{}, invalid("TESTING_CHECK_RESULT_INVALID", "result must be pass, fail, partial, absent or skipped")
	}
	note = strings.TrimSpace(note)
	attribution = slices.Clone(attribution)
	for i := range attribution {
		attribution[i].Cause = s.scrub(attribution[i].Cause)
	}
	if n := utf8.RuneCountInString(note); n > maxCheckNoteChars {
		return Check{}, invalid("TESTING_CHECK_NOTE_TOO_LONG", fmt.Sprintf("check note is %d characters; shorten --note to %d characters or fewer", n, maxCheckNoteChars))
	}
	finish := func(c *Check, all []Check) error {
		if err := setCheckAttribution(c, all, result, attribution); err != nil {
			return err
		}
		sameBug := ""
		if len(attribution) == 1 {
			sameBug = strings.TrimSpace(attribution[0].SameBug)
		}
		if shot != 0 {
			if _, ok := headAfterShot(all, shot); !ok {
				return invalid("TESTING_ARGS_INVALID", "--shot must name a head receipt with a saved after-shot in this run")
			}
		}
		if sameFault && (c.onLeg() != "base" || (result != CheckFail && result != CheckPartial)) {
			return invalid("TESTING_CHECK_SAME_FAULT", "--same-fault requires a base result of fail or partial")
		}
		if c.State != CheckRunning {
			return conflict("TESTING_CHECK_STATE", fmt.Sprintf("check %s is %s; only a running check can end", c.ID, c.State))
		}
		if result != CheckSkipped && !slices.ContainsFunc(c.Receipts, func(r Receipt) bool { return r.onLeg() == c.onLeg() }) {
			return conflict("TESTING_CHECK_NO_RECEIPT", fmt.Sprintf("check %s has no receipt on the %s leg; act on it (click, fill, press, ...) with --leg %s first", c.ID, c.onLeg(), c.onLeg()))
		}
		state, stored := result, note
		var err error
		if c.Attribution != "environment" {
			state, stored, err = checkEvidence(*c, result, note)
		}
		if err != nil {
			return err
		}
		if sameBug != "" {
			if err := validateSameBug(c, all, sameBug, state); err != nil {
				return err
			}
		}
		if sameFault && state != CheckFail && state != CheckPartial {
			return invalid("TESTING_CHECK_SAME_FAULT", "--same-fault requires a base result of fail or partial")
		}
		c.State, c.Note, c.SameFault, c.SameBug = state, stored, sameFault, sameBug
		c.Shot = shot
		return nil
	}
	// Validate before capture, then revalidate when persisting. Never hold the
	// service mutex while waiting for the browser or taking the engine mutex.
	s.mu.Lock()
	run, err := s.load(sessionID)
	var checks []Check
	if err == nil {
		checks, err = s.store.Checks(run.ID)
	}
	s.mu.Unlock()
	if err != nil {
		return Check{}, err
	}
	i := slices.IndexFunc(checks, func(c Check) bool { return c.ID == id })
	if i < 0 {
		return Check{}, apierr.NotFound("TESTING_CHECK_NOT_FOUND", "no check "+strconv.Quote(id))
	}
	c := checks[i]
	if slices.Contains(outcomes, c.State) {
		var sameBug string
		if len(attribution) > 0 {
			if len(attribution) != 1 || attribution[0].Kind != "" || attribution[0].Cause != "" || attribution[0].Receipt != 0 {
				return Check{}, invalid("TESTING_ARGS_INVALID", "re-ending can only replace the note or same-bug link; omit --shot, --same-fault and other attribution")
			}
			sameBug = strings.TrimSpace(attribution[0].SameBug)
			if sameBug == "" {
				return Check{}, invalid("TESTING_ARGS_INVALID", "re-ending can only replace the note or same-bug link; provide a check id for --same-bug")
			}
		}
		if shot != 0 || sameFault {
			return Check{}, invalid("TESTING_ARGS_INVALID", "re-ending can only replace the note or same-bug link; omit --shot and --same-fault")
		}
		return s.updateCheck(sessionID, id, func(c *Check, all []Check) error {
			if c.State != result {
				return conflict("TESTING_CHECK_STATE", fmt.Sprintf("check %s is %s; re-ending can only replace its note with the same result", c.ID, c.State))
			}
			if sameBug != "" {
				if err := validateSameBug(c, all, sameBug, c.State); err != nil {
					return err
				}
				c.SameBug = sameBug
			}
			if note != "" && note != c.Note {
				c.NoteHistory = append(c.NoteHistory, c.Note)
				c.Note = note
			}
			retireReplacement(c, all)
			return nil
		})
	}
	if err := finish(&c, checks); err != nil {
		return Check{}, err
	}
	var shots []ThemeShot
	var themeNote string
	if leg, err := pickLeg(run, c.onLeg()); err == nil && !c.unreached() {
		_, currentLeg, done, beginErr := s.beginLegCommand(sessionID, leg.Name)
		if beginErr != nil {
			return Check{}, beginErr
		}
		defer done()
		leg = currentLeg
		e := s.engineFor(run.ID, leg)
		e.mu.Lock()
		defer e.mu.Unlock()
		// A competing end may have finished while this call waited for input.
		// Reject it before it can overwrite the accepted check's pictures.
		s.mu.Lock()
		current, readErr := s.store.Checks(run.ID)
		s.mu.Unlock()
		if readErr != nil {
			return Check{}, readErr
		}
		j := slices.IndexFunc(current, func(x Check) bool { return x.ID == id })
		if j < 0 {
			return Check{}, apierr.NotFound("TESTING_CHECK_NOT_FOUND", "no check "+strconv.Quote(id))
		}
		c = current[j]
		if err := finish(&c, current); err != nil {
			return Check{}, err
		}
		shots, themeNote = s.themePair(ctx, e, c.ID)
	}
	return s.updateCheck(sessionID, id, func(c *Check, all []Check) error {
		if err := finish(c, all); err != nil {
			return err
		}
		retireReplacement(c, all)
		if len(shots) > 0 || themeNote != "" {
			i := slices.IndexFunc(c.Receipts, func(r Receipt) bool { return r.onLeg() == c.onLeg() })
			// The last action is the screen that this check finished on.
			for j := range c.Receipts {
				if c.Receipts[j].onLeg() == c.onLeg() {
					i = j
				}
			}
			if i < 0 {
				c.Receipts = append(c.Receipts, Receipt{Leg: c.onLeg(), Action: "check end", At: s.clock()})
				i = len(c.Receipts) - 1
			}
			c.Receipts[i].ThemeShots, c.Receipts[i].ThemeNote = shots, themeNote
		}
		return nil
	})
}

// retireReplacement hides the old claim only after its replacement has a
// completed usable result. A skipped, pending, absent, or reclassified
// replacement leaves the old observation active.
func retireReplacement(c *Check, checks []Check) {
	if c == nil || !replacementCompleted(*c) || strings.TrimSpace(c.Replaces) == "" {
		return
	}
	for i := range checks {
		if checks[i].ID != c.Replaces || !replacementCompatible(checks[i], *c) {
			continue
		}
		checks[i].ReplacedBy = c.ID
		if strings.TrimSpace(c.ReplaceReason) != "" {
			checks[i].ReplaceReason = c.ReplaceReason
		}
		return
	}
}

// setCheckAttribution validates the tester's cause and permits a same-bug link without attribution.
func setCheckAttribution(c *Check, checks []Check, result CheckState, attrs []CheckAttribution) error {
	if len(attrs) == 0 {
		return nil
	}
	if len(attrs) != 1 {
		return invalid("TESTING_CHECK_ATTRIBUTION", "choose one attribution")
	}
	a := attrs[0]
	a.Cause = strings.TrimSpace(a.Cause)
	if a.Kind == "" {
		if a.Cause != "" || a.Receipt != 0 {
			return invalid("TESTING_CHECK_ATTRIBUTION", "cause and attribution receipt are for environment only")
		}
		if strings.TrimSpace(a.SameBug) == "" {
			return invalid("TESTING_CHECK_ATTRIBUTION", "choose an attribution or a same-bug check")
		}
	} else {
		if !slices.Contains([]string{"pr", "pre-existing", "environment", "unknown"}, a.Kind) {
			return invalid("TESTING_CHECK_ATTRIBUTION", "attribution must be pr, pre-existing, environment or unknown")
		}
		if a.Kind == "environment" {
			if a.Cause == "" || a.Receipt <= 0 {
				return invalid("TESTING_CHECK_ENVIRONMENT_EVIDENCE", "environment needs a cause and a receipt")
			}
			if !slices.ContainsFunc(c.legReceipts(), func(r Receipt) bool { return r.Seq == a.Receipt }) {
				return invalid("TESTING_CHECK_ENVIRONMENT_EVIDENCE", "environment receipt must belong to this check on its leg")
			}
		} else if a.Cause != "" || a.Receipt != 0 {
			return invalid("TESTING_CHECK_ATTRIBUTION", "cause and attribution receipt are for environment only")
		}
		if a.Kind == "pre-existing" {
			ended := *c
			ended.State = result
			if !matchingBaseFailure(checks, ended) {
				return invalid("TESTING_CHECK_PRE_EXISTING_EVIDENCE", "pre-existing needs a head fail or partial and a base twin that ended in the same state with a saved after-shot; use unknown or pr otherwise")
			}
		}
		if a.Kind == "environment" && c.onLeg() == "head" && (result == CheckFail || result == CheckAbsent) {
			if i := baseTwin(checks, *c); i >= 0 && checks[i].State == CheckPass && checks[i].Attribution != "environment" {
				return invalid("TESTING_CHECK_ENVIRONMENT_DIFFERENCE", "the base twin passed and the head failed; this is a PR difference, so use unknown or pr")
			}
		}
		c.Attribution, c.EnvironmentCause, c.AttributionReceipt = a.Kind, a.Cause, a.Receipt
	}
	return nil
}

// validateSameBug restricts links to an ended failed or partial head check.
// Checks may be ended in either order because testers often discover the
// related observation after the check they want to lead with.
func validateSameBug(c *Check, checks []Check, targetID string, result CheckState) error {
	if c.onLeg() != "head" || (result != CheckFail && result != CheckPartial) {
		return invalid("TESTING_CHECK_SAME_BUG", "--same-bug requires a head result of fail or partial")
	}
	if strings.TrimSpace(targetID) == c.ID {
		return invalid("TESTING_CHECK_SAME_BUG", "--same-bug cannot target the check itself")
	}
	targetIndex := slices.IndexFunc(checks, func(other Check) bool { return other.ID == targetID })
	if targetIndex < 0 {
		return invalid("TESTING_CHECK_SAME_BUG", "--same-bug must name an existing check")
	}
	target := checks[targetIndex]
	if replacementRetired(checks, target) || target.onLeg() != "head" || (target.State != CheckFail && target.State != CheckPartial) {
		return invalid("TESTING_CHECK_SAME_BUG", "--same-bug target must be an unreplaced failed or partial head check")
	}
	if sameBugWouldCycle(checks, c.ID, targetID) {
		return invalid("TESTING_CHECK_SAME_BUG", "--same-bug would create a cycle")
	}
	return nil
}

// sameBugWouldCycle follows a proposed link through at most one pass over the
// stored checks. Existing malformed cycles are rejected too, so re-ending a
// check cannot make a previously finite chain unbounded.
func sameBugWouldCycle(checks []Check, sourceID, targetID string) bool {
	seen := make(map[string]bool, len(checks))
	current := targetID
	for steps := 0; steps < len(checks); steps++ {
		if current == sourceID || seen[current] {
			return true
		}
		seen[current] = true
		target, found := checkByID(checks, current)
		if !found || strings.TrimSpace(target.SameBug) == "" {
			return false
		}
		current = strings.TrimSpace(target.SameBug)
	}
	return true
}

// checkEvidence requires a saved screenshot for an outcome unless the check was skipped.
func checkEvidence(c Check, result CheckState, note string) (CheckState, string, error) {
	if result == CheckSkipped || slices.ContainsFunc(c.legReceipts(), func(r Receipt) bool { return fileExists(r.AfterPath) }) {
		return result, note, nil
	}
	return "", "", conflict("TESTING_CHECK_EVIDENCE", "no picture was saved; act again or end it skipped")
}

// busyVerb is a long verb running for a run: when it began, and a channel that
// closes when it ends so a retry can wait for it instead of failing.
type busyVerb struct {
	since time.Time
	done  chan struct{}
}

// running returns the channel that closes when verb finishes for the session's
// run, and false when nothing is in flight.
func (s *Service) running(sessionID, verb string) (<-chan struct{}, bool) {
	id, err := runID(sessionID)
	if err != nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fl, on := s.busy[id+" "+verb]
	return fl.done, on
}

// begin loads the session's open run and marks verb in flight, so a second call
// (a retry after a CLI timeout) gets a conflict instead of racing the first.
func (s *Service) begin(sessionID, verb string) (Run, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.load(sessionID)
	if err != nil {
		return Run{}, nil, err
	}
	done, err := s.markBusy(run.ID, verb)
	return run, done, err
}

// beginLegCommand shares the close boundary with submit/cleanup, then resolves
// the current leg so a concurrent relaunch cannot leave stale engine metadata.
func (s *Service) beginLegCommand(sessionID, name string) (Run, Leg, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.load(sessionID)
	if err != nil {
		return Run{}, Leg{}, nil, err
	}
	leg, err := pickLeg(run, name)
	if err != nil {
		return Run{}, Leg{}, nil, err
	}
	done, err := s.markBusy(run.ID, "drive "+leg.Name)
	return run, leg, done, err
}

// markBusy marks verb of run in flight and returns the func that clears it. The mutating verbs exclude each other
// across keys: submit and cleanup refuse while any launch runs (they would stop a recording the launch has not
// stored yet), and a launch refuses while a submit or cleanup runs. Callers hold s.mu.
func (s *Service) markBusy(runID, verb string) (func(), error) {
	key := runID + " " + verb
	if _, on := s.busy[key]; on {
		return nil, conflict("TESTING_BUSY", verb+" is already in progress for "+runID+"; poll `ao testing status`")
	}
	closing := verb == "submit" || verb == "cleanup"
	launchLeg := func(v string) string {
		for _, prefix := range []string{"launch ", "relaunch "} {
			if leg, ok := strings.CutPrefix(v, prefix); ok {
				return leg
			}
		}
		return ""
	}
	leg := launchLeg(verb)
	driveLeg, driving := strings.CutPrefix(verb, "drive ")
	for k := range s.busy {
		other, ours := strings.CutPrefix(k, runID+" ")
		if ours && (verb == "sweep" || other == "sweep" || (closing && (other == "submit" || other == "cleanup" || launchLeg(other) != "" || strings.HasPrefix(other, "drive "))) || (driving && (other == "submit" || other == "cleanup" || launchLeg(other) == driveLeg)) || (leg != "" && (other == "submit" || other == "cleanup" || launchLeg(other) == leg))) {
			return nil, conflict("TESTING_RUN_BUSY", fmt.Sprintf("%s is refused while %s is in progress for %s; wait for it to finish", verb, other, runID))
		}
	}
	fl := busyVerb{since: s.clock(), done: make(chan struct{})}
	if s.ownedRuns == nil {
		s.ownedRuns = map[string]bool{}
	}
	if verb != "sweep" {
		s.ownedRuns[runID] = true
	}
	s.busy[key] = fl
	return func() { s.mu.Lock(); delete(s.busy, key); s.mu.Unlock(); close(fl.done) }, nil
}

// lockRun serializes teardown and local verdict writes without holding the
// service mutex through process shutdown or filesystem scans.
func (s *Service) lockRun(id string) func() {
	s.mu.Lock()
	if s.runLocks == nil {
		s.runLocks = map[string]*sync.Mutex{}
	}
	lock := s.runLocks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		s.runLocks[id] = lock
	}
	s.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// update re-reads the run, applies fn and saves it: long verbs run unlocked and merge their result here.
func (s *Service) update(id string, fn func(*Run)) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.store.Get(id)
	if err != nil {
		return Run{}, err
	}
	fn(&run)
	return run, s.store.Save(run)
}

// load refuses closed runs and testing after the deadline. Only the deadline
// sweep closes runs; an accepted submit can finalize beyond its deadline.
// Callers hold s.mu.
func (s *Service) load(sessionID string) (Run, error) {
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
	state := run.State
	if !state.Closed() && s.clock().After(run.Deadline) {
		if _, submitting := s.busy[run.ID+" submit"]; submitting {
			return run, nil
		}
		state = RunExpired
	}
	if state.Closed() {
		why := ""
		if state == RunExpired && run.Error != "" {
			why = ": " + run.Error
		}
		return Run{}, conflict("TESTING_RUN_CLOSED", fmt.Sprintf("run %s is %s%s; testing verbs are refused", run.ID, state, why))
	}
	return run, nil
}

// updateCheck applies fn to one check and persists the ledger.
func (s *Service) updateCheck(sessionID, id string, fn func(c *Check, all []Check) error) (Check, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.load(sessionID)
	if err != nil {
		return Check{}, err
	}
	checks, err := s.store.Checks(run.ID)
	if err != nil {
		return Check{}, err
	}
	for i := range checks {
		if checks[i].ID != id {
			continue
		}
		if err := fn(&checks[i], checks); err != nil {
			return Check{}, err
		}
		return checks[i], s.store.SaveChecks(run.ID, checks)
	}
	return Check{}, apierr.NotFound("TESTING_CHECK_NOT_FOUND", "no check "+strconv.Quote(id))
}
