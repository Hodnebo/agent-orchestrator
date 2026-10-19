import { useEffect, useRef, useState } from "react";
import { queryClient } from "../../lib/query-client";
import { useTranslation } from "react-i18next";
import { ChevronDown, Loader2 } from "lucide-react";
import { useTriageStore } from "../../stores/triage-store";
import { cachedTriageOrigin, configuredTriageSnapshot, currentTriageOrigin, defaultTriageSelection, triageDiagnosticPrompt, triageEvidenceText, triageHref, triageManualIssueUrl, validTriageSelection } from "../../lib/triage";
import { aoBridge } from "../../lib/bridge";
import { collectReportProblemDiagnostics, sanitizeReportText } from "../../lib/report-problem";
import { TRIAGE_APPROVAL_REQUIRED, type TriageOrigin, type TriageSelection } from "../../../shared/triage";
import { AgentSelectMenuItem } from "../settings/AgentSelectMenuItem";
import { workspaceQueryKey } from "../../hooks/useWorkspaceQuery";
import { sessionNavigateTarget } from "../../lib/navigate-to-session";
import { Button } from "../ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "../ui/dropdown-menu";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "../ui/dialog";
import { useUiStore } from "../../stores/ui-store";

export function TriageLauncher({ active = true, origin, observation }: { active?: boolean; origin?: TriageOrigin; observation?: string }) {
	const { t } = useTranslation();
	const state = useTriageStore();
	const [open, setOpen] = useState(false);
	const openRef = useRef(false);
	const preparationGeneration = useRef<number | null>(null);
	const [launching, setLaunching] = useState(false);
	const [failed, setFailed] = useState(false);
	const [fallbackStatus, setFallbackStatus] = useState<string>();
	const choice = state.choices.find((item) => item.harness === state.selection?.harness);
	const preparation = state.runs.find((run) => run.id === state.preparationId);
	const uncertainSession = preparation?.execution === "session" && !preparation.sessionId;
	const discardOwnPreparation = () => {
		const current = useTriageStore.getState();
		if (!current.starting && preparationGeneration.current === current.preparationGeneration) current.discardPreparation();
	};
	const close = (value: boolean) => {
		openRef.current = value;
		setOpen(value);
		if (!value) discardOwnPreparation();
	};
	useEffect(() => () => {
		if (openRef.current) discardOwnPreparation();
	}, []);
	const changeSelection = (selection: TriageSelection) => {
		state.setSelection(selection);
		useTriageStore.setState({ error: null });
		setFailed(false);
	};
	const prepare = async () => {
		close(true);
		setFailed(false);
		setFallbackStatus(undefined);
		const context = cachedTriageOrigin(queryClient, origin ?? currentTriageOrigin());
		state.setContext(context, observation);
		void state.prepare(context);
		preparationGeneration.current = useTriageStore.getState().preparationGeneration;
		await state.loadCatalog((native) => configuredTriageSnapshot(queryClient, native));
		if (!openRef.current || preparationGeneration.current !== useTriageStore.getState().preparationGeneration) return;
		const current = useTriageStore.getState();
		const pendingSession = current.runs.find((run) => run.execution === "session" && !run.sessionId);
		if (current.error && !pendingSession) { setFailed(true); await collectFallback(); return; }
		if (!current.choices.length && !pendingSession) {
			useTriageStore.setState({ error: t("triage.empty") });
			setFailed(true); await collectFallback(); return;
		}
		const selection = pendingSession?.request ?? defaultTriageSelection(queryClient, current.choices, current.selection, context);
		useTriageStore.setState({ selection });
	};
	const collectFallback = async () => {
		const diagnostics = await collectReportProblemDiagnostics();
		const current = useTriageStore.getState();
		const preparation = current.runs.find((run) => run.id === current.preparationId);
		const body = sanitizeReportText([current.observation.trim(), `AO ${diagnostics.appVersion}, ${diagnostics.platform}`,
			`Daemon: ${diagnostics.daemonState}`, current.origin.daemonCode, current.origin.daemonOutput, preparation ? triageEvidenceText(preparation) : "", current.error, current.preparationError].filter(Boolean).join("\n\n"));
		return body;
	};
	const start = async (withoutApprovals = false) => {
		if (launching || state.starting) return;
		setLaunching(true);
		setFailed(false);
		try {
			const current = useTriageStore.getState();
			const uncertain = current.runs.find((run) => run.id === current.preparationId && run.execution === "session" && !run.sessionId);
			const resolved = defaultTriageSelection(queryClient, current.choices, current.selection, current.origin);
			const configured = current.choices.find((item) => item.harness === resolved?.harness);
			const { mode: _mode, permissions: _permissions, ...defaults } = resolved ?? {};
			const selection = uncertain?.request ?? (resolved ? { ...defaults, harness: resolved.harness,
				...(configured?.mode ? { mode: configured.mode } : {}), ...(configured?.permissions ? { permissions: configured.permissions } : {}),
			} : null);
			if (!uncertain && !validTriageSelection(current.choices, selection)) {
				throw new Error(configured?.unavailableReason || t(selection ? "triage.defaultUnavailable" : "triage.empty"));
			}
			useTriageStore.setState({ selection });
			const explicitlyApproved = withoutApprovals && selection?.harness === "unreal-agent" && current.error?.includes(TRIAGE_APPROVAL_REQUIRED);
			const run = await current.start(explicitlyApproved ? "bypass-permissions" : undefined);
			if (run) {
				if (run.execution === "session" && !run.sessionId) {
					useTriageStore.setState({ error: run.error || "AO could not confirm whether the session started. Retry Start investigation to check the same launch." });
					return;
				}
				close(false);
				useUiStore.setState({ settingsModal: null });
				if (run.sessionId) {
					await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
					const target = sessionNavigateTarget(undefined, run.sessionId);
					window.location.hash = target.to.replace("$sessionId", encodeURIComponent(target.params.sessionId));
				} else window.location.hash = triageHref(run.id).slice(1);
			}
			else { setFailed(true); await collectFallback(); }
		} catch (error) {
			useTriageStore.setState({ error: error instanceof Error ? error.message : String(error) });
			setFailed(true);
			await collectFallback();
		} finally { setLaunching(false); if (!openRef.current) discardOwnPreparation(); }
	};
	const manual = async (open: boolean) => {
		try {
			const body = await collectFallback();
			await aoBridge.clipboard.writeText(open ? body : triageDiagnosticPrompt(useTriageStore.getState().selection, body));
			if (open) await aoBridge.app.openExternal(triageManualIssueUrl(t("triage.manualTitle"), body));
			setFallbackStatus(t("triage.copied"));
		} catch (error) { setFallbackStatus(sanitizeReportText(error instanceof Error ? error.message : t("report.copyFailed"))); }
	};
	return <>
		<Button size="sm" disabled={!active || launching || state.starting} onClick={() => void prepare()}>{t("triage.reportProblem")}</Button>
		<Dialog open={open} onOpenChange={close}>
			<DialogContent className="max-w-md" data-testid="triage-launcher">
				<DialogHeader>
					<DialogTitle>{t("triage.reportProblem")}</DialogTitle>
					<DialogDescription>{t("triage.launcherIntro")}</DialogDescription>
				</DialogHeader>
		<div className="flex flex-wrap items-center gap-2" aria-busy={state.loading}>
			<DropdownMenu>
				<DropdownMenuTrigger asChild>
					<Button variant="outline" size="sm" disabled={state.loading || state.starting || uncertainSession || state.choices.length === 0} aria-label={t("triage.agent")}>
						{choice?.label ?? state.selection?.harness ?? t("triage.chooseAgent")}<ChevronDown className="size-3" aria-hidden="true" />
					</Button>
				</DropdownMenuTrigger>
				<DropdownMenuContent align="start" className="max-w-sm">
					{state.choices.map((item) => <DropdownMenuItem key={item.harness} className="flex-col items-stretch gap-1"
						onSelect={() => changeSelection(defaultTriageSelection(queryClient, [item], null, {}, item.harness) ?? { harness: item.harness })}>
						<AgentSelectMenuItem agentId={item.harness} label={item.label} selected={item.harness === state.selection?.harness} />
						{!item.available ? <span className="line-clamp-2 whitespace-normal break-words pl-8 text-caption leading-snug text-muted-foreground"
							title={sanitizeReportText(item.unavailableReason || t("triage.unavailable"))}>{sanitizeReportText(item.unavailableReason || t("triage.unavailable"))}</span> : null}
					</DropdownMenuItem>)}
				</DropdownMenuContent>
			</DropdownMenu>
		</div>
		{state.preparing || preparation ? <p role="status" className="text-xs text-muted-foreground">{preparation ? sanitizeReportText(preparation.activity) : t("triage.collectingInitial")}</p> : null}
		{state.preparationError ? <p className="text-xs text-destructive">{sanitizeReportText(state.preparationError)}</p> : null}
		<p className="text-xs leading-relaxed text-muted-foreground">{t("triage.disclosure")}</p>
		{!state.loading && state.catalogLoaded && !state.choices.length && !state.error ? <p className="text-xs text-muted-foreground">{t("triage.empty")}</p> : null}
		{state.error ? <p role="alert" className="text-xs text-destructive">{sanitizeReportText(state.error)}</p> : null}
		{state.selection?.harness === "unreal-agent" && state.error?.includes(TRIAGE_APPROVAL_REQUIRED) ? <Button size="sm" variant="outline" disabled={launching || state.starting}
			onClick={() => void start(true)}>{t("newTask.startWithoutApprovals", { defaultValue: "Start without approvals" })}</Button> : null}
		{failed ? <>
			<p className="text-xs text-muted-foreground">{t("triage.fallbackCollected")}</p>
			<div className="flex flex-wrap gap-2">
				<Button variant="outline" size="sm" onClick={() => void manual(false)}>{t("triage.copyReport")}</Button>
				<Button variant="ghost" size="sm" onClick={() => void manual(true)}>{t("triage.openDraft")}</Button>
			</div>
			{fallbackStatus ? <p role="status" className="text-xs text-muted-foreground">{fallbackStatus}</p> : null}
		</> : null}
		<div className="flex justify-end"><Button size="sm" disabled={state.loading || launching || state.starting || !state.choices.length && !uncertainSession} onClick={() => void start()}>
			{launching || state.starting ? <Loader2 className="size-3 animate-spin" aria-hidden="true" /> : null}{t("triage.start")}
		</Button></div>
			</DialogContent>
		</Dialog>
	</>;
}
