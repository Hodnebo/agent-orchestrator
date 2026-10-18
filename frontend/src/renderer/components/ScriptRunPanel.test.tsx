import { act, fireEvent, render as rtlRender, screen, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CloudCpClientEvent } from "../lib/cloud-cp";
import { groupScriptRuns, SCRIPT_STEP_EVENT } from "../lib/cloud-script-runs";
import { ScriptRunPanel } from "./ScriptRunPanel";
import { TooltipProvider } from "./ui/tooltip";

function render(ui: ReactElement) {
	const result = rtlRender(<TooltipProvider>{ui}</TooltipProvider>);
	return { ...result, rerender: (next: ReactElement) => result.rerender(<TooltipProvider>{next}</TooltipProvider>) };
}

let sequence = 0;
function stepEvent(payload: Record<string, unknown>): CloudCpClientEvent {
	sequence++;
	return {
		sessionId: "s1", sequence, type: SCRIPT_STEP_EVENT, createdAt: "2026-10-18T10:00:00Z",
		payload: { phase: "setup", runId: "run-1", total: 3, repository: "", ...payload },
	};
}

function runOf(...payloads: Record<string, unknown>[]) {
	return groupScriptRuns(payloads.map(stepEvent))[0];
}

const steps = () => screen.getAllByTestId("script-step");
/** A step row's disclosure button (its body also holds a copy button). */
const toggleOf = (row: HTMLElement) => row.querySelector<HTMLButtonElement>("button[aria-expanded]");

beforeEach(() => {
	sequence = 0;
	vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
	vi.setSystemTime(new Date("2026-10-18T10:00:12Z"));
});
afterEach(() => vi.useRealTimers());

describe("ScriptRunPanel", () => {
	it("shows a running run with the live step open, its output and a live timer", () => {
		const run = runOf(
			{ step: 1, status: "succeeded", command: "npm ci", exitCode: 0, startedAt: "2026-10-18T10:00:00Z", durationMs: 4000, output: "added 120 packages" },
			{ step: 2, status: "running", command: "docker compose up -d", repository: "api", startedAt: "2026-10-18T10:00:04Z", output: "Pulling db\nPulling cache" },
		);
		const { container } = render(<ScriptRunPanel run={run} />);
		expect(screen.getByText("Setup")).toBeInTheDocument();
		expect(screen.getByText("Running step 2 of 3")).toBeInTheDocument();
		expect(screen.getByLabelText("Elapsed 0:12")).toBeInTheDocument();
		const [first, second, third] = steps();
		expect(first).toHaveAttribute("data-status", "succeeded");
		expect(toggleOf(first)).toHaveAttribute("aria-expanded", "false");
		const running = toggleOf(second)!;
		expect(running).toHaveAttribute("aria-expanded", "true");
		expect(document.getElementById(running.getAttribute("aria-controls")!)).not.toBeNull();
		expect(within(second).getByText("api")).toBeInTheDocument();
		expect(within(second).getByText("0:08")).toBeInTheDocument();
		expect(within(second).getByTestId("script-step-output")).toHaveTextContent("Pulling db Pulling cache");
		expect(third).toHaveAttribute("data-status", "pending");
		expect(toggleOf(third)).toBeNull();
		act(() => { vi.advanceTimersByTime(3000); });
		expect(within(second).getByText("0:11")).toBeInTheDocument();
		expect(container.querySelector("[data-testid=script-run-panel]")).toMatchSnapshot();
	});

	it("follows new output only while the reader is at the bottom", () => {
		const base = { step: 1, status: "running", command: "make", startedAt: "2026-10-18T10:00:00Z" };
		const { rerender } = render(<ScriptRunPanel run={runOf({ ...base, output: "a" })} />);
		const pre = screen.getByTestId("script-step-output");
		Object.defineProperty(pre, "scrollHeight", { configurable: true, get: () => 1000 });
		Object.defineProperty(pre, "clientHeight", { configurable: true, get: () => 240 });
		sequence = 0;
		rerender(<ScriptRunPanel run={runOf({ ...base, output: "a\nb" })} />);
		expect(pre.scrollTop).toBe(1000);
		pre.scrollTop = 100;
		fireEvent.scroll(pre);
		sequence = 0;
		rerender(<ScriptRunPanel run={runOf({ ...base, output: "a\nb\nc" })} />);
		expect(pre.scrollTop).toBe(100);
	});

	it("collapses a succeeded run in the timeline and keeps its steps closed when opened", () => {
		vi.setSystemTime(new Date("2026-10-18T11:00:00Z"));
		const run = groupScriptRuns([
			stepEvent({ total: 2, step: 1, status: "succeeded", command: "npm ci", exitCode: 0, startedAt: "2026-10-18T10:00:00Z", durationMs: 4000 }),
			stepEvent({ total: 2, step: 2, status: "succeeded", command: "npm run build", exitCode: 0, startedAt: "2026-10-18T10:00:04Z", durationMs: 8000 }),
		])[0];
		const { container } = render(<ScriptRunPanel collapsible run={run} />);
		const header = screen.getByRole("button", { name: /Setup/ });
		expect(header).toHaveAttribute("aria-expanded", "false");
		expect(screen.getByText("Succeeded")).toBeInTheDocument();
		expect(screen.getByLabelText("Elapsed 0:12")).toBeInTheDocument();
		expect(screen.queryAllByTestId("script-step")).toHaveLength(0);
		fireEvent.click(header);
		expect(header).toHaveAttribute("aria-expanded", "true");
		expect(steps()).toHaveLength(2);
		for (const row of steps()) expect(toggleOf(row)).toHaveAttribute("aria-expanded", "false");
		expect(container.querySelector("[data-testid=script-run-panel]")).toMatchSnapshot();
	});

	it("opens a failed step with its hint, who ran it and where, and dims the skipped steps", () => {
		const run = runOf(
			{ step: 1, status: "succeeded", command: "npm ci", exitCode: 0, startedAt: "2026-10-18T10:00:00Z", durationMs: 1000 },
			{
				step: 2, status: "failed", command: "docker compose up -d", exitCode: 1, startedAt: "2026-10-18T10:00:01Z", durationMs: 2000,
				output: "permission denied while trying to connect to the Docker daemon socket", outputTruncated: true,
				logPath: "/home/coder/.ao/setup-logs/step-2.log", user: "ao-worker", groups: ["ao-worker"], dir: "/home/coder/app",
				hint: "This step ran as ao-worker, which cannot use Docker.",
			},
			{ step: 3, status: "skipped", command: "npm test" },
		);
		const { container } = render(<ScriptRunPanel collapsible run={run} />);
		expect(screen.getByText("Failed at step 2")).toBeInTheDocument();
		const [, failed, skipped] = steps();
		expect(toggleOf(failed)).toHaveAttribute("aria-expanded", "true");
		expect(within(failed).getByText("This step ran as ao-worker, which cannot use Docker.")).toBeInTheDocument();
		expect(within(failed).getByText("Exited with code 1")).toBeInTheDocument();
		expect(within(failed).getByText("Ran as ao-worker (groups: ao-worker)")).toBeInTheDocument();
		expect(within(failed).getByText("Ran in /home/coder/app")).toBeInTheDocument();
		expect(within(failed).getByText("Showing the end of the output. Full output: /home/coder/.ao/setup-logs/step-2.log")).toBeInTheDocument();
		expect(within(failed).getByRole("button", { name: "Copy output" })).toBeInTheDocument();
		expect(skipped).toHaveAttribute("data-status", "skipped");
		expect(toggleOf(skipped)).toBeNull();
		expect(container.querySelector("[data-testid=script-run-panel]")).toMatchSnapshot();
	});

	it("respects a manual toggle as the run advances", () => {
		const first = { step: 1, command: "npm ci", startedAt: "2026-10-18T10:00:00Z" };
		const { rerender } = render(<ScriptRunPanel run={runOf({ ...first, status: "running", output: "x" })} />);
		const button = () => toggleOf(steps()[0])!;
		expect(button()).toHaveAttribute("aria-expanded", "true");
		// Finishing closes it by default...
		sequence = 0;
		rerender(<ScriptRunPanel run={runOf({ ...first, status: "succeeded", exitCode: 0, durationMs: 1000, output: "x" })} />);
		expect(button()).toHaveAttribute("aria-expanded", "false");
		// ...but a reader who opens it keeps it open.
		fireEvent.click(button());
		sequence = 0;
		rerender(<ScriptRunPanel run={runOf(
			{ ...first, status: "succeeded", exitCode: 0, durationMs: 1000, output: "x" },
			{ step: 2, command: "make", status: "running", startedAt: "2026-10-18T10:00:01Z" },
		)} />);
		expect(button()).toHaveAttribute("aria-expanded", "true");
		expect(within(steps()[1]).getByText("No output yet")).toBeInTheDocument();
	});

	it("toggles a step from the keyboard", () => {
		render(<ScriptRunPanel run={runOf({ total: 1, step: 1, status: "succeeded", command: "true", exitCode: 0, durationMs: 5 })} />);
		const button = toggleOf(steps()[0])!;
		button.focus();
		expect(document.activeElement).toBe(button);
		// Native buttons activate on Enter and Space; the browser turns either into a click.
		fireEvent.click(button);
		expect(button).toHaveAttribute("aria-expanded", "true");
		expect(screen.getByText("No output")).toBeInTheDocument();
	});

	it("draws an old Cleanup hook's steps without durations or a bogus truncation note", () => {
		const run = groupScriptRuns([
			stepEvent({ phase: "cleanup", runId: "c", total: 2, step: 1, status: "succeeded", exitCode: 0, command: "", output: "", outputTruncated: true, logPath: "/x/cleanup.log" }),
			stepEvent({ phase: "cleanup", runId: "c", total: 2, step: 2, status: "failed", exitCode: 1, command: "", output: "cleanup step 2 failed\nran as ao-worker", logPath: "/x/cleanup.log" }),
		])[0];
		render(<ScriptRunPanel run={run} />);
		expect(screen.getByText("Failed at step 2")).toBeInTheDocument();
		expect(screen.queryByLabelText(/Elapsed/)).toBeNull();
		const [first, second] = steps();
		expect(within(first).getByText("Step 1")).toBeInTheDocument();
		expect(within(first).queryByText(/\d:\d\d/)).toBeNull();
		fireEvent.click(toggleOf(first)!);
		expect(within(first).getByText("No output")).toBeInTheDocument();
		expect(within(first).getByText("Full output: /x/cleanup.log")).toBeInTheDocument();
		expect(within(first).queryByText(/Showing the end/)).toBeNull();
		expect(within(second).getByTestId("script-step-output")).toHaveTextContent("cleanup step 2 failed");
		expect(within(second).getByText("Exited with code 1")).toBeInTheDocument();
	});

	it("shows a cleanup run and an interrupted run truthfully", () => {
		const runs = groupScriptRuns([
			stepEvent({ runId: "old", total: 2, step: 1, status: "running", command: "npm ci", startedAt: "2026-10-18T09:00:00Z" }),
			stepEvent({ runId: "cleanup-1", phase: "cleanup", total: 1, step: 1, status: "succeeded", command: "docker compose down", exitCode: 0, startedAt: "2026-10-18T10:00:00Z", durationMs: 3000 }),
			stepEvent({ runId: "new", total: 2, step: 1, status: "running", command: "npm ci", startedAt: "2026-10-18T10:00:05Z" }),
		]);
		const { container, unmount } = render(<ScriptRunPanel run={runs[0]} />);
		expect(screen.getByText("Stopped before finishing")).toBeInTheDocument();
		expect(steps()[0]).toHaveAttribute("data-status", "stopped");
		expect(container.querySelector(".animate-spin")).toBeNull();
		unmount();
		render(<ScriptRunPanel run={runs[1]} />);
		expect(screen.getByText("Cleanup")).toBeInTheDocument();
		expect(screen.getByText("Succeeded", { selector: ".truncate" })).toBeInTheDocument();
		expect(screen.getByText("docker compose down")).toBeInTheDocument();
	});

	it("stops the spinner of a run that ended without reporting its last step", () => {
		const run = groupScriptRuns([
			stepEvent({ total: 2, step: 1, status: "running", command: "npm ci", startedAt: "2026-10-18T10:00:00Z", output: "added 10" }),
		], { sessionEnded: true })[0];
		const { container } = render(<ScriptRunPanel run={run} />);
		expect(screen.getByText("Ended without reporting a result")).toBeInTheDocument();
		const [first, second] = steps();
		expect(first).toHaveAttribute("data-status", "unknown");
		expect(within(first).getByText("Result unknown")).toBeInTheDocument();
		expect(second).toHaveAttribute("data-status", "pending");
		expect(container.querySelector(".animate-spin")).toBeNull();
		expect(screen.queryByLabelText(/Elapsed/)).toBeNull();
	});
});
