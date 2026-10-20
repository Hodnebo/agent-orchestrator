import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { useSessionRename } from "./useSessionRename";

const h = vi.hoisted(() => ({ cloudRename: vi.fn(), daemonRename: vi.fn() }));
vi.mock("./useCloudCp", () => ({ useCloudCp: () => ({ client: { renameSession: h.cloudRename } }) }));
vi.mock("../lib/rename-session", () => ({ renameSession: h.daemonRename }));

const wrapper = ({ children }: { children: ReactNode }) => (
	<QueryClientProvider client={new QueryClient()}>{children}</QueryClientProvider>
);

async function rename(session: Parameters<typeof useSessionRename>[0], name: string) {
	const onRenamed = vi.fn();
	const { result } = renderHook(() => useSessionRename(session, onRenamed), { wrapper });
	act(() => result.current.begin());
	act(() => result.current.setDraft(name));
	await act(() => result.current.commit());
	return onRenamed;
}

describe("useSessionRename", () => {
	it("renames a cloud session on its control plane, as a local one is renamed on the daemon", async () => {
		h.cloudRename.mockResolvedValue({ session: {} });
		const onRenamed = await rename({ id: "s-1", title: "old", cloud: { orgId: "org-1" } }, " Fix login ");
		expect(h.cloudRename).toHaveBeenCalledWith("org-1", "s-1", "Fix login");
		expect(h.daemonRename).not.toHaveBeenCalled();
		expect(onRenamed).toHaveBeenCalled();
	});

	it("renames a local session on the daemon", async () => {
		h.cloudRename.mockClear();
		h.daemonRename.mockResolvedValue(undefined);
		await rename({ id: "s-2", title: "old" }, "New name");
		expect(h.daemonRename).toHaveBeenCalledWith("s-2", "New name");
		expect(h.cloudRename).not.toHaveBeenCalled();
	});
});
