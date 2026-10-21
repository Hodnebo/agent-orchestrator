import { useCallback, useEffect, useRef, useState } from "react";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { clientForSessionHost } from "../lib/host-clients";

type PreparedClone = components["schemas"]["ClonePreparationResult"];

export function usePreparedClone(hostId?: string) {
	const currentRef = useRef<PreparedClone | null>(null);
	const inputRef = useRef<{ remoteUrl: string; destinationParent: string } | null>(null);
	const [isCleaning, setIsCleaning] = useState(false);
	const activeRequest = useRef<AbortController | null>(null);

	useEffect(() => () => {
		activeRequest.current?.abort();
		currentRef.current = null;
		inputRef.current = null;
	}, [hostId]);

	const checkDestination = useCallback(async (input: { remoteUrl: string; destinationParent: string }, signal: AbortSignal) => {
		const { data, error } = await clientForSessionHost(hostId).POST("/api/v1/projects/clone/check", { body: input, signal });
		if (error) throw error;
		if (!data) throw new Error("Could not check the destination. Retry or choose another folder.");
		return data;
	}, [hostId]);

	const prepare = useCallback(async (remoteUrl: string, destinationParent: string) => {
		const controller = new AbortController();
		activeRequest.current = controller;
		try {
			const { data, error } = await (hostId ? clientForSessionHost(hostId) : apiClient).POST("/api/v1/projects/clone/prepare", {
				body: { remoteUrl, destinationParent },
				signal: controller.signal,
			});
			if (error) throw error;
			if (!data) throw new Error("Could not clone repository");
			if (controller.signal.aborted) throw new DOMException("Cancelled", "AbortError");
			currentRef.current = data;
			inputRef.current = { remoteUrl, destinationParent };
			return data;
		} finally {
			if (activeRequest.current === controller) activeRequest.current = null;
		}
	}, [hostId]);

	const matches = useCallback((input: { remoteUrl: string; destinationParent: string }) =>
		inputRef.current?.remoteUrl === input.remoteUrl && inputRef.current.destinationParent === input.destinationParent, []);

	const cancel = useCallback(() => activeRequest.current?.abort(), []);

	const cleanup = useCallback(async (keepCheckout = false) => {
		const current = currentRef.current;
		if (!current) return;
		setIsCleaning(true);
		try {
			const { error } = await (hostId ? clientForSessionHost(hostId) : apiClient).POST("/api/v1/projects/clone/cleanup", {
				body: { path: current.path, preparationId: current.preparationId, ...(keepCheckout ? { keepCheckout: true } : {}) },
			});
			if (error) throw new Error(apiErrorMessage(error, "Could not clean up prepared clone"));
			currentRef.current = null;
			inputRef.current = null;
		} finally {
			setIsCleaning(false);
		}
	}, [hostId]);

	const complete = useCallback(() => {
		currentRef.current = null;
		inputRef.current = null;
	}, []);

	const current = useCallback(() => currentRef.current, []);

	return { cancel, checkDestination, cleanup, complete, current, isCleaning, matches, prepare };
}
