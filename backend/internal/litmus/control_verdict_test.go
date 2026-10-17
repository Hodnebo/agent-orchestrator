package litmus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func closeWithHeadControl(t *testing.T, control *LegControl, checks []Check, deadline bool, blocked string) Verdict {
	t.Helper()
	v, _, _ := closeHeadControlArtifacts(t, control, checks, deadline, blocked)
	return v
}

func closeHeadControlArtifacts(t *testing.T, control *LegControl, checks []Check, deadline bool, blocked string) (Verdict, Run, string) {
	t.Helper()
	svc, _, runDir, _ := submitSvc(t)
	run, err := svc.store.Get("run_s1")
	if err != nil {
		t.Fatal(err)
	}
	for i := range run.Legs {
		if run.Legs[i].Name == "head" {
			run.Legs[i].Control = control
			break
		}
	}
	if err := svc.store.Save(run); err != nil {
		t.Fatal(err)
	}
	var v Verdict
	if deadline {
		v, err = svc.closeRunAtDeadline(ctx, run, checks, "", blocked, "")
	} else {
		v, err = svc.closeRun(ctx, run, checks, "", blocked, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	run, err = svc.store.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	public := append(append([]string{}, run.Gaps...), v.Reasons...)
	for _, name := range []string{"verdict.md", "comment.md"} {
		data, err := os.ReadFile(filepath.Join(runDir, name))
		if err != nil {
			t.Fatal(err)
		}
		public = append(public, string(data))
	}
	for _, text := range public {
		if strings.Contains(text, "ao testing") {
			t.Fatalf("CLI hint reached public text: %s", text)
		}
	}
	return v, run, runDir
}

func TestUncontrollableHeadBlocksUnreachedClaim(t *testing.T) {
	control := &LegControl{
		State:  "lost",
		Since:  17,
		Cause:  "native_dialog",
		Detail: `blocked by a native dialog: "Switch profile?" buttons [No] [Yes]`,
	}
	checks := []Check{{ID: "c1", Name: "settings opens", Claim: true, State: CheckSkipped}}
	v := closeWithHeadControl(t, control, checks, false, "")
	want := `head app uncontrollable since action 17: the app is blocked by a native dialog with title "Switch profile?" and buttons No, Yes; unreached claim checks: settings opens`
	if v.Overall != CheckBlocked || len(v.Reasons) != 1 || v.Reasons[0] != want {
		t.Fatalf("verdict = %s, %q; want blocked with %q", v.Overall, v.Reasons, want)
	}
}

func TestControlRecoveryLeavesPartialVerdict(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "menu opens", Claim: true, State: CheckPass},
		{ID: "b1", Name: "menu opens", Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
		{ID: "c2", Name: "settings opens", Claim: true, State: CheckSkipped},
	}
	v := closeWithHeadControl(t, &LegControl{State: "ok", Since: 17, Cause: "native_dialog", Detail: "dialog closed"}, checks, false, "")
	if v.Overall != CheckPartial {
		t.Fatalf("recovered control verdict = %s, reasons %q; want partial", v.Overall, v.Reasons)
	}
}

func TestUncontrollableHeadDoesNotOverrideFailure(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "menu opens", Claim: true, State: CheckFail},
		{ID: "c2", Name: "settings opens", Claim: true, State: CheckSkipped},
	}
	v := closeWithHeadControl(t, &LegControl{State: "lost", Since: 8, Cause: "native_dialog", Detail: "native dialog is open"}, checks, false, "")
	if v.Overall != CheckFail {
		t.Fatalf("lost control verdict = %s, reasons %q; want fail", v.Overall, v.Reasons)
	}
}

func TestUncontrollableHeadAfterClaimsResolvedDoesNotChangeVerdict(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "menu opens", Claim: true, State: CheckPass},
		{ID: "b1", Name: "menu opens", Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
	}
	v := closeWithHeadControl(t, &LegControl{State: "lost", Since: 9, Cause: "native_dialog", Detail: "native dialog is open"}, checks, false, "")
	if v.Overall != CheckPass {
		t.Fatalf("lost control after claims resolved changed verdict to %s, reasons %q; want pass", v.Overall, v.Reasons)
	}
}

func TestDeadlineBlocksLostHeadWithPendingClaim(t *testing.T) {
	checks := []Check{{ID: "c1", Name: "settings opens", Claim: true, State: CheckPending}}
	v := closeWithHeadControl(t, &LegControl{State: "lost", Since: 23, Cause: "native_dialog", Detail: "app is not responding"}, checks, true, "")
	if v.Overall != CheckBlocked || !strings.Contains(strings.Join(v.Reasons, "\n"), "head app uncontrollable since action 23: the app is blocked by a native dialog; unreached claim checks: settings opens") {
		t.Fatalf("deadline verdict = %s, reasons %q; want blocked for settings opens", v.Overall, v.Reasons)
	}
}

func TestControlBlockReasonCoexistsWithExistingBlockedReason(t *testing.T) {
	checks := []Check{{ID: "c1", Name: "settings opens", Claim: true, State: CheckSkipped}}
	v := closeWithHeadControl(t, &LegControl{State: "lost", Since: 4, Cause: "native_dialog", Detail: "native dialog is open"}, checks, false, "agent stopped unexpectedly")
	if v.Overall != CheckBlocked || len(v.Reasons) != 1 || !strings.Contains(v.Reasons[0], "agent stopped unexpectedly") || !strings.Contains(v.Reasons[0], "head app uncontrollable since action 4") {
		t.Fatalf("combined block reasons = %s, %q; want both causes", v.Overall, v.Reasons)
	}
}

func TestAppControlLossStaysPublicWithoutBlocking(t *testing.T) {
	for _, tc := range []struct{ cause, detail, gap string }{
		{"app_exited", "the head app exited (pid 41)", "The PR build's app exited during action 23."},
		{"app_unresponsive", "the head app is not responding (main process alive, no native dialog open)", "The PR build's app is not responding during action 23."},
	} {
		for _, deadline := range []bool{false, true} {
			name := tc.cause + map[bool]string{false: "/submit", true: "/deadline"}[deadline]
			t.Run(name, func(t *testing.T) {
				state := CheckSkipped
				if deadline {
					state = CheckPending
				}
				checks := []Check{{ID: "c1", Name: "settings opens", Claim: true, State: state}}
				wantOverall, _ := derive(checks)
				v, run, runDir := closeHeadControlArtifacts(t, &LegControl{State: "lost", Since: 23, Cause: tc.cause, Detail: tc.detail}, checks, deadline, "")
				if v.Overall != wantOverall || v.Overall == CheckBlocked {
					t.Fatalf("verdict = %s, want unchanged %s", v.Overall, wantOverall)
				}
				if len(run.Gaps) != 1 || run.Gaps[0] != tc.gap {
					t.Fatalf("gaps = %q, want %q", run.Gaps, tc.gap)
				}
				for _, name := range []string{"verdict.md", "comment.md"} {
					body, err := os.ReadFile(filepath.Join(runDir, name))
					if err != nil || !strings.Contains(string(body), tc.gap) {
						t.Fatalf("%s missing control loss %q: %s, %v", name, tc.gap, body, err)
					}
				}
			})
		}
	}
}

func TestHarnessControlLossBlocksAtSubmitAndDeadline(t *testing.T) {
	for _, tc := range []struct{ cause, detail string }{
		{"native_dialog", "the app is blocked by a native dialog"},
		{"browser_control_unusable", "browser control is unavailable"},
	} {
		for _, deadline := range []bool{false, true} {
			t.Run(tc.cause+map[bool]string{false: "/submit", true: "/deadline"}[deadline], func(t *testing.T) {
				checks := []Check{{ID: "c1", Name: "settings opens", Claim: true, State: CheckSkipped}}
				v := closeWithHeadControl(t, &LegControl{State: "lost", Since: 23, Cause: tc.cause, Detail: "control unavailable"}, checks, deadline, "")
				if v.Overall != CheckBlocked || len(v.Reasons) != 1 || v.Reasons[0] != "head app uncontrollable since action 23: "+tc.detail+"; unreached claim checks: settings opens" {
					t.Fatalf("verdict = %s, reasons %q", v.Overall, v.Reasons)
				}
			})
		}
	}
}

func TestEveryControlCausePreservesRecoveryAndFailure(t *testing.T) {
	for _, cause := range []string{"native_dialog", "browser_control_unusable", "app_exited", "app_unresponsive"} {
		for _, deadline := range []bool{false, true} {
			t.Run(cause+map[bool]string{false: "/submit", true: "/deadline"}[deadline], func(t *testing.T) {
				checks := []Check{
					{ID: "c1", Name: "menu opens", Claim: true, State: CheckPass},
					{ID: "b1", Name: "menu opens", Leg: "base", State: CheckFail, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
					{ID: "c2", Name: "settings opens", Claim: true, State: CheckSkipped},
				}
				v := closeWithHeadControl(t, &LegControl{State: "ok", Since: 17, Cause: cause}, checks, deadline, "")
				if v.Overall != CheckPartial {
					t.Fatalf("recovered verdict = %s, want partial", v.Overall)
				}
				checks[0].State = CheckFail
				checks[1].State = CheckPass
				v = closeWithHeadControl(t, &LegControl{State: "lost", Since: 17, Cause: cause, Detail: "control unavailable"}, checks, deadline, "")
				if v.Overall != CheckFail {
					t.Fatalf("failure verdict = %s, want fail", v.Overall)
				}
			})
		}
	}
}
