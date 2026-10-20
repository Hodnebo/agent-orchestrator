import { focusManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render as renderView, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { awakeQueryKey, type AwakeState } from "../hooks/useAwake";
import { useStayAwake } from "../hooks/useStayAwake";
import { mobileStatusQueryKey } from "../lib/mobile-status-key";
import { AwakeControl } from "./AwakeControl";
import { TooltipProvider } from "./ui/tooltip";

function render(ui: React.ReactNode) {
	return renderView(<TooltipProvider delayDuration={0}>{ui}</TooltipProvider>);
}

const test = vi.hoisted(() => ({ mac: true, get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/platform", () => ({ isMacPlatform: () => test.mac }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: test.get, POST: test.post },
	apiErrorMessage: (e: unknown) => String(e),
}));
let client: QueryClient;
let state: AwakeState;
let mobile: { enabled: boolean; keepAwake: { supported: boolean; enabled: boolean; active: boolean; lidClosed: boolean } };
let release: (() => void) | undefined;
let blockStart: boolean;
function snapshot() { return structuredClone(state); }
function awakeSession(overrides: Partial<AwakeState["sessions"][number]>) {
	return { id: "awake", scope: "ao" as const, sessionId: "", mode: "indefinite" as const,
		boundId: "", boundIdentity: "", createdAt: "2026-10-19T00:00:00Z", endReason: "", label: "For AO", lidClosed: false, ...overrides };
}
beforeEach(() => {
	test.mac = true;
	release = undefined;
	blockStart = false;
	client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
	state = { settings: { lidClosed: true }, sessions: [], rules: [], activeRules: [], active: false, paused: false, capabilities: [{ id: "idle", available: true }] } as unknown as AwakeState;
	mobile = { enabled: false, keepAwake: { supported: true, enabled: false, active: false, lidClosed: false } };
	test.get.mockReset().mockImplementation(async (path) => ({ data: path === "/api/v1/awake" ? snapshot() : structuredClone(mobile) }));
	test.post.mockReset().mockImplementation(async (path, options) => {
		if (path === "/api/v1/mobile/keep-awake") {
			Object.assign(mobile.keepAwake, options.body);
			if (!options.body.enabled) mobile.keepAwake.active = false;
			return { data: structuredClone(mobile) };
		}
		if (path === "/api/v1/awake/sessions") {
			if (blockStart) await new Promise<void>((resolve) => { release = resolve; });
			state.sessions.push(awakeSession({ id: "new", ...options.body }));
			state.active = true;
			state.lidActive = options.body.lidClosed;
		} else if (path === "/api/v1/awake/stop") {
			state.sessions = state.sessions.map((s) => ({ ...s, endedAt: "now" }));
			state.paused = true;
			state.active = false;
			state.lidActive = false;
			state.activeRules = [];
		} else if (path === "/api/v1/awake/sessions/{awakeId}/end") {
			state.sessions = state.sessions.map((s) => s.id === options.params.path.awakeId ? { ...s, endedAt: "now" } : s);
			const live = state.sessions.filter((s) => !s.endedAt);
			state.active = live.length > 0;
			state.lidActive = live.some((s) => s.lidClosed);
		} else if (path === "/api/v1/awake/resume") state.paused = false;
		return { data: snapshot() };
	});
});
function MobileControl() {
	const stay = useStayAwake();
	return <>
		<button disabled={stay.busy || !stay.supported} onClick={() => stay.set(!stay.on, true)} aria-pressed={stay.on}>Mobile wake</button>
		<button disabled={stay.busy || !stay.supported} onClick={() => stay.setLid(!stay.lidOn)} aria-pressed={stay.lidOn}>Lid wake</button>
	</>;
}
async function show() {
	render(<QueryClientProvider client={client}><AwakeControl /><MobileControl /></QueryClientProvider>);
	await waitFor(() => expect(screen.getByRole("button", { name: "Mobile wake" })).toBeEnabled());
}
function coffee() { return screen.getByRole("button", { name: "Stay Awake" }); }

describe("shared Stay Awake controls", () => {
	it("renders no coffee button off macOS", () => {
		test.mac = false;
		render(<QueryClientProvider client={client}><AwakeControl /></QueryClientProvider>);
		expect(screen.queryByTestId("awake-button")).toBeNull();
	});
	it.each([false, true])("does not poll mobile status in steady state (error=%s)", async (failing) => {
		vi.useFakeTimers();
		focusManager.setFocused(true);
		test.get.mockImplementation(async (path) => path === "/api/v1/awake"
			? { data: snapshot() }
			: failing ? { error: "mobile unavailable" } : { data: structuredClone(mobile) });
		const view = render(<QueryClientProvider client={client}><AwakeControl /></QueryClientProvider>);
		try {
			await act(async () => { await vi.advanceTimersByTimeAsync(0); });
			const mobileReads = () => test.get.mock.calls.filter(([path]) => path === "/api/v1/mobile/status").length;
			expect(mobileReads()).toBe(1);
			await act(async () => { await vi.advanceTimersByTimeAsync(120_000); });
			expect(mobileReads()).toBe(1);
		} finally {
			view.unmount();
			client.clear();
			vi.useRealTimers();
			focusManager.setFocused(undefined);
		}
	});
	it("refreshes mobile status on focus only after its freshness window expires", async () => {
		vi.useFakeTimers();
		focusManager.setFocused(false);
		client.setDefaultOptions({ queries: { retry: false, refetchOnWindowFocus: false } });
		const view = render(<QueryClientProvider client={client}><AwakeControl /></QueryClientProvider>);
		try {
			await act(async () => { await vi.advanceTimersByTimeAsync(0); });
			const mobileReads = () => test.get.mock.calls.filter(([path]) => path === "/api/v1/mobile/status").length;
			expect(mobileReads()).toBe(1);
			await act(async () => { focusManager.setFocused(true); await vi.advanceTimersByTimeAsync(0); });
			expect(mobileReads()).toBe(1);
			focusManager.setFocused(false);
			await act(async () => { await vi.advanceTimersByTimeAsync(60_001); });
			expect(mobileReads()).toBe(1);
			await act(async () => { focusManager.setFocused(true); await vi.advanceTimersByTimeAsync(0); });
			expect(mobileReads()).toBe(2);
		} finally {
			view.unmount();
			client.clear();
			vi.useRealTimers();
			focusManager.setFocused(undefined);
		}
	});

	it("allows desktop wake while the mobile startup request is still pending", async () => {
		let finishMobile: (() => void) | undefined;
		test.get.mockImplementation(async (path) => path === "/api/v1/awake" ? { data: snapshot() }
			: await new Promise((resolve) => { finishMobile = () => resolve({ data: structuredClone(mobile) }); }));
		render(<QueryClientProvider client={client}><AwakeControl /></QueryClientProvider>);
		await waitFor(() => expect(coffee()).toBeEnabled());
		expect(client.getQueryData(mobileStatusQueryKey)).toBeUndefined();
		fireEvent.click(coffee());
		await waitFor(() => expect(state.active).toBe(true));
		await act(async () => finishMobile!());
	});
	it("allows desktop wake when mobile status fails", async () => {
		test.get.mockImplementation(async (path) => path === "/api/v1/awake" ? { data: snapshot() } : { error: "mobile unavailable" });
		render(<QueryClientProvider client={client}><AwakeControl /></QueryClientProvider>);
		await waitFor(() => expect(client.getQueryState(mobileStatusQueryKey)?.status).toBe("error"));
		expect(coffee()).toBeEnabled();
		fireEvent.click(coffee());
		await waitFor(() => expect(state.active).toBe(true));
	});

	it.each([-1, 0])("forwards tabIndex %i to the coffee button and respects keyboard navigation", async (tabIndex) => {
		const user = userEvent.setup();
		render(<QueryClientProvider client={client}>
			<button>Before coffee</button><AwakeControl tabIndex={tabIndex} /><button>After coffee</button>
		</QueryClientProvider>);
		await waitFor(() => expect(coffee()).toBeEnabled());
		expect(coffee()).toHaveAttribute("tabindex", String(tabIndex));
		await user.click(screen.getByRole("button", { name: "Before coffee" }));
		await user.tab();
		expect(tabIndex === -1 ? screen.getByRole("button", { name: "After coffee" }) : coffee()).toHaveFocus();
		expect(test.post).not.toHaveBeenCalled();
	});

	it("falls back to idle-only wake when closed-lid capability is unavailable", async () => {
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(test.post).toHaveBeenCalledWith("/api/v1/awake/sessions", { body: expect.objectContaining({ scope: "ao", mode: "indefinite", lidClosed: false }) }));
		await waitFor(() => expect(coffee()).toHaveAttribute("aria-pressed", "true"));
		expect(screen.getByRole("button", { name: "Lid wake" })).toHaveAttribute("aria-pressed", "false");
	});
	it("starts desktop closed-lid wake without mobile access, synchronizes both switches, and stops it", async () => {
		state.capabilities.push({ id: "lid", available: true, reason: "" });
		await show();
		expect(coffee()).toHaveAccessibleDescription(/including with the lid closed.*below 10% battery.*critical heat/);
		fireEvent.click(coffee());
		await waitFor(() => expect(coffee()).toBeEnabled());
		expect(test.post).toHaveBeenCalledWith("/api/v1/awake/sessions", { body: expect.objectContaining({ scope: "ao", mode: "indefinite", lidClosed: true }) });
		await waitFor(() => expect(coffee()).toHaveAccessibleDescription(/being kept awake, including with the lid closed/));
		expect(screen.getByRole("button", { name: "Lid wake" })).toHaveAttribute("aria-pressed", "true");
		expect(mobile.enabled).toBe(false);
		expect(mobile.keepAwake.enabled).toBe(false);
		expect(test.post).not.toHaveBeenCalledWith("/api/v1/mobile/keep-awake", expect.anything());
		fireEvent.click(coffee());
		await waitFor(() => expect(coffee()).toHaveAttribute("aria-pressed", "false"));
		expect(state.lidActive).toBe(false);
		expect(screen.getByRole("button", { name: "Lid wake" })).toHaveAttribute("aria-pressed", "false");
	});
	it("keeps the mobile main toggle idle-only even on a lid-capable Mac", async () => {
		state.capabilities.push({ id: "lid", available: true, reason: "" });
		await show();
		fireEvent.click(screen.getByRole("button", { name: "Mobile wake" }));
		await waitFor(() => expect(test.post).toHaveBeenCalledWith("/api/v1/mobile/keep-awake", { body: { enabled: true, lidClosed: false } }));
		expect(state.sessions[0].lidClosed).toBe(false);
	});
	it.each(["setup failed", "low_battery", "critical_heat"])("reports a refused closed-lid start without silently falling back (%s)", async (reason) => {
		state.capabilities.push({ id: "lid", available: true, reason: "" });
		test.post.mockResolvedValue({ error: reason });
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(reason));
		expect(test.post).toHaveBeenCalledTimes(1);
		expect(test.post).toHaveBeenCalledWith("/api/v1/awake/sessions", { body: expect.objectContaining({ lidClosed: true }) });
		expect(state.active).toBe(false);
		expect(mobile.keepAwake.enabled).toBe(false);
	});
	it.each([true, false])("waits for closed-lid acknowledgement even when idle wake is already active (acknowledged=%s)", async (acknowledged) => {
		state.capabilities.push({ id: "lid", available: true, reason: "" });
		const post = test.post.getMockImplementation()!;
		test.post.mockImplementation(async (path, options) => {
			const result = await post(path, options);
			if (path === "/api/v1/awake/sessions") {
				state.lidActive = false;
				state.reason = "starting";
				return { data: snapshot() };
			}
			return result;
		});
		await show();
		vi.useFakeTimers();
		try {
			fireEvent.click(coffee());
			await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
			expect(state.active).toBe(true);
			expect(coffee()).toHaveAttribute("aria-busy", "true");
			expect(coffee()).toHaveAccessibleDescription(/Starting Stay Awake/);
			expect(screen.queryByTestId("awake-inactive-indicator")).toBeNull();
			state.lidActive = acknowledged;
			await act(async () => { await vi.advanceTimersByTimeAsync(acknowledged ? 600 : 8_100); });
			expect(coffee()).not.toHaveAttribute("aria-busy");
			expect(coffee()).toBeEnabled();
			if (acknowledged) {
				expect(coffee()).toHaveAccessibleDescription(/including with the lid closed/);
				expect(screen.queryByTestId("awake-inactive-indicator")).toBeNull();
			} else {
				expect(coffee()).toHaveAccessibleDescription(/closed-lid wake is not active/);
				expect(screen.getByTestId("awake-inactive-indicator")).toBeInTheDocument();
			}
		} finally {
			client.clear();
			vi.useRealTimers();
		}
	});
	it("shows the daemon's closed-lid failure reason in the sidebar tooltip", async () => {
		state.capabilities.push({ id: "lid", available: true, reason: "" });
		state.sessions = [awakeSession({ lidClosed: true })];
		state.active = true;
		state.reason = "native_failed";
		await show();
		expect(coffee()).toHaveAccessibleDescription(/closed-lid wake is not active\. macOS did not confirm the lid control/);
	});
	it("states the configured battery cutoff", async () => {
		state.capabilities.push({ id: "lid", available: true, reason: "" });
		state.settings.stopBatteryPercent = 25;
		await show();
		expect(coffee()).toHaveAccessibleDescription(/below 25% battery/);
	});
	it("uses a desktop lid lease for the Connect Mobile lid switch while mobile access is off", async () => {
		state.capabilities.push({ id: "lid", available: true, reason: "" });
		await show();
		fireEvent.click(screen.getByRole("button", { name: "Lid wake" }));
		await waitFor(() => expect(test.post).toHaveBeenCalledWith("/api/v1/mobile/keep-awake", { body: { enabled: true, lidClosed: true } }));
		expect(test.post).toHaveBeenCalledWith("/api/v1/awake/sessions", { body: expect.objectContaining({ lidClosed: true }) });
		await waitFor(() => expect(coffee()).toHaveAccessibleDescription(/including with the lid closed/));
		expect(screen.queryByTestId("awake-inactive-indicator")).toBeNull();
	});
	it("turning closed-lid off ends only lid sessions and keeps other owners", async () => {
		state.capabilities.push({ id: "lid", available: true, reason: "" });
		state.sessions = [awakeSession({ id: "lid", lidClosed: true }), awakeSession({ id: "timer", mode: "duration" })];
		state.active = true;
		state.lidActive = true;
		await show();
		fireEvent.click(screen.getByRole("button", { name: "Lid wake" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Lid wake" })).toHaveAttribute("aria-pressed", "false"));
		expect(test.post).not.toHaveBeenCalledWith("/api/v1/awake/stop");
		expect(test.post).not.toHaveBeenCalledWith("/api/v1/awake/sessions", expect.anything());
		expect(state.paused).toBe(false);
		expect(state.sessions.find((s) => s.id === "lid")?.endedAt).toBe("now");
		expect(state.sessions.find((s) => s.id === "timer")?.endedAt).toBeUndefined();
		expect(coffee()).toHaveAttribute("aria-pressed", "true");
	});
	it("replaces a sidebar lid session with idle wake when closed-lid is turned off", async () => {
		state.capabilities.push({ id: "lid", available: true, reason: "" });
		state.sessions = [awakeSession({ id: "lid", lidClosed: true })];
		state.active = true;
		state.lidActive = true;
		await show();
		fireEvent.click(screen.getByRole("button", { name: "Lid wake" }));
		await waitFor(() => expect(state.sessions.find((s) => s.id === "lid")?.endedAt).toBe("now"));
		expect(test.post).toHaveBeenCalledWith("/api/v1/awake/sessions", { body: expect.objectContaining({ lidClosed: false }) });
		expect(state.active).toBe(true);
		expect(state.lidActive).toBe(false);
	});
	it("stops session-scoped, timed, and automatic wake owners", async () => {
		state.sessions = [awakeSession({ id: "scoped", scope: "session", sessionId: "work" }), awakeSession({ id: "timer", mode: "duration" })];
		state.rules = [{ id: "automatic", enabled: true }] as AwakeState["rules"];
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(test.post).toHaveBeenCalledWith("/api/v1/awake/stop"));
		await waitFor(() => expect(coffee()).toHaveAttribute("aria-pressed", "false"));
		expect(state.paused).toBe(true);
		expect(state.sessions.every((s) => s.endedAt)).toBe(true);
	});
	it("shows armed automatic rules even without a manual session", async () => {
		state.rules = [{ id: "automatic", enabled: true }] as AwakeState["rules"];
		await show();
		expect(coffee()).toHaveAttribute("aria-pressed", "true");
		fireEvent.click(coffee());
		await waitFor(() => expect(coffee()).toHaveAttribute("aria-pressed", "false"));
	});
	it.each(["mobile", "session", "rule"])("does not report a persisted %s wake request as starting", async (source) => {
		if (source === "mobile") mobile.keepAwake.enabled = true;
		if (source === "session") state.sessions = [awakeSession({})];
		if (source === "rule") state.rules = [{ id: "automatic", enabled: true }] as AwakeState["rules"];
		await show();
		expect(coffee()).toHaveAttribute("aria-pressed", "true");
		expect(coffee()).not.toHaveAttribute("aria-busy");
	});

	it.each([
		{ reason: "low_battery", stopBatteryPercent: 10, explanation: /battery reached the configured safety limit/ },
		{ reason: "low_battery", stopBatteryPercent: 45, explanation: /battery reached the configured safety limit/ },
		{ reason: "critical_heat", stopBatteryPercent: 10, explanation: /Mac needs to cool down/ },
		{ reason: "battery_unknown", stopBatteryPercent: 10, explanation: /battery state could not be read/ },
	])("explains a confirmed $reason safety stop (battery limit $stopBatteryPercent%) and keeps it clearable", async ({ reason, stopBatteryPercent, explanation }) => {
		state.sessions = [awakeSession({})];
		state.settings.stopBatteryPercent = stopBatteryPercent;
		state.active = true;
		await show();
		expect(coffee().querySelector("[data-nav-row-highlight]")).toHaveClass("bg-interactive-active");
		state.active = false;
		state.paused = true;
		state.reason = reason;
		await act(async () => { await client.invalidateQueries({ queryKey: awakeQueryKey }); });
		expect(coffee()).toHaveAttribute("aria-pressed", "true");
		await waitFor(() => expect(screen.getByTestId("awake-inactive-indicator")).toBeInTheDocument());
		expect(coffee().querySelector("[data-nav-row-highlight]")).not.toHaveClass("bg-interactive-active");
		expect(coffee()).toHaveAccessibleDescription(/inactive/);
		expect(coffee()).toHaveAccessibleDescription(explanation);
		await userEvent.hover(coffee());
		expect(await screen.findByRole("tooltip")).toHaveTextContent(/inactive/);
		expect(screen.getByRole("tooltip")).toHaveTextContent(explanation);
		expect(screen.getByRole("tooltip")).not.toHaveTextContent(/\d+%/);
		fireEvent.click(coffee());
		await waitFor(() => expect(coffee()).toHaveAttribute("aria-pressed", "false"));
		expect(test.post).toHaveBeenCalledWith("/api/v1/awake/stop");
	});
	it.each(["helper_missing", "unsupported", "isolated_data_dir", "observation_unavailable", "persistence_failed"])("keeps an unmatched armed rule's inactive description generic despite %s", async (reason) => {
		state.rules = [{ id: "automatic", enabled: true }] as AwakeState["rules"];
		state.reason = reason;
		await show();
		const description = "Stay Awake is configured, but this Mac is not being kept awake. Click to turn off.";
		expect(coffee()).toHaveAttribute("aria-pressed", "true");
		expect(coffee()).toHaveAccessibleDescription(description);
		await userEvent.hover(coffee());
		expect(await screen.findByRole("tooltip")).toHaveTextContent(description);
	});
	it.each([
		{ paused: false, reason: "low_battery" },
		{ paused: false, reason: "critical_heat" },
		{ paused: false, reason: "battery_unknown" },
		{ paused: true, reason: "observation_unavailable" },
		{ paused: true, reason: "persistence_failed" },
		{ paused: true, reason: "unknown_future_reason" },
	])("does not describe an unconfirmed safety stop ($reason, paused=$paused)", async ({ paused, reason }) => {
		state.sessions = [awakeSession({})];
		state.paused = paused;
		state.reason = reason;
		await show();
		expect(coffee()).toHaveAttribute("aria-pressed", "true");
		expect(coffee()).toHaveAccessibleDescription("Stay Awake is configured, but this Mac is not being kept awake. Click to turn off.");
	});
	it.each(["mobile", "rule"])("marks configured but inactive %s wake without an active fill", async (source) => {
		if (source === "mobile") mobile.keepAwake.enabled = true;
		else state.rules = [{ id: "automatic", enabled: true }] as AwakeState["rules"];
		await show();
		expect(coffee()).toHaveAccessibleDescription(/configured.*not being kept awake/);
		expect(screen.getByTestId("awake-inactive-indicator")).toBeInTheDocument();
		expect(coffee().querySelector("[data-nav-row-highlight]")).not.toHaveClass("bg-interactive-active");
	});
	it.each(["desktop", "mobile"])("fills the coffee button only when %s actually holds wake", async (source) => {
		if (source === "desktop") state.active = true;
		else Object.assign(mobile.keepAwake, { enabled: true, active: true });
		await show();
		expect(coffee()).toHaveAccessibleDescription(/being kept awake/);
		expect(screen.queryByTestId("awake-inactive-indicator")).toBeNull();
		expect(coffee().querySelector("[data-nav-row-highlight]")).toHaveClass("bg-interactive-active");
	});

	it("fetches restored mobile wake with an empty cache and clears its cached response", async () => {
		mobile.keepAwake.enabled = true;
		await show();
		expect(coffee()).toHaveAttribute("aria-pressed", "true");
		fireEvent.click(coffee());
		await waitFor(() => expect(coffee()).toHaveAttribute("aria-pressed", "false"));
		expect(client.getQueryData(mobileStatusQueryKey)).toMatchObject({ keepAwake: { enabled: false } });
	});
	it("disables both controls until a pending start settles, preventing double-click races", async () => {
		blockStart = true;
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(release).toBeDefined());
		expect(coffee()).toHaveAttribute("aria-busy", "true");
		expect(coffee()).toBeDisabled();
		expect(screen.getByRole("button", { name: "Mobile wake" })).toBeDisabled();
		fireEvent.click(coffee());
		await act(async () => release!());
		await waitFor(() => expect(coffee()).toBeEnabled());
		expect(coffee()).not.toHaveAttribute("aria-busy");
		fireEvent.click(coffee());
		await waitFor(() => expect(coffee()).toHaveAttribute("aria-pressed", "false"));
		expect(state.sessions.filter((s) => !s.endedAt)).toHaveLength(0);
	});
	it.each(["active", "safety stop", "timeout", "status error", "hung status"])("waits for native acknowledgement after the POST (%s)", async (outcome) => {
		const post = test.post.getMockImplementation()!;
		test.post.mockImplementation(async (path, options) => {
			const result = await post(path, options);
			if (path === "/api/v1/awake/sessions") {
				state.active = false;
				state.reason = "starting";
				return { data: snapshot() };
			}
			return result;
		});
		await show();
		vi.useFakeTimers();
		try {
			fireEvent.click(coffee());
			await act(async () => { await vi.advanceTimersByTimeAsync(0); });
			// The POST has finished, but native ownership has not been acknowledged.
			expect(state.sessions).toHaveLength(1);
			for (let i = 0; i < 4; i++) {
				expect(coffee()).toHaveAttribute("aria-busy", "true");
				expect(coffee()).toHaveAccessibleDescription(/Starting Stay Awake/);
				expect(coffee()).toBeDisabled();
				expect(screen.getByRole("button", { name: "Mobile wake" })).toBeDisabled();
				expect(screen.queryByTestId("awake-inactive-indicator")).toBeNull();
				await act(async () => { await vi.advanceTimersByTimeAsync(500); });
			}
			if (outcome === "active") state.active = true;
			if (outcome === "safety stop") {
				state.paused = true;
				state.reason = "low_battery";
			}
			if (outcome === "status error") test.get.mockRejectedValue(new Error("status unavailable"));
			if (outcome === "hung status") test.get.mockImplementation(() => new Promise(() => {}));
			await act(async () => { await vi.advanceTimersByTimeAsync(outcome === "timeout" || outcome === "hung status" ? 3_100 : 600); });
			expect(coffee()).not.toHaveAttribute("aria-busy");
			expect(coffee()).toBeEnabled();
			if (outcome === "active") {
				expect(screen.queryByTestId("awake-inactive-indicator")).toBeNull();
				expect(coffee()).toHaveAccessibleDescription(/being kept awake/);
				expect(coffee().querySelector("[data-nav-row-highlight]")).toHaveClass("bg-interactive-active");
			} else {
				expect(screen.getByTestId("awake-inactive-indicator")).toBeInTheDocument();
				if (outcome === "safety stop") expect(coffee()).toHaveAccessibleDescription(/battery/i);
			}
		} finally {
			client.clear();
			vi.useRealTimers();
		}
	});

	it("releases legacy desktop lid ownership when the lid option is turned off", async () => {
		state.sessions = [awakeSession({ id: "legacy", lidClosed: true })];
		mobile.keepAwake.enabled = true;
		mobile.keepAwake.lidClosed = true;
		await show();
		fireEvent.click(screen.getByRole("button", { name: "Lid wake" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Lid wake" })).toHaveAttribute("aria-pressed", "false"));
		expect(state.sessions.filter((s) => !s.endedAt)).toEqual([expect.objectContaining({ lidClosed: false })]);
		expect(mobile.keepAwake.lidClosed).toBe(false);
		expect(coffee()).toHaveAttribute("aria-pressed", "true");
	});
	it("keeps lid opt-in on the mobile backend and desktop idle-only while mobile access is on", async () => {
		mobile.enabled = true;
		await show();
		fireEvent.click(screen.getByRole("button", { name: "Lid wake" }));
		await waitFor(() => expect(mobile.keepAwake.lidClosed).toBe(true));
		expect(state.sessions[0].lidClosed).toBe(false);
	});
	it("resumes paused wake before starting", async () => {
		state.paused = true;
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(state.active).toBe(true));
		expect(test.post.mock.calls.map(([path]) => path)).toEqual(["/api/v1/awake/resume", "/api/v1/awake/sessions"]);
	});
	it("refreshes a safety stop that happened after the last poll without parsing the error wording", async () => {
		const post = test.post.getMockImplementation()!;
		let attempts = 0;
		test.post.mockImplementation(async (path, options) => {
			if (path === "/api/v1/awake/sessions" && attempts++ === 0) {
				state.paused = true;
				return { error: "A completely reworded daemon error" };
			}
			return post(path, options);
		});
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(state.active).toBe(true));
		expect(test.post.mock.calls.map(([path]) => path)).toEqual(["/api/v1/awake/sessions", "/api/v1/awake/resume", "/api/v1/awake/sessions"]);
		expect(screen.queryByRole("alert")).toBeNull();
	});
	it("does not resume or retry an unrelated failure even when its text mentions safety stop", async () => {
		test.post.mockResolvedValue({ error: "unrelated failure mentioning safety stop" });
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("unrelated failure"));
		expect(test.post).toHaveBeenCalledTimes(1);
		expect(state.paused).toBe(false);
	});
	it("preserves the start failure without resuming when refreshed state is unavailable", async () => {
		await show();
		test.post.mockImplementation(async () => {
			test.get.mockRejectedValue(new Error("status unavailable"));
			return { error: "start refused" };
		});
		fireEvent.click(coffee());
		await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("start refused"));
		expect(test.post).toHaveBeenCalledTimes(1);
	});

	it("does not arm mobile wake when the sidebar starts desktop wake", async () => {
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(state.active).toBe(true));
		expect(mobile.keepAwake.enabled).toBe(false);
		expect(test.post).not.toHaveBeenCalledWith("/api/v1/mobile/keep-awake", expect.anything());
	});
	it("reports safety rearm failures without enabling mobile wake", async () => {
		state.paused = true;
		test.post.mockResolvedValue({ error: "cannot rearm: low_battery" });
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("low_battery"));
		expect(coffee()).not.toHaveAttribute("aria-busy");
		expect(test.post).toHaveBeenCalledTimes(1);
		expect(mobile.keepAwake.enabled).toBe(false);
	});

	it("attempts mobile cleanup even when desktop stop fails and displays the error", async () => {
		mobile.keepAwake.enabled = true;
		test.post.mockImplementation(async (path) => path === "/api/v1/awake/stop" ? { error: "stop failed" } : { data: { keepAwake: { enabled: false } } });
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("stop failed"));
		expect(test.post).toHaveBeenCalledWith("/api/v1/mobile/keep-awake", { body: { enabled: false, lidClosed: false } });
	});
});
