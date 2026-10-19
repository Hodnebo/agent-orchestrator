import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it } from "vitest";
import type { TriageChoice, TriageRun } from "../../shared/triage";
import { STANDALONE_WORKSPACE_ID, type WorkspaceSession } from "../types/workspace";
import { rememberTaskComposerPreference } from "./task-composer-preferences";
import { cachedTriageOrigin, configuredTriageSnapshot, defaultTriageSelection, isTriageActive, mergeAgentRows, triageDiagnosticPrompt, triageManualIssueUrl, triagePublicUrl, triageReportText, validTriageSelection } from "./triage";

const choice: TriageChoice = { harness: "codex", label: "Codex", available: true, updatedAt: "2026-10-19T10:00:00Z",
	models: [{ id: "gpt-5.4", label: "GPT 5.4", default: true, efforts: ["low", "high"] }], accounts: [{ id: "work", label: "Work", default: true }] };
const ready = (id: string, state = "authorized") => ({ id, label: id, authentication: { state, checkedAt: "2026-10-19T10:00:00Z" }, installation: { state: "installed" }, effectiveReadiness: "ready", usageCount: 1 });

beforeEach(() => localStorage.clear());

describe("triage configured choices", () => {
	it("projects only configured harnesses and enabled models from configured providers", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], { harnessDefaults: { codex: { disabledModels: ["hidden"] } } });
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex"), ready("unconfigured", "unknown")] });
		cache.setQueryData(["agent-models", "codex", ""], { fetchedAt: choice.updatedAt, configuredProviders: ["openai"], models: [
			{ id: "gpt-5.4", label: "GPT 5.4", provider: "openai", isDefault: true },
			{ id: "hidden", label: "Hidden", provider: "openai" }, { id: "public", label: "Public catalog", provider: "other" },
		] });
		expect(configuredTriageSnapshot(cache, [choice])).toEqual([{ ...choice, label: "codex", source: "configured-snapshot", models: [{ id: "gpt-5.4", label: "GPT 5.4", default: true }] }]);
	});

	it("does not replace native configuration before settings and readiness load", () => {
		const cache = new QueryClient();
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex")] });
		expect(configuredTriageSnapshot(cache, [choice])).toBeUndefined();
		cache.setQueryData(["settings"], {});
		expect(configuredTriageSnapshot(cache, [])).toEqual([expect.objectContaining({ harness: "codex", models: [], modelsMissing: true, available: false,
			unavailableReason: "Configured model information is not cached. Refresh this agent's models in AO." })]);
	});

	it("retains native Codex and an independently configured uncached agent", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex"), ready("command-code", "configured"), ready("unconfigured", "unknown")] });
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "unconfigured", { model: "not-configured", mode: "" });
		const snapshot = configuredTriageSnapshot(cache, [choice]);
		expect(snapshot?.map((item) => item.harness)).toEqual(["codex", "command-code"]);
		expect(snapshot?.[0]).toMatchObject({ models: choice.models, accounts: choice.accounts, available: true });
		expect(snapshot?.[0].modelsMissing).not.toBe(true);
		expect(snapshot?.[1]).toMatchObject({ models: [], modelsMissing: true, available: false, unavailableReason: expect.stringContaining("not cached") });
	});

	it.each([undefined, false, true])("distinguishes prior authoritative empty models from a saved missing marker %s", (modelsMissing) => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex")] });
		const snapshot = configuredTriageSnapshot(cache, [{ ...choice, models: [], modelsMissing }]);
		expect(snapshot?.[0].modelsMissing === true).toBe(modelsMissing === true);
	});

	it.each(["configured-model", ""])("reuses the explicit saved composer model %j without a catalog", (model) => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [ready("command-code", "configured")] });
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "command-code", { model, mode: "", effort: "high" });
		const snapshot = configuredTriageSnapshot(cache, []);
		expect(snapshot?.[0]).toMatchObject({ harness: "command-code", models: [{ id: model, label: model || "Configured default", efforts: ["high"] }] });
		expect(defaultTriageSelection(cache, snapshot ?? [], null, {})).toEqual({ harness: "command-code", model, effort: "high" });
	});

	it("reuses an explicitly cached project worker model without inventing one", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [ready("command-code", "configured")] });
		cache.setQueryData(["project", "local-project"], { agent: "other", config: { worker: { agent: "command-code", agentConfig: { model: "project-model", effort: "low" } } } });
		cache.setQueryData(["project", "remote-host", "remote-project"], { agent: "command-code", config: { agentConfig: { model: "remote-model" } } });
		expect(configuredTriageSnapshot(cache, [])?.[0].models).toEqual([{ id: "project-model", label: "project-model", efforts: ["low"] }]);
	});

	it("reuses an already cached local project catalog with recent session models", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [ready("command-code", "configured")] });
		cache.setQueryData(["agent-models", "command-code", "local-project"], { agentId: "command-code", fetchedAt: choice.updatedAt,
			models: [{ id: "recent-model", label: "Recent model", lastUsedAt: choice.updatedAt }] });
		expect(configuredTriageSnapshot(cache, [])?.[0].models).toEqual([{ id: "recent-model", label: "Recent model" }]);
	});

	it.each(["empty", "disabled"])("keeps a loaded %s catalog unavailable despite saved selections", (kind) => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], { harnessDefaults: { codex: { disabledModels: kind === "disabled" ? ["gpt-5.4"] : [] } } });
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex")] });
		cache.setQueryData(["agent-models", "codex", ""], { fetchedAt: choice.updatedAt, models: kind === "disabled" ? [{ id: "gpt-5.4", label: "GPT 5.4" }] : [] });
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "codex", { model: "gpt-5.4", mode: "" });
		const snapshot = configuredTriageSnapshot(cache, [choice]);
		expect(snapshot?.[0]).toMatchObject({ models: [], available: false, unavailableReason: "No enabled configured models are available for this agent." });
		expect(snapshot?.[0].modelsMissing).not.toBe(true);
		expect(validTriageSelection(snapshot ?? [], { harness: "codex", model: "gpt-5.4", accountId: "work" })).toBe(false);
	});

	it("does not use an unresolved native default to bypass disabled model settings", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], { harnessDefaults: { codex: { disabledModels: ["gpt-5.4"] } } });
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex")] });
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "codex", { model: "", mode: "" });
		expect(configuredTriageSnapshot(cache, [{ ...choice, models: [{ id: "", label: "Configured default" }] }])?.[0]).toMatchObject({ models: [], available: false });
		expect(configuredTriageSnapshot(cache, [{ ...choice, models: [], modelsMissing: true }])?.[0].modelsMissing).not.toBe(true);
	});

	it.each(["unknown", "absent"])("retains last-known saved configuration when readiness is %s", (state) => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: state === "absent" ? [] : [ready("codex", state)] });
		expect(configuredTriageSnapshot(cache, [{ ...choice, source: "configured-snapshot" }])?.[0]).toMatchObject({ harness: "codex", models: choice.models, accounts: choice.accounts });
	});

	it("drops saved configuration only on explicit unauthorized readiness", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex", "unauthorized")] });
		expect(configuredTriageSnapshot(cache, [{ ...choice, source: "configured-snapshot" }])).toEqual([]);
	});

	it("retains native-discovered configuration without readiness data but respects explicit sign-out", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [] });
		const native = [{ ...choice, source: "native-configuration" }];
		expect(configuredTriageSnapshot(cache, native)?.[0].models).toEqual(choice.models);
		cache.setQueryData(["agent-readiness"], { agents: [{ ...ready("codex", "unauthorized"), authentication: { state: "unauthorized", account: { label: "Old account" } } }] });
		expect(configuredTriageSnapshot(cache, native)).toEqual([]);
	});

	it("does not treat an empty model list as an eligible native default", () => {
		expect(validTriageSelection([{ ...choice, models: [] }], { harness: "codex", accountId: "work" })).toBe(false);
		expect(validTriageSelection([{ ...choice, models: [{ id: "", label: "Configured default", default: true }] }], { harness: "codex", accountId: "work" })).toBe(true);
	});

	it("keeps native configured rows when readiness is unknown and still honors disabled models", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], { harnessDefaults: { codex: { disabledModels: ["gpt-5.4"] } } });
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex", "unknown")] });
		const snapshot = configuredTriageSnapshot(cache, [choice]);
		expect(snapshot).toHaveLength(1);
		expect(snapshot?.[0]).toMatchObject({ harness: "codex", label: "Codex", models: [], accounts: choice.accounts });
	});
});

describe("triage cached accounts", () => {
	it("projects exact signed-in provider accounts and preselects the actual new default", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex"), ready("claude-code")] });
		cache.setQueryData(["agent-accounts"], [
			{ id: "personal", provider: "codex", signedIn: true, default: false, email: "personal@example.com", limits: { token: "private-usage" } },
			{ id: "new-work", provider: "codex", signedIn: true, default: true, email: "work@example.com", credential: "private-credential" },
			{ id: "signed-out", provider: "codex", signedIn: false, default: false, email: "old@example.com" },
			{ id: "claude-work", provider: "claude", signedIn: true, default: true, email: "claude@example.com" },
		]);
		const snapshot = configuredTriageSnapshot(cache, [choice, { ...choice, harness: "claude-code" }]) ?? [];
		expect(snapshot[0].accounts).toEqual([
			{ id: "personal", label: "personal@example.com", default: false }, { id: "new-work", label: "work@example.com", default: true },
		]);
		expect(snapshot[1].accounts).toEqual([{ id: "claude-work", label: "claude@example.com", default: true }]);
		const selection = defaultTriageSelection(cache, snapshot, null, {}, "codex");
		expect(selection).toEqual({ harness: "codex", model: "gpt-5.4", accountId: "new-work" });
		expect(validTriageSelection(snapshot, selection)).toBe(true);
		expect(defaultTriageSelection(cache, snapshot, null, {}, "claude-code")?.accountId).toBe("claude-work");
		expect(JSON.stringify(snapshot)).not.toContain("private-credential");
		expect(JSON.stringify(snapshot)).not.toContain("private-usage");
	});

	it("preserves native accounts when the local cache is absent and ignores remote metadata", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex")] });
		cache.setQueryData(["agent-accounts", "remote-host"], [{ id: "remote", provider: "codex", signedIn: true, default: true }]);
		const snapshot = configuredTriageSnapshot(cache, [choice]) ?? [];
		expect(snapshot[0].accounts).toEqual(choice.accounts);
		expect(defaultTriageSelection(cache, snapshot, null, {})?.accountId).toBe("work");
	});

	it.each(["empty", "signed-out"])("clears old native accounts when the loaded local cache is %s", (state) => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex")] });
		cache.setQueryData(["agent-accounts"], state === "empty" ? [] : [{ id: "work", provider: "codex", signedIn: false, default: true }]);
		const snapshot = configuredTriageSnapshot(cache, [choice]) ?? [];
		expect(snapshot[0]).toMatchObject({ accounts: [], available: false, unavailableReason: "No signed-in configured accounts are available for this agent." });
		const selection = defaultTriageSelection(cache, snapshot, null, {});
		expect(selection).not.toHaveProperty("accountId");
		expect(validTriageSelection(snapshot, selection)).toBe(false);
	});

	it("does not replace a signed-out default with a different billing account", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], {});
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex")] });
		cache.setQueryData(["agent-accounts"], [
			{ id: "work", provider: "codex", signedIn: false, default: true },
			{ id: "other", provider: "codex", signedIn: true, default: false, email: "/Users/private/account" },
		]);
		const snapshot = configuredTriageSnapshot(cache, [choice]) ?? [];
		expect(snapshot[0].accounts).toEqual([{ id: "other", label: "Codex account", default: false }]);
		const selection = defaultTriageSelection(cache, snapshot, null, {});
		expect(selection).not.toHaveProperty("accountId");
		expect(validTriageSelection(snapshot, selection)).toBe(false);
		const saved = { harness: "codex", model: "gpt-5.4", accountId: "work" };
		expect(defaultTriageSelection(cache, snapshot, saved, {})).toEqual({ harness: "codex", model: "gpt-5.4" });
		expect(validTriageSelection(snapshot, saved)).toBe(false);
	});
});

describe("one click default resolution", () => {
	it("uses the affected local project's configured agent mode and permissions, independently of the session interface mode", () => {
		const cache = new QueryClient();
		cache.setQueryData(["workspaces"], [{ id: "affected-project", sessions: [{ id: "affected-session", mode: "tui" }] }]);
		cache.setQueryData(["project", "affected-project"], { agent: "amp", config: { worker: { agent: "amp", agentConfig: { model: "smart", mode: "smart", permissions: "default" } } } });
		const choices = [{ ...choice, harness: "amp", models: [{ id: "smart", label: "Smart", default: true }], accounts: undefined }];
		expect(defaultTriageSelection(cache, choices, null, { sessionId: "affected-session" })).toEqual({ harness: "amp", model: "smart", mode: "smart", permissions: "default" });
	});

	it("preserves native configured mode and permissions without granting a new policy", () => {
		const selection = defaultTriageSelection(new QueryClient(), [{ ...choice, mode: "configured-mode", permissions: "plan" }], null, {});
		expect(selection).toEqual({ harness: "codex", model: "gpt-5.4", accountId: "work", mode: "configured-mode", permissions: "plan" });
	});

	it("inherits existing native settings for an older saved selection without replacing its model or account", () => {
		const saved = { harness: "codex", model: "gpt-5.4", accountId: "work" };
		expect(defaultTriageSelection(new QueryClient(), [{ ...choice, mode: "configured-mode", permissions: "plan" }], saved, {}))
			.toEqual({ ...saved, mode: "configured-mode", permissions: "plan" });
	});

	it("does not inherit a coincident local project's permission policy for a remote report", () => {
		const cache = new QueryClient();
		cache.setQueryData(["workspaces"], [{ id: "local-project", sessions: [{ id: "same-id" }] }]);
		cache.setQueryData(["project", "local-project"], { agent: "codex", config: { worker: { agent: "codex", agentConfig: { permissions: "bypass-permissions" } } } });
		expect(defaultTriageSelection(cache, [choice], null, { sessionId: "same-id", hostId: "remote-host" }))
			.toEqual({ harness: "codex", model: "gpt-5.4", accountId: "work" });
	});
	it("does not preselect an unconfigured remembered or ready harness", () => {
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "removed", { model: "old", mode: "" });
		const cache=new QueryClient();
		cache.setQueryData(["agent-readiness"],{agents:[ready("removed")]});
		expect(defaultTriageSelection(cache,[choice],null,{})).toEqual({harness:"codex",model:"gpt-5.4",accountId:"work"});
	});
	it("keeps an unavailable saved harness visible without retaining its stale hidden subchoices", () => {
		const selection = { harness: "removed", model: "old", accountId: "paid" };
		expect(defaultTriageSelection(new QueryClient(), [choice], selection, {})).toEqual({ harness: "removed" });
		expect(validTriageSelection([choice], selection)).toBe(false);
	});

	it("refreshes disabled saved models, stale composer memory and a replaced account to current same-harness defaults", () => {
		const cache = new QueryClient();
		cache.setQueryData(["settings"], { harnessDefaults: { codex: { disabledModels: ["retired"] } } });
		cache.setQueryData(["agent-readiness"], { agents: [ready("codex")] });
		cache.setQueryData(["agent-models", "codex", ""], { fetchedAt: choice.updatedAt, models: [
			{ id: "retired", label: "Retired", lastUsedAt: choice.updatedAt, efforts: ["high"] },
			{ id: "current", label: "Current", isDefault: true, efforts: ["medium", "high"], defaultEffort: "medium" },
		] });
		cache.setQueryData(["agent-accounts"], [{ id: "replacement", provider: "codex", signedIn: true, default: true }]);
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "codex", { model: "retired", mode: "", effort: "high" });
		const choices = configuredTriageSnapshot(cache, [choice]) ?? [];
		const saved = { harness: "codex", model: "retired", effort: "high", accountId: "removed-account" };
		const expected = { harness: "codex", model: "current", effort: "medium", accountId: "replacement" };
		expect(defaultTriageSelection(cache, choices, saved, {})).toEqual(expected);
		expect(defaultTriageSelection(cache, choices, saved, {}, "codex")).toEqual(expected);
		expect(validTriageSelection(choices, expected)).toBe(true);
	});

	it("drops an unsupported remembered effort and uses the selected model's current explicit default", () => {
		const cache = new QueryClient();
		cache.setQueryData(["agent-models", "codex", ""], { models: [{ id: "gpt-5.4", isDefault: true, efforts: ["low", "high"], defaultEffort: "low" }] });
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "codex", { model: "gpt-5.4", mode: "", effort: "removed-level" });
		expect(defaultTriageSelection(cache, [choice], { harness: "codex", effort: "removed-level" }, {}))
			.toEqual({ harness: "codex", model: "gpt-5.4", effort: "low", accountId: "work" });
	});

	it("retains a supported configured effort when the project inherits its model default", () => {
		const cache = new QueryClient();
		cache.setQueryData(["workspaces"], [{ id: "project", sessions: [{ id: "affected" }] }]);
		cache.setQueryData(["project", "project"], { config: { worker: { agent: "codex", agentConfig: { effort: "high" } } } });
		expect(defaultTriageSelection(cache, [choice], null, { sessionId: "affected" }))
			.toEqual({ harness: "codex", model: "gpt-5.4", effort: "high", accountId: "work" });
	});

	it("uses normal composer harness, model, effort and the actual default account", () => {
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "codex", { model: "gpt-5.4", mode: "", effort: "high" });
		const choices = [{ ...choice, accounts: [{ id: "personal", label: "Personal" }, ...choice.accounts!] }];
		expect(defaultTriageSelection(new QueryClient(), choices, null, {})).toEqual({ harness: "codex", model: "gpt-5.4", effort: "high", accountId: "work" });
	});

	it("keeps a configured native default and never picks an arbitrary billing account", () => {
		const choices = [{ ...choice, models: [{ id: "", label: "Native", default: true }], accounts: [{ id: "a", label: "A" }, { id: "b", label: "B" }] }];
		const selection = defaultTriageSelection(new QueryClient(), choices, null, {});
		expect(selection).toEqual({ harness: "codex", model: "" });
		expect(validTriageSelection(choices, selection)).toBe(false);
	});

	it("honors an explicit agent change instead of restoring the composer's remembered harness", () => {
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "removed", { model: "old", mode: "" });
		expect(defaultTriageSelection(new QueryClient(), [choice], null, {}, "codex")).toEqual({ harness: "codex", model: "gpt-5.4", accountId: "work" });
	});

	it("preserves a configured unavailable default instead of substituting an available Claude agent", () => {
		rememberTaskComposerPreference(STANDALONE_WORKSPACE_ID, "codex", { model: "gpt-5.4", mode: "", effort: "high" });
		const choices = [{ ...choice, harness: "claude-code", label: "Claude Code" }, { ...choice, available: false, unavailableReason: "Transport unavailable" }];
		const selection = defaultTriageSelection(new QueryClient(), choices, null, {});
		expect(selection).toEqual({ harness: "codex", model: "gpt-5.4", effort: "high", accountId: "work" });
		expect(validTriageSelection(choices, selection)).toBe(false);
	});
});

it("prepares a manual prompt from selected desktop diagnostics without changing providers", () => {
	const prompt = triageDiagnosticPrompt({ harness: "codex", model: "gpt-5.4", effort: "high", accountId: "work", mode: "configured-mode", permissions: "plan" },
		"AO 0.13.0\nDaemon: error\nOPENAI_API_KEY=sk-private\n/Users/person/project", "Blank after updating", "Transport unavailable");
	expect(prompt).toContain("Agent: codex");
	expect(prompt).toContain("Effort: high");
	expect(prompt).toContain("Account: work");
	expect(prompt).toContain("Mode: configured-mode");
	expect(prompt).toContain("Permissions: plan");
	expect(prompt).toContain("Blank after updating");
	expect(prompt).toContain("Daemon: error");
	expect(prompt).not.toContain("sk-private");
	expect(prompt).not.toContain("/Users/person/project");
});

it("does not create a manual diagnostic prompt for a running investigation", () => {
	const active: TriageRun = { id: "active", version: 1, createdAt: "2026-10-19T10:00:00Z", updatedAt: "2026-10-19T10:00:00Z",
		request: { harness: "codex", publishAuthorized: true }, state: "running", activity: "Investigating", messages: [], publication: { state: "" } };
	expect(triageReportText(active)).toBe("");
	const failed = { ...active, state: "failed", publication: { state: "", title: "Partial report title" }, evidence: [
		{ id: "diagnostic", kind: "daemon", source: "desktop", status: "collected", collectedAt: active.updatedAt, text: "Daemon exited with code 1" },
	] };
	expect(triageReportText(failed)).toContain("Agent: codex");
	expect(triageReportText(failed)).toContain("Prepared title: Partial report title");
	expect(triageReportText(failed)).toContain("Daemon exited with code 1");
});

it("separates helper runs from session actions and prepares the canonical manual issue", () => {
	const session = { id: "session" } as WorkspaceSession;
	const run = { id: "support" } as TriageRun;
	const rows = mergeAgentRows([session], [run]);
	const first = rows[0];
	expect(first.kind).toBe("triage");
	expect(first).not.toHaveProperty("session");
	expect(rows[1]).toEqual({ kind: "session", id: "session", session });
	const url = new URL(triageManualIssueUrl("Failure", "token=sk-hidden /Users/private/project"));
	expect(url.pathname).toBe("/OrchestratorInc/agent-orchestrator/issues/new");
	expect(url.searchParams.get("body")).not.toContain("/Users/private/project");
});

it("keeps collection-only preparations out of active investigations and normal agent rows", () => {
	const preparation = { id: "preparation", preparation: true, state: "collecting" } as TriageRun;
	const claimed = { ...preparation, preparation: false };
	const session = { id: "session" } as WorkspaceSession;
	expect(isTriageActive(preparation)).toBe(false);
	expect(mergeAgentRows([session], [preparation])).toEqual([{ kind: "session", id: "session", session }]);
	expect(isTriageActive(claimed)).toBe(true);
	expect(mergeAgentRows([], [claimed])).toEqual([{ kind: "triage", id: "triage:preparation", run: claimed }]);
});

it.each([undefined, "ordinary-session"])("leaves normal session launches to the ordinary session list, including an uncertain result %s", (sessionId) => {
	const run = { id: "handoff", execution: "session", sessionId, state: "needs_input" } as TriageRun;
	const session = { id: "ordinary-session" } as WorkspaceSession;
	expect(isTriageActive(run)).toBe(false);
	expect(mergeAgentRows([session], [run])).toEqual([{ kind: "session", id: session.id, session }]);
});

it("opens matching canonical pull requests as well as issues without accepting another repository", () => {
	expect(triagePublicUrl("https://github.com/OrchestratorInc/agent-orchestrator/pull/42")).toBe("https://github.com/OrchestratorInc/agent-orchestrator/pull/42");
	expect(triagePublicUrl("https://github.com/other/repository/pull/42")).toBeUndefined();
});

it.each(["collecting", "starting", "dispatching", "running", "needs_input", "publishing"])("keeps backend %s runs active", (state) => {
	expect(isTriageActive({ state } as TriageRun)).toBe(true);
});

it("adds affected session and machine data only from existing cache", () => {
	const cache = new QueryClient();
	const session = { id: "affected", title: "Stopped agent", provider: "codex" };
	cache.setQueryData(["workspaces"], [{ sessions: [session, { id: "unrelated" }] }]);
	cache.setQueryData(["session-memory", "all"], { system: { availableBytes: 100 }, app: { rssBytes: 20 }, sessions: [{ id: "unrelated" }], fetchedAt: "2026-10-19T10:00:00Z" });
	expect(cachedTriageOrigin(cache, { sessionId: "affected" })).toEqual({ sessionId: "affected", session,
		machine: { system: { availableBytes: 100 }, app: { rssBytes: 20 }, fetchedAt: "2026-10-19T10:00:00Z" } });
	expect(cachedTriageOrigin(new QueryClient(), { sessionId: "missing" })).toEqual({ sessionId: "missing" });
});

it("propagates the affected remote session owner and does not attach a coincident local session", () => {
	const cache = new QueryClient();
	const remote = { id: "shared-id", hostId: "remote-host", title: "Remote agent" };
	cache.setQueryData(["workspaces"], [{ sessions: [{ id: "shared-id", title: "Unrelated local agent" }] }]);
	cache.setQueryData(["remote-workspaces", "remote-host"], [{ sessions: [remote] }]);
	expect(cachedTriageOrigin(cache, { sessionId: "shared-id", hostId: "remote-host" })).toEqual({ sessionId: "shared-id", hostId: "remote-host", session: remote });
	cache.setQueryData(["workspaces"], [{ sessions: [remote] }]);
	expect(cachedTriageOrigin(cache, { sessionId: "shared-id" })).toEqual({ sessionId: "shared-id", hostId: "remote-host", session: remote });
	expect(cachedTriageOrigin(cache, { sessionId: "shared-id", hostId: "uncached-host" })).toEqual({ sessionId: "shared-id", hostId: "uncached-host" });
});

it("keeps supplied cloud session context instead of coincident local records or machine readings", () => {
	const cache = new QueryClient();
	const cloud = { id: "shared-id", title: "Cloud agent", cloud: { orgId: "org" } };
	cache.setQueryData(["workspaces"], [{ sessions: [{ id: "shared-id", title: "Unrelated local agent" }] }]);
	cache.setQueryData(["session-memory", "all"], { system: { availableBytes: 100 }, app: { rssBytes: 20 } });
	expect(cachedTriageOrigin(cache, { sessionId: "shared-id", session: cloud })).toEqual({ sessionId: "shared-id", session: cloud });
});
