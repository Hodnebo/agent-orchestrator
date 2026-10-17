// Trusted input (tier T1) through a transient chrome.debugger attachment.
// The extension attaches only for the duration of one action, uses the Input
// domain for pointer and keyboard events plus the Page domain to notice
// JavaScript dialogs, and detaches immediately. Runtime is enabled only for
// an explicit console/errors request, then disabled again. Chrome shows its
// "started debugging this browser" bar while attached; that is the visible
// cost of trusted events.
//
// One exception: when an action opens alert/confirm/prompt, the attachment
// stays until the dialog is answered, because only the debugger that saw a
// dialog open can answer it and the page is blocked until then.

import type { ChromeApi, DebuggeeTarget } from "./chrome-api";
import { humanPath, type Point } from "./human-path";
import { keyEvents } from "./keys";
import { BridgeError } from "./protocol";

const PROTOCOL_VERSION = "1.3";

type Send = (method: string, params?: Record<string, unknown>) => Promise<unknown>;

export type ConsoleMessage = { level: string; message: string; timestamp?: number; source?: string };
export type DialogInfo = { type: string; message: string; defaultPrompt?: string };
export type InputResult = { dialogOpened?: boolean; dialog?: DialogInfo };

const DIALOG_OPENED = Symbol("dialog-opened");

export class TrustedInput {
	private readonly locks = new Map<number, Promise<unknown>>();
	private readonly lastPointer = new Map<number, Point>();
	/** Tabs we stay attached to because a dialog is waiting for an answer. */
	private readonly pendingDialogs = new Map<number, DialogInfo>();
	private readonly dialogWaiters = new Map<number, Set<(dialog: DialogInfo) => void>>();
	private listening = false;

	constructor(private readonly chrome: ChromeApi) {}

	private listen(): void {
		if (this.listening) return;
		this.listening = true;
		this.chrome.debugger.onEvent.addListener((source, method, params) => {
			if (source.tabId === undefined || !params || typeof params !== "object") return;
			const event = params as Record<string, unknown>;
			if (method === "Page.javascriptDialogOpening") {
				const dialog: DialogInfo = {
					type: String(event.type ?? ""),
					message: String(event.message ?? ""),
					...(typeof event.defaultPrompt === "string" ? { defaultPrompt: event.defaultPrompt } : {}),
				};
				this.pendingDialogs.set(source.tabId, dialog);
				for (const waiter of this.dialogWaiters.get(source.tabId) ?? []) waiter(dialog);
			} else if (method === "Page.javascriptDialogClosed") {
				this.pendingDialogs.delete(source.tabId);
			}
		});
		// The user cancelled the debugging bar, or the tab closed.
		this.chrome.debugger.onDetach.addListener((source) => {
			if (source.tabId !== undefined) this.pendingDialogs.delete(source.tabId);
		});
	}

	/** The dialog an earlier action left open on this tab, if any. */
	pendingDialog(tabId: number): DialogInfo | undefined {
		return this.pendingDialogs.get(tabId);
	}

	/** Run fn with the debugger attached to tabId, serialized per tab. */
	async withDebugger<T>(tabId: number, fn: (send: Send) => Promise<T>): Promise<T> {
		this.listen();
		const previous = this.locks.get(tabId) ?? Promise.resolve();
		const run = previous.catch(() => undefined).then(() => this.attached(tabId, fn));
		this.locks.set(tabId, run);
		try {
			return await run;
		} finally {
			if (this.locks.get(tabId) === run) this.locks.delete(tabId);
		}
	}

	private async attached<T>(tabId: number, fn: (send: Send) => Promise<T>): Promise<T> {
		const target: DebuggeeTarget = { tabId };
		const send: Send = (method, params) => this.chrome.debugger.sendCommand(target, method, params ?? {});
		// Already attached while a dialog waits: reuse that attachment.
		const reuse = this.pendingDialogs.has(tabId);
		if (!reuse) {
			try {
				await this.chrome.debugger.attach(target, PROTOCOL_VERSION);
			} catch (error) {
				const message = error instanceof Error ? error.message : String(error);
				throw new BridgeError(
					"BROWSER_DEBUGGER_UNAVAILABLE",
					/attached|another debugger/i.test(message)
						? "Another debugger (DevTools?) is attached to this tab; close it or use synthetic input for this site"
						: `Chrome refused trusted input for this tab: ${message}`,
				);
			}
			await send("Page.enable").catch(() => undefined);
		}
		try {
			return await fn(send);
		} finally {
			if (!this.pendingDialogs.has(tabId)) {
				await send("Page.disable").catch(() => undefined);
				await this.chrome.debugger.detach(target).catch(() => undefined);
			}
		}
	}

	/**
	 * Send input events in order, but stop at the moment one of them opens a
	 * JavaScript dialog: Chrome does not finish dispatching that event until
	 * the dialog is answered.
	 */
	private async dispatch(tabId: number, send: Send, events: [string, Record<string, unknown>][], paceMs = 0): Promise<InputResult> {
		const waiters = this.dialogWaiters.get(tabId) ?? new Set();
		this.dialogWaiters.set(tabId, waiters);
		let opened: (dialog: DialogInfo) => void = () => undefined;
		const dialogOpened = new Promise<typeof DIALOG_OPENED>((resolve) => {
			opened = () => resolve(DIALOG_OPENED);
		});
		waiters.add(opened);
		try {
			for (const [method, params] of events) {
				// Human-paced pointer movement: a short, jittered gap between moves.
				if (paceMs > 0 && params.type === "mouseMoved") await sleep(paceMs + Math.random() * paceMs);
				const outcome = await Promise.race([send(method, params), dialogOpened]);
				if (outcome === DIALOG_OPENED) return { dialogOpened: true, dialog: this.pendingDialogs.get(tabId) };
			}
			// A handler can open a dialog just after the last event returns.
			const late = await Promise.race([dialogOpened, sleep(50).then(() => undefined)]);
			return late === DIALOG_OPENED ? { dialogOpened: true, dialog: this.pendingDialogs.get(tabId) } : {};
		} finally {
			waiters.delete(opened);
		}
	}

	private assertNoDialog(tabId: number): void {
		const dialog = this.pendingDialogs.get(tabId);
		if (dialog) {
			throw new BridgeError("BROWSER_DIALOG_OPEN", `A ${dialog.type} dialog is open; answer it with \`ao browser dialog accept|dismiss\``);
		}
	}

	async click(tabId: number, point: Point, options: { clicks?: number; human?: boolean } = {}): Promise<InputResult> {
		this.assertNoDialog(tabId);
		const clicks = options.clicks ?? 1;
		return this.withDebugger(tabId, async (send) => {
			const events: [string, Record<string, unknown>][] = this.moveEvents(tabId, point, options.human === true);
			for (let i = 1; i <= clicks; i++) {
				events.push(["Input.dispatchMouseEvent", { type: "mousePressed", x: point.x, y: point.y, button: "left", buttons: 1, clickCount: i }]);
				events.push(["Input.dispatchMouseEvent", { type: "mouseReleased", x: point.x, y: point.y, button: "left", buttons: 0, clickCount: i }]);
			}
			return this.dispatch(tabId, send, events, options.human === true ? 8 : 0);
		});
	}

	async hover(tabId: number, point: Point, human = false): Promise<InputResult> {
		this.assertNoDialog(tabId);
		return this.withDebugger(tabId, (send) => this.dispatch(tabId, send, this.moveEvents(tabId, point, human), human ? 8 : 0));
	}

	async drag(tabId: number, from: Point, to: Point, human = false): Promise<InputResult> {
		this.assertNoDialog(tabId);
		return this.withDebugger(tabId, async (send) => {
			const events = this.moveEvents(tabId, from, human);
			events.push(["Input.dispatchMouseEvent", { type: "mousePressed", x: from.x, y: from.y, button: "left", buttons: 1, clickCount: 1 }]);
			for (const point of human ? humanPath(from, to) : linearPath(from, to, 12)) {
				events.push(["Input.dispatchMouseEvent", { type: "mouseMoved", x: point.x, y: point.y, button: "left", buttons: 1 }]);
			}
			events.push(["Input.dispatchMouseEvent", { type: "mouseReleased", x: to.x, y: to.y, button: "left", buttons: 0, clickCount: 1 }]);
			this.lastPointer.set(tabId, to);
			return this.dispatch(tabId, send, events, human ? 8 : 0);
		});
	}

	async insertText(tabId: number, text: string): Promise<InputResult> {
		this.assertNoDialog(tabId);
		return this.withDebugger(tabId, (send) => this.dispatch(tabId, send, [["Input.insertText", { text }]]));
	}

	async press(tabId: number, chord: string): Promise<InputResult> {
		this.assertNoDialog(tabId);
		const events = keyEvents(chord).map((event): [string, Record<string, unknown>] => ["Input.dispatchKeyEvent", event]);
		return this.withDebugger(tabId, (send) => this.dispatch(tabId, send, events));
	}

	/** Console messages and exceptions Chrome kept for the page. */
	async consoleMessages(tabId: number): Promise<{ console: ConsoleMessage[]; errors: ConsoleMessage[] }> {
		this.assertNoDialog(tabId);
		const console: ConsoleMessage[] = [];
		const errors: ConsoleMessage[] = [];
		const listener = (source: DebuggeeTarget, method: string, params: unknown) => {
			if (source.tabId !== tabId || !params || typeof params !== "object") return;
			const event = params as Record<string, unknown>;
			if (method === "Runtime.consoleAPICalled") {
				const args = Array.isArray(event.args) ? event.args : [];
				const level = String(event.type ?? "log");
				const entry = { level: level === "warning" ? "warn" : level, message: args.map(remoteObjectText).join(" "), timestamp: Number(event.timestamp) || undefined };
				console.push(entry);
				if (level === "error" || level === "assert") errors.push(entry);
			} else if (method === "Runtime.exceptionThrown") {
				const details = (event.exceptionDetails ?? {}) as Record<string, unknown>;
				const exception = (details.exception ?? {}) as Record<string, unknown>;
				errors.push({
					level: "error",
					message: String(exception.description ?? details.text ?? "Uncaught exception"),
					timestamp: Number(event.timestamp) || undefined,
					source: typeof details.url === "string" ? details.url : undefined,
				});
			}
		};
		this.chrome.debugger.onEvent.addListener(listener);
		try {
			await this.withDebugger(tabId, async (send) => {
				// Enabling Runtime replays the messages Chrome kept for the page.
				await send("Runtime.enable");
				await sleep(300);
				await send("Runtime.disable");
			});
		} finally {
			this.chrome.debugger.onEvent.removeListener(listener);
		}
		return { console: console.slice(-200), errors: errors.slice(-200) };
	}

	/** Inspect or answer a JavaScript dialog (alert/confirm/prompt). */
	async dialog(tabId: number, operation: "status" | "accept" | "dismiss", promptText?: string): Promise<Record<string, unknown>> {
		const pending = this.pendingDialogs.get(tabId);
		if (operation === "status") return pending ? { open: true, ...pending } : { open: false };
		if (!pending) throw new BridgeError("DIALOG_NOT_OPEN", "No dialog opened by an AO action is waiting on this page");
		return this.withDebugger(tabId, async (send) => {
			await send("Page.handleJavaScriptDialog", { accept: operation === "accept", ...(promptText !== undefined ? { promptText } : {}) });
			this.pendingDialogs.delete(tabId);
			return { open: true, ...pending, handled: operation };
		});
	}

	private moveEvents(tabId: number, point: Point, human: boolean): [string, Record<string, unknown>][] {
		const from = this.lastPointer.get(tabId);
		const path = human ? humanPath(from ?? { x: point.x - 120, y: point.y + 80 }, point) : [point];
		this.lastPointer.set(tabId, point);
		return path.map((step) => ["Input.dispatchMouseEvent", { type: "mouseMoved", x: step.x, y: step.y, button: "none", buttons: 0 }]);
	}
}

function linearPath(from: Point, to: Point, steps: number): Point[] {
	return Array.from({ length: steps }, (_, i) => {
		const t = (i + 1) / steps;
		return { x: from.x + (to.x - from.x) * t, y: from.y + (to.y - from.y) * t };
	});
}

function remoteObjectText(value: unknown): string {
	if (!value || typeof value !== "object") return String(value);
	const object = value as Record<string, unknown>;
	if ("value" in object) return typeof object.value === "string" ? object.value : JSON.stringify(object.value);
	return String(object.description ?? object.type ?? "");
}

function sleep(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}
