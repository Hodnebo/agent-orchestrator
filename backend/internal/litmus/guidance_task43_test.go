package litmus

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTask43ReplayHintsUseReplayAdmissibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Run, *[]Check)
		want string
	}{
		{name: "new twin"},
		{name: "pending twin", edit: func(_ *Run, checks *[]Check) {
			*checks = append(*checks, Check{ID: "b1", Name: "menu", Leg: "base", State: CheckPending, Purpose: "explore"})
		}},
		{name: "running twin", edit: func(_ *Run, checks *[]Check) {
			*checks = append(*checks, Check{ID: "b1", Name: "menu", Leg: "base", State: CheckRunning, Purpose: "explore"})
		}},
		{name: "another check running", want: "TESTING_CHECK_STATE", edit: func(_ *Run, checks *[]Check) {
			*checks = append(*checks, Check{ID: "c2", Name: "other", State: CheckRunning, Purpose: "explore"})
		}},
		{name: "mixed supported and unsupported actions", want: "TESTING_REPLAY_ACTION", edit: func(_ *Run, checks *[]Check) {
			(*checks)[0].Receipts = append((*checks)[0].Receipts, Receipt{Leg: "head", Action: "snapshot"})
		}},
		{name: "two target action", want: "TESTING_REPLAY_ACTION", edit: func(_ *Run, checks *[]Check) {
			(*checks)[0].Receipts = append((*checks)[0].Receipts, Receipt{Leg: "head", Action: "drag"})
		}},
		{name: "unsupported case", want: "TESTING_REPLAY_ACTION", edit: func(_ *Run, checks *[]Check) {
			(*checks)[0].Receipts[0].Action = "PRESS"
		}},
		{name: "base actions ignored", edit: func(_ *Run, checks *[]Check) {
			(*checks)[0].Receipts = append((*checks)[0].Receipts, Receipt{Leg: "base", Action: "snapshot"})
		}},
		{name: "empty head actions", want: "TESTING_REPLAY_EMPTY", edit: func(_ *Run, checks *[]Check) {
			(*checks)[0].Receipts[0].Leg = "base"
		}},
		{name: "head running", want: "TESTING_REPLAY_CHECK_STATE", edit: func(_ *Run, checks *[]Check) {
			(*checks)[0].State = CheckRunning
		}},
		{name: "ended twin", want: "TESTING_REPLAY_CHECK_STATE", edit: func(_ *Run, checks *[]Check) {
			*checks = append(*checks, Check{ID: "b1", Name: "menu", Leg: "base", State: CheckPass, Purpose: "explore"})
		}},
		{name: "twin classification differs", want: "TESTING_CHECK_TWIN_CLASS", edit: func(_ *Run, checks *[]Check) {
			*checks = append(*checks, Check{ID: "b1", Name: "menu", Leg: "base", State: CheckPending, Purpose: "risk", Risk: "menu closes"})
		}},
		{name: "base not launched", want: "TESTING_LEG_NOT_LAUNCHED", edit: func(run *Run, _ *[]Check) {
			run.Legs = slices.DeleteFunc(run.Legs, func(leg Leg) bool { return leg.Name == "base" })
		}},
		{name: "closed run", want: "TESTING_RUN_CLOSED", edit: func(run *Run, _ *[]Check) { run.State = RunSubmitted }},
		{name: "expired run", want: "TESTING_RUN_CLOSED", edit: func(run *Run, _ *[]Check) { run.Deadline = run.CreatedAt }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := driveSvc(t)
			base, err := svc.attachEngine(ctx, "run_s1", Leg{Name: "base", State: "running", DebugPort: 9401, Engine: &EngineInfo{AppOrigin: "http://app.test"}})
			is(t, "")(nil, err)
			svc.engineFor("run_s1", base).identity = fakeDaemonIdentity
			run, err := svc.store.Get("run_s1")
			is(t, "")(nil, err)
			run.Legs = append(run.Legs, base)
			checks := []Check{{ID: "c1", Name: "menu", State: CheckPass, Purpose: "explore", Receipts: []Receipt{{Action: "press", Leg: "head", Args: []string{"Enter"}}}}}
			if tc.edit != nil {
				tc.edit(&run, &checks)
			}
			is(t, "")(nil, svc.store.Save(run))
			is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
			_, hints := testingStatusGuidance(run, checks, svc.clock())
			hinted := slices.ContainsFunc(hints, func(hint TestingHint) bool {
				return hint.CheckID == "c1" && strings.Contains(hint.Text, "ao testing replay c1 --leg base")
			})
			if hinted != (tc.want == "") {
				t.Fatalf("replay hint present = %t, want %t; hints %+v", hinted, tc.want == "", hints)
			}
			_, err = svc.Replay(ctx, "s1", "c1", "base")
			is(t, tc.want)(nil, err)
			if tc.want != "" {
				after, err := svc.store.Checks(run.ID)
				is(t, "")(nil, err)
				if !reflect.DeepEqual(after, checks) {
					t.Fatal("refused replay changed the ledger")
				}
			}
		})
	}
}

func TestTask43SameBugHintsUseCheckEndValidation(t *testing.T) {
	svc, _ := newSvc(t)
	run, err := svc.Intake(ctx, "s1", prURL)
	is(t, "")(nil, err)
	checks := []Check{
		{ID: "c1", Name: "first", State: CheckFail, Purpose: "risk"},
		{ID: "c2", Name: "second", State: CheckPartial, Purpose: "risk"},
		{ID: "c3", Name: "passed", State: CheckPass, Purpose: "risk"},
		{ID: "b1", Name: "base", Leg: "base", State: CheckFail, Purpose: "risk"},
	}
	_, hints := testingStatusGuidance(run, checks, svc.clock())
	count := 0
	for _, hint := range hints {
		if !strings.Contains(hint.Text, "--same-bug") {
			continue
		}
		source, _ := checkByID(checks, hint.CheckID)
		for _, target := range checks {
			if !strings.Contains(hint.Text, target.ID+" \"") {
				continue
			}
			count++
			is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
			is(t, "")(svc.CheckEnd(ctx, "s1", source.ID, source.State, "", false, 0, CheckAttribution{SameBug: target.ID}))
		}
	}
	if count != 2 {
		t.Fatalf("checked %d suggested same-bug links, want 2; hints %+v", count, hints)
	}
}
