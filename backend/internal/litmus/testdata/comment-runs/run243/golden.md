**Result: something broke; do not merge yet.**

Bug 1 of 1:

On macOS, Cmd+Shift+O does nothing on the Sample project 243 board, even with an idle orchestrator. Clicking the topbar Orchestrator button opens it.

![main on the left and the PR build on the right](bug-1.png)

Check-end screenshots in light theme.

<details><summary>Recording for Project board shortcut opens orchestrator</summary>

[Watch the check](../clips/c11-pr.mp4)

</details>

Passed: Default shortcut opens running orchestrator (not on main); Shortcut customization (not on main); Settings and root board guard (not on main); Resume exited orchestrator (not on main); Localized shortcut label (not on main)

- Project board shortcut opens orchestrator: fail (not on main). See Bug 1.

Not tested: Spawn orchestrator once (Could not reach a configured project without an orchestrator; not on main); No configured agent opens settings (The sample project was configured during the running-orchestrator setup; not on main); Shortcut with native Browser focus (Opened the worker inspector Browser and loaded example.com in its native view; not on main); Windows and Linux default binding (Both builds run on macOS; not tested on main); Cloud orchestrator shortcut (The private test window shows Sign in to AO Cloud; not tested on main)

<details><summary>Full check notes</summary>

- Default shortcut opens running orchestrator. Expected: With a worker terminal focused, Cmd+Shift+O opens the project orchestrator once.

  The PR build: pass. Configured Codex with GPT-6-Luna in project Agents settings. From Sample chat 243, opened Terminal 1, focused Terminal input, and pressed Cmd+Shift+O. It opened the existing idle orchestrator. Setup had stale-ref clicks, not shortcut failures.

  Main: absent. Cmd+Shift+O leaves Sample chat 243 on Terminal 1. Base has the Orchestrator button but no Open orchestrator shortcut command. Its sample project has no configured orchestrator; no running-orchestrator comparison was possible without setup.

- Shortcut customization. Expected: Open orchestrator is listed with Cmd+Shift+O and a saved custom chord opens it.

  The PR build: pass. Settings > Shortcuts lists Open orchestrator as Cmd+Shift+O. Changed it to Cmd+Alt+O, reopened Settings and saw the saved binding, then used it from Sample chat 243 to open the existing orchestrator. Reset restored Cmd+Shift+O. One stale-ref worker click failed during setup and was corrected.

  Main: absent. Searching orchestrator in Settings > Shortcuts shows No matching commands. Base cannot customize this command.

- Settings and root board guard. Expected: Cmd+Shift+O leaves settings and the root board unchanged.

  The PR build: pass. Opened global Settings over the worker terminal and pressed Cmd+Shift+O. Settings stayed open and Back returned to the same worker. Navigated to root and pressed it again; the welcome board stayed unchanged.

  Main: absent. Cmd+Shift+O leaves global Settings unchanged. Navigating to root kept the Settings overlay open. Base has no Open orchestrator command, so these guards are not independently exercised there.

- Spawn orchestrator once. Expected: A double press in a configured project without an orchestrator creates and opens only one.

  The PR build: skipped. Could not reach a configured project without an orchestrator. Sample setup auto-opened one after configuration. Folder creation opened an undrivable native chooser. Clone showed Could not clone repository; retry did not advance. Private test repo Rishet11/litmus-pr6854-ao243 remains: deletion failed with HTTP 403, missing delete_repo scope. Spawn and double-press deduplication remain untested.

  Main: absent. Base has no Open orchestrator shortcut. Double pressing Cmd+Shift+O cannot test the new spawn command there.

- Resume exited orchestrator. Expected: Cmd+Shift+O reopens the same exited orchestrator.

  The PR build: pass. Switched the orchestrator to terminal UI, exited it with /exit and Ctrl+D, and observed Exited plus Resume agent. From the worker, Cmd+Shift+O reopened it as Idle at the same session ID, litmus-head-run_ao-243-2. An early /exit was typed during startup; the exited state was checked before testing resume.

  Main: absent. Cmd+Shift+O leaves the base project board unchanged. Base lacks the shortcut command, so there is no shortcut resume path to compare.

- No configured agent opens settings. Expected: Cmd+Shift+O opens project settings when its orchestrator agent is unset.

  The PR build: skipped. The sample project was configured during the running-orchestrator setup. Tried creating another project with Open a local folder, but it opened a native chooser that ao testing could not drive; Escape did not dismiss it. Reload recovered the renderer. No unconfigured head project remains.

  Main: absent. Settings > Shortcuts has no matching command for orchestrator on base. The new shortcut path for an unset agent is absent.

- Project board shortcut opens orchestrator. Expected: Cmd+Shift+O on the project board opens its existing orchestrator, as the visible Orchestrator button does.

  The PR build: fail. On macOS, Cmd+Shift+O does nothing on the Sample project 243 board, even with an idle orchestrator. Clicking the topbar Orchestrator button opens it. The same default chord works from the worker session. Repeated on the board after reload with the same result. The shortcut is wired only in ShellTopbar, while the macOS board renders SessionsBoard actions instead.

  Main: absent. On the base project board, Cmd+Shift+O also does nothing. Base lacks the new shortcut command entirely; this is not an existing broken implementation of it.

- Shortcut with native Browser focus. Expected: From a worker Browser view, Cmd+Shift+O opens the project orchestrator.

  The PR build: skipped. Opened the worker inspector Browser and loaded example.com in its native view. ao testing lists it as t2 but refuses switching away from the app target, so I could not send the chord to the focused native page. A renderer-target press opened the orchestrator but does not prove native Browser forwarding.

  Main: absent. Base has no Open orchestrator shortcut command. Native Browser shortcut forwarding for this command is absent.

- Localized shortcut label. Expected: In German Settings > Shortcuts, the command is shown as Orchestrator öffnen with Cmd+Shift+O.

  The PR build: pass. Changed Language to Deutsch. Settings > Tastenkürzel shows Orchestrator öffnen with Cmd+Shift+O, and its change/add controls use the German label. German was the sampled non-English locale; the other six translations were not driven.

  Main: absent. Changed Language to Deutsch and searched Orchestrator under Tastenkürzel. Base shows no matching commands.

- Windows and Linux default binding. Expected: Ctrl+Shift+G opens the in-scope orchestrator on Windows or Linux.

  The PR build: skipped. Both builds run on macOS. Windows/Linux Ctrl+Shift+G and its Open in editor conflict avoidance were not tested on their native platforms.

  Main: skipped. The base window also runs on macOS; Windows and Linux input behavior cannot be checked here.

- Cloud orchestrator shortcut. Expected: The shortcut creates an orchestrator for an AO Cloud project.

  The PR build: skipped. The private test window shows Sign in to AO Cloud. Cloud project creation requires login, so the cloud spawn path was not tested.

  Main: skipped. The base window is not signed in to AO Cloud either. Cloud project creation was not attempted.

</details>

<!-- litmus:comment -->