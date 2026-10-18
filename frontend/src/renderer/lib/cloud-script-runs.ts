import type { CloudCpClientEvent } from "./cloud-cp";

/**
 * Live Setup and Cleanup script logs (`workspace.script.step`).
 *
 * The worker publishes one event when a step starts, coalesced updates while it
 * runs (each carrying the latest output tail), one final event per step and,
 * after a failure, `skipped` for the steps that never ran. This module turns
 * those events into one view model per run; the panel renders it as-is.
 *
 * The payload type is declared here rather than read from the generated cloud
 * client schema so the renderer stays tolerant of fields added later.
 */
export const SCRIPT_STEP_EVENT = "workspace.script.step";

export type ScriptPhase = "setup" | "cleanup";
export type ScriptStepStatus = "running" | "succeeded" | "failed" | "timed_out" | "skipped";

/** The `workspace.script.step` payload, as the contract defines it. */
export interface ScriptStepEventPayload {
	phase: ScriptPhase;
	runId: string;
	/** 1-based. */
	step: number;
	total: number;
	/** One line, redacted, at most 300 bytes. */
	command: string;
	/** Folder of the repository the step ran in; "" is the main repository. */
	repository: string;
	status: ScriptStepStatus;
	exitCode?: number;
	startedAt?: string;
	durationMs?: number;
	/** Redacted, ANSI-stripped tail of combined stdout and stderr (at most 16 KiB). */
	output?: string;
	outputTruncated?: boolean;
	logPath?: string;
	user?: string;
	groups?: string[];
	dir?: string;
	hint?: string;
}

/** A step slot. `pending` is a step the run has not reached (no event yet). */
export interface ScriptRunStep {
	step: number;
	status: ScriptStepStatus | "pending";
	command: string;
	repository: string;
	exitCode?: number;
	startedAt?: string;
	durationMs?: number;
	output: string;
	outputTruncated: boolean;
	logPath?: string;
	user?: string;
	groups?: string[];
	dir?: string;
	hint?: string;
}

/**
 * `interrupted`: the run never finished and a later run of the same phase
 * started (a worker restart or a retry), so its running step will not end.
 */
export type ScriptRunStatus = "running" | "succeeded" | "failed" | "interrupted";

export interface ScriptRun {
	runId: string;
	phase: ScriptPhase;
	total: number;
	steps: ScriptRunStep[];
	status: ScriptRunStatus;
	/** The step that failed or timed out, when the run failed. */
	failedStep?: number;
	/** The step running now, while the run is running. */
	runningStep?: number;
	startedAt?: string;
	/** Wall time of a finished run, from its first step's start to its last step's end. */
	durationMs?: number;
	/** Sequence of the run's first event: where it sits in the session's history. */
	firstSequence: number;
	/** Sequence of its newest event: changes whenever the run does. */
	lastSequence: number;
	/** Number of events folded into the run; a cheap revision for memoized rows. */
	eventCount: number;
}

const statuses = new Set<ScriptStepStatus>(["running", "succeeded", "failed", "timed_out", "skipped"]);

const text = (value: unknown): string | undefined => (typeof value === "string" ? value : undefined);
const nonEmpty = (value: unknown): string | undefined => {
	const result = text(value)?.trim();
	return result ? result : undefined;
};
const integer = (value: unknown): number | undefined =>
	typeof value === "number" && Number.isFinite(value) ? Math.trunc(value) : undefined;

/** Reads a `workspace.script.step` payload, or undefined when it is not one. */
export function parseScriptStepPayload(payload: unknown): ScriptStepEventPayload | undefined {
	if (!payload || typeof payload !== "object") return undefined;
	const value = payload as Record<string, unknown>;
	const phase = value.phase === "setup" || value.phase === "cleanup" ? value.phase : undefined;
	const runId = nonEmpty(value.runId);
	const step = integer(value.step);
	const status = typeof value.status === "string" && statuses.has(value.status as ScriptStepStatus) ? value.status as ScriptStepStatus : undefined;
	if (!phase || !runId || step === undefined || step < 1 || !status) return undefined;
	const total = Math.max(integer(value.total) ?? step, step);
	const groups = Array.isArray(value.groups) ? value.groups.filter((group): group is string => typeof group === "string" && group !== "") : undefined;
	return {
		phase, runId, step, total, status,
		command: text(value.command) ?? "",
		repository: text(value.repository) ?? "",
		exitCode: integer(value.exitCode),
		startedAt: nonEmpty(value.startedAt),
		durationMs: integer(value.durationMs),
		output: text(value.output),
		outputTruncated: value.outputTruncated === true,
		logPath: nonEmpty(value.logPath),
		user: nonEmpty(value.user),
		groups: groups && groups.length > 0 ? groups : undefined,
		dir: nonEmpty(value.dir),
		hint: nonEmpty(value.hint),
	};
}

/** True for a step that has ended (any outcome but running or not started). */
export function scriptStepFinished(status: ScriptRunStep["status"]): boolean {
	return status === "succeeded" || status === "failed" || status === "timed_out" || status === "skipped";
}

function pendingStep(step: number): ScriptRunStep {
	return { step, status: "pending", command: "", repository: "", output: "", outputTruncated: false };
}

function timeOf(value: string | undefined): number | undefined {
	if (!value) return undefined;
	const parsed = Date.parse(value);
	return Number.isFinite(parsed) ? parsed : undefined;
}

/**
 * Groups `workspace.script.step` events by `runId`, oldest run first. The
 * newest event per (runId, step) wins: each carries the latest output tail, so
 * nothing is accumulated. Other event types are ignored, so callers can pass a
 * session's whole event list.
 */
export function groupScriptRuns(events: readonly CloudCpClientEvent[]): ScriptRun[] {
	const runs = new Map<string, {
		phase: ScriptPhase;
		total: number;
		latest: Map<number, { sequence: number; payload: ScriptStepEventPayload }>;
		firstSequence: number;
		lastSequence: number;
		eventCount: number;
		firstCreatedAt: string;
	}>();
	for (const event of events) {
		if (event.type !== SCRIPT_STEP_EVENT) continue;
		const payload = parseScriptStepPayload(event.payload);
		if (!payload) continue;
		let run = runs.get(payload.runId);
		if (!run) {
			run = { phase: payload.phase, total: 0, latest: new Map(), firstSequence: event.sequence, lastSequence: event.sequence, eventCount: 0, firstCreatedAt: event.createdAt };
			runs.set(payload.runId, run);
		}
		run.total = Math.max(run.total, payload.total);
		run.firstSequence = Math.min(run.firstSequence, event.sequence);
		run.lastSequence = Math.max(run.lastSequence, event.sequence);
		run.eventCount++;
		const previous = run.latest.get(payload.step);
		if (!previous || event.sequence >= previous.sequence) run.latest.set(payload.step, { sequence: event.sequence, payload });
	}
	const ordered = [...runs.entries()].sort((a, b) => a[1].firstSequence - b[1].firstSequence);
	return ordered.map(([runId, run], index) => {
		const steps: ScriptRunStep[] = [];
		for (let step = 1; step <= run.total; step++) {
			const payload = run.latest.get(step)?.payload;
			if (!payload) {
				steps.push(pendingStep(step));
				continue;
			}
			const finished = scriptStepFinished(payload.status);
			const failed = payload.status === "failed" || payload.status === "timed_out";
			steps.push({
				step,
				status: payload.status,
				command: payload.command,
				repository: payload.repository,
				exitCode: finished ? payload.exitCode : undefined,
				startedAt: payload.startedAt,
				durationMs: finished ? payload.durationMs : undefined,
				output: payload.output ?? "",
				outputTruncated: payload.outputTruncated === true,
				logPath: payload.logPath,
				user: failed ? payload.user : undefined,
				groups: failed ? payload.groups : undefined,
				dir: failed ? payload.dir : undefined,
				hint: failed ? payload.hint : undefined,
			});
		}
		const failedStep = steps.find((step) => step.status === "failed" || step.status === "timed_out")?.step;
		const allSucceeded = steps.length > 0 && steps.every((step) => step.status === "succeeded");
		const superseded = ordered.slice(index + 1).some(([, later]) => later.phase === run.phase);
		const status: ScriptRunStatus = failedStep !== undefined ? "failed"
			: allSucceeded ? "succeeded"
			: superseded ? "interrupted"
			: "running";
		const runningStep = status === "running"
			? (steps.find((step) => step.status === "running") ?? steps.find((step) => step.status === "pending"))?.step
			: undefined;
		const startedAt = steps.find((step) => step.startedAt)?.startedAt ?? run.firstCreatedAt;
		let durationMs: number | undefined;
		if (status === "succeeded" || status === "failed") {
			const start = timeOf(startedAt);
			const ends = steps.flatMap((step) => {
				const stepStart = timeOf(step.startedAt);
				return stepStart !== undefined && step.durationMs !== undefined ? [stepStart + step.durationMs] : [];
			});
			const timed = steps.filter((step) => step.durationMs !== undefined);
			// An old Cleanup hook reports no times at all: show none rather than 0:00.
			durationMs = start !== undefined && ends.length > 0
				? Math.max(0, Math.max(...ends) - start)
				: timed.length > 0 ? timed.reduce((sum, step) => sum + step.durationMs!, 0) : undefined;
		}
		return {
			runId, phase: run.phase, total: run.total, steps, status, failedStep, runningStep, startedAt, durationMs,
			firstSequence: run.firstSequence, lastSequence: run.lastSequence, eventCount: run.eventCount,
		};
	});
}

/** The newest run of a phase, if any. */
export function latestScriptRun(runs: readonly ScriptRun[], phase: ScriptPhase): ScriptRun | undefined {
	for (let index = runs.length - 1; index >= 0; index--) {
		if (runs[index].phase === phase) return runs[index];
	}
	return undefined;
}

/** Keeps only `workspace.script.step` events, so a small cache can hold them. */
export function scriptStepEvents(events: readonly CloudCpClientEvent[]): CloudCpClientEvent[] {
	return events.filter((event) => event.type === SCRIPT_STEP_EVENT);
}

/** Merges events into a sequence-ordered list without duplicates. */
export function mergeScriptStepEvents(existing: readonly CloudCpClientEvent[], incoming: readonly CloudCpClientEvent[]): CloudCpClientEvent[] {
	const fresh = scriptStepEvents(incoming);
	if (fresh.length === 0) return existing as CloudCpClientEvent[];
	const bySequence = new Map<number, CloudCpClientEvent>();
	for (const event of existing) bySequence.set(event.sequence, event);
	let changed = false;
	for (const event of fresh) {
		if (bySequence.has(event.sequence)) continue;
		bySequence.set(event.sequence, event);
		changed = true;
	}
	if (!changed) return existing as CloudCpClientEvent[];
	return [...bySequence.values()].sort((a, b) => a.sequence - b.sequence);
}

/** m:ss, or h:mm:ss past an hour, as GitHub Actions shows step times. */
export function formatScriptDuration(durationMs: number): string {
	const total = Math.max(0, Math.floor(durationMs / 1000));
	const hours = Math.floor(total / 3600);
	const minutes = Math.floor((total % 3600) / 60);
	const seconds = String(total % 60).padStart(2, "0");
	return hours > 0 ? `${hours}:${String(minutes).padStart(2, "0")}:${seconds}` : `${minutes}:${seconds}`;
}
