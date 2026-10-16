import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useHostConnection } from "../hooks/useHostConnection";
import type { FilesSource } from "../hooks/useSessionWorkspaceFiles";
import { getApiBaseUrl } from "../lib/api-client";
import type { FileMediaKind } from "../lib/file-media";
import { PanelMessage, RetryButton } from "./WorkspaceDiffView";

export function FileMediaPreview({ hostId, kind, path, sessionId, source, version }: {
	hostId?: string;
	kind: FileMediaKind;
	path: string;
	sessionId: string;
	source: FilesSource;
	version: number | string;
}) {
	const { t } = useTranslation();
	const { baseUrl: remoteBaseUrl } = useHostConnection(hostId);
	const [failedSrc, setFailedSrc] = useState<string | null>(null);
	const base = hostId ? remoteBaseUrl : getApiBaseUrl();
	// Use the streamed preview route: the image-diff blob route buffers whole
	// revisions and caps them at 16 MiB, which is too small for recordings.
	const rawUrl = source.kind === "artifact" ? source.rawUrl : base === undefined ? undefined
		: `${base}/api/v1/sessions/${encodeURIComponent(sessionId)}/preview/files/${path.split("/").map(encodeURIComponent).join("/")}?raw=true&v=${encodeURIComponent(version)}`;
	const src = source.kind === "artifact" && rawUrl ? `${rawUrl}${rawUrl.includes("?") ? "&" : "?"}v=${encodeURIComponent(version)}` : rawUrl;
	if (!src || failedSrc === src) {
		return <PanelMessage action={src ? <RetryButton onClick={() => setFailedSrc(null)} /> : undefined}>{t("files.error.loadFile")}</PanelMessage>;
	}
	const onError = () => setFailedSrc(src);
	return (
		<div className="grid min-w-0 place-items-center p-3">
			{kind === "image" ? (
				<img alt={path} aria-label={path} className="max-h-[70vh] max-w-full object-contain" key={src} onError={onError} src={src} />
			) : kind === "video" ? (
				<video aria-label={path} className="max-h-[70vh] max-w-full" controls key={src} onError={onError} playsInline preload="metadata" src={src} />
			) : (
				<audio aria-label={path} className="w-full max-w-lg" controls key={src} onError={onError} preload="metadata" src={src} />
			)}
		</div>
	);
}
