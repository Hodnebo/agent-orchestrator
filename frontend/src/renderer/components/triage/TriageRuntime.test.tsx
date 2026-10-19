import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import type { TriageChoice, TriageRun } from "../../../shared/triage";
import { queryClient } from "../../lib/query-client";
import { useTriageStore } from "../../stores/triage-store";
import { TriageRuntime } from "./TriageRuntime";

const mock = vi.hoisted(() => ({ catalog: vi.fn(), configure: vi.fn(), list: vi.fn(), subscribe: vi.fn(), start: vi.fn(), signInGitHub: vi.fn(), publish: vi.fn() }));
vi.mock("../../lib/bridge", () => ({ aoBridge: { triage: mock } }));
vi.mock("./TriageConversation", () => ({ useTriageStateLabel: () => "Working" }));

const native: TriageChoice = { harness: "codex", label: "Codex", models: [{ id: "gpt-5.4", label: "GPT 5.4", default: true }],
	accounts: [{ id: "work", label: "Work", default: true }], available: true, source: "native-configuration", updatedAt: "2026-10-19T10:00:00Z" };
const ready = (id: string) => ({ id, label: id, authentication: { state: "configured", checkedAt: native.updatedAt }, installation: { state: "installed" } });
function configureCache() {
	queryClient.setQueryData(["settings"], {});
	queryClient.setQueryData(["agent-readiness"], { agents: [ready("codex"), ready("command-code")] });
	queryClient.setQueryData(["agent-models", "command-code", ""], { models: [{ id: "configured-model", label: "Configured model", isDefault: true }], fetchedAt: native.updatedAt });
}

beforeEach(() => {
	vi.clearAllMocks();
	queryClient.clear();
	localStorage.clear();
	useTriageStore.setState(useTriageStore.getInitialState(), true);
	mock.catalog.mockResolvedValue([native]);
	mock.configure.mockResolvedValue(undefined);
	mock.list.mockResolvedValue([]);
	mock.subscribe.mockReturnValue(() => {});
});

it("retains configured agents for daemon outages before the launcher has ever opened", async () => {
	configureCache();
	useTriageStore.setState({ selection: { harness: "command-code", model: "configured-model" }, explicitSelection: true });
	render(<TriageRuntime />);
	await waitFor(() => expect(mock.configure).toHaveBeenCalledTimes(1));
	expect(mock.configure.mock.calls[0][0]).toEqual(expect.arrayContaining([
		expect.objectContaining({ harness: "codex", models: native.models, accounts: native.accounts }),
		expect.objectContaining({ harness: "command-code", models: [expect.objectContaining({ id: "configured-model" })] }),
	]));
	expect(useTriageStore.getState()).toMatchObject({ selection: { harness: "command-code", model: "configured-model" }, catalogLoaded: false, loading: false, error: null });
	expect(mock.start).not.toHaveBeenCalled();
});

it("does not surface collection-only preparations as normal investigation indicators", async () => {
	const preparation: TriageRun = { id: "preparation", version: 1, preparation: true, state: "collecting", activity: "Collecting",
		createdAt: native.updatedAt, updatedAt: native.updatedAt, request: { harness: "", publishAuthorized: false }, messages: [], publication: { state: "" } };
	mock.list.mockResolvedValue([preparation]);
	const view = render(<TriageRuntime />);
	await waitFor(() => expect(useTriageStore.getState().runs).toHaveLength(1));
	act(() => useTriageStore.getState().receive(preparation, true));
	expect(view.container.querySelector("a")).toBeNull();
	act(() => useTriageStore.getState().receive({ ...preparation, preparation: false, request: { harness: "codex", publishAuthorized: true } }, true));
	expect(view.container.querySelector("a")).toHaveAttribute("href", "#/triage?run=preparation");
});

it.each([undefined, "ordinary-session"])("never advertises a separate support conversation for a normal session launch %s", async (sessionId) => {
	const handoff: TriageRun = { id: "handoff", version: 1, execution: "session", sessionId, state: "needs_input", activity: "Opening normal agent",
		createdAt: native.updatedAt, updatedAt: native.updatedAt, request: { harness: "codex", publishAuthorized: true }, messages: [], publication: { state: "" } };
	mock.list.mockResolvedValue([handoff]);
	const view = render(<TriageRuntime />);
	await waitFor(() => expect(useTriageStore.getState().runs).toHaveLength(1));
	act(() => useTriageStore.getState().receive(handoff, true));
	expect(view.container.querySelector("a")).toBeNull();
});

it("offers GitHub sign-in for a saved normal-session report and removes the action after the watched publication succeeds", async () => {
	const run: TriageRun = { id: "handoff", version: 1, execution: "session", sessionId: "normal-agent", state: "report_saved", activity: "Report saved",
		createdAt: native.updatedAt, updatedAt: native.updatedAt, request: { harness: "codex", publishAuthorized: true }, messages: [], publication: { state: "auth_required" } };
	mock.list.mockResolvedValue([run]);
	let rejectSignIn!: (error: Error) => void;
	mock.signInGitHub.mockReturnValue(new Promise<TriageRun>((_resolve, reject) => { rejectSignIn = reject; }));
	render(<TriageRuntime />);
	fireEvent.click(await screen.findByRole("button", { name: "Sign in to GitHub" }));
	expect(mock.signInGitHub).toHaveBeenCalledExactlyOnceWith("handoff");
	expect(screen.getByRole("button", { name: "Sign in to GitHub" })).toBeDisabled();
	expect(screen.getByRole("status")).toHaveTextContent("Finish GitHub sign-in in your browser");
	expect(screen.getByRole("link", { name: "Open session" })).toHaveAttribute("href", "#/sessions/normal-agent");
	await act(async () => rejectSignIn(new Error("Browser sign-in cancelled")));
	expect(await screen.findByRole("alert")).toHaveTextContent("Browser sign-in cancelled");
	expect(screen.getByRole("button", { name: "Sign in to GitHub" })).toBeEnabled();
	act(() => mock.subscribe.mock.calls[0][0]({ ...run, updatedAt: "2026-10-19T10:00:01Z", publication: { state: "published" } }));
	expect(screen.queryByTestId("session-report-action")).toBeNull();
	expect(mock.start).not.toHaveBeenCalled();
});

it.each(["issue_pending", "issue_unknown", "evidence_unknown", "issue_failed", "evidence_failed"])("uses the existing safe publication command for normal-session %s", async (state) => {
	const run: TriageRun = { id: "handoff", version: 1, execution: "session", sessionId: "normal-agent", state: "report_saved", activity: "Report saved",
		createdAt: native.updatedAt, updatedAt: native.updatedAt, request: { harness: "codex", publishAuthorized: true }, messages: [], publication: { state } };
	mock.list.mockResolvedValue([run]);
	mock.publish.mockResolvedValue({ ...run, updatedAt: "2026-10-19T10:00:01Z", publication: { state: "published" } });
	render(<TriageRuntime />);
	fireEvent.click(await screen.findByRole("button", { name: state.endsWith("failed") ? "Retry filing report" : "Check submission" }));
	await waitFor(() => expect(mock.publish).toHaveBeenCalledExactlyOnceWith("handoff"));
	await waitFor(() => expect(screen.queryByTestId("session-report-action")).toBeNull());
	expect(mock.start).not.toHaveBeenCalled();
	expect(mock.signInGitHub).not.toHaveBeenCalled();
});

it("offers search authentication in the same normal agent without publishing or opening another conversation", async () => {
	const run: TriageRun = { id: "search-handoff", version: 1, execution: "session", sessionId: "normal-agent", searchAuthRequired: true, state: "running", activity: "GitHub sign-in required",
		createdAt: native.updatedAt, updatedAt: native.updatedAt, request: { harness: "codex", publishAuthorized: true }, messages: [], publication: { state: "" } };
	mock.list.mockResolvedValue([run]);
	mock.signInGitHub.mockResolvedValue({ ...run, updatedAt: "2026-10-19T10:00:01Z", searchAuthRequired: false });
	render(<TriageRuntime />);
	fireEvent.click(await screen.findByRole("button", { name: "Sign in to GitHub" }));
	expect(mock.signInGitHub).toHaveBeenCalledExactlyOnceWith("search-handoff");
	await waitFor(() => expect(screen.queryByTestId("session-report-action")).toBeNull());
	expect(mock.publish).not.toHaveBeenCalled();
	expect(mock.start).not.toHaveBeenCalled();
});

it("waits for existing local display data and never fetches missing catalogs", async () => {
	render(<TriageRuntime />);
	expect(mock.catalog).not.toHaveBeenCalled();
	act(() => {
		queryClient.setQueryData(["settings", "remote-host"], {});
		queryClient.setQueryData(["agent-readiness", "remote-host"], { agents: [ready("command-code")] });
	});
	expect(mock.catalog).not.toHaveBeenCalled();
	act(() => configureCache());
	await waitFor(() => expect(mock.configure).toHaveBeenCalledTimes(1));
	expect(queryClient.getQueryState(["agent-models", "codex", ""])).toBeUndefined();
	expect(mock.start).not.toHaveBeenCalled();
});

it("serializes changes so the latest enabled-model configuration is the one retained", async () => {
	configureCache();
	let release!: () => void;
	mock.configure.mockImplementationOnce(() => new Promise<void>((resolve) => { release = resolve; }));
	render(<TriageRuntime />);
	await waitFor(() => expect(mock.configure).toHaveBeenCalledTimes(1));
	act(() => queryClient.setQueryData(["settings"], { harnessDefaults: { "command-code": { disabledModels: ["configured-model"] } } }));
	expect(mock.configure).toHaveBeenCalledTimes(1);
	await act(async () => { release(); });
	await waitFor(() => expect(mock.configure).toHaveBeenCalledTimes(2));
	expect(mock.configure.mock.calls[1][0]).toEqual(expect.arrayContaining([
		expect.objectContaining({ harness: "command-code", models: [] }),
	]));
	expect(mock.start).not.toHaveBeenCalled();
});

it("does not rewrite unchanged configuration or persist updates after unmount", async () => {
	configureCache();
	const view = render(<TriageRuntime />);
	await waitFor(() => expect(mock.configure).toHaveBeenCalledTimes(1));
	await act(async () => {
		queryClient.setQueryData(["agent-readiness"], { agents: [ready("codex"), ready("command-code")] });
		queryClient.setQueryData(["workspaces"], []);
		queryClient.setQueryData(["agent-models", "command-code", "remote-project"], { models: [] });
	});
	expect(mock.configure).toHaveBeenCalledTimes(1);
	view.unmount();
	act(() => queryClient.setQueryData(["settings"], { harnessDefaults: { codex: { disabledModels: ["gpt-5.4"] } } }));
	expect(mock.configure).toHaveBeenCalledTimes(1);
});

it("retains an existing account change without changing the launcher selection", async () => {
	configureCache();
	useTriageStore.setState({ selection: { harness: "codex", model: "gpt-5.4", accountId: "work" }, explicitSelection: true });
	render(<TriageRuntime />);
	await waitFor(() => expect(mock.configure).toHaveBeenCalledTimes(1));
	act(() => queryClient.setQueryData(["agent-accounts"], [{ id: "new-work", provider: "codex", signedIn: true, default: true, device: false, email: "work@example.com" }]));
	await waitFor(() => expect(mock.configure).toHaveBeenCalledTimes(2));
	expect(mock.configure.mock.calls[1][0]).toEqual(expect.arrayContaining([
		expect.objectContaining({ harness: "codex", accounts: [expect.objectContaining({ id: "new-work", default: true })] }),
	]));
	expect(useTriageStore.getState().selection?.accountId).toBe("work");
	expect(mock.start).not.toHaveBeenCalled();
});

it("persists an authoritative empty catalog after the same model list was only missing", async () => {
	mock.catalog.mockResolvedValue([{ ...native, models: [], modelsMissing: true }]);
	queryClient.setQueryData(["settings"], {});
	queryClient.setQueryData(["agent-readiness"], { agents: [ready("codex")] });
	render(<TriageRuntime />);
	await waitFor(() => expect(mock.configure).toHaveBeenCalledTimes(1));
	expect(mock.configure.mock.calls[0][0][0]).toMatchObject({ harness: "codex", models: [], modelsMissing: true });
	act(() => queryClient.setQueryData(["agent-models", "codex", ""], { models: [], fetchedAt: native.updatedAt }));
	await waitFor(() => expect(mock.configure).toHaveBeenCalledTimes(2));
	expect(mock.configure.mock.calls[1][0][0].models).toEqual([]);
	expect(mock.configure.mock.calls[1][0][0].modelsMissing).not.toBe(true);
	expect(mock.start).not.toHaveBeenCalled();
});
