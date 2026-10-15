// @vitest-environment node
import { createServer, type Server } from "node:http";
import { QueryClient } from "@tanstack/react-query";
import { expect, it, vi } from "vitest";
import { getApiBaseUrl, setApiBaseUrl } from "../lib/api-client";
import { sessionArtifactFileQueryOptions } from "./useSessionWorkspaceFiles";

vi.mock("../lib/bridge", () => ({ aoBridge: {} }));

it("reads artifacts from IPv4 AO while another app owns the same port on IPv6", async (context) => {
	const requests: string[] = [];
	const daemon = createServer((request, response) => {
		requests.push(request.url ?? "");
		response.end("# The AO artifact");
	});
	let competingRequests = 0;
	const competitor = createServer((_request, response) => {
		competingRequests++;
		response.writeHead(404);
		response.end("Unrelated application");
	});
	const close = (server: Server) => new Promise<void>((resolve, reject) => {
		if (!server.listening) return resolve();
		server.close((error) => error ? reject(error) : resolve());
		server.closeAllConnections();
	});
	const nativeFetch = globalThis.fetch;
	const previousBase = getApiBaseUrl();
	const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	try {
		await new Promise<void>((resolve) => daemon.listen(0, "127.0.0.1", resolve));
		const address = daemon.address();
		if (!address || typeof address === "string") throw new Error("Expected a TCP listener");
		const { port } = address;
		try {
			await new Promise<void>((resolve, reject) => {
				competitor.once("error", reject);
				competitor.listen({ host: "::1", port, ipv6Only: true }, resolve);
			});
		} catch (error) {
			if (["EAFNOSUPPORT", "EADDRNOTAVAIL"].includes((error as NodeJS.ErrnoException).code ?? "")) {
				context.skip("IPv6 loopback is unavailable");
				return;
			}
			throw error;
		}
		const hostname = "ao-preview-artifact.session.localhost";
		const rawUrl = `http://${hostname}:${port}/sub/report.md?raw=true`;
		// Reproduce Chromium choosing ::1 deterministically, without relying on
		// the test host's DNS support or address ordering for *.localhost.
		const fetch = vi.fn((input: string | URL | Request, init?: RequestInit) =>
			nativeFetch(String(input).replace(`http://${hostname}:`, "http://[::1]:"), init));
		vi.stubGlobal("fetch", fetch);
		expect((await fetch(rawUrl)).status).toBe(404);
		expect(competingRequests).toBe(1);
		fetch.mockClear();
		setApiBaseUrl(`http://127.0.0.1:${port}`);

		const detail = await client.fetchQuery(sessionArtifactFileQueryOptions("session-1", "sub/report.md", rawUrl));

		expect(detail.content).toBe("# The AO artifact");
		expect(detail.editable).toBe(true);
		expect(detail.fileFingerprint).toMatch(/^[a-f0-9]{64}$/);
		expect(requests).toEqual(["/api/v1/sessions/session-1/preview/files/sub/report.md?source=artifact&raw=true"]);
		expect(competingRequests).toBe(1);
		expect(fetch).toHaveBeenCalledExactlyOnceWith(`http://127.0.0.1:${port}${requests[0]}`);

		// A stopped/restarting daemon must not fall back to the ambiguous host.
		client.clear();
		fetch.mockClear();
		setApiBaseUrl(null);
		await expect(client.fetchQuery(sessionArtifactFileQueryOptions("session-1", "sub/report.md", rawUrl))).rejects.toThrow("Unable to load artifact");
		expect(fetch).not.toHaveBeenCalled();
	} finally {
		client.clear();
		setApiBaseUrl(previousBase || null);
		vi.unstubAllGlobals();
		await Promise.all([close(daemon), close(competitor)]);
	}
});
