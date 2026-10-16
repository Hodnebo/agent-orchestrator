import { STANDALONE_WORKSPACE_ID, type WorkspaceSession } from "../types/workspace";

export function sessionBranchPresentation(session?: WorkspaceSession): { branch?: string; naming: boolean } {
	// Worktree completion follows naming and persistence. Keep its provisional
	// branch private, including when creation fails before that step completes.
	const unfinished = session && !session.cloud && session.kind !== "orchestrator"
		&& session.workspaceId !== STANDALONE_WORKSPACE_ID
		&& (!session.provisionSteps?.length || session.provisionSteps.some((step) => step.id === "worktree" && step.status !== "done"))
		&& (session.provisionState === "provisioning" || session.provisionState === "failed");
	return {
		branch: unfinished ? undefined : session?.branch,
		naming: Boolean(unfinished && session?.provisionState === "provisioning"),
	};
}
