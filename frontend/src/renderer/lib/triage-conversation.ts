import type { TriageRun } from "../../shared/triage";
import type { ConversationActivity, ConversationItem, ConversationSnapshot } from "../types/conversation";
import { sanitizeReportText } from "./report-problem";
import { isTriageActive } from "./triage";

/** A renderer view model only. This identity never enters normal session APIs. */
export function triageConversationSnapshot(run: TriageRun, result = ""): ConversationSnapshot {
	const identity = `triage:${run.id}`;
	const items: ConversationItem[] = run.messages.map((message, index) => message.role === "user" || message.role === "assistant" ? {
		kind: "message", id: message.id, sequence: index + 1, revision: 1,
		role: message.role, origin: message.role === "user" ? "human" : "provider",
		text: sanitizeReportText(message.text), streaming: false, createdAt: message.at,
	} : {
		kind: "activity", id: message.id, sequence: index + 1, revision: 1,
		activityKind: "system", status: "completed", summary: sanitizeReportText(message.text), createdAt: message.at,
	});
	if (run.activity) items.push({ kind: "activity", id: `${identity}:activity`, sequence: items.length + 1, revision: 1,
		activityKind: "system", status: isTriageActive(run) && run.state !== "needs_input" ? "running" : "completed",
		summary: sanitizeReportText(run.activity), createdAt: run.updatedAt });
	if (run.nativeControlsAvailable && run.pendingInput?.origin === "native") {
		const screen = run.pendingInput.questions.filter((question) => question.options?.length).map((question) => sanitizeReportText(question.prompt)).join("\n\n");
		const fence = "`".repeat(Math.max(2, ...Array.from(screen.matchAll(/`+/g), (match) => match[0].length)) + 1);
		items.push({ kind: "message", id: `${identity}:${run.pendingInput.id}:screen`, sequence: items.length + 1, revision: 1,
			role: "assistant", origin: "daemon", text: `${fence}text\n${screen}\n${fence}`, streaming: false, createdAt: run.updatedAt });
	}
	if (result) items.push({ kind: "message", id: `${identity}:result`, sequence: items.length + 1, revision: 1,
		role: "assistant", origin: "daemon", text: result, streaming: false, createdAt: run.updatedAt });
	return { conversationId: identity, sessionId: identity, harness: run.request.harness, title: "Report a problem", mode: "chat",
		controller: { state: isTriageActive(run) ? "busy" : "ready" }, turns: [], items,
		latestSequence: items.at(-1)?.sequence ?? 0, oldestSequence: items[0]?.sequence ?? 0, hasMoreBefore: false,
		settings: { model: run.request.model, reasoningEffort: run.request.effort }, capabilities: [] };
}

type Question = NonNullable<TriageRun["pendingInput"]>["questions"][number];
const other = (value: string) => /^(?:something else|other)(?:\b|$)/i.test(value);
const unsure = (value: string) => /^(?:i(?:'|’)m not sure|not sure|unsure)(?:\b|$)/i.test(value);
function escapeValue(question: Question, kind: "other" | "unsure"): string {
	let value = `__ao_triage_${kind}__`;
	while (question.options?.includes(value)) value += "_";
	return value;
}
function questions(run: TriageRun): Question[] {
	const supplied = run.pendingInput?.questions ?? [];
	return supplied.length ? supplied.filter((question) => !(question.id === "text" && !question.options?.length && supplied.some((item) => item.options?.length)))
		: [{ id: "_reply", prompt: "What would you like to add?", allowText: true }];
}

/** Adapt exact provider enums to the same question dock used by ordinary Chat. */
export function triageInputActivity(run: TriageRun, labels: { other: string; unsure: string; details: string }): ConversationActivity {
	const properties: Record<string, Record<string, unknown>> = {};
	const required: string[] = [];
	questions(run).forEach((question, index) => {
		const options = (question.options ?? []).map((value) => ({ const: value, title: sanitizeReportText(value) }));
		if (!(run.nativeControlsAvailable && run.pendingInput?.origin === "native")) {
			if (!question.options?.some(other)) options.push({ const: escapeValue(question, "other"), title: labels.other });
			if (!question.options?.some(unsure)) options.push({ const: escapeValue(question, "unsure"), title: labels.unsure });
		}
		const key = `question_${index}`;
		properties[key] = { title: sanitizeReportText(question.prompt), type: question.multiple ? "array" : "string", oneOf: options };
		properties[`${key}_custom`] = { title: labels.details, type: "string" };
		required.push(key);
	});
	return { kind: "activity", id: `triage:${run.id}:input`, sequence: run.messages.length + 1, revision: 1,
		activityKind: "user_input", status: "pending", summary: "Agent question", requestId: run.pendingInput?.id ?? `triage:${run.id}:reply`, createdAt: run.updatedAt,
		detail: { inputMode: "form", schema: { type: "object", properties, required } } };
}

/** UI escape values are converted to ordinary human text, never native enums. */
export function triageInputAnswer(run: TriageRun, content: Record<string, unknown>): { text: string; answers?: Record<string, string[]> } {
	const answers: Record<string, string[]> = {};
	const text: string[] = [];
	questions(run).forEach((question, index) => {
		const raw = content[`question_${index}`];
		const selected = (Array.isArray(raw) ? raw : [raw]).filter((value): value is string => typeof value === "string");
		const notes = content[`question_${index}_custom`];
		const detail = typeof notes === "string" ? notes.trim() : "";
		const escaped = selected.some((value) => value === escapeValue(question, "other") || value === escapeValue(question, "unsure"));
		for (const value of selected) {
			if (value === escapeValue(question, "other")) text.push(`${question.prompt}: Something else`);
			else if (value === escapeValue(question, "unsure")) text.push(`${question.prompt}: I'm not sure`);
			else if (question.options?.includes(value)) (answers[question.id] ??= []).push(value);
		}
		if (detail) {
			const nativeTextSlot = run.pendingInput?.origin === "native" && run.pendingInput.questions.some((item) => item.id === "text" && !item.options?.length);
			if (nativeTextSlot && !escaped) answers.text = [detail];
			else text.push(detail);
		}
	});
	return { text: text.join("\n\n"), ...(Object.keys(answers).length ? { answers } : {}) };
}
