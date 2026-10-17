package litmus

import "testing"

func TestSubmitBudgetCoversAllPhases(t *testing.T) {
	parts := FinalizeBudget + TeardownBudget + doneReportBudget + PostBudget + PruneBudget
	if SubmitBudget < parts {
		t.Fatalf("submit budget %s is shorter than its phases %s", SubmitBudget, parts)
	}
}
