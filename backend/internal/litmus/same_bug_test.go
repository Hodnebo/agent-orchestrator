package litmus

import "testing"

func TestCheckEndSameBugValidationAndPersistence(t *testing.T) {
	t.Run("target ended before source", func(t *testing.T) {
		svc, _ := newSvc(t)
		run, err := svc.Intake(ctx, "s1", prURL)
		is(t, "")(run, err)
		target := Check{ID: "c2", Name: "second observation", State: CheckRunning, Receipts: []Receipt{{Seq: 1, AfterPath: evidenceFile(t)}}}
		source := Check{ID: "c1", Name: "first observation", State: CheckRunning, Receipts: []Receipt{{Seq: 2, AfterPath: evidenceFile(t)}}}
		is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{source, target}))
		is(t, "")(svc.CheckEnd(ctx, "s1", "c2", CheckFail, "original fault", false, 0, CheckAttribution{Kind: "pr"}))

		linked, err := svc.CheckEnd(ctx, "s1", "c1", CheckPartial, "same rendering bug", false, 0, CheckAttribution{Kind: "unknown", SameBug: "c2"})
		is(t, "")(linked, err)
		if linked.SameBug != "c2" || linked.State != CheckPartial || linked.Attribution != "unknown" {
			t.Fatalf("returned link = %+v", linked)
		}
		saved, err := svc.store.Checks(run.ID)
		is(t, "")(nil, err)
		if saved[0].SameBug != "c2" || saved[0].State != CheckPartial {
			t.Fatalf("persisted link = %+v", saved[0])
		}
		bugs := failingBugComments(saved)
		if len(bugs) != 1 || bugs[0].Check.ID != "c2" || len(bugs[0].Linked) != 1 || bugs[0].Linked[0].ID != "c1" {
			t.Fatalf("ended checks did not resolve to c2: %+v", bugs)
		}
	})

	for _, tc := range []struct {
		name   string
		target Check
		source Check
		result CheckState
		want   string
	}{
		{name: "missing target", target: Check{ID: "c9", State: CheckFail}, source: Check{ID: "c1", State: CheckRunning}, result: CheckFail, want: "TESTING_CHECK_SAME_BUG"},
		{name: "passing target", target: Check{ID: "c2", State: CheckPass}, source: Check{ID: "c1", State: CheckRunning}, result: CheckFail, want: "TESTING_CHECK_SAME_BUG"},
		{name: "running target", target: Check{ID: "c2", State: CheckRunning}, source: Check{ID: "c1", State: CheckRunning}, result: CheckFail, want: "TESTING_CHECK_SAME_BUG"},
		{name: "base target", target: Check{ID: "c2", Leg: "base", State: CheckFail}, source: Check{ID: "c1", State: CheckRunning}, result: CheckFail, want: "TESTING_CHECK_SAME_BUG"},
		{name: "passing source", target: Check{ID: "c2", State: CheckFail}, source: Check{ID: "c1", State: CheckRunning}, result: CheckPass, want: "TESTING_CHECK_SAME_BUG"},
		{name: "absent source", target: Check{ID: "c2", State: CheckFail}, source: Check{ID: "c1", State: CheckRunning}, result: CheckAbsent, want: "TESTING_CHECK_SAME_BUG"},
		{name: "base source", target: Check{ID: "c2", State: CheckFail}, source: Check{ID: "c1", Leg: "base", State: CheckRunning}, result: CheckFail, want: "TESTING_CHECK_SAME_BUG"},
		{name: "self target", target: Check{ID: "c1", State: CheckFail}, source: Check{ID: "c1", State: CheckRunning}, result: CheckFail, want: "TESTING_CHECK_SAME_BUG"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newSvc(t)
			run, err := svc.Intake(ctx, "s1", prURL)
			is(t, "")(run, err)
			target, source := tc.target, tc.source
			target.Receipts = []Receipt{{Seq: 1, Leg: target.Leg, AfterPath: evidenceFile(t)}}
			source.Receipts = []Receipt{{Seq: 2, Leg: source.Leg, AfterPath: evidenceFile(t)}}
			is(t, "")(nil, svc.store.SaveChecks(run.ID, []Check{target, source}))
			_, err = svc.CheckEnd(ctx, "s1", "c1", tc.result, "same bug", false, 0, CheckAttribution{SameBug: "c2"})
			is(t, tc.want)(nil, err)
		})
	}

	t.Run("re-ending changes only the link", func(t *testing.T) {
		svc, _ := newSvc(t)
		run, err := svc.Intake(ctx, "s1", prURL)
		is(t, "")(run, err)
		checks := []Check{
			{ID: "c1", Name: "first", State: CheckPartial, SameBug: "c2", Note: "old note", Receipts: []Receipt{{Seq: 1, AfterPath: evidenceFile(t)}}},
			{ID: "c2", Name: "second", State: CheckFail, Receipts: []Receipt{{Seq: 2, AfterPath: evidenceFile(t)}}},
			{ID: "c3", Name: "third", State: CheckFail, Receipts: []Receipt{{Seq: 3, AfterPath: evidenceFile(t)}}},
		}
		is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
		updated, err := svc.CheckEnd(ctx, "s1", "c1", CheckPartial, "corrected note", false, 0, CheckAttribution{SameBug: "c3"})
		is(t, "")(updated, err)
		if updated.SameBug != "c3" || updated.Note != "corrected note" || updated.State != CheckPartial {
			t.Fatalf("re-ended check = %+v", updated)
		}
		_, err = svc.CheckEnd(ctx, "s1", "c1", CheckPartial, "bad shot", false, 9, CheckAttribution{SameBug: "c2"})
		is(t, "TESTING_ARGS_INVALID")(nil, err)
		_, err = svc.CheckEnd(ctx, "s1", "c1", CheckPartial, "bad attribution", false, 0, CheckAttribution{Kind: "unknown", SameBug: "c2"})
		is(t, "TESTING_ARGS_INVALID")(nil, err)
	})

	t.Run("re-ending rejects a cycle", func(t *testing.T) {
		svc, _ := newSvc(t)
		run, err := svc.Intake(ctx, "s1", prURL)
		is(t, "")(run, err)
		checks := []Check{
			{ID: "c1", Name: "first", State: CheckFail, Receipts: []Receipt{{Seq: 1, AfterPath: evidenceFile(t)}}},
			{ID: "c2", Name: "second", State: CheckFail, Receipts: []Receipt{{Seq: 2, AfterPath: evidenceFile(t)}}},
		}
		is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
		is(t, "")(svc.CheckEnd(ctx, "s1", "c1", CheckFail, "link", false, 0, CheckAttribution{SameBug: "c2"}))
		_, err = svc.CheckEnd(ctx, "s1", "c2", CheckFail, "cycle", false, 0, CheckAttribution{SameBug: "c1"})
		is(t, "TESTING_CHECK_SAME_BUG")(nil, err)
	})
}
