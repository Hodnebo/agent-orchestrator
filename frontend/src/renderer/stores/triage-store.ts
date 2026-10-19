import { create } from "zustand";
import type { TriageChoice, TriageOrigin, TriageRun, TriageSelection } from "../../shared/triage";
import { aoBridge } from "../lib/bridge";
import { isTriageSessionRun } from "../lib/triage";

type TriageState = {
	choices: TriageChoice[];
	selection: TriageSelection | null;
	explicitSelection: boolean;
	catalogLoaded: boolean;
	loading: boolean;
	starting: boolean;
	preparationId: string | null;
	preparationGeneration: number;
	preparing: boolean;
	preparationPromise: Promise<TriageRun | undefined> | null;
	preparationError: string | null;
	error: string | null;
	runs: TriageRun[];
	runErrors: Record<string, string | undefined>;
	pending: Record<string, boolean | undefined>;
	replies: Record<string, string | undefined>;
	observation: string;
	origin: TriageOrigin;
	attentionId: string | null;
	setContext: (origin?: TriageOrigin, observation?: string) => void;
	setObservation: (value: string) => void;
	setSelection: (value: TriageSelection) => void;
	loadCatalog: (project?: (native: TriageChoice[]) => TriageChoice[] | undefined) => Promise<void>;
	loadRuns: () => Promise<void>;
	loadRun: (id: string) => Promise<void>;
	receive: (run: TriageRun, notify?: boolean) => void;
	start: (permissionsForThisStart?: "bypass-permissions") => Promise<TriageRun | undefined>;
	prepare: (origin: TriageOrigin) => Promise<TriageRun | undefined>;
	discardPreparation: () => void;
	setReply: (id: string, text: string) => void;
	reply: (id: string, requestId?: string, answers?: Record<string, string[]>, text?: string) => Promise<void>;
	cancel: (id: string) => Promise<void>;
	publish: (id: string) => Promise<void>;
	signInGitHub: (id: string) => Promise<void>;
	nativeInput: (id: string) => Promise<void>;
};

const message = (error: unknown) => error instanceof Error ? error.message : String(error);

export const useTriageStore = create<TriageState>((set, get) => {
	const runAction = async (id: string, action: () => Promise<TriageRun>, interrupt = false) => {
		if (get().pending[id] && !interrupt) return;
		set((state) => ({ pending: { ...state.pending, [id]: true }, runErrors: { ...state.runErrors, [id]: undefined } }));
		try { get().receive(await action()); }
		catch (error) { set((state) => ({ runErrors: { ...state.runErrors, [id]: message(error) } })); }
		finally { set((state) => ({ pending: { ...state.pending, [id]: false } })); }
	};
	return {
		choices: [], selection: null, explicitSelection: false, catalogLoaded: false, loading: false, starting: false, error: null,
		preparationId: null, preparationGeneration: 0, preparing: false, preparationPromise: null, preparationError: null,
		runs: [], runErrors: {}, pending: {}, replies: {}, observation: "", origin: {}, attentionId: null,
		setContext: (origin, observation) => set({ origin: origin ?? {}, observation: observation ?? "" }),
		setObservation: (observation) => set({ observation }),
		setSelection: (selection) => set({ selection, explicitSelection: true }),
		prepare: (origin) => {
			const uncertain = get().runs.find((run) => run.execution === "session" && !run.sessionId);
			if (uncertain) {
				const promise = Promise.resolve(uncertain);
				set({ preparationId: uncertain.id, preparationPromise: promise, preparing: false, preparationError: null });
				return promise;
			}
			get().discardPreparation();
			const generation = get().preparationGeneration;
			set({ preparing: true, preparationError: null });
			const promise = (async () => {
				try {
					const run = await aoBridge.triage.prepare(origin);
					get().receive(run);
					const current = get().runs.find((item) => item.id === run.id) ?? run;
					if (get().preparationGeneration !== generation) {
						if (current.preparation && !isTriageSessionRun(current)) get().receive(await aoBridge.triage.cancel(run.id));
						return;
					}
					set({ preparationId: current.preparation ? run.id : null });
					return current;
				} catch (error) {
					if (get().preparationGeneration === generation) set({ preparationError: message(error) });
				} finally {
					if (get().preparationGeneration === generation) set({ preparing: false });
				}
			})();
			set({ preparationPromise: promise });
			return promise;
		},
		discardPreparation: () => {
			if (get().starting) return;
			const id = get().preparationId;
			const run = get().runs.find((run) => run.id === id);
			if (run && isTriageSessionRun(run) && !run.sessionId) return;
			const preparation = run?.preparation && !isTriageSessionRun(run);
			set((state) => ({ preparationId: null, preparing: false, preparationPromise: null, preparationGeneration: state.preparationGeneration + 1 }));
			if (id && preparation) void aoBridge.triage.cancel(id).then((run) => get().receive(run)).catch(() => {});
		},
		loadCatalog: async (project) => {
			if (get().loading) return;
			set({ loading: true, error: null });
			try {
				const [native, preference] = await Promise.all([aoBridge.triage.catalog(), aoBridge.triage.getPreference()]);
				const configured = project?.(native);
				if (configured) await aoBridge.triage.configure(configured);
				const choices = configured ? await aoBridge.triage.catalog() : native;
				// Keep an unavailable saved choice visible. Never switch to another account or provider.
				set({ choices, selection: get().explicitSelection ? get().selection : preference, catalogLoaded: true });
			} catch (error) { set({ error: message(error), catalogLoaded: true }); }
			finally { set({ loading: false }); }
		},
		loadRuns: async () => {
			try { for (const run of await aoBridge.triage.list()) get().receive(run); }
			catch { /* Support may be unavailable; its entry point shows the fallback. */ }
		},
		loadRun: async (id) => {
			try { get().receive(await aoBridge.triage.get(id)); }
			catch (error) { set((state) => ({ runErrors: { ...state.runErrors, [id]: message(error) } })); }
		},
		receive: (run, notify = false) => set((state) => {
			const previous = state.runs.find((item) => item.id === run.id);
			if (previous?.sessionId && run.sessionId && previous.sessionId !== run.sessionId) return state;
			// Version identifies the protocol, not a revision. Normalize Go's UTC
			// fractional timestamps so even updates within one millisecond stay ordered.
			const timestamp = (at: string) => at.replace(/(?:\.(\d+))?Z$/, (_, fraction: string | undefined) => `.${(fraction ?? "").padEnd(9, "0")}Z`);
			if (previous && timestamp(previous.updatedAt) > timestamp(run.updatedAt)) {
				if (!previous.sessionId && run.sessionId) return { runs: state.runs.map((item) => item.id === run.id ? { ...previous, execution: "session" as const, sessionId: run.sessionId } : item) };
				return state;
			}
			const current = previous?.sessionId ? { ...run, execution: "session" as const, sessionId: previous.sessionId } : run;
			return {
				runs: [...state.runs.filter((item) => item.id !== run.id), current].sort((a, b) => b.createdAt.localeCompare(a.createdAt)),
				...(notify ? { attentionId: run.id } : {}),
			};
		}),
		start: async (permissionsForThisStart) => {
			const { selection: normalSelection, starting, observation, origin } = get();
			if (!normalSelection || starting) return;
			set({ starting: true, error: null });
			try {
				const prepared = await get().preparationPromise;
				const existing = prepared && (get().runs.find((run) => run.id === prepared.id) ?? prepared);
				if (existing && !existing.preparation && (!isTriageSessionRun(existing) || existing.sessionId)) {
					get().receive(existing, true);
					set({ preparationId: null, preparationPromise: null });
					return existing;
				}
				const selection = existing?.execution === "session" && !existing.sessionId ? existing.request
					: permissionsForThisStart && normalSelection.harness === "unreal-agent" ? { ...normalSelection, permissions: permissionsForThisStart } : normalSelection;
				if (get().explicitSelection && !permissionsForThisStart && existing?.execution !== "session") await aoBridge.triage.setPreference(selection);
				const request = existing?.execution === "session" && !existing.sessionId
					? { ...existing.request, preparationId: existing.request.preparationId || existing.id }
					: { ...selection, ...(prepared ? { preparationId: prepared.id } : {}), observation: observation.trim(), origin, publishAuthorized: true };
				const run = await aoBridge.triage.start(request);
				get().receive(run, true);
				const current = get().runs.find((item) => item.id === run.id) ?? run;
				if (current.execution === "session" && !current.sessionId) set({ preparationId: current.id, preparationPromise: Promise.resolve(current) });
				else set({ preparationId: null, preparationPromise: null });
				return current;
			} catch (error) { set({ error: message(error) }); }
			finally { set({ starting: false }); }
		},
		setReply: (id, text) => set((state) => ({ replies: { ...state.replies, [id]: text } })),
		reply: async (id, requestId, answers, providedText) => {
			const text = providedText ?? get().replies[id] ?? "";
			await runAction(id, async () => {
				const run = await aoBridge.triage.reply(id, text, requestId, answers);
				set((state) => ({ replies: { ...state.replies, [id]: "" } }));
				return run;
			});
		},
		cancel: (id) => runAction(id, () => aoBridge.triage.cancel(id), true),
		publish: (id) => runAction(id, () => aoBridge.triage.publish(id)),
		signInGitHub: (id) => runAction(id, () => aoBridge.triage.signInGitHub(id)),
		nativeInput: (id) => runAction(id, () => aoBridge.triage.nativeInput(id)),
	};
});
