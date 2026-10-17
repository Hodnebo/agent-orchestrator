package litmus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTask43DeadlineClosePreservesBrokenLinkCountOnRestart(t *testing.T) {
	svc, runDir, _ := askSvc(t, true)
	run, err := svc.store.Get("run_s1")
	is(t, "")(nil, err)
	checks, err := svc.store.Checks(run.ID)
	is(t, "")(nil, err)
	checks[0].Note = "The menu opens. [note](missing-note.png)"
	blocker := filepath.Join(runDir, verdictRenderFile)
	is(t, "")(nil, os.Mkdir(blocker, 0o700))
	_, err = svc.closeRunAtDeadline(ctx, run, checks,
		"Observed [summary](missing-summary.png).",
		"Could not finish [blocked](missing-blocked.png).",
		"Main needs [fixture](missing-fixture.png).")
	if err == nil {
		t.Fatal("deadline close succeeded despite blocked verdict write")
	}
	accepted, err := svc.store.Get(run.ID)
	is(t, "")(nil, err)
	if accepted.submission == nil || accepted.submission.BrokenLinks != 4 || !accepted.submission.Deadline {
		t.Fatalf("saved deadline inputs lost the broken-link count: %+v", accepted.submission)
	}
	if !accepted.TestingEndedAt.Equal(run.Deadline) {
		t.Fatalf("testing end = %v, want deadline %v", accepted.TestingEndedAt, run.Deadline)
	}
	is(t, "")(nil, os.Remove(blocker))
	fresh := newTestService(t, NewStore(svc.store.dataDir()), func() time.Time { return run.Deadline.Add(time.Second) }).WithGH(fixtureGH(t))
	fresh.runner, fresh.retryPause = gitFixture(t), 0
	sweep(fresh, nil)
	closed, err := fresh.store.Get(run.ID)
	is(t, "")(nil, err)
	if !closed.State.Closed() {
		t.Fatalf("restart did not finish the accepted deadline close: %s", closed.State)
	}
	md, err := os.ReadFile(filepath.Join(runDir, "verdict.md"))
	is(t, "")(nil, err)
	const line = "At the deadline, 4 broken relative link(s) were shown as plain text."
	if strings.Count(string(md), line) != 1 {
		t.Fatalf("recovered verdict lost or duplicated the broken-link count:\n%s", md)
	}
	if broken := brokenRelativeLinks(runDir, string(md)); len(broken) != 0 {
		t.Fatalf("recovered verdict contains broken links: %v", broken)
	}
}
