import { act, renderHook } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { usePreparedClone } from "./usePreparedClone";

const api = vi.hoisted(() => ({ POST: vi.fn() }));
vi.mock("../lib/api-client", () => ({ apiClient: api, apiErrorMessage: () => "Request failed" }));
vi.mock("../lib/host-clients", () => ({ clientForSessionHost: () => api }));

beforeEach(() => { api.POST.mockReset(); });

it.each([404, 405])("allows cloning on an older daemon without destination preflight (%s)", async (status) => {
	api.POST.mockResolvedValueOnce({ error: { message: "Route unavailable" }, response: { status } });
	const { result } = renderHook(() => usePreparedClone("older-host"));
	const input = { remoteUrl: "https://github.com/acme/repo.git", destinationParent: "/projects" };
	const signal = new AbortController().signal;
	await expect(result.current.checkDestination(input, signal)).resolves.toEqual({
		remoteUrl: input.remoteUrl, path: "", exists: false, canUseExisting: false,
	});
	expect(api.POST).toHaveBeenCalledWith("/api/v1/projects/clone/check", { body: input, signal });
	const conflict = { code: "CLONE_DESTINATION_EXISTS", message: "Destination exists" };
	api.POST.mockResolvedValueOnce({ error: conflict, response: { status: 409 } });
	await expect(result.current.prepare(input.remoteUrl, input.destinationParent)).rejects.toBe(conflict);
	expect(result.current.current()).toBeNull();
});

it.each([400, 401, 403, 500, 503])("preserves destination check failures other than missing routes (%s)", async (status) => {
	const error = { message: "Could not inspect destination" };
	api.POST.mockResolvedValueOnce({ error, response: { status } });
	const { result } = renderHook(() => usePreparedClone("remote-host"));
	await expect(result.current.checkDestination({ remoteUrl: "file:///source", destinationParent: "/projects" }, new AbortController().signal)).rejects.toBe(error);
});

it("cancels the active clone request", async () => {
	api.POST.mockImplementation((_path, { signal }: { signal: AbortSignal }) => new Promise((_resolve, reject) => {
		signal.addEventListener("abort", () => reject(new DOMException("Cancelled", "AbortError")), { once: true });
	}));
	const { result } = renderHook(() => usePreparedClone());
	const pending = result.current.prepare("file:///source", "~/projects");
	const rejection = expect(pending).rejects.toMatchObject({ name: "AbortError" });
	act(() => result.current.cancel());
	await rejection;
	expect(result.current.current()).toBeNull();
});

it("retains a checkout and its original input spelling for validation retries", async () => {
	const prepared = { path: "/Users/example/projects/source", remoteUrl: "file:///source", preparationId: "prepared" };
	api.POST.mockResolvedValue({ data: prepared });
	const { result } = renderHook(() => usePreparedClone());
	await result.current.prepare("file:///source", "~/projects");
	expect(result.current.current()).toEqual(prepared);
	expect(result.current.matches({ remoteUrl: "file:///source", destinationParent: "~/projects" })).toBe(true);
	expect(result.current.matches({ remoteUrl: "file:///other", destinationParent: "~/projects" })).toBe(false);
});

it("aborts a clone when its owning host changes", async () => {
	let requestSignal: AbortSignal | undefined;
	api.POST.mockImplementation((_path, { signal }: { signal: AbortSignal }) => {
		requestSignal = signal;
		return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject(new DOMException("Cancelled", "AbortError"))));
	});
	const { result, rerender } = renderHook(({ hostId }) => usePreparedClone(hostId), { initialProps: { hostId: "a" } });
	const pending = result.current.prepare("file:///source", "/projects");
	const rejection = expect(pending).rejects.toMatchObject({ name: "AbortError" });
	rerender({ hostId: "b" });
	await rejection;
	expect(requestSignal?.aborted).toBe(true);
});
