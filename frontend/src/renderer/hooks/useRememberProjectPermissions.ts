import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { apiErrorMessage } from "../lib/api-client";
import { clientForSessionHost } from "../lib/host-clients";
import type { ApprovalMode } from "../types/conversation";
import { remoteWorkspaceQueryKey, workspaceQueryKey } from "./useWorkspaceQuery";

type ProjectPermissionsConfig = {
	config?: {
		agentConfig?: { permissions?: string };
		worker?: { agentConfig?: { permissions?: string } };
	};
};

/**
 * The project's remembered permission mode, and saving or forgetting it. Only an
 * explicit toggle writes it; selecting a session mode is separate. `null` forgets.
 */
export function useRememberProjectPermissions(projectId: string, sourceHarness?: string, hostId?: string) {
	const queryClient = useQueryClient();
	const projectKey = hostId ? ["project", projectId, hostId] : ["project", projectId];
	const project = useQuery({
		queryKey: projectKey,
		enabled: Boolean(projectId),
		queryFn: async () => {
			const { data, error } = await clientForSessionHost(hostId).GET("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			if (data?.status !== "ok") throw new Error("Project config unavailable");
			return data.project as ProjectPermissionsConfig;
		},
	});
	const mutation = useMutation({
		mutationFn: async ({ targetProjectId, permissions, sourceHarness }: { targetProjectId: string; permissions: ApprovalMode | null; sourceHarness?: string }) => {
			const client = clientForSessionHost(hostId);
			const { error } = permissions === null
				? await client.DELETE("/api/v1/projects/{id}/permissions", { params: { path: { id: targetProjectId } } })
				: await client.PATCH("/api/v1/projects/{id}/permissions", {
					params: { path: { id: targetProjectId } },
					body: { permissions, ...(sourceHarness ? { sourceHarness } : {}) },
				});
			if (error) throw error;
		},
		onSuccess: (_data, { targetProjectId }) => {
			void Promise.all([
				queryClient.invalidateQueries({ queryKey: hostId ? ["project", targetProjectId, hostId] : ["project", targetProjectId] }),
				queryClient.invalidateQueries({ queryKey: hostId ? remoteWorkspaceQueryKey(hostId) : workspaceQueryKey }),
			]).catch(() => {});
		},
	});
	const remember = useCallback((permissions: ApprovalMode | null) =>
		mutation.mutateAsync({ targetProjectId: projectId, permissions, sourceHarness }), [mutation.mutateAsync, projectId, sourceHarness]);
	const belongsToProject = mutation.variables?.targetProjectId === projectId;
	const config = project.data?.config;
	const stored = (config?.worker?.agentConfig?.permissions || config?.agentConfig?.permissions || undefined) as ApprovalMode | undefined;
	// The last write wins until the refetched project catches up. The daemon stores
	// Codex's default as Full access, so the saved mode reads the same for every agent.
	const written = mutation.variables;
	const savedMode = belongsToProject && mutation.isSuccess && written
		? written.sourceHarness === "codex" && written.permissions === "default" ? "bypass-permissions" : written.permissions ?? undefined
		: stored;
	return {
		remember,
		pending: belongsToProject && mutation.isPending,
		savedMode,
		error: belongsToProject && mutation.error
			? apiErrorMessage(mutation.error, "Could not remember permissions for this project.")
			: undefined,
	};
}
