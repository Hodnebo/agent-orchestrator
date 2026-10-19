import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useCloudSessionsQuery } from "./useWorkspaceQuery";

const cloud = vi.hoisted(() => ({ listSessions: vi.fn(), listSessionArtifacts: vi.fn() }));
vi.mock("./useCloudCp", () => ({ useCloudCp: () => ({ client: cloud, ready: true, baseUrl: "https://cp.test" }) }));
vi.mock("./useCloudOrg", () => ({ useCloudOrg: () => ({ org: { id: "org-1" }, ready: true }) }));

const files = [{ path: "report.html", name: "report.html", kind: "html", contentType: "text/html", size: 3, sha256: "a".repeat(64), updatedAt: "2026-10-16T10:00:00Z" }];

function session(id: string, updatedAt: string, artifactCount?: number) {
	return { id, orgId: "org-1", projectId: "p-1", updatedAt, createdAt: updatedAt, artifactCount };
}

describe("useCloudSessionsQuery artifact files", () => {
	let queryClient: QueryClient;
	const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;

	beforeEach(() => {
		queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		cloud.listSessions.mockReset();
		cloud.listSessionArtifacts.mockReset().mockResolvedValue({ artifacts: files });
	});

	it("reads the files of a session that has any, once per session version", async () => {
		cloud.listSessions.mockResolvedValue({ items: [session("s-1", "2026-10-16T10:00:00Z", 1), session("s-2", "2026-10-16T10:00:00Z", 0)] });
		const { result } = renderHook(() => useCloudSessionsQuery(), { wrapper });
		await waitFor(() => expect(result.current.data).toBeDefined());
		expect(result.current.data?.[0]?.artifactFiles).toEqual(files);
		expect(result.current.data?.[1]?.artifactFiles).toBeUndefined();
		expect(cloud.listSessionArtifacts).toHaveBeenCalledTimes(1);

		await result.current.refetch();
		expect(cloud.listSessionArtifacts).toHaveBeenCalledTimes(1);

		cloud.listSessions.mockResolvedValue({ items: [session("s-1", "2026-10-16T10:05:00Z", 1)] });
		await result.current.refetch();
		expect(cloud.listSessionArtifacts).toHaveBeenCalledTimes(2);
	});

	it("keeps the session when its files cannot be read", async () => {
		cloud.listSessions.mockResolvedValue({ items: [session("s-1", "2026-10-16T10:00:00Z", 1)] });
		cloud.listSessionArtifacts.mockRejectedValue(new Error("unavailable"));
		const { result } = renderHook(() => useCloudSessionsQuery(), { wrapper });
		await waitFor(() => expect(result.current.data).toHaveLength(1));
		expect(result.current.data?.[0]?.artifactFiles).toBeUndefined();
	});
});
