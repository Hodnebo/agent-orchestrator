import { act, create, type ReactTestRenderer } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

vi.mock("react-native", () => ({
	View: "View",
	Text: "Text",
	Pressable: "Pressable",
	Modal: "Modal",
	StyleSheet: { create: (styles: object) => styles, absoluteFill: {} },
	useWindowDimensions: () => ({ width: 390, height: 800 }),
}));
vi.mock("react-native-webview", () => ({ WebView: "WebView" }));
vi.mock("react-native-safe-area-context", () => ({ useSafeAreaInsets: () => ({ top: 0, bottom: 0, left: 0, right: 0 }) }));
vi.mock("expo-linking", () => ({ openURL: vi.fn() }));
vi.mock("../config", () => ({ authHeaders: () => ({ Authorization: "Bearer test" }), httpBase: () => "http://lab:3011" }));
vi.mock("../haptics", () => ({ haptics: { tap: () => {} } }));
vi.mock("../icons", () => ({ Feather: "Feather" }));
vi.mock("../store", () => ({ useApp: () => ({ config: { host: "lab", httpPort: "3011", muxPort: "", password: "test" } }) }));
vi.mock("../ThemeProvider", () => {
	const theme = new Proxy({}, { get: () => "#000" });
	return { useTheme: () => theme, useThemeState: () => ({ scheme: "dark" }), useThemedStyles: (factory: (t: unknown) => unknown) => factory(theme) };
});
vi.mock("./RenderSkeleton", () => ({ RenderSkeleton: "RenderSkeleton" }));
vi.mock("./renderHeightStore", () => ({ startRenderHeightStore: () => {} }));

import { RenderFrame } from "./RenderFrame";
import { notifyRenderViewport } from "./renderFrameModel";

const render = { id: "page", title: "Page", height: 300, path: "/api/v1/sessions/s/renders/page" };
// Where the frame sits in the window; measureInWindow reads it.
let frameTop = 100;
let renderer: ReactTestRenderer | undefined;

beforeEach(() => {
	vi.useFakeTimers();
	vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
	frameTop = 100;
	act(() => {
		renderer = create(<RenderFrame render={render} />, {
			createNodeMock: () => ({
				measureInWindow: (callback: (x: number, y: number, w: number, h: number) => void) => callback(0, frameTop, 350, 300),
				injectJavaScript: () => {},
			}),
		});
	});
	// Layout gives the frame a width and a first measure; it is on screen, so its page mounts.
	act(() => frame().props.onLayout({ nativeEvent: { layout: { width: 350 } } }));
});

afterEach(() => {
	act(() => renderer?.unmount());
	vi.useRealTimers();
	vi.unstubAllGlobals();
});

const frame = () => renderer!.root.findAll((node) => node.type === ("View" as never) && typeof node.props.onLayout === "function")[0];
const webView = () => renderer!.root.findAll((node) => node.type === ("WebView" as never))[0];
const skeletonShown = () => renderer!.root.findAll((node) => node.type === ("RenderSkeleton" as never)).length > 0;
const moveFrame = (top: number) => act(() => { frameTop = top; notifyRenderViewport(); });

it("keeps the skeleton for a page that returns after leaving range mid-load", () => {
	expect(webView()).toBeDefined();
	// The page's document loads, which arms the fallback that shows a page that never reports a height.
	act(() => webView().props.onLoadEnd());
	// Scrolled far out of range before the page reported its height: the WebView goes.
	moveFrame(10_000);
	expect(webView()).toBeUndefined();
	// The old page's fallback would have fired by now.
	act(() => { vi.advanceTimersByTime(5_000); });
	// Back in range: a fresh WebView loads, and the skeleton covers it until its height arrives.
	moveFrame(100);
	expect(webView()).toBeDefined();
	expect(skeletonShown()).toBe(true);
});

it("keeps the skeleton for a page remounted after its process died", () => {
	act(() => webView().props.onLoadEnd());
	// The OS ends the page's process; the frame mounts the page afresh.
	act(() => webView().props.onRenderProcessGone());
	expect(webView()).toBeDefined();
	act(() => { vi.advanceTimersByTime(5_000); });
	expect(skeletonShown()).toBe(true);
});

it("still shows a page that loads but never reports a height", () => {
	act(() => webView().props.onLoadEnd());
	expect(skeletonShown()).toBe(true);
	act(() => { vi.advanceTimersByTime(5_000); });
	expect(skeletonShown()).toBe(false);
});

it("shows the page as soon as it reports its height", () => {
	act(() => webView().props.onLoadEnd());
	act(() => webView().props.onMessage({ nativeEvent: { data: JSON.stringify({ type: "height", height: 280 }) } }));
	expect(skeletonShown()).toBe(false);
});
