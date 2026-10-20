import { describe, expect, it, vi } from "vitest";
import { CloudCpError } from "./cloud-cp";
import type { CloudCpCreateSessionRequest } from "./cloud-cp";
import { cloudSessionInterfaceMode, createCloudSession } from "./cloud-session-create";

const body: CloudCpCreateSessionRequest = {
	projectId: "project-1",
	kind: "worker",
	harness: "claude-code",
	displayName: "Task",
	prompt: "do the work",
};

describe("cloudSessionInterfaceMode", () => {
	it("asks for Chat only when the default session interface is Chat", () => {
		expect(cloudSessionInterfaceMode({ defaultSessionMode: "chat" })).toBe("chat");
		expect(cloudSessionInterfaceMode({ defaultSessionMode: "tui" })).toBeUndefined();
		expect(cloudSessionInterfaceMode(undefined)).toBeUndefined();
	});
});

describe("createCloudSession", () => {
	it("sends the interface with the create", async () => {
		const createSession = vi.fn().mockResolvedValue({ session: { id: "s1" } });
		await createCloudSession({ createSession }, "org-1", body, "chat");
		expect(createSession).toHaveBeenCalledTimes(1);
		expect(createSession).toHaveBeenCalledWith("org-1", { ...body, interfaceMode: "chat" });
	});

	it("sends the request an older desktop sent when there is no interface to ask for", async () => {
		const createSession = vi.fn().mockResolvedValue({ session: { id: "s1" } });
		await createCloudSession({ createSession }, "org-1", body, undefined);
		expect(createSession).toHaveBeenCalledWith("org-1", body);
		expect(createSession.mock.calls[0]?.[1]).not.toHaveProperty("interfaceMode");
	});

	it("retries without the field on a control plane that rejects it", async () => {
		const createSession = vi.fn()
			.mockRejectedValueOnce(new CloudCpError("The request body is invalid.", { status: 400, code: "invalid_request" }))
			.mockResolvedValueOnce({ session: { id: "s1" } });
		await expect(createCloudSession({ createSession }, "org-1", body, "chat")).resolves.toEqual({ session: { id: "s1" } });
		expect(createSession).toHaveBeenNthCalledWith(2, "org-1", body);
	});

	it("does not retry any other refusal", async () => {
		const refusal = new CloudCpError("Connect a credential.", { status: 422, code: "agent_provider_required" });
		const createSession = vi.fn().mockRejectedValue(refusal);
		await expect(createCloudSession({ createSession }, "org-1", body, "chat")).rejects.toBe(refusal);
		expect(createSession).toHaveBeenCalledTimes(1);
	});
});
