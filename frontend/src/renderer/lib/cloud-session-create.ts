import { CloudCpError } from "./cloud-cp";
import type { CloudCpClient, CloudCpCreateSessionRequest, CloudCpInterfaceMode, CloudCpSessionResponse } from "./cloud-cp";
import type { Settings } from "../hooks/useSettings";

/**
 * The default session interface a new cloud session is created with: the
 * daemon-owned "Default session interface" (Settings > General), which local
 * spawns resolve from too. The control plane resolves the session's interface
 * from it exactly as the local daemon does, per kind and harness, so the
 * desktop keeps no rule or harness list of its own. "tui" is not sent, which
 * keeps the request an older desktop sent.
 */
export function cloudDefaultInterfaceMode(
	settings: Pick<Settings, "defaultSessionMode"> | undefined,
): CloudCpInterfaceMode | undefined {
	return settings?.defaultSessionMode === "chat" ? "chat" : undefined;
}

/**
 * Creates a cloud session with the user's default session interface. A
 * control plane that predates the field rejects the whole body with 400
 * invalid_request before creating anything, so the create is sent again
 * without it and the session starts in the terminal, as every session did
 * there.
 */
export async function createCloudSession(
	client: Pick<CloudCpClient, "createSession">,
	orgId: string,
	body: CloudCpCreateSessionRequest,
	defaultInterfaceMode: CloudCpInterfaceMode | undefined,
): Promise<CloudCpSessionResponse> {
	if (defaultInterfaceMode === undefined) return client.createSession(orgId, body);
	try {
		return await client.createSession(orgId, { ...body, defaultInterfaceMode });
	} catch (error) {
		if (!(error instanceof CloudCpError) || error.status !== 400 || error.code !== "invalid_request") throw error;
		return client.createSession(orgId, body);
	}
}
