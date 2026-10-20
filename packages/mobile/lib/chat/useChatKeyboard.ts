import { useHeaderHeight } from "expo-router/build/react-navigation/elements";
import { useEffect, useReducer, useRef } from "react";
import { Keyboard, Platform } from "react-native";
import { KeyboardEvents } from "react-native-keyboard-controller";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import {
	CHAT_KEYBOARD_HIDDEN,
	chatDockRiseOverride,
	dockRestingInset,
	keyboardVerticalOffset,
	reduceChatKeyboard,
	type ChatKeyboardSignal,
	type ChatKeyboardState,
} from "../session/keyboardInset";

const platform = Platform.OS === "ios" ? "ios" : "android";

function initialState(insetsBottom: number): ChatKeyboardState {
	// A screen can mount with the keyboard already up (focus kept across a push).
	// React Native's own record is the one that cannot have gone stale.
	if (!Keyboard.isVisible()) return CHAT_KEYBOARD_HIDDEN;
	const height = Keyboard.metrics()?.height ?? 0;
	return reduceChatKeyboard(platform, CHAT_KEYBOARD_HIDDEN, { kind: "platform-shown", height, insetsBottom });
}

/**
 * Keyboard avoidance shared by the session chat and the reviewer chat, so the
 * two composers sit in the same place and recover the same way. See
 * `reduceChatKeyboard` for why it listens to two sources.
 *
 * iOS keeps keyboard-controller's avoiding view, which interpolates its padding
 * with the keyboard. Android pads the root by hand: edge-to-edge defeats
 * `adjustResize`, so the window no longer shrinks for the keyboard.
 */
export function useChatKeyboard() {
	const headerHeight = useHeaderHeight();
	const insets = useSafeAreaInsets();
	const insetsBottom = useRef(insets.bottom);
	insetsBottom.current = insets.bottom;
	const [state, dispatch] = useReducer(
		(current: ChatKeyboardState, signal: ChatKeyboardSignal) => reduceChatKeyboard(platform, current, signal),
		insets.bottom,
		initialState,
	);

	useEffect(() => {
		const subscriptions = [
			KeyboardEvents.addListener("keyboardWillShow", (event) => dispatch({ kind: "controller-show", height: event.height })),
			KeyboardEvents.addListener("keyboardWillHide", () => dispatch({ kind: "controller-hide-start" })),
			KeyboardEvents.addListener("keyboardDidHide", () => dispatch({ kind: "controller-hidden" })),
			Keyboard.addListener("keyboardDidShow", (event) => dispatch({ kind: "platform-shown", height: event.endCoordinates.height, insetsBottom: insetsBottom.current })),
			Keyboard.addListener("keyboardDidHide", () => dispatch({ kind: "platform-hidden" })),
		];
		return () => subscriptions.forEach((subscription) => subscription.remove());
	}, []);

	return {
		keyboardVisible: state.visible,
		rootStyle: platform === "android" ? { paddingBottom: state.visible ? state.height : 0 } : undefined,
		behavior: platform === "ios" ? "padding" as const : undefined,
		keyboardVerticalOffset: platform === "ios" ? keyboardVerticalOffset(headerHeight) : 0,
		restingInset: dockRestingInset(insets.bottom),
		dockRiseOverride: platform === "android" ? chatDockRiseOverride(state) : undefined,
	};
}
