import { describe, expect, it } from "vitest";
import type { CloudCpClientEvent } from "./cloud-cp";
import {
	formatScriptDuration,
	groupScriptRuns,
	latestScriptRun,
	mergeScriptStepEvents,
	parseScriptStepPayload,
	SCRIPT_STEP_EVENT,
} from "./cloud-script-runs";

let sequence = 0;
function step(payload: Record<string, unknown>, overrides: Partial<CloudCpClientEvent> = {}): CloudCpClientEvent {
	sequence++;
	return {
		sessionId: "s1", sequence, type: SCRIPT_STEP_EVENT, createdAt: `2026-10-18T10:00:${String(sequence).padStart(2, "0")}Z`,
		payload: { phase: "setup", runId: "run-1", total: 3, command: "npm ci", repository: "", ...payload },
		...overrides,
	};
}

describe("parseScriptStepPayload", () => {
	it("rejects payloads missing the run, the step or a known status", () => {
		expect(parseScriptStepPayload(undefined)).toBeUndefined();
		expect(parseScriptStepPayload({ phase: "setup", step: 1, status: "running" })).toBeUndefined();
		expect(parseScriptStepPayload({ phase: "setup", runId: "r", step: 0, status: "running" })).toBeUndefined();
		expect(parseScriptStepPayload({ phase: "setup", runId: "r", step: 1, status: "exploded" })).toBeUndefined();
		expect(parseScriptStepPayload({ phase: "deploy", runId: "r", step: 1, status: "running" })).toBeUndefined();
	});

	it("never lets total fall below the step it describes", () => {
		expect(parseScriptStepPayload({ phase: "cleanup", runId: "r", step: 2, total: 1, status: "succeeded" })?.total).toBe(2);
	});
});

describe("groupScriptRuns", () => {
	it("ignores other event types and keeps the newest event per step", () => {
		sequence = 0;
		const events = [
			{ sessionId: "s1", sequence: 0, type: "workspace.setup", payload: { status: "succeeded" }, createdAt: "2026-10-18T10:00:00Z" },
			step({ step: 1, status: "running", startedAt: "2026-10-18T10:00:00Z", output: "" }),
			step({ step: 1, status: "running", startedAt: "2026-10-18T10:00:00Z", output: "added 10" }),
			step({ step: 1, status: "running", startedAt: "2026-10-18T10:00:00Z", output: "added 10\nadded 20" }),
		];
		const [run] = groupScriptRuns(events);
		expect(run.status).toBe("running");
		expect(run.runningStep).toBe(1);
		expect(run.steps.map((entry) => entry.status)).toEqual(["running", "pending", "pending"]);
		expect(run.steps[0].output).toBe("added 10\nadded 20");
		expect(run.eventCount).toBe(3);
	});

	it("is order independent: a late older event does not overwrite a newer one", () => {
		sequence = 0;
		const first = step({ step: 1, status: "running", output: "a" });
		const second = step({ step: 1, status: "succeeded", exitCode: 0, durationMs: 1000, output: "a\nb" });
		const [run] = groupScriptRuns([second, first]);
		expect(run.steps[0].status).toBe("succeeded");
		expect(run.steps[0].output).toBe("a\nb");
	});

	it("reports a succeeded run with its wall time", () => {
		sequence = 0;
		const [run] = groupScriptRuns([
			step({ total: 2, step: 1, status: "succeeded", exitCode: 0, startedAt: "2026-10-18T10:00:00Z", durationMs: 4000 }),
			step({ total: 2, step: 2, status: "succeeded", exitCode: 0, startedAt: "2026-10-18T10:00:04Z", durationMs: 8000 }),
		]);
		expect(run.status).toBe("succeeded");
		expect(run.durationMs).toBe(12_000);
		expect(run.runningStep).toBeUndefined();
	});

	it("reports a failed run, its failed step, the failure context and skipped steps", () => {
		sequence = 0;
		const [run] = groupScriptRuns([
			step({ step: 1, status: "succeeded", exitCode: 0, startedAt: "2026-10-18T10:00:00Z", durationMs: 1000, user: "ignored-on-success" }),
			step({
				step: 2, status: "failed", exitCode: 2, startedAt: "2026-10-18T10:00:01Z", durationMs: 2000, output: "denied",
				user: "ao-worker", groups: ["ao-worker", "docker"], dir: "/home/coder/app", hint: "No Docker", logPath: "/x/step-2.log",
			}),
			step({ step: 3, status: "skipped", command: "npm test" }),
		]);
		expect(run.status).toBe("failed");
		expect(run.failedStep).toBe(2);
		expect(run.durationMs).toBe(3000);
		expect(run.steps[0].user).toBeUndefined();
		expect(run.steps[1]).toMatchObject({ user: "ao-worker", groups: ["ao-worker", "docker"], hint: "No Docker", logPath: "/x/step-2.log" });
		expect(run.steps[2].status).toBe("skipped");
	});

	it("treats a timed-out step as a failure", () => {
		sequence = 0;
		const [run] = groupScriptRuns([step({ total: 1, step: 1, status: "timed_out", durationMs: 900_000 })]);
		expect(run.status).toBe("failed");
		expect(run.failedStep).toBe(1);
	});

	it("separates runs, orders them by first event and marks a superseded unfinished run interrupted", () => {
		sequence = 0;
		const runs = groupScriptRuns([
			step({ runId: "a", step: 1, status: "running" }),
			step({ runId: "c", phase: "cleanup", total: 1, step: 1, status: "succeeded", exitCode: 0, durationMs: 10 }),
			step({ runId: "b", step: 1, status: "running" }),
		]);
		expect(runs.map((run) => [run.runId, run.status])).toEqual([["a", "interrupted"], ["c", "succeeded"], ["b", "running"]]);
		expect(latestScriptRun(runs, "setup")?.runId).toBe("b");
		expect(latestScriptRun(runs, "cleanup")?.runId).toBe("c");
	});

	it("accepts a fast step that never sent running and an old cleanup hook with no times", () => {
		sequence = 0;
		const [fast] = groupScriptRuns([step({ total: 1, step: 1, status: "succeeded", exitCode: 0, durationMs: 40, startedAt: "2026-10-18T10:00:00Z" })]);
		expect(fast).toMatchObject({ status: "succeeded", durationMs: 40 });
		const [cleanup] = groupScriptRuns([
			step({ phase: "cleanup", runId: "c", total: 2, step: 1, status: "succeeded", exitCode: 0, command: "", output: "", outputTruncated: true }),
			step({ phase: "cleanup", runId: "c", total: 2, step: 2, status: "succeeded", exitCode: 0, command: "", output: "", outputTruncated: true }),
		]);
		expect(cleanup.status).toBe("succeeded");
		expect(cleanup.durationMs).toBeUndefined();
		expect(cleanup.steps[0]).toMatchObject({ command: "", startedAt: undefined, durationMs: undefined, outputTruncated: true });
	});

	it("handles an old hook's single cleanup event without markers", () => {
		sequence = 0;
		const [run] = groupScriptRuns([step({ phase: "cleanup", runId: "cleanup-1", step: 1, total: 1, status: "failed", exitCode: 1, output: "rm: busy" })]);
		expect(run).toMatchObject({ phase: "cleanup", status: "failed", failedStep: 1, total: 1 });
	});
});

describe("mergeScriptStepEvents", () => {
	it("keeps only step events, sorted, without duplicates, and returns the same list when nothing is new", () => {
		sequence = 0;
		const a = step({ step: 1, status: "running" });
		const b = step({ step: 1, status: "succeeded" });
		const other: CloudCpClientEvent = { sessionId: "s1", sequence: 99, type: "agent.ready", payload: {}, createdAt: "2026-10-18T10:00:00Z" };
		const merged = mergeScriptStepEvents([b], [a, other, b]);
		expect(merged.map((event) => event.sequence)).toEqual([a.sequence, b.sequence]);
		expect(mergeScriptStepEvents(merged, [a])).toBe(merged);
	});
});

describe("formatScriptDuration", () => {
	it("formats like a step timer", () => {
		expect(formatScriptDuration(0)).toBe("0:00");
		expect(formatScriptDuration(12_400)).toBe("0:12");
		expect(formatScriptDuration(65_000)).toBe("1:05");
		expect(formatScriptDuration(3_725_000)).toBe("1:02:05");
	});
});
