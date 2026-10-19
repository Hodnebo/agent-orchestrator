import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { TRIAGE_APPROVAL_REQUIRED, type TriageChoice, type TriageRun } from "../../../shared/triage";
import { useTriageStore } from "../../stores/triage-store";
import { queryClient } from "../../lib/query-client";
import { TriageConversation } from "../triage/TriageConversation";
import { TriageRuntime } from "../triage/TriageRuntime";
import { ReportProblemContent } from "./ReportProblemContent";
import { rememberTaskComposerPreference } from "../../lib/task-composer-preferences";
import { STANDALONE_WORKSPACE_ID } from "../../types/workspace";
import { SessionTopbarProvider } from "../SessionTopbarPortal";

const mock = vi.hoisted(() => ({
	catalog: vi.fn(), configure: vi.fn(), getPreference: vi.fn(), setPreference: vi.fn(), list: vi.fn(), get: vi.fn(), prepare: vi.fn(), start: vi.fn(), reply: vi.fn(), cancel: vi.fn(), publish: vi.fn(), signInGitHub: vi.fn(), nativeInput: vi.fn(),
	subscribe: vi.fn(), writeText: vi.fn(), openExternal: vi.fn(), listener: undefined as ((run: TriageRun) => void) | undefined,
}));
vi.mock("../../lib/bridge", () => ({ aoBridge: {
	triage: mock,
	clipboard: { writeText: mock.writeText },
	app: { getVersion: async () => "0.13.0", openExternal: mock.openExternal,
		onCloseShellTerminalShortcut: () => () => {}, onPreviousTabShortcut: () => () => {}, onNextTabShortcut: () => () => {}, setCloseShellTerminalShortcutEnabled: () => {} },
	daemon: { getStatus: async () => ({ state: "error", code: "not_ready" }) },
} }));

const configured: TriageChoice = { harness: "codex", label: "Codex", available: true, models: [{ id: "gpt-5.4", label: "GPT 5.4", default: true, efforts: ["low", "high"] }], accounts: [{ id: "work", label: "Work account", default: true }], updatedAt: "2026-10-19T10:00:00Z" };
function run(overrides: Partial<TriageRun> = {}): TriageRun {
	return { id: "report-1", version: 1, createdAt: "2026-10-19T10:00:00Z", updatedAt: "2026-10-19T10:00:00Z",
		request: { harness: "codex", model: "gpt-5.4", accountId: "work", publishAuthorized: true }, state: "running", activity: "Reading AO diagnostics", messages: [], publication: { state: "" }, ...overrides };
}

beforeEach(() => {
	vi.clearAllMocks();
	useTriageStore.setState(useTriageStore.getInitialState(), true);
	queryClient.clear(); localStorage.clear(); window.location.hash = "/";
	mock.catalog.mockResolvedValue([configured]); mock.configure.mockResolvedValue(undefined); mock.getPreference.mockResolvedValue(null); mock.setPreference.mockResolvedValue(undefined);
		mock.list.mockResolvedValue([]); mock.get.mockResolvedValue(run()); mock.start.mockResolvedValue(run({ execution: "session", sessionId: "normal-session" })); mock.reply.mockResolvedValue(run({ version: 3, state: "running" }));
	mock.prepare.mockResolvedValue(run({ id: "preparation-1", preparation: true, state: "collecting", request: { harness: "", publishAuthorized: false }, activity: "Collecting initial diagnostics" }));
	mock.signInGitHub.mockResolvedValue(run({ state: "completed", publication: { state: "published", issueUrl: "https://github.com/OrchestratorInc/agent-orchestrator/issues/12" } }));
	mock.nativeInput.mockResolvedValue(run({ state: "needs_input", nativeControlsAvailable: true, pendingInput: { id: "native-controls", origin: "native", questions: [
		{ id: "action", prompt: "Continue?\n> Yes\n  No", options: ["Press Enter", "Press Escape"], multiple: false, allowText: false },
	] } }));
	mock.cancel.mockResolvedValue(run({ version: 4, state: "cancelled" })); mock.publish.mockResolvedValue(run({ version: 5, state: "completed", publication: { state: "published", issueUrl: "https://github.com/OrchestratorInc/agent-orchestrator/issues/12" } }));
	mock.subscribe.mockImplementation((listener) => { mock.listener = listener; return () => { mock.listener = undefined; }; });
	mock.writeText.mockResolvedValue(undefined); mock.openExternal.mockResolvedValue(undefined);
});

describe("ReportProblemContent", () => {
	it("collects immediately in parallel with the still-loading chooser without launching or authorizing publication", async () => {
		let resolveCatalog!: (value: TriageChoice[]) => void;
		mock.catalog.mockReturnValue(new Promise<TriageChoice[]>((resolve) => { resolveCatalog = resolve; }));
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await screen.findByText("Collecting initial diagnostics");
		expect(mock.prepare).toHaveBeenCalledTimes(1);
		expect(mock.catalog).toHaveBeenCalledTimes(1);
		expect(mock.start).not.toHaveBeenCalled();
		expect(mock.publish).not.toHaveBeenCalled();
		expect(mock.prepare.mock.calls[0][0]).not.toHaveProperty("publishAuthorized");
		await act(async () => resolveCatalog([configured]));
		await screen.findByRole("button", { name: "Start investigation" });
	});

	it("cancels a dismissed preparation even if its acknowledgement arrives late", async () => {
		let resolvePreparation!: (value: TriageRun) => void;
		mock.prepare.mockReturnValue(new Promise<TriageRun>((resolve) => { resolvePreparation = resolve; }));
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await screen.findByText("Collecting diagnostics…");
		fireEvent.click(screen.getByRole("button", { name: "Close" }));
		await act(async () => resolvePreparation(run({ id: "late-preparation", preparation: true, state: "prepared" })));
		await waitFor(() => expect(mock.cancel).toHaveBeenCalledWith("late-preparation"));
		expect(mock.start).not.toHaveBeenCalled();
		expect(useTriageStore.getState().preparationId).toBeNull();
	});

	it.each(["before closing", "after closing"])("does not cancel an existing investigation returned %s the chooser", async (timing) => {
		const existing = run({ preparation: false });
		useTriageStore.getState().receive(existing);
		let resolvePreparation!: (value: TriageRun) => void;
		if (timing === "after closing") mock.prepare.mockReturnValue(new Promise<TriageRun>((resolve) => { resolvePreparation = resolve; }));
		else mock.prepare.mockResolvedValue(existing);
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("Codex"));
		fireEvent.click(screen.getByRole("button", { name: "Close" }));
		if (timing === "after closing") await act(async () => resolvePreparation(existing));
		expect(mock.cancel).not.toHaveBeenCalled();
		expect(mock.start).not.toHaveBeenCalled();
		expect(useTriageStore.getState().preparationId).toBeNull();
		expect(useTriageStore.getState().runs.find((run) => run.id === existing.id)?.state).toBe("running");
	});

	it("reopens the existing investigation through Start without relaunching it or changing its selection", async () => {
		const existing = run({ preparation: false });
		mock.prepare.mockResolvedValue(existing);
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("Codex"));
		fireEvent.click(screen.getByRole("button", { name: "Close" }));
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Start investigation" })).toBeEnabled());
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(window.location.hash).toBe("#/triage?run=report-1"));
		expect(mock.cancel).not.toHaveBeenCalled();
		expect(mock.start).not.toHaveBeenCalled();
		expect(mock.setPreference).not.toHaveBeenCalled();
		expect(useTriageStore.getState().runs.find((run) => run.id === existing.id)?.request).toEqual(existing.request);
	});

		it("opens the independent recovery conversation only when start explicitly returns the outage fallback", async () => {
		mock.start.mockResolvedValue(run({ id: "preparation-1", state: "collecting" }));
		useTriageStore.setState({ observation: "A previous unrelated report" });
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("Codex"));
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(mock.start).toHaveBeenCalledWith(expect.objectContaining({ preparationId: "preparation-1", observation: "" })));
		await waitFor(() => expect(window.location.hash).toBe("#/triage?run=preparation-1"));
		expect(useTriageStore.getState().preparationId).toBeNull();
		expect(mock.cancel).not.toHaveBeenCalled();
	});

	it("uses the selected standalone harness policy instead of inheriting the affected project's mode and permissions", async () => {
		mock.getPreference.mockResolvedValue({ harness: "codex", model: "gpt-5.4", accountId: "work", mode: "configured-mode", permissions: "plan" });
		mock.catalog.mockResolvedValue([{ ...configured, mode: "native-mode", permissions: "default" }]);
		window.location.hash = "/sessions/affected";
		queryClient.setQueryData(["workspaces"], [{ id: "project", sessions: [{ id: "affected" }] }]);
		queryClient.setQueryData(["project", "project"], { config: { worker: { agent: "codex", agentConfig: { model: "gpt-5.4", mode: "configured-mode", permissions: "plan" } } } });
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("Codex"));
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(mock.start).toHaveBeenCalledWith(expect.objectContaining({ harness: "codex", model: "gpt-5.4", mode: "native-mode", permissions: "default" })));
		expect(mock.configure).not.toHaveBeenCalled();
	});
	it("launches the saved non-Claude harness with its current normal model, effort and account", async () => {
		const selection = { harness: "opencode", model: "openai/gpt-5.4", effort: "high", accountId: "opencode-work" };
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "opencode", { model: selection.model, mode: "", effort: "high" });
		mock.catalog.mockResolvedValue([
			{ ...configured, harness: "claude-code", label: "Claude Code" },
			{ ...configured, harness: "opencode", label: "OpenCode", models: [{ id: selection.model, label: "GPT 5.4", efforts: ["high"] }], accounts: [{ id: selection.accountId, label: "Work" }] },
		]);
		mock.getPreference.mockResolvedValue(selection);
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("OpenCode"));
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(mock.start).toHaveBeenCalledExactlyOnceWith(expect.objectContaining(selection)));
		expect(mock.openExternal).not.toHaveBeenCalled();
	});

	it("lets the user select an unavailable configured agent and keeps it for the precise manual fallback", async () => {
		const user = userEvent.setup();
		const reason = "This agent's guided conversation transport is unavailable.";
		mock.catalog.mockResolvedValue([
			{ ...configured, harness: "claude-code", label: "Claude Code" },
			{ ...configured, harness: "cursor", label: "Cursor", available: false, unavailableReason: reason,
				models: [{ id: "selected-model", label: "Selected model", default: true }], accounts: [{ id: "cursor-paid", label: "Selected account", default: true }] },
		]);
		mock.getPreference.mockResolvedValue({ harness: "claude-code", model: "gpt-5.4", accountId: "work" });
		render(<ReportProblemContent active />);
		await user.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("Claude Code"));
		await user.click(screen.getByRole("button", { name: "Support agent" }));
		const option = await screen.findByRole("menuitem", { name: /Cursor/ });
		expect(option).not.toHaveAttribute("aria-disabled", "true");
		await user.click(option);
		expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("Cursor");
		await user.click(screen.getByRole("button", { name: "Start investigation" }));
		await screen.findByText(reason);
		expect(mock.start).not.toHaveBeenCalled();
		expect(useTriageStore.getState().selection).toEqual({ harness: "cursor", model: "selected-model", accountId: "cursor-paid" });
		await user.click(await screen.findByRole("button", { name: "Copy report" }));
		await waitFor(() => expect(mock.writeText).toHaveBeenCalledWith(expect.stringContaining("Agent: cursor")));
		const copied = mock.writeText.mock.calls.at(-1)![0] as string;
		expect(copied).toContain("Model: selected-model");
		expect(copied).toContain("Account: cursor-paid");
		expect(copied).toContain(reason);
		expect(copied).not.toContain("Agent: claude-code");
		expect(mock.openExternal).not.toHaveBeenCalled();
	});

	it("opens a compact launcher with normal defaults and starts only on Start investigation", async () => {
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "codex", { model: "gpt-5.4", mode: "", effort: "high" });
		render(<ReportProblemContent active />);
		expect(screen.getAllByRole("button")).toHaveLength(1);
		expect(screen.queryByRole("textbox")).toBeNull();
		expect(mock.catalog).not.toHaveBeenCalled();
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("Codex"));
		expect(mock.prepare).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({ surface: "report-problem" }));
		expect(mock.start).not.toHaveBeenCalled();
		expect(screen.queryByRole("textbox")).toBeNull();
		expect(screen.queryByText("Agent settings")).toBeNull();
		expect(screen.queryByLabelText("Model")).toBeNull();
		expect(screen.queryByLabelText("Account")).toBeNull();
		expect(screen.queryByText("Configured default")).toBeNull();
		expect(screen.queryByText("This computer")).toBeNull();
		queryClient.setQueryData(["workspaces"], []);
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(mock.start).toHaveBeenCalledWith(expect.objectContaining({ harness: "codex", model: "gpt-5.4", effort: "high", accountId: "work", preparationId: "preparation-1", publishAuthorized: true })));
		expect(mock.setPreference).not.toHaveBeenCalled();
		await waitFor(() => expect(window.location.hash).toBe("#/sessions/normal-session"));
		expect(queryClient.getQueryState(["workspaces"])?.isInvalidated).toBe(true);
	});

	it("retries an uncertain normal launch with the same preparation after closing and reopening, without starting recovery", async () => {
		const request = { ...run().request, preparationId: "preparation-1", observation: "", origin: { surface: "report-problem" }, publishAuthorized: true };
		mock.start.mockResolvedValueOnce(run({ id: "preparation-1", execution: "session", state: "prepared", request }))
			.mockResolvedValueOnce(run({ id: "preparation-1", execution: "session", sessionId: "real-session", request }));
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Start investigation" })).toBeEnabled());
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await screen.findByRole("alert");
		expect(window.location.hash).toBe("#/");
		expect(screen.getByRole("button", { name: "Support agent" })).toBeDisabled();
		expect(screen.queryByRole("button", { name: "Copy report" })).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "Close" }));
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Start investigation" })).toBeEnabled());
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(window.location.hash).toBe("#/sessions/real-session"));
		expect(mock.start).toHaveBeenCalledTimes(2);
		expect(mock.start.mock.calls[1][0]).toEqual(mock.start.mock.calls[0][0]);
		expect(mock.prepare).toHaveBeenCalledTimes(1);
		expect(mock.cancel).not.toHaveBeenCalled();
	});

	it("replays the exact pending session request even after its model and account are removed from current choices", async () => {
		const request = { ...run().request, preparationId: "preparation-1", observation: "", origin: { surface: "report-problem" }, publishAuthorized: true };
		mock.start.mockResolvedValueOnce(run({ id: "preparation-1", execution: "session", state: "prepared", request }))
			.mockResolvedValueOnce(run({ id: "preparation-1", execution: "session", sessionId: "already-created", request }));
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Start investigation" })).toBeEnabled());
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await screen.findByRole("alert");
		fireEvent.click(screen.getByRole("button", { name: "Close" }));
		mock.catalog.mockResolvedValue([]);
		queryClient.setQueryData(["agent-models", "codex", ""], { models: [] });
		queryClient.setQueryData(["agent-accounts"], []);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Start investigation" })).toBeEnabled());
		expect(screen.getByRole("button", { name: "Support agent" })).toBeDisabled();
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(window.location.hash).toBe("#/sessions/already-created"));
		expect(mock.start).toHaveBeenCalledTimes(2);
		expect(mock.start.mock.calls[1][0]).toEqual(mock.start.mock.calls[0][0]);
		expect(mock.start.mock.calls[1][0]).toMatchObject({ model: "gpt-5.4", accountId: "work", preparationId: "preparation-1" });
		expect(mock.prepare).toHaveBeenCalledTimes(1);
		expect(mock.configure).not.toHaveBeenCalled();
		expect(mock.cancel).not.toHaveBeenCalled();
	});

	it("merges a late known session identity without rewinding newer publication data, and rejects a conflicting identity", async () => {
		let resolveStart!: (value: TriageRun) => void;
		mock.start.mockReturnValue(new Promise<TriageRun>((resolve) => { resolveStart = resolve; }));
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Start investigation" })).toBeEnabled());
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(mock.start).toHaveBeenCalledTimes(1));
		const newer = run({ id: "preparation-1", updatedAt: "2026-10-19T10:01:00Z", state: "report_saved", activity: "Newer report saved",
			messages: [{ id: "new", role: "assistant", text: "Preserve this update", at: "2026-10-19T10:01:00Z" }], publication: { state: "issue_unknown", body: "Current saved body" } });
		act(() => useTriageStore.getState().receive(newer));
		await act(async () => resolveStart(run({ id: "preparation-1", execution: "session", sessionId: "known-session", activity: "Old acknowledgement" })));
		await waitFor(() => expect(window.location.hash).toBe("#/sessions/known-session"));
		const linked = useTriageStore.getState().runs.find((run) => run.id === "preparation-1");
		expect(linked).toEqual({ ...newer, execution: "session", sessionId: "known-session" });
		act(() => useTriageStore.getState().receive(run({ id: "preparation-1", execution: "session", sessionId: "different-session", updatedAt: "2026-10-19T10:02:00Z" })));
		expect(useTriageStore.getState().runs.find((run) => run.id === "preparation-1")).toEqual(linked);
		expect(mock.cancel).not.toHaveBeenCalled();
	});

	it("keeps native model and device-account defaults internal without launcher settings entries", async () => {
		mock.catalog.mockResolvedValue([{ ...configured, models: [{ id: "", label: "Configured default", default: true }], accounts: [{ id: "device-codex", label: "This computer", default: true }] }]);
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("Codex"));
		expect(screen.queryByText("Agent settings")).toBeNull();
		expect(screen.queryByText("Configured default")).toBeNull();
		expect(screen.queryByText("This computer")).toBeNull();
		expect(screen.queryByRole("combobox")).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(mock.start).toHaveBeenCalledWith(expect.objectContaining({ harness: "codex", model: "", accountId: "device-codex" })));
		await waitFor(() => expect(window.location.hash).toBe("#/sessions/normal-session"));
	});

	it("repairs stale hidden choices from current defaults and reselects the same harness without a setup control", async () => {
		const user = userEvent.setup();
		mock.catalog.mockResolvedValue([{ ...configured, models: [{ id: "current", label: "Current", default: true, efforts: ["medium"] }], accounts: [{ id: "replacement", label: "Current account", default: true }] }]);
		mock.getPreference.mockResolvedValue({ harness: "codex", model: "retired", effort: "high", accountId: "removed" });
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "codex", { model: "retired", mode: "", effort: "high" });
		queryClient.setQueryData(["settings"], { harnessDefaults: { codex: { disabledModels: ["retired"] } } });
		queryClient.setQueryData(["agent-models", "codex", ""], { models: [{ id: "retired", label: "Old" }, { id: "current", label: "Current", isDefault: true, efforts: ["medium"], defaultEffort: "medium" }] });
		render(<ReportProblemContent active />);
		await user.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("Codex"));
		expect(useTriageStore.getState().selection).toEqual({ harness: "codex", model: "current", effort: "medium", accountId: "replacement" });
		await user.click(screen.getByRole("button", { name: "Support agent" }));
		await user.click(await screen.findByRole("menuitem", { name: /Codex/ }));
		await user.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(mock.start).toHaveBeenCalledWith(expect.objectContaining({ harness: "codex", model: "current", effort: "medium", accountId: "replacement" })));
		expect(screen.queryByText("Agent settings")).toBeNull();
	});

	it("requires the explicit Unreal approval-less action without ever changing its configured policy", async () => {
		mock.catalog.mockResolvedValue([{ ...configured, harness: "unreal-agent", label: "Unreal Agent", models: [{ id: "native", label: "Native", default: true }], accounts: undefined, permissions: "default" }]);
		mock.start.mockRejectedValueOnce(new Error(`Error invoking remote method: ${TRIAGE_APPROVAL_REQUIRED}`))
			.mockResolvedValueOnce(run({ id: "preparation-1", execution: "session", sessionId: "unreal-session", request: { harness: "unreal-agent", model: "native", permissions: "bypass-permissions", publishAuthorized: true } }));
		useTriageStore.setState({ explicitSelection: true });
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("Unreal Agent"));
		expect(screen.queryByRole("button", { name: "Start without approvals" })).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		const retry = await screen.findByRole("button", { name: "Start without approvals" });
		expect(mock.start).toHaveBeenCalledTimes(1);
		expect(mock.start.mock.calls[0][0]).toMatchObject({ harness: "unreal-agent", permissions: "default", preparationId: "preparation-1" });
		expect(mock.configure).not.toHaveBeenCalled();
		fireEvent.click(retry);
		await waitFor(() => expect(window.location.hash).toBe("#/sessions/unreal-session"));
		expect(mock.start).toHaveBeenCalledTimes(2);
		expect(mock.start.mock.calls[1][0]).toMatchObject({ harness: "unreal-agent", permissions: "bypass-permissions", preparationId: "preparation-1" });
		expect(mock.configure).not.toHaveBeenCalled();
		expect(useTriageStore.getState().choices[0].permissions).toBe("default");
		expect(mock.setPreference.mock.calls.every(([selection]) => selection.permissions !== "bypass-permissions")).toBe(true);
		expect(mock.cancel).not.toHaveBeenCalled();
	});

	it("keeps a pending normal start alive when its chooser closes before acknowledgement", async () => {
		let resolveStart!: (value: TriageRun) => void;
		mock.start.mockReturnValue(new Promise<TriageRun>((resolve) => { resolveStart = resolve; }));
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Start investigation" })).toBeEnabled());
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await waitFor(() => expect(mock.start).toHaveBeenCalledTimes(1));
		fireEvent.click(screen.getByRole("button", { name: "Close" }));
		expect(mock.cancel).not.toHaveBeenCalled();
		await act(async () => resolveStart(run({ id: "preparation-1", execution: "session", sessionId: "real-session" })));
		await waitFor(() => expect(window.location.hash).toBe("#/sessions/real-session"));
		expect(mock.cancel).not.toHaveBeenCalled();
	});

	it("does not launch on remount or replace an unavailable saved provider", async () => {
		mock.getPreference.mockResolvedValue({ harness: "removed", model: "old", accountId: "personal" });
		const view = render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await waitFor(() => expect(screen.getByRole("button", { name: "Support agent" })).toHaveTextContent("removed"));
		fireEvent.click(screen.getByRole("button", { name: "Start investigation" }));
		await screen.findByText(/Your saved agent, model, or account is unavailable/);
		expect(mock.start).not.toHaveBeenCalled();
		view.unmount(); render(<ReportProblemContent active />);
		expect(mock.start).not.toHaveBeenCalled();
		expect(mock.setPreference).not.toHaveBeenCalled();
	});

	it("collects desktop fallback when the support binary is unavailable", async () => {
		mock.catalog.mockRejectedValue(new Error("Support helper executable is missing"));
		render(<ReportProblemContent active />);
		fireEvent.click(screen.getByRole("button", { name: "Report a problem" }));
		await screen.findByText("Support helper executable is missing");
		fireEvent.click(screen.getByRole("button", { name: "Open GitHub draft" }));
		await waitFor(() => expect(mock.openExternal).toHaveBeenCalledWith(expect.stringContaining("github.com/OrchestratorInc/agent-orchestrator/issues/new")));
		expect(mock.writeText).toHaveBeenCalledWith(expect.stringContaining("AO 0.13.0"));
		expect(mock.start).not.toHaveBeenCalled();
	});
});

describe("support conversation", () => {
	it("mounts the real shared chat and question dock without daemon requests or a normal session, with Stop visible under the shell provider", async () => {
		const fetch = vi.spyOn(globalThis, "fetch").mockRejectedValue(new Error("Daemon unavailable"));
		mock.get.mockResolvedValue(run({ state: "needs_input", messages: [{ id: "answer", role: "assistant", text: "I checked the startup report.", at: "2026-10-19T10:00:00Z" }], pendingInput: { id: "shared-question", origin: "triage", questions: [
			{ id: "symptom", prompt: "What went wrong?", options: ["Blank window", "Slow response"], multiple: false },
		] } }));
		try {
			render(<SessionTopbarProvider><TriageConversation runId="report-1" /></SessionTopbarProvider>);
			await screen.findByRole("radio", { name: "Blank window" });
			expect(screen.getByRole("region", { name: "Chat" })).toBeInTheDocument();
			expect(screen.getByTestId("chat-timeline")).toBeInTheDocument();
			expect(screen.getByTestId("elicitation-dock")).toBeInTheDocument();
			expect(screen.getByText("I checked the startup report.")).toBeInTheDocument();
			expect(screen.getByRole("button", { name: "Stop" })).toBeEnabled();
			expect(screen.getByRole("textbox", { name: "Reply to support agent" })).not.toHaveAttribute("aria-required", "true");
			fireEvent.click(screen.getByRole("radio", { name: "Blank window" }));
			fireEvent.click(screen.getByRole("button", { name: "Continue" }));
			await waitFor(() => expect(mock.reply).toHaveBeenCalledWith("report-1", "", "shared-question", { symptom: ["Blank window"] }));
			expect(screen.getByRole("combobox", { name: "Message the agent" })).toHaveAttribute("contenteditable", "true");
			expect(screen.getByRole("button", { name: "Stop" })).toBeEnabled();
			fireEvent.click(screen.getByRole("button", { name: "Stop" }));
			await waitFor(() => expect(mock.cancel).toHaveBeenCalledWith("report-1"));
			await waitFor(() => expect(screen.getByRole("combobox", { name: "Message the agent" })).toHaveAttribute("contenteditable", "false"));
			expect(fetch).not.toHaveBeenCalled();
			expect(mock.start).not.toHaveBeenCalled();
		} finally { fetch.mockRestore(); }
	});
	it("hides native controls for conversations which do not advertise them", async () => {
		render(<TriageConversation runId="report-1" />);
		await screen.findByText("Reading AO diagnostics");
		expect(screen.queryByRole("button", { name: "Agent needs input?" })).toBeNull();
		expect(mock.nativeInput).not.toHaveBeenCalled();
	});

	it("requests the advertised native screen without guessing or submitting an answer", async () => {
		mock.get.mockResolvedValue(run({ nativeControlsAvailable: true }));
		render(<TriageConversation runId="report-1" />);
		fireEvent.click(await screen.findByRole("button", { name: "Agent needs input?" }));
		await waitFor(() => expect(mock.nativeInput).toHaveBeenCalledExactlyOnceWith("report-1"));
		await screen.findByRole("radio", { name: "Press Enter" });
		expect(screen.queryByRole("radio", { name: "Something else" })).toBeNull();
		expect(screen.queryByRole("radio", { name: "I'm not sure" })).toBeNull();
		expect(screen.queryByRole("button", { name: "Agent needs input?" })).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "Continue" }));
		await screen.findByText("Choose an answer.");
		expect(mock.reply).not.toHaveBeenCalled();
		expect(mock.start).not.toHaveBeenCalled();
	});
	it("submits a single selected triage option without typing or recording an unselected symptom", async () => {
		mock.get.mockResolvedValue(run({ state: "needs_input", pendingInput: { id: "symptom-question", origin: "triage", questions: [
			{ id: "symptom", prompt: "What went wrong?", options: ["Blank window", "Slow response", "Something else", "I'm not sure"], multiple: false, allowText: true },
		] } }));
		render(<TriageConversation runId="report-1" />);
		await screen.findByRole("radio", { name: "Blank window" });
		expect(mock.reply).not.toHaveBeenCalled();
		fireEvent.click(screen.getByRole("button", { name: "Continue" }));
		await screen.findByText("Choose an answer.");
		expect(mock.reply).not.toHaveBeenCalled();
		expect(screen.getByRole("textbox", { name: "Reply to support agent" })).not.toHaveAttribute("aria-required", "true");
		fireEvent.click(screen.getByRole("radio", { name: "Blank window" }));
		fireEvent.click(screen.getByRole("radio", { name: "Slow response" }));
		expect(screen.getByRole("radio", { name: "Blank window" })).not.toBeChecked();
		fireEvent.click(screen.getByRole("button", { name: "Continue" }));
		await waitFor(() => expect(mock.reply).toHaveBeenCalledExactlyOnceWith("report-1", "", "symptom-question", { symptom: ["Slow response"] }));
	});

	it("allows multiselect and optional Something else details without requiring text", async () => {
		mock.get.mockResolvedValue(run({ state: "needs_input", pendingInput: { id: "multi-question", origin: "triage", questions: [
			{ id: "surfaces", prompt: "Where does it happen?", options: ["Chat", "Sidebar", "Something else", "I'm not sure"], multiple: true, allowText: true },
		] } }));
		render(<TriageConversation runId="report-1" />);
		fireEvent.click(await screen.findByRole("checkbox", { name: "Chat" }));
		fireEvent.click(screen.getByRole("checkbox", { name: "Sidebar" }));
		fireEvent.click(screen.getByRole("button", { name: "Continue" }));
		await waitFor(() => expect(mock.reply).toHaveBeenCalledWith("report-1", "", "multi-question", { surfaces: ["Chat", "Sidebar"] }));
	});

	it("sends an uncertainty escape as ordinary text instead of inventing a native enum value", async () => {
		mock.get.mockResolvedValue(run({ state: "needs_input", pendingInput: { id: "native-question", origin: "native", questions: [
			{ id: "frequency", prompt: "How often?", options: ["Always", "Sometimes"], multiple: false, allowText: false },
		] } }));
		render(<TriageConversation runId="report-1" />);
		fireEvent.click(await screen.findByRole("radio", { name: "I'm not sure" }));
		expect(screen.getByRole("textbox", { name: "Reply to support agent" })).not.toHaveAttribute("aria-required", "true");
		fireEvent.click(screen.getByRole("button", { name: "Continue" }));
		await waitFor(() => expect(mock.reply).toHaveBeenCalledWith("report-1", "How often?: I'm not sure", "native-question", undefined));
	});

	it("offers optional native Something else text and submits without requiring it", async () => {
		mock.get.mockResolvedValue(run({ state: "needs_input", pendingInput: { id: "native-question", origin: "native", questions: [
			{ id: "frequency", prompt: "How often?", options: ["Always", "Sometimes"], multiple: false, allowText: false },
		] } }));
		render(<TriageConversation runId="report-1" />);
		fireEvent.click(await screen.findByRole("radio", { name: "Something else" }));
		expect(screen.getByRole("textbox", { name: "Reply to support agent" })).toHaveValue("");
		fireEvent.click(screen.getByRole("button", { name: "Continue" }));
		await waitFor(() => expect(mock.reply).toHaveBeenCalledWith("report-1", "How often?: Something else", "native-question", undefined));
	});

	it.each(["Press Enter", "Send typed text"])("handles the native %s action with an optional text slot", async (action) => {
		mock.get.mockResolvedValue(run({ state: "needs_input", nativeControlsAvailable: true, pendingInput: { id: "native-terminal", origin: "native", questions: [
			{ id: "action", prompt: "Continue?\n> Yes\n  No", options: ["Press Enter", "Press Escape", "Move up", "Move down", "Send typed text"], multiple: false, allowText: false },
			{ id: "text", prompt: "Text (optional)", allowText: true },
		] } }));
		render(<TriageConversation runId="report-1" />);
		fireEvent.click(await screen.findByRole("radio", { name: action }));
		if (action === "Send typed text") {
			fireEvent.change(screen.getByRole("textbox", { name: "Reply to support agent" }), { target: { value: "A human-selected value" } });
		} else expect(screen.getByRole("textbox", { name: "Reply to support agent" })).toHaveValue("");
		fireEvent.click(screen.getByRole("button", { name: "Continue" }));
		await waitFor(() => expect(mock.reply).toHaveBeenCalledWith("report-1", "", "native-terminal", {
			action: [action], ...(action === "Send typed text" ? { text: ["A human-selected value"] } : {}),
		}));
	});

	it("reuses integrated GitHub sign-in only when the saved publication requires it", async () => {
		mock.get.mockResolvedValue(run({ state: "report_saved", draft: { title: "Problem", body: "Evidence" }, publication: { state: "auth_required" } }));
		render(<TriageConversation runId="report-1" />);
		fireEvent.click(await screen.findByRole("button", { name: "Sign in to GitHub" }));
		await waitFor(() => expect(mock.signInGitHub).toHaveBeenCalledExactlyOnceWith("report-1"));
		await screen.findByRole("link", { name: "View issue" });
		expect(mock.start).not.toHaveBeenCalled();
	});
	it.each([false, true])("uses the standard Skip action without sending a native key, terminal=%s", async (terminal) => {
		mock.get.mockResolvedValue(run({ state: "needs_input", nativeControlsAvailable: terminal, pendingInput: { id: "input", origin: terminal ? "native" : "triage", questions: [
			{ id: "action", prompt: "What happened?", options: terminal ? ["Press Enter", "Keep waiting"] : ["Crash", "Hang"] },
		] } }));
		render(<TriageConversation runId="report-1" />);
		fireEvent.click(await screen.findByRole("button", { name: "Skip" }));
		await waitFor(() => expect(mock.reply).toHaveBeenCalledExactlyOnceWith("report-1", terminal ? "" : "I'm not sure", "input", terminal ? { action: ["Keep waiting"] } : undefined));
	});

	it("uses the standard question Cancel action to stop the investigation", async () => {
		mock.get.mockResolvedValue(run({ state: "needs_input", pendingInput: { id: "input", origin: "triage", questions: [{ id: "symptom", prompt: "What happened?", options: ["Crash"] }] } }));
		render(<TriageConversation runId="report-1" />);
		fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
		await waitFor(() => expect(mock.cancel).toHaveBeenCalledExactlyOnceWith("report-1"));
		expect(mock.reply).not.toHaveBeenCalled();
	});
	it("copies collected evidence and a selected-agent diagnostic prompt after a failed run without a draft", async () => {
		mock.get.mockResolvedValue(run({ state: "failed", error: "Guided transport unavailable", evidence: [
			{ id: "daemon", kind: "daemon log", source: "desktop", status: "collected", collectedAt: "2026-10-19T10:00:00Z",
				text: "Daemon exited with code 1. Authorization: Bearer private-bearer /Users/person/private-project",
				values: { apiKey: "private-api-key", nested: { client_secret: "private-client-secret" }, exitCode: 1 } },
		] }));
		render(<TriageConversation runId="report-1" />);
		fireEvent.click(await screen.findByRole("button", { name: "Copy report" }));
		await waitFor(() => expect(mock.writeText).toHaveBeenCalled());
		const copied = mock.writeText.mock.calls.at(-1)![0] as string;
		expect(copied).toContain("Agent: codex");
		expect(copied).toContain("Model: gpt-5.4");
		expect(copied).toContain("Account: work");
		expect(copied).toContain("Do not switch providers, models, or accounts");
		expect(copied).toContain("Daemon exited with code 1");
		expect(copied).toContain('"exitCode": 1');
		expect(copied).toContain("[redacted-secret]");
		for (const secret of ["private-bearer", "private-api-key", "private-client-secret", "/Users/person/private-project"]) expect(copied).not.toContain(secret);
		expect(screen.queryByRole("button", { name: "Open GitHub draft" })).toBeNull();
		expect(mock.start).not.toHaveBeenCalled();
		expect(mock.publish).not.toHaveBeenCalled();
		expect(mock.openExternal).not.toHaveBeenCalled();
	});

	it("preserves a newer input event when an older command snapshot arrives afterward", async () => {
		let resolveGet!: (run: TriageRun) => void;
		mock.get.mockReturnValue(new Promise<TriageRun>((resolve) => { resolveGet = resolve; }));
		render(<><TriageRuntime /><TriageConversation runId="report-1" /></>);
		act(() => mock.listener?.(run({ version: 1, state: "needs_input", updatedAt: "2026-10-19T10:00:00.123456789Z", activity: "Waiting for your answer" })));
		await waitFor(() => expect(screen.getByRole("combobox", { name: "Message the agent" })).toHaveAttribute("contenteditable", "true"));
		await act(async () => resolveGet(run({ version: 1, state: "running", updatedAt: "2026-10-19T10:00:00.123456788Z", activity: "Old acknowledgement" })));
		expect(screen.getByRole("combobox", { name: "Message the agent" })).toHaveAttribute("contenteditable", "true");
		expect(screen.queryByText("Old acknowledgement")).toBeNull();
	});

	it("uses native updates, typed questions, text replies and Stop", async () => {
		render(<><TriageRuntime /><TriageConversation runId="report-1" /></>);
		await screen.findByText("Reading AO diagnostics");
		act(() => mock.listener?.(run({ version: 2, state: "needs_input", activity: "Waiting for your observation", pendingInput: { id: "question-1", questions: [{ id: "frequency", prompt: "How often?", options: ["Every launch", "Sometimes"] }] } })));
		fireEvent.click(await screen.findByRole("radio", { name: "Every launch" }));
		expect(screen.getByRole("textbox", { name: "Reply to support agent" })).not.toHaveAttribute("aria-required", "true");
		fireEvent.change(screen.getByRole("textbox", { name: "Reply to support agent" }), { target: { value: "It started after updating" } });
		fireEvent.click(screen.getByRole("button", { name: "Continue" }));
		await waitFor(() => expect(mock.reply).toHaveBeenCalledWith("report-1", "It started after updating", "question-1", { frequency: ["Every launch"] }));
		fireEvent.click(screen.getByRole("button", { name: "Stop" }));
		await waitFor(() => expect(mock.cancel).toHaveBeenCalledWith("report-1"));
		await waitFor(() => expect(screen.queryByRole("button", { name: "Stop" })).toBeNull());
	});

	it("keeps the saved draft and checks an unknown submission without claiming success", async () => {
		mock.get.mockResolvedValue(run({ state: "report_saved", draft: { title: "Startup failure", body: "Observed startup error" }, publication: { state: "issue_unknown" } }));
		const view = render(<TriageConversation runId="report-1" />);
		await screen.findByText(/Issue submission is unconfirmed/);
		expect(screen.queryByRole("link", { name: "View issue" })).toBeNull();
		expect(screen.queryByText("Report details")).toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "Copy report" }));
		await waitFor(() => expect(mock.writeText).toHaveBeenCalledWith("Startup failure\n\nObserved startup error"));
		view.unmount(); render(<TriageConversation runId="report-1" />);
		expect(mock.start).not.toHaveBeenCalled();
		fireEvent.click(await screen.findByRole("button", { name: "Check submission" }));
		await waitFor(() => expect(mock.publish).toHaveBeenCalledWith("report-1"));
		await screen.findByRole("link", { name: "View issue" });
	});

	it("offers the preserved browser draft only when GitHub auth is required", async () => {
		mock.get.mockResolvedValue(run({ state: "report_saved", draft: { title: "Startup failure", body: "Observed startup error" }, publication: { state: "auth_required" } }));
		render(<TriageConversation runId="report-1" />);
		fireEvent.click(await screen.findByRole("button", { name: "Open GitHub draft" }));
		await waitFor(() => expect(mock.openExternal).toHaveBeenCalledWith(expect.stringContaining("body=Startup+failure%0A%0AObserved+startup+error")));
		expect(mock.start).not.toHaveBeenCalled();
	});
});
