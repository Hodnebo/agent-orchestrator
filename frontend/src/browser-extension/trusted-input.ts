// Trusted input (tier T1) through a transient chrome.debugger attachment.
// The extension attaches only for the duration of one action, uses only the
// Input domain for pointer and keyboard events, and detaches immediately.
// Runtime and Page are enabled only for an explicit console/errors/dialog
// request, then disabled again. Chrome shows its "started debugging this
// browser" bar while attached; that is the visible cost of trusted events.

import type { ChromeApi, DebuggeeTarget } from "./chrome-api";
import { humanPath, type Point } from "./human-path";
import { keyEvents } from "./keys";
import { BridgeError } from "./protocol";

const PROTOCOL_VERSION = "1.3";

type Send = (method: string, params?: Record<string, unknown>) => Promise<unknown>;

export type ConsoleMessage = { level: string; message: string; timestamp?: number; source?: string };

export class TrustedInput {
	private readonly locks = new Map<number, Promise<unknown>>();
	private readonly lastPointer = new Map<number, Point>();

	constructor(private readonly chrome: ChromeApi) {}

	/** Run fn with the debugger attached to tabId, serialized per tab. */
	async withDebugger<T>(tabId: number, fn: (send: Send) => Promise<T>): Promise<T> {
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
		try {
			return await fn((method, params) => this.chrome.debugger.sendCommand(target, method, params ?? {}));
		} finally {
			await this.chrome.debugger.detach(target).catch(() => undefined);
		}
	}

	async click(tabId: number, point: Point, options: { clicks?: number; human?: boolean } = {}): Promise<void> {
		const clicks = options.clicks ?? 1;
		await this.withDebugger(tabId, async (send) => {
			await this.moveTo(send, tabId, point, options.human === true);
			for (let i = 1; i <= clicks; i++) {
				await send("Input.dispatchMouseEvent", { type: "mousePressed", x: point.x, y: point.y, button: "left", buttons: 1, clickCount: i });
				await send("Input.dispatchMouseEvent", { type: "mouseReleased", x: point.x, y: point.y, button: "left", buttons: 0, clickCount: i });
			}
		});
	}

	async hover(tabId: number, point: Point, human = false): Promise<void> {
		await this.withDebugger(tabId, (send) => this.moveTo(send, tabId, point, human));
	}

	async drag(tabId: number, from: Point, to: Point, human = false): Promise<void> {
		await this.withDebugger(tabId, async (send) => {
			await this.moveTo(send, tabId, from, human);
			await send("Input.dispatchMouseEvent", { type: "mousePressed", x: from.x, y: from.y, button: "left", buttons: 1, clickCount: 1 });
			const path = human ? humanPath(from, to) : linearPath(from, to, 12);
			for (const point of path) {
				await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: point.x, y: point.y, button: "left", buttons: 1 });
			}
			await send("Input.dispatchMouseEvent", { type: "mouseReleased", x: to.x, y: to.y, button: "left", buttons: 0, clickCount: 1 });
			this.lastPointer.set(tabId, to);
		});
	}

	async insertText(tabId: number, text: string): Promise<void> {
		await this.withDebugger(tabId, (send) => send("Input.insertText", { text }).then(() => undefined));
	}

	async press(tabId: number, chord: string): Promise<void> {
		const events = keyEvents(chord);
		await this.withDebugger(tabId, async (send) => {
			for (const event of events) await send("Input.dispatchKeyEvent", event);
		});
	}

	/** Console messages and exceptions Chrome kept for the page. */
	async consoleMessages(tabId: number): Promise<{ console: ConsoleMessage[]; errors: ConsoleMessage[] }> {
		const console: ConsoleMessage[] = [];
		const errors: ConsoleMessage[] = [];
		const listener = (source: DebuggeeTarget, method: string, params: unknown) => {
			if (source.tabId !== tabId || !params || typeof params !== "object") return;
			const event = params as Record<string, unknown>;
			if (method === "Runtime.consoleAPICalled") {
				const args = Array.isArray(event.args) ? event.args : [];
				const text = args.map(remoteObjectText).join(" ");
				const level = String(event.type ?? "log");
				const entry = { level: level === "warning" ? "warn" : level, message: text, timestamp: Number(event.timestamp) || undefined };
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
		let opened: Record<string, unknown> | undefined;
		const listener = (source: DebuggeeTarget, method: string, params: unknown) => {
			if (source.tabId === tabId && method === "Page.javascriptDialogOpening" && params && typeof params === "object") {
				opened = params as Record<string, unknown>;
			}
		};
		this.chrome.debugger.onEvent.addListener(listener);
		try {
			return await this.withDebugger(tabId, async (send) => {
				await send("Page.enable");
				await sleep(200);
				try {
					if (!opened) {
						if (operation !== "status") throw new BridgeError("DIALOG_NOT_OPEN", "No dialog is open on this page");
						return { open: false };
					}
					const info = { open: true, type: String(opened.type ?? ""), message: String(opened.message ?? "") };
					if (operation === "status") return info;
					await send("Page.handleJavaScriptDialog", {
						accept: operation === "accept",
						...(promptText !== undefined ? { promptText } : {}),
					});
					return { ...info, handled: operation };
				} finally {
					await send("Page.disable").catch(() => undefined);
				}
			});
		} finally {
			this.chrome.debugger.onEvent.removeListener(listener);
		}
	}

	private async moveTo(send: Send, tabId: number, point: Point, human: boolean): Promise<void> {
		const from = this.lastPointer.get(tabId);
		const path = human && from ? humanPath(from, point) : human ? humanPath({ x: point.x - 120, y: point.y + 80 }, point) : [point];
		for (const step of path) {
			await send("Input.dispatchMouseEvent", { type: "mouseMoved", x: step.x, y: step.y, button: "none", buttons: 0 });
			if (human) await sleep(8 + Math.random() * 10);
		}
		this.lastPointer.set(tabId, point);
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
