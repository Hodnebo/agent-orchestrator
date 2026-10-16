# ADR 0006: desktop Stay Awake ownership

Status: implementation in progress; physical acceptance and privileged power-control decision pending.

## Intent and controls

Settings contains a dedicated Stay Awake page using the current grouped settings
layout. Its controls create indefinite, timed, end-at-time, running-app, or typed
AO-browser-download requests. The Summary inspector offers a quick indefinite
request for its session or for AO. Session and board toolbars offer an AO-wide
request. Info controls open the Stay Awake settings page. Mobile access is
not required. Turning off one scope never withdraws another scope's request or
Connect Mobile's lease.

Detailed native observations run only for active policy or while the Stay Awake settings page
requests them. An unused feature does not launch observation processes. The
daemon owns requests, settings and rules in a versioned singleton SQLite
aggregate. Its `/api/v1/awake` routes are loopback-only and blocked from LAN and
managed mobile tunnels, including with valid mobile authentication. Electron
only feeds typed download state; it sends IDs, never transfer URLs or filenames.
The renderer displays observations but does not decide when native ownership
expires or restore system state.

## Lifetimes and observations

Manual requests end after normal daemon shutdown. Startup durably marks
previous manual requests interrupted; it never resurrects an indefinite request.
Finite deadlines use Go's monotonic clock after creation and a native monotonic
remaining deadline after first observation. A request bound to a session,
application PID/start identity, download, or automatic rule carries a native
maximum deadline fifteen seconds after its latest successful policy check.
This prevents the independent lease-renewal goroutine extending stale bindings
if the policy loop stalls. Unchanged native sequences still expire after sixty
seconds, as in ADR 0005.

Application exit and PID reuse end an app request. Failed probes provide neither
positive ownership nor proof of termination. Download heartbeats expire after
twenty seconds; losing the heartbeat is recorded separately from completion.
Rules support all/any composition across agent activity, schedules with IANA
timezones, power source, battery level, network CIDR, connected volumes,
external display count, running applications, uptime and AO browser downloads.
Activation requires matching observations spanning five seconds. Unknown or
stale observations immediately withdraw automatic ownership. Active battery
conditions have three percentage points of hysteresis. Operator pause and
critical safety stops require explicit resumption; a manual request does not
silently resume paused rules.

## Native protocol and recovery

Protocol 3 extends the existing unprivileged launchd coordinator rather than
installing a second owner of the global clamshell control. Each desktop daemon
has a lease separate from its mobile lease. Idle and display wake use
process-owned IOPM assertions. Closed-lid ownership is independent of optional
audio and app effects; legacy mobile leases omit the new flag and retain their
closed-lid meaning. A coordinator watchdog terminates a stalled coordinator so
launchd can replay its already-fsynced journals. Scratch/custom data directories
cannot join or install this coordinator.

A protocol-2 registration is not silently replaced. Open the lid, disable
closed-lid requests in every AO instance, wait for recovery, and run:

```sh
launchctl bootout gui/$(id -u)/com.aoagents.mobile-wake
```

Then enable the requested feature again. The same exact procedure is included
in upgrade errors. Preserve unfinished recovery journals.

Below the configured battery threshold (minimum 10%), unknown native battery
state, or critical thermal state, the helper latches affected leases and releases
ownership. The daemon persists the stop and requires fresh safe observations
plus explicit Resume. Normal display sleep and lock-screen authentication
remain macOS responsibilities; this feature does not unlock an authenticated
screen.

Optional closed-lid audio muting and selected background-app pausing use a
separate private recovery journal. Audio ownership records the device UID and
original mute value. Restore never targets a different default output and
respects detectable user changes. Pausing records same-user PID/start identities
before SIGSTOP, then acknowledges successful application. AO, agents, terminals,
IDEs, supported browsers, foreground apps and recording activity are protected.
App groups with protected descendants are skipped. Groups and journals are
bounded. Prepared but unacknowledged pauses after a crash require the explicit
recovery confirmation before SIGCONT. Already stopped apps are not adopted.
Expired and abandoned effects restore before releasing owned lid control.
Unrecoverable or unreadable state is preserved and reported, never erased as
though cleanup succeeded.

Hotspot recovery is opt-in and limited to saved macOS Wi-Fi profiles. Credentials
remain with macOS. Only a known disconnect lasting ten seconds permits an
attempt; attempts have a thirty-second cooldown and a three-attempt budget per
outage. Stopping wake, disabling recovery, another working network, or user
network changes cancel handoff. Outcomes are durable; an unconfirmed association
is never reported as success.

Browser relaunch is opt-in and separately confirmed because it closes windows.
Only installed Chrome and Brave bundles satisfying their expected Apple code
signing/team requirements are supported. AO/agent-managed browser processes are
excluded; a browser that refuses graceful termination is not force-quit.
Requests journal intent before relaunch. Replaying an uncertain request ID cannot
close the browser again automatically. Tab recovery belongs to the browser.

## Settings surface and unfinished capabilities

The current settings page exposes session modes, uptime, lid/display defaults,
automatic activation, and battery/heat protection. Optional display/keyboard/audio
effects, app pausing, hotspot recovery, agent reconnection, browser relaunch,
and explicit recovery/history operations have backend/native contracts but no
settings controls in this version. Their presence in the API is not a claim that
the desktop settings expose the complete original PR2 feature list.

## Unfinished capabilities and acceptance

Automatic Low Power Mode needs a product decision about a macOS-authorized
privileged power helper versus opening macOS settings. No root installation or
sudoers changes are part of this implementation so far. Display-brightness and keyboard-backlight controls use runtime-gated private
APIs only on macOS 26–27; older supported macOS versions retain core wake/lid
support. Keyboard selectors must match their measured ABI. Missing symbols,
unsupported hardware and missing fresh open-lid brightness samples prevent
mutations. A journal records original brightness, display UUID/keyboard identity
and boot UUID before dimming. Reopening restores only same-identity AO-owned zero
values; detectable user changes are preserved. Reboot identity mismatches remain
explicit recovery warnings. The private bridge declarations are informed by
[Insomnia's source](https://github.com/krishhgg/Insomnia/blob/main/Sources/Insomnia/System/DisplayPower.swift),
with its MIT notice included beside the helper. These paths still need physical
validation.

Guarded agent reconnection is limited to a positively observed transport loss
from an AO-managed Chat controller. Explicit close, termination, hibernation,
agent/interface switching and changed controller generations exclude recovery.
With the option enabled, an active awake request and a known recovered network
permit one durable attempt per interrupted generation. The session manager
rechecks durable ownership under its normal mutation gate. The Chat controller
gate consumes the original transport-loss evidence before provider I/O; explicit
stop invalidates it. Recovery uses only
`ReconnectOnly`; it never restores a worktree, starts a replacement provider or
manufactures a new prompt. Provider adapters without a reconnect-only contract
fail explicitly. Uncertain attempts are not automatically replayed. Terminal
agents and independently managed CLI conversations are not restarted.

Automatic Low Power Mode still reports unavailable with a reason. It is not an
implemented capability and must not be advertised as completed.

Automated tests and compilation do not establish physical MacBook behavior.
The ADR 0005 matrix remains a release gate: AC/battery and charger transitions,
closed-lid panel darkness and screen lock, overlapping owners, daemon/helper
SIGKILL, helper stall recovery, login, updates and shutdown. Optional pause,
audio, network and browser actions need their own device tests, including
user changes, recording, crashes between journal and mutation, and failed
restoration. Do not infer macOS 13/14/15/27 device coverage from compilation.
