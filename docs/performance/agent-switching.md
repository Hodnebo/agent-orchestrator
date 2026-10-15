# Agent switching performance

Switching a chat to another agent should feel instant and lose nothing. This
page counts the time from send to the target agent running the user's message.
The model's own time to first token comes after that and is not counted.

## How a switch runs

1. Picking an agent in the model menu records the choice in the renderer and
   prewarms the target's readiness check (`POST
   /agents/{agent}/chat/prewarm`). No session is created.
2. The next send carries the switch: `POST /sessions/{id}/switch-agent` with the
   message, model, and reasoning effort. The message shows at once and reads as
   Working from the send. Messages sent in the meantime wait in the queue dock.
3. The daemon stops the source agent and starts the target fresh. It hands
   context over in the target's system prompt and delivers the message as the
   target's first turn. There is no separate activation turn.
4. The chat shows a "Switched from X to Y" divider above that message.

Every chat agent with a Chat driver can be a source or a target: Claude Code,
Codex, opencode, Droid, Cursor, Grok, fx, pi, and omp. TUI sessions still
switch only between Claude Code, Codex, and fx.

## What the target is told

The handoff carries the git, PR, and CI facts, the original task, and AO's own
transcript of the chat. The transcript holds every message plus a compact line
for each action: commands with their exit code (and a failed command's output
tail), file changes, MCP tool calls, plans, errors, approvals, and questions.
It fills a 96 KiB budget from the newest end and shrinks if the prompt would
overflow. The provider's native transcript path is added as a pointer when one
exists.

The target always starts a new native session, even when switching back to an
agent that ran earlier in the chat. Resumed Claude Code and Codex sessions
ignore a new system prompt, so they would miss whatever happened while they
were away. Cursor ignores the system prompt entirely, so its handoff goes in
as a text block ahead of the first message.

The chat's approval mode carries over to the target.

## Results

Measured on one macOS machine in October 2026 against a headless daemon on its
own port and data dir. Each run starts a fresh chat on the source agent and
plants context over two turns: fact A plus a real shell command, then fact B.
It then switches with a five-part quiz the target must answer from the handoff
alone: both facts, the exact command, the file it created, and the first word
of the first message.

"Ready" is send to the target running the user's message. On main a switch
first runs an activation turn and the message is sent after it, so main's
number covers both.

| Switch | main | This branch | Speedup | Quiz (main / branch) |
| --- | --- | --- | --- | --- |
| Claude Code to Codex | 11.57s | 0.36s | 32× | 5/5 / 5/5 |
| Codex to Claude Code | 8.85s | 2.03s | 4.4× | 5/5 / 5/5 |
| Claude Code to pi | refused | 0.76s | new | n/a / 5/5 |
| Claude Code to Grok | refused | 0.70s | new | n/a / 5/5 |
| Claude Code to omp | refused | 1.09s | new | n/a / 5/5 |
| Claude Code to fx | refused | 1.61s | new | n/a / 5/5 |
| Claude Code to opencode | refused | 1.72s | new | n/a / 5/5 |
| Claude Code to Droid | refused | 2.6 to 3.3s | new | n/a / 5/5 |
| Claude Code to Cursor | refused | 5.7 to 10.5s, usually ~6s | new | n/a / 5/5 |

Main refuses every chat switch outside Claude Code and Codex with
`UNSUPPORTED_SWITCH_HARNESS`. pi's first ever start on a machine took 4.8s.

A chain through every agent, Claude Code to Codex, opencode, Droid, Grok, fx,
omp, pi, Cursor, and back to Claude Code, telling each one a new fact, recalled
all 9 facts at the end in each of three runs.

## Where the time goes

Each switch logs `agent switch timing` with one duration per step. Each ACP
start logs `chat: ACP start stage` for `process_open`, `initialize`, and
`session_new`.

AO's own steps (database writes, the transcript excerpt, publishing the handoff)
take 10 to 30ms in total. The rest is stopping the source and starting the
target.

### Stopping the source

The host closes the provider's stdin and waits 300ms. Then it sends SIGTERM to
the provider's process group and kills it at 3s. Cursor and Grok ignore stdin
closing, so before the SIGTERM step every switch away from them waited out the
full 3s. Cursor now stops in 0.34s. Grok stops in 1.6s, because it answers
`session/close` slowly; AO now waits at most 500ms for that answer. Droid stops
in about 1.1s. Claude Code, Codex, and opencode stop in under 0.2s.

### Starting the target

| Agent | Start through AO |
| --- | --- |
| Codex | ~0.3s |
| pi, Grok | 0.5 to 0.8s |
| omp, fx, opencode | 1.0 to 1.7s |
| Claude Code | ~1.8s |
| Droid | 2 to 3s |
| Cursor | 5.5 to 10s |

## opencode: the per-worktree install (fixed)

opencode npm-installs `@opencode-ai/plugin` into every `.opencode` directory it
loads (`packages/opencode/src/config/config.ts`). AO writes its activity plugin
and the using-ao skill into each worktree's `.opencode/`. So the first opencode
start in every new worktree ran that install, which took 4 to 7s inside
`initialize`. An agent switch to opencode nearly always lands in such a
worktree.

opencode skips the install when `node_modules` exists and `package-lock.json`
names every declared package (`packages/core/src/npm.ts`; it checks names, not
versions). AO's plugin imports the package only as a type, which Bun erases, so
nothing needs installing. `GetAgentHooks` therefore seeds an empty
`.opencode/node_modules` and a stub `package-lock.json`, but only when
`.opencode` holds nothing except AO's files. A repository with its own opencode
plugins keeps the real install. With the stubs, a switch to opencode in a fresh
worktree takes 1.7s instead of 8.4s. The plugin still fires.

opencode v2 also copied its downloaded tool cache (`bin/`) into every new data
home. That copy is now skipped.

## Cursor: slow session creation

Cursor's `session/new` takes 5.5 to 6s on a fresh process and about 2s on a
warm one, measured raw over ACP. That time is spent on Cursor's servers. None of
these changed it:

- CLI flags, a clean `cli-config.json`, or removing MCP servers and skills.
  AO's MCP server adds 0.3 to 0.5s.
- `session/load`, which is no faster. A session with no turns cannot be loaded
  at all ("Session not found").
- An idle, already initialized process. Only the second `session/new` on a
  process is fast.
- `session/set_config_option` for the model takes 2 to 3s and rewrites the
  CLI's global default model in `~/.cursor/cli-config.json`, which changes
  every other Cursor session. Do not change the model at switch time.

The only way to get Cursor under about 4s is to create its session before the
send: a standby session opened on pick or menu open, handed to the chat when
the switch commits. That is not built. It costs a Cursor session for every
pick that is not followed by a send.

## Reproducing

The scripts for these numbers are kept out of the repo.

- Switch bench: start a standalone chat on the source, plant the two facts,
  then `POST /sessions/{id}/switch-agent` with the quiz as the message. Poll
  `/sessions/{id}/agent-switches` and `/sessions/{id}/conversation` until the
  quiz turn runs and completes.
- For main, switch without a message, wait for the switch to complete, then
  send the quiz.
- Raw ACP timing: spawn the agent, send `initialize`, then two `session/new`
  calls with `mcpServers: []`, timing each.
- Read `agent switch timing` and `chat: ACP start stage` from the daemon log.
