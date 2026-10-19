import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TriageRun } from "../../../shared/triage";
import { useTriageStore } from "../../stores/triage-store";
import { aoBridge } from "../../lib/bridge";
import { isTriageActive, triageManualIssueUrl, triagePublicUrl, triageReportText } from "../../lib/triage";
import { sanitizeReportText } from "../../lib/report-problem";
import { triageConversationSnapshot, triageInputActivity, triageInputAnswer } from "../../lib/triage-conversation";
import { ChatWorkspace } from "../chat/ChatWorkspace";
import { Button } from "../ui/button";
import { TooltipProvider } from "../ui/tooltip";

const states = {
	collecting: "triage.state.collecting", starting: "triage.state.starting", working: "triage.state.working",
	dispatching: "triage.state.working", running: "triage.state.working", publishing: "triage.state.publishing", published: "triage.state.published", prepared: "triage.state.report_saved",
	needs_input: "triage.state.needs_input", report_saved: "triage.state.report_saved", completed: "triage.state.completed",
	interrupted: "triage.state.interrupted", cancelled: "triage.state.cancelled", failed: "triage.state.failed",
} as const;
const publications = {
	prepared: "triage.publication.prepared", issue_pending: "triage.publication.issue_pending", issue_unknown: "triage.publication.issue_unknown",
	issue_failed: "triage.publication.issue_failed", evidence_pending: "triage.publication.evidence_pending", evidence_unknown: "triage.publication.evidence_unknown",
	evidence_failed: "triage.publication.evidence_failed", published: "triage.publication.published", duplicate: "triage.publication.duplicate",
	auth_required: "triage.publication.auth_required", blocked: "triage.publication.blocked",
} as const;

export function useTriageStateLabel(run: TriageRun): string {
	const { t } = useTranslation();
	return t(states[run.state as keyof typeof states] ?? "triage.state.unknown");
}

export function TriageConversation({ runId }: { runId: string }) {
	const { t } = useTranslation();
	const run = useTriageStore((state) => state.runs.find((item) => item.id === runId));
	const error = useTriageStore((state) => state.runErrors[runId]);
	useEffect(() => { void useTriageStore.getState().loadRun(runId); }, [runId]);
	if (!run) return <p role={error ? "alert" : "status"} className="p-6 text-sm text-muted-foreground">{error ? sanitizeReportText(error) : t("triage.loading")}</p>;
	return <Conversation key={run.id} run={run} />;
}

function Conversation({ run }: { run: TriageRun }) {
	const { t } = useTranslation();
	const stateLabel = useTriageStateLabel(run);
	const busy = useTriageStore((state) => state.pending[run.id]);
	const error = useTriageStore((state) => state.runErrors[run.id]);
	const [copyStatus, setCopyStatus] = useState<string>();
	const draftOwner = useMemo(() => ({ sessionId: `triage:${run.id}`, incarnation: run.createdAt }), [run.id, run.createdAt]);
	const active = isTriageActive(run);
	const publication = run.publication;
	const issueUrl = triagePublicUrl(publication.issueUrl);
	const evidenceUrl = triagePublicUrl(publication.evidenceUrl);
	const publicationLabel = publications[publication.state as keyof typeof publications];
	const reportText = triageReportText(run);
	const hasReportDraft = !!(publication.body || run.draft?.body);
	const canCheck = ["issue_unknown", "evidence_unknown"].includes(publication.state);
	const canPublish = ["prepared", "issue_failed", "evidence_failed"].includes(publication.state) && hasReportDraft;
	const result = [
		publicationLabel ? t(publicationLabel) : "",
		issueUrl ? `[${t(publication.state === "duplicate" ? "triage.existingIssue" : "triage.viewIssue")}](${issueUrl})` : "",
		evidenceUrl ? `[${t("triage.viewEvidence")}](${evidenceUrl})` : "",
	].filter(Boolean).join("\n\n");
	const snapshot = useMemo(() => triageConversationSnapshot(run, result), [run, result]);
	const inputRequest = useMemo(() => run.pendingInput ? triageInputActivity(run, { other: t("triage.somethingElse"), unsure: t("triage.unsure"), details: t("triage.reply") }) : undefined, [run, t]);
	const copy = async (open = false) => {
		try {
			await aoBridge.clipboard.writeText(reportText);
			if (open) await aoBridge.app.openExternal(triageManualIssueUrl(publication.title || run.draft?.title || t("triage.manualTitle"), reportText));
			setCopyStatus(t("triage.copied"));
		} catch (error) { setCopyStatus(sanitizeReportText(error instanceof Error ? error.message : t("report.copyFailed"))); }
	};
	return <TooltipProvider><ChatWorkspace
		snapshot={snapshot} inputRequest={inputRequest} draftOwner={draftOwner} sessionTitle={t("triage.reportProblem")} inlineHeader
		busy={!!busy} newWorkDisabled={!active}
		commandError={sanitizeReportText(error || run.error || publication.error || "") || undefined}
		headerActions={<>
			<span className="px-2 text-xs text-muted-foreground" role="status">{copyStatus || (busy && publication.state === "auth_required" ? t("triage.waitingGitHubSignIn") : stateLabel)}</span>
			{run.state === "running" && run.nativeControlsAvailable && !run.pendingInput ? <Button size="sm" variant="ghost" disabled={busy} onClick={() => void useTriageStore.getState().nativeInput(run.id)}>{t("triage.nativeInput")}</Button> : null}
			{publication.state === "auth_required" ? <Button size="sm" disabled={busy} onClick={() => void useTriageStore.getState().signInGitHub(run.id)}>{t("triage.signInGitHub")}</Button> : null}
			{canCheck || canPublish ? <Button size="sm" variant="ghost" disabled={busy} onClick={() => void useTriageStore.getState().publish(run.id)}>{t(canCheck ? "triage.checkSubmission" : "triage.retryPublish")}</Button> : null}
			{!active && reportText ? <Button size="sm" variant="ghost" onClick={() => void copy()}>{t("triage.copyReport")}</Button> : null}
			{hasReportDraft && ["auth_required", "blocked", "issue_failed"].includes(publication.state) ? <Button size="sm" variant="ghost" onClick={() => void copy(true)}>{t("triage.openDraft")}</Button> : null}
			{active || busy && publication.state === "auth_required" ? <Button size="sm" variant="ghost" disabled={busy && publication.state !== "auth_required"} onClick={() => void useTriageStore.getState().cancel(run.id)}>{t("triage.stop")}</Button> : null}
		</>}
		onSend={(text) => useTriageStore.getState().reply(run.id, undefined, undefined, text)}
		onResolveInput={async (_requestId, action, content) => {
			if (action === "cancel") return useTriageStore.getState().cancel(run.id);
			if (action === "decline") {
				const waiting = run.nativeControlsAvailable && run.pendingInput?.origin === "native" && run.pendingInput.questions.find((question) => question.options?.includes("Keep waiting"));
				return useTriageStore.getState().reply(run.id, run.pendingInput?.id, waiting ? { [waiting.id]: ["Keep waiting"] } : undefined, waiting ? "" : "I'm not sure");
			}
			const reply = triageInputAnswer(run, content ?? {});
			return useTriageStore.getState().reply(run.id, run.pendingInput?.id, reply.answers, reply.text);
		}}
		onLinkOpen={(url) => { void aoBridge.app.openExternal(url); }}
	/></TooltipProvider>;
}
