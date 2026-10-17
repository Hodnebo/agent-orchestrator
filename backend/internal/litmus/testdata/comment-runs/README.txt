Fixture provenance

Source runs were copied read-only from:

- run243: /Users/rishetmehra/.ao/data/litmus/host/litmus/runs/run_ao-243
- run244: /Users/rishetmehra/.ao/data/litmus/host/litmus/runs/run_ao-244
- run246: /Users/rishetmehra/.ao/data/litmus/host/litmus/runs/run_ao-246

Each fixture retains run.json and checks.json. The testdata also retains only
the theme screenshots needed to compose the current bug image, plus the three
run246 base after shots needed by ranAndFailed for originalProblemLine. All
other receipts, recordings, app state, worktrees, logs, comments, and generated
run artifacts were left out.

Receipt paths in the copied JSON were sanitized from host paths. The test then
rebases BeforePath, AfterPath, and ThemeShots paths into a fresh temporary
evidence directory and copies only the retained screenshots there. The source
run directories are never modified.

The test creates small per-check placeholder files in its temporary store so
the renderer can emit relative recording links. They are only Markdown-link
fixtures and are not video evidence. No clip binaries are stored here.

run243/golden.md and run244/golden.md are Markdown snapshots of the draft
renderer. Refresh them only for intentional changes to the rendered Markdown.
