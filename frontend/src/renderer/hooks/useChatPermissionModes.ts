import { useQuery } from "@tanstack/react-query";
import { CHAT_SWITCH_TARGETS } from "../lib/agent-switch-targets";
import { clientForSessionHost } from "../lib/host-clients";
import type { ApprovalMode } from "../types/conversation";

/** The permission modes each chat agent runs in, strictest first, as the daemon reports them without starting anything. */
export function useChatPermissionModes(hostId: string | undefined, pendingHarness: string | undefined) {
	return useQuery({
		queryKey: ["chat-permission-modes", hostId ?? ""],
		queryFn: async () => {
			const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/agents/chat/permission-modes", {
				params: { query: { agents: CHAT_SWITCH_TARGETS.join(",") } },
			});
			if (error) throw error;
			return new Map(data.agents.map((entry) => [entry.agent, entry.modes as ApprovalMode[]]));
		},
		enabled: Boolean(pendingHarness),
		staleTime: 60_000,
		// An agent whose check had not finished (just after daemon start) is left out; ask again until it is there.
		refetchInterval: (query) => pendingHarness && query.state.data && !query.state.data.has(pendingHarness) ? 3_000 : false,
	}).data;
}
