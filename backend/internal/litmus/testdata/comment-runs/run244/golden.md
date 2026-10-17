**Result: something broke; do not merge yet.**

Bug 1 of 1:

With General > Default session interface set to Terminal, /mods lists 7 built-in mods and no user mod. Command Code v1.84.0.

![main on the left and the PR build on the right](bug-1.png)

Check-end screenshots in light theme.

<details><summary>Recording for User mods remain available</summary>

[Watch the check](../clips/c3-pr.mp4)

</details>

- Standing instructions in Chat: partial (main does part of this, environment: provider request limit). Chat returned 'Implementation worker, CEDAR-6778' without tools on DeepSeek V4 Flash. Environment: Initial model is not in the account plan.
- Standing instructions in Terminal: partial (not tested on main). Terminal returned 'implementation worker, CEDAR-6778' on DeepSeek V4 Flash after /model deepseek-v4-flash. Environment: Initial model is not in the account plan.
- User mods remain available: fail (works on main). See Bug 1.

Not tested: Chat survives mode switch and restore (ran out of time; main: not compared)

<details><summary>Full check notes</summary>

- Standing instructions in Chat. Expected: Command Code Chat loads AO role instructions alongside repo instructions without adding them to the conversation.

  The PR build: partial. Chat returned 'Implementation worker, CEDAR-6778' without tools on DeepSeek V4 Flash. GPT-5.4 Mini first failed with MODEL_NOT_IN_PLAN, then I changed models and retried. Repo AGENTS.md loaded alongside the AO role. System-prompt placement and compaction survival remain unverified. Local-folder selection was unreachable; clone attempts inside scratch hit AO's state-directory safety rule. Cloning the private fixture to the app's normal projects location worked. Environment: Initial model is not in the account plan.

  Main: partial. Cloned the same private fixture and selected DeepSeek V4 Flash in Chat. The role-plus-word request completed with no visible answer. A shorter follow-up failed with TokenRouter HTTP 429, maximum 5 requests per minute. The initial zai turn was interrupted to change models. System-prompt placement and compaction survival remain unverified. Environment: Provider request limit.

- Standing instructions in Terminal. Expected: Command Code Terminal loads AO role instructions alongside repo instructions.

  The PR build: partial. Terminal returned 'implementation worker, CEDAR-6778' on DeepSeek V4 Flash after /model deepseek-v4-flash. The initial GPT-5.4 Mini turn failed with MODEL_NOT_IN_PLAN. /memory also offered project and user memory. I set the default interface to Terminal. Earlier paste attempts hit a hidden terminal; I corrected the target to the visible terminal. Environment: Initial model is not in the account plan.

  Main: skipped. ran out of time

- User mods remain available. Expected: Command Code lists the same installed user mods on head and base. Risk: Redirected HOME could hide installed user mods

  The PR build: fail. With General > Default session interface set to Terminal, /mods lists 7 built-in mods and no user mod. Command Code v1.84.0. Native keypresses reached the terminal after insert-text left only the hidden textarea changed.

  Main: pass. Set General > Default session interface to Terminal. Command Code v1.84.0 prints tokenrouter-provider startup and /mods lists 8, including tokenrouter-provider as a user mod. Head lists 7 built-ins and omits both.

- Chat survives mode switch and restore. Expected: A Command Code conversation keeps its prior answer after switching modes and restoring the session. Risk: Per-session homes could break conversation restore

  The PR build: skipped. ran out of time

</details>

<!-- litmus:comment -->