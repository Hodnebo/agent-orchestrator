import { spawn, type ChildProcessWithoutNullStreams, type SpawnOptionsWithoutStdio } from "node:child_process";
import { randomUUID } from "node:crypto";
import { existsSync, watch, type FSWatcher } from "node:fs";
import { request as httpRequest } from "node:http";
import os from "node:os";
import path from "node:path";
import type { IpcMain } from "electron";
import { resolveDaemonLaunch } from "../shared/daemon-launch";
import type { DaemonStatus } from "../shared/daemon-status";
import type { ProviderAuthCredential } from "./provider-auth-flow";
import {
	TRIAGE_CHANNELS,
	TRIAGE_APPROVAL_REQUIRED,
	type TriageBridge,
	type TriageChoice,
	type TriageOrigin,
	type TriageRun,
	type TriageSelection,
	type TriageStartRequest,
} from "../shared/triage";

export const MAX_TRIAGE_COMMAND_BYTES = 1024 * 1024;
export const MAX_TRIAGE_FRAME_BYTES = 8 * 1024 * 1024;
const MAX_PENDING_REQUESTS = 64;
const MAX_BUFFERED_COMMAND_BYTES = 4 * MAX_TRIAGE_COMMAND_BYTES;
const MAX_DIAGNOSTIC_BYTES = 16 * 1024;

type Command = {
	type: string;
	runId?: string;
	sessionId?: string;
	start?: TriageStartRequest;
	origin?: TriageOrigin;
	githubCredential?: { accessToken: string; refreshToken?: string; expiresIn?: number; refreshTokenExpiresIn?: number };
	choices?: TriageChoice[];
	selection?: TriageSelection;
	text?: string;
	requestId?: string;
	answers?: Record<string, string[]>;
};
type Response = {
	version: 1;
	id?: string;
	type: string;
	run?: TriageRun;
	launch?: SessionLaunch;
	runs?: TriageRun[];
	choices?: TriageChoice[];
	selection?: TriageSelection;
	githubIdentity?: { login: string; id: number };
	error?: string;
};
type SessionLaunch = {
	clientRequestId: string;
	prompt: string;
	attachments: Array<{ mimeType: string; data: string }>;
};
type DaemonRequest = (port: number, method: "GET" | "POST", endpoint: "/healthz" | "/api/v1/sessions" | `/api/v1/sessions/${string}/send`, body?: Record<string, unknown>) => Promise<{ status: number; body: Record<string, unknown> }>;
type SpawnHelper = (command: string, args: string[], options: SpawnOptionsWithoutStdio) => ChildProcessWithoutNullStreams;
export type TriageServiceOptions = {
	dataDir: string;
	resolveBinary: () => string;
	context: () => { appVersion: string; buildMode: string; daemonStatus: DaemonStatus; daemonOutput?: string };
	env?: () => NodeJS.ProcessEnv;
	spawnProcess?: SpawnHelper;
	terminateProcess?: (child: ChildProcessWithoutNullStreams) => void;
	binaryExists?: (binary: string) => boolean;
	requestTimeoutMs?: number;
	shutdownTimeoutMs?: number;
	authenticateGitHub?: (dataDir: string, signal: AbortSignal) => Promise<ProviderAuthCredential>;
	requestDaemon?: DaemonRequest;
};

// Direct loopback requests never consult a proxy, follow redirects, or use a
// host supplied by the renderer. A failed POST has an unknown outcome, so its
// persisted clientRequestId must be reused on the next attempt.
const requestDaemon: DaemonRequest = (port, method, endpoint, body) => new Promise((resolve, reject) => {
	const raw = body === undefined ? undefined : JSON.stringify(body);
	const request = httpRequest({ hostname: "127.0.0.1", port, method, path: endpoint, agent: false,
		headers: raw ? { "Content-Type": "application/json", "Content-Length": Buffer.byteLength(raw), "X-AO-Attachment-Upload": "1" } : undefined,
	}, (response) => {
		const chunks: Buffer[] = [];
		let size = 0;
		response.on("data", (chunk: Buffer) => {
			size += chunk.length;
			if (size > 2 * 1024 * 1024) { response.destroy(new Error("The daemon response exceeded 2 MiB.")); return; }
			chunks.push(chunk);
		});
		response.on("error", reject);
		response.on("end", () => {
			try { const raw = Buffer.concat(chunks).toString("utf8"); resolve({ status: response.statusCode ?? 0, body: raw ? record(JSON.parse(raw)) : {} }); }
			catch { reject(new Error("The daemon returned an invalid response.")); }
		});
	});
	const deadline = setTimeout(() => request.destroy(new Error("The standalone session request timed out.")), 30_000);
	request.on("close", () => clearTimeout(deadline));
	request.on("error", reject);
	request.end(raw);
});

/** Resolve the app-owned binary without consulting shell or daemon overrides. */
export function resolveTriageBinary(input: {
	isPackaged: boolean;
	resourcesPath: string;
	appPath: string;
	homeDir: string;
	platform: NodeJS.Platform;
}): string {
	const launch = resolveDaemonLaunch({}, input.isPackaged, input.resourcesPath, input.appPath, input.homeDir, input.platform);
	if (!launch || !path.isAbsolute(launch.command)) throw new Error("The bundled support helper path is unavailable.");
	return launch.command;
}

/** Scrub retained startup output before it enters a report or fallback text. */
export function scrubTriageDiagnostics(input: string): string {
	return input
		.replace(/-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?(?:-----END [^-]*PRIVATE KEY-----|$)/g, "[redacted private key]")
		.replace(/(\b(?:authorization|proxy-authorization|cookie|set-cookie)\s*:\s*)[^\r\n]*/gi, "$1[redacted]")
		.replace(/\b(Bearer|Basic)\s+[^\s,;"']+/gi, "$1 [redacted]")
		.replace(/\b(?:sk-[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{12,}|github_pat_[A-Za-z0-9_]{12,}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)/g, "[redacted]")
		.replace(/((?:[\w-]*(?:token|password|passwd|secret|credential|api[_-]?key|authorization|cookie)[\w-]*)["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}]+)/gi, "$1[redacted]")
		.replace(/(--(?:token|password|secret|api-key)(?:=|\s+))\S+/gi, "$1[redacted]")
		.replace(/\b(?:https?|wss?):\/\/[^\s<>"']+/gi, (raw) => {
			try {
				const url = new URL(raw);
				url.username = "";
				url.password = "";
				url.search = "";
				url.hash = "";
				return url.toString();
			} catch { return "[redacted URL]"; }
		})
		.replace(/(?:\/Users\/|\/home\/|[A-Z]:\\Users\\)[^\s/\\"']+/gi, "[home]")
		.slice(-MAX_DIAGNOSTIC_BYTES);
}

function record(value: unknown): Record<string, unknown> {
	if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid support request.");
	return value as Record<string, unknown>;
}

function text(value: unknown, limit = 1024, required = false): string {
	if (value === undefined && !required) return "";
	if (typeof value !== "string" || value.length > limit || (required && !value.trim()) || value.includes("\0")) {
		throw new Error("Invalid support request text.");
	}
	return value;
}

function cachedSnapshot(value: unknown): Record<string, unknown> {
	const source = record(value);
	const json = JSON.stringify(source);
	if (Buffer.byteLength(json) > 64 * 1024) throw new Error("The cached diagnostic snapshot is too large.");
	return JSON.parse(json) as Record<string, unknown>;
}

function selection(value: unknown): TriageSelection {
	const source = record(value);
	return {
		harness: text(source.harness, 128, true),
		...(source.model === undefined ? {} : { model: text(source.model, 512) }),
		...(source.effort === undefined ? {} : { effort: text(source.effort, 128) }),
		...(source.accountId === undefined ? {} : { accountId: text(source.accountId, 512) }),
		...(source.mode === undefined ? {} : { mode: text(source.mode, 128) }),
		...(source.permissions === undefined ? {} : { permissions: text(source.permissions, 128) }),
	};
}

function choices(value: unknown): TriageChoice[] {
	if (!Array.isArray(value) || value.length > 64) throw new Error("Invalid configured support agents.");
	return value.map((entry) => {
		const source = record(entry);
		const sourceModels = source.models ?? [];
		if (!Array.isArray(sourceModels) || sourceModels.length > 512 || typeof source.available !== "boolean") {
			throw new Error("Invalid configured support agent.");
		}
		if (source.modelsMissing !== undefined && typeof source.modelsMissing !== "boolean") throw new Error("Invalid support model availability.");
		const models = sourceModels.map((entry) => {
			const model = record(entry);
			if (model.default !== undefined && typeof model.default !== "boolean") throw new Error("Invalid support model default.");
			let efforts: string[] | undefined;
			if (model.efforts !== undefined) {
				if (!Array.isArray(model.efforts) || model.efforts.length > 32) throw new Error("Invalid support model efforts.");
				efforts = model.efforts.map((effort) => text(effort, 128, true));
			}
			if (model.id === undefined) throw new Error("Invalid configured model.");
			return { id: text(model.id, 512), label: text(model.label, 512, true), ...(efforts ? { efforts } : {}), ...(model.default === undefined ? {} : { default: model.default }) };
		});
		let accounts: TriageChoice["accounts"];
		if (source.accounts !== undefined) {
			if (!Array.isArray(source.accounts) || source.accounts.length > 128) throw new Error("Invalid support accounts.");
			accounts = source.accounts.map((entry) => {
				const account = record(entry);
				if (account.default !== undefined && typeof account.default !== "boolean") throw new Error("Invalid support account default.");
				return { id: text(account.id, 512, true), label: text(account.label, 512, true), ...(account.default === undefined ? {} : { default: account.default }) };
			});
		}
		const updatedAt = text(source.updatedAt, 64, true);
		if (!Number.isFinite(Date.parse(updatedAt))) throw new Error("Invalid support agent update time.");
		return {
			harness: text(source.harness, 128, true), label: text(source.label, 512, true), models,
			available: source.available, updatedAt,
			...(source.modelsMissing === undefined ? {} : { modelsMissing: source.modelsMissing }),
			...(accounts ? { accounts } : {}),
			...(source.unavailableReason === undefined ? {} : { unavailableReason: scrubTriageDiagnostics(text(source.unavailableReason, 2048)) }),
			...(source.source === undefined ? {} : { source: text(source.source, 128) }),
			...(source.mode === undefined ? {} : { mode: text(source.mode, 128) }),
			...(source.permissions === undefined ? {} : { permissions: text(source.permissions, 128) }),
		};
	});
}

function isRun(value: unknown): value is TriageRun {
	if (!value || typeof value !== "object") return false;
	const run = value as Partial<TriageRun>;
	return typeof run.id === "string" && run.version === 1 && typeof run.state === "string"
		&& typeof run.activity === "string" && typeof run.createdAt === "string" && typeof run.updatedAt === "string"
		&& Array.isArray(run.messages) && !!run.request && typeof run.request === "object"
		&& !!run.publication && typeof run.publication.state === "string";
}

function parseResponse(line: string): Response {
	const value = record(JSON.parse(line));
	if (value.version !== 1 || typeof value.type !== "string" || (value.id !== undefined && typeof value.id !== "string")) {
		throw new Error("Invalid support helper protocol.");
	}
	switch (value.type) {
		case "run": if (!isRun(value.run)) throw new Error("Invalid support run."); break;
		case "session-launch": {
			if (!isRun(value.run) || value.run.execution !== "session") throw new Error("Invalid standalone investigation.");
			if (value.launch !== undefined) {
				const launch = record(value.launch);
				text(launch.clientRequestId, 128, true);
				text(launch.prompt, 16 * 1024, true);
				if (!Array.isArray(launch.attachments) || !launch.attachments.length || launch.attachments.length > 8) throw new Error("Invalid investigation attachments.");
				let bytes = 0;
				for (const entry of launch.attachments) {
					const attachment = record(entry);
					if (!["text/plain", "text/markdown", "application/json"].includes(String(attachment.mimeType)) || typeof attachment.data !== "string" || !attachment.data.length || Buffer.from(attachment.data, "base64").toString("base64") !== attachment.data) throw new Error("Invalid investigation attachment.");
					bytes += Buffer.byteLength(attachment.data, "base64");
					if (bytes > 1024 * 1024) throw new Error("Investigation attachments exceed 1 MiB.");
				}
			} else if (!value.run.sessionId) throw new Error("The investigation has no launch payload.");
			break;
		}
		case "runs": if (value.runs !== undefined && (!Array.isArray(value.runs) || !value.runs.every(isRun))) throw new Error("Invalid support runs."); break;
		case "catalog": if (value.choices !== undefined) value.choices = choices(value.choices); break;
		case "selection": if (value.selection !== undefined && value.selection !== null) value.selection = selection(value.selection); break;
		case "error": if (typeof value.error !== "string") throw new Error("Invalid support helper error."); break;
		case "github-auth-stored": {
			const identity = record(value.githubIdentity);
			if (typeof identity.login !== "string" || !Number.isSafeInteger(identity.id) || Number(identity.id) <= 0) throw new Error("Invalid GitHub identity.");
			break;
		}
		case "configured": case "quit": break;
		default: throw new Error("Unknown support helper response.");
	}
	return value as Response;
}

function terminateHelper(child: ChildProcessWithoutNullStreams): void {
	try {
		if (process.platform !== "win32" && child.pid) process.kill(-child.pid, "SIGTERM");
		else child.kill("SIGTERM");
	} catch { /* It may already have exited. */ }
}

/** One lazy process per desktop app. Unsubscribing never cancels its work. */
export class TriageService implements TriageBridge {
	private child: ChildProcessWithoutNullStreams | null = null;
	private failure: Error | null = null;
	private closing = false;
	private shutdownPromise: Promise<void> | null = null;
	private output = Buffer.alloc(0);
	private stderr = "";
	private listeners = new Set<(run: TriageRun) => void>();
	private pending = new Map<string, { expected: string; resolve: (response: Response) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }>();
	private terminationTimer: ReturnType<typeof setTimeout> | undefined;
	private exited: Promise<void> = Promise.resolve();
	private githubSignIns = new Map<string, { controller: AbortController; promise: Promise<TriageRun> }>();
	private reportWatchers = new Map<string, FSWatcher>();
	private reportRefreshes = new Map<string, { dirty: boolean }>();
	private knownSessions = new Map<string, string>();

	constructor(private readonly options: TriageServiceOptions) {}

	private watchSessionReport(run: TriageRun): void {
		if (this.closing || run.execution !== "session" || !/^[a-zA-Z0-9_-]{1,128}$/.test(run.id) || this.reportWatchers.has(run.id)) return;
		// The ordinary agent's reporting tool writes the same private manifest.
		// Watch only that filename, and read it through the existing helper so
		// publication/auth updates reach the UI without a second conversation.
		try {
			if (this.reportWatchers.size >= 64) {
				const oldest = this.reportWatchers.keys().next().value as string;
				this.reportWatchers.get(oldest)?.close();
				this.reportWatchers.delete(oldest);
			}
			const watcher = watch(path.join(this.options.dataDir, "reports", "triage", "runs", run.id), { persistent: false }, (_event, filename) => {
				if (filename?.toString() === "manifest.json") void this.refreshSessionReport(run.id);
			});
			watcher.on("error", () => { watcher.close(); this.reportWatchers.delete(run.id); });
			this.reportWatchers.set(run.id, watcher);
		} catch { /* A removed saved report does not affect its ordinary session. */ }
	}

	private async refreshSessionReport(id: string): Promise<void> {
		const pending = this.reportRefreshes.get(id);
		if (pending) { pending.dirty = true; return; }
		const refresh = { dirty: true };
		this.reportRefreshes.set(id, refresh);
		try {
			while (refresh.dirty && !this.closing) {
				refresh.dirty = false;
				const run = await this.get(id);
				for (const listener of this.listeners) { try { listener(run); } catch { /* Views are independent. */ } }
			}
		} catch { /* Preserve the last saved status when the helper is unavailable. */ }
		finally { this.reportRefreshes.delete(id); }
	}

	private closeReportWatchers(): void {
		for (const watcher of this.reportWatchers.values()) watcher.close();
		this.reportWatchers.clear();
	}

	private diagnostics(): string {
		const context = this.options.context();
		const summary = scrubTriageDiagnostics([
			`AO ${context.appVersion}, ${context.buildMode}, ${process.platform}/${process.arch}`,
			`Daemon: ${context.daemonStatus.state}${context.daemonStatus.code ? ` (${context.daemonStatus.code})` : ""}`,
		].join("\n"));
		const details = scrubTriageDiagnostics([
			context.daemonStatus.message, context.daemonStatus.details, context.daemonOutput, this.stderr,
		].filter(Boolean).join("\n"));
		return `${summary}\n${details}`;
	}

	private error(message: string): Error {
		return new Error(`${message}\nYou can copy these details into a GitHub report:\n${this.diagnostics()}`);
	}

	private fail(error: Error): void {
		this.failure ??= error;
		for (const request of this.pending.values()) { clearTimeout(request.timer); request.reject(this.failure); }
		this.pending.clear();
	}

	private endFailedHelper(message: string): void {
		this.fail(this.error(message));
		const child = this.child;
		if (!child) return;
		child.stdin.end();
		this.terminationTimer ??= setTimeout(() => (this.options.terminateProcess ?? terminateHelper)(child), 1000);
		this.terminationTimer.unref();
	}

	private ensureHelper(): ChildProcessWithoutNullStreams {
		if (this.failure) throw this.failure;
		if (this.child) return this.child;
		try {
			const binary = this.options.resolveBinary();
			if (!path.isAbsolute(binary) || !(this.options.binaryExists ?? existsSync)(binary)) throw new Error("missing binary");
			const child = (this.options.spawnProcess ?? spawn)(binary, ["support-host", "--data-dir", this.options.dataDir], {
				cwd: path.dirname(binary), shell: false, stdio: "pipe", detached: process.platform !== "win32", windowsHide: true,
				env: this.options.env?.() ?? process.env,
			});
			this.child = child;
			this.exited = new Promise((resolve) => {
				child.once("exit", () => {
					if (this.terminationTimer) clearTimeout(this.terminationTimer);
					if (!this.closing) (this.options.terminateProcess ?? terminateHelper)(child);
					this.child = null;
					this.fail(this.error("The support helper exited. Restart AO to try again."));
					resolve();
				});
			});
			child.once("error", () => this.endFailedHelper("The support helper could not start."));
			child.stdin.on("error", () => this.endFailedHelper("The support helper connection closed."));
			child.stderr.on("data", (data: Buffer | string) => { this.stderr = (this.stderr + data.toString()).slice(-MAX_DIAGNOSTIC_BYTES); });
			child.stdout.on("data", (data: Buffer | string) => this.onData(Buffer.isBuffer(data) ? data : Buffer.from(data)));
			return child;
		} catch {
			this.failure = this.error("The bundled support helper is unavailable. Restart AO or reinstall the desktop app.");
			throw this.failure;
		}
	}

	private onData(data: Buffer): void {
		if (this.failure) return;
		// Never concatenate an unbounded read chunk, even when it contains newlines.
		let offset = 0;
		while (offset < data.length) {
			const newline = data.indexOf(10, offset);
			const end = newline < 0 ? data.length : newline;
			if (this.output.length + end - offset > MAX_TRIAGE_FRAME_BYTES) {
				this.endFailedHelper("The support helper sent an oversized response."); return;
			}
			this.output = Buffer.concat([this.output, data.subarray(offset, end)]);
			if (newline < 0) return;
			const line = this.output.toString("utf8");
			this.output = Buffer.alloc(0);
			offset = newline + 1;
			let response: Response;
			try { response = parseResponse(line); }
			catch { this.endFailedHelper("The support helper sent an invalid response."); return; }
			const retainSession = (run: TriageRun) => {
				const sessionId = this.knownSessions.get(run.id);
				return sessionId && run.execution === "session" && !run.sessionId ? { ...run, sessionId } : run;
			};
			if (response.run) response.run = retainSession(response.run);
			if (response.runs) response.runs = response.runs.map(retainSession);
			if (response.run) this.watchSessionReport(response.run);
			for (const run of response.runs?.slice(0, 64).reverse() ?? []) this.watchSessionReport(run);
			if (!response.id) {
				if (response.type === "run" && response.run) {
					for (const listener of this.listeners) { try { listener(response.run); } catch { /* A view cannot stop another subscriber. */ } }
				} else if (response.type === "error") {
					this.endFailedHelper("The support helper reported a protocol error."); return;
				}
				continue;
			}
			const request = this.pending.get(response.id);
			if (!request) continue; // A timed-out response must not be retried or cancel the run.
			this.pending.delete(response.id);
			clearTimeout(request.timer);
			if (response.type === "error") request.reject(new Error(scrubTriageDiagnostics(response.error ?? "Support request failed.")));
			else if (response.type !== request.expected) {
				request.reject(this.error("The support helper returned the wrong response."));
				this.endFailedHelper("The support helper returned the wrong response."); return;
			} else request.resolve(response);
		}
	}

	private request(command: Command, expected: string, timeoutMs = this.options.requestTimeoutMs ?? 30_000, quitting = false): Promise<Response> {
		if (this.closing && !quitting) return Promise.reject(new Error("The support helper is shutting down."));
		const id = randomUUID();
		const frame = JSON.stringify({ ...command, id }) + "\n";
		if (Buffer.byteLength(frame) > MAX_TRIAGE_COMMAND_BYTES) return Promise.reject(new Error("The support request is too large."));
		if (this.pending.size >= MAX_PENDING_REQUESTS) return Promise.reject(new Error("Too many support requests are pending."));
		let child: ChildProcessWithoutNullStreams;
		try { child = this.ensureHelper(); } catch (error) { return Promise.reject(error); }
		if (child.stdin.writableLength + Buffer.byteLength(frame) > MAX_BUFFERED_COMMAND_BYTES) return Promise.reject(new Error("The support helper is busy. Try again shortly."));
		return new Promise((resolve, reject) => {
			const timer = setTimeout(() => {
				this.pending.delete(id);
				reject(new Error("The support request timed out. The investigation may still be running; reopen it to check its progress."));
			}, timeoutMs);
			this.pending.set(id, { expected, resolve, reject, timer });
			try { child.stdin.write(frame); }
			catch { this.endFailedHelper("The support helper connection closed."); }
		});
	}

	async catalog(): Promise<TriageChoice[]> { return (await this.request({ type: "catalog" }, "catalog")).choices ?? []; }
	async configure(value: TriageChoice[]): Promise<void> { await this.request({ type: "configure", choices: choices(value) }, "configured"); }
	async getPreference(): Promise<TriageSelection | null> { return (await this.request({ type: "preference" }, "selection")).selection ?? null; }
	async setPreference(value: TriageSelection): Promise<void> { await this.request({ type: "preference", selection: selection(value) }, "selection"); }
	async list(): Promise<TriageRun[]> { return (await this.request({ type: "list" }, "runs")).runs ?? []; }
	async get(runId: string): Promise<TriageRun> { return (await this.request({ type: "get", runId: text(runId, 128, true) }, "run")).run!; }
	private origin(value: unknown, context = this.options.context()): TriageOrigin {
		const provided = value === undefined ? {} : record(value);
		// Already available state only; never refresh or start the daemon.
		const cachedMachine = provided.machine === undefined ? {} : record(provided.machine);
		return {
			...(provided.hostId === undefined ? {} : { hostId: text(provided.hostId, 255) }),
			...(provided.surface === undefined ? {} : { surface: text(provided.surface, 128) }),
			...(provided.sessionId === undefined ? {} : { sessionId: text(provided.sessionId, 255) }),
			appVersion: context.appVersion, buildMode: context.buildMode,
			daemonState: context.daemonStatus.state, daemonCode: context.daemonStatus.code,
			daemonOutput: scrubTriageDiagnostics([context.daemonStatus.message, context.daemonStatus.details, context.daemonOutput].filter(Boolean).join("\n")),
			machine: { platform: process.platform, arch: process.arch, release: os.release(),
				...(cachedMachine.app === undefined ? {} : { app: cachedSnapshot(cachedMachine.app) }),
				...(cachedMachine.system === undefined ? {} : { system: cachedSnapshot(cachedMachine.system) }),
				...(cachedMachine.fetchedAt === undefined ? {} : { fetchedAt: text(cachedMachine.fetchedAt, 128) }),
			},
			...(provided.session === undefined ? {} : { session: cachedSnapshot(provided.session) }),
		};
	}
	async prepare(value: TriageOrigin): Promise<TriageRun> {
		return (await this.request({ type: "prepare", origin: this.origin(value) }, "run")).run!;
	}
	async start(value: TriageStartRequest): Promise<TriageRun> {
		const source = record(value);
		if (typeof source.publishAuthorized !== "boolean") throw new Error("Choose whether to authorize publishing this report.");
		const context = this.options.context();
		const start: TriageStartRequest = {
			...selection(source), observation: text(source.observation, MAX_TRIAGE_COMMAND_BYTES), origin: this.origin(source.origin, context),
			...(source.preparationId === undefined ? {} : { preparationId: text(source.preparationId, 128, true) }),
			publishAuthorized: source.publishAuthorized,
		};
		const status = context.daemonStatus;
		// Select the owner once. A stopped daemon may use the independent helper,
		// but a normal session's uncertain launch must never start another agent.
		if (status.state === "ready") return this.startSession(start, status);
		if (start.preparationId) {
			const prepared = await this.get(start.preparationId);
			if (prepared.sessionId) return prepared;
			if (prepared.execution === "session") {
				if (prepared.sessionLaunchStarted) throw new Error("The standalone investigation may already exist. Reconnect to the daemon and press Start investigation again to reopen the same session.");
				return (await this.request({ type: "start", start: { ...prepared.request, preparationId: prepared.id } }, "run")).run!;
			}
		}
		return (await this.request({ type: "start", start }, "run")).run!;
	}

	private async startSession(start: TriageStartRequest, status: DaemonStatus): Promise<TriageRun> {
		if (!Number.isInteger(status.port) || !status.port || status.port < 1 || status.port > 65535 || !Number.isInteger(status.pid) || !status.pid || status.pid < 1) throw new Error("The daemon identity is not ready. Try again when AO reconnects.");
		if (start.harness === "unreal-agent" && start.permissions !== "bypass-permissions") throw new Error(TRIAGE_APPROVAL_REQUIRED);
		if (!start.preparationId) start.preparationId = (await this.prepare(start.origin ?? {})).id;
		const handoff = await this.request({ type: "handoff", start }, "session-launch");
		if (handoff.run!.sessionId) return handoff.run!;
		const launch = handoff.launch!;
		const request = this.options.requestDaemon ?? requestDaemon;
		let postIntentAttempted = false;
		let createdSessionId: string | undefined;
		let intentRun = handoff.run!;
		const sameDaemon = () => {
			const current = this.options.context().daemonStatus;
			return current.state === "ready" && current.pid === status.pid && current.port === status.port && current.executablePath === status.executablePath;
		};
		try {
			const health = await request(status.port, "GET", "/healthz");
			if (health.status !== 200 || health.body.status !== "ok" || health.body.service !== "agent-orchestrator-daemon" || health.body.pid !== status.pid ||
				(status.executablePath && health.body.executablePath !== status.executablePath) || !sameDaemon()) throw new Error("The daemon identity changed before the investigation started.");
			postIntentAttempted = true;
			intentRun = (await this.request({ type: "session-create-intent", runId: handoff.run!.id }, "run")).run!;
			if (!sameDaemon()) throw new Error("The daemon changed while the investigation was starting.");
			const selected = handoff.run!.request;
			const response = await request(status.port, "POST", "/api/v1/sessions", {
				clientRequestId: launch.clientRequestId, kind: "worker", harness: selected.harness,
				displayName: "Report a Problem", prompt: launch.prompt, attachments: launch.attachments,
				...(selected.harness === "unreal-agent" ? { mode: "chat" } : {}),
				...(selected.model ? { model: selected.model } : {}), ...(selected.effort ? { effort: selected.effort } : {}),
				...(selected.permissions ? { approvalMode: selected.permissions } : {}),
			});
			if (response.status < 200 || response.status >= 300) {
				const error = response.body.error && typeof response.body.error === "object" ? record(response.body.error) : {};
				throw new Error(typeof error.message === "string" ? scrubTriageDiagnostics(error.message) : `The daemon returned HTTP ${response.status}.`);
			}
			const session = record(response.body.session);
			const sessionId = text(session.id, 255, true);
			createdSessionId = sessionId;
			this.knownSessions.set(handoff.run!.id, sessionId);
			const linked = (await this.request({ type: "session-linked", runId: handoff.run!.id, sessionId }, "run")).run!;
			if (linked.id !== handoff.run!.id || linked.sessionId !== sessionId) throw new Error("The helper could not confirm the standalone session link.");
			return linked;
		} catch (error) {
			if (createdSessionId) {
				// A failed manifest link cannot turn a successful ordinary create
				// into an uncertain launch. The persisted create key remains safe
				// to replay if this app exits before linkage can be saved.
				return { ...intentRun, sessionId: createdSessionId };
			}
			if (!postIntentAttempted && !handoff.run!.sessionLaunchStarted) {
				// No create request could have been sent. The helper checks the
				// durable intent again before adopting this same run offline.
				return (await this.request({ type: "start", start: { ...handoff.run!.request, preparationId: handoff.run!.id } }, "run")).run!;
			}
			const reason = error instanceof Error ? scrubTriageDiagnostics(error.message) : "The session request failed.";
			throw new Error(`${reason} Press Start investigation again to check or retry the same standalone session.`);
		}
	}
	async reply(runId: string, value: string, requestId?: string, valueAnswers?: Record<string, string[]>): Promise<TriageRun> {
		let answers: Record<string, string[]> | undefined;
		if (valueAnswers !== undefined) {
			const source = record(valueAnswers);
			if (Object.keys(source).length > 64) throw new Error("Too many support answers.");
			answers = Object.create(null) as Record<string, string[]>;
			for (const [key, entries] of Object.entries(source)) {
				if (!Array.isArray(entries) || entries.length > 64) throw new Error("Invalid support answers.");
				answers[text(key, 128, true)] = entries.map((entry) => text(entry, 16 * 1024));
			}
		}
		return (await this.request({ type: "reply", runId: text(runId, 128, true), text: text(value, MAX_TRIAGE_COMMAND_BYTES), requestId: requestId === undefined ? undefined : text(requestId, 128), answers }, "run")).run!;
	}
	async cancel(runId: string): Promise<TriageRun> {
		this.githubSignIns.get(runId)?.controller.abort();
		return (await this.request({ type: "cancel", runId: text(runId, 128, true) }, "run")).run!;
	}
	async publish(runId: string): Promise<TriageRun> { return (await this.request({ type: "publish", runId: text(runId, 128, true) }, "run")).run!; }
	async nativeInput(runId: string): Promise<TriageRun> { return (await this.request({ type: "native-input", runId: text(runId, 128, true) }, "run")).run!; }
	signInGitHub(runId: string): Promise<TriageRun> {
		const id = text(runId, 128, true);
		const existing = this.githubSignIns.get(id);
		if (existing) return existing.promise;
		const controller = new AbortController();
		const promise = (async () => {
			const run = await this.get(id);
			if (run.publication.state !== "auth_required" && !run.searchAuthRequired) throw new Error("GitHub sign-in is only needed when this report requests authentication.");
			const authenticate = this.options.authenticateGitHub ?? (async (dataDir: string, signal: AbortSignal) =>
				(await import("./provider-auth-flow")).providerAuthFlow("github").authenticate(dataDir, signal));
			const credential = await authenticate(this.options.dataDir, controller.signal);
			if (controller.signal.aborted) throw new Error("GitHub sign-in was cancelled.");
			if (credential.provider !== "github" || credential.credentialType !== "access_token") throw new Error("GitHub did not return a usable credential.");
			await this.request({ type: "github-auth-store", githubCredential: {
				accessToken: text(credential.secret, 16 * 1024, true),
				...(credential.refreshToken ? { refreshToken: text(credential.refreshToken, 16 * 1024, true) } : {}),
				...(credential.expiresIn === undefined ? {} : { expiresIn: credential.expiresIn }),
				...(credential.refreshTokenExpiresIn === undefined ? {} : { refreshTokenExpiresIn: credential.refreshTokenExpiresIn }),
			} }, "github-auth-stored");
			if (controller.signal.aborted) throw new Error("GitHub sign-in was cancelled.");
			if (run.execution === "session" && run.searchAuthRequired) {
				const status = this.options.context().daemonStatus;
				if (status.state !== "ready" || !status.port || !status.pid || !run.sessionId) throw new Error("GitHub sign-in is saved. Reopen the investigation when AO reconnects and ask the agent to continue its issue and PR search.");
				const request = this.options.requestDaemon ?? requestDaemon;
				const health = await request(status.port, "GET", "/healthz");
				const current = this.options.context().daemonStatus;
				if (health.status !== 200 || health.body.service !== "agent-orchestrator-daemon" || health.body.pid !== status.pid ||
					(status.executablePath && health.body.executablePath !== status.executablePath) || current.state !== "ready" || current.pid !== status.pid || current.port !== status.port) throw new Error("GitHub sign-in is saved, but AO reconnected during sign-in. Continue in the existing investigation.");
				const sent = await request(status.port, "POST", `/api/v1/sessions/${encodeURIComponent(run.sessionId)}/send`, {
					message: "GitHub sign-in completed for this report. Retry the bounded issue and PR search with the reporting tool, then continue the existing investigation. Do not start another agent or bypass duplicate search.",
				});
				if (sent.status < 200 || sent.status >= 300) throw new Error("GitHub sign-in is saved. Continue in the existing agent to retry its issue and PR search.");
				return this.get(id);
			}
			return this.publish(id);
		})().finally(() => { this.githubSignIns.delete(id); });
		this.githubSignIns.set(id, { controller, promise });
		return promise;
	}
	subscribe(listener: (run: TriageRun) => void): () => void {
		this.listeners.add(listener);
		return () => { this.listeners.delete(listener); };
	}

	shutdown(): Promise<void> {
		if (this.shutdownPromise) return this.shutdownPromise;
		this.closing = true;
		this.closeReportWatchers();
		for (const { controller } of this.githubSignIns.values()) controller.abort();
		const child = this.child;
		if (!child) return Promise.resolve();
		this.shutdownPromise = (async () => {
			const timeout = this.options.shutdownTimeoutMs ?? 1500;
			try { await this.request({ type: "quit" }, "quit", timeout, true); } catch { /* EOF also persists an interrupted run. */ }
			child.stdin.end();
			let deadline: ReturnType<typeof setTimeout> | undefined;
			await Promise.race([this.exited, new Promise<void>((resolve) => {
				deadline = setTimeout(() => { (this.options.terminateProcess ?? terminateHelper)(child); resolve(); }, timeout);
			})]);
			if (deadline) clearTimeout(deadline);
			if (this.terminationTimer) clearTimeout(this.terminationTimer);
		})();
		return this.shutdownPromise;
	}

	closeOnExit(): void {
		this.closeReportWatchers();
		for (const { controller } of this.githubSignIns.values()) controller.abort();
		if (this.child) { this.child.stdin.end(); (this.options.terminateProcess ?? terminateHelper)(this.child); }
	}
}

/** IPC is registered once and accepts only the app's trusted shell contents. */
export function registerTriageIpc(input: {
	ipcMain: Pick<IpcMain, "handle">;
	service: TriageService;
	isTrustedSender: (event: Parameters<Parameters<IpcMain["handle"]>[1]>[0]) => boolean;
}): void {
	const handlers = {
		catalog: () => input.service.catalog(), configure: (choices: TriageChoice[]) => input.service.configure(choices),
		getPreference: () => input.service.getPreference(), setPreference: (selection: TriageSelection) => input.service.setPreference(selection),
		list: () => input.service.list(), get: (runId: string) => input.service.get(runId),
		prepare: (origin: TriageOrigin) => input.service.prepare(origin),
		start: (request: TriageStartRequest) => input.service.start(request),
		reply: (runId: string, text: string, requestId?: string, answers?: Record<string, string[]>) => input.service.reply(runId, text, requestId, answers),
		cancel: (runId: string) => input.service.cancel(runId), publish: (runId: string) => input.service.publish(runId),
		signInGitHub: (runId: string) => input.service.signInGitHub(runId),
		nativeInput: (runId: string) => input.service.nativeInput(runId),
	};
	for (const name of Object.keys(handlers) as Array<keyof typeof handlers>) {
		input.ipcMain.handle(TRIAGE_CHANNELS[name], (event, ...args: unknown[]) => {
			if (!input.isTrustedSender(event)) throw new Error("Untrusted support request.");
			return Reflect.apply(handlers[name], undefined, args);
		});
	}
}
