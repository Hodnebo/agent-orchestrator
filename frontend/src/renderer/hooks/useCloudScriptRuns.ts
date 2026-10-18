import { hashKey, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useSyncExternalStore } from "react";
import type { CloudCpClientEvent } from "../lib/cloud-cp";
import { subscribeSessionEventsBridged } from "../lib/cloud-cp/stream-bridge";
import { groupScriptRuns, mergeScriptStepEvents, SCRIPT_RUN_END_EVENTS, SCRIPT_STEP_EVENT, type ScriptRun } from "../lib/cloud-script-runs";
import { useCloudCp } from "./useCloudCp";

/**
 * A session's `workspace.script.step` events, kept apart from the chat event
 * list so terminal sessions (which never load that list) can show their Setup
 * and Cleanup runs too.
 */
export function cloudScriptStepsQueryKey(orgId: string, sessionId: string) {
	return ["cloud-script-steps", orgId, sessionId] as const;
}

/** The chat surface's own event list (CloudSessionChatSurface); read, never fetched, here. */
function cloudChatEventsQueryKey(orgId: string, sessionId: string) {
	return ["cloud-chat-events", orgId, sessionId] as const;
}

/**
 * Folds live events into the script-step cache. The startup loader calls this
 * from the stream and poll it already runs, so the inspector and the failure
 * card update without a request of their own.
 */
export function recordCloudScriptStepEvents(queryClient: QueryClient, orgId: string, sessionId: string, events: readonly CloudCpClientEvent[]) {
	if (!orgId || !events.some((event) => event.type === SCRIPT_STEP_EVENT || SCRIPT_RUN_END_EVENTS.has(event.type))) return;
	queryClient.setQueryData<CloudCpClientEvent[]>(cloudScriptStepsQueryKey(orgId, sessionId), (current) => mergeScriptStepEvents(current ?? [], events));
}

/** Subscribes to a query's cached data without becoming an observer that could fetch it. */
function useCachedQueryData<T>(queryKey: readonly unknown[], enabled: boolean): T | undefined {
	const queryClient = useQueryClient();
	const hash = hashKey(queryKey);
	const subscribe = useCallback((notify: () => void) => queryClient.getQueryCache().subscribe((event) => {
		if (event.query.queryHash === hash) notify();
	}), [hash, queryClient]);
	const read = useCallback(() => (enabled ? queryClient.getQueryData<T>(queryKey) : undefined), [enabled, hash, queryClient]); // eslint-disable-line react-hooks/exhaustive-deps
	return useSyncExternalStore(subscribe, read, read);
}

/**
 * Setup and Cleanup runs for a Cloud session, oldest first.
 *
 * Reads, in order of preference: the chat surface's event list when it is
 * loaded (it already refreshes while the chat is open), then the script-step
 * cache the startup loader fills from its live stream. Only when neither has
 * the session does it page the session's events once. `live` additionally
 * listens on the session's event stream, for a surface (the failure card
 * after Retry) that waits on a run nothing else is watching.
 */
export function useCloudScriptRuns(
	orgId: string | undefined,
	sessionId: string | undefined,
	{ live = false, fetch = true, sessionEnded = false }: {
		live?: boolean;
		/** The session's sandbox is gone: a run that never reported its end is shown as ended. */
		sessionEnded?: boolean;
		/** Off for a caller that already feeds the cache from its own stream (the startup loader). */
		fetch?: boolean;
	} = {},
): ScriptRun[] {
	const { client, ready, baseUrl } = useCloudCp();
	const queryClient = useQueryClient();
	const enabled = Boolean(orgId && sessionId);
	const chatEvents = useCachedQueryData<CloudCpClientEvent[]>(cloudChatEventsQueryKey(orgId ?? "", sessionId ?? ""), enabled);
	const steps = useQuery({
		queryKey: cloudScriptStepsQueryKey(orgId ?? "", sessionId ?? ""),
		enabled: enabled && ready && fetch,
		staleTime: Infinity,
		refetchOnWindowFocus: false,
		queryFn: async ({ signal }) => {
			const known = queryClient.getQueryData<CloudCpClientEvent[]>(cloudScriptStepsQueryKey(orgId!, sessionId!)) ?? [];
			const chat = queryClient.getQueryData<CloudCpClientEvent[]>(cloudChatEventsQueryKey(orgId!, sessionId!));
			if (chat) return mergeScriptStepEvents(known, chat);
			let events = known;
			let after = 0;
			for (;;) {
				const page = await client.listChatEvents(orgId!, sessionId!, { after, limit: 500 }, { signal });
				events = mergeScriptStepEvents(events, page.events);
				if (!page.hasMore || page.nextAfter <= after) return events;
				after = page.nextAfter;
			}
		},
	});
	useEffect(() => {
		if (!live || !enabled || !ready || !baseUrl) return;
		const controller = new AbortController();
		const after = queryClient.getQueryData<CloudCpClientEvent[]>(cloudScriptStepsQueryKey(orgId!, sessionId!))?.at(-1)?.sequence;
		void subscribeSessionEventsBridged({
			baseUrl, orgId: orgId!, sessionId: sessionId!, after, signal: controller.signal,
			onEvent: (event) => recordCloudScriptStepEvents(queryClient, orgId!, sessionId!, [event]),
		});
		return () => controller.abort();
	}, [baseUrl, enabled, live, orgId, queryClient, ready, sessionId]);
	return useMemo(() => {
		const merged = chatEvents ? mergeScriptStepEvents(steps.data ?? [], chatEvents) : steps.data ?? [];
		return merged.length > 0 ? groupScriptRuns(merged, { sessionEnded }) : [];
	}, [chatEvents, sessionEnded, steps.data]);
}
