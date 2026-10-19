import { describe, expect, it } from "vitest";
import type { TriageRun } from "../../shared/triage";
import { triageConversationSnapshot, triageInputActivity, triageInputAnswer } from "./triage-conversation";

const run: TriageRun = { id: "report", version: 1, createdAt: "2026-10-19T10:00:00Z", updatedAt: "2026-10-19T10:01:00Z",
	request: { harness: "codex", model: "selected", effort: "high", publishAuthorized: true }, state: "running", activity: "Reading diagnostics",
	messages: [{ id: "human", role: "user", text: "Blank window", at: "2026-10-19T10:00:00Z" },
		{ id: "agent", role: "assistant", text: "Checking the startup report.", at: "2026-10-19T10:00:10Z" }], publication: { state: "" } };
const labels = { other: "Something else", unsure: "I'm not sure", details: "Details (optional)" };

describe("triage conversation adapter", () => {
	it("uses renderer-only identity and actual messages/activity without inventing native turns or capabilities", () => {
		const snapshot = triageConversationSnapshot(run);
		expect(snapshot).toMatchObject({ sessionId: "triage:report", conversationId: "triage:report", harness: "codex", mode: "chat", turns: [], capabilities: [],
			settings: { model: "selected", reasoningEffort: "high" }, controller: { state: "busy" } });
		expect(snapshot.items.map((item) => item.id)).toEqual(["human", "agent", "triage:report:activity"]);
		expect(snapshot.items[0]).toMatchObject({ role: "user", origin: "human", text: "Blank window", streaming: false });
		expect(snapshot.items[2]).toMatchObject({ activityKind: "system", status: "running", summary: run.activity });
	});

	it("renders engine notices as notices and saved reports without a stopped-controller failure", () => {
		const snapshot = triageConversationSnapshot({ ...run, state: "completed", messages: [{ id: "notice", role: "system", text: "Report saved", at: run.updatedAt }] });
		expect(snapshot.items[0]).toMatchObject({ kind: "activity", activityKind: "system", summary: "Report saved" });
		expect(snapshot.controller).toEqual({ state: "ready" });
	});

	it("shows publication links as an engine message and preserves the complete native screen as literal text", () => {
		const screen = "Continue?\n> Yes\n  No\n```example```";
		const snapshot = triageConversationSnapshot({ ...run, nativeControlsAvailable: true, pendingInput: { id: "input", origin: "native", questions: [{ id: "action", prompt: screen, options: ["Press Enter"] }] } }, "[View issue](https://github.com/OrchestratorInc/agent-orchestrator/issues/12)");
		expect(snapshot.items.at(-2)).toMatchObject({ kind: "message", role: "assistant", origin: "daemon", text: `\`\`\`\`text\n${screen}\n\`\`\`\`` });
		expect(snapshot.items.at(-1)).toMatchObject({ kind: "message", origin: "daemon", text: "[View issue](https://github.com/OrchestratorInc/agent-orchestrator/issues/12)" });
	});

	it("uses the shared dock's single/multiple schemas with no selected defaults and optional text", () => {
		const input = { ...run, pendingInput: { id: "input", questions: [
			{ id: "symptom", prompt: "What happened?", options: ["Blank window", "Slow"], multiple: false },
			{ id: "surfaces", prompt: "Where?", options: ["Chat", "Sidebar"], multiple: true },
		] } };
		const schema = triageInputActivity(input, labels).detail?.schema;
		expect(schema?.properties?.question_0).toMatchObject({ type: "string", title: "What happened?" });
		expect(schema?.properties?.question_1).toMatchObject({ type: "array" });
		expect(schema?.required).toEqual(["question_0", "question_1"]);
		expect(Object.values(schema?.properties ?? {}).every((property) => property.default === undefined)).toBe(true);
		expect(triageInputAnswer(input, { question_0: "Slow", question_1: ["Chat", "Sidebar"] })).toEqual({ text: "", answers: { symptom: ["Slow"], surfaces: ["Chat", "Sidebar"] } });
	});

	it("converts a UI uncertainty escape into ordinary text without an invented native enum", () => {
		const input = { ...run, pendingInput: { id: "native", origin: "native" as const, questions: [{ id: "frequency", prompt: "How often?", options: ["Always", "Sometimes"], allowText: false }] } };
		expect(triageInputAnswer(input, { question_0: "__ao_triage_unsure__" })).toEqual({ text: "How often?: I'm not sure" });
		expect(triageInputAnswer(input, { question_0: "__ao_triage_other__", question_0_custom: "After updating" })).toEqual({ text: "How often?: Something else\n\nAfter updating" });
	});

	it("maps optional terminal text to the native text slot while key-only input stays text-free", () => {
		const input = { ...run, nativeControlsAvailable: true, pendingInput: { id: "native", origin: "native" as const, questions: [
			{ id: "action", prompt: "Continue?\n> Yes\n No", options: ["Press Enter", "Send typed text", "Keep waiting"] }, { id: "text", prompt: "Text", allowText: true },
		] } };
		expect(triageInputActivity(input, labels).detail?.schema?.required).toEqual(["question_0"]);
		expect(triageInputActivity(input, labels).detail?.schema?.properties?.question_0?.oneOf).toEqual([
			{ const: "Press Enter", title: "Press Enter" }, { const: "Send typed text", title: "Send typed text" }, { const: "Keep waiting", title: "Keep waiting" },
		]);
		expect(triageInputAnswer(input, { question_0: "Press Enter" })).toEqual({ text: "", answers: { action: ["Press Enter"] } });
		expect(triageInputAnswer(input, { question_0: "Send typed text", question_0_custom: "chosen text" })).toEqual({ text: "", answers: { action: ["Send typed text"], text: ["chosen text"] } });
	});
});
