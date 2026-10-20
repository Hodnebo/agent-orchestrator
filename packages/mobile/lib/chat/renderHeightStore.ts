import AsyncStorage from "@react-native-async-storage/async-storage";
import { restoreRememberedHeights, serializeRememberedHeights, subscribeRememberedHeights } from "./renderFrameModel";

// Page heights only: no content, no credentials. Plain app storage is fine.
const KEY = "ao.chatRenderHeights.v1";
// A thread reports many heights as it loads; save once it settles.
const SAVE_DELAY_MS = 1000;

let saveTimer: ReturnType<typeof setTimeout> | undefined;

/**
 * Remembered render heights outlive the launch, so a page opens at the height
 * it had last time instead of shifting once more. Loaded as the chat code is
 * first imported, well before a conversation opens.
 */
export function startRenderHeightStore(): void {
	void AsyncStorage.getItem(KEY).then(restoreRememberedHeights, () => {});
	subscribeRememberedHeights(() => {
		clearTimeout(saveTimer);
		saveTimer = setTimeout(() => void AsyncStorage.setItem(KEY, serializeRememberedHeights()).catch(() => {}), SAVE_DELAY_MS);
	});
}
