package litmus

import (
	"strings"
	"testing"
)

func TestCommentNotesPreserveTesterProse(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain words",
			in:   "Its worktree HEAD is abc; stale-base still compares head and base.",
			want: "Its worktree HEAD is abc; stale-base still compares head and base.",
		},
		{
			name: "spacing",
			in:   "the head works;  base passed; head and base remain stale-base",
			want: "the head works; base passed; head and base remain stale-base",
		},
		{
			name: "markdown and mentions",
			in:   "`head failed` and https://host/base/failed and @e12 | <b> [head and base] @octocat",
			want: "`head failed` and https://host/base/failed and @\u200be12 \\| &lt;b> \\[head and base] @\u200boctocat",
		},
		{
			name: "newline",
			in:   "worktree HEAD is abc;\n\tstale-base compares head and base!",
			want: "worktree HEAD is abc; stale-base compares head and base!",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := []Check{{ID: "c1", Name: "build identity", State: CheckFail, Attribution: "pr", Note: " \n" + tc.in + "\t "}}
			v := commentVerdict(checks)
			docs := draftCommentFixture(t, commentRunFixture{run: commentRun, checks: checks})
			if len(docs) != 1 {
				t.Fatalf("draft count = %d, want 1", len(docs))
			}
			if row := compactChecks(commentRun, v); row != "- build identity: fail (main: not compared). "+tc.want {
				t.Errorf("row changed the note bytes: %q", row)
			}
			if details := fullCheckNotes(commentRun, v); details != "<details><summary>Full check notes</summary>\n\n- build identity\n\n  The PR build: fail. "+tc.want+"\n\n</details>" {
				t.Errorf("details changed the note bytes or generated label: %q", details)
			}
			if !strings.Contains(docs[0].Body, "Bug 1 of 1:\n\n"+tc.want+"\n\n") {
				t.Errorf("comment 1 changed the note bytes:\n%s", docs[0].Body)
			}
			checks[0].State, checks[0].Leg = CheckSkipped, "base"
			if rows := strings.Join(skippedCheckRows(commentRun, checks), "\n"); rows != "build identity (main: "+tc.want+")" {
				t.Errorf("skipped row changed the tester note:\n%s", rows)
			}
			if details := fullCheckNotes(commentRun, commentVerdict(checks)); !strings.Contains(details, "Main: skipped. "+tc.want) {
				t.Errorf("main details changed the tester note or generated label:\n%s", details)
			}
		})
	}
}

func TestFailVerdictAlwaysHasBugComments(t *testing.T) {
	after := evidenceFile(t)
	for _, headState := range []CheckState{CheckFail, CheckPartial, CheckAbsent} {
		for _, baseState := range []CheckState{"", CheckPass, CheckFail, CheckPartial, CheckAbsent, CheckSkipped} {
			for _, attribution := range []string{"", "pr", "unknown", "environment", "pre-existing"} {
				for _, purpose := range []string{"claim", "risk"} {
					for _, sameFault := range []bool{false, true} {
						for _, evidenced := range []bool{false, true} {
							checks := []Check{{ID: "c1", Name: "behavior", State: headState, Purpose: purpose, Claim: purpose == "claim", Attribution: attribution}}
							if baseState != "" {
								base := Check{ID: "c2", Name: "behavior", Leg: "base", State: baseState, SameFault: sameFault}
								if evidenced {
									base.Receipts = []Receipt{{Leg: "base", AfterPath: after}}
								}
								checks = append(checks, base)
							}
							if overall, reasons := derive(checks); overall == CheckFail && len(failingBugComments(checks)) == 0 {
								t.Fatalf("fail has no bug: head=%s main=%s attribution=%q purpose=%s sameFault=%t evidenced=%t reasons=%q", headState, baseState, attribution, purpose, sameFault, evidenced, reasons)
							}
						}
					}
				}
			}
		}
	}
}
