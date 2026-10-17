package litmus

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// commentDoc is one GitHub comment and its local draft. The first document is
// the only one that carries the run marker; the other documents are addressed
// by the ids recorded alongside the run's drafts.
type commentDoc struct {
	Name      string
	Body      string
	Marker    bool
	ImageFile string
	ClipFile  string
	Number    int
	Fixed     bool
	CommentID int64
}

type commentManifestEntry struct {
	Name   string
	File   string
	Number int
	Fixed  bool
	ID     int64
}

// PostedComment is the durable mapping from a check name to its PR comment.
// Number is the bug number used in the rendered body; Fixed keeps reruns from
// treating a resolved comment as a new bug.
type PostedComment struct {
	Name   string `json:"name"`
	ID     int64  `json:"id,omitempty"`
	URL    string `json:"url,omitempty"`
	Number int    `json:"number,omitempty"`
	Fixed  bool   `json:"fixed,omitempty"`
}

// bugComment is a head-side failure that the available evidence does not
// establish as pre-existing.
type bugComment struct {
	Check     Check
	Linked    []Check
	Base      *Check
	Number    int
	Total     int
	ImageFile string
	ImageURL  string
	ClipFile  string
	ClipURL   string
}

// Comment modes, set on the run by intake.
const (
	CommentPost = "post"
	CommentOff  = "off"
)

const (
	commentCap     = 6000 // GitHub allows far more; a PR comment this long is not read
	commentTimeout = 60 * time.Second
	bugImageAlt    = "main on the left and the PR build on the right"
)

// commentFit controls optional labels and run-wide limits.
// Check rows have their own sentence budget; full notes stay in details.
type commentFit struct{ note, limit, bullets, notes int }

// commentFits are the optional-detail settings retained by older render callers.
var commentFits = []commentFit{{80, 300, 12, 600}, {80, 150, 6, 300}, {80, 80, 3, 150}, {60, 60, 2, 0}, {40, 40, 1, 0}}

func commentMarker(repo string, n int) string {
	return fmt.Sprintf("<!-- litmus:comment %s#%d -->", repo, n)
}

// litmusCommentMarker is the marker used by the per-bug comment set. The
// repository-qualified form above is retained for comments made by older
// Litmus runs and is accepted when reading them.
func litmusCommentMarker() string { return "<!-- litmus:comment -->" }

func commentChecksAgainstBase(checks []Check) []Check {
	// Replaced checks are historical ledger entries. They must not appear
	// in the public comment or act as a base twin for the replacement.
	active := activeReplacementChecks(checks)
	resolved := active
	for i := range resolved {
		resolved[i] = checkAgainstBase(active, active[i])
	}
	return resolved
}

// failingBugComments returns head failures that can be shown as regressions.
// Eligibility belongs to each check. Resolve links only after that decision so
// an ineligible root cannot hide an eligible linked check.
func failingBugComments(checks []Check) []bugComment {
	resolved := commentChecksAgainstBase(checks)
	eligible := make(map[string]bool, len(resolved))
	for _, c := range resolved {
		eligible[c.ID] = eligibleBugCheck(resolved, c)
	}

	rootFor := make(map[string]string, len(resolved))
	for _, c := range resolved {
		if !eligible[c.ID] {
			continue
		}
		rootFor[c.ID] = nearestEligibleBugAncestor(resolved, c.ID, eligible)
	}

	var groups []bugComment
	byRoot := make(map[string]int, len(rootFor))
	for _, c := range resolved {
		rootID, ok := rootFor[c.ID]
		if !ok {
			continue
		}
		index, ok := byRoot[rootID]
		if !ok {
			root, found := checkByID(resolved, rootID)
			if !found {
				continue
			}
			byRoot[rootID] = len(groups)
			groups = append(groups, bugComment{Check: root, Base: baseTwinCheck(resolved, root)})
			index = len(groups) - 1
		}
		if c.ID != rootID {
			groups[index].Linked = append(groups[index].Linked, c)
		}
	}

	numberBugComments(groups)
	return groups
}

// eligibleBugCheck applies the bug rule to one head check before links are
// resolved. Unknown legacy causes remain visible when the verdict records a
// failure. Environment and proven pre-existing observations stay in the ledger.
func eligibleBugCheck(checks []Check, c Check) bool {
	if c.onLeg() != "head" || (c.State != CheckFail && c.State != CheckPartial && c.State != CheckAbsent) {
		return false
	}
	original, found := checkByID(checks, c.ID)
	if !found {
		original = c
	}
	// checkAgainstBase can turn an environment attribution into an unknown
	// PR difference when main passed. Use that resolved result here so a real
	// regression still gets a bug section. Same-fault evidence is independent
	// of the verdict's pre-existing risk/exploration rule and never becomes a
	// PR bug, including when the check is a claim.
	if c.Attribution == "environment" || preExisting(checks, c) || sameFaultFailure(checks, original) {
		return false
	}
	base := baseTwinCheck(checks, c)
	if base != nil && base.unreached() {
		base = nil
	}
	if base == nil {
		return c.State == CheckFail || c.State == CheckAbsent || c.Attribution != ""
	}
	if c.State == CheckAbsent || c.Attribution == "pr" {
		return true
	}
	if base.State == CheckPass || base.State == CheckAbsent {
		return true
	}
	// derive treats a failing main check with a saved after-shot as an
	// unresolved same fault unless the tester marked the head result as PR. A
	// base failure without that evidence, and a base partial result, do not
	// establish that this head failure is pre-existing.
	if c.State == CheckFail {
		return base.State != CheckFail || !ranAndFailed(*base)
	}
	return false
}

func checkByID(checks []Check, id string) (Check, bool) {
	for _, c := range checks {
		if c.ID == id {
			return c, true
		}
	}
	return Check{}, false
}

// nearestEligibleBugAncestor follows at most len(checks) links. A missing
// target or a cycle makes the link local to the check, so malformed stored
// data cannot hang rendering.
func nearestEligibleBugAncestor(checks []Check, id string, eligible map[string]bool) string {
	seen := make(map[string]bool, len(checks))
	current := id
	root := id
	for steps := 0; steps < len(checks); steps++ {
		if seen[current] {
			return id
		}
		seen[current] = true
		c, found := checkByID(checks, current)
		if !found || strings.TrimSpace(c.SameBug) == "" {
			return root
		}
		targetID := strings.TrimSpace(c.SameBug)
		if seen[targetID] {
			return id
		}
		if _, found := checkByID(checks, targetID); !found {
			return id
		}
		current = targetID
		if eligible[current] {
			root = current
		}
	}
	return root
}

// originalProblemLine calls out claims that failed on main with an after-shot
// and passed on the PR build.
func originalProblemLine(run Run, checks []Check) string {
	checks = commentChecksAgainstBase(checks)
	count := 0
	for _, c := range checks {
		if c.onLeg() != "head" || !c.Claim || c.State != CheckPass {
			continue
		}
		i := baseTwin(checks, c)
		if i < 0 {
			continue
		}
		base := checks[i]
		if base.Claim && ranAndFailed(base) {
			count++
		}
	}
	if count == 0 {
		return ""
	}
	return fmt.Sprintf("%s reproduces the original problem in %d claim %s that %s on the PR build.",
		recase(run.baseName(), true), count, plural(count, "check", "checks"), map[bool]string{true: "passes", false: "pass"}[count == 1])
}

func postedCommentFor(comments []PostedComment, name string) (PostedComment, bool) {
	for _, c := range comments {
		if strings.EqualFold(strings.TrimSpace(c.Name), strings.TrimSpace(name)) {
			return c, true
		}
	}
	return PostedComment{}, false
}

func (s *Service) previousComments(run Run) []PostedComment {
	current := slices.Clone(run.Comments)
	runs, err := s.store.List()
	if err != nil {
		return current
	}
	for i := len(runs) - 1; i >= 0; i-- {
		other := runs[i]
		if other.ID == run.ID || other.Repo != run.Repo || other.PRNumber != run.PRNumber || len(other.Comments) == 0 {
			continue
		}
		return mergeCommentMappings(current, other.Comments)
	}
	return current
}

func numberBugComments(bugs []bugComment) {
	for i := range bugs {
		bugs[i].Number, bugs[i].Total = i+1, len(bugs)
	}
}

var bugCommentHeading = regexp.MustCompile(`^Bug [0-9]+ of [0-9]+:`)

// baseTwinCheck is the check-name form of baseTwin. It returns nil when main
// was not repeated, which is still useful evidence for a head-only failure.
func baseTwinCheck(checks []Check, c Check) *Check {
	if replacementRetired(checks, c) {
		return nil
	}
	var fallback *Check
	for i := range checks {
		b := &checks[i]
		if replacementRetired(checks, *b) || b.onLeg() != "base" || !strings.EqualFold(b.Name, c.Name) || !slices.Contains(outcomes, b.State) {
			continue
		}
		if b.State == CheckPass || b.State == CheckAbsent {
			return b
		}
		fallback = b
	}
	return fallback
}

// bugCommentBody is deliberately small enough to leave room for one image
// and one check recording. Image and clip are repository URLs after evidence
// publishing, or run-relative files while a draft waits for approval.
func bugCommentBody(run Run, v Verdict, b bugComment, image, clip string, marker bool, showChecks ...bool) string {
	allChecks := v.Checks
	v.Checks = commentChecksAgainstBase(v.Checks)
	b.Check = checkAgainstBase(v.Checks, b.Check)
	var parts []string
	if b.Number == 1 {
		parts = append(parts, fmt.Sprintf("**Result: %s.**", commentResultLine(v.Overall, count(v.Checks), run.baseName())))
		if summary := testerSummary(v.Summary); summary != "" {
			parts = append(parts, summary)
		}
	}
	note := strings.TrimSpace(b.Check.Note)
	if note == "" {
		note = recase(plainSentence(outcomeWords[b.Check.State], math.MaxInt), true)
	}
	parts = append(parts, fmt.Sprintf("Bug %d of %d:", b.Number, b.Total), mdText(noteSentencePrefix(note, 2), math.MaxInt))
	if len(showChecks) > 0 && showChecks[0] {
		if line := originalProblemLine(run, v.Checks); line != "" {
			parts = append(parts, line)
		}
	}
	if len(b.Linked) > 0 {
		labels := []string{compactCheckLabel(b.Check)}
		for _, c := range b.Linked {
			labels = append(labels, compactCheckLabel(c))
		}
		parts = append(parts, "Checks: "+strings.Join(labels, "; "))
	}
	if len(showChecks) > 0 && showChecks[0] {
		if worth := worthALookRows(run, v.Checks); len(worth) > 0 {
			parts = append(parts, "Worth a look:\n\n"+strings.Join(worth, "\n"))
		}
	}
	if v.StoppedEarly != "" {
		parts = append(parts, "Stopped early: "+plainSentence(plainTester(v.StoppedEarly, run, v.Checks), math.MaxInt))
	}
	console := compareConsoleErrors(b.Check, v.Checks)
	if len(console.lines) > 0 {
		details := "<details><summary>App console errors during this check</summary>\n\n" + mdTextLines(strings.Join(console.lines, "\n\n"))
		if !console.compared {
			details += "\n\nNo console errors were recorded on " + run.baseName() + "."
		}
		parts = append(parts, details+"\n\n</details>")
	}
	if image != "" {
		parts = append(parts, "!["+bugImageAlt+"]("+image+")")
		if pair, err := selectBugImage(b, allChecks); err == nil {
			parts = append(parts, pair.caption())
		}
	} else {
		parts = append(parts, "A comparison image could not be made.")
	}
	if clip != "" {
		parts = append(parts, "<details><summary>Recording for "+mdText(strings.ReplaceAll(b.Check.Name, "-", " "), 120)+"</summary>\n\n[Watch the check]("+clip+")\n\n</details>")
	} else {
		parts = append(parts, "<details><summary>Recording for "+mdText(strings.ReplaceAll(b.Check.Name, "-", " "), 120)+"</summary>\n\nThe check recording was not available.\n\n</details>")
	}
	if len(showChecks) > 0 && showChecks[0] {
		if lists := compactChecks(run, v, failingBugComments(v.Checks)); lists != "" {
			parts = append(parts, lists)
		}
	}
	if len(showChecks) > 0 && showChecks[0] {
		if notes := fullCheckNotes(run, v); notes != "" {
			parts = append(parts, notes)
		}
	}

	if marker {
		parts = append(parts, litmusCommentMarker())
	}
	return strings.Join(parts, "\n\n")
}

// consolePaths matches URLs as well as paths so remote URLs stay intact.
var consolePaths = regexp.MustCompile("(?:[a-zA-Z][a-zA-Z0-9+.-]*://|[a-zA-Z]:[\\\\/]|/)[^\\s\"'`<>()]+")

func checkConsoleErrors(c Check) []string {
	return consoleLines(c, "head")
}

type consoleErrorComparison struct {
	lines    []string
	compared bool
}

func compareConsoleErrors(c Check, checks []Check) consoleErrorComparison {
	head, _ := consoleLineRecords(c, "head")
	base, baseAvailable := consoleLineRecords(c, "base")
	if twin := baseTwinCheck(checks, c); twin != nil {
		baseRecords, available := consoleLineRecords(*twin, "base")
		base = append(base, baseRecords...)
		baseAvailable = baseAvailable || available
	}
	if !baseAvailable {
		return consoleErrorComparison{lines: limitConsoleLines(consoleLineTexts(head))}
	}
	baseKeys := make(map[string]bool, len(base))
	for _, line := range base {
		baseKeys[line.key] = true
	}
	var onlyHead []consoleLine
	for _, line := range head {
		if !baseKeys[line.key] {
			onlyHead = append(onlyHead, line)
		}
	}
	return consoleErrorComparison{lines: limitConsoleLines(consoleLineTexts(onlyHead)), compared: true}
}

func consoleLines(c Check, leg string) []string {
	lines, _ := consoleLineRecords(c, leg)
	return limitConsoleLines(consoleLineTexts(lines))
}

func limitConsoleLines(lines []string) []string {
	return lines[:min(len(lines), 5)]
}

type consoleLine struct {
	text string
	key  string
}

func consoleLineTexts(lines []consoleLine) []string {
	texts := make([]string, len(lines))
	for i, line := range lines {
		texts[i] = line.text
	}
	return texts
}

func consoleLineRecords(c Check, leg string) ([]consoleLine, bool) {
	var lines []consoleLine
	seen := map[string]bool{}
	available := false
	for _, r := range c.Receipts {
		receiptLeg := r.onLeg()
		if r.Leg == "" && c.Leg != "" {
			receiptLeg = c.Leg
		}
		if receiptLeg != leg {
			continue
		}
		if strings.TrimSpace(r.ConsoleTail) != "" {
			available = true
		}
		for _, line := range strings.Split(r.ConsoleTail, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			key := normalizeConsoleError(line)
			if seen[key] {
				continue
			}
			chars := []rune(line)
			line = string(chars[:min(len(chars), 200)]) // raw console output keeps its exact cap
			line = consolePaths.ReplaceAllStringFunc(line, func(path string) string {
				if strings.Contains(path, "://") && !strings.HasPrefix(path, "file://") {
					return path
				}
				return filepath.Base(strings.ReplaceAll(path, `\`, "/"))
			})
			seen[key] = true
			lines = append(lines, consoleLine{text: line, key: key})
		}
	}
	return lines, available
}

// Console tails often repeat the same error with a fresh request id, port, or
// timestamp on every receipt. Normalize those volatile values for de-duplication
// while retaining the first readable line in the published comment.
var consoleVolatile = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:(?:request|req|trace|session|run)(?:[_-]?id)?|id)[=:][[:alnum:]_-]+`),
	regexp.MustCompile(`(?i)\b(?:request|req|trace|session|run)[_-][[:alnum:]-]+\b`),
	regexp.MustCompile(`(?i)\b(?:port|PORT)[=: ]+[0-9]+\b`),
	regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`),
	regexp.MustCompile(`(?i)\b(?:hash|sha|checksum)(?:[_-]?id)?[=:][[:alnum:]_-]+`),
	regexp.MustCompile(`(?i)\b[0-9a-f]{7,64}\b`),
	regexp.MustCompile(`\b[0-9]{4}-[0-9]{2}-[0-9]{2}[T ][0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})?\b`),
	regexp.MustCompile(`(?i)\b(?:timestamp|time|ts)\s*[:=]\s*[0-9]{10,16}\b`),
	regexp.MustCompile(`(?:^|[[:space:]\[])[0-9]{1,2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})?`),
	regexp.MustCompile(`(?i)\b(?:localhost|127\.0\.0\.1|\[::1\]):[0-9]{2,5}\b`),
	regexp.MustCompile(`(?i)//[^/\s:]+:[0-9]{2,5}\b`),
}

func normalizeConsoleError(line string) string {
	line = consolePaths.ReplaceAllString(line, "<path>")
	for _, re := range consoleVolatile {
		line = re.ReplaceAllString(line, "<value>")
	}
	return strings.Join(strings.Fields(line), " ")
}

// summaryCommentBody gives the result, claim outcomes, and skipped-check reasons.
func summaryCommentBody(run Run, v Verdict, now time.Time, marker bool) string {
	allChecks := v.Checks
	v.Checks = commentChecksAgainstBase(v.Checks)
	var paragraphs []string
	main := run.baseName()
	both := slices.ContainsFunc(v.Checks, func(c Check) bool {
		return c.onLeg() == "base" || slices.ContainsFunc(c.Receipts, func(r Receipt) bool { return r.onLeg() == "base" })
	})
	started := len(run.Legs) > 0 || v.Overall != CheckBlocked || slices.ContainsFunc(v.Checks, func(c Check) bool { return len(c.Receipts) > 0 })
	if started {
		where := "on the PR build"
		if both {
			where += " and on " + main
		}
		took := lasted(now.Sub(run.CreatedAt))
		if run.prepared() {
			took = preparedTiming(run, now)
		}
		paragraphs = append(paragraphs, fmt.Sprintf("Litmus tried #%d %s (%s).", run.PRNumber, where, took))
	} else {
		paragraphs = append(paragraphs, fmt.Sprintf("Litmus could not start on #%d.", run.PRNumber))
	}
	tally := count(v.Checks)
	paragraphs = append(paragraphs, "**Result: "+commentResultLine(v.Overall, tally, main)+".**")
	if lines := replacementLines(allChecks); len(lines) > 0 {
		paragraphs = append(paragraphs, "Replacements:\n\n"+strings.Join(lines, "\n"))
	}
	if line := originalProblemLine(run, v.Checks); line != "" {
		paragraphs = append(paragraphs, line)
	}
	if v.Overall != CheckBlocked {
		paragraphs = append(paragraphs, coverageLine(tally, main))
	}
	if worth := worthALookRows(run, v.Checks); len(worth) > 0 {
		paragraphs = append(paragraphs, "Worth a look:\n\n"+strings.Join(worth, "\n"))
	}
	for _, gap := range run.Gaps {
		if text := plainSentence(gap, math.MaxInt); text != "" {
			paragraphs = append(paragraphs, text)
		}
	}
	if lists := compactChecks(run, v); lists != "" {
		paragraphs = append(paragraphs, lists)
	}
	if summary := testerSummary(v.Summary); summary != "" {
		paragraphs = append(paragraphs, summary)
	}

	if notes := fullCheckNotes(run, v); notes != "" {
		paragraphs = append(paragraphs, notes)
	}

	if marker {
		paragraphs = append(paragraphs, litmusCommentMarker())
	}
	return strings.Join(paragraphs, "\n\n")
}

// firstNoteSentence keeps a complete sentence even when it exceeds the row budget.
// Passing notes and all remaining sentences stay in the full-notes block.
func firstNoteSentence(note string) string {
	return noteSentencePrefix(note, 1)
}

var noteSentenceEnd = regexp.MustCompile(`[.!?](?:\s+|$)`)

func noteSentencePrefix(note string, limit int) string {
	note = strings.TrimSpace(note)
	ends := noteSentenceEnd.FindAllStringIndex(note, limit)
	if len(ends) == limit {
		note = strings.TrimSpace(note[:ends[limit-1][1]])
	}
	return note
}

func compactCheckLabel(c Check) string {
	return mdText(strings.ReplaceAll(c.Name, "-", " "), math.MaxInt)
}

func hasHeadTwin(checks []Check, c Check) bool {
	return c.onLeg() == "base" && slices.ContainsFunc(checks, func(h Check) bool {
		return h.onLeg() == "head" && strings.EqualFold(h.Name, c.Name)
	})
}

// commentBaseTwin includes unfinished checks so their notes are retained too.
func commentBaseTwin(checks []Check, c Check) *Check {
	if replacementRetired(checks, c) {
		return nil
	}
	for i := range checks {
		if !replacementRetired(checks, checks[i]) && checks[i].onLeg() == "base" && strings.EqualFold(checks[i].Name, c.Name) {
			return &checks[i]
		}
	}
	return nil
}

func mainCheckResult(run Run, checks []Check, c Check) string {
	if text := mainComparison(run, checks, c); text != "" {
		return " (" + text + ")"
	}
	return ""
}

func mainComparison(run Run, checks []Check, c Check) string {
	if c.onLeg() == "base" {
		return ""
	}
	if twin := commentBaseTwin(checks, c); twin != nil {
		status := mainStatus(run.baseName(), twin.State, checkPurpose(c))
		if twin.Attribution == "environment" {
			if detail := environmentDetail(*twin); detail != "" {
				status += ", environment: " + detail
			}
		}
		return status
	}
	return run.baseName() + ": not compared"
}

func mainStatus(main string, state CheckState, purpose string) string {
	if purpose == "claim" {
		switch state {
		case CheckPass:
			return main + " already does this"
		case CheckFail:
			return main + " does not do this"
		case CheckPartial:
			return main + " does part of this"
		}
	}
	switch state {
	case CheckAbsent:
		return "not on " + main
	case CheckFail:
		return "fails on " + main
	case CheckPartial:
		return "partly broken on " + main
	case CheckPass:
		return "works on " + main
	case CheckSkipped, CheckPending, CheckRunning:
		return "not tested on " + main
	default:
		return "not compared on " + main
	}
}

// checkPurpose keeps older stored checks, which only have Claim, on the same
// wording path as current checks with an explicit purpose.
func checkPurpose(c Check) string {
	if c.Purpose == "claim" || c.Claim {
		return "claim"
	}
	return c.Purpose
}

func environmentDetail(c Check) string {
	detail := strings.TrimSpace(c.EnvironmentCause)
	if detail == "" {
		detail = strings.TrimSpace(c.Note)
	}
	detail = strings.TrimSpace(strings.TrimRight(detail, "."))
	if detail == "" {
		return ""
	}
	return lowerFirst(mdText(detail, math.MaxInt))
}

func commentUntested(c Check) bool {
	return c.unreached() || !slices.Contains(outcomes, c.State)
}

// compactChecks budgets each observation rather than dropping checks to fit.
func compactChecks(run Run, v Verdict, bugSections ...[]bugComment) string {
	checks := commentChecksAgainstBase(v.Checks)
	var bugs []bugComment
	if len(bugSections) > 0 {
		bugs = bugSections[0]
	}
	var passed, rows []string
	for _, c := range checks {
		if hasHeadTwin(checks, c) || commentUntested(c) {
			continue
		}
		label := compactCheckLabel(c)
		if c.onLeg() == "base" {
			label += " (" + run.baseName() + ")"
		}
		comparison := mainCheckResult(run, checks, c)
		if c.State == CheckPass && c.Attribution != "environment" {
			passed = append(passed, label+comparison)
			continue
		}
		note := mdText(firstNoteSentence(c.Note), math.MaxInt)
		for _, b := range bugs {
			if b.Check.ID == c.ID || slices.ContainsFunc(b.Linked, func(link Check) bool { return link.ID == c.ID }) {
				note = fmt.Sprintf("See Bug %d.", b.Number)
				break
			}
		}
		if c.onLeg() == "head" && preExisting(checks, c) {
			note = "Pre-existing. " + note
		}
		if c.Attribution == "environment" {
			if detail := environmentDetail(c); detail != "" {
				note += " Environment: " + recase(detail, true) + "."
			}
		}
		rows = append(rows, strings.TrimSpace("- "+label+": "+string(c.State)+comparison+". "+note))
	}
	var parts []string
	if len(passed) > 0 {
		parts = append(parts, "Passed: "+strings.Join(passed, "; "))
	}
	if len(rows) > 0 {
		parts = append(parts, strings.Join(rows, "\n"))
	}
	if skipped := skippedCheckRows(run, checks); len(skipped) > 0 {
		parts = append(parts, "Not tested: "+strings.Join(skipped, "; "))
	}
	return strings.Join(parts, "\n\n")
}

func skippedCheckRows(run Run, checks []Check) []string {
	checks = commentChecksAgainstBase(checks)
	var rows []string
	for _, effect := range verdictChecks(checks) {
		c := effect.Check
		if hasHeadTwin(checks, c) || !commentUntested(c) || effect.supersededBy != "" {
			continue
		}
		label := compactCheckLabel(c)
		details := mdText(firstNoteSentence(cmp.Or(c.EnvironmentCause, c.Note, "No result recorded")), math.MaxInt)
		if c.onLeg() == "base" {
			details = run.baseName() + ": " + details
		} else if comparison := mainComparison(run, checks, c); comparison != "" {
			details = strings.TrimSuffix(details, ".")
			details += "; " + comparison
		}
		rows = append(rows, label+" ("+details+")")
	}
	return rows
}

// testerSummary keeps the tester's complete summary separate from generated
// check coverage, which comes from recorded checks and their skip reasons.
func testerSummary(summary string) string {
	if strings.TrimSpace(summary) == "" {
		return ""
	}
	return "Tester summary:\n\n" + mdTextLines(summary)
}

// mdTextLines applies the public-comment escaping used for notes while keeping
// the tester's paragraph breaks.
func mdTextLines(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = mdText(lines[i], math.MaxInt)
	}
	return strings.Join(lines, "\n")
}

// fullCheckNotes is the single collapsed ledger. Labels and notes stay whole.
func fullCheckNotes(run Run, v Verdict) string {
	v.Checks = commentChecksAgainstBase(v.Checks)
	if len(v.Checks) == 0 {
		return ""
	}
	parts := []string{"<details><summary>Full check notes</summary>"}
	note := func(c Check) string {
		text := recase(run.sideName(c.onLeg()), true) + ": " + cmp.Or(string(c.State), "not recorded") + "."
		if note := strings.TrimSpace(c.Note); note != "" {
			text += " " + mdText(note, math.MaxInt)
		}
		if c.Attribution == "environment" || c.EnvironmentCause != "" {
			text += " " + environmentPhrase(c)
		}
		return text
	}
	for _, c := range v.Checks {
		if hasHeadTwin(v.Checks, c) {
			continue
		}
		line := "- " + compactCheckLabel(c)
		if c.Expect != "" {
			line += ". Expected: " + plainSentence(c.Expect, math.MaxInt)
		}
		if c.Risk != "" {
			line += " Risk: " + mdText(c.Risk, math.MaxInt)
		}
		line += "\n\n  " + note(c)
		if twin := commentBaseTwin(v.Checks, c); c.onLeg() == "head" && twin != nil {
			if twin.Expect != "" && twin.Expect != c.Expect {
				line += "\n\n  " + recase(run.baseName(), true) + " expected: " + plainSentence(twin.Expect, math.MaxInt)
			}
			if twin.Risk != "" && twin.Risk != c.Risk {
				line += " Risk: " + mdText(twin.Risk, math.MaxInt)
			}
			line += "\n\n  " + note(*twin)
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n\n") + "\n\n</details>"
}

// fixedCommentBody keeps the marker on the first comment so a later rerun can
// still find the run, while the visible text is exactly the requested result.
func fixedCommentBody(sha string, marker bool) string {
	body := "Fixed at " + mdText(clip(strings.TrimSpace(sha), 80), math.MaxInt)
	if marker {
		body += "\n\n" + litmusCommentMarker()
	}
	return body
}

var githubAutolink = regexp.MustCompile(`(?i)(https?://github\.com/[[:alnum:]_.-]+/[[:alnum:]_.-]+/(issues|pull)/[0-9]+|https?://github\.com/[[:alnum:]_.-]+/[[:alnum:]_.-]+/commit/[[:xdigit:]]{7,}|[[:alnum:]_.-]+/[[:alnum:]_.-]+@[[:xdigit:]]{7,}|[[:alnum:]_.-]+@[[:xdigit:]]{7,}|[[:alnum:]_.-]+/[[:alnum:]_.-]+#[0-9]+|GH-[0-9]+|#[0-9]+|[[:xdigit:]]{7,})`)

// codeIssueReferences keeps GitHub from turning documented issue, pull request,
// and commit references into links while leaving the visible text unchanged.
func codeIssueReferences(s string) string {
	unmatched := unmatchedBackticks(s)
	var out strings.Builder
	last := 0
	for _, span := range githubAutolink.FindAllStringIndex(s, -1) {
		ref := s[span[0]:span[1]]
		if issueReferenceInsideCodeOrURL(s, span[0], span[1]) || !autolinkWordBoundary(s, span[0], span[1], ref) {
			continue
		}
		writeTextWithBacktickEscapes(&out, s, unmatched, last, span[0])
		out.WriteByte('`')
		out.WriteString(s[span[0]:span[1]])
		out.WriteByte('`')
		last = span[1]
	}
	writeTextWithBacktickEscapes(&out, s, unmatched, last, len(s))
	return out.String()
}

func autolinkWordBoundary(s string, start, end int, ref string) bool {
	if !strings.HasPrefix(strings.ToUpper(ref), "GH-") && !isBareSHA(ref) {
		return true
	}
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:start])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return false
		}
	}
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return false
		}
	}
	return true
}

func isBareSHA(ref string) bool {
	if len(ref) < 7 {
		return false
	}
	for _, r := range ref {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func issueReferenceInsideCodeOrURL(s string, start, end int) bool {
	if insideBacktickCode(s, start) {
		return true
	}
	// mdText escaped the ampersand of a literal numeric character reference.
	if s[start] == '#' && strings.HasSuffix(s[:start], "&amp;") && end < len(s) && s[end] == ';' {
		return true
	}
	from := strings.LastIndexFunc(s[:start], unicode.IsSpace) + 1
	to := len(s)
	if next := strings.IndexFunc(s[end:], unicode.IsSpace); next >= 0 {
		to = end + next
	}
	token := s[from:to]
	return (strings.Contains(token, "://") && !githubIssueOrCommitURL(token)) || strings.HasPrefix(token, "mailto:")
}

func githubIssueOrCommitURL(token string) bool {
	token = strings.TrimRight(token, ".,;:!?)]}")
	lower := strings.ToLower(token)
	return githubAutolink.MatchString(token) && (strings.Contains(lower, "/issues/") || strings.Contains(lower, "/pull/") || strings.Contains(lower, "/commit/"))
}

func writeTextWithBacktickEscapes(out *strings.Builder, s string, unmatched []bool, start, end int) {
	for i := start; i < end; {
		if !unmatched[i] {
			out.WriteByte(s[i])
			i++
			continue
		}
		out.WriteString("&#96;")
		i++
	}
}

func unmatchedBackticks(s string) []bool {
	unmatched := make([]bool, len(s))
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && s[j] == '`' {
			j++
		}
		if escapedBacktickRun(s, i) {
			for k := i; k < j; k++ {
				unmatched[k] = true
			}
			i = j
			continue
		}
		_, closeEnd := matchingBacktickRun(s, j, j-i)
		if closeEnd < 0 {
			for k := i; k < j; k++ {
				unmatched[k] = true
			}
			i = j
			continue
		}
		i = closeEnd
	}
	return unmatched
}

// mdText makes tester-written text safe for a public comment. It keeps the
// tester's words, folds whitespace to a safe single line, shortens only at
// sentence boundaries, and escapes markup and mentions.
func mdText(s string, n int) string {
	s = sentences(strings.Join(strings.Fields(s), " "), n)
	s = cell(strings.ReplaceAll(s, `\`, `\\`))
	s = strings.NewReplacer("&", "&amp;", "<", "&lt;", "[", `\[`).Replace(s)
	s = codeIssueReferences(s)
	// Commit references need their @ intact inside the code span. Neutralize
	// mentions only after references have been wrapped, and only in prose.
	var escaped strings.Builder
	codeRun := 0
	for i := 0; i < len(s); {
		if s[i] == '`' {
			end := i + 1
			for end < len(s) && s[end] == '`' {
				end++
			}
			if codeRun == 0 {
				codeRun = end - i
			} else if codeRun == end-i {
				codeRun = 0
			}
			escaped.WriteString(s[i:end])
			i = end
			continue
		}
		if s[i] == '@' && codeRun == 0 {
			escaped.WriteString("@\u200b")
		} else {
			escaped.WriteByte(s[i])
		}
		i++
	}
	s = escaped.String()
	// Every caller may place this text at a line start. Whitespace folding
	// removes indentation; escape block markers before they reach Markdown.
	if s != "" && (strings.ContainsRune("#>-+*=_", rune(s[0])) || strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~")) {
		return `\` + s
	}
	// CommonMark ordered lists accept up to nine digits and either delimiter.
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i <= 9 && i < len(s) && (s[i] == '.' || s[i] == ')') && (i+1 == len(s) || s[i+1] == ' ') {
		return s[:i] + `\` + s[i:]
	}
	return s
}

// plainSentence escapes tester prose, shortens at a sentence boundary, and adds a final stop.
func plainSentence(note string, n int) string {
	out := plainCell(note, n)
	if out == "" || strings.HasSuffix(out, "...") || strings.ContainsAny(out[len(out)-1:], "!?") {
		return out
	}
	if trailingEntity.MatchString(out) {
		return out + "."
	}
	return strings.TrimRight(out, ".;:,") + "."
}

// shorten keeps a complete sentence when one fits; otherwise it keeps the
// whole text so an older stored field never ends mid-sentence.
func shorten(s string, n int) string {
	return sentences(s, n)
}

// sentences shortens s only when a complete sentence fits in n characters.
func sentences(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		prefix := string(r[:n])
		for i := len(prefix) - 1; i > 0; i-- {
			if (prefix[i-1] == '.' || prefix[i-1] == '!' || prefix[i-1] == '?') && prefix[i] == ' ' {
				return prefix[:i]
			}
		}
	}
	return s
}

func isTesterWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || strings.ContainsRune("_-/\\`@#=&%", r)
}

// insideBacktickCode reports whether pos is inside a Markdown code span. The
// opening and closing delimiters must use the same run length, so this also
// handles double-backtick spans and triple-backtick fences.
func insideBacktickCode(s string, pos int) bool {
	for i := 0; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && s[j] == '`' {
			j++
		}
		run := j - i
		if escapedBacktickRun(s, i) {
			i = j
			continue
		}
		closeStart, closeEnd := matchingBacktickRun(s, j, run)
		if closeStart < 0 {
			i = j
			continue
		}
		if pos >= j && pos < closeStart {
			return true
		}
		if pos < closeEnd {
			return false
		}
		i = closeEnd
	}
	return false
}

func matchingBacktickRun(s string, from, want int) (int, int) {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && s[j] == '`' {
			j++
		}
		if j-i == want && !escapedBacktickRun(s, i) {
			return i, j
		}
		i = j
	}
	return -1, -1
}

func escapedBacktickRun(s string, start int) bool {
	slashes := 0
	for i := start - 1; i >= 0 && s[i] == '\\'; i-- {
		slashes++
	}
	return slashes%2 == 1
}

func testerWordAllowed(s string, start, end int) bool {
	if insideBacktickCode(s, start) {
		return false
	}
	// A URL's query and fragment are part of the same token as its path.
	from := strings.LastIndexFunc(s[:start], unicode.IsSpace) + 1
	to := len(s)
	if next := strings.IndexFunc(s[end:], unicode.IsSpace); next >= 0 {
		to = end + next
	}
	if token := s[from:to]; strings.Contains(token, "://") || strings.HasPrefix(token, "mailto:") {
		return false
	}
	if start > 0 {
		before, _ := utf8.DecodeLastRuneInString(s[:start])
		if isTesterWordRune(before) || before == '.' || before == ':' {
			return false
		}
	}
	if end < len(s) {
		after, _ := utf8.DecodeRuneInString(s[end:])
		switch {
		case isTesterWordRune(after):
			return false
		case after == ':' && end+1 < len(s):
			next, _ := utf8.DecodeRuneInString(s[end+1:])
			return unicode.IsSpace(next)
		case s[end] == '.' && !sentencePeriod(s, end):
			return false
		}
	}
	return true
}

func sentencePeriod(s string, pos int) bool {
	next, _ := utf8.DecodeRuneInString(s[pos+1:])
	return pos+1 == len(s) || unicode.IsSpace(next)
}

// plainTester preserves tester prose. Build names belong in generated labels.
func plainTester(s string, _ Run, _ []Check) string {
	return strings.TrimSpace(s)
}

func environmentPhrase(c Check) string {
	cause := strings.TrimSpace(c.EnvironmentCause)
	if cause == "" {
		cause = strings.TrimSpace(c.Note)
	}
	if cause == "" {
		cause = "the test environment was unavailable"
	}
	prefix := "Environment: "
	if c.unreached() {
		prefix = "Litmus couldn't test this: "
	}
	return prefix + plainSentence(cause, math.MaxInt)
}

// subjectLed reports whether a note already normalized for display opens with a build as its subject, "The PR build shows",
// "The main build is" or "Main has", so the comment keeps it as its own sentence instead of adding "On the PR build,".
func subjectLed(note string, run Run) bool {
	return regexp.MustCompile(`(?i)^(?:the (?:PR|main) build|main|` + regexp.QuoteMeta(run.baseName()) + `)(?:'s)?(?:\s|$)`).MatchString(note)
}

var compared = regexp.MustCompile(`(?i)\bcompared\b`)

func hasProseMatch(re *regexp.Regexp, s string, start, end int) bool {
	return slices.ContainsFunc(re.FindAllStringIndex(s[start:end], -1), func(loc []int) bool {
		return testerWordAllowed(s, start+loc[0], start+loc[1])
	})
}

// withoutComparison drops the sentences of a tester's note that say what was or was not compared with the base branch.
// plainVerdict applies it only to a check with no result on the base branch, where such a claim would be false. The other sentences stay. Sentences end at . ! ? or ; before a space.
func withoutComparison(note string, run Run) string {
	main := regexp.MustCompile(`(?i)\b(?:main|` + regexp.QuoteMeta(run.baseName()) + `)\b`)
	var kept []string
	start, dropped := 0, 0
	for _, loc := range append(sentenceStop.FindAllStringIndex(note, -1), []int{len(note), len(note)}) {
		if s := note[start:loc[1]]; strings.TrimSpace(s) != "" {
			if hasProseMatch(compared, note, start, loc[1]) && hasProseMatch(main, note, start, loc[1]) {
				dropped++
			} else {
				kept = append(kept, strings.TrimSpace(s))
			}
		}
		start = loc[1]
	}
	out := strings.Join(kept, " ")
	if trimmed := strings.TrimRight(out, " ;,"); dropped > 0 && trimmed != out { // a dropped clause leaves its separator behind
		out = trimmed + "."
	}
	return out
}

var sentenceStop = regexp.MustCompile(`[.!?;](?:\s+|$)`)
var trailingEntity = regexp.MustCompile(`&(?:amp;)*[A-Za-z0-9#]+;$`)

// plainVerdict copies checks without rewriting the tester's words. Renderers
// filter completed replacements while keeping the audit links available.
func plainVerdict(_ Run, v Verdict) Verdict {
	v.Checks = slices.Clone(v.Checks)
	return v
}

// recase raises or lowers the first letter; a lower-case request leaves acronyms and names (second letter capital) and "I" alone.
func recase(s string, up bool) string {
	r := []rune(s)
	if len(r) > 1 && up {
		r[0] = unicode.ToUpper(r[0])
	} else if len(r) > 1 && unicode.IsUpper(r[0]) && !unicode.IsUpper(r[1]) && (r[0] != 'I' || r[1] != ' ') {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

func lowerFirst(s string) string { return recase(s, false) }

// leadWords are the words an expectation can open with that are not a name: lowering them is safe.
var leadWords = []string{"a", "an", "the", "after", "when", "with", "in", "on", "at", "if", "once", "every", "each", "both", "while", "before"}

// lowerLead lowers the first letter of an expectation that follows "that", unless the word is a label or a name.
// A function word or an -ing word ("typing", "opening") is always lowered. Any other word stays when it is all
// capitals or holds a digit, dot or slash (CLI, Orchestrator.inc, Local/Cloud), or when the same capitalised word
// turns up again, not at the start of a sentence, later in the expectation or in one of the others (Go in "Go back
// and Go forward"). Everything else is a common noun that only sat first: "Search field" becomes "search field".
func lowerLead(s string, others ...string) string {
	w, rest, _ := strings.Cut(s, " ")
	w = strings.Trim(w, `.,;:!?()"'`)
	if lw := strings.ToLower(w); slices.Contains(leadWords, lw) || (len(lw) > 4 && strings.HasSuffix(lw, "ing")) {
		return lowerFirst(s)
	}
	if w == "" || strings.ContainsAny(w, "0123456789./") || (len(w) > 1 && w == strings.ToUpper(w)) {
		return s
	}
	if capitalised(w, rest, true) || slices.ContainsFunc(others, func(o string) bool { return capitalised(w, o, false) }) {
		return s
	}
	return lowerFirst(s)
}

// capitalised reports whether w turns up in text anywhere but at the start of a sentence. mid says text itself
// begins mid-sentence (the rest of an expectation after its first word); otherwise text begins a sentence.
func capitalised(w, text string, mid bool) bool {
	for _, t := range strings.Fields(text) {
		if mid && strings.Trim(t, `.,;:!?()"'`) == w {
			return true
		}
		mid = !strings.ContainsAny(t[len(t)-1:], ".!?")
	}
	return false
}

// checkLabel names a check by what it expects (its name, de-slugged, when there is no expectation).
func checkLabel(c Check) string {
	return mdText(sentences(cmp.Or(c.Expect, strings.ReplaceAll(c.Name, "-", " ")), 60), 80)
}

// rowLabel is the label of the table row c belongs to: a base check beside its head twin is named like the twin.
func rowLabel(checks []Check, c Check) string {
	if c.onLeg() == "base" {
		if i := slices.IndexFunc(checks, func(h Check) bool { return h.onLeg() == "head" && strings.EqualFold(h.Name, c.Name) }); i >= 0 {
			return checkLabel(checks[i])
		}
	}
	return checkLabel(c)
}

// cellWords are the table's words for a result.
var cellWords = map[string]string{"pass": "works", "fail": "fails", "partial": "partly", "absent": "not there", "skipped": "not reached", "observed": "looked at", "not compared on base": "not compared", "-": "not compared"}

func cellWord(s string) string { return cmp.Or(cellWords[s], s) }

// tally counts completed head checks separately from comparisons and differences.
type tally struct {
	completed, compared, notRun, differed, headProblems, failed, worth int
	environmentProblems                                                int
	skippedClaims                                                      []Check
}

func count(checks []Check) (t tally) {
	for _, effect := range verdictChecks(checks) {
		c := effect.Check
		_, notes := splitWorthNotes(c.Note)
		t.worth += len(notes)
		if c.unreached() {
			if effect.skippedClaim {
				t.skippedClaims = append(t.skippedClaims, c)
			}
			continue
		}
		if c.onLeg() != "head" || !slices.Contains(outcomes, c.State) {
			continue
		}
		t.completed++
		if c.Attribution != "environment" && effect.cap != "" {
			t.headProblems++
		}
		if c.State == CheckFail || c.State == CheckAbsent {
			t.failed++
		}
		if c.Attribution == "environment" && effect.cap != "" {
			t.environmentProblems++
		}
		base := CheckState(baseResult(checks, c))
		if base == "observed" || slices.Contains(outcomes, base) && base != CheckSkipped {
			t.compared++
			if base != "observed" && base != c.State {
				t.differed++
			}
		} else {
			t.notRun++
		}
	}
	return t
}

func verdictLine(overall CheckState, t tally, main string) string {
	switch overall {
	case CheckBlocked:
		return "could not test this one"
	case CheckUntested:
		return "the PR's stated behavior was not checked"
	case CheckFail:
		return "something broke; do not merge yet"
	case CheckPass:
		if t.worth > 0 {
			return fmt.Sprintf("pass, %d %s worth a look", t.worth, plural(t.worth, "thing", "things"))
		}
		return "pass"
	case CheckPartial:
		line := "partial: not everything could be tested"
		if t.headProblems > 0 {
			line = "partial: something misbehaved"
		} else if t.environmentProblems == 0 && t.completed > 0 && t.compared == t.completed && t.differed == 0 && len(t.skippedClaims) == 0 {
			line = "partial: no difference from " + main + " shown"
		}
		var skipped []string
		for _, c := range t.skippedClaims {
			side := "the PR build"
			if c.onLeg() == "base" {
				side = main
			}
			skipped = append(skipped, mdText(strings.ReplaceAll(c.Name, "-", " "), math.MaxInt)+" on "+side)
		}
		if len(skipped) > 0 {
			line += "; skipped claim checks: " + joinNatural(skipped)
		}
		return line
	}
	return string(overall)
}

// Skipped checks are named once in the compact ledger below the result.
func commentResultLine(overall CheckState, t tally, main string) string {
	line, _, _ := strings.Cut(verdictLine(overall, t, main), "; skipped claim checks:")
	return line
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func coverageLine(t tally, main string) string {
	return fmt.Sprintf("%d completed %s on the PR build; %d %s compared on %s; %d %s with different results.",
		t.completed, plural(t.completed, "check", "checks"), t.compared, plural(t.compared, "check", "checks"), main,
		t.differed, plural(t.differed, "check", "checks"))
}

// splitWorthNotes keeps each marked opinion separate from the check's outcome.
func splitWorthNotes(note string) (string, []string) {
	const marker = "worth a look:"
	at := strings.Index(strings.ToLower(note), marker)
	if at < 0 {
		return note, nil
	}
	main, rest := strings.TrimSpace(note[:at]), note[at+len(marker):]
	var notes []string
	for {
		at = strings.Index(strings.ToLower(rest), marker)
		part := rest
		if at >= 0 {
			part = rest[:at]
		}
		if part = strings.TrimSpace(part); part != "" {
			notes = append(notes, part)
		}
		if at < 0 {
			return main, notes
		}
		rest = rest[at+len(marker):]
	}
}

func worthALookRows(run Run, checks []Check) []string {
	checks = commentChecksAgainstBase(checks)
	var rows []string
	for _, c := range checks {
		_, notes := splitWorthNotes(c.Note)
		if len(notes) == 0 {
			continue
		}
		receipt := "receipt unavailable"
		seq := c.Shot
		if seq == 0 {
			if receipts := c.legReceipts(); len(receipts) > 0 {
				seq = receipts[len(receipts)-1].Seq
			}
		}
		if seq > 0 {
			receipt = fmt.Sprintf("receipt %d", seq)
		}
		for _, note := range notes {
			rows = append(rows, "- "+mdText(strings.ReplaceAll(c.Name, "-", " "), math.MaxInt)+", "+run.sideName(c.onLeg())+", "+receipt+": "+mdText(firstNoteSentence(note), math.MaxInt))
		}
	}
	return rows
}

func joinNatural(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	case 2:
		return words[0] + " and " + words[1]
	default:
		return strings.Join(words[:len(words)-1], ", ") + ", and " + words[len(words)-1]
	}
}

// plainReason says a derive reason in the comment's words; a tester's own reason passes through.
func plainReason(r, main string) string {
	switch r {
	case reasonPreOnly:
		return "Whatever failed on the PR build failed on " + main + " too."
	case reasonPrePartial:
		return "What I could not confirm on the PR build I could not confirm on " + main + " either."
	case reasonPreBoth:
		return plainReason(reasonPreOnly, main) + " " + plainReason(reasonPrePartial, main)
	case reasonNoneReached:
		return "I could not reach any check on the PR build."
	case reasonNoDifference:
		return "Nothing passed on the PR build that failed on " + main + ", so I never saw the difference."
	case reasonUntested:
		return "I did not check the PR's stated behavior."
	}
	return mdText(r, math.MaxInt)
}

var outcomeWords = map[CheckState]string{CheckPass: "it worked", CheckFail: "it did not work", CheckPartial: "it only partly worked", CheckAbsent: "it is not there"}

// checkPara tells one check the way a teammate would: what was tried, what happened on the PR build, then on main.
func checkPara(run Run, v Verdict, c Check, n, i int) string {
	c.Note, _ = splitWorthNotes(c.Note)
	v.Checks = commentChecksAgainstBase(v.Checks)
	c = checkAgainstBase(v.Checks, c)
	notes := make([]string, 0, 2*len(v.Checks)) // every note and expectation: a word capitalised mid-sentence there is a label
	for _, x := range v.Checks {
		notes = append(notes, x.Note, x.Expect)
	}
	label, main := lowerLead(mdText(strings.TrimRight(sentences(cmp.Or(c.Expect, strings.ReplaceAll(c.Name, "-", " ")), 160), ".!?"), 400), notes...), run.baseName()
	if c.unreached() {
		if c.Attribution == "environment" {
			return strings.TrimSpace(fmt.Sprintf("Not checked: %s. %s", label, environmentPhrase(c)))
		}
		return strings.TrimSpace(fmt.Sprintf("Not checked: %s. %s", label, recase(plainSentence(c.Note, math.MaxInt), true)))
	}
	if c.onLeg() == "head" && outcomeWords[c.State] == "" && plainSentence(c.Note, math.MaxInt) == "" { // nothing was seen on the PR build
		return recase(mdText(strings.ReplaceAll(c.Name, "-", " "), math.MaxInt), true) + " could not be run on the PR build."
	}
	say := func(who string, x Check) string {
		note := plainSentence(x.Note, math.MaxInt)
		if x.Attribution == "environment" {
			note = strings.TrimSpace(cmp.Or(note, outcomeWords[x.State]+".") + " " + environmentPhrase(x))
		}
		if subjectLed(note, run) {
			return recase(note, true)
		}
		return fmt.Sprintf("On %s, %s", who, cmp.Or(lowerLead(note, notes...), outcomeWords[x.State]+"."))
	}
	lead := []string{"I checked that ", "Next I checked that ", "I also checked that "}[i%3]
	if c.Claim {
		lead = "- " + string(c.State) + ": "
	}
	out := []string{lead + label + "."}
	if c.onLeg() == "head" {
		if c.State == CheckAbsent {
			out = append(out, "The PR build does not have this.")
		}
		out = append(out, say("the PR build", c))
	}
	twin := baseTwin(v.Checks, c)
	baseEnvironment := twin >= 0 && v.Checks[twin].Attribution == "environment"
	switch {
	case c.onLeg() == "base":
		out = append(out, say(main, c))
	case twin >= 0 && checkPurpose(c) == "claim":
		line := mainComparison(run, v.Checks, c) + "."
		if strings.TrimSpace(v.Checks[twin].Note) != "" {
			base := v.Checks[twin]
			base.Attribution = "" // mainComparison already includes the environment cause.
			line += " " + say(main, base)
		}
		out = append(out, line)
	case twin >= 0 && v.Checks[twin].State == CheckSkipped:
		out = append(out, "I did not repeat this on "+main+".")
	case twin >= 0 && v.Checks[twin].State == CheckAbsent && !baseEnvironment: // main has no such feature
		line := main + " does not have this" + skipWhy(v.Checks[twin].Note) + "."
		if c.State != CheckAbsent && c.Attribution != "environment" {
			line = "This is new in the PR; " + line
		} else {
			line = recase(line, true)
		}
		out = append(out, line)
	case twin >= 0:
		out = append(out, say(main, v.Checks[twin]))
	case baseResult(v.Checks, c) != "":
		out = append(out, "I also looked at it on "+main+".")
	case v.SkipBase != "": // said once, after the result line
	default:
		out = append(out, "I could not compare it with "+main+".")
	}
	switch {
	case c.onLeg() != "head" || c.Attribution == "environment" || baseEnvironment:
	case preExisting(v.Checks, c):
		out = append(out, "This is pre-existing; the same outcome was recorded on "+main+".")
	case c.State == CheckPartial && baseResult(v.Checks, c) == string(CheckPartial):
		out = append(out, "It is also partial on "+main+"; I could not tell whether it is the same fault.")
	case c.State == CheckFail && baseResult(v.Checks, c) == string(CheckFail):
		out = append(out, "It also fails on "+main+"; I could not tell whether it is the same fault.")
	case (c.State == CheckFail || c.State == CheckPartial) && baseResult(v.Checks, c) == string(CheckAbsent):
		out = append(out, "Since "+main+" has no such feature, this is the PR's own problem.")
	}
	return strings.Join(out, " ")
}

// skipWhy is ": <why>" for the note on a check the tester did not repeat on the base, or "" when it gave none.
func skipWhy(note string) string {
	if why := lowerFirst(mdText(strings.TrimRight(strings.TrimSpace(note), "."), math.MaxInt)); why != "" {
		return ": " + why
	}
	return ""
}

// lasted is how long a stretch of the run lasted, in whole minutes, for the first line: "7 minutes".
func lasted(d time.Duration) string {
	switch m := int(d.Round(time.Minute) / time.Minute); {
	case m < 1:
		return "under a minute"
	case m == 1:
		return "1 minute"
	default:
		return fmt.Sprintf("%d minutes", m)
	}
}

func preparedTiming(run Run, now time.Time) string {
	builds, testing := run.buildsAndTesting(now)
	testingText := "not recorded"
	if !run.testingEndedAt(now).IsZero() {
		testingText = lasted(testing)
	}
	return "builds " + lasted(builds) + ", testing " + testingText
}

// renderComment is the PR comment for a stored verdict, ending in its run marker.
// Every check stays visible, with full notes in the final collapsed block.
func renderComment(run Run, v Verdict, ev evidence, now time.Time) string {
	return commentBody(run, v, ev, now, commentFits[0]) + "\n\n" + commentMarker(run.Repo, run.PRNumber)
}

// issuesFor is " (for #6390)", " (for #6390 and #6391)" or " (for #1, #2 and #3)", each number linked to its issue; ""
// when the PR closes none.
func issuesFor(issues []IssueRef) string {
	refs := make([]string, 0, len(issues))
	for _, is := range issues {
		ref := "#" + strconv.Itoa(is.Number)
		if is.URL != "" {
			ref = "[" + ref + "](" + is.URL + ")"
		}
		refs = append(refs, ref)
	}
	switch len(refs) {
	case 0:
		return ""
	case 1:
		return " (for " + refs[0] + ")"
	}
	return " (for " + strings.Join(refs[:len(refs)-1], ", ") + " and " + refs[len(refs)-1] + ")"
}

func commentBody(run Run, v Verdict, ev evidence, now time.Time, f commentFit) string {
	allChecks := v.Checks
	v.Checks = commentChecksAgainstBase(v.Checks)
	fullNotes := fullCheckNotes(run, v)
	var blocks []string // paragraphs, joined by a blank line
	add := func(format string, a ...any) { blocks = append(blocks, fmt.Sprintf(format, a...)) }
	main, n := run.baseName(), max(f.notes, f.note)
	worth, tally := worthALookRows(run, v.Checks), count(v.Checks)
	var themes []string
	v.Checks = slices.Clone(v.Checks)
	for i, c := range v.Checks {
		v.Checks[i].Note, _ = splitWorthNotes(c.Note)
		for _, r := range c.Receipts {
			if r.ThemeNote != "" {
				themes = append(themes, checkLabel(c)+", "+run.sideName(r.onLeg())+": "+mdText(r.ThemeNote, n))
			}
		}
	}
	both := slices.ContainsFunc(v.Checks, func(c Check) bool {
		return c.onLeg() == "base" || slices.ContainsFunc(c.Receipts, func(r Receipt) bool { return r.onLeg() == "base" })
	})
	blocked := v.Overall == CheckBlocked // no check ran: the result line and the reason below say it all
	// started: something was built or seen; a blocked run with no leg and no receipt never got that far
	started := len(run.Legs) > 0 || !blocked || slices.ContainsFunc(v.Checks, func(c Check) bool { return len(c.Receipts) > 0 })
	if started {
		took := "(" + lasted(now.Sub(run.CreatedAt)) + ")" // a run that never finished preparing has no testing time to split out
		if run.prepared() {
			took = "(" + preparedTiming(run, now) + ")"
		}
		add("Litmus tried #%d%s on the PR build%s %s.", run.PRNumber, issuesFor(run.Issues), map[bool]string{true: " and on " + main + run.atBranchPoint()}[both], took)
	} else { // nothing was built: say so, not "tried on the PR build"
		why := ""
		if blocked && len(v.Reasons) > 0 {
			why = " (" + lowerFirst(mdText(v.Reasons[0], math.MaxInt)) + ")"
		}
		add("Litmus could not start on #%d%s%s. %s", run.PRNumber, issuesFor(run.Issues), why, run.builtLine())
	}
	add("**Result: %s.**", commentResultLine(v.Overall, tally, main))
	if lines := replacementLines(allChecks); len(lines) > 0 {
		add("Replacements:\n\n%s", strings.Join(lines, "\n"))
	}
	if summary := testerSummary(v.Summary); summary != "" {
		add("%s", summary)
	}
	if line := originalProblemLine(run, v.Checks); line != "" {
		add("%s", line)
	}
	if !blocked {
		add("%s", coverageLine(tally, main))
	}
	if len(worth) > 0 {
		add("Worth a look:\n\n%s", strings.Join(worth, "\n"))
	}
	if v.OwnerLoginWarning != "" {
		add("%s", mdText(v.OwnerLoginWarning, math.MaxInt))
	}
	if why := lowerFirst(mdText(strings.TrimRight(v.SkipBase, "."), math.MaxInt)); why != "" && !blocked {
		switch compared, notRun := baseCoverage(v.Checks); { // each paragraph below says why; this says how much
		case len(compared) == 0:
			add("I did not run %s: %s.", main, why)
		case len(notRun) > 0:
			labels := make([]string, len(notRun))
			for i, c := range notRun {
				labels[i] = mdText(strings.ReplaceAll(c.Name, "-", " "), math.MaxInt)
			}
			add("I compared %d %s on %s; %d %s not run there: %s.", len(compared), map[bool]string{true: "check", false: "checks"}[len(compared) == 1], main, len(notRun), map[bool]string{true: "was", false: "were"}[len(notRun) == 1], strings.Join(labels, ", "))
		}
	}
	if lists := compactChecks(run, v); lists != "" {
		add("%s", lists)
	}
	if len(themes) > 0 {
		add("%s", strings.Join(themes, "\n"))
	}
	for _, it := range ev.items {
		if it.kind == kindHero {
			add("%s\n%s", it.image(), it.caption)
		}
	}
	if line := areaLine(run.Areas, v.Checks); line != "" {
		add("%s", line)
	}
	more, vids := slices.DeleteFunc(slices.Clone(ev.items), func(it evItem) bool { return it.kind == kindHero }), videoParts(run, ev)
	if len(more)+len(vids) > 0 {
		slices.SortStableFunc(more, func(a, b evItem) int { return a.kind - b.kind }) // the recordings, then the screenshots
		parts := append([]string{"<details><summary>Recordings and every screenshot</summary>"}, vids...)
		for _, it := range more {
			parts = append(parts, it.caption+"\n"+it.image())
		}
		add("%s\n\n</details>", strings.Join(parts, "\n\n"))
	}
	var gaps, limits []string
	for _, g := range run.Gaps { // what intake could not get, as the pack and verdict.md say it: in full at every fit, the other limits give way
		if text := strings.TrimSpace(g); text != "" {
			gaps = append(gaps, mdText(text, math.MaxInt))
		}
	}
	limits = append(limits, ev.limits...)
	for i, r := range v.Reasons { // a reason that opens with a check's id and name is that check's note, already told above
		switch {
		case blocked && !started && i == 0: // the first line carries all of it
		case blocked:
			limits = append(limits, "I could not run the checks. "+recase(plainSentence(r, math.MaxInt), true))
		case r == reasonNoneReached && len(tally.skippedClaims) > 0: // every check was skipped: the result line says so
		case !slices.ContainsFunc(v.Checks, func(c Check) bool { return strings.HasPrefix(r, c.ID+" "+c.Name+": ") }):
			limits = append(limits, plainReason(r, main))
		}
	}
	room := max(f.bullets-len(gaps), 0) // the gaps take bullets first and are never clipped or folded into "And N more."
	for i, l := range limits {
		if i == room {
			limits = append(limits[:i], fmt.Sprintf("And %d more.", len(limits)-i))
			break
		}
		limits[i] = l
	}
	if limits = append(gaps, limits...); len(limits) > 0 {
		add("%s", strings.Join(limits, "\n"))
	}
	if fullNotes != "" {
		add("%s", fullNotes)
	}
	return strings.Join(blocks, "\n\n")
}

// videoParts are the details block's entries for the uploaded videos, ahead of the GIFs: a caption, then the link in
// a paragraph of its own so GitHub shows a player. A video that did not go up says so, and points at the GIF when
// there is one.
func videoParts(run Run, ev evidence) (out []string) {
	for _, v := range ev.videos {
		side := sideLabel(run, v.leg)
		if v.url == "" {
			note := side + " video could not be attached"
			if slices.ContainsFunc(ev.items, func(it evItem) bool { return it.kind == kindGIF && it.file == sideSlug(run, v.leg)+".gif" }) {
				note += "; the GIF below shows the steps"
			}
			out = append(out, note+".")
			continue
		}
		how := map[bool]string{true: "only the moments around each step"}[v.cut]
		caption := slices.DeleteFunc([]string{side, spanText(v.span), how}, func(s string) bool { return s == "" })
		out = append(out, strings.Join(caption, ", ")+"\n\n"+v.url)
	}
	return out
}

var (
	words     = regexp.MustCompile(`[a-z0-9]+`)
	nameWords = regexp.MustCompile(`[A-Z]+[a-z0-9]*|[a-z0-9]+`) // TitlebarNav is titlebar and nav
)

// bareStems are file-name words too common to show that a check was about the file.
var bareStems = []string{"index", "main", "app", "use", "get", "set", "util", "utils", "type", "types", "test", "spec", "project", "file", "files"}

// covers reports whether a check's words share a stem with a word of one of the area's file names: nav is the stem
// of navigate. A run from before areas kept file names falls back to the words of the area's own name.
func covers(said []string, a Area) bool {
	stems := a.Stems
	if len(stems) == 0 {
		stems = []string{a.Name}
	}
	for _, stem := range stems {
		for _, w := range nameWords.FindAllString(stem, -1) {
			if w = strings.ToLower(w); len(w) >= 3 && !slices.Contains(bareStems, w) && slices.ContainsFunc(said, func(t string) bool { return strings.HasPrefix(t, w) }) {
				return true
			}
		}
	}
	return false
}

// areaLine says which changed areas (six at most) the run did not check, as one sentence, or "" when it checked them
// all. Tests are never run here. Another area is covered when a check that ran on the PR build (pass, fail or partial;
// a skipped check ran nothing) shares a word stem with one of its files, or when it is the only one: the checks came
// from the PR's own claims, so they are about it.
func areaLine(areas []Area, checks []Check) string {
	var said []string
	ran := false
	for _, c := range checks {
		if c.onLeg() != "head" || (c.State != CheckPass && c.State != CheckFail && c.State != CheckPartial) {
			continue
		}
		ran = true
		said = append(said, words.FindAllString(strings.ToLower(c.Name+" "+c.Expect+" "+c.Note), -1)...)
	}
	others := len(areas)
	if slices.ContainsFunc(areas, func(a Area) bool { return a.Name == "Tests" }) {
		others--
	}
	var left []string
	for _, a := range areas {
		if a.Name != "Tests" && ran && (others == 1 || covers(said, a)) {
			continue
		}
		left = append(left, fmt.Sprintf("%s (%d %s)", lowerFirst(mdText(a.Name, 40)), a.Files, map[bool]string{true: "file", false: "files"}[a.Files == 1]))
	}
	if len(left) == 0 {
		return ""
	}
	out := "Not checked by this run: "
	if len(left) > 6 {
		return out + strings.Join(left[:6], ", ") + fmt.Sprintf(", and %d more %s.", len(left)-6, map[bool]string{true: "area", false: "areas"}[len(left)-6 == 1])
	}
	return out + strings.Join(left, ", ") + "."
}

// plainCell escapes tester prose and shortens only at a sentence boundary.
func plainCell(note string, n int) string {
	return mdText(strings.TrimSpace(note), n)
}

// postComment writes comment.md (the exact body) and, when the run asked for it and is not blocked, publishes the
// evidence images and posts it. It never fails the submit: the outcome is the comment's URL and a state (posted,
// updated, off or failed:<reason>), plus what became of the evidence. A run that asks before posting stops after the
// first step (see closeRun) and takes the second (publishComment) when its post is approved.
func (s *Service) postComment(ctx context.Context, run Run, v Verdict) (url, state string, ev evidence) {
	body, ev, err := s.composeComment(run, v)
	if err != nil {
		return "", s.commentFailed(run, err), ev
	}
	if run.Comment != CommentPost || v.Overall == CheckBlocked {
		return "", CommentOff, ev
	}
	return s.publishComment(ctx, run, v, body)
}

// writeComment renders the comment for ev and writes it to comment.md.
func (s *Service) writeComment(run Run, v Verdict, ev evidence) (string, error) {
	body := renderComment(run, v, ev, s.clock())
	return body, s.store.WriteFile(run.ID, "comment.md", []byte(body))
}

// draftComments creates the reviewable files used by both ordinary posts and
// ask-before-posting runs. A later post reads these files back, so edits made
// while approval is pending survive evidence publication.
func (s *Service) draftComments(run Run, v Verdict) ([]commentDoc, error) {
	docs, err := s.renderCommentDrafts(run, v, s.clock())
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.store.root, run.ID, "comments")
	for i := range docs {
		if docs[i].Number == 0 {
			docs[i].Number = i + 1
		}
		if err := writeAtomic(filepath.Join(dir, strconv.Itoa(i+1)+".md"), []byte(docs[i].Body)); err != nil {
			return nil, err
		}
	}
	if err := s.writeCommentManifest(run, docs); err != nil {
		return nil, err
	}
	if len(docs) > 0 {
		if err := s.store.WriteFile(run.ID, "comment.md", []byte(docs[0].Body)); err != nil {
			return nil, err
		}
	}
	return docs, nil
}

func (s *Service) renderCommentDrafts(run Run, v Verdict, now time.Time) ([]commentDoc, error) {
	dir := filepath.Join(s.store.root, run.ID, "comments")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	plain := plainVerdict(run, v)
	bugs := failingBugComments(plain.Checks)
	numberBugComments(bugs)
	history := s.previousComments(run)
	markerName := ""
	for _, old := range history {
		if !strings.EqualFold(strings.TrimSpace(old.Name), "summary") && old.Number == 1 {
			markerName = old.Name
			break
		}
	}
	if markerName == "" {
		for _, old := range history {
			if strings.EqualFold(strings.TrimSpace(old.Name), "summary") {
				markerName = old.Name
				break
			}
		}
	}
	isMarker := func(name string, index int) bool {
		return (markerName != "" && strings.EqualFold(strings.TrimSpace(markerName), strings.TrimSpace(name))) || (markerName == "" && len(history) == 0 && index == 0)
	}
	docs := make([]commentDoc, 0, max(len(bugs), 1))
	if len(bugs) == 0 {
		marker := isMarker("summary", 0)
		body := summaryCommentBody(run, plain, now, marker)
		if plain.Overall == CheckBlocked || plain.Overall == CheckFail {
			body = renderComment(run, plain, evidence{}, now)
			body = strings.ReplaceAll(body, commentMarker(run.Repo, run.PRNumber), litmusCommentMarker())
			if !marker {
				body = strings.ReplaceAll(body, litmusCommentMarker(), "")
			}
		}
		docs = append(docs, commentDoc{Name: "summary", Marker: marker, Body: body})
	} else {
		for i := range bugs {
			bugs[i].ImageFile = fmt.Sprintf("bug-%d.png", bugs[i].Number)
			imagePath := filepath.Join(dir, bugs[i].ImageFile)
			image := ""
			if err := makeBugImage(bugs[i], v.Checks, imagePath); err != nil {
				s.commentLogf(run, "comment image %s: %v", bugs[i].Check.Name, err)
			} else {
				image = bugs[i].ImageFile
			}
			marker := isMarker(bugs[i].Check.Name, i)
			clip := ""
			if p := checkClipPath(filepath.Join(s.store.root, run.ID), bugs[i].Check.ID); fileExists(p) {
				clip = filepath.ToSlash(filepath.Join("..", "clips", filepath.Base(p)))
			}
			docs = append(docs, commentDoc{Name: bugs[i].Check.Name, Number: bugs[i].Number, Marker: marker, ImageFile: image, ClipFile: clip, Body: bugCommentBody(run, v, bugs[i], image, clip, marker, i == 0)})
		}
	}
	current := make(map[string]bool, len(bugs))
	for _, bug := range bugs {
		current[strings.ToLower(strings.TrimSpace(bug.Check.Name))] = true
	}
	for _, old := range history {
		name := strings.TrimSpace(old.Name)
		key := strings.ToLower(name)
		if name == "" || key == "summary" || old.Fixed || current[key] {
			continue
		}
		state, found := headCheckState(plain.Checks, name)
		if !found || state != CheckPass {
			continue
		}
		marker := isMarker(name, len(docs))
		docs = append(docs, commentDoc{Name: name, Number: old.Number, Fixed: true, CommentID: old.ID, Marker: marker, Body: fixedCommentBody(run.HeadSHA, marker)})
	}
	if warning := mdText(v.OwnerLoginWarning, math.MaxInt); len(docs) > 0 && warning != "" && !strings.Contains(docs[0].Body, warning) {
		docs[0].Body += "\n\n" + warning + "\n"
	}
	for i := range docs {
		if docs[i].Number == 0 {
			docs[i].Number = i + 1
		}
	}
	return docs, nil
}

// DraftComment is a numbered comment file kept for review before posting.
type DraftComment struct {
	Number int
	Body   string
}

// DraftComments reads the same numbered drafts used by the post path.
func (s *Store) DraftComments(id string) ([]DraftComment, error) {
	dir, err := s.path(id, "comments")
	if err != nil {
		return nil, err
	}
	// Stat first: on Windows, ReadDir on a regular file reports no error or
	// ErrNotExist, so a broken comments path would read as "no drafts".
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: errors.New("not a directory")}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var docs []DraftComment
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".md"))
		if err != nil || n <= 0 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		docs = append(docs, DraftComment{Number: n, Body: string(data)})
	}
	slices.SortFunc(docs, func(a, b DraftComment) int { return cmp.Compare(a.Number, b.Number) })
	return docs, nil
}

func (s *Service) readDraftComments(run Run) ([]commentDoc, error) {
	drafts, err := s.store.DraftComments(run.ID)
	if err != nil {
		return nil, err
	}
	docs := make([]commentDoc, 0, len(drafts))
	for _, draft := range drafts {
		docs = append(docs, commentDoc{Number: draft.Number, Body: draft.Body, Marker: strings.Contains(draft.Body, litmusCommentMarker())})
	}
	return docs, nil
}

func (s *Service) readCommentManifest(run Run) ([]commentDoc, error) {
	dir := filepath.Join(s.store.root, run.ID, "comments")
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []commentManifestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	docs := make([]commentDoc, 0, len(entries))
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.File))
		if err != nil {
			return nil, err
		}
		docs = append(docs, commentDoc{Name: entry.Name, Number: entry.Number, Fixed: entry.Fixed, CommentID: entry.ID, Body: string(body), Marker: strings.Contains(string(body), litmusCommentMarker())})
	}
	return docs, nil
}

func (s *Service) writeCommentManifest(run Run, docs []commentDoc) error {
	dir := filepath.Join(s.store.root, run.ID, "comments")
	entries := make([]commentManifestEntry, 0, len(docs))
	for i, doc := range docs {
		entries = append(entries, commentManifestEntry{Name: doc.Name, File: strconv.Itoa(i+1) + ".md", Number: doc.Number, Fixed: doc.Fixed, ID: doc.CommentID})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "manifest.json"), data)
}

func (s *Service) commentDrafts(run Run, v Verdict, ev evidence, fallback string) ([]commentDoc, error) {
	docs, err := s.readCommentManifest(run)
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		docs, err = s.readDraftComments(run)
		if err != nil {
			return nil, err
		}
	}
	plain := plainVerdict(run, v)
	bugs := failingBugComments(plain.Checks)
	numberBugComments(bugs)
	history := s.previousComments(run)
	markerName := ""
	for _, old := range history {
		if !strings.EqualFold(strings.TrimSpace(old.Name), "summary") && old.Number == 1 {
			markerName = old.Name
			break
		}
	}
	if markerName == "" {
		for _, old := range history {
			if strings.EqualFold(strings.TrimSpace(old.Name), "summary") {
				markerName = old.Name
				break
			}
		}
	}
	if len(docs) == 0 {
		if len(bugs) == 0 {
			docs = []commentDoc{{Name: "summary", Number: 1, Body: fallback}}
		} else {
			for i, bug := range bugs {
				marker := len(history) == 0 && i == 0
				image := ""
				imageFile := fmt.Sprintf("bug-%d.png", bug.Number)
				if fileExists(filepath.Join(s.store.root, run.ID, "comments", imageFile)) {
					image = imageFile
				}
				clip := ""
				clipPath := checkClipPath(filepath.Join(s.store.root, run.ID), bug.Check.ID)
				if fileExists(clipPath) {
					clip = filepath.ToSlash(filepath.Join("..", "clips", filepath.Base(clipPath)))
				}
				docs = append(docs, commentDoc{Name: bug.Check.Name, Number: bug.Number, Body: bugCommentBody(run, v, bug, image, clip, marker, i == 0)})
			}
		}
	}
	for i := range docs {
		if !docs[i].Fixed && i < len(bugs) {
			docs[i].Name = bugs[i].Check.Name
			imageFile := fmt.Sprintf("bug-%d.png", bugs[i].Number)
			oldImageFile := fmt.Sprintf("bug-%d.png", docs[i].Number)
			docs[i].Body = bugCommentHeading.ReplaceAllString(docs[i].Body, fmt.Sprintf("Bug %d of %d:", bugs[i].Number, bugs[i].Total))
			docs[i].Body = strings.ReplaceAll(docs[i].Body, "("+oldImageFile+")", "("+imageFile+")")
			// Keep the generated target even when the local file was removed, so a
			// failed evidence publish cannot leak a broken path into GitHub.
			docs[i].ImageFile = imageFile
			clipPath := checkClipPath(filepath.Join(s.store.root, run.ID), bugs[i].Check.ID)
			docs[i].ClipFile = filepath.ToSlash(filepath.Join("..", "clips", filepath.Base(clipPath)))
			docs[i].Number = bugs[i].Number
		}
		if docs[i].Name == "" {
			docs[i].Name = "summary"
		}
		isMarker := (markerName != "" && strings.EqualFold(strings.TrimSpace(markerName), strings.TrimSpace(docs[i].Name))) || (markerName == "" && len(history) == 0 && i == 0)
		if isMarker {
			docs[i].Body = strings.ReplaceAll(docs[i].Body, commentMarker(run.Repo, run.PRNumber), "")
			if !strings.Contains(docs[i].Body, litmusCommentMarker()) {
				docs[i].Body = strings.TrimSpace(docs[i].Body) + "\n\n" + litmusCommentMarker()
			}
			docs[i].Marker = true
		} else {
			docs[i].Body = strings.ReplaceAll(docs[i].Body, litmusCommentMarker(), "")
			docs[i].Body = strings.ReplaceAll(docs[i].Body, commentMarker(run.Repo, run.PRNumber), "")
			docs[i].Marker = false
		}
		if docs[i].ImageFile != "" {
			if url := ev.bugImages[docs[i].Name]; url != "" {
				docs[i].Body = strings.ReplaceAll(docs[i].Body, "("+docs[i].ImageFile+")", "("+url+")")
			} else if strings.Contains(docs[i].Body, "]("+docs[i].ImageFile+")") {
				imageLink := regexp.MustCompile(`!\[[^\]]*\]\(` + regexp.QuoteMeta(docs[i].ImageFile) + `\)\n*`)
				docs[i].Body = imageLink.ReplaceAllString(docs[i].Body, "")
				docs[i].Body = strings.TrimSpace(docs[i].Body) + "\n\nThe comparison image was not available in the published evidence."
			}
		}
		if docs[i].ClipFile != "" {
			if url := ev.checkClips[docs[i].Name]; url != "" {
				docs[i].Body = strings.ReplaceAll(docs[i].Body, "("+docs[i].ClipFile+")", "("+url+")")
			} else {
				docs[i].Body = strings.ReplaceAll(docs[i].Body, "[Watch the check]("+docs[i].ClipFile+")", "The check recording was not available.")
			}
		}
	}
	return docs, nil
}

func commentIDFromURL(url string) int64 {
	const marker = "#issuecomment-"
	i := strings.LastIndex(url, marker)
	if i < 0 {
		return 0
	}
	id, _ := strconv.ParseInt(strings.TrimSpace(url[i+len(marker):]), 10, 64)
	return id
}

func (s *Service) sendComment(ctx context.Context, run Run, body string, id int64) (sent map[string]any, state string, err error) {
	method, path, state := "POST", "repos/"+run.Repo+"/issues/"+strconv.Itoa(run.PRNumber)+"/comments", "posted"
	if id != 0 {
		method, path, state = "PATCH", "repos/"+run.Repo+"/issues/comments/"+strconv.FormatInt(id, 10), "updated"
	}
	out, err := s.ghSend(ctx, run, method, path, map[string]string{"body": body})
	if err != nil {
		return nil, "", err
	}
	sent = make(map[string]any)
	if err := json.Unmarshal(out, &sent); err != nil {
		return nil, "", err
	}
	if _, ok := sent["id"]; !ok {
		sent["id"] = id
	}
	return sent, state, nil
}

func sentCommentID(sent map[string]any, fallback int64) int64 {
	if id, ok := sent["id"].(float64); ok {
		return int64(id)
	}
	if url, ok := sent["html_url"].(string); ok {
		return commentIDFromURL(url)
	}
	return fallback
}

func sentCommentURL(sent map[string]any) string {
	url, _ := sent["html_url"].(string)
	return url
}

func headCheckState(checks []Check, name string) (CheckState, bool) {
	for _, check := range checks {
		if check.onLeg() == "head" && strings.EqualFold(strings.TrimSpace(check.Name), strings.TrimSpace(name)) {
			return check.State, true
		}
	}
	return "", false
}

func mergeCommentMappings(current, history []PostedComment) []PostedComment {
	out := slices.Clone(current)
	for _, old := range history {
		found := false
		for _, existing := range out {
			if strings.EqualFold(strings.TrimSpace(existing.Name), strings.TrimSpace(old.Name)) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, old)
		}
	}
	return out
}

func (s *Service) publishCommentSet(ctx context.Context, run Run, v Verdict, ev evidence, fallback string) (string, string, error) {
	docs, err := s.commentDrafts(run, v, ev, fallback)
	if err != nil {
		return "", "", err
	}
	if len(docs) == 0 {
		return "", "", errors.New("no comment drafts found")
	}
	if err := s.store.put(run.ID, commentMediaFile, commentMedia{BugImages: ev.bugImages, CheckClips: ev.checkClips}); err != nil {
		return "", "", fmt.Errorf("save published comment media: %w", err)
	}
	if err := s.store.WriteFile(run.ID, "comment.md", []byte(docs[0].Body)); err != nil {
		return "", "", err
	}
	login, err := s.ghLogin(ctx)
	if err != nil {
		return "", "", err
	}
	api := "repos/" + run.Repo
	raw, err := s.gh.Run(ctx, "api", api+"/issues/"+strconv.Itoa(run.PRNumber)+"/comments", "--paginate")
	if err != nil {
		return "", "", err
	}
	markerID, err := findComment(raw, litmusCommentMarker(), login)
	if err != nil {
		return "", "", fmt.Errorf("unreadable comment list: %w", err)
	}
	if markerID == 0 {
		markerID, err = findComment(raw, commentMarker(run.Repo, run.PRNumber), login)
		if err != nil {
			return "", "", fmt.Errorf("unreadable comment list: %w", err)
		}
	}
	history := s.previousComments(run)
	if len(history) > 0 {
		if _, err := s.update(run.ID, func(cur *Run) { cur.Comments = mergeCommentMappings(cur.Comments, history) }); err != nil {
			return "", "", err
		}
	}
	previous := make(map[string]PostedComment, len(history))
	for _, c := range history {
		previous[strings.ToLower(strings.TrimSpace(c.Name))] = c
	}
	var firstURL, lastState string
	for i, doc := range docs {
		old := previous[strings.ToLower(strings.TrimSpace(doc.Name))]
		id := old.ID
		if id == 0 {
			id = doc.CommentID
		}
		if id == 0 {
			id = commentIDFromURL(old.URL)
		}
		if i == 0 && id == 0 && markerID != 0 && len(history) == 0 {
			id = markerID
		}
		if markerID != 0 {
			if id != markerID {
				doc.Body = strings.ReplaceAll(doc.Body, litmusCommentMarker(), "")
			} else if !strings.Contains(doc.Body, litmusCommentMarker()) {
				doc.Body = strings.TrimSpace(doc.Body) + "\n\n" + litmusCommentMarker()
			}
		}
		if markerID == 0 && i != 0 {
			doc.Body = strings.ReplaceAll(doc.Body, litmusCommentMarker(), "")
		}
		if doc.Fixed && id == 0 {
			continue
		}
		sent, state, err := s.sendComment(ctx, run, doc.Body, id)
		if err != nil {
			return "", "", err
		}
		url := sentCommentURL(sent)
		if firstURL == "" {
			firstURL = url
		}
		lastState = state
		mapping := PostedComment{Name: doc.Name, ID: sentCommentID(sent, id), URL: url, Number: doc.Number, Fixed: doc.Fixed}
		if _, err := s.update(run.ID, func(cur *Run) { cur.Comments = appendCommentMapping(cur.Comments, mapping) }); err != nil {
			return "", "", err
		}
	}
	if v.Overall == CheckFail && s.reviewOn(run) {
		if err := s.requestChanges(ctx, run, login, firstURL); err != nil {
			s.commentLog(run, fmt.Errorf("review not submitted: %w", err))
		}
	}
	return firstURL, lastState, nil
}

func appendCommentMapping(current []PostedComment, next PostedComment) []PostedComment {
	for i := range current {
		if strings.EqualFold(strings.TrimSpace(current[i].Name), strings.TrimSpace(next.Name)) {
			current[i] = next
			return current
		}
	}
	return append(current, next)
}

// composeComment is the first step of posting: it writes comment.md with no evidence in it, and touches no network.
func (s *Service) composeComment(run Run, v Verdict) (string, evidence, error) {
	ev := evidence{state: EvidenceOff, videoState: "off"}
	if _, err := s.writeComment(run, v, ev); err != nil {
		return "", ev, err
	}
	docs, err := s.draftComments(run, v)
	if err != nil {
		return "", ev, err
	}
	if len(docs) == 0 {
		return "", ev, errors.New("no comment draft was written")
	}
	return docs[0].Body, ev, nil
}

// publishComment is the second step: it publishes the evidence images and videos and posts or updates the comment.
// Evidence is published only once the token's login is known, so a gh that cannot post does not push files either.
func (s *Service) publishComment(ctx context.Context, run Run, v Verdict, body string) (url, state string, ev evidence) {
	ev = evidence{state: EvidenceOff, videoState: "off"}
	lctx, cancel := context.WithTimeout(ctx, commentTimeout)
	_, err := s.ghLogin(lctx)
	cancel()
	if err != nil {
		return "", s.commentFailed(run, err), ev
	}
	ev = s.publishEvidence(ctx, run, v)
	ev.checkClips, ev.videoState = s.publishCheckClips(ctx, run, v)
	pctx, cancel := context.WithTimeout(ctx, commentTimeout)
	defer cancel()
	url, state, err = s.publishCommentSet(pctx, run, v, ev, body)
	if err != nil {
		return "", s.commentFailed(run, err), ev
	}
	return url, state, ev
}

// commentFailed logs err to comment.log in the run dir and returns the state it earns.
func (s *Service) commentFailed(run Run, err error) string {
	slog.Warn("litmus: PR comment failed", "run", run.ID, "error", err)
	s.commentLog(run, err)
	reason, _, _ := strings.Cut(err.Error(), "\n")
	return "failed:" + sentences(strings.TrimSpace(reason), 120)
}

func (s *Service) commentLog(run Run, err error) { s.commentLogf(run, "%v", err) }

func (s *Service) commentLogf(run Run, format string, a ...any) {
	f, ferr := os.OpenFile(filepath.Join(s.store.root, run.ID, "comment.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if ferr != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s %s\n", s.clock().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

// requestChanges submits the REQUEST_CHANGES review, unless the token owns the PR: GitHub refuses that from an author.
func (s *Service) requestChanges(ctx context.Context, run Run, login, commentURL string) error {
	n := strconv.Itoa(run.PRNumber)
	author, err := s.gh.Run(ctx, "pr", "view", n, "--repo", run.Repo, "--json", "author", "--jq", ".author.login")
	if err != nil || strings.EqualFold(strings.TrimSpace(string(author)), login) {
		return err
	}
	link := cmp.Or(commentURL, fmt.Sprintf("https://github.com/%s/pull/%d", run.Repo, run.PRNumber))
	_, err = s.ghSend(ctx, run, "POST", "repos/"+run.Repo+"/pulls/"+n+"/reviews",
		map[string]string{"event": "REQUEST_CHANGES", "body": "Litmus found a failing check on this pull request.\nDetails: " + link})
	return err
}

// ghLogin is the login the gh token acts as, asked once per service.
func (s *Service) ghLogin(ctx context.Context) (string, error) {
	s.mu.Lock()
	login := s.login
	s.mu.Unlock()
	if login != "" {
		return login, nil
	}
	out, err := s.gh.Run(ctx, "api", "user", "--jq", ".login")
	if login = strings.TrimSpace(string(out)); err == nil && login == "" {
		err = errors.New("gh api user returned no login")
	}
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.login = login
	s.mu.Unlock()
	return login, nil
}

// ghSend calls the GitHub API with a JSON body read from a file, never from argv, and returns the object gh prints.
func (s *Service) ghSend(ctx context.Context, run Run, method, path string, body map[string]string) ([]byte, error) {
	data, _ := json.Marshal(body)
	f, err := os.CreateTemp(filepath.Join(s.store.root, run.ID), ".gh-*.json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	return s.gh.Run(ctx, "api", "--method", method, path, "--input", f.Name())
}

// findComment returns the id of the comment holding marker that login wrote, or 0. `gh --paginate` prints one JSON
// array per page, back to back.
func findComment(raw []byte, marker, login string) (int64, error) {
	for dec := json.NewDecoder(bytes.NewReader(raw)); dec.More(); {
		var page []struct {
			ID   int64
			Body string
			User struct{ Login string }
		}
		if err := dec.Decode(&page); err != nil {
			return 0, err
		}
		for _, c := range page {
			if strings.Contains(c.Body, marker) && strings.EqualFold(c.User.Login, login) {
				return c.ID, nil
			}
		}
	}
	return 0, nil
}
