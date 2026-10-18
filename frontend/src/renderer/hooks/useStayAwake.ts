import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { mobileStatusQueryKey } from "../lib/mobile-status-key";
import { awakeCapability, useAwake } from "./useAwake";

type MobileStatus = components["schemas"]["MobileStatusResponse"];

/**
 * One on/off Stay Awake for the sidebar button and the Connect Mobile switch.
 *
 * "On" is an indefinite AO-scope awake session. Turning it off also clears the
 * older mobile-only keep-awake flag when the cached mobile status shows it set,
 * so a flag saved before the two were joined cannot keep the Mac awake with no
 * switch left to turn it off.
 */
export function useStayAwake(enabled = true) {
	const cache = useQueryClient();
	const awake = useAwake(enabled);
	const [intent, setIntent] = useState<boolean | null>(null);
	const [failure, setFailure] = useState<string | null>(null);
	const sessions = awake.state?.sessions?.filter((s) => !s.endedAt && s.scope === "ao") ?? [];
	// Subscribe to the mobile status the Connect Mobile page keeps fresh, without fetching it here.
	const mobile = useQuery<MobileStatus>({ queryKey: mobileStatusQueryKey, enabled: false });
	const legacyOn = mobile.data?.keepAwake?.enabled ?? false;
	const on = intent ?? (sessions.length > 0 || legacyOn);
	const set = async (next: boolean) => {
		setIntent(next);
		setFailure(null);
		try {
			if (next) {
				if (sessions.length === 0) {
					const start = () =>
						awake.actAsync({
							kind: "start",
							request: { scope: "ao", sessionId: "", mode: "indefinite", lidClosed: awake.state?.settings.lidClosed ?? true },
						});
					// A battery or heat stop blocks new sessions until it is resumed. Turning
					// the switch on is the user's request to resume, so do that and retry;
					// resume still refuses while the battery or heat limit is still breached.
					if (awake.state?.paused) await awake.actAsync({ kind: "resume" });
					try {
						await start();
					} catch (e) {
						if (!(e instanceof Error) || !e.message.includes("safety stop")) throw e;
						await awake.actAsync({ kind: "resume" });
						await start();
					}
				}
			} else {
				for (const source of sessions) await awake.actAsync({ kind: "end", id: source.id });
				if (legacyOn) {
					const { error } = await apiClient.POST("/api/v1/mobile/keep-awake", { body: { enabled: false, lidClosed: false } });
					if (error) throw new Error(apiErrorMessage(error));
					await cache.invalidateQueries({ queryKey: mobileStatusQueryKey });
				}
			}
		} catch (e) {
			setFailure(e instanceof Error ? e.message : String(e));
		} finally {
			setIntent(null);
		}
	};
	return {
		on,
		set,
		busy: awake.busy,
		error: failure,
		supported: awakeCapability(awake.state, "idle")?.available ?? false,
		starting: on && !awake.state?.active,
	};
}
