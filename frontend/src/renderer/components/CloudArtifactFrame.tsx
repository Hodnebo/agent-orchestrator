import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { cloudCpFetch } from "../lib/cloud-cp/renderer-client";
import { PanelMessage } from "./WorkspaceDiffView";

/**
 * A cloud session's HTML artifact, framed from the control plane. The frame
 * is sandboxed without allow-same-origin or popups, so the page runs in an
 * opaque origin and cannot open a window; the control plane's own
 * Content-Security-Policy sandboxes it too and blocks its network requests.
 * A file whose stored object is gone shows as unavailable; a refused link
 * calls onAccessRefused so the caller can take a fresh one.
 */
export function CloudArtifactFrame({ title, url, onAccessRefused }: { title: string; url: string; onAccessRefused?: () => void }) {
	const { t } = useTranslation();
	const status = useQuery({
		queryKey: ["cloud-artifact-status", url],
		queryFn: async () => (await cloudCpFetch(url, { method: "HEAD" })).status,
		retry: false,
	});
	const refused = status.data === 401 || status.data === 403;
	useEffect(() => {
		if (refused) onAccessRefused?.();
	}, [refused, onAccessRefused]);
	if (status.isPending || refused) return <PanelMessage>{t("files.loading")}</PanelMessage>;
	if (status.data === 404) return <PanelMessage>{t("artifacts.unavailable")}</PanelMessage>;
	if (status.isError || (status.data !== undefined && status.data >= 400)) return <PanelMessage>{t("artifacts.accessError")}</PanelMessage>;
	return (
		<iframe
			className="block h-full w-full border-0 bg-white"
			referrerPolicy="no-referrer"
			sandbox="allow-scripts allow-forms"
			src={url}
			title={title}
		/>
	);
}
