import importlib.util
from datetime import datetime, timedelta, timezone
from math import log1p
import sys
from pathlib import Path
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))

SPEC = importlib.util.spec_from_file_location("build_owners", Path(__file__).resolve().parents[1] / "scripts/build_owners.py")
builder = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(builder)
AS_OF = datetime(2026, 10, 21, tzinfo=timezone.utc)
TAXONOMY = {"repo": "OrchestratorInc/agent-orchestrator", "areas": [
    {"id": key, "label": key} for key in ["a", "b", "shared-infra", "needs-area"]]}
RULES = [{"glob": "src/a/*", "area": "a"}, {"glob": "src/*", "area": "b"}, {"glob": "*", "area": "shared-infra"}]


def pr(number, author="alice", age=0, files=None, reviewers=()):
    return {"number": number, "author": author, "merged_at": (AS_OF - timedelta(days=age)).isoformat(),
            "files": files if files is not None else [{"path": "src/a/file.py", "churn": 100}], "reviewers": list(reviewers)}


def build(prs, blame=None, **kwargs):
    seeds = [pr(1000 + i, f"seed{i}", files=[{"path": "README.md", "churn": 1}]) for i in range(8)]
    raw = {"meta": {"generated_at": AS_OF.isoformat(), "origin_main_sha": "a" * 40, "excluded_bot_logins": ["cursor"]},
           "prs": seeds + prs, "blame": blame or {}}
    return builder.build(raw, RULES, TAXONOMY, generated_at=AS_OF.isoformat(), threshold=kwargs.pop("threshold", 1), **kwargs)


def owner(data, area, login="alice"):
    return next(person for person in data["areas"][area]["owners"] if person["login"] == login)


class OwnerBuilderTests(unittest.TestCase):
    def test_mechanical_files_do_not_change_pr_credit_or_churn(self):
        source = {"path": "src/a/file.py", "churn": 100}
        mechanical = [{"path": path, "churn": 1000000} for path in [
            "package-lock.json", "frontend/pnpm-lock.yaml", "go.sum", "yarn.lock",
            "vendor/import.go", "a/generated/code.ts", "a/gen/code.go", "a/dist/bundle.js",
            "test/__snapshots__/ui.snap", "api.pb.go", "backend/internal/httpd/apispec/openapi.yaml",
            "frontend/src/api/schema.ts", "packages/cloud-client/src/schema.ts",
        ]]
        _, baseline = build([pr(1, files=[source], reviewers=["seed0"])])
        _, actual = build([pr(1, files=[source] + mechanical, reviewers=["seed0"])])
        self.assertEqual(actual, baseline)

    def test_mechanical_only_and_fallback_prs_never_suggest_owners(self):
        core, data = build([pr(1, files=[{"path": "package-lock.json", "churn": 1000000}]),
                            pr(2, files=[{"path": "README.md", "churn": 100}])])
        self.assertTrue(any(person["login"] == "alice" for person in core["contributors"]))
        self.assertFalse(any(area["owners"] for area in data["areas"].values()))

    def test_pr_churn_grows_logarithmically(self):
        _, data = build([pr(1), pr(2, "bob", files=[{"path": "src/a/file.py", "churn": 1000000}])])
        alice = owner(data, "a")["weighted_churn"]
        bob = owner(data, "a", "bob")["weighted_churn"]
        self.assertAlmostEqual(bob, log1p(1000000), places=6)
        self.assertLess(bob / alice, 3)

    def test_churn_proportions_half_life_and_evidence(self):
        core, data = build([
            pr(1, age=90, files=[{"path": "src/a/one.py", "churn": 75}, {"path": "src/b.py", "churn": 25}]),
            pr(2), pr(3, files=[{"path": "src/b.py", "churn": 100}]),
        ], {"src/a/one.py": {"alice": 20}, "src/b.py": {"alice": 7}})
        a, b = owner(data, "a"), owner(data, "b")
        self.assertAlmostEqual(a["merged_prs_all_time"], 1.75)
        self.assertAlmostEqual(b["merged_prs_90d"], 1.25)
        self.assertAlmostEqual(a["weighted_churn"], log1p(100) * 1.375, places=6)
        self.assertAlmostEqual(b["weighted_churn"], log1p(100) * 1.125, places=6)
        self.assertEqual(a["blame_lines"], 20)
        self.assertEqual(a["last_merged_at"], AS_OF.isoformat())
        alice = next(person for person in core["contributors"] if person["login"] == "alice")
        self.assertEqual(alice["merged_prs_90d"], 3)
        self.assertEqual(alice["top_areas"], ["a", "b"])
        self.assertEqual(data["origin_main_sha"], "a" * 40)
        self.assertEqual(core["status"], "proposed")

    def test_window_boundary_and_old_prs(self):
        _, data = build([pr(1, age=90), pr(2, age=91), pr(3, age=180)])
        item = owner(data, "a")
        self.assertEqual(item["merged_prs_90d"], 1)
        self.assertEqual(item["merged_prs_all_time"], 3)
        self.assertAlmostEqual(item["weighted_churn"], log1p(100) * (0.5 + 2 ** (-91 / 90) + 0.25), places=6)

    def test_reviews_are_unique_proportional_recent_and_not_self_reviews(self):
        _, data = build([
            pr(1, "seed0", files=[{"path": "src/a/x", "churn": 50}, {"path": "src/b", "churn": 50}], reviewers=["alice", "alice", "seed0"]),
            pr(2, "seed0", reviewers=["alice"]), pr(3, "seed0", reviewers=["alice"]),
            pr(4, "seed0", age=91, reviewers=["alice"]),
            pr(5, files=[{"path": "README.md", "churn": 1}]),
        ])
        self.assertEqual(owner(data, "a")["reviews_90d"], 2.5)
        self.assertEqual(owner(data, "a", "seed0")["reviews_90d"], 0)

    def test_old_blame_and_tiny_recent_changes_do_not_establish_ownership(self):
        _, data = build([
            pr(1, files=[{"path": "src/a/x", "churn": 1}, {"path": "src/b", "churn": 999}]),
            pr(2, age=180), pr(3, files=[{"path": "src/b", "churn": 100}]),
        ], {"src/a/x": {"alice": 1000000}})
        self.assertEqual(data["areas"]["a"]["owners"], [])
        self.assertTrue(data["areas"]["a"]["unowned"])
        self.assertTrue(data["areas"]["needs-area"]["unowned"])

    def test_recent_prs_outrank_old_blame_and_noncore_is_excluded(self):
        records = [pr(1), pr(2), pr(3, "bob"), pr(4, "outsider", age=91)]
        _, data = build(records, {"src/a/x": {"bob": 1000000, "outsider": 1000000}})
        self.assertEqual([person["login"] for person in data["areas"]["a"]["owners"]], ["alice", "bob"])
        self.assertEqual(owner(data, "a")["score"], 85)
        self.assertLess(owner(data, "a", "bob")["score"], 85)

    def test_zero_churn_prs_split_equally_and_first_path_match_wins(self):
        _, data = build([pr(i, files=[{"path": "src/a/b", "churn": 0}, {"path": "src/c", "churn": 0}]) for i in [1, 2]])
        self.assertEqual(owner(data, "a")["merged_prs_90d"], 1)
        self.assertEqual(owner(data, "b")["merged_prs_90d"], 1)
        self.assertEqual(owner(data, "a")["weighted_churn"], 0)

    def test_bots_unresolved_emails_and_case_duplicates(self):
        core, data = build([pr(1, "Alice"), pr(2, "alice"), pr(3, "cursor"), pr(4, "app[bot]"), pr(5, "name@example.com")],
                           {"src/a/x": {"ALICE": 3, "cursor": 100, "name@example.com": 100}})
        self.assertEqual(next(person for person in core["contributors"] if person["login"] == "Alice")["merged_prs_90d"], 2)
        self.assertEqual([item["login"] for item in data["areas"]["a"]["owners"]], ["Alice"])
        self.assertEqual(owner(data, "a", "Alice")["blame_lines"], 3)

    def test_bad_input_and_core_threshold_stop_generation(self):
        for records, kwargs, message in [
            ([pr(1), pr(1)], {}, "duplicate PR"), ([pr(1, age=-1)], {}, "after the evidence"),
            ([pr(1)], {"threshold": 10}, "selects 0"),
        ]:
            with self.subTest(message=message), self.assertRaisesRegex(ValueError, message):
                build(records, **kwargs)

    def test_edited_core_membership_controls_owner_candidates(self):
        edited = {"repo": TAXONOMY["repo"], "contributors": [{"login": "bob"}]}
        core, data = build([pr(1), pr(2), pr(3, "bob")], core_override=edited)
        self.assertEqual([person["login"] for person in core["contributors"]], ["bob"])
        self.assertEqual([person["login"] for person in data["areas"]["a"]["owners"]], ["bob"])
        self.assertEqual(core["selection"], "edited core list")
        self.assertIsNone(core["threshold_merged_prs_90d"])


if __name__ == "__main__":
    unittest.main()
