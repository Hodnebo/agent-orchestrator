import { CloudCpError } from "./cloud-cp";
import type { CloudCpClient, CloudCpCreateSessionRequest, CloudCpInterfaceMode, CloudCpSessionResponse } from "./cloud-cp";
import type { Settings } from "../hooks/useSettings";
import { reportCloudSessionCreateRequested } from "./cloud-telemetry";

/**
 * The default session interface a new cloud session is created with: the
 * daemon-owned "Default session interface" (Settings > General), which local
 * spawns resolve from too. The control plane resolves the session's interface
 * from it as the local daemon does, per kind and harness, so the desktop keeps
 * no rule or harness list of its own. It is always sent, "tui" included: the
 * control plane starts a create without it (an older desktop's) in the
 * terminal, an orchestrator too.
 */
export function cloudDefaultInterfaceMode(
	settings: Pick<Settings, "defaultSessionMode"> | undefined,
): CloudCpInterfaceMode {
	return settings?.defaultSessionMode === "chat" ? "chat" : "tui";
}

/**
 * Creates a cloud session with the user's default session interface. A
 * control plane that predates the field rejects the whole body with 400
 * invalid_request before creating anything, so the create is sent again
 * without it and the session starts in the terminal, as every session did
 * there. The user's request is reported once, not once per call.
 */
export async function createCloudSession(
	client: Pick<CloudCpClient, "createSession">,
	orgId: string,
	body: CloudCpCreateSessionRequest,
	defaultInterfaceMode: CloudCpInterfaceMode | undefined,
): Promise<CloudCpSessionResponse> {
	try {
		reportCloudSessionCreateRequested(body);
	} catch {
		// Telemetry must never break the create.
	}
	if (defaultInterfaceMode === undefined) return client.createSession(orgId, body);
	try {
		return await client.createSession(orgId, { ...body, defaultInterfaceMode });
	} catch (error) {
		if (!(error instanceof CloudCpError) || error.status !== 400 || error.code !== "invalid_request") throw error;
		return client.createSession(orgId, body);
	}
}
