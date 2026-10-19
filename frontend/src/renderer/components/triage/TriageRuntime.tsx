import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { CircleHelp, Loader2 } from "lucide-react";
import { aoBridge } from "../../lib/bridge";
import { configuredTriageSnapshot, isTriageActive, isTriageSessionRun, triageHref } from "../../lib/triage";
import { queryClient } from "../../lib/query-client";
import type { TriageChoice, TriageRun } from "../../../shared/triage";
import { useTriageStore } from "../../stores/triage-store";
import { useTriageStateLabel } from "./TriageConversation";
import { sanitizeReportText } from "../../lib/report-problem";
import { Button } from "../ui/button";

const sessionPublicationLabels = {
	auth_required: "triage.publication.auth_required", issue_pending: "triage.publication.issue_pending", issue_unknown: "triage.publication.issue_unknown",
	issue_failed: "triage.publication.issue_failed", evidence_pending: "triage.publication.evidence_pending", evidence_unknown: "triage.publication.evidence_unknown", evidence_failed: "triage.publication.evidence_failed",
} as const;

/** Lives outside the router and the daemon gate so navigation cannot stop a run. */
export function TriageRuntime() {
	const { t } = useTranslation();
	const sessionRun = useTriageStore((state) => state.runs.find((item) => isTriageSessionRun(item) && (item.searchAuthRequired || item.publication.state in sessionPublicationLabels)));
	const run = useTriageStore((state) => state.runs.find((item) => !item.preparation && !isTriageSessionRun(item) && item.state === "needs_input") ?? state.runs.find(isTriageActive) ?? state.runs.find((item) => !item.preparation && !isTriageSessionRun(item) && item.id === state.attentionId));
	useEffect(() => {
		void useTriageStore.getState().loadRuns();
		const dispose = aoBridge.triage.subscribe((run) => useTriageStore.getState().receive(run, true));
		return dispose;
	}, []);
	useEffect(() => {
		let active = true;
		let syncing = false;
		let changed = false;
		let native: TriageChoice[] | undefined;
		let saved = "";
		// Retain existing display data for daemon outages. Never fetch readiness,
		// load model catalogs, or start a provider to fill a missing cache entry.
		const sync = async () => {
			changed = true;
			if (syncing || !queryClient.getQueryData(["settings"]) || !queryClient.getQueryData(["agent-readiness"])) return;
			syncing = true;
			try {
				const discovered = native ?? await aoBridge.triage.catalog();
				native = discovered;
				while (active && changed) {
					changed = false;
					const choices = configuredTriageSnapshot(queryClient, discovered);
					if (!choices) continue;
					const fingerprint = JSON.stringify(choices.map(({ harness, models, modelsMissing, accounts, mode, permissions }) => ({ harness, models, modelsMissing, accounts, mode, permissions })));
					if (fingerprint === saved) continue;
					await aoBridge.triage.configure(choices);
					saved = fingerprint;
				}
			} catch {
				// The launcher owns visible helper errors. Retry on the next actual
				// configuration update, rather than polling or changing its selection.
				native = undefined;
			} finally { syncing = false; }
		};
		const unsubscribe = queryClient.getQueryCache().subscribe((event) => {
			if (event.type !== "updated" || event.action.type !== "success") return;
			const key = event.query.queryKey;
			if ((key.length === 1 && (key[0] === "settings" || key[0] === "agent-readiness" || key[0] === "agent-accounts")) ||
				(key[0] === "project" && key.length === 2) ||
				(key[0] === "agent-models" && (key.length === 3 || (key.length === 4 && key[3] === "catalog")))) void sync();
		});
		void sync();
		return () => { active = false; unsubscribe(); };
	}, []);
	return sessionRun ? <SessionPublicationAction run={sessionRun} /> : run ? <TriageIndicator run={run} label={t("triage.title")} /> : null;
}

function SessionPublicationAction({ run }: { run: TriageRun }) {
	const { t } = useTranslation();
	const pending = useTriageStore((state) => state.pending[run.id]);
	const error = useTriageStore((state) => state.runErrors[run.id]);
	const auth = run.searchAuthRequired || run.publication.state === "auth_required";
	const check = ["issue_pending", "evidence_pending", "issue_unknown", "evidence_unknown"].includes(run.publication.state);
	const label = sessionPublicationLabels[run.publication.state as keyof typeof sessionPublicationLabels];
	return <div className="fixed bottom-4 right-4 z-overlay flex max-w-sm flex-col gap-2 rounded-lg border border-border bg-background p-3 text-xs shadow-lg" data-testid="session-report-action">
		<p role="status">{run.searchAuthRequired ? t("triage.signInGitHub") : pending && auth ? t("triage.waitingGitHubSignIn") : t(label)}</p>
		{error || run.publication.error ? <p role="alert" className="text-destructive">{sanitizeReportText(error || run.publication.error || "")}</p> : null}
		<div className="flex items-center gap-3">
			<Button size="sm" disabled={pending} onClick={() => void (auth ? useTriageStore.getState().signInGitHub(run.id) : useTriageStore.getState().publish(run.id))}>
				{pending ? <Loader2 className="size-3 animate-spin" aria-hidden="true" /> : null}{t(auth ? "triage.signInGitHub" : check ? "triage.checkSubmission" : "triage.retryPublish")}
			</Button>
			{run.sessionId ? <a className="text-primary underline underline-offset-2" href={`#/sessions/${encodeURIComponent(run.sessionId)}`}>{t("notify.openSessionTitle")}</a> : null}
		</div>
	</div>;
}

function TriageIndicator({ run, label }: { run: import("../../../shared/triage").TriageRun; label: string }) {
	const stateLabel = useTriageStateLabel(run);
	return <a href={triageHref(run.id)} aria-label={`${label}: ${stateLabel}`}
		className="fixed bottom-4 right-4 z-overlay inline-flex max-w-xs items-center gap-2 rounded-lg border border-border bg-background px-3 py-2 text-xs shadow-lg">
		<CircleHelp className="size-4 shrink-0" aria-hidden="true" /><span aria-live="polite">{label} · {stateLabel}</span>
	</a>;
}
