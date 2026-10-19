import { useQuery } from "@tanstack/react-query";
import { CHAT_SWITCH_TARGETS } from "../lib/agent-switch-targets";
import { clientForSessionHost } from "../lib/host-clients";
import type { ApprovalMode } from "../types/conversation";

/**
 * The permission modes each chat agent runs in, strictest first, as the daemon
 * reports them without starting anything. A switch to an agent from a chat in
 * another mode starts it in the first of its modes.
 */
export function useChatPermissionModes(hostId: string | undefined, enabled: boolean) {
	return useQuery({
		queryKey: ["chat-permission-modes", hostId ?? ""],
		queryFn: async () => {
			const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/agents/chat/permission-modes", {
				params: { query: { agents: CHAT_SWITCH_TARGETS.join(",") } },
			});
			if (error) throw error;
			return new Map(data.agents.map((entry) => [entry.agent, entry.modes as ApprovalMode[]]));
		},
		enabled,
		staleTime: 5 * 60_000,
	}).data;
}
