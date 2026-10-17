# Verdict: pass

o/r#7, run run_s1, base 0ca80cfc370b21443c4a2b9d22199c047b8c74e7, head e2204e0f361867a28310e71421a4227ab1d09544

3 completed checks on the PR build; 2 checks compared on main; 1 check with different results.

Duration: 0s, never prepared. Tokens: not measured by Litmus, see the session's usage. Video: none (recording was not available).

claim sentences not checked


## Checks

| check | purpose | expect | head result | base result | receipts |
|---|---|---|---|---|---|
| c1 claim fix | claim | the fix works | pass | absent |  |
| c2 cleanup | claim | cleanup preserves facts | skipped | - |  |
| c5 existing fault | risk: folder setup can break | existing folders open | partial (pre-existing) | partial |  |
| c12 cleanup with facts | claim | cleanup preserves facts | pass | not compared on base |  |

## Superseded skips

- c2 superseded by c12

## Notes

### c1 claim fix (head leg): pass

Expected: the fix works

Note: 

### b1 claim fix (base leg): absent

Expected: 

Note: 

### c2 cleanup (head leg): skipped

Expected: cleanup preserves facts

Note: no cleanup facts

### c5 existing fault (head leg): partial

Expected: existing folders open

Note: existing folders remain partly broken

### b5 existing fault (base leg): partial

Expected: 

Note: 

### c12 cleanup with facts (head leg): pass

Expected: cleanup preserves facts

Note: created cleanup facts; cleanup preserved them

## Limits

- The base leg was not launched, so nothing was observed on it.
- c5 existing fault: pre-existing, same on base: existing folders remain partly broken
- c12 cleanup with facts: not compared on base, so whether the behavior changed cannot be told.
- A claimed head check is the proof of the PR's stated behavior. Skipped checks are unreached. Environment attribution preserves the observation and never proves a PR failure or improvement; an environmental head fail or partial without same-fault base evidence caps the run at partial. A same-fault head claim fail or partial caps the result at partial and is described as fails the same way on main and the PR build. A same-fault risk or explore head fail or partial with the same failing or partial base outcome and an existing base after-shot is pre-existing and does not cap the result. A recorded PR head failure wins before untested, including when no claimed check finished; an absent PR feature is a failure. Otherwise the result is untested when no claimed head check ended pass, fail, or partial. A head failure beside an unmarked base failure with evidence stays partial until the tester marks the same fault. Other head failures or partials cap the result. Pass requires every claimed head check to pass, at least one head pass to differ from the base, and no unexplained completed head partial. A base failure or a partial claim twin counts as a difference only with an existing after-shot. A head failure or partial beside an absent base is the PR's own. A skipped claimed check keeps the result partial at best unless a later claim check in stored order on the same leg has the identical non-empty claim sentence and ended pass, fail, or partial. Such a skip is superseded; the later result counts as itself. Skipped non-claim checks and base-only checks are context.
- The PR title claims a behavior change. That is informational and does not change the result.
- Only the checks above were tried; the rest of the PR's changes are untested.
