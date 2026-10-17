package litmus

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionartifacts"
)

// noChange matches the conventional-commit types of PRs that claim no behavior change.
var noChange = regexp.MustCompile(`(?i)^(docs|chore|tests?|ci|build|style)(\([^)]*\))?!?:`)

// claimsChange reports whether the PR says it changes behavior: its title is not a docs, chore, test, ci,
// build or style commit. A run without a stored title claims nothing.
func claimsChange(run Run) bool {
	return strings.TrimSpace(run.Title) != "" && !noChange.MatchString(run.Title)
}

// baseResult is what the base leg showed for a head check: the outcome of a base-leg check of the same
// name, "observed" when only base receipts exist, or "" when the base was never looked at.
func baseResult(checks []Check, c Check) string {
	if replacementRetired(checks, c) {
		return ""
	}
	if i := baseTwin(checks, c); i >= 0 {
		return string(checks[i].State)
	}
	if slices.ContainsFunc(c.Receipts, func(r Receipt) bool { return r.onLeg() == "base" }) {
		return "observed"
	}
	return ""
}

// ranAndFailed reports whether a base check failed with a saved screenshot.
func ranAndFailed(b Check) bool {
	return b.Attribution != "environment" && !b.unreached() && b.State == CheckFail && slices.ContainsFunc(b.legReceipts(), func(r Receipt) bool { return fileExists(r.AfterPath) })
}

// failedOnBase reports whether the head check's base twin shows main without the
// behavior. A partial base result is enough for a claim only when its after-shot
// proves that the partial result was observed.
func failedOnBase(checks []Check, c Check) bool {
	i := baseTwin(checks, c)
	if i < 0 {
		return false
	}
	base := checks[i]
	if base.Attribution == "environment" || base.unreached() {
		return false
	}
	if base.State == CheckAbsent || ranAndFailed(base) {
		return true
	}
	return checkPurpose(c) == "claim" && base.State == CheckPartial && slices.ContainsFunc(base.legReceipts(), func(r Receipt) bool { return fileExists(r.AfterPath) })
}

// matchingBaseFailure requires the same failing outcome and a saved base after-shot.
func matchingBaseFailure(checks []Check, c Check) bool {
	i := baseTwin(checks, c)
	if c.onLeg() != "head" || (c.State != CheckFail && c.State != CheckPartial) || i < 0 || checks[i].Attribution == "environment" || checks[i].unreached() || checks[i].State != c.State {
		return false
	}
	return slices.ContainsFunc(checks[i].legReceipts(), func(r Receipt) bool { return fileExists(r.AfterPath) })
}

// preExisting accepts the tester's attribution or --same-fault only for a risk or
// exploration check, and only with matching base evidence. Claims describe the
// PR's promised behavior, so a same-fault claim failure still caps the verdict.
func preExisting(checks []Check, c Check) bool {
	if c.Claim || (c.Purpose != "risk" && c.Purpose != "explore") {
		return false
	}
	if !matchingBaseFailure(checks, c) {
		return false
	}
	if checks[baseTwin(checks, c)].SameFault {
		return true
	}
	return c.Attribution == "pre-existing"
}

func sameFaultFailure(checks []Check, c Check) bool {
	i := baseTwin(checks, c)
	return matchingBaseFailure(checks, c) && i >= 0 && checks[i].SameFault
}

// checkAgainstBase also handles a base pass recorded after an environmental head failure.
// Keep the saved attribution intact, but do not let it override the observed difference.
func checkAgainstBase(checks []Check, c Check) Check {
	if c.Attribution == "environment" && c.onLeg() == "head" && (c.State == CheckFail || c.State == CheckAbsent) {
		if i := baseTwin(checks, c); i >= 0 && checks[i].State == CheckPass && checks[i].Attribution != "environment" {
			c.Attribution = "unknown"
		}
	}
	if c.onLeg() == "head" && preExisting(checks, c) {
		c.Attribution = "pre-existing"
	}
	return c
}

// headlineRule is the rule derive applies, as verdict.md states it (the result is the overall verdict).
const headlineRule = "A claimed head check is the proof of the PR's stated behavior. Skipped checks are unreached. Environment attribution preserves the observation and never proves a PR failure or improvement; an environmental head fail or partial without same-fault base evidence caps the run at partial. A same-fault head claim fail or partial caps the result at partial and is described as fails the same way on main and the PR build. A same-fault risk or explore head fail or partial with the same failing or partial base outcome and an existing base after-shot is pre-existing and does not cap the result. A recorded PR head failure wins before untested, including when no claimed check finished; an absent PR feature is a failure. Otherwise the result is untested when no claimed head check ended pass, fail, or partial. A head failure beside an unmarked base failure with evidence stays partial until the tester marks the same fault. Other head failures or partials cap the result. Pass requires every claimed head check to pass, at least one head pass to differ from the base, and no unexplained completed head partial. A base failure or a partial claim twin counts as a difference only with an existing after-shot. A head failure or partial beside an absent base is the PR's own. A skipped claimed check keeps the result partial at best unless a later claim check in stored order on the same leg has the identical non-empty claim sentence and ended pass, fail, or partial. Such a skip is superseded; the later result counts as itself. Skipped non-claim checks and base-only checks are context."

// The reasons derive gives for a partial with nothing worse; the comment says them in its own words.
const (
	reasonPreOnly      = "every head check that did not pass also did not pass on the base"
	reasonPrePartial   = "every head check that was partial was partial on the base too"
	reasonPreBoth      = "every head check that did not pass also did not pass on the base, failing or partial"
	reasonNoneReached  = "no head check could be reached"
	reasonNoDifference = "no check passed on the head while failing on the base, so the base-vs-head difference was not shown"
	reasonUntested     = "no claimed head check reached a pass, fail, or partial result"
	maxSummaryChars    = 1200
	maxReasonChars     = 600
)

// verdictCheck classifies an observation once for the verdict and its headline.
// cap is fail or partial only when the head observation limits the result.
type verdictCheck struct {
	Check
	cap                  CheckState
	exempt               bool
	skippedClaim         bool
	supersededBy         string
	sameFault            bool
	unproven             bool
	environmentOverruled bool
}

func replacementPurpose(c Check) string {
	if c.Purpose != "" {
		return c.Purpose
	}
	if c.Claim {
		return "claim"
	}
	return ""
}

func replacementCompleted(c Check) bool {
	return c.State == CheckPass || c.State == CheckFail || c.State == CheckPartial
}

// replacementCompatible is the only path that can retire an old check. A
// replacement keeps the claim source and observes the same leg.
func replacementCompatible(old, replacement Check) bool {
	return strings.TrimSpace(replacement.Replaces) == old.ID &&
		replacementCompleted(replacement) &&
		old.onLeg() == replacement.onLeg() &&
		replacementPurpose(old) == "claim" &&
		replacementPurpose(replacement) == "claim" &&
		strings.TrimSpace(old.ClaimSentence) != "" &&
		strings.TrimSpace(old.ClaimSentence) == strings.TrimSpace(replacement.ClaimSentence)
}

// replacementRetired requires both sides of the persisted link. This keeps a
// stale ReplacedBy field from hiding a failure when an older run was only
// partially written.
func replacementRetired(checks []Check, old Check) bool {
	if strings.TrimSpace(old.ReplacedBy) == "" {
		return false
	}
	replacement, found := checkByID(checks, old.ReplacedBy)
	return found && replacementCompatible(old, replacement)
}

func activeReplacementChecks(checks []Check) []Check {
	return slices.DeleteFunc(slices.Clone(checks), func(c Check) bool { return replacementRetired(checks, c) })
}

func replacementReason(checks []Check, replacement Check) string {
	if reason := strings.TrimSpace(replacement.ReplaceReason); reason != "" {
		return reason
	}
	if old, found := checkByID(checks, replacement.Replaces); found {
		return strings.TrimSpace(old.ReplaceReason)
	}
	return ""
}

func replacementLines(checks []Check) []string {
	var lines []string
	for _, c := range checks {
		if strings.TrimSpace(c.Replaces) == "" {
			continue
		}
		old, found := checkByID(checks, c.Replaces)
		if found && old.ReplacedBy == c.ID && replacementRetired(checks, old) {
			lines = append(lines, fmt.Sprintf("- %s replaces %s: %s", mdText(c.ID, math.MaxInt), mdText(c.Replaces, math.MaxInt), mdText(replacementReason(checks, c), math.MaxInt)))
			continue
		}
		outcome := "did not finish"
		if replacementCompleted(c) {
			outcome = "did not take effect"
		}
		lines = append(lines, fmt.Sprintf("- %s was meant to replace %s but %s: %s", mdText(c.ID, math.MaxInt), mdText(c.Replaces, math.MaxInt), outcome, mdText(replacementReason(checks, c), math.MaxInt)))
	}
	return lines
}

func verdictChecks(checks []Check) []verdictCheck {
	active := activeReplacementChecks(checks)
	classified := make([]verdictCheck, 0, len(active))
	for i, original := range active {
		c := checkAgainstBase(active, original)
		effect := verdictCheck{Check: c, environmentOverruled: original.Attribution == "environment" && c.Attribution == "unknown"}
		if c.unreached() {
			if c.Claim && c.ClaimSentence != "" {
				for _, later := range active[i+1:] {
					if later.Claim && later.onLeg() == c.onLeg() && later.ClaimSentence == c.ClaimSentence && (later.State == CheckPass || later.State == CheckFail || later.State == CheckPartial) {
						effect.supersededBy = later.ID
						break
					}
				}
			}
			// Legacy base twins may lack their head check's claim flag.
			claim := c.Claim || c.onLeg() == "base" && slices.ContainsFunc(active, func(h Check) bool {
				return h.onLeg() == "head" && h.Claim && strings.EqualFold(h.Name, c.Name)
			})
			effect.skippedClaim = claim && effect.supersededBy == ""
		} else if c.onLeg() == "head" {
			effect.exempt = preExisting(active, c)
			if !effect.exempt {
				if c.Attribution != "environment" {
					effect.sameFault = c.Claim && sameFaultFailure(active, c)
					if j := baseTwin(active, c); j >= 0 && !active[j].SameFault && c.Attribution != "pr" {
						base := active[j]
						effect.unproven = c.State == CheckFail && base.State == CheckFail && ranAndFailed(base)
					}
				}
				switch c.State {
				case CheckFail, CheckAbsent:
					effect.cap = CheckFail
				case CheckPartial:
					effect.cap = CheckPartial
				}
				if effect.cap != "" && (c.Attribution == "environment" || effect.sameFault || effect.unproven) {
					effect.cap = CheckPartial
				}
			}
		}
		classified = append(classified, effect)
	}
	return classified
}

func hasActiveNonexemptHeadFailure(checks []Check) bool {
	for _, check := range verdictChecks(checks) {
		c := check.Check
		if c.onLeg() == "head" && !check.exempt && (c.State == CheckFail || c.State == CheckPartial) {
			return true
		}
	}
	return false
}

// derive folds the head checks into the overall outcome by headlineRule. Claims
// are the checks that can establish a pass. Other checks can still expose a
// regression, but a skipped non-claim check does not cap the result.
func derive(checks []Check) (overall CheckState, reasons []string) {
	failed, partial, allClaimsPass, differs, headDiffers, reached, skipped, claimEnded, preFail, prePartial := false, false, true, false, false, false, false, false, false, false
	for _, effect := range verdictChecks(checks) {
		c := effect.Check
		if c.onLeg() != "head" || effect.supersededBy != "" {
			continue
		}
		if effect.environmentOverruled {
			reasons = append(reasons, c.ID+" "+c.Name+": the base passed while the head failed; environment attribution is not supported")
		}
		if c.unreached() {
			if c.Attribution == "environment" {
				reasons = append(reasons, c.ID+" "+c.Name+": "+environmentPhrase(c))
			}
			if effect.skippedClaim {
				skipped = true
				if c.Attribution != "environment" {
					reasons = append(reasons, c.ID+" "+c.Name+": not checked: "+c.Note)
				}
			}
			continue
		}
		if c.Claim && (c.State == CheckPass || c.State == CheckFail || c.State == CheckPartial) {
			claimEnded = true
		}
		failed = failed || effect.cap == CheckFail
		partial = partial || effect.cap == CheckPartial
		if c.Attribution == "environment" {
			if c.Claim && (c.State == CheckPass || c.State == CheckFail || c.State == CheckPartial) {
				allClaimsPass = allClaimsPass && c.State == CheckPass
			}
			reasons = append(reasons, c.ID+" "+c.Name+": "+c.Note+" "+environmentPhrase(c))
			reached = reached || c.Claim
			continue
		}
		if effect.exempt {
			if c.Claim {
				preFail, prePartial = preFail || c.State == CheckFail, prePartial || c.State == CheckPartial
			}
			continue
		}
		if c.Claim && (c.State == CheckPass || c.State == CheckFail || c.State == CheckPartial) {
			allClaimsPass = allClaimsPass && c.State == CheckPass
		}
		if effect.sameFault {
			reasons = append(reasons, c.ID+" "+c.Name+": fails the same way on main and the PR build")
		}
		if c.State != CheckPass && c.Note != "" {
			reasons = append(reasons, c.ID+" "+c.Name+": "+c.Note)
		}
		if effect.unproven {
			reasons = append(reasons, c.ID+" "+c.Name+": fails on main too; not shown to be the same fault")
		}
		if c.State == CheckAbsent {
			reasons = append(reasons, c.ID+" "+c.Name+": the PR build does not have this")
		}
		reached = reached || c.Claim
		if b := baseResult(checks, c); b != "" && b != "observed" && b != string(CheckSkipped) && b != string(c.State) {
			differs = true
		}
		headDiffers = headDiffers || (c.State == CheckPass && failedOnBase(checks, c))
	}
	switch {
	case failed:
		return CheckFail, reasons
	case !claimEnded:
		return CheckUntested, append(reasons, reasonUntested)
	case allClaimsPass && headDiffers && !skipped && !partial:
		return CheckPass, reasons
	case !reached && preFail && prePartial:
		reasons = append(reasons, reasonPreBoth)
	case !reached && prePartial:
		reasons = append(reasons, reasonPrePartial)
	case !reached && preFail:
		reasons = append(reasons, reasonPreOnly)
	case !reached:
		reasons = append(reasons, reasonNoneReached)
	case allClaimsPass && !differs:
		reasons = append(reasons, reasonNoDifference)
	}
	return CheckPartial, reasons
}

// uncontrollableHeadReason blocks only harness control failures that prevent
// reaching claimed checks. App exits and hangs remain public observations.
func uncontrollableHeadReason(run Run, checks []Check) string {
	var control *LegControl
	for i := range run.Legs {
		if run.Legs[i].Name == "head" {
			control = run.Legs[i].Control
			break
		}
	}
	if control == nil || control.State != "lost" || (control.Cause != "native_dialog" && control.Cause != "browser_control_unusable") {
		return ""
	}
	if overall, _ := derive(checks); overall == CheckFail {
		return ""
	}

	var names []string
	for _, effect := range verdictChecks(checks) {
		c := effect.Check
		if c.onLeg() != "head" || !c.Claim || effect.supersededBy != "" {
			continue
		}
		switch c.State {
		case CheckSkipped, CheckPending, CheckRunning:
			name := c.Name
			if name == "" {
				name = c.ID
			}
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	detail := lowerFirst(strings.TrimSuffix(publicControlDetail(*control), "."))
	return fmt.Sprintf("head app uncontrollable since action %d: %s; unreached claim checks: %s", control.Since, detail, strings.Join(names, ", "))
}

func appControlLossGap(run Run) string {
	for _, leg := range run.Legs {
		control := leg.Control
		if leg.Name != "head" || control == nil || control.State != "lost" || (control.Cause != "app_exited" && control.Cause != "app_unresponsive") {
			continue
		}
		detail := strings.TrimSuffix(publicControlDetail(*control), ".")
		detail = strings.Replace(detail, "The app", "The PR build's app", 1)
		return fmt.Sprintf("%s during action %d.", detail, control.Since)
	}
	return ""
}

// readIntakeClaims returns source text, never a guessed list of claims.
func readIntakeClaims(runDir string) []string {
	data, err := os.ReadFile(filepath.Join(runDir, "intake-claims.json"))
	var words []string
	if err == nil && json.Unmarshal(data, &words) == nil {
		return words
	}
	// Older in-flight runs have a context pack but no source-text artifact.
	data, _ = os.ReadFile(filepath.Join(runDir, "run.json"))
	var run Run
	if json.Unmarshal(data, &run) == nil && run.Title != "" {
		words = append(words, run.Title)
	}
	pack, _ := os.ReadFile(filepath.Join(runDir, "context-pack.md"))
	if _, body, ok := strings.Cut(string(pack), "### Body\n\n"); ok {
		body, _, _ = strings.Cut(body, "### Comments and reviews")
		for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
			words = append(words, strings.TrimPrefix(line, "> "))
		}
	}
	return words
}

// Submit validates the evidence, closes the video, derives the overall outcome, writes verdict.md and
// copies the evidence into the session's artifact dir. Every check needs an outcome and, unless skipped, a receipt;
// otherwise nothing is written and the offenders are listed.
//
// A prepared base that was never launched refuses the submit (TESTING_BASE_NOT_RUN) while more than baseGuardLeft of the
// deadline remains, unless skipBase carries the reason the tester gave for not running it ("" means no skip). A skip is
// honoured at any time; the head checks that have no result on the base then get a skipped base twin with that reason.
func (s *Service) Submit(ctx context.Context, sessionID, summary, skipBase string) (Verdict, error) {
	if n := len([]rune(summary)); n > maxSummaryChars {
		return Verdict{}, invalid("TESTING_SUMMARY_TOO_LONG", fmt.Sprintf("summary is %d characters; shorten --summary to %d characters or fewer", n, maxSummaryChars))
	}
	if n := len([]rune(skipBase)); n > maxReasonChars {
		return Verdict{}, invalid("TESTING_REASON_TOO_LONG", fmt.Sprintf("skip-base reason is %d characters; shorten --reason to %d characters or fewer", n, maxReasonChars))
	}
	skip := strings.Join(strings.Fields(skipBase), " ")
	if skipBase != "" && skip == "" {
		return Verdict{}, invalid("TESTING_REASON_REQUIRED", "--skip-base needs a non-empty --reason")
	}
	run, done, err := s.begin(sessionID, "submit") // a retry after a CLI timeout gets TESTING_BUSY
	if err != nil {
		return Verdict{}, err
	}
	defer done()
	checks, err := s.store.Checks(run.ID)
	if err != nil {
		return Verdict{}, err
	}
	if len(checks) == 0 {
		words := readIntakeClaims(filepath.Join(s.store.root, run.ID))
		return Verdict{}, apierr.Invalid("TESTING_NO_CHECKS", "submit needs at least one claim check. PR claims from intake:\n"+strings.Join(words, "\n"), map[string]any{"intakeClaims": words})
	}
	if !slices.ContainsFunc(checks, func(c Check) bool { return !replacementRetired(checks, c) && c.onLeg() == "head" && c.Claim }) {
		words := readIntakeClaims(filepath.Join(s.store.root, run.ID))
		return Verdict{}, apierr.Invalid("TESTING_NO_CLAIM_CHECKS", "submit needs a head claim check; add one with --claim quoting the PR author's title or body, and run it. If unreachable, skip it with a reason. PR claims from intake:\n"+strings.Join(words, "\n"), map[string]any{"intakeClaims": words})
	}
	var incomplete []string // no receipt, or never ended
	for _, c := range checks {
		if replacementRetired(checks, c) {
			continue
		}
		if (len(c.Receipts) == 0 && c.State != CheckSkipped) || !slices.Contains(outcomes, c.State) {
			incomplete = append(incomplete, c.ID+" "+c.Name)
		}
	}
	if len(incomplete) > 0 {
		return Verdict{}, apierr.Invalid("TESTING_CHECKS_INCOMPLETE", "every check needs an outcome and a receipt (a skipped check needs none); incomplete: "+strings.Join(incomplete, "; "), map[string]any{"checks": incomplete})
	}

	if skip == "" && s.baseFailed(run) {
		return Verdict{}, conflict("TESTING_BASE_NOT_RUN", run.baseName()+" was prepared but failed to launch, so there are no base results; submit with `--skip-base --reason \"<say that "+run.baseName()+" failed to launch and why>\"`")
	}
	if skip == "" && s.baseNeverLaunched(run) {
		how := "run " + run.baseName() + " (`ao testing launch base`), repeat each check there in the app and record its visible result"
		return Verdict{}, conflict("TESTING_BASE_NOT_RUN", how+"; record pass, fail, partial, or skipped with a note saying why, or submit with `--skip-base --reason \"<why main was not run>\"`")
	}
	if skip == "" {
		if ids := s.notComparedOnBase(run, checks); len(ids) > 0 {
			return Verdict{}, conflict("TESTING_BASE_INCOMPLETE", baseMissing(run, ids))
		}
	}
	return s.closeRun(ctx, run, skipOnBase(checks, skip), summary, "", skip)
}

// checkIDs lists the ids of the checks, "c11, c12".
func checkIDs(checks []Check) string {
	ids := make([]string, len(checks))
	for i, c := range checks {
		ids[i] = c.ID
	}
	return strings.Join(ids, ", ")
}

// baseGuardLeft is how much of the deadline must remain for an unlaunched base to refuse a submit.
const baseGuardLeft = 4 * time.Minute

// baseCoverage splits the head checks the tester reached by whether they have a result on the base: a base twin that
// was skipped, or no base look at all, is none.
func baseCoverage(checks []Check) (compared, notRun []Check) {
	for _, c := range checks {
		if replacementRetired(checks, c) || c.onLeg() != "head" || c.unreached() {
			continue
		}
		if b := baseResult(checks, c); b == "" || b == string(CheckSkipped) {
			notRun = append(notRun, c)
		} else {
			compared = append(compared, c)
		}
	}
	return compared, notRun
}

// skipOnBase records the main side of every head check that has no base result as skipped, with the reason the tester
// gave for --skip-base, so each row says it was not run there. A run with no result on the base at all keeps its
// run-wide reason instead: there is no comparison to qualify.
func skipOnBase(checks []Check, why string) []Check {
	if why == "" {
		return checks
	}
	compared, notRun := baseCoverage(checks)
	if len(compared) == 0 {
		return checks
	}
	out := slices.Clone(checks)
	for _, c := range notRun {
		if baseResult(checks, c) == "" {
			out = append(out, Check{ID: "c" + strconv.Itoa(len(out)+1), Name: c.Name, Expect: c.Expect, State: CheckSkipped, Leg: "base", Note: why, Purpose: c.Purpose, Claim: c.Claim, ClaimSentence: c.ClaimSentence, Risk: c.Risk})
		}
	}
	return out
}

// baseNeverLaunched is true for a run whose base leg was prepared but not launched and that still has time to launch it.
func (s *Service) baseNeverLaunched(run Run) bool {
	return slices.ContainsFunc(run.Legs, func(l Leg) bool { return l.Name == "base" && l.State == "prepared" }) && run.Deadline.Sub(s.clock()) > baseGuardLeft
}

// baseFailed is true for a run whose base leg launched and then failed.
func (s *Service) baseFailed(run Run) bool {
	return slices.ContainsFunc(run.Legs, func(l Leg) bool { return l.Name == "base" && l.State == "failed" })
}

// baseMissing tells the tester how to give the head checks ids a result on the base.
func baseMissing(run Run, ids []string) string {
	main, who := run.baseName(), "check "+ids[0]+" has"
	if len(ids) > 1 {
		who = "checks " + strings.Join(ids, ", ") + " have"
	}
	return who + " no result on " + main + "; repeat it there or record it with --result skipped and a note saying why, or submit with `--skip-base --reason \"<why>\"`"
}

// notComparedOnBase lists the head checks the tester could reach that have no base result of any kind (a base check
// ended skipped, with a note saying why, counts), once the base launched
// and while at least baseGuardLeft of the deadline remains: the comment says what each check showed on the base or why it did not.
func (s *Service) notComparedOnBase(run Run, checks []Check) (ids []string) {
	ran := func(l Leg) bool {
		return l.Name == "base" && l.State == "running"
	}
	if run.Deadline.Sub(s.clock()) <= baseGuardLeft || !slices.ContainsFunc(run.Legs, ran) {
		return nil
	}
	for _, c := range checks {
		if !replacementRetired(checks, c) && c.onLeg() == "head" && !c.unreached() && (baseResult(checks, c) == "" || baseSkippedWithoutNote(checks, c)) {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// baseSkippedWithoutNote is true when a head check's only base result is a skip that says nothing about why.
func baseSkippedWithoutNote(checks []Check, c Check) bool {
	i := baseTwin(checks, c)
	return i >= 0 && checks[i].State == CheckSkipped && strings.TrimSpace(checks[i].Note) == ""
}

// SubmitBlocked stops a run early: a recorded failure stays fail, otherwise the result is blocked. No receipts are
// required; verdict.md carries the reason and whatever evidence the run has.
func (s *Service) SubmitBlocked(ctx context.Context, sessionID, reason string) (Verdict, error) {
	if strings.TrimSpace(reason) == "" {
		return Verdict{}, invalid("TESTING_REASON_REQUIRED", "a blocked submit needs a non-empty reason")
	}
	if n := len([]rune(reason)); n > maxReasonChars {
		return Verdict{}, invalid("TESTING_REASON_TOO_LONG", fmt.Sprintf("blocked reason is %d characters; shorten --reason to %d characters or fewer", n, maxReasonChars))
	}
	run, done, err := s.begin(sessionID, "submit")
	if err != nil {
		return Verdict{}, err
	}
	defer done()
	checks, err := s.store.Checks(run.ID)
	if err != nil {
		return Verdict{}, err
	}
	return s.closeRun(ctx, run, checks, "", strings.TrimSpace(reason), "")
}

func brokenRelativeLinks(runDir, md string) []string {
	var broken []string
	doc := goldmark.DefaultParser().Parse(text.NewReader([]byte(md)))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var dest []byte
		switch n := n.(type) {
		case *ast.Link:
			dest = n.Destination
		case *ast.Image:
			dest = n.Destination
		default:
			return ast.WalkContinue, nil
		}
		target := string(util.URLEscape(dest, true))
		u, err := url.Parse(target)
		if err == nil && (u.IsAbs() || u.Host != "" || u.Path == "" || filepath.IsAbs(u.Path)) {
			return ast.WalkContinue, nil
		}
		if err == nil {
			path := filepath.Join(runDir, filepath.FromSlash(u.Path))
			rel, relErr := filepath.Rel(runDir, path)
			if relErr == nil && filepath.IsLocal(rel) && fileExists(path) {
				return ast.WalkContinue, nil
			}
		}
		link := string(dest)
		if !slices.Contains(broken, link) {
			broken = append(broken, link)
		}
		return ast.WalkContinue, nil
	})
	return broken
}

func validateVerdictLinks(runDir, summary, blocked string, checks []Check, extra ...string) error {
	var broken []string
	checkLinks := func(field, md string) {
		for _, link := range brokenRelativeLinks(runDir, md) {
			entry := field + ": " + link
			if !slices.Contains(broken, entry) {
				broken = append(broken, entry)
			}
		}
	}
	checkLinks("summary", summary)
	checkLinks("blocked reason", blocked)
	for _, text := range extra {
		checkLinks("skip-base reason", text)
	}
	for _, c := range checks {
		if !replacementRetired(checks, c) {
			checkLinks("check "+c.ID+" note", c.Note)
		}
	}
	if len(broken) > 0 {
		message := "verdict.md links to files missing from the run folder: " + strings.Join(broken, "; ") + "; fix the summary, or re-end that check with a corrected note, then submit again"
		if slices.ContainsFunc(broken, func(link string) bool {
			return strings.HasPrefix(link, "skip-base reason: ")
		}) {
			message += "; edit the --skip-base --reason to remove or correct its broken link"
		}
		if slices.ContainsFunc(broken, func(link string) bool {
			return strings.HasPrefix(link, "blocked reason: ")
		}) {
			message += "; edit the blocked --reason to remove or correct its broken link"
		}
		return invalid("TESTING_VERDICT_LINKS_MISSING", message)
	}
	return nil
}

// neutralizeVerdictLinks keeps a deadline close useful when a tester left a
// relative link to an artifact that was never saved. Ordinary submit still
// refuses these links through validateVerdictLinks.
func neutralizeVerdictLinks(runDir, md string) (string, int) {
	broken := brokenRelativeLinks(runDir, md)
	for _, dest := range broken {
		q := regexp.QuoteMeta(dest)
		inline := regexp.MustCompile(`(?m)!?\[([^\]\n]*)\]\(\s*(?:<` + q + `>|` + q + `)(?:\s+[^)\n]*)?\s*\)`)
		md = inline.ReplaceAllString(md, `$1`)
		definition := regexp.MustCompile(`(?m)^\[([^\]\n]+)\]:\s*<?` + q + `>?(?:\s+.*)?$`)
		for _, match := range definition.FindAllStringSubmatch(md, -1) {
			reference := regexp.MustCompile(`(?m)!?\[([^\]\n]*)\]\[` + regexp.QuoteMeta(match[1]) + `\]`)
			md = reference.ReplaceAllString(md, `$1`)
			md = definition.ReplaceAllString(md, "")
		}
	}
	if len(brokenRelativeLinks(runDir, md)) > 0 {
		// Complex labels or multiline references still must not lose the deadline verdict.
		// Escaping brackets renders the remaining link markup as ordinary text.
		md = strings.ReplaceAll(strings.ReplaceAll(md, `\`, `\\`), `[`, `\[`)
	}
	return md, len(broken)
}

// closeRun turns the checks into a verdict (a recorded fail survives a blocked reason), writes verdict.md,
// publishes the evidence, marks the run submitted and reports the session done. A run of a project that asks before
// posting stops at awaiting-approval instead, with comment.md written and nothing published or posted. skipBase is
// the reason the tester gave for not running the base, and lands in Limits.
func (s *Service) closeRun(ctx context.Context, run Run, checks []Check, summary, blocked, skipBase string) (Verdict, error) {
	return s.closeRunWithLinkMode(ctx, run, checks, summary, blocked, skipBase, false)
}

func (s *Service) closeRunAtDeadline(ctx context.Context, run Run, checks []Check, summary, blocked, skipBase string) (Verdict, error) {
	return s.closeRunWithLinkMode(ctx, run, checks, summary, blocked, skipBase, true)
}

func (s *Service) closeRunWithLinkMode(ctx context.Context, run Run, checks []Check, summary, blocked, skipBase string, deadline bool) (Verdict, error) {
	runDir := filepath.Join(s.store.root, run.ID)
	resuming := run.submission != nil
	var brokenLinks int
	if resuming {
		in := run.submission
		checks, summary, blocked, skipBase, deadline = in.Checks, in.Summary, in.BlockedReason, in.SkipBaseReason, in.Deadline
		brokenLinks = in.BrokenLinks
	}
	// Capture the tester's end at close entry, but do not persist it until the
	// submitted evidence has passed link validation. A rejected submit is not
	// the end of testing, while a deadline close still uses the budget boundary
	// even when its sweep reaches it a little later.
	endedAt := s.clock()
	if deadline && !run.Deadline.IsZero() {
		endedAt = run.Deadline
	}
	loginWarning := s.warnOwnerLogin(ctx, run.SessionID)
	if gap := appControlLossGap(run); gap != "" && !slices.Contains(run.Gaps, gap) {
		var err error
		run, err = s.update(run.ID, func(cur *Run) {
			if !slices.Contains(cur.Gaps, gap) {
				cur.Gaps = append(cur.Gaps, gap)
			}
		})
		if err != nil {
			return Verdict{}, fmt.Errorf("record app control loss: %w", err)
		}
	}
	if controlReason := uncontrollableHeadReason(run, checks); !resuming && controlReason != "" {
		if blocked == "" {
			blocked = controlReason
		} else {
			blocked = strings.TrimSpace(blocked) + "\n" + controlReason
		}
	}
	if deadline {
		var n int
		summary, n = neutralizeVerdictLinks(runDir, summary)
		brokenLinks += n
		blocked, n = neutralizeVerdictLinks(runDir, blocked)
		brokenLinks += n
		skipBase, n = neutralizeVerdictLinks(runDir, skipBase)
		brokenLinks += n
		checks = slices.Clone(checks)
		for i := range checks {
			checks[i].Note, n = neutralizeVerdictLinks(runDir, checks[i].Note)
			brokenLinks += n
		}
	}
	if err := validateVerdictLinks(runDir, summary, blocked, checks, skipBase); err != nil {
		return Verdict{}, err
	}
	if run.TestingEndedAt.IsZero() {
		var err error
		run, err = s.update(run.ID, func(cur *Run) {
			if cur.TestingEndedAt.IsZero() {
				cur.TestingEndedAt = endedAt
				cur.submission = &submitInputs{
					Summary: summary, Blocked: blocked != "", BlockedReason: blocked,
					SkipBase: skipBase != "", SkipBaseReason: skipBase,
					Checks: checks, Deadline: deadline, BrokenLinks: brokenLinks,
				}
			}
		})
		if err != nil {
			return Verdict{}, fmt.Errorf("record testing end: %w", err)
		}
	}
	run = s.stopRecording(ctx, run) // while the engines are still alive; returns the run with its recording state
	videos := s.finishVideo(ctx, runDir, run)
	var limits []string
	if skipBase != "" {
		switch compared, notRun := baseCoverage(checks); {
		case len(compared) == 0:
			limits = append(limits, "base not run: "+cell(skipBase))
		case len(notRun) > 0:
			limits = append(limits, fmt.Sprintf("base compared on %d of %d checks; not run there: %s: %s", len(compared), len(compared)+len(notRun), checkIDs(notRun), cell(skipBase)))
		}
	}
	claims := claimsChange(run)
	verdictChecks := slices.Clone(checks)
	for i := range verdictChecks {
		verdictChecks[i] = checkAgainstBase(checks, verdictChecks[i])
	}
	v := Verdict{IntakeClaims: readIntakeClaims(runDir), Checks: verdictChecks, Summary: summary, SkipBase: skipBase, OwnerLoginWarning: loginWarning}
	if blocked != "" {
		v.Overall, v.Reasons = derive(checks)
		if hasActiveNonexemptHeadFailure(checks) {
			v.Reasons = append([]string{blocked}, v.Reasons...)
			v.StoppedEarly = blocked
			limits = append(limits, "Testing stopped early: "+cell(blocked))
		} else {
			v.Overall, v.Reasons = CheckBlocked, []string{blocked}
		}
	} else {
		v.Overall, v.Reasons = derive(checks)
	}
	cleanErr := s.teardown(ctx, run) // every accepted close cleans up after itself; a failure is reported, never fatal
	if cleanErr != nil {
		slog.Warn("litmus: cleanup after submit failed", "run", run.ID, "error", cleanErr)
		limits = append(limits, "Cleanup after submit failed, stop leftovers with `ao testing cleanup`: "+sentences(cell(cleanErr.Error()), 400))
	}
	if diagnostics, err := intakeDiagnostics(runDir); err != nil {
		slog.Warn("litmus: could not read intake diagnostics", "run", run.ID, "error", err)
		limits = append(limits, "Private intake diagnostic: could not read intake diagnostics: "+cell(err.Error()))
	} else {
		for _, diagnostic := range diagnostics {
			limits = append(limits, "Private intake diagnostic: "+cell(diagnostic))
		}
	}
	writeVerdict := func() error {
		unlock := s.lockRun(run.ID)
		defer unlock()
		if warning := s.warnOwnerLogin(ctx, run.SessionID); warning != "" {
			v.OwnerLoginWarning = warning
		}
		inputs := verdictRenderInputs{Run: run, Verdict: v, Claims: claims, VideoSummary: videoLine(videos), At: s.clock(), Limits: limits, Deadline: deadline}
		md := inputs.render(runDir)
		if deadline {
			var n int
			_, n = neutralizeVerdictLinks(runDir, renderVerdict(run, v, claims, videos, runDir, inputs.At, limits))
			brokenLinks += n
			if brokenLinks > 0 {
				when := "At the deadline"
				limits = append(limits, fmt.Sprintf("%s, %d broken relative link(s) were shown as plain text.", when, brokenLinks))
				inputs.Limits = limits
				md = inputs.render(runDir)
			}
		}
		if err := s.store.put(run.ID, verdictRenderFile, inputs); err != nil {
			return fmt.Errorf("save verdict render inputs: %w", err)
		}
		if err := s.store.put(run.ID, verdictFile, v); err != nil {
			return fmt.Errorf("save verdict: %w", err)
		}
		if err := s.store.WriteFile(run.ID, "verdict.md", []byte(md)); err != nil {
			return err
		}
		if err := publishArtifacts(sessionartifacts.Dir(s.store.dataDir(), domain.SessionID(run.SessionID)), runDir, md, renderIndex(run, v, videos), videos); err != nil {
			return fmt.Errorf("copy evidence to the session artifact dir: %w", err)
		}
		return nil
	}
	if err := writeVerdict(); err != nil {
		slog.Warn("litmus: could not finalize verdict", "run", run.ID, "error", err)
		return Verdict{}, err
	}
	if s.mustAsk(run, v) { // compose the comment and stop: nothing is published or posted until the user approves it
		if _, _, err := s.composeComment(run, v); err != nil {
			slog.Warn("litmus: could not compose comment", "run", run.ID, "error", err)
			return Verdict{}, fmt.Errorf("write comment.md: %w", err)
		}
		v.CommentState, v.EvidenceState = CommentAwaiting, EvidenceOff
		if _, err := s.update(run.ID, func(cur *Run) {
			cur.State, cur.CommentState, cur.EvidenceState = RunAwaitingApproval, v.CommentState, v.EvidenceState
		}); err != nil {
			return v, err
		}
	} else {
		if _, err := s.update(run.ID, func(cur *Run) {
			cur.State = RunSubmitted
		}); err != nil {
			return v, err
		}
		var ev evidence
		postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), PostBudget)
		v.CommentURL, v.CommentState, ev = s.postComment(postCtx, run, v) // never fails the submit
		cancel()
		v.EvidenceState, v.EvidenceCommit = ev.state, ev.commit
		if _, err := s.update(run.ID, func(cur *Run) {
			cur.CommentURL, cur.CommentState, cur.EvidenceState, cur.EvidenceCommit = v.CommentURL, v.CommentState, v.EvidenceState, v.EvidenceCommit
			cur.VideoState, cur.VideoURLs = ev.videoState, ev.videoURLs()
		}); err != nil {
			slog.Warn("litmus: could not save the comment state", "run", run.ID, "error", err)
		}
	}
	if s.report != nil { // the tester's last act: the session closes itself without `ao report --done`
		note := fmt.Sprintf("Litmus verdict %s for %s#%d: see verdict.md in the session artifacts.", v.Overall, run.Repo, run.PRNumber)
		reportCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), doneReportBudget)
		if err := s.report(reportCtx, run.SessionID, note); err != nil {
			slog.Warn("litmus: done report failed", "run", run.ID, "session", run.SessionID, "error", err)
		}
		cancel()
	}
	if s.kill != nil {
		time.AfterFunc(endGrace, func() {
			kctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.kill(kctx, run.SessionID); err != nil {
				slog.Warn("litmus: could not stop the tester after its run closed", "run", run.ID, "session", run.SessionID, "error", err)
			}
		})
	}
	s.autoPrune(ctx, run) // the retention pass every finished run ends with; never fails the submit
	if s.sweepAfterClose != nil {
		s.sweepAfterClose()
	}
	return v, nil
}

func cell(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), " "), "|", `\|`)
}

// relPath is p relative to the run dir, which is also where it sits in the session artifact dir.
func relPath(runDir, p string) string {
	if rel, err := filepath.Rel(runDir, p); err == nil && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(p)
}

// fileExists reports whether p is a regular file: the verdict links only to evidence that is there.
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// untestedLimit is the standing last line of every Limits list.
const untestedLimit = "Only the checks above were tried; the rest of the PR's changes are untested."

// compareCells is a check's head and base cells in the checks table. beside is true for a base-leg check
// that is shown beside its head twin and so gets no row of its own.
func environmentCell(c Check) string {
	if c.unreached() {
		return "Litmus couldn't test this: " + cell(c.EnvironmentCause)
	}
	return string(c.State) + " (environment: " + cell(c.EnvironmentCause) + ")"
}

func compareCells(checks []Check, c Check) (head, base string, beside bool) {
	c = checkAgainstBase(checks, c)
	if c.onLeg() == "base" {
		if slices.ContainsFunc(checks, func(h Check) bool { return h.onLeg() == "head" && strings.EqualFold(h.Name, c.Name) }) {
			return "", "", true
		}
		if c.Attribution == "environment" {
			return "-", environmentCell(c), false
		}
		return "-", string(c.State), false
	}
	switch base = baseResult(checks, c); {
	case base == string(CheckSkipped):
		base = "not compared on base"
	case base != "":
	case c.unreached():
		base = "-"
	default:
		base = "not compared on base"
	}
	if c.Attribution == "environment" {
		return environmentCell(c), base, false
	}
	if preExisting(checks, c) {
		return string(c.State) + " (pre-existing)", base, false
	}
	return string(c.State), base, false
}

// checkLimit is the Limits line a check earns, or "": the tester could not reach it, or the base never showed it.
func checkLimit(checks []Check, c Check) string {
	c = checkAgainstBase(checks, c)
	switch {
	case c.Attribution == "environment":
		return fmt.Sprintf("%s %s (%s leg): %s", c.ID, c.Name, c.onLeg(), environmentCell(c))
	case c.unreached():
		return fmt.Sprintf("%s %s (%s leg): not checked. %s", c.ID, c.Name, c.onLeg(), cell(c.Note))
	case preExisting(checks, c):
		return fmt.Sprintf("%s %s: pre-existing, same on base: %s", c.ID, c.Name, cell(c.Note))
	case c.onLeg() == "head" && baseResult(checks, c) == "":
		return fmt.Sprintf("%s %s: not compared on base, so whether the behavior changed cannot be told.", c.ID, c.Name)
	}
	return ""
}

func renderVerdict(run Run, v Verdict, claims bool, videos []legVideo, runDir string, now time.Time, limits []string) string {
	return renderVerdictDocument(run, v, claims, videoLine(videos), runDir, now, limits)
}

func renderVerdictDocument(run Run, v Verdict, claims bool, videoSummary, runDir string, now time.Time, limits []string) string {
	allChecks := v.Checks
	v.Checks = activeReplacementChecks(v.Checks)
	for i := range v.Checks {
		v.Checks[i] = checkAgainstBase(v.Checks, v.Checks[i])
	}
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	if v.OwnerLoginWarning != "" {
		p("%s\n", v.OwnerLoginWarning)
	}
	tally := count(v.Checks)
	headline := string(v.Overall)
	if v.Overall == CheckPass || v.Overall == CheckPartial {
		headline = verdictLine(v.Overall, tally, run.baseName())
	}
	p("# Verdict: %s\n\n%s#%d, run %s, base %s, head %s\n", headline, run.Repo, run.PRNumber, run.ID, cmp.Or(run.BaseSHA, "none"), cmp.Or(run.HeadSHA, "none"))
	if section, err := homeChangesSection(runDir); err != nil {
		p("## Owner home changes\n\n%s\n\nUnverified: %s\n", homeChangesCaveat, cell(err.Error()))
	} else if section != "" {
		p("%s", section)
	}
	if v.Overall != CheckBlocked {
		p("%s\n", coverageLine(tally, run.baseName()))
	}
	if worth := worthALookRows(run, v.Checks); len(worth) > 0 {
		p("## Worth a look\n\n%s\n", strings.Join(worth, "\n"))
	}
	duration := now.Sub(run.CreatedAt).Round(time.Second).String() + ", never prepared"
	if run.prepared() {
		builds, testing := run.buildsAndTesting(now)
		if endedAt := run.testingEndedAt(now); endedAt.IsZero() {
			duration = fmt.Sprintf("builds %s, testing time unavailable (end not recorded)", builds.Round(time.Second))
		} else {
			finalizing := max(now.Sub(endedAt), 0)
			budget := fmt.Sprintf("%dm", int(RunBudget/time.Minute))
			duration = fmt.Sprintf("builds %s, testing %s of a %s budget, finalizing %s", builds.Round(time.Second), testing.Round(time.Second), budget, finalizing.Round(time.Second))
		}
	}
	p("Duration: %s. Tokens: not measured by Litmus, see the session's usage. Video: %s.\n", duration, videoSummary)
	if v.Overall == CheckBlocked {
		p("## Blocked\n\n%s\n", strings.Join(v.Reasons, "\n"))
		if run.Error != "" {
			p("Daemon: %s\n", run.Error)
		}
	}
	if v.StoppedEarly != "" {
		p("Stopped early: %s\n", v.StoppedEarly)
	}
	if v.Summary != "" {
		p("## Summary\n\n%s\n", v.Summary)
	}
	var gaps []string
	for _, g := range run.Gaps {
		if strings.HasPrefix(g, "The PR build started without its sample content (seed step ") || strings.HasPrefix(g, "The main build started without its sample content (seed step ") ||
			g == "The PR build's recording was not available." || g == "The main build's recording was not available." {
			limits = append(limits, g)
		} else {
			gaps = append(gaps, g)
		}
	}
	if len(gaps) > 0 {
		p("## Gaps\n\nWhat intake could not get; none of it changes the result.\n")
		for _, g := range gaps {
			p("- %s", g)
		}
		p("")
	}
	if len(v.IntakeClaims) > 0 {
		p("## PR claims from intake\n\nThe PR author's words; coverage is the tester's explicit choice.\n")
		for _, words := range v.IntakeClaims {
			p("%s\n", quote(words))
		}
		for _, c := range v.Checks {
			if c.Claim && c.onLeg() == "head" {
				p("- %s covers: %s", c.ID, cell(c.ClaimSentence))
			}
		}
		p("")
	}
	if len(v.IntakeClaims) == 0 {
		p("claim sentences not checked\n")
	}
	for _, c := range v.Checks {
		for _, r := range c.Reclassifications {
			p("- %s reclassified %s -> %s: %s", c.ID, cell(r.From.Purpose), cell(r.To.Purpose), cell(r.Reason))
		}
	}
	if lines := replacementLines(allChecks); len(lines) > 0 {
		p("\n## Replacements\n")
		for _, line := range lines {
			p("%s", line)
		}
	}
	if len(v.Checks) > 0 {
		p("\n## Checks\n\n| check | purpose | expect | head result | base result | receipts |\n|---|---|---|---|---|---|")
	}
	for _, c := range v.Checks {
		head, base, beside := compareCells(v.Checks, c)
		if beside {
			continue // shown beside its head check
		}
		var links []string
		for _, r := range c.Receipts {
			switch {
			case r.AfterPath == "":
			case fileExists(r.AfterPath):
				links = append(links, fmt.Sprintf("[%d](%s)", r.Seq, relPath(runDir, r.AfterPath)))
			default:
				limits = append(limits, fmt.Sprintf("%s receipt %d: its after-shot file is missing, so it is not linked.", c.ID, r.Seq))
			}
		}
		purpose := c.Purpose
		if purpose == "risk" {
			purpose += ": " + c.Risk
		}
		p("| %s %s | %s | %s | %s | %s | %s |", c.ID, cell(c.Name), cell(purpose), cell(c.Expect), head, base, strings.Join(links, " "))
	}
	var skipped, superseded []string
	for _, effect := range verdictChecks(v.Checks) {
		c := effect.Check
		if effect.supersededBy != "" {
			superseded = append(superseded, fmt.Sprintf("- %s superseded by %s", c.ID, effect.supersededBy))
		} else if c.Attribution == "environment" && c.unreached() {
			skipped = append(skipped, fmt.Sprintf("- %s %s (%s leg): Litmus couldn't test this: %s. Receipt %d.", c.ID, cell(c.Name), c.onLeg(), cell(c.EnvironmentCause), c.AttributionReceipt))
		} else if c.unreached() {
			skipped = append(skipped, fmt.Sprintf("- %s %s (%s leg): %s", c.ID, cell(c.Name), c.onLeg(), cell(c.Note)))
		}
	}
	if len(skipped) > 0 {
		p("\n## Litmus couldn't test this\n\n%s\n", strings.Join(skipped, "\n"))
	}
	if len(superseded) > 0 {
		p("\n## Superseded skips\n\n%s\n", strings.Join(superseded, "\n"))
	}
	if slices.ContainsFunc(v.Checks, func(c Check) bool { return c.Attribution != "environment" || !c.unreached() }) {
		p("## Notes\n")
	}
	for _, c := range v.Checks {
		if c.Attribution == "environment" && c.unreached() {
			continue
		}
		note, _ := splitWorthNotes(c.Note)
		p("### %s %s (%s leg): %s\n\nExpected: %s\n\nNote: %s\n", c.ID, c.Name, c.onLeg(), c.State, c.Expect, note)
		if c.Attribution == "environment" {
			p("%s\n", environmentPhrase(c))
		}
		switch {
		case c.Diff == "":
		case fileExists(c.Diff):
			p("Base vs head pixel diff: [%s](%s)\n", filepath.Base(c.Diff), relPath(runDir, c.Diff))
		default:
			limits = append(limits, fmt.Sprintf("%s pixel diff: the image file is missing, so it is not linked.", c.ID))
		}
	}
	acts := readActions(runDir)
	if len(run.Legs) > 0 {
		p("## Logs\n")
	}
	for _, l := range run.Legs {
		tail := tailFile(filepath.Join(runDir, l.Name, "logs", "dev.log"), 30)
		p("### %s\n\nDaemon log tail:\n\n~~~~\n%s\n~~~~\n\nConsole errors:\n\n~~~~\n%s\n~~~~\n", l.Name, strings.ReplaceAll(tail, "~~~", "---"), strings.ReplaceAll(consoleErrors(acts, l.Name), "~~~", "---"))
	}
	p("## Limits\n")
	if !slices.ContainsFunc(run.Legs, func(l Leg) bool { return l.Name == "base" && l.State == "running" }) {
		p("- The base leg was not launched, so nothing was observed on it.")
	}
	for _, effect := range verdictChecks(v.Checks) {
		if effect.supersededBy != "" {
			continue
		}
		if l := checkLimit(v.Checks, effect.Check); l != "" {
			p("- %s", l)
		}
	}
	p("- %s", headlineRule)
	p("- The PR title %s a behavior change. That is informational and does not change the result.", map[bool]string{true: "claims", false: "does not claim"}[claims])
	if slices.ContainsFunc(acts, func(a actionRec) bool { return a.Unassigned }) {
		p("- Some state-changing actions ran while no check on their leg was open; they are not evidence for any check.")
	}
	for _, l := range limits {
		p("- %s", l)
	}
	p("- %s", untestedLimit)
	return b.String()
}

func renderIndex(run Run, v Verdict, videos []legVideo) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	p("# Litmus: %s#%d, verdict %s\n\nFull report: [verdict.md](verdict.md).\n", run.Repo, run.PRNumber, v.Overall)
	var links []string
	for _, vid := range videos {
		for _, f := range vid.files() {
			links = append(links, fmt.Sprintf("[%s](%s)", f, f))
		}
	}
	if len(links) > 0 {
		p("Video: %s.\n", strings.Join(links, ", "))
	}
	for _, c := range v.Checks {
		if replacementRetired(v.Checks, c) {
			continue
		}
		p("## %s %s: %s\n", c.ID, c.Name, c.State)
		for _, r := range c.Receipts {
			if r.AfterPath != "" && fileExists(r.AfterPath) {
				p(`<img src="receipts/%s" alt="%s %s" width="480">`, filepath.Base(r.AfterPath), r.onLeg(), r.Action)
			}
		}
		if c.Diff != "" && fileExists(c.Diff) {
			p(`<img src="receipts/%s" alt="pixel diff" width="480">`, filepath.Base(c.Diff))
		}
		p("")
	}
	return b.String()
}

// publishArtifacts copies the receipts and each leg's videos into the session's artifact dir and writes verdict.md and
// index.md beside them; index.md with relative <img> links is how AO shows content inline in the session.
func publishArtifacts(dir, runDir, verdictMD, indexMD string, videos []legVideo) error {
	if dir == "" {
		return errors.New("no artifact dir for this session")
	}
	if err := os.MkdirAll(filepath.Join(dir, "receipts"), 0o750); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(runDir, "receipts", "*"))
	copies := map[string]string{} // source -> destination
	for _, f := range files {
		copies[f] = filepath.Join(dir, relPath(runDir, f))
	}
	for _, video := range videos {
		if video.raw {
			copies[cmp.Or(video.source, recordingPath(runDir, video.leg))] = filepath.Join(dir, video.leg+".mp4")
		}
		if video.cut {
			copies[cutPath(runDir, video.leg)] = filepath.Join(dir, video.leg+"-cut.mp4")
		}
		for _, clip := range video.clips {
			copies[clip] = filepath.Join(dir, "clips", filepath.Base(clip))
		}
	}
	for src, dst := range copies {
		if fileExists(src) {
			if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
				return err
			}
			if err := copyFile(src, dst); err != nil {
				return err
			}
		}
	}
	if err := writeAtomic(filepath.Join(dir, "verdict.md"), []byte(verdictMD)); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "index.md"), []byte(indexMD))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

// readActions loads actions.jsonl up to its first unreadable line.
func readActions(runDir string) (recs []actionRec) {
	f, err := os.Open(filepath.Join(runDir, "actions.jsonl"))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	for dec := json.NewDecoder(f); dec.More(); {
		var r actionRec
		if dec.Decode(&r) != nil {
			break
		}
		recs = append(recs, r)
	}
	return recs
}

// consoleErrors collects the distinct console lines mentioning an error that a leg's receipts captured.
func consoleErrors(acts []actionRec, leg string) string {
	var out []string
	for _, a := range acts {
		for _, l := range strings.Split(a.ConsoleTail, "\n") {
			if l = strings.TrimSpace(l); a.Leg == leg && strings.Contains(strings.ToLower(l), "error") && !slices.Contains(out, l) && len(out) < 10 {
				out = append(out, clip(l, 300))
			}
		}
	}
	return strings.Join(out, "\n")
}

// tailFile returns the last n lines of a file, reading only its final 64 KiB.
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return "(no log)"
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil && st.Size() > 64<<10 {
		_, _ = f.Seek(-(64 << 10), io.SeekEnd)
	}
	data, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	lines := strings.Split(lastLines(data, n), "\n")
	for i, l := range lines {
		lines[i] = clip(l, 240)
	}
	return strings.Join(lines, "\n")
}

// clip bounds raw diagnostic output and identifiers. Authored prose uses
// sentences instead, so a report never cuts a tester's sentence in half.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
