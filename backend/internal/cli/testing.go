package cli

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
)

// testingCLI is the shared plumbing for `ao testing` verbs: each verb is one
// HTTP call to /api/v1/testing/... attributed to the AO_SESSION_ID worker.
type testingCLI struct {
	c      *commandContext
	asJSON bool
	leg    string
}

func verb(use, short string, args cobra.PositionalArgs, run func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: args, RunE: run}
}

func newTestingCommand(ctx *commandContext) *cobra.Command {
	t := &testingCLI{c: ctx}
	cmd := verb("testing", "Test a pull request: pin it, build base and head, drive the app, submit a verdict", noArgs, nil)
	cmd.PersistentFlags().BoolVar(&t.asJSON, "json", false, "print the structured response as JSON")
	cmd.PersistentFlags().StringVar(&t.leg, "leg", "", "leg a driving verb acts on (base or head); needed when both are launched")

	cmd.AddCommand(t.startCommand())
	cmd.AddCommand(verb("intake <pr-url>", "Start a testing run for a GitHub pull request (25 minutes from when the builds are ready)", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		return testingCallTimeout(t, cmd, "testing/intake", map[string]any{"prUrl": a[0]}, runLine, testingSetupCallTimeout)
	}))
	cmd.AddCommand(verb("prepare", "Prepare the base and head clones and builds", noArgs, func(cmd *cobra.Command, _ []string) error {
		return testingCallTimeout(t, cmd, "testing/prepare", map[string]any{}, runLine, prepareCallTimeout)
	}))
	cmd.AddCommand(verb("launch <base|head>", "Launch a private copy of the base or head build", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		return testingCallTimeout(t, cmd, "testing/launch", map[string]any{"leg": a[0]}, func(r litmus.Run) string { return launchLine(r, a[0]) }, launchCallTimeout)
	}))
	cmd.AddCommand(verb("relaunch <head|base>", "Kill and relaunch one testing leg while keeping its profile and data", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		if a[0] != "head" && a[0] != "base" {
			return usageError{errors.New("usage: relaunch requires head or base")}
		}
		return testingCallTimeout(t, cmd, "testing/relaunch", map[string]any{"leg": a[0]}, func(r litmus.Run) string { return launchLine(r, a[0]) }, relaunchCallTimeout)
	}))
	cmd.AddCommand(verb("agent-version <agent> <version>", "Set the agent version on a testing leg", exactArgs(2), func(cmd *cobra.Command, a []string) error {
		if t.leg != "head" && t.leg != "base" {
			return usageError{errors.New("usage: agent-version requires --leg head or --leg base")}
		}
		if strings.TrimSpace(a[0]) == "" || strings.TrimSpace(a[1]) == "" {
			return usageError{errors.New("usage: agent-version requires a non-empty agent and version")}
		}
		return testingCallTimeout(t, cmd, "testing/agent-version", map[string]any{"leg": t.leg, "agent": a[0], "version": a[1]}, runLine, 4*time.Minute)
	}))
	cmd.AddCommand(verb("status", "Show the run, its deadline and its checks", noArgs, func(cmd *cobra.Command, _ []string) error {
		return testingStatusCall(t, cmd, func(out litmus.RunStatus) string {
			lines := make([]string, 0, 1+len(out.Run.Legs)+len(out.Checks))
			lines = append(lines, runLineWithSubmit(out.Run, hasInFlightVerb(out.InFlight, "submit")))
			if out.OwnerLogins != "" {
				lines = append(lines, out.OwnerLogins)
			}
			for _, f := range out.InFlight {
				lines = append(lines, fmt.Sprintf("%s in flight for %s", f.Verb, (time.Duration(f.Seconds)*time.Second).String()))
			}
			for _, l := range out.Run.Legs {
				lines = append(lines, legLine(l))
			}
			for _, c := range out.Checks {
				lines = append(lines, fmt.Sprintf("%s (%d receipts)", checkLine(c), len(c.Receipts)))
			}
			lines = append(lines, statusHintLines(out, "")...)
			return strings.Join(lines, "\n")
		})
	}))
	cmd.AddCommand(t.checkCommand(), t.compareCommand(), t.replayCommand(), t.submitCommand())
	cmd.AddCommand(verb("cleanup", "Stop the run's targets and remove its private state", noArgs, func(cmd *cobra.Command, _ []string) error {
		return testingCall(t, cmd, "testing/cleanup", map[string]any{}, runLine)
	}))
	cmd.AddCommand(t.recipeCommand(), t.waitCommand(), t.recordCommand(), t.driveCommand())
	cmd.AddCommand(t.pruneCommand())
	cmd.AddCommand(t.runsCommand(), t.showCommand(), t.rerunCommand(), t.decideCommand("post", "Publish the evidence and post the comment of a run awaiting approval"),
		t.decideCommand("discard", "Close a run awaiting approval without posting its comment"), t.settingsCommand())

	for _, d := range []struct {
		use, short string
		args       cobra.PositionalArgs
	}{
		{"snapshot", "Read the UI as text with element refs", noArgs},
		{"click <ref>", "Click an element by ref", exactArgs(1)},
		{"fill <ref> <text>", "Replace an input's text", exactArgs(2)},
		{"type <ref> <text>", "Type text into an element", exactArgs(2)},
		{"press <key>", "Press a key", exactArgs(1)},
		{"screenshot [path]", "Save a screenshot", atMostOneArg},
		{"console", "Print console output", noArgs},
		{"errors", "Print page errors", noArgs},
		{"network", "Print network requests", noArgs},
		{"tab [list]", "List the target's tabs", atMostOneArg},
	} {
		action := strings.Fields(d.use)[0]
		cmd.AddCommand(verb(d.use, d.short, d.args, func(cmd *cobra.Command, a []string) error { return t.drive(cmd, action, a) }))
	}
	return cmd
}

// startCommand spawns the worker that tests a PR. The worker, not this command, makes the run.
func (t *testingCLI) startCommand() *cobra.Command {
	var pr, projectID, harness string
	var noComment, noEvidence bool
	cmd := verb("start --pr <url>", "Spawn a worker that tests a pull request (claude-code or codex)", noArgs, func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(pr) == "" {
			return usageError{errors.New("usage: --pr is required")}
		}
		project, err := t.c.resolveSpawnProject(cmd.Context(), projectID)
		if err != nil {
			return err
		}
		var out litmus.Started
		body := withCaller(map[string]any{"projectId": project.ID, "prUrl": pr})
		if h := strings.TrimSpace(harness); h != "" { // else the daemon picks: the project's Litmus setting, its worker agent, claude-code
			if !litmus.Supported(h) {
				return usageError{fmt.Errorf("testing workers run on %s or %s, not %q", litmus.Harness, litmus.HarnessCodex, h)}
			}
			body["harness"] = h
		}
		if noComment {
			body["noComment"] = true
		}
		if noEvidence {
			body["noEvidence"] = true
		}
		if err := t.c.postJSON(cmd.Context(), "testing/start", body, &out); err != nil {
			return err
		}
		return t.show(cmd, out, fmt.Sprintf("spawned session %s %q", out.SessionID, out.Name))
	})
	f := cmd.Flags()
	f.StringVar(&pr, "pr", "", "GitHub pull request URL (or owner/repo#N) to test")
	f.StringVar(&projectID, "project", "", "Project id the worker runs in (default: AO_PROJECT_ID or the current registered repo)")
	f.StringVar(&harness, "harness", "", "Agent harness, claude-code or codex (default: the project's Litmus harness setting, then its worker agent if supported, then claude-code)")
	f.BoolVar(&noComment, "no-comment", false, "Do not post the verdict on the pull request (it is still written to comment.md in the run dir)")
	f.BoolVar(&noEvidence, "no-evidence", false, "Do not publish screenshots and GIFs to the repo's litmus/evidence branch for the comment")
	return cmd
}

func (t *testingCLI) checkCommand() *cobra.Command {
	check := verb("check", "Plan and bracket the checks a verdict is built from", noArgs, nil)
	var expect, result, note string
	var sameFault bool
	var shot int
	var addClaim, startClaim, reclassClaim, addRisk, startRisk, reclassRisk string
	var addExplore, startExplore, reclassExplore bool
	var replaces, replaceReason string
	var attribution, cause, sameBug string
	var attributionReceipt int
	add := verb("add <name>", "Add a check with the observable you expect", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		if strings.TrimSpace(expect) == "" {
			return usageError{errors.New("usage: --expect is required")}
		}
		if t.leg != "" && t.leg != "base" && t.leg != "head" {
			return usageError{errors.New("usage: --leg must be base or head")}
		}
		replaces = strings.TrimSpace(replaces)
		replaceReason = strings.TrimSpace(replaceReason)
		if (cmd.Flags().Changed("replaces")) != (cmd.Flags().Changed("reason")) {
			return usageError{errors.New("usage: --replaces and --reason must be provided together")}
		}
		if cmd.Flags().Changed("replaces") && (replaces == "" || replaceReason == "") {
			return usageError{errors.New("usage: --replaces and --reason must be non-empty")}
		}
		classificationChanged := cmd.Flags().Changed("claim") || cmd.Flags().Changed("risk") || cmd.Flags().Changed("explore")
		body := map[string]any{}
		if classificationChanged || t.leg != "base" {
			var err error
			body, err = checkClassificationBody(addClaim, addRisk, addExplore, cmd.Flags().Changed("claim"), cmd.Flags().Changed("risk"), cmd.Flags().Changed("explore"))
			if err != nil {
				return err
			}
		}
		body["name"], body["expect"] = a[0], expect
		if cmd.Flags().Changed("replaces") {
			body["replaces"], body["reason"] = replaces, replaceReason
		}
		if t.leg != "" {
			body["leg"] = t.leg
		}
		return testingCheckCall(t, cmd, "testing/checks", body, checkLine)
	})
	add.Flags().StringVar(&expect, "expect", "", "Observable that proves the check passes (add it with --leg base to record what the base shows)")
	add.Flags().StringVar(&addClaim, "claim", "", "PR sentence this check tests")
	add.Flags().StringVar(&addRisk, "risk", "", "What this diff could break on a path the PR does not mention")
	add.Flags().BoolVar(&addExplore, "explore", false, "Mark this check as exploratory coverage")
	add.Flags().StringVar(&replaces, "replaces", "", "Failed or partial claim this replaces after a pass, fail or partial result on the same PR sentence")
	add.Flags().StringVar(&replaceReason, "reason", "", "PR sentence explaining why this check replaces it")
	list := verb("list", "List the checks, their legs and receipts", noArgs, func(cmd *cobra.Command, _ []string) error {
		return testingStatusCall(t, cmd, func(out litmus.RunStatus) string {
			lines := make([]string, 0, len(out.Checks))
			for _, c := range out.Checks {
				lines = append(lines, fmt.Sprintf("%s (%d receipts): %s", checkLine(c), len(c.Receipts), c.Expect))
			}
			return strings.Join(lines, "\n")
		})
	})
	start := verb("start <id>", "Start a check; actions record receipts against it", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		classificationChanged := cmd.Flags().Changed("claim") || cmd.Flags().Changed("risk") || cmd.Flags().Changed("explore")
		body := map[string]any{}
		if classificationChanged {
			var err error
			body, err = checkClassificationBody(startClaim, startRisk, startExplore, cmd.Flags().Changed("claim"), cmd.Flags().Changed("risk"), cmd.Flags().Changed("explore"))
			if err != nil {
				return err
			}
		}
		return testingCheckCall(t, cmd, "testing/checks/"+url.PathEscape(a[0])+"/start", body, checkLine)
	})
	start.Aliases = []string{"begin"}
	start.Flags().StringVar(&startClaim, "claim", "", "PR sentence this check tests")
	start.Flags().StringVar(&startRisk, "risk", "", "What this diff could break on a path the PR does not mention")
	start.Flags().BoolVar(&startExplore, "explore", false, "Mark this check as exploratory coverage")
	reclassify := verb("reclassify <id>", "Change a check's coverage classification and record why", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		reason, err := cmd.Flags().GetString("reason")
		if err != nil {
			return err
		}
		if strings.TrimSpace(reason) == "" {
			return usageError{errors.New("usage: --reason is required for reclassify")}
		}
		body, err := checkClassificationBody(reclassClaim, reclassRisk, reclassExplore, cmd.Flags().Changed("claim"), cmd.Flags().Changed("risk"), cmd.Flags().Changed("explore"))
		if err != nil {
			return err
		}
		body["reason"] = strings.TrimSpace(reason)
		return testingCall(t, cmd, "testing/checks/"+url.PathEscape(a[0])+"/reclassify", body, checkLine)
	})
	reclassify.Flags().StringVar(&reclassClaim, "claim", "", "PR sentence this check tests")
	reclassify.Flags().StringVar(&reclassRisk, "risk", "", "What this diff could break on a path the PR does not mention")
	reclassify.Flags().BoolVar(&reclassExplore, "explore", false, "Mark this check as exploratory coverage")
	reclassify.Flags().String("reason", "", "Why the check's coverage changed")
	end := verb("end <id>", "End a check and record its outcome", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		if result != "pass" && result != "fail" && result != "partial" && result != "absent" && result != "skipped" {
			return usageError{errors.New("usage: --result must be pass, fail, partial, absent or skipped")}
		}
		if attribution == "" && (strings.TrimSpace(cause) != "" || attributionReceipt != 0) {
			return usageError{errors.New("usage: --cause and --receipt require --attribution")}
		}
		if attribution != "" && attribution != "pr" && attribution != "pre-existing" && attribution != "environment" && attribution != "unknown" {
			return usageError{errors.New("usage: --attribution must be pr, pre-existing, environment or unknown")}
		}
		if attribution == "environment" && (strings.TrimSpace(cause) == "" || attributionReceipt <= 0) {
			return usageError{errors.New("usage: --attribution environment requires --cause and a positive --receipt")}
		}
		if attribution != "environment" && (strings.TrimSpace(cause) != "" || attributionReceipt != 0) {
			return usageError{errors.New("usage: --cause and --receipt are only valid with --attribution environment")}
		}
		if attributionReceipt < 0 {
			return usageError{errors.New("usage: --receipt must be non-negative")}
		}
		if cmd.Flags().Changed("same-bug") && strings.TrimSpace(sameBug) == "" {
			return usageError{errors.New("usage: --same-bug requires a check id")}
		}
		body := map[string]any{"result": result, "note": note}
		if cmd.Flags().Changed("shot") {
			if shot <= 0 {
				return usageError{errors.New("usage: --shot must be a positive receipt number")}
			}
			body["shot"] = shot
		}
		if sameFault {
			body["sameFault"] = true
		}
		if strings.TrimSpace(sameBug) != "" {
			body["sameBug"] = strings.TrimSpace(sameBug)
		}
		if attribution != "" {
			body["attribution"] = attribution
			if attribution == "environment" {
				body["environmentCause"], body["attributionReceipt"] = strings.TrimSpace(cause), attributionReceipt
			}
		}
		return testingCheckCall(t, cmd, "testing/checks/"+url.PathEscape(a[0])+"/end", body, func(c litmus.Check) string {
			lines := []string{checkLine(c)}
			for _, r := range c.Receipts {
				for _, shot := range r.ThemeShots {
					lines = append(lines, fmt.Sprintf("after (%s): %s", shot.Theme, shot.Path))
				}
				if r.ThemeNote != "" {
					lines = append(lines, r.ThemeNote)
				}
			}
			return strings.Join(lines, "\n")
		})
	})
	end.Flags().StringVar(&result, "result", "", "Outcome: pass, fail (the feature is there and broken), partial, absent (this build lacks the feature entirely; needs a screenshot of where it would be) or skipped (could not reach it; needs no receipt)")
	end.Flags().StringVar(&note, "note", "", "Why, in one line")
	end.Flags().BoolVar(&sameFault, "same-fault", false, "Base leg only: the failure or partial is the same fault as on the PR build")
	end.Flags().StringVar(&sameBug, "same-bug", "", "Link this head failure or partial to an ended failed or partial head check for the same bug; set when ending and change only by re-ending")
	end.Flags().IntVar(&shot, "shot", 0, "Head receipt number whose after-shot shows the failure")
	end.Flags().StringVar(&attribution, "attribution", "", "Cause of the outcome: pr, pre-existing, environment or unknown")
	end.Flags().StringVar(&cause, "cause", "", "Environment cause for --attribution environment")
	end.Flags().IntVar(&attributionReceipt, "receipt", 0, "Receipt proving an environment cause")
	check.AddCommand(add, list, start, reclassify, end)
	return check
}

// checkClassificationBody requires each check verb to carry an explicit
// coverage choice. The daemon repeats this validation for non-CLI callers.
func checkClassificationBody(claimSentence, risk string, explore, claimChanged, riskChanged, exploreChanged bool) (map[string]any, error) {
	claimSentence = strings.TrimSpace(claimSentence)
	if claimChanged && exploreChanged || claimChanged && riskChanged || riskChanged && exploreChanged {
		return nil, usageError{errors.New("usage: --claim, --risk and --explore cannot be combined")}
	}
	if claimChanged {
		if claimSentence == "" {
			return nil, usageError{errors.New("usage: --claim requires a non-empty PR sentence")}
		}
		return map[string]any{"purpose": "claim", "claimSentence": claimSentence}, nil
	}
	if riskChanged {
		risk = strings.TrimSpace(risk)
		if risk == "" {
			return nil, usageError{errors.New("usage: --risk requires what the diff could break")}
		}
		return map[string]any{"purpose": "risk", "risk": risk}, nil
	}
	if exploreChanged && explore {
		return map[string]any{"purpose": "explore"}, nil
	}
	return nil, usageError{errors.New("usage: choose --claim '<PR sentence>', --risk '<what the diff could break>' or --explore")}
}

// compareCommand pixel-diffs a check's last base after-shot against the head page; it reruns nothing.
func (t *testingCLI) compareCommand() *cobra.Command {
	return verb("compare <checkID>", "Diff a check's last base and head after-shots and keep the image on the check", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		return testingCall(t, cmd, "testing/checks/"+url.PathEscape(a[0])+"/compare", map[string]any{}, prettyJSON)
	})
}

func (t *testingCLI) replayCommand() *cobra.Command {
	return verb("replay <headCheckId>", "Repeat a check already walked on the PR build against the base build", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		if t.leg != "base" {
			return usageError{errors.New("usage: replay requires --leg base")}
		}
		return testingCallTimeout(t, cmd, "testing/checks/"+url.PathEscape(a[0])+"/replay", map[string]any{"leg": t.leg}, driveText, litmus.RunBudget+time.Minute)
	})
}

func (t *testingCLI) submitCommand() *cobra.Command {
	var verdictFile, reason string
	var blocked, skipBase bool
	cmd := verb("submit", "Submit the verdict; every check needs an outcome and a receipt (--blocked --reason closes a run you cannot finish)", noArgs, func(cmd *cobra.Command, _ []string) error {
		body := map[string]any{}
		switch {
		case blocked && skipBase:
			return usageError{errors.New("usage: --blocked and --skip-base cannot be combined")}
		case blocked && strings.TrimSpace(reason) == "":
			return usageError{errors.New("usage: --blocked requires a non-empty --reason")}
		case skipBase && strings.TrimSpace(reason) == "":
			return usageError{errors.New("usage: --skip-base requires a non-empty --reason")}
		case blocked && verdictFile != "":
			return usageError{errors.New("usage: --blocked takes --reason, not --verdict-file")}
		case !blocked && !skipBase && reason != "":
			return usageError{errors.New("usage: --reason requires --blocked or --skip-base")}
		case blocked:
			body["blocked"], body["reason"] = true, strings.TrimSpace(reason)
		default:
			var summary string
			if verdictFile != "" {
				data, err := os.ReadFile(verdictFile)
				if err != nil {
					return err
				}
				summary = string(data)
			}
			if summary != "" {
				body["summary"] = summary
			}
			if skipBase {
				body["skipBase"] = strings.TrimSpace(reason)
			}
		}
		return testingCallTimeout(t, cmd, "testing/submit", body, func(v litmus.Verdict) string {
			lines := []string{"verdict: " + string(v.Overall)}
			if len(v.IntakeClaims) > 0 {
				lines = append(lines, "PR claims from intake:")
				for _, claim := range v.IntakeClaims {
					lines = append(lines, "- "+claim)
				}
			}
			lines = append(lines, v.Reasons...)
			lines = append(lines, commentLine(v.CommentState, v.CommentURL)...)
			lines = append(lines, evidenceLine(v.EvidenceState, v.EvidenceCommit)...)
			return strings.Join(lines, "\n")
		}, submitCallTimeout)
	})
	cmd.Flags().StringVar(&verdictFile, "verdict-file", "", "File whose text is kept as the verdict summary")
	cmd.Flags().BoolVar(&blocked, "blocked", false, "Close the run as blocked: no receipts needed, the reason becomes the verdict")
	cmd.Flags().BoolVar(&skipBase, "skip-base", false, "Submit without a base result for the checks that have none, at any time; needs --reason")
	cmd.Flags().StringVar(&reason, "reason", "", "What failed (with --blocked) or why the base was not run (with --skip-base)")
	return cmd
}

func (t *testingCLI) recipeCommand() *cobra.Command {
	recipe := verb("recipe", "Work with per-repo recipes", noArgs, nil)
	var sha string
	validate := verb("validate <path>", "Validate a recipe file; with --sha also run it against that commit", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		abs, err := filepath.Abs(a[0]) // the daemon's working directory is not the worker's
		if err != nil {
			return err
		}
		ref, err := litmus.RecipeRef(abs, sha)
		if err != nil {
			return usageError{err}
		}
		timeout := commandTimeout
		if sha != "" {
			timeout = testingSetupCallTimeout
		}
		return testingCallTimeout(t, cmd, "testing/recipe/validate", map[string]any{"path": ref}, func(r litmus.Recipe) string {
			return strings.Join(append([]string{"recipe ok: " + r.Kind}, r.Timings...), "\n")
		}, timeout)
	})
	validate.Flags().StringVar(&sha, "sha", "", "Prepare, launch, seed and clean up a head leg at this commit and print the timings")
	recipe.AddCommand(validate)
	return recipe
}

func (t *testingCLI) waitCommand() *cobra.Command {
	var text string
	cmd := verb("wait [selector|ms]", "Wait for a selector, a delay in ms, or --text", atMostOneArg, func(cmd *cobra.Command, a []string) error {
		switch {
		case text != "" && len(a) == 0:
			a = []string{"--text", text}
		case text != "" || len(a) != 1:
			return usageError{errors.New("usage: wait takes one selector or ms, or --text")}
		}
		return t.drive(cmd, "wait", a)
	})
	cmd.Flags().StringVar(&text, "text", "", "Wait until this text appears")
	return cmd
}

// launchCallTimeout covers the default recipe's launch budget plus a minute for the response.
const launchCallTimeout = litmus.DefaultLaunchBudget + time.Minute

// relaunchCallTimeout allows the relaunch path's bounded process kill before the normal launch budget.
const relaunchCallTimeout = launchCallTimeout + 10*time.Second

// testingSetupCallTimeout adds a minute to intake and recipe validation.
const testingSetupCallTimeout = litmus.RecipeValidateBudget + time.Minute

const postCallTimeout = litmus.PostBudget + time.Minute
const submitCallTimeout = litmus.SubmitBudget + time.Minute

// prepareCallTimeout outlasts the daemon's 20 minute cap on one prepare: a cold
// build of both legs runs for minutes, and a call cut short by the client would
// only come back as a retry that waits for the same build.
const prepareCallTimeout = litmus.PrepareBudget + time.Minute

func (t *testingCLI) recordCommand() *cobra.Command {
	record := verb("record", "Record the target's window to a video", noArgs, nil)
	record.AddCommand(verb("start <path>", "Start recording to path", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		return t.drive(cmd, "record", []string{"start", a[0]})
	}), verb("stop", "Stop recording", noArgs, func(cmd *cobra.Command, _ []string) error {
		return t.drive(cmd, "record", []string{"stop"})
	}))
	return record
}

// driveCommand leaves agent-browser flags intact. Litmus handles --leg on the daemon.
func (t *testingCLI) driveCommand() *cobra.Command {
	cmd := verb("drive <command> [args...]", "Run any agent-browser command on the leg, including hover, scroll, scrollintoview, dblclick, keyboard, eval or cdp", nil, func(cmd *cobra.Command, args []string) error {
		// Cobra leaves inherited flags before the subcommand in args when parsing is disabled.
		for len(args) > 0 {
			if args[0] == "--leg" && len(args) > 1 {
				t.leg = args[1]
				args = args[2:]
				continue
			}
			if strings.HasPrefix(args[0], "--leg=") {
				t.leg = strings.TrimPrefix(args[0], "--leg=")
				args = args[1:]
				continue
			}
			if args[0] == "--json" {
				t.asJSON = true
				args = args[1:]
				continue
			}
			break
		}
		if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
			return cmd.Help()
		}
		if len(args) == 0 {
			return usageError{errors.New("usage: drive <command> [args...] [--leg head|base]")}
		}
		return t.drive(cmd, args[0], args[1:])
	})
	cmd.DisableFlagParsing = true
	cmd.Long = fmt.Sprintf("Pass agent-browser commands and flags through to the leg's own browser target. Page-changing commands settle and save an after-screenshot. Waits accept at most %d ms (%s), plus 30s of command overhead. Use drive cdp <Method> [json-params] for raw page CDP. Browser/profile switching and host access are refused.", litmus.MaxDriveWait.Milliseconds(), litmus.MaxDriveWait)
	return cmd
}

// testingCall sends one request for the current session (a POST of body, or a
// GET when body is nil), decodes the daemon's answer as T and prints it with text.
func testingCall[T any](t *testingCLI, cmd *cobra.Command, path string, body map[string]any, text func(T) string) error {
	return testingCallTimeout(t, cmd, path, body, text, commandTimeout)
}

// testingCheckCall prints a check mutation and, in text mode, the run's time
// remaining. The mutation response is still the only structured value emitted
// in JSON mode.
func testingCheckCall(t *testingCLI, cmd *cobra.Command, path string, body map[string]any, text func(litmus.Check) string) error {
	sessionID, err := testingSessionID()
	if err != nil {
		return err
	}
	body["sessionId"] = sessionID
	var out litmus.Check
	if err := t.c.doJSONPathWithHeadersAndTimeout(cmd.Context(), http.MethodPost, "/api/v1/"+path, body, &out, nil, commandTimeout); err != nil {
		return err
	}
	if err := t.show(cmd, out, text(out)); err != nil {
		return err
	}
	if t.asJSON {
		return nil
	}
	checkEndID := ""
	if strings.HasSuffix(path, "/end") {
		checkEndID = out.ID
	}
	return appendTestingTimeAndHints(t, cmd, sessionID, checkEndID)
}

type testingStatusJSON struct {
	OwnerLogins string            `json:"ownerLogins,omitempty"`
	Run         litmus.Run        `json:"run"`
	Checks      []litmus.Check    `json:"checks"`
	InFlight    []litmus.InFlight `json:"inFlight,omitempty"`
}

func testingStatusCall(t *testingCLI, cmd *cobra.Command, text func(litmus.RunStatus) string) error {
	sessionID, err := testingSessionID()
	if err != nil {
		return err
	}
	var out litmus.RunStatus
	if err := t.c.getJSON(cmd.Context(), "testing/status?sessionId="+url.QueryEscape(sessionID), &out); err != nil {
		return err
	}
	if t.asJSON {
		return writeJSON(cmd.OutOrStdout(), testingStatusJSON{OwnerLogins: out.OwnerLogins, Run: out.Run, Checks: out.Checks, InFlight: out.InFlight})
	}
	return t.show(cmd, out, text(out))
}

func statusHintLines(status litmus.RunStatus, checkID string) []string {
	var lines []string
	for _, hint := range status.Hints {
		if hint.CheckID == "" || hint.CheckID == checkID {
			lines = append(lines, hint.Text)
		}
	}
	return lines
}

func appendTestingTimeAndHints(t *testingCLI, cmd *cobra.Command, sessionID, checkID string) error {
	var status litmus.RunStatus
	err := t.c.getJSON(cmd.Context(), "testing/status?sessionId="+url.QueryEscape(sessionID), &status)
	if err == nil && status.Run.Deadline.IsZero() {
		err = errors.New("testing status did not include run deadline")
	}
	if err != nil {
		_, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "warning: minutes-left unavailable: %v\n", err)
		return writeErr
	}
	lines := append([]string{checkTimeLeft(status.Run, t.c.deps.Now())}, statusHintLines(status, checkID)...)
	for _, line := range lines {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
			return err
		}
	}
	return nil
}

// testingCallTimeout is testingCall with its own bound on the daemon call, for a verb that outlasts commandTimeout.
func testingCallTimeout[T any](t *testingCLI, cmd *cobra.Command, path string, body map[string]any, text func(T) string, timeout time.Duration) error {
	sessionID, err := testingSessionID()
	if err != nil {
		return err
	}
	var out T
	if body == nil {
		err = t.c.getJSON(cmd.Context(), path+"?sessionId="+url.QueryEscape(sessionID), &out)
	} else {
		body["sessionId"] = sessionID
		err = t.c.doJSONPathWithHeadersAndTimeout(cmd.Context(), http.MethodPost, "/api/v1/"+path, body, &out, nil, timeout)
	}
	if err != nil {
		return err
	}
	return t.show(cmd, out, text(out))
}

// drive sends one engine action and prints its compact result.
func (t *testingCLI) drive(cmd *cobra.Command, action string, args []string) error {
	if t.leg != "" {
		args = append(args[:len(args):len(args)], "--leg", t.leg)
	}
	sessionID, err := testingSessionID()
	if err != nil {
		return err
	}
	body := map[string]any{"sessionId": sessionID, "action": action, "args": args}
	var out map[string]any
	if err := t.c.doJSONPathWithHeadersAndTimeout(cmd.Context(), http.MethodPost, "/api/v1/testing/drive", body, &out, nil, litmus.DriveBudget+time.Minute); err != nil {
		return err
	}
	if err := t.show(cmd, out, driveText(out)); err != nil {
		return err
	}
	if t.asJSON {
		return nil
	}
	return appendTestingTimeAndHints(t, cmd, sessionID, "")
}

func driveText(out map[string]any) string {
	if text, ok := out["text"].(string); ok {
		return text
	}
	return prettyJSON(out)
}

func prettyJSON(out map[string]any) string {
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}

func (t *testingCLI) show(cmd *cobra.Command, v any, text string) error {
	if t.asJSON {
		return writeJSON(cmd.OutOrStdout(), v)
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), text)
	return err
}

// withCaller names the calling session (AO_SESSION_ID) in the body of a verb only the user may run, so the daemon can
// refuse it from a Litmus tester's own session whatever its harness. A user's terminal has no session id.
func withCaller(body map[string]any) map[string]any {
	if id := strings.TrimSpace(os.Getenv("AO_SESSION_ID")); id != "" {
		body["sessionId"] = id
	}
	return body
}

func testingSessionID() (string, error) {
	sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if sessionID == "" {
		return "", usageError{errors.New("usage: AO_SESSION_ID is required")}
	}
	return sessionID, nil
}

func runLine(r litmus.Run) string {
	return runLineWithSubmit(r, false)
}

func runLineWithSubmit(r litmus.Run, submitInFlight bool) string {
	line := fmt.Sprintf("run %s %s %s#%d deadline %s", r.ID, testingRunState(r, submitInFlight), r.Repo, r.PRNumber, r.Deadline.Format("15:04:05"))
	if r.Error != "" {
		line += "\n" + r.Error
	}
	return strings.Join(append(append([]string{line}, commentLine(r.CommentState, r.CommentURL)...), evidenceLine(r.EvidenceState, r.EvidenceCommit)...), "\n")
}

func testingRunState(r litmus.Run, submitInFlight ...bool) string {
	if len(submitInFlight) > 0 && submitInFlight[0] && !r.State.Closed() {
		return "submitted, finalizing"
	}
	return string(r.State)
}

func hasInFlightVerb(verbs []litmus.InFlight, want string) bool {
	for _, verb := range verbs {
		if verb.Verb == want {
			return true
		}
	}
	return false
}

// checkTimeLeft rounds up so the display never understates the whole minutes
// still available. A deadline at or before now reports zero minutes.
func checkTimeLeft(r litmus.Run, now time.Time) string {
	remaining := r.Deadline.Sub(now)
	minutes := int64(0)
	if remaining > 0 {
		minutes = int64((remaining + time.Minute - 1) / time.Minute)
	}
	return fmt.Sprintf("%d min left", minutes)
}

// launchLine is runLine plus any seed step that failed on this leg: its sample content is missing, which the
// tester must know before it judges a check.
func launchLine(r litmus.Run, leg string) string {
	lines := []string{runLine(r)}
	prefix := "The " + map[string]string{"head": "PR", "base": "main"}[leg] + " build started without its sample content (seed step "
	for _, g := range r.Gaps {
		if strings.HasPrefix(g, prefix) {
			lines = append(lines, g)
		}
	}
	return strings.Join(lines, "\n")
}

// commentLine says what became of the PR comment: its URL, or the state when there is none (off, failed:<reason>).
// A run that has not submitted has neither and gets no line.
func commentLine(state, link string) []string {
	switch {
	case link != "":
		return []string{"comment " + state + ": " + link}
	case state != "":
		return []string{"comment " + state}
	}
	return nil
}

// evidenceLine says what became of the comment's images: published at a commit, off, or why not.
func evidenceLine(state, commit string) []string {
	switch {
	case commit != "":
		return []string{"evidence " + state + ": " + commit[:min(len(commit), 7)]}
	case state != "":
		return []string{"evidence " + state}
	}
	return nil
}

func legLine(l litmus.Leg) string {
	line := fmt.Sprintf("  %s %s", l.Name, l.State)
	if l.State == "running" {
		line += fmt.Sprintf(" daemon :%d debug :%d", l.DaemonPort, l.DebugPort)
	}
	return line
}

// pruneCommand asks the daemon to apply the disk retention rules; it needs no session.
func (t *testingCLI) pruneCommand() *cobra.Command {
	var keepRuns, keepCheckouts int
	var dryRun bool
	cmd := verb("prune", "Delete old checkouts and reviewed run dirs; unreviewed runs are always kept", noArgs, func(cmd *cobra.Command, _ []string) error {
		body := withCaller(map[string]any{})
		if cmd.Flags().Changed("keep-runs") {
			body["keepRuns"] = keepRuns
		}
		if cmd.Flags().Changed("keep-checkouts") {
			body["keepCheckouts"] = keepCheckouts
		}
		if dryRun {
			body["dryRun"] = true
		}
		var out litmus.PruneReport
		if err := t.c.postJSON(cmd.Context(), "testing/prune", body, &out); err != nil {
			return err
		}
		return t.show(cmd, out, strings.Join(out.Lines, "\n"))
	})
	f := cmd.Flags()
	f.IntVar(&keepRuns, "keep-runs", litmus.DefaultKeepRuns, "Reviewed finished run dirs to keep besides unreviewed runs and the current one; 0 keeps no reviewed history")
	f.IntVar(&keepCheckouts, "keep-checkouts", litmus.DefaultKeepCheckouts, "Most recent reviewed finished runs whose checkouts are kept besides those of unreviewed runs")
	f.BoolVar(&dryRun, "dry-run", false, "Report what would be deleted and delete nothing")
	return cmd
}

// projectFlag is the project a panel verb acts on: --project, else AO_PROJECT_ID, else none.
func projectFlag(explicit string) string {
	if id := strings.TrimSpace(explicit); id != "" {
		return id
	}
	return strings.TrimSpace(os.Getenv("AO_PROJECT_ID"))
}

// runsCommand lists a project's runs. Like prune it needs no session.
func (t *testingCLI) runsCommand() *cobra.Command {
	var project string
	var limit int
	cmd := verb("runs", "List a project's testing runs, newest first", noArgs, func(cmd *cobra.Command, _ []string) error {
		q := url.Values{}
		if id := projectFlag(project); id != "" {
			q.Set("projectId", id)
		}
		if cmd.Flags().Changed("limit") {
			q.Set("limit", strconv.Itoa(limit))
		}
		path := "testing/runs"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var out litmus.RunList
		if err := t.c.getJSON(cmd.Context(), path, &out); err != nil {
			return err
		}
		lines := make([]string, 0, len(out.Runs))
		for _, r := range out.Runs {
			lines = append(lines, runRowLine(r))
		}
		return t.show(cmd, out, cmp.Or(strings.Join(lines, "\n"), "no runs"))
	})
	cmd.Flags().StringVar(&project, "project", "", "Project id (default: AO_PROJECT_ID, else every project)")
	cmd.Flags().IntVar(&limit, "limit", litmus.DefaultRunsLimit, "Most runs to list")
	return cmd
}

func (t *testingCLI) showCommand() *cobra.Command {
	return verb("show <runId>", "Show a run's verdict and drafted comments", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		var out litmus.RunDetail
		if err := t.c.getJSON(cmd.Context(), "testing/runs/"+url.PathEscape(a[0]), &out); err != nil {
			return err
		}
		if t.asJSON {
			return writeJSON(cmd.OutOrStdout(), out)
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		drafts, err := litmus.NewStore(cfg.DataDir).DraftComments(a[0])
		if err != nil {
			return err
		}
		state := out.Run
		state.State = out.State
		parts := []string{fmt.Sprintf("run %s %s#%d %s verdict %s", out.ID, out.Repo, out.PR.Number, testingRunState(state), out.Verdict)}
		if out.Error != "" {
			parts = append(parts, out.Error)
		}
		if table := verdictChecksTable(out.VerdictMD); table != "" {
			parts = append(parts, table)
		}
		homeChanges, err := litmus.NewStore(cfg.DataDir).HomeChanges(a[0])
		if err != nil {
			return err
		}
		if homeChanges != "" {
			parts = append(parts, "Owner home changes:\n"+homeChanges)
		}
		dir := filepath.Join(cfg.DataDir, "litmus", "runs", a[0], "comments")
		for _, draft := range drafts {
			parts = append(parts, filepath.Join(dir, strconv.Itoa(draft.Number)+".md")+"\n"+draft.Body)
			for _, match := range draftMediaLinks.FindAllStringSubmatch(draft.Body, -1) {
				link, err := url.Parse(match[1])
				if err == nil && !link.IsAbs() && link.Host == "" && link.Path != "" {
					parts = append(parts, "media: "+filepath.Join(dir, filepath.FromSlash(link.Path)))
				}
			}
		}
		if len(drafts) == 0 && out.CommentMD != "" {
			parts = append(parts, out.CommentMD)
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), strings.Join(parts, "\n\n"))
		return err
	})
}

var draftMediaLinks = regexp.MustCompile(`!?\[[^\]\n]*\]\(([^)\n]+)\)`)

func verdictChecksTable(md string) string {
	var rows []string
	inChecks := false
	for _, line := range strings.Split(md, "\n") {
		if strings.TrimSpace(line) == "## Checks" {
			inChecks = true
			continue
		}
		if !inChecks {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			rows = append(rows, line)
		} else if len(rows) > 0 || strings.HasPrefix(line, "## ") {
			break
		}
	}
	return strings.Join(rows, "\n")
}

// runRowLine is one run of the list: id, verdict, state, PR, duration and what became of the comment.
func runRowLine(r litmus.RunSummary) string {
	line := fmt.Sprintf("%s %s %s %s#%d %s", r.ID, r.Verdict, r.State, r.Repo, r.PR.Number, (time.Duration(r.DurationSeconds) * time.Second).String())
	if r.Error != "" {
		line += " | " + r.Error
	}
	return strings.Join(append([]string{line}, commentLine(r.CommentState, r.CommentURL)...), " | ")
}

// rerunCommand starts a new tester on the PR of an earlier run.
func (t *testingCLI) rerunCommand() *cobra.Command {
	var project string
	cmd := verb("rerun <runId>", "Start a new tester on the pull request of an earlier run", exactArgs(1), func(cmd *cobra.Command, a []string) error {
		body := withCaller(map[string]any{"runId": a[0]})
		if id := projectFlag(project); id != "" {
			body["projectId"] = id
		}
		var out litmus.Rerun
		if err := t.c.postJSON(cmd.Context(), "testing/rerun", body, &out); err != nil {
			return err
		}
		return t.show(cmd, out, fmt.Sprintf("spawned session %s %q, run %s", out.SessionID, out.Name, out.RunID))
	})
	cmd.Flags().StringVar(&project, "project", "", "Project id, only for a run that did not record its project (default: AO_PROJECT_ID)")
	return cmd
}

// decideCommand is post or discard: the verdict on a run that waits for approval.
func (t *testingCLI) decideCommand(name, short string) *cobra.Command {
	return verb(name+" <runId>", short, exactArgs(1), func(cmd *cobra.Command, a []string) error {
		var out litmus.Run
		timeout := commandTimeout
		if name == "post" {
			timeout = postCallTimeout
		}
		if err := t.c.doJSONPathWithHeadersAndTimeout(cmd.Context(), http.MethodPost, "/api/v1/testing/runs/"+url.PathEscape(a[0])+"/"+name, withCaller(map[string]any{}), &out, nil, timeout); err != nil {
			return err
		}
		return t.show(cmd, out, runLine(out))
	})
}

// settingsCommand shows or sets a project's Litmus settings. The API replaces the whole record, so a set reads the
// current one first and changes only the flags given.
func (t *testingCLI) settingsCommand() *cobra.Command {
	var project, ask, review, harness, model, effort string
	cmd := verb("settings", "Show or set a project's Litmus settings", noArgs, func(cmd *cobra.Command, _ []string) error {
		id := projectFlag(project)
		if id == "" {
			return usageError{errors.New("usage: --project is required")}
		}
		flags := cmd.Flags()
		askOn, reviewOn := false, false
		for _, o := range []struct {
			name, val string
			on        *bool
		}{{"ask-before-posting", ask, &askOn}, {"review", review, &reviewOn}} {
			if !flags.Changed(o.name) {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(o.val)) {
			case "on", "true":
				*o.on = true
			case "off", "false":
			default:
				return usageError{fmt.Errorf("usage: --%s must be on or off", o.name)}
			}
		}
		path := "testing/projects/" + url.PathEscape(id) + "/settings"
		var out litmus.ProjectSettings
		if err := t.c.getJSON(cmd.Context(), path, &out); err != nil {
			return err
		}
		if flags.Changed("ask-before-posting") || flags.Changed("review") || flags.Changed("harness") || flags.Changed("model") || flags.Changed("effort") {
			if flags.Changed("ask-before-posting") {
				out.AskBeforePosting = askOn
			}
			if flags.Changed("review") {
				out.Review = reviewOn
			}
			if flags.Changed("harness") {
				if h := strings.TrimSpace(harness); h != out.Harness && out.Model != "" && !flags.Changed("model") {
					return usageError{fmt.Errorf("usage: the stored model %q belongs to the current harness and may not exist on %q; pass --model in the same call (--model \"\" for the default)", out.Model, cmp.Or(h, "the default harness"))}
				}
				out.Harness = strings.TrimSpace(harness)
			}
			if flags.Changed("model") {
				out.Model = strings.TrimSpace(model)
			}
			if flags.Changed("effort") {
				out.Effort = strings.TrimSpace(effort)
			}
			var saved litmus.ProjectSettings
			if err := t.c.putJSON(cmd.Context(), path, out, &saved); err != nil {
				return err
			}
			out = saved
		}
		return t.show(cmd, out, "ask-before-posting "+map[bool]string{true: "on", false: "off"}[out.AskBeforePosting]+" | review "+map[bool]string{true: "on", false: "off"}[out.Review]+
			" | harness "+cmp.Or(out.Harness, "(default)")+" | model "+cmp.Or(out.Model, "(default)")+" | effort "+cmp.Or(out.Effort, "(default)"))
	})
	cmd.Flags().StringVar(&project, "project", "", "Project id (default: AO_PROJECT_ID)")
	cmd.Flags().StringVar(&ask, "ask-before-posting", "", "on: a finished run waits for `ao testing post <runId>` instead of commenting on the PR; off: it posts at once")
	cmd.Flags().StringVar(&review, "review", "", "on: a failing verdict also submits a REQUEST_CHANGES review beside the comment; off (default): the comment alone")
	cmd.Flags().StringVar(&harness, "harness", "", "Agent the project's testers run on: claude-code or codex (empty: the project's worker agent if supported, then claude-code)")
	cmd.Flags().StringVar(&model, "model", "", "Model the project's testers run on, e.g. opus, or gpt-6.1-sol with --harness codex (empty: the project's worker model, then sonnet on claude-code or Codex's default)")
	cmd.Flags().StringVar(&effort, "effort", "", "Reasoning effort the project's testers run at, one the model supports (empty: the project's worker effort, then high)")
	return cmd
}

func checkLine(c litmus.Check) string {
	line := fmt.Sprintf("%s %s %s", c.ID, c.State, c.Name)
	if c.Leg != "" {
		line += " [" + c.Leg + "]"
	}
	if c.Purpose != "" {
		line += " [" + c.Purpose + "]"
	}
	if c.Risk != "" {
		line += ": " + c.Risk
	}
	if c.SameBug != "" {
		line += " [same-bug: " + c.SameBug + "]"
	}
	return line
}
