# 7. Keep problem reporting available during daemon outages

Date: 2026-10-19
Status: Proposed

## Context

The Help form requires the user to describe a failure and collect its context.
Starting an ordinary agent session also requires the daemon and SQLite. That
cannot report a daemon startup failure reliably.

## Decision

The desktop starts one bundled `ao support-host` process over private standard
input/output pipes. This hidden entry point runs a support workflow, like the
existing internal provider-host entry points. Ordinary CLI commands remain thin
daemon clients. No HTTP listener or daemon route is added.

The support process owns its configured-agent snapshot, private run manifests,
outage provider conversation, and publication state under the AO data directory's
`reports/triage` directory. Its provider hosts have a separate state root so
normal daemon reconciliation cannot remove them as orphaned sessions.

Report a Problem starts diagnostic collection while the user chooses a
configured harness. Start investigation uses current model, effort and account
defaults without a separate settings expander. It authorizes filing the resulting
scrubbed report.

When the daemon is healthy, the desktop creates an ordinary projectless worker
through the existing session endpoint and opens its normal session route. The
agent receives a bundled bug-triage playbook and bounded private diagnostics as
ordinary attachments. Existing attachment persistence covers reopening and
workspace restoration. The session keeps normal typing, tools, permissions and
Chat or terminal behavior. Unreal uses Chat and requires the same explicit
approval choice as an ordinary launch. A one-start override does not change the
configured default.

The handoff freezes the create request and client request ID. Repeating an
uncertain request reopens or retries that same session. A durable checkpoint
before POST prevents a second investigator after an unknown create outcome. A
known-unsent handoff can instead use the independent outage path. Neither a
later daemon outage nor navigation silently changes an existing session's owner.

When the daemon is unavailable, the helper runs the selected agent independently.
This recovery conversation reuses ChatWorkspace, messages, composer and option
controls. Ordinary text queues until the provider is ready, separately from
native approval answers. Saving a draft does not close the conversation. Queued
human corrections are reviewed before automatic publication. This fallback is
not a normal daemon-owned session. App exit stops its provider and saves an
interruption. Closing the chooser cancels only an unused preparation.

Automatic evidence includes independent build/process/health facts, retained
desktop startup output, cached machine/session facts when present, and selected
fields from the affected provider-host descriptor. Cached facts retain their
source and measurement flags. Missing data is explicitly unavailable. The first
snapshot works without SQLite or the daemon. An optional retained-context stage
reuses the existing read-only SQLite adapter for bounded session/turn facts and
workspace roots. Global entry points discover recent local session labels so the
agent can ask which session is affected without requiring a typed ID. The engine
checks that selection before reading its private history. It starts no services
or migrations.

The engine privately supplies available retained daemon logs and the selected
session's conversation, tool events, provider journal and hook errors. Missing
history, provider stderr that was never retained, and old schemas remain explicitly
unavailable. The agent can request more bounded pages or relevant files from the
affected workspace. File reads reuse the preview reader with a pinned root,
reject symlinks and sensitive files, and stay within per-read and run limits.
Remote or cloud sessions cannot read local records. Private chunks go only to the
selected provider. The saved report keeps collection receipts; the publisher
receives the agent's selected, scrubbed evidence. No browser profile is collected.

Only explicitly configured choices appear. The outage fallback reuses the
existing Chat registry. Registered terminal adapters use a hidden private PTY
and structured request/result files when native Chat is unavailable or an explicit
terminal mode is selected. Each result carries the run, dispatch and random
identity; screen text and process exit are never treated as a completed draft.
The support process checks streaming and interrupt capabilities before sending a
native Chat turn. It preserves normal configured mode and permissions, including
an explicitly configured bypass mode, without silently enabling one or choosing
another agent.
Native resume is unnecessary because an interrupted job saves its report.
Reopening the helper reconciles abandoned native and terminal providers under
the run's ownership claim. A cleanup error remains visible on the saved report.

The desktop retains existing local settings, readiness, model and account
display data as it changes, before the launcher is opened. It does not fetch
missing catalogs or launch providers for this snapshot. An uncached model list
for one configured harness does not hide the others. Last-known choices survive
unknown readiness; explicit sign-out and disabled models are respected.

The reporting instructions prohibit project changes and additional investigators.
Normal sessions retain their ordinary tools and permission controls. The outage
fallback supplies bounded context and declines unsupported native permissions;
terminal prompts identified by the selected adapter show their current screen
and forward only a key or exact text the human explicitly chooses. This does
not promise universal filesystem or network isolation for configured provider
tools, integrations or startup hooks. Failed launches retain a copy/report fallback.
The outage fallback uses Unreal's persisted no-tools diagnostic policy, which
also skips workspace skill discovery. Normal investigation sessions use the
ordinary mutable policy and require an explicit permission choice.

Clarification uses a shared option-first question contract with Something else,
I'm not sure and optional details. It does not require a report form. Progress,
questions and the final link stay with the selected investigating agent.

The agent drafts the report. A separate publisher uses the existing GitHub
login and resolves the canonical repository. Bounded searches read open and
closed issues and PRs for the agent to compare with the symptoms and affected
build. A merged PR is not proof its fix shipped. A selected existing report is
verified again before returning its link. Otherwise, the publisher persists the
exact intent before publishing. Unknown POST outcomes are reconciled rather
than blindly retried. Starting the investigation explicitly authorizes filing
the resulting scrubbed issue. The launcher discloses that action. If credentials
are missing, desktop GitHub sign-in reuses the existing browser OAuth flow.
The reporting engine passes credentials only from main to the private helper,
never through the report UI or tool results. Normal agents use explicit bounded
read, search, save and publish operations; ordinary assistant messages are not
workflow commands. The existing AO MCP server exposes one report operation tool
outside the harness's shell sandbox. It uses the inherited data directory and
calling session identity, and checks the report's saved session link under its
ownership lock. Tool arguments cannot select another data directory or establish
that link. Harnesses without this MCP integration use the exact bundled helper
path in the private attachment and request native permission if required. The
flow never widens permissions automatically.

Small sign-in and retry actions remain available beside the
normal session. Search sign-in resumes the same agent. A pre-publication sign-in
retry may reassess existing reports with the same selected agent; it preserves
the frozen report and never bypasses an uncertain POST.

## Consequences

Basic collection and outage reporting do not depend on the daemon. They need a small
private IPC contract and its own persistence and ownership tests. Protocol types
are manually mirrored in Go and TypeScript, not added to the daemon OpenAPI.

The app must retain a manual copy/report fallback when the bundled helper or
selected provider cannot run. A saved interrupted run is readable; it is not
automatically replayed into a replacement provider conversation.

The collector does not start the daemon, repair configuration, or run readiness
commands. Optional evidence failures produce a partial report. The report is not
a complete session or project archive.
