import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, ChevronDown, ChevronRight, Copy, Loader2, RotateCcw } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useCloudCp } from "../hooks/useCloudCp";
import { useCloudScriptRuns } from "../hooks/useCloudScriptRuns";
import { cloudSessionsQueryKey } from "../hooks/useWorkspaceQuery";
import { CloudCpError } from "../lib/cloud-cp";
import { isWorkspaceStartupError, type CloudStartupProblem } from "../lib/cloud-lifecycle";
import { latestScriptRun } from "../lib/cloud-script-runs";
import { cn } from "../lib/utils";
import { CoderWorkspaceLinkButton } from "./CoderWorkspaceLinkButton";
import { ScriptRunPanel } from "./ScriptRunPanel";
import { Button } from "./ui/button";

/**
 * How long a workspace retry may show no new Setup run before the card stops
 * waiting and offers Retry again. A checkout and Setup that run report their
 * own run long before this; it only covers a retry nothing visibly picked up.
 */
const RETRY_PICKUP_LIMIT_MS = 5 * 60_000;

/** True once `waiting` has held for {@link RETRY_PICKUP_LIMIT_MS} since this retry. */
function useRetryStalled(waiting: boolean, retry: object | undefined): boolean {
	const [stalled, setStalled] = useState<object>();
	useEffect(() => {
		if (!waiting || !retry) return;
		const timer = window.setTimeout(() => setStalled(retry), RETRY_PICKUP_LIMIT_MS);
		return () => window.clearTimeout(timer);
	}, [retry, waiting]);
	return stalled !== undefined && stalled === retry;
}

/**
 * Replaces a cloud session's pane once its runtime has ended without a worker:
 * either AO gave up starting it (a server-provided reason plus Retry) or the
 * runtime stopped with no recorded startup reason. Never leaves the pane blank.
 */
export function CloudSessionStartupError({
	orgId,
	sessionId,
	problem,
	coderWorkspaceUrl,
}: {
	orgId: string;
	sessionId: string;
	problem: Exclude<CloudStartupProblem, { phase: "retrying" }>;
	/** Bring-your-own Coder workspace page, so the user can inspect it. */
	coderWorkspaceUrl?: string;
}) {
	const { t } = useTranslation();
	const { client } = useCloudCp();
	const queryClient = useQueryClient();
	// The worker could not prepare the workspace (checkout, working directory or
	// the project's Setup). It is already running, so a retry asks it to prepare
	// the workspace again instead of re-running the worker bootstrap.
	const workspaceProblem = problem.phase === "failed" && isWorkspaceStartupError(problem.code);
	const failureAt = problem.phase === "failed" ? problem.at : undefined;
	// A workspace retry only queues the request: the worker reports again once
	// the checkout and Setup finish, which can take minutes. Until that retry
	// visibly ends (or the session starts and this screen goes away), show that
	// it is running rather than the old failure with Retry enabled.
	const [retried, setRetried] = useState<{ failureAt: string; afterSequence: number }>();
	const awaitingRetry = workspaceProblem && retried !== undefined && retried.failureAt === failureAt;
	// The step log, also read while a retry runs: after Retry the worker runs
	// Setup again, and listening on the session's stream shows that run live.
	const scriptRuns = useCloudScriptRuns(orgId, sessionId, { live: awaitingRetry });
	const latestRun = latestScriptRun(scriptRuns, "setup");
	// The Setup run the retry started, if any: one that began after Retry.
	const retryRun = awaitingRetry && latestRun && latestRun.firstSequence > retried.afterSequence ? latestRun : undefined;
	const retryStalled = useRetryStalled(awaitingRetry && !retryRun, retried);
	// The retry is over once a newer failure arrives (a new `at`), its Setup
	// run ends without succeeding (the control plane may re-send the same
	// failure unchanged), or nothing started for a long while. A Setup that
	// succeeded keeps it up until the agent starts and this screen goes away.
	const rerunning = awaitingRetry
		&& !(retryRun && (retryRun.status === "failed" || retryRun.status === "unknown" || retryRun.status === "interrupted"))
		&& !retryStalled;
	const retry = useMutation({
		mutationFn: async (): Promise<number> => {
			// Runs that exist now are not the retry's, whatever arrives later.
			const afterSequence = Math.max(0, ...scriptRuns.map((run) => run.lastSequence));
			if (workspaceProblem) await client.requestWorkspaceCheckout(orgId, sessionId);
			else await client.retrySessionStartup(orgId, sessionId);
			return afterSequence;
		},
		onSuccess: async (afterSequence) => {
			if (workspaceProblem && failureAt !== undefined) setRetried({ failureAt, afterSequence });
			await Promise.allSettled([
				queryClient.invalidateQueries({ queryKey: cloudSessionsQueryKey }),
				queryClient.invalidateQueries({ queryKey: ["cloud-session"] }),
			]);
		},
	});
	const failed = problem.phase === "failed";
	const failureDetail = problem.phase === "failed" ? problem.detail : undefined;
	// The workspace's ready check never passed: the server message names the
	// check, and the details carry its last output.
	const setupNotReady = problem.phase === "failed" && problem.code === "workspace_setup_not_ready";
	const projectSetupFailed = problem.phase === "failed" && problem.code === "project_setup_failed";
	const title = rerunning
		? t("cloud.startupError.workspaceRerunningTitle")
		: setupNotReady
		? t("cloud.startupError.workspaceSetupNotReadyTitle")
		: projectSetupFailed
			? t("cloud.startupError.projectSetupFailedTitle")
			: problem.phase === "failed" && problem.code === "repository_checkout_failed"
				? t("cloud.startupError.repositoryCheckoutFailedTitle")
				: problem.phase === "failed" && problem.code === "working_directory_unusable"
					? t("cloud.startupError.workingDirectoryUnusableTitle")
					: failed ? t("cloud.startupError.title") : t("cloud.startupError.unavailableTitle");
	// The failed Setup run as a step log, or the retry's run while it goes.
	const latestSetup = projectSetupFailed || rerunning ? latestRun : undefined;
	const setupRun = latestSetup && (latestSetup.status === "failed" || (rerunning && latestSetup.status === "running")) ? latestSetup : undefined;
	// A failed Setup step's output is what the user needs to fix the script, so
	// it starts expanded, unless the step log below already shows it.
	const [detailToggled, setShowDetail] = useState<boolean>();
	const showDetail = detailToggled ?? (projectSetupFailed && !setupRun);
	const [copied, setCopied] = useState(false);
	const copyDetail = () => {
		if (problem.phase !== "failed") return;
		const text = [problem.code, problem.message, failureDetail].filter(Boolean).join("\n\n");
		void navigator.clipboard?.writeText(text).then(() => setCopied(true), () => undefined);
	};
	// The previous failure's output is stale while the workspace is prepared again.
	const detail = rerunning ? undefined : failureDetail;
	const retryError = retry.isError
		? retry.error instanceof CloudCpError && retry.error.code === "startup_retry_unavailable"
			? t("cloud.startupError.retryUnavailable")
			: t("cloud.startupError.retryFailed", { message: retry.error instanceof Error ? retry.error.message : String(retry.error) })
		: undefined;
	return (
		<div
			className={cn("absolute inset-0 z-chrome grid place-items-center bg-background p-4", setupRun && "overflow-y-auto")}
			data-testid="cloud-session-startup-error"
		>
			<div className={cn("flex max-w-full flex-col items-center gap-4", setupRun ? "w-[36rem]" : "w-96")}>
				<div className="flex w-full flex-col items-center gap-3 text-center" role="alert">
					<h2 className="text-heading-sm font-semibold text-foreground">
						{title}
					</h2>
					<p className="text-sm leading-relaxed text-muted-foreground">
						{rerunning ? t("cloud.startupError.workspaceRerunning") : problem.message ?? t("cloud.startupError.unavailableDescription")}
					</p>
					{failed ? (
						<Button
							type="button"
							variant="outline"
							className="mt-1"
							disabled={retry.isPending || rerunning}
							onClick={() => retry.mutate()}
						>
							{retry.isPending || rerunning
								? <Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
								: <RotateCcw aria-hidden="true" className="size-3.5" />}
							{retry.isPending || rerunning ? t("cloud.startupError.retrying") : t("cloud.startupError.retry")}
						</Button>
					) : null}
					{retryError ? <p className="text-xs text-error">{retryError}</p> : null}
					{detail || coderWorkspaceUrl ? (
						<div className="flex w-full flex-col items-center gap-2">
							<div className="flex items-center gap-1">
								{detail ? (
									<>
										<Button type="button" variant="ghost" size="sm" onClick={() => setShowDetail(!showDetail)}>
											{showDetail
												? <ChevronDown aria-hidden="true" className="size-3.5" />
												: <ChevronRight aria-hidden="true" className="size-3.5" />}
											{showDetail ? t("cloud.startupError.hideDetails") : t("cloud.startupError.showDetails")}
										</Button>
										<Button type="button" variant="ghost" size="sm" onClick={copyDetail}>
											{copied ? <Check aria-hidden="true" className="size-3.5" /> : <Copy aria-hidden="true" className="size-3.5" />}
											{copied ? t("cloud.startupError.copied") : t("cloud.startupError.copyDetails")}
										</Button>
									</>
								) : null}
								<CoderWorkspaceLinkButton url={coderWorkspaceUrl} />
							</div>
							{detail && showDetail ? (
								<pre
									className="max-h-48 w-full overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted p-2 text-left font-mono text-xs text-muted-foreground select-text"
									data-testid="cloud-session-startup-detail"
								>
									{detail}
								</pre>
							) : null}
						</div>
					) : null}
				</div>
				{/* Outside the alert, so live Setup output is not announced as an alert. */}
				{setupRun ? <ScriptRunPanel className="w-full" run={setupRun} /> : null}
			</div>
		</div>
	);
}
