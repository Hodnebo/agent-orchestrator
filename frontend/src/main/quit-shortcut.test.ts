import { describe, expect, it } from "vitest";
import { createQuitShortcutGuard } from "./quit-shortcut";

describe("createQuitShortcutGuard", () => {
	it("warns on the first press and quits on a second press inside the window", () => {
		let time = 1_000;
		const guard = createQuitShortcutGuard({ now: () => time, windowMs: 2_000 });

		expect(guard.press()).toBe(false);
		time += 1_999;
		expect(guard.press()).toBe(true);
	});

	it("re-arms instead of quitting when the second press comes too late", () => {
		let time = 0;
		const guard = createQuitShortcutGuard({ now: () => time, windowMs: 2_000 });

		expect(guard.press()).toBe(false);
		time += 2_001;
		expect(guard.press()).toBe(false);
		time += 500;
		expect(guard.press()).toBe(true);
	});

	it("starts over after a confirmed quit so a cancelled quit needs two presses again", () => {
		let time = 0;
		const guard = createQuitShortcutGuard({ now: () => time, windowMs: 2_000 });

		guard.press();
		expect(guard.press()).toBe(true);
		time += 100;
		expect(guard.press()).toBe(false);
	});
});
