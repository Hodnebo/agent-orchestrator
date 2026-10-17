package litmus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run 245 recorded the existing-folder regression as a risk. Its matching
// base partial is therefore a pre-existing note and must not cap the proven
// claim fixes. The source run lives under ~/.ao; this small fixture keeps the
// verdict test independent of that machine data.
func TestRun245RegeneratesPassWithExistingFolderRisk(t *testing.T) {
	raw, err := os.ReadFile("testdata/run245-checks.json")
	if err != nil {
		t.Fatal(err)
	}
	var checks []Check
	if err := json.Unmarshal(raw, &checks); err != nil {
		t.Fatal(err)
	}
	baseAfter := filepath.Join(t.TempDir(), "base-after.png")
	if err := os.WriteFile(baseAfter, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := range checks {
		if checks[i].onLeg() != "base" {
			continue
		}
		for j := range checks[i].Receipts {
			checks[i].Receipts[j].AfterPath = baseAfter
		}
	}

	overall, reasons := derive(checks)
	if overall != CheckPass {
		t.Fatalf("run 245 derived %s, reasons %q", overall, reasons)
	}
	for _, check := range checks {
		if check.onLeg() == "head" && check.Name == "Existing-folder setup" {
			if check.Purpose != "risk" || !preExisting(checks, check) {
				t.Fatalf("run 245 c5 was not treated as a risk pre-existing check: %+v", check)
			}
			return
		}
	}
	t.Fatal("run 245 fixture has no head Existing-folder setup check")
}

func TestClaimSameFaultReasonNamesBothBuilds(t *testing.T) {
	for _, state := range []CheckState{CheckFail, CheckPartial} {
		t.Run(string(state), func(t *testing.T) {
			checks := []Check{
				{ID: "c1", Name: "claimed behavior", Purpose: "claim", Claim: true, State: state},
				{ID: "c2", Name: "claimed behavior", Purpose: "claim", Claim: true, Leg: "base", State: state, SameFault: true, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
			}
			overall, reasons := derive(checks)
			if overall != CheckPartial || !strings.Contains(strings.Join(reasons, " "), "fails the same way on main and the PR build") {
				t.Fatalf("same-fault claim %s = %s, %q", state, overall, reasons)
			}
		})
	}
}

func TestClaimFlagWinsConflictingRiskPurpose(t *testing.T) {
	checks := []Check{
		{ID: "c1", Name: "claimed behavior", Purpose: "risk", Claim: true, State: CheckFail},
		{ID: "c2", Name: "claimed behavior", Purpose: "risk", Claim: true, Leg: "base", State: CheckFail, SameFault: true, Receipts: []Receipt{{Leg: "base", AfterPath: evidenceFile(t)}}},
	}
	if preExisting(checks, checks[0]) {
		t.Fatal("a claim must not be exempted by its conflicting risk purpose")
	}
	overall, reasons := derive(checks)
	if overall != CheckPartial || !strings.Contains(strings.Join(reasons, " "), "fails the same way on main and the PR build") {
		t.Fatalf("conflicting-purpose claim = %s, %q", overall, reasons)
	}
}
