import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AwakeState } from "../hooks/useAwake";
import { useStayAwake } from "../hooks/useStayAwake";
import { mobileStatusQueryKey } from "../lib/mobile-status-key";
import { AwakeControl } from "./AwakeControl";

const test = vi.hoisted(() => ({ mac: true, get: vi.fn(), post: vi.fn() }));
vi.mock("../lib/platform", () => ({ isMacPlatform: () => test.mac }));
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: test.get, POST: test.post },
	apiErrorMessage: (e: unknown) => String(e),
}));
let client: QueryClient;
let state: AwakeState;
let mobile: { keepAwake: { supported: boolean; enabled: boolean; lidClosed: boolean } };
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
	mobile = { keepAwake: { supported: true, enabled: false, lidClosed: false } };
	test.get.mockReset().mockImplementation(async (path) => ({ data: path === "/api/v1/awake" ? snapshot() : structuredClone(mobile) }));
	test.post.mockReset().mockImplementation(async (path, options) => {
		if (path === "/api/v1/mobile/keep-awake") {
			Object.assign(mobile.keepAwake, options.body);
			return { data: structuredClone(mobile) };
		}
		if (path === "/api/v1/awake/sessions") {
			if (blockStart) await new Promise<void>((resolve) => { release = resolve; });
			state.sessions.push(awakeSession({ id: "new", ...options.body }));
			state.active = true;
		} else if (path === "/api/v1/awake/stop") {
			state.sessions = state.sessions.map((s) => ({ ...s, endedAt: "now" }));
			state.paused = true;
			state.active = false;
			state.lidActive = false;
			state.activeRules = [];
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

	it("starts idle-only wake even when the saved desktop lid default is true", async () => {
		await show();
		fireEvent.click(coffee());
		await waitFor(() => expect(test.post).toHaveBeenCalledWith("/api/v1/awake/sessions", { body: expect.objectContaining({ scope: "ao", mode: "indefinite", lidClosed: false }) }));
		await waitFor(() => expect(coffee()).toHaveAttribute("aria-pressed", "true"));
		expect(screen.getByRole("button", { name: "Lid wake" })).toHaveAttribute("aria-pressed", "false");
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
	it("keeps lid opt-in on the mobile backend and desktop idle-only", async () => {
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
