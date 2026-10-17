package litmus

import (
	"strings"
	"testing"
	"time"
)

func TestCommentTimingDoesNotInventLegacyTestingWindow(t *testing.T) {
	run := commentRun
	run.State = RunSubmitted
	run.Deadline = run.CreatedAt.Add(3*time.Minute + RunBudget)
	run.TestingEndedAt = time.Time{}
	for name, text := range map[string]string{
		"summary": summaryCommentBody(run, commentVerdict(nil), run.CreatedAt.Add(40*time.Minute), false),
		"comment": renderComment(run, commentVerdict(nil), evidence{}, run.CreatedAt.Add(40*time.Minute)),
	} {
		if !strings.Contains(text, "builds 3 minutes, testing not recorded") {
			t.Errorf("%s invented testing duration: %s", name, text)
		}
	}
}
