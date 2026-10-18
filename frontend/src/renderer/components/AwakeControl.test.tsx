import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AwakeState } from "../hooks/useAwake";
import { mobileStatusQueryKey } from "../lib/mobile-status-key";
import { AwakeControl } from "./AwakeControl";

const test = vi.hoisted(() => ({
	actAsync: vi.fn(),
	mac: true,
	state: undefined as AwakeState | undefined,
	post: vi.fn(),
}));
vi.mock("../lib/platform", () => ({ isMacPlatform: () => test.mac }));
vi.mock("../lib/api-client", () => ({
	apiClient: { POST: test.post },
	apiErrorMessage: (e: unknown) => String(e),
}));
vi.mock("../hooks/useAwake", async (importOriginal) => ({
	...(await importOriginal<typeof import("../hooks/useAwake")>()),
	useAwake: () => ({
		state: test.state,
		actAsync: test.actAsync,
		busy: false,
		error: null,
	}),
}));

let client: QueryClient;
beforeEach(() => {
	test.actAsync.mockReset().mockResolvedValue(undefined);
	test.post.mockReset().mockResolvedValue({ data: {} });
	test.mac = true;
	client = new QueryClient();
	test.state = {
		settings: { lidClosed: true },
		sessions: [],
		capabilities: [{ id: "idle", available: true }],
	} as unknown as AwakeState;
});
function show() {
	return render(
		<QueryClientProvider client={client}>
			<AwakeControl />
		</QueryClientProvider>,
	);
}

describe("Stay Awake sidebar toggle", () => {
	it("renders nothing off macOS", () => {
		test.mac = false;
		show();
		expect(screen.queryByTestId("awake-button")).toBeNull();
	});

	it("turns on with an indefinite AO session and shows pressed at once", async () => {
		show();
		fireEvent.click(screen.getByRole("button", { name: "Stay Awake" }));
		expect(screen.getByRole("button", { name: "Stay Awake" })).toHaveAttribute("aria-pressed", "true");
		await waitFor(() =>
			expect(test.actAsync).toHaveBeenCalledWith({
				kind: "start",
				request: { scope: "ao", sessionId: "", mode: "indefinite", lidClosed: true },
			}),
		);
	});

	it("turns off by ending every AO session and leaves session-scoped ones alone", async () => {
		test.state!.sessions = [
			{ id: "a", scope: "ao" },
			{ id: "mine", scope: "session", sessionId: "work" },
		] as AwakeState["sessions"];
		show();
		const button = screen.getByRole("button", { name: "Stay Awake" });
		expect(button).toHaveAttribute("aria-pressed", "true");
		fireEvent.click(button);
		await waitFor(() => expect(test.actAsync).toHaveBeenCalledWith({ kind: "end", id: "a" }));
		expect(test.actAsync).toHaveBeenCalledTimes(1);
	});

	it("is on when the Connect Mobile keep-awake flag is on, and off clears it", async () => {
		client.setQueryData(mobileStatusQueryKey, { keepAwake: { supported: true, enabled: true } });
		show();
		const button = screen.getByRole("button", { name: "Stay Awake" });
		expect(button).toHaveAttribute("aria-pressed", "true");
		fireEvent.click(button);
		await waitFor(() =>
			expect(test.post).toHaveBeenCalledWith("/api/v1/mobile/keep-awake", {
				body: { enabled: false, lidClosed: false },
			}),
		);
	});

	it("resumes after a safety stop and retries the start", async () => {
		test.actAsync.mockImplementation(async (action: { kind: string }) => {
			if (action.kind === "start" && test.actAsync.mock.calls.filter(([a]) => a.kind === "start").length === 1)
				throw new Error("invalid stay-awake request: resume after the safety stop before starting another session");
		});
		show();
		fireEvent.click(screen.getByRole("button", { name: "Stay Awake" }));
		await waitFor(() => expect(test.actAsync.mock.calls.map(([a]) => a.kind)).toEqual(["start", "resume", "start"]));
		expect(screen.queryByRole("alert")).toBeNull();
	});

	it("resumes first when the daemon reports it paused", async () => {
		test.state!.paused = true;
		show();
		fireEvent.click(screen.getByRole("button", { name: "Stay Awake" }));
		await waitFor(() => expect(test.actAsync.mock.calls.map(([a]) => a.kind)).toEqual(["resume", "start"]));
	});

	it("is disabled when the Mac cannot hold idle sleep off", () => {
		test.state!.capabilities = [{ id: "idle", available: false }] as AwakeState["capabilities"];
		show();
		expect(screen.getByRole("button", { name: "Stay Awake" })).toBeDisabled();
	});
});
