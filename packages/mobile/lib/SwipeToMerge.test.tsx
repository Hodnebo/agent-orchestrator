import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, expect, it, vi } from "vitest";

const gesture = vi.hoisted(() => ({ callbacks: {} as Record<string, (...args: any[]) => void> }));
vi.mock("react-native", () => ({ View: "View", Text: "Text", ActivityIndicator: "ActivityIndicator", StyleSheet: { create: (styles: object) => styles } }));
vi.mock("react-native-gesture-handler", () => ({
	GestureDetector: "GestureDetector",
	Gesture: { Pan: () => {
		const pan: Record<string, (...args: any[]) => unknown> = {};
		for (const method of ["enabled", "activeOffsetX", "failOffsetY", "onBegin", "onUpdate", "onEnd", "onFinalize"]) {
			pan[method] = (callback) => { if (method.startsWith("on")) gesture.callbacks[method] = callback as never; return pan; };
		}
		return pan;
	} },
}));
vi.mock("react-native-reanimated", async () => {
	const React = await import("react");
	return {
		default: { View: "AnimatedView" },
		Easing: { out: (value: unknown) => value, inOut: (value: unknown) => value, cubic: null, quad: null },
		cancelAnimation: () => {},
		interpolate: () => 0,
		interpolateColor: () => "",
		useSharedValue: (value: unknown) => React.useRef({ value }).current,
		useAnimatedStyle: (style: () => unknown) => style,
		withRepeat: (value: unknown) => value,
		withSequence: (value: unknown) => value,
		withSpring: (value: number) => value,
		withTiming: (value: number, _options: unknown, done?: (finished: boolean) => void) => { if (done) queueMicrotask(() => done(true)); return value; },
	};
});
vi.mock("react-native-worklets", () => ({ scheduleOnRN: (fn: (...args: unknown[]) => void, ...args: unknown[]) => fn(...args) }));
vi.mock("./ThemeProvider", () => ({ useThemeState: () => ({ scheme: "dark" }) }));
vi.mock("./useReducedMotion", () => ({ useReducedMotion: () => true }));
vi.mock("./icons", () => ({ Feather: "Feather" }));
vi.mock("./haptics", () => ({ haptics: { tap: () => {}, select: () => {}, error: () => {} } }));
vi.mock("./tokens", () => ({ type: { subheadline: { fontSize: 15, lineHeight: 20 } }, space: { xs: 4 } }));

import { SwipeToMerge } from "./SwipeToMerge";

let renderer: ReactTestRenderer | undefined;
afterEach(() => { act(() => renderer?.unmount()); vi.unstubAllGlobals(); gesture.callbacks = {}; });

function mount(onMerge: () => Promise<boolean>) {
	vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
	act(() => { renderer = create(<SwipeToMerge label="Swipe to merge PR #1" busyLabel="Merging…" onMerge={onMerge} onAccessibilityActivate={() => {}} />); });
	// A 300pt track leaves the knob 244pt of travel.
	const track = renderer!.root.findAll((node) => typeof node.props.onLayout === "function")[0];
	act(() => track.props.onLayout({ nativeEvent: { layout: { width: 300 } } }));
}

it("counts the distance a fast flick covers before the pan activates", async () => {
	const onMerge = vi.fn(async () => false);
	mount(onMerge);
	// Android reports translation from the activation point, so a fast flick's
	// first jump never shows up in translationX; the finger still reached the end.
	await act(async () => {
		gesture.callbacks.onBegin({ absoluteX: 20, translationX: 0 });
		gesture.callbacks.onUpdate({ absoluteX: 90, translationX: 0 });
		gesture.callbacks.onUpdate({ absoluteX: 200, translationX: 110 });
		gesture.callbacks.onUpdate({ absoluteX: 250, translationX: 160 });
		gesture.callbacks.onEnd({ absoluteX: 270, translationX: 180 });
	});
	expect(onMerge).toHaveBeenCalledTimes(1);
});

it("does not merge when the finger stops short of the end", async () => {
	const onMerge = vi.fn(async () => false);
	mount(onMerge);
	await act(async () => {
		gesture.callbacks.onBegin({ absoluteX: 20, translationX: 0 });
		gesture.callbacks.onUpdate({ absoluteX: 150, translationX: 130 });
		gesture.callbacks.onEnd({ absoluteX: 180, translationX: 160 });
	});
	expect(onMerge).not.toHaveBeenCalled();
});
