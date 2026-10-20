import { useIsMutating, useMutation, useMutationState, useQuery, useQueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { mobileStatusQueryKey } from "../lib/mobile-status-key";
import { awakeCapability, awakeQueryKey, type AwakeState, useAwake } from "./useAwake";

type MobileStatus = components["schemas"]["MobileStatusResponse"];
type Change = { on: boolean; lidClosed?: boolean };
const mutationKey = ["stay-awake-toggle"] as const;

/** The desktop and mobile controls share intent, pending state, and ownership. */
export function useStayAwake(enabled = true) {
	const cache = useQueryClient();
	const awake = useAwake(enabled);
	const mobile = useQuery({
		queryKey: mobileStatusQueryKey,
		enabled,
		queryFn: async ({ signal }) => {
			const { data, error } = await apiClient.GET("/api/v1/mobile/status", { signal });
			if (error) throw new Error(apiErrorMessage(error));
			if (!data) throw new Error("Mobile status is unavailable");
			return data;
		},
		// Status can spawn host probes; the sidebar only refreshes stale data on
		// mount/focus. Connect Mobile owns its separate pairing-pending interval.
		staleTime: 60_000,
		refetchOnWindowFocus: true,
		refetchInterval: false,
		retry: false,
	});
	const sessions = awake.state?.sessions?.filter((s) => !s.endedAt) ?? [];
	const rules = awake.state?.paused ? [] : awake.state?.rules?.filter((r) => r.enabled && !r.suppressed) ?? [];
	const desktopOn = sessions.length > 0 || rules.length > 0 || !!awake.state?.active;
	const desktopLid = sessions.some((s) => s.lidClosed) || rules.some((r) => r.lidClosed) || !!awake.state?.lidActive;
	const pending = useMutationState<Change>({
		filters: { mutationKey, status: "pending" },
		select: (m) => m.state.variables as Change,
	});
	const intent = pending.at(-1);
	const busy = useIsMutating({ mutationKey }) > 0 || awake.busy;
	// Requested ownership must stay clearable even when a rule is waiting or a
	// safety stop has released the host assertion. Actual wake is separate.
	const active = !!awake.state?.active || !!mobile.data?.keepAwake?.active;
	const on = intent?.on ?? (desktopOn || !!mobile.data?.keepAwake?.enabled || active);
	const lidSupported = awakeCapability(awake.state, "lid")?.available ?? false;
	const lidOn = intent ? intent.on && (intent.lidClosed ?? lidSupported) : desktopLid || !!(mobile.data?.keepAwake?.enabled && mobile.data.keepAwake.lidClosed);
	const lidActive = !!awake.state?.lidActive || !!mobile.data?.keepAwake?.lidActive;
	// The desktop status reports its lid failure as the overall reason.
	const lidReason = lidOn && !lidActive
		? (desktopLid ? awake.state?.reason : mobile.data?.keepAwake?.lidReason) || undefined
		: undefined;
	// Status also includes recovery/lid errors unrelated to an inactive request.
	const pauseReason = awake.state?.paused ? awake.state.reason : undefined;
	const reason = pauseReason === "low_battery" || pauseReason === "critical_heat" || pauseReason === "battery_unknown"
		? pauseReason : undefined;
	const mutation = useMutation({
		mutationKey,
		mutationFn: async (change: Change) => {
			// Cancel an older status read before writing the authoritative response.
			await cache.cancelQueries({ queryKey: mobileStatusQueryKey });
			const releaseMobile = async () => {
				const { data, error } = await apiClient.POST("/api/v1/mobile/keep-awake", { body: { enabled: false, lidClosed: false } });
				if (error) throw new Error(apiErrorMessage(error));
				if (!data) throw new Error("Mobile status is unavailable");
				cache.setQueryData<MobileStatus>(mobileStatusQueryKey, data);
			};
			const start = async (lidClosed = false) => {
				let latest = await awake.actAsync({ kind: "start", request: { scope: "ao", sessionId: "", mode: "indefinite", lidClosed } });
				// A successful POST writes a lease; the native helper acknowledges it
				// on its next two-second scan. Keep the shared intent pending until
				// that acknowledgement instead of flashing an inactive warning. A first
				// closed-lid start also registers the helper before its first scan.
				const deadline = Date.now() + (lidClosed ? 10_000 : 5_000);
				while ((!latest.active || (lidClosed && !latest.lidActive)) && !latest.paused && (!latest.reason || latest.reason === "starting" || latest.reason === "helper_unavailable") && Date.now() < deadline) {
					await new Promise((resolve) => setTimeout(resolve, 500));
					let timeout: number | undefined;
					try {
						const confirmed = await Promise.race([
							cache.fetchQuery<AwakeState>({ queryKey: awakeQueryKey, staleTime: 0 }),
							new Promise<undefined>((resolve) => { timeout = window.setTimeout(resolve, Math.max(0, deadline - Date.now())); }),
						]);
						if (!confirmed) break;
						latest = confirmed;
					} catch {
						// An unavailable status endpoint must not leave the controls busy.
						break;
					} finally {
						window.clearTimeout(timeout);
					}
				}
			};
			const ensureDesktop = async (lidClosed = false) => {
				if (awake.state?.paused) await awake.actAsync({ kind: "resume" });
				if (sessions.length > 0 && (!lidClosed || desktopLid)) return;
				try {
					await start(lidClosed);
				} catch (e) {
					// A safety stop can happen after the last poll. Re-read the daemon's
					// structured pause state rather than interpreting its error wording.
					await cache.cancelQueries({ queryKey: awakeQueryKey });
					const latest = await cache.fetchQuery<AwakeState>({ queryKey: awakeQueryKey, staleTime: 0 }).catch(() => undefined);
					if (!latest?.paused) throw e;
					await awake.actAsync({ kind: "resume" });
					await start(lidClosed);
				}
			};
			if (!change.on) {
				// Stop all scopes and pause automatic rules, including pre-upgrade owners.
				// Attempt both releases even if one backend fails.
				const results = await Promise.allSettled([awake.actAsync({ kind: "stop" }), releaseMobile()]);
				for (const result of results) if (result.status === "rejected") throw result.reason;
			} else if (change.lidClosed === false) {
				if (rules.some((r) => r.lidClosed)) {
					// Automatic lid rules would rearm; pause them and replace with idle-only wake.
					const results = await Promise.allSettled([awake.actAsync({ kind: "stop" }), releaseMobile()]);
					for (const result of results) if (result.status === "rejected") throw result.reason;
					await start();
				} else if (desktopLid) {
					// End only lid sessions, starting idle wake first so the Mac never
					// sleeps in between. Timed and session-scoped owners keep running.
					if (!sessions.some((s) => !s.lidClosed)) await start();
					const endLid = async () => {
						for (const s of sessions) if (s.lidClosed) await awake.actAsync({ kind: "end", id: s.id });
					};
					const results = await Promise.allSettled([endLid(), releaseMobile()]);
					for (const result of results) if (result.status === "rejected") throw result.reason;
				} else {
					await ensureDesktop();
					await releaseMobile();
				}
			} else {
				await ensureDesktop(change.lidClosed ?? lidSupported);
				// Both controls use desktop ownership and its safety policy. Retire
				// legacy mobile ownership only after desktop wake has been established.
				if (mobile.data?.keepAwake?.enabled || mobile.data?.keepAwake?.active || mobile.data?.keepAwake?.lidClosed) await releaseMobile();
			}
		},
	});
	const change = (next: Change) => {
		// The cache guard also covers two calls before React renders the disabled UI.
		if (cache.isMutating({ mutationKey }) > 0 || awake.busy) return;
		mutation.mutate(next);
	};
	return {
		on,
		active,
		attention: on && (!active || (lidOn && !lidActive)) && !busy,
		reason: desktopOn ? reason : undefined,
		lidOn,
		set: (on: boolean) => change({ on }),
		lidSupported,
		lidActive,
		lidReason,
		stopBatteryPercent: awake.state?.settings?.stopBatteryPercent || 10,
		setLid: (lidClosed: boolean) => change({ on: true, lidClosed }),
		busy,
		error: mutation.error instanceof Error ? mutation.error.message : null,
		supported: awakeCapability(awake.state, "idle")?.available ?? false,
		starting: intent?.on === true && (!active || ((intent.lidClosed ?? lidSupported) && !lidActive)),
	};
}
