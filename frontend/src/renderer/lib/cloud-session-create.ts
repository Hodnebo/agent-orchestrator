import { CloudCpError } from "./cloud-cp";
import type { CloudCpClient, CloudCpCreateSessionRequest, CloudCpInterfaceMode, CloudCpSessionResponse } from "./cloud-cp";
import type { Settings } from "../hooks/useSettings";

/**
 * The interface a new cloud session asks to start in: the daemon-owned
 * "Default session interface" (Settings > General), which local spawns apply
 * too. The control plane decides per harness, as the local daemon does: it
 * starts Chat only for a harness that can start there and silently starts any
 * other in the terminal, so the desktop keeps no harness list of its own.
 * "tui" is not sent, which keeps the request an older desktop sent.
 */
export function cloudSessionInterfaceMode(
	settings: Pick<Settings, "defaultSessionMode"> | undefined,
): CloudCpInterfaceMode | undefined {
	return settings?.defaultSessionMode === "chat" ? "chat" : undefined;
}

/**
 * Creates a cloud session that starts in interfaceMode. A control plane that
 * predates the field rejects the whole body with 400 invalid_request before
 * creating anything, so the create is sent again without it and the session
 * starts in the terminal, as every session did there.
 */
export async function createCloudSession(
	client: Pick<CloudCpClient, "createSession">,
	orgId: string,
	body: CloudCpCreateSessionRequest,
	interfaceMode: CloudCpInterfaceMode | undefined,
): Promise<CloudCpSessionResponse> {
	if (interfaceMode === undefined) return client.createSession(orgId, body);
	try {
		return await client.createSession(orgId, { ...body, interfaceMode });
	} catch (error) {
		if (!(error instanceof CloudCpError) || error.status !== 400 || error.code !== "invalid_request") throw error;
		return client.createSession(orgId, body);
	}
}
