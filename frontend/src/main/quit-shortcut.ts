// Like Chrome and other macOS apps, a single stray ⌘Q only warns; a second
// press inside this window quits. The renderer shows the warning for the same
// duration so the hint disappears exactly when the second press stops counting.
export const QUIT_SHORTCUT_CONFIRM_MS = 2_500;

export function createQuitShortcutGuard(options: { now?: () => number; windowMs?: number } = {}) {
	const now = options.now ?? Date.now;
	const windowMs = options.windowMs ?? QUIT_SHORTCUT_CONFIRM_MS;
	let armedAt: number | null = null;

	return {
		/** Records a ⌘Q press; true when it confirms an earlier one and the app should quit. */
		press(): boolean {
			const time = now();
			if (armedAt !== null && time - armedAt <= windowMs) {
				armedAt = null;
				return true;
			}
			armedAt = time;
			return false;
		},
	};
}
