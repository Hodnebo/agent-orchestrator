# Agent switching performance

Switching a chat to another agent should feel instant. The target is the
switch itself, from send to the new agent accepting the message, in under one
second. The model's time to its first token comes after and is not counted.

## How a switch runs

1. Picking an agent in the model menu only records the choice in the renderer.
   Nothing reaches the daemon, so picking and changing your mind cost nothing.
2. The next send carries the switch: `POST /sessions/{id}/switch-agent` with the
   message, model, and reasoning effort. The message shows at once and reads as
   Working from the send; messages sent meanwhile wait in the queue dock.
3. The daemon stops the source agent, starts the target, hands context over in
   the target's system prompt, and delivers the message as the target's first
   turn. There is no separate activation turn.
4. The chat shows a "Switched from X to Y" divider above that message.

Context handed over: git, PR, and CI facts, the original task, and a bounded
tail of the conversation. Claude Code and Codex also get a pointer to their own
provider transcript; every other agent gets AO's stored transcript.

## Where the time goes

Each switch logs `agent switch timing` with one duration per step, and each ACP
start logs `chat: ACP start stage` for `process_open`, `initialize`, and
`session_new`. Measured on one macOS machine, October 2026, against a headless
daemon (`ao daemon` on its own port and data dir), each switch carrying a
message:

| Switch | Switch total | AO's own steps | Target start |
| --- | --- | --- | --- |
| Claude Code to Codex | 0.94s | ~0.2s preflight, ~50ms rest | 0.48s |
| Codex to Claude Code | 1.69s | ~50ms | 1.60s (native resume) |
| opencode to Codex | 0.55s | ~0.1s | 0.41s |
| Claude Code to opencode, before the fix below | 8.42s | ~0.3s | 8.07s |
| Codex to opencode, after the fix below | 2.00s | ~0.4s | 1.62s |
| Codex to Cursor | 10.36s | ~1.1s, mostly preflight | 9.21s |

AO's own work (database writes, stopping the source, building the handoff) is
25 to 180ms. Stopping the source agent is 20 to 160ms. The rest is the target
agent starting, plus the target preflight when its readiness is not cached
(up to 0.9s).

## Agent startup, measured raw

Timed by speaking ACP over stdio directly, with nothing from AO in the way.
"Second session" is a `session/new` on the same, already running process.

| Agent | `initialize` | First `session/new` | Second session |
| --- | --- | --- | --- |
| opencode 1.18.35 | 0.4 to 0.8s | 0.7 to 0.9s | 0.00s |
| Cursor (`cursor-agent acp`) | 0.25 to 0.3s | 5.4 to 10.7s, usually ~6s | ~2.0s |
| Claude Code (through AO) | 0.09 to 0.16s | ~0.8s | not measured |

## opencode: the per-worktree install (fixed)

opencode npm-installs `@opencode-ai/plugin` into every `.opencode` directory it
loads (`packages/opencode/src/config/config.ts`). AO writes its activity plugin
and the using-ao skill into each worktree's `.opencode/`, so the first opencode
start in every new worktree ran that install: 4 to 7s inside `initialize`
(2.65s raw with a warm npm cache, 4.4 to 9.7s through AO). An agent switch to
opencode nearly always lands in such a worktree.

opencode skips the install when `node_modules` exists and `package-lock.json`
names every declared package (`packages/core/src/npm.ts`, names only, not
versions). AO's plugin imports the package only as a type, which Bun erases, so
nothing needs installing. `GetAgentHooks` therefore seeds an empty
`.opencode/node_modules` and a stub `package-lock.json`, but only when
`.opencode` holds nothing except AO's files. A repository with its own opencode
plugins keeps the real install. Result: `initialize` 0.44s, and a switch to
opencode in a fresh worktree takes 2.0s instead of 8.4s. The plugin still fires
(verified with a stub `ao` on `PATH` recording `ao hooks opencode
session-start`).

## Cursor: slow session creation

Cursor's `session/new` takes about 6s on a fresh process and about 2s on a warm
one, measured raw. That time is on Cursor's side. Through AO it took 9s; the
extra ~3s is not yet attributed (the standing-rules plugin directory and AO's
MCP server are the candidates).

- `session/load` is no faster: ~2s warm, 5.5 to 6.8s fresh. A session with no
  turns cannot be loaded at all ("Session not found").
- `session/set_config_option` for the model takes 2 to 3s and rewrites the
  CLI's global default model in `~/.cursor/cli-config.json`, which changes
  every other Cursor session. Do not change the model at switch time.
- `@cursor/sdk` runs the same agent loop in a Node process; inference still
  goes to Cursor. It needs its own API-key sign-in rather than the CLI login,
  and AO's backend is Go, so adopting it means a Node sidecar. Its
  `prewarmLocalWorkspace()` documents workspace resolution as the slowest part
  of a first turn. SDK timings are not measured yet.

## Making every switch instant

Ranked by payoff. None of these is built yet.

1. **Warm agent processes.** Keep one initialized ACP process per agent and
   open sessions on it. opencode's second session costs 0.00s; Cursor's ~2s.
2. **Start the target session before the send.** Open it when the model menu
   opens or the agent is picked, held as a standby that does not own the
   session until the switch commits. This needs the handoff moved from the
   system prompt to the first message (so a standby can start without it), a
   standby host identity separate from the session's, and a pending browser
   credential that becomes the session's only at commit. For Cursor this is the
   only way under one second.
3. **Cache the target preflight** at pick time (up to 0.9s today).
4. **Keep the source agent parked** after a switch, so switching back is a
   swap plus the messages it missed.

## Reproducing

Scripts used for these numbers (keep them out of the repo):

- Raw ACP timing: spawn the agent, send `initialize`, then two `session/new`
  calls with `mcpServers: []`, timing each.
- Switch chain: start a standalone chat, plant a fact, then switch through
  agents with a message asking for it, polling
  `/sessions/{id}/agent-switches` and `/sessions/{id}/conversation`.
- Read `agent switch timing` and `chat: ACP start stage` from the daemon log.
