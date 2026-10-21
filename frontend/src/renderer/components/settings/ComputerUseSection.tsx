import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, ExternalLink, LoaderCircle } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { apiClient, apiErrorMessage } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import { Button } from "../ui/button";
import { HarnessStateText, type HarnessTone } from "./harness/harness-ui";
import type { InstallJob } from "./installState";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

type ComputerUseStatus = components["schemas"]["ComputerUseStatus"];
type PermissionState = components["schemas"]["ComputerUsePermissions"]["accessibility"];

export const computerUseStatusQueryKey = ["computer-use", "status"] as const;
const installJobQueryKey = ["system-install", "cua-driver"] as const;
const JOB_POLL_MS = 1_500;
const GRANT_POLL_MS = 2_000;
const RELEASES_URL = "https://cua.ai/docs/how-to-guides/driver/install";

async function fetchStatus(): Promise<ComputerUseStatus> {
	const { data, error } = await apiClient.GET("/api/v1/computer-use/status");
	if (error || !data) throw new Error(apiErrorMessage(error));
	return data;
}

async function fetchInstallJob(): Promise<InstallJob> {
	const { data, error } = await apiClient.GET("/api/v1/system/install/{target}", { params: { path: { target: "cua-driver" } } });
	if (error || !data) throw new Error(apiErrorMessage(error));
	return data;
}

/**
 * Cua Driver, the computer-use driver agents reach through `ao cua`. Installs
 * through the daemon's fixed installer allowlist like the agent CLIs do, then
 * shows the facts that decide whether agents can use it: the background
 * service and, on macOS, the privacy permissions CuaDriver.app holds.
 */
export function ComputerUseSection({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const status = useQuery({
		queryKey: computerUseStatusQueryKey,
		queryFn: fetchStatus,
		// A permission request waits on the user in System Settings; follow it.
		refetchInterval: (query) => (query.state.data?.permissions?.granting ? GRANT_POLL_MS : false),
	});
	const job = useQuery({
		queryKey: installJobQueryKey,
		queryFn: fetchInstallJob,
		refetchInterval: (query) => (query.state.data?.status === "running" ? JOB_POLL_MS : false),
	});
	const [action, setAction] = useState<"install" | "start" | "grant" | "doctor" | null>(null);
	const [actionError, setActionError] = useState<string | null>(null);
	const [doctorReport, setDoctorReport] = useState<string | null>(null);
	const [showOutput, setShowOutput] = useState(false);
	const [copied, setCopied] = useState(false);

	// A finished install changes what status reports; refresh it once.
	const jobStatus = job.data?.status;
	const previousJobStatus = useRef(jobStatus);
	useEffect(() => {
		if (previousJobStatus.current === "running" && jobStatus && jobStatus !== "running") {
			void queryClient.invalidateQueries({ queryKey: computerUseStatusQueryKey });
		}
		previousJobStatus.current = jobStatus;
	}, [jobStatus, queryClient]);

	const run = async (name: NonNullable<typeof action>, work: () => Promise<void>) => {
		setAction(name);
		setActionError(null);
		try {
			await work();
		} catch (cause) {
			setActionError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			setAction(null);
		}
	};
	const install = () => run("install", async () => {
		const { data, error } = await apiClient.POST("/api/v1/system/install/{target}", { params: { path: { target: "cua-driver" } } });
		if (error || !data) throw new Error(apiErrorMessage(error));
		setShowOutput(false);
		queryClient.setQueryData(installJobQueryKey, data);
	});
	const startService = () => run("start", async () => {
		const { data, error } = await apiClient.POST("/api/v1/computer-use/daemon/start");
		if (error || !data) throw new Error(apiErrorMessage(error));
		queryClient.setQueryData(computerUseStatusQueryKey, data);
	});
	const grant = () => run("grant", async () => {
		const { data, error } = await apiClient.POST("/api/v1/computer-use/permissions/grant");
		if (error || !data) throw new Error(apiErrorMessage(error));
		queryClient.setQueryData(computerUseStatusQueryKey, data);
	});
	const doctor = () => run("doctor", async () => {
		const { data, error } = await apiClient.GET("/api/v1/computer-use/doctor");
		if (error || !data) throw new Error(apiErrorMessage(error));
		setDoctorReport(data.report);
	});
	const copyCommand = async (command: string) => {
		await navigator.clipboard.writeText(command);
		setCopied(true);
		window.setTimeout(() => setCopied(false), 1_500);
	};

	const current = status.data;
	const installed = Boolean(current?.installed);
	const installing = action === "install" || jobStatus === "running";
	const failed = jobStatus === "failed" || jobStatus === "interrupted";
	// Unsupported with a command: AO knows the command but cannot run it here.
	const manualCommand = jobStatus === "unsupported" ? job.data?.command : undefined;
	const permissions = current?.permissions;

	const driverAction = installing ? (
		<span role="status" className="inline-flex items-center gap-1.5 text-xs text-settings-muted">
			<LoaderCircle className="size-3.5 animate-spin" aria-hidden="true" />
			{t(installed ? "settings.computerUse.updating" : "settings.computerUse.installing")}
		</span>
	) : !installed && manualCommand ? (
		<Button type="button" size="sm" variant="ghost" onClick={() => void copyCommand(manualCommand)}>
			{copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
			{t(copied ? "settings.harness.copied" : "settings.harness.copyCommand")}
		</Button>
	) : !installed && status.isSuccess ? (
		<Button type="button" size="sm" variant="ghost" onClick={() => void install()}>
			{t(failed ? "settings.computerUse.retryInstall" : "settings.computerUse.install")}
		</Button>
	) : installed && current?.updateAvailable && current.latestVersion ? (
		<Button type="button" size="sm" variant="ghost" onClick={() => void install()}>
			{t("settings.computerUse.update", { version: current.latestVersion })}
		</Button>
	) : installed ? (
		<span className="text-xs text-settings-muted">{t("settings.computerUse.installed")}</span>
	) : null;

	const driverDescription = failed && job.data?.error ? <span className="text-error">{job.data.error}</span>
		: !installed && manualCommand ? job.data?.error || t("settings.computerUse.manualInstall")
		: installed && current?.version ? <span className="font-mono">{current.version}</span>
		: t("settings.computerUse.driverDescription");

	return (
		<>
			<SettingsSection title={t("settings.computerUse")} sectionId="computerUse" titleHidden={titleHidden} grouped>
				<SettingsRow label={t("settings.computerUse.driver")} description={driverDescription}>
					{driverAction}
				</SettingsRow>
				{installed && current ? (
					<>
						<SettingsRow label={t("settings.computerUse.service")} description={current.daemon === "stopped" ? t("settings.computerUse.serviceStopped") : undefined}>
							<span className="flex items-center gap-3">
								<HarnessStateText tone={serviceTone(current.daemon)}>{t(`settings.computerUse.daemon.${current.daemon}`)}</HarnessStateText>
								{current.daemon !== "running" ? (
									<Button type="button" size="sm" variant="ghost" disabled={action !== null} onClick={() => void startService()}>
										{action === "start" ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : null}
										{t("settings.computerUse.start")}
									</Button>
								) : null}
							</span>
						</SettingsRow>
						{permissions ? (
							<SettingsRow
								label={t("settings.computerUse.permissions")}
								description={permissions.grantError ? <span className="text-error">{permissions.grantError}</span>
									: current.daemon !== "running" ? t("settings.computerUse.permissionsNeedService")
									// All granted reads from the trailing state alone; otherwise name each one.
									: allGranted(permissions.accessibility, permissions.screenRecording) ? undefined
									: `${t("settings.computerUse.accessibility")}: ${t(`settings.computerUse.permission.${permissions.accessibility}`)} · ${t("settings.computerUse.screenRecording")}: ${t(`settings.computerUse.permission.${permissions.screenRecording}`)}`}
							>
								{permissions.granting ? (
									<span role="status" className="inline-flex items-center gap-1.5 text-xs text-settings-muted">
										<LoaderCircle className="size-3.5 animate-spin" aria-hidden="true" />
										{t("settings.computerUse.granting")}
									</span>
								) : allGranted(permissions.accessibility, permissions.screenRecording) ? (
									<span className="text-xs text-settings-muted">{t("settings.computerUse.permission.granted")}</span>
								) : (
									<Button type="button" size="sm" variant="ghost" disabled={action !== null} onClick={() => void grant()}>
										{t("settings.computerUse.grant")}
									</Button>
								)}
							</SettingsRow>
						) : null}
						<SettingsRow label={t("settings.computerUse.agentAccess")} description={t("settings.computerUse.agentAccessDescription")}>
							<code className="rounded bg-(--color-bg-settings-input) px-1.5 py-0.5 font-mono text-xs text-settings-label">ao cua</code>
						</SettingsRow>
					</>
				) : null}
				{status.isError ? (
					<div className="settings-content-row">
						<p role="alert" className="text-xs text-error">{status.error instanceof Error ? status.error.message : t("settings.computerUse.loadFailed")}</p>
					</div>
				) : null}
			</SettingsSection>

			{actionError ? <p role="alert" className="px-(--settings-row-inset) text-xs text-error">{actionError}</p> : null}

			{installed && current ? (
				<SettingsSection
					title={t("settings.computerUse.details")}
					sectionId="computerUseDetails"
					grouped
					action={
						<Button type="button" size="sm" variant="ghost" className="text-settings-muted hover:text-settings-label" disabled={action !== null} onClick={() => void doctor()}>
							{action === "doctor" ? <LoaderCircle className="animate-spin" aria-hidden="true" /> : null}
							{t("settings.computerUse.runDoctor")}
						</Button>
					}
				>
					<SettingsRow label={t("settings.computerUse.executable")} description={<span className="break-all font-mono">{current.binaryPath}</span>} />
					<SettingsRow label={t("settings.computerUse.screenshots")} description={<span className="break-all font-mono">{current.imageDirectory}</span>} />
					{current.updateAvailable && current.releaseNotesUrl ? (
						<SettingsRow label={t("settings.computerUse.latestVersion")} description={<span className="font-mono">{current.latestVersion}</span>}>
							<Button type="button" size="sm" variant="ghost" onClick={() => void aoBridge.app.openExternal(current.releaseNotesUrl!)}>
								<ExternalLink aria-hidden="true" />{t("settings.computerUse.releaseNotes")}
							</Button>
						</SettingsRow>
					) : null}
					{doctorReport ? (
						<div className="settings-content-row">
							<pre className="settings-thin-scrollbar max-h-64 overflow-auto overscroll-contain whitespace-pre-wrap break-words font-mono text-xs text-settings-muted">{doctorReport}</pre>
						</div>
					) : null}
				</SettingsSection>
			) : null}

			{!installed && status.isSuccess && !installing ? (
				<div className="flex px-(--settings-row-inset)">
					<Button type="button" size="sm" variant="ghost" className="-ml-2.5 text-settings-muted hover:text-settings-label" onClick={() => void aoBridge.app.openExternal(RELEASES_URL)}>
						<ExternalLink aria-hidden="true" />{t("settings.harness.installGuide")}
					</Button>
				</div>
			) : null}

			{job.data?.output && (failed || jobStatus === "succeeded") ? (
				<div className="flex flex-col gap-2 px-(--settings-row-inset)">
					<Button type="button" aria-expanded={showOutput} size="sm" variant="ghost" className="-ml-2.5 self-start text-settings-muted hover:text-settings-label" onClick={() => setShowOutput((open) => !open)}>
						{t(showOutput ? "settings.harness.hideDiagnostics" : "settings.harness.showDiagnostics")}
					</Button>
					{showOutput ? (
						<pre data-computer-use-install-output="" className="settings-thin-scrollbar max-h-48 overflow-auto overscroll-contain whitespace-pre-wrap break-words rounded-md bg-(--color-bg-settings-input) p-3 font-mono text-xs text-settings-muted">
							{job.data.command ? `$ ${job.data.command}\n\n` : ""}{job.data.output}
						</pre>
					) : null}
				</div>
			) : null}
		</>
	);
}

function serviceTone(daemon: ComputerUseStatus["daemon"]): HarnessTone {
	return daemon === "running" ? "ok" : daemon === "stopped" ? "error" : "off";
}

function allGranted(...states: PermissionState[]): boolean {
	return states.every((state) => state === "granted");
}
