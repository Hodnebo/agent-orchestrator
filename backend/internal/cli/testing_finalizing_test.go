package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/litmus"
)

func TestTestingRunStateShowsAcceptedSubmitFinalizing(t *testing.T) {
	run := litmus.Run{ID: "run_tester", State: litmus.RunRunning, TestingEndedAt: time.Now()}
	if got := testingRunState(run); got != "running" {
		t.Fatalf("persisted end marker changed state=%q", got)
	}
	if got := testingRunState(run, true); got != "submitted, finalizing" {
		t.Fatalf("in-flight submit state=%q", got)
	}
	if got := runLine(run); strings.Contains(got, "submitted, finalizing") {
		t.Fatalf("status inferred finalization from timestamp: %q", got)
	}
	if got := runLineWithSubmit(run, true); !strings.Contains(got, "submitted, finalizing") {
		t.Fatalf("status=%q", got)
	}
	run.State = litmus.RunSubmitted
	if got := testingRunState(run); got != "submitted" {
		t.Fatalf("completed state=%q", got)
	}
}
