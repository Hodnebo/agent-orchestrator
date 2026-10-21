#!/usr/bin/env python3
"""Read public PRs and origin/main. Delete cached graphql-*.json to refresh PRs."""
import argparse
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import time

REPO = "OrchestratorInc/agent-orchestrator"
ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT.parents[2]
PAGE = "pageInfo { hasNextPage endCursor }"
FILES = "files(first:100,after:$cursor) { nodes { path additions deletions } " + PAGE + " }"
REVIEWS = "reviews(first:100,after:$cursor,states:[APPROVED,COMMENTED]) { nodes { author { login } } " + PAGE + " }"
IDENTITY = "oid author { email user { login } }"
QUERY = '''query($cursor:String) { repository(owner:"OrchestratorInc",name:"agent-orchestrator") {
  pullRequests(first:50,after:$cursor,states:MERGED,orderBy:{field:CREATED_AT,direction:DESC}) {
    totalCount nodes { number mergedAt changedFiles author { login }
      mergeCommit { IDENTITY } commits(last:1) { nodes { commit { IDENTITY } } }
      FILES REVIEWS
    } PAGE
  }
} }'''.replace("IDENTITY", IDENTITY).replace("FILES", FILES.replace(",after:$cursor", "")).replace("REVIEWS", REVIEWS.replace(",after:$cursor", "")).replace("PAGE", PAGE)


def is_bot(value, bot_logins=()):
    value = (value or "").lower()
    return bool(value in bot_logins or "[bot]" in value or value in {"noreply@github.com", "actions@github.com"} or
                re.search(r"(^|[^a-z0-9])(dependabot|github-actions|renovate)([^a-z0-9]|$)", value) or
                re.search(r"(^|[+._-])bot([+._@-]|$)", value))


def email_key(email):
    return email.strip().strip("<>").lower()


def email_login_map(identities):
    candidates = defaultdict(dict)
    for identity in identities:
        login = (identity.get("user") or {}).get("login")
        if login and identity.get("email"):
            candidates[email_key(identity["email"])][login.lower()] = login
    return {email: next(iter(logins.values())) for email, logins in candidates.items() if len(logins) == 1}


def resolve_email(email, mapping):
    email = email_key(email)
    if email in mapping:
        return mapping[email]
    match = re.fullmatch(r"(?:\d+\+)?([a-z0-9][a-z0-9-]*(?:\[bot\])?)@users\.noreply\.github\.com", email)
    return match[1] if match else None


def parse_blame(porcelain):
    counts, commits = Counter(), {}
    email = sha = None
    for line in porcelain.split("\n"):
        if re.match(r"^[0-9a-f]{40,64} \d+ \d+(?: \d+)?$", line):
            sha, email = line.split()[0], None
        elif line.startswith("author-mail "):
            email = email_key(line[12:])
        elif line.startswith("\t"):
            if not email or not sha:
                raise ValueError("Blame source line has no author email or commit")
            counts[email] += 1
            commits.setdefault(email, sha)
    return dict(counts), commits


def skip_reason(path, content=None):
    parts = PurePosixPath(path.lower()).parts
    name = parts[-1]
    if set(parts[:-1]) & {"node_modules", "dist", "build", "vendor", "vendored", "generated", "gen"}:
        return "generated_vendor_build"
    if name in {"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "go.sum", "bun.lockb"} or name.endswith(".lock"):
        return "lockfile"
    if name.endswith((".snap", ".snapshot")) or set(parts[:-1]) & {"snapshots", "__snapshots__"}:
        return "snapshot"
    if name.endswith((".min.js", ".min.css", ".map", ".pb.go")) or re.search(r"[._](?:gen|generated)\.", name) or path in {
        "backend/internal/httpd/apispec/openapi.yaml", "frontend/src/api/schema.ts", "packages/cloud-client/src/schema.ts",
    }:
        return "generated_minified"
    if content is None:
        return None
    try:
        text = content.decode("utf-8")
    except UnicodeDecodeError:
        return "binary_or_non_utf8"
    if "\0" in text:
        return "binary_or_non_utf8"
    if re.search(r"(?im)^[\s/*#;<!-]*(?:code generated\b|this file (?:is|was) (?:(?:auto[- ]?|automatically ))?generated\b|automatically generated\b|auto[- ]generated (?:file|code)\b|@generated\b)", text[:4000]):
        return "generated_header"
    lines = text.splitlines()
    if set(parts[:-1]) & {"fixture", "fixtures", "__fixtures__", "testdata"} and len(lines) > 2000:
        return "large_fixture"
    if name.endswith((".js", ".css")) and any(len(line) > 2000 for line in lines) and len(text) / max(len(lines), 1) > 500:
        return "minified_content"
    return None


def save(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    temporary.replace(path)


def api(cache, endpoint, variables=None):
    key = hashlib.sha256(json.dumps([endpoint, variables], sort_keys=True).encode()).hexdigest()
    path = cache / (("graphql-" if variables is not None else "commit-") + key + ".json")
    if path.exists():
        return json.loads(path.read_text())
    command = ["gh", "api", "graphql", "-f", "query=" + endpoint] if variables is not None else ["gh", "api", endpoint]
    for key, value in (variables or {}).items():
        if value is not None:
            command += ["-F", f"{key}={value}"]
    for attempt in range(3):
        try:
            response = subprocess.run(command, capture_output=True, text=True, check=True, timeout=60)
            break
        except (subprocess.TimeoutExpired, subprocess.CalledProcessError) as error:
            if attempt == 2:
                if error.stderr:
                    print(error.stderr.strip(), flush=True)
                raise
            print("GitHub request failed; retrying: " + str(error.stderr or type(error).__name__)[:200], flush=True)
    data = json.loads(response.stdout)
    if data.get("errors"):
        raise RuntimeError(json.dumps(data["errors"]))
    save(path, data)
    return data


def pull_requests(cache, cap):
    nodes, cursor = [], None
    while True:
        connection = api(cache, QUERY, {"cursor": cursor})["data"]["repository"]["pullRequests"]
        total = connection["totalCount"]
        nodes.extend(connection["nodes"][:cap - len(nodes)])
        if len(nodes) % 250 == 0 or len(nodes) == min(cap, total):
            print(f"PRs: {len(nodes)}/{min(cap, total)}", flush=True)
        if len(nodes) >= cap or not connection["pageInfo"]["hasNextPage"]:
            break
        cursor = connection["pageInfo"]["endCursor"]
    if len(nodes) != min(cap, total) or len({n["number"] for n in nodes}) != len(nodes):
        raise RuntimeError("PR pagination is inconsistent; refresh the GraphQL cache")
    identities, prs, representatives = [], [], {}
    for node in nodes:
        for commit in [node["mergeCommit"]] + [n["commit"] for n in node["commits"]["nodes"]]:
            if commit:
                identities.append(commit["author"])
        for field, selection in [("files", FILES), ("reviews", REVIEWS)]:
            connection = node[field]
            while connection["pageInfo"]["hasNextPage"]:
                query = 'query($number:Int!,$cursor:String) { repository(owner:"OrchestratorInc",name:"agent-orchestrator") { pullRequest(number:$number) { ' + selection + ' } } }'
                connection = api(cache, query, {"number": node["number"], "cursor": connection["pageInfo"]["endCursor"]})["data"]["repository"]["pullRequest"][field]
                node[field]["nodes"].extend(connection["nodes"])
        if len(node["files"]["nodes"]) != node["changedFiles"]:
            raise RuntimeError(f"Incomplete file list for PR #{node['number']}")
        for actor in [node["author"]] + [r["author"] for r in node["reviews"]["nodes"]]:
            if actor:
                representatives.setdefault(actor["login"], node["number"])
        author = (node["author"] or {}).get("login")
        if not is_bot(author):
            reviewers = {(r["author"] or {}).get("login") for r in node["reviews"]["nodes"]}
            prs.append({"number": node["number"], "merged_at": node["mergedAt"], "author": author,
                        "reviewers": sorted(r for r in reviewers if r and not is_bot(r)),
                        "files": [{"path": f["path"], "churn": f["additions"] + f["deletions"]} for f in node["files"]["nodes"]]})
    selection = "author { login __typename } reviews(first:100,states:[APPROVED,COMMENTED]) { nodes { author { login __typename } } }"
    query = 'query { repository(owner:"OrchestratorInc",name:"agent-orchestrator") { ' + ' '.join(f'p{n}:pullRequest(number:{n}) {{ {selection} }}' for n in sorted(set(representatives.values()))) + ' } }'
    typed = api(cache, query, {})["data"]["repository"].values()
    actors = [a for p in typed for a in [p["author"]] + [r["author"] for r in p["reviews"]["nodes"]] if a]
    if set(representatives) - {a["login"] for a in actors}:
        raise RuntimeError("Incomplete GitHub actor type lookup")
    bots = {a["login"].lower() for a in actors if a["__typename"] == "Bot"}
    prs = [{**p, "reviewers": [r for r in p["reviewers"] if not is_bot(r, bots)]}
           for p in prs if not is_bot(p["author"], bots)]
    return prs, identities, len(nodes), total, len(nodes) - len(prs), bots


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=SOURCE)
    parser.add_argument("--pr-cap", type=int, default=3000)
    args = parser.parse_args()
    if not 1 <= args.pr_cap <= 3000:
        parser.error("--pr-cap must be between 1 and 3000")
    started = time.monotonic()
    cache = ROOT / "research/ownership-cache"
    def git(*arguments):
        return subprocess.check_output(["git", "-C", str(args.repo), *arguments])
    sha = git("rev-parse", "origin/main").decode().strip()
    prs, identities, fetched, total, bot_prs, bots = pull_requests(cache, args.pr_cap)
    skipped, paths = Counter(), []
    with subprocess.Popen(["git", "-C", str(args.repo), "cat-file", "--batch"], stdin=subprocess.PIPE, stdout=subprocess.PIPE) as blobs:
        for entry in git("ls-tree", "-rz", sha).split(b"\0"):
            if not entry:
                continue
            metadata, raw_path = entry.split(b"\t", 1)
            mode, kind, oid = metadata.split()
            path = raw_path.decode()
            reason = "non_regular_file" if kind != b"blob" or mode == b"120000" else skip_reason(path)
            if reason is None:
                blobs.stdin.write(oid + b"\n")
                blobs.stdin.flush()
                size = int(blobs.stdout.readline().split()[-1])
                content = blobs.stdout.read(size)
                if blobs.stdout.read(1) != b"\n":
                    raise RuntimeError("Invalid cat-file batch response")
                reason = skip_reason(path, content)
            if reason:
                skipped[reason] += 1
            else:
                paths.append(path)
    def blame(path):
        key = hashlib.sha256((sha + path).encode()).hexdigest()
        cached = cache / ("blame-" + key + ".json")
        if not cached.exists():
            save(cached, parse_blame(git("blame", "--line-porcelain", "-w", "-M", "-C", sha, "--", path).decode("utf-8", errors="replace")))
        return path, json.loads(cached.read_text())
    raw, candidates = {}, defaultdict(set)
    with ThreadPoolExecutor(max_workers=4) as pool:
        for index, (path, (counts, commits)) in enumerate(pool.map(blame, paths), 1):
            raw[path] = counts
            for email, commit in commits.items():
                candidates[email].add(commit)
            if index % 100 == 0 or index == len(paths):
                print(f"Blame files: {index}/{len(paths)}", flush=True)
    mapping, lookups, lookup_errors = email_login_map(identities), 0, []
    for email, commits in sorted(candidates.items()):
        if is_bot(email, bots) or resolve_email(email, mapping):
            continue
        for commit in sorted(commits)[:2]:
            if lookups >= 40:
                break
            lookups += 1
            try:
                data = api(cache, f"repos/{REPO}/commits/{commit}")
            except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
                lookup_errors.append({"sha": commit, "error": str(error.stderr or error).strip()})
                continue
            identities.append({"email": data["commit"]["author"]["email"], "user": data["author"]})
            mapping = email_login_map(identities)
            if resolve_email(email, mapping):
                break
    canonical = {login.lower(): login for login in list(mapping.values()) + [p["author"] for p in prs if p["author"]]}
    blame_output, unmapped, bot_lines = {}, set(), 0
    for path, counts in raw.items():
        humans = Counter()
        for email, lines in counts.items():
            login = resolve_email(email, mapping)
            if is_bot(email, bots) or is_bot(login, bots):
                bot_lines += lines
                continue
            if login is None:
                unmapped.add(email)
            humans[canonical.get((login or email).lower(), login or email)] += lines
        blame_output[path] = dict(humans)
    meta = {"generated_at": datetime.now(timezone.utc).isoformat(), "origin_main_sha": sha,
            "pr_count": len(prs), "pr_fetched_count": fetched, "pr_total_count": total,
            "pr_cap": args.pr_cap, "pr_cap_hit": total > fetched, "blame_file_count": len(paths),
            "skipped": dict(skipped, bot_prs=bot_prs, bot_blame_lines=bot_lines), "unmapped_emails": sorted(unmapped),
            "identity_lookup_attempts": lookups, "identity_lookup_limit": 40, "identity_lookup_errors": lookup_errors,
            "excluded_bot_logins": sorted(bots),
            "review_states": ["APPROVED", "COMMENTED"], "runtime_seconds": round(time.monotonic() - started, 2)}
    save(ROOT / "research/ownership-raw.json", {"meta": meta, "prs": prs, "blame": blame_output})
    print(json.dumps(meta, indent=2), flush=True)


if __name__ == "__main__":
    main()
