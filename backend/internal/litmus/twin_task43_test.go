package litmus

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTask43ReclassifyTwinCannotChangeReplacementCoverage(t *testing.T) {
	for _, leg := range []string{"head", "base"} {
		for _, state := range []CheckState{CheckPending, CheckRunning} {
			for _, choice := range []CheckClassification{
				{Purpose: "explore"},
				{Purpose: "risk", Risk: "the menu can close"},
				{Purpose: "claim", ClaimSentence: `Clicking "Other models" in the chat model menu no longer closes the menu, and the "Use agent model" row is gone from the model list.`},
			} {
				t.Run(leg+"/"+string(state)+"/"+choice.Purpose, func(t *testing.T) {
					svc, _ := newSvc(t)
					run, err := svc.Intake(ctx, "s1", prURL)
					is(t, "")(nil, err)
					other := "base"
					if leg == "base" {
						other = "head"
					}
					const claim = "The row now stops that click."
					checks := []Check{
						{ID: "old", Name: "old", Leg: leg, State: CheckFail, Purpose: "claim", Claim: true, ClaimSentence: claim},
						{ID: "replacement", Name: "menu", Leg: leg, State: state, Purpose: "claim", Claim: true, ClaimSentence: claim, Replaces: "old"},
						{ID: "twin", Name: "menu", Leg: other, State: CheckPending, Purpose: "claim", Claim: true, ClaimSentence: claim},
					}
					is(t, "")(nil, svc.store.SaveChecks(run.ID, checks))
					path := filepath.Join(svc.store.root, run.ID, "checks.json")
					before, err := os.ReadFile(path)
					is(t, "")(nil, err)
					is(t, "TESTING_CHECK_REPLACEMENT")(svc.CheckReclassify(ctx, "s1", "twin", choice, "change the paired coverage"))
					after, err := os.ReadFile(path)
					is(t, "")(nil, err)
					if string(before) != string(after) {
						t.Fatal("refused twin reclassification changed saved coverage or audit history")
					}
				})
			}
		}
	}
}
