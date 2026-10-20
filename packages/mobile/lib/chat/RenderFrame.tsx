import * as Linking from "expo-linking";
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Modal, Pressable, StyleSheet, Text, View, useWindowDimensions } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { WebView } from "react-native-webview";
import { authHeaders, httpBase } from "../config";
import { haptics } from "../haptics";
import { Feather } from "../icons";
import { useApp } from "../store";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles, useThemeState } from "../ThemeProvider";
import { space, type } from "../tokens";
import {
	RENDER_BRIDGE_SCRIPT,
	RENDER_ZOOM_SCRIPT,
	clampRenderHeight,
	initialRenderHeight,
	isRenderPageLoad,
	readRenderBridgeMessage,
	rememberRenderHeight,
	rememberedRenderHeight,
	renderFrameLive,
	renderTheme,
	renderThemeFragment,
	renderThemeScript,
	subscribeRenderViewport,
	type RenderDisplayMode,
	type RenderRef,
	type RenderTheme,
} from "./renderFrameModel";

// A link opens only from a touch the viewer just received, so a page cannot
// open one on its own by posting to the bridge.
const LINK_TAP_WINDOW_MS = 1000;

function useRenderTheme(): RenderTheme {
	const t = useTheme();
	const { scheme } = useThemeState();
	return useMemo(() => renderTheme(t, scheme === "light" ? "light" : "dark"), [t, scheme]);
}

/**
 * The page in a WebView. The daemon serves it with its images inlined, so the
 * one authenticated request is all it needs; its CSP sandbox keeps it on an
 * opaque origin. Theme rides in the fragment for first paint and is posted
 * after, so the page never reloads.
 */
function RenderDocument({ path, displayMode, onHeight, onFailed }: {
	path: string;
	displayMode: RenderDisplayMode;
	onHeight?: (height: number) => void;
	onFailed: () => void;
}) {
	const styles = useThemedStyles(makeStyles);
	const { config } = useApp();
	const web = useRef<WebView>(null);
	const lastTouch = useRef(0);
	const loaded = useRef(false);
	const theme = useRenderTheme();
	const themeRef = useRef(theme);
	themeRef.current = theme;
	const pageUrl = config ? `${httpBase(config)}${path}` : undefined;
	// The fragment only seeds first paint; a theme change is posted instead.
	const uri = useMemo(() => pageUrl && `${pageUrl}${renderThemeFragment(themeRef.current, displayMode)}`, [pageUrl, displayMode]);
	const fullscreen = displayMode === "fullscreen";
	// A stable source: a new object each render would make the WebView re-check it.
	const source = useMemo(() => uri && config ? { uri, headers: authHeaders(config) } : undefined, [uri, config]);

	useEffect(() => {
		if (loaded.current) web.current?.injectJavaScript(renderThemeScript(theme));
	}, [theme]);

	if (!uri || !source) return null;
	return <View style={styles.document} onTouchStart={() => { lastTouch.current = Date.now(); }} onTouchEnd={() => { lastTouch.current = Date.now(); }}>
		<WebView
			ref={web}
			source={source}
			injectedJavaScript={fullscreen ? `${RENDER_ZOOM_SCRIPT}\n${RENDER_BRIDGE_SCRIPT}` : RENDER_BRIDGE_SCRIPT}
			onMessage={(event) => {
				const message = readRenderBridgeMessage(event.nativeEvent.data);
				if (message?.type === "height") onHeight?.(message.height);
				else if (message?.type === "link" && fullscreen && Date.now() - lastTouch.current < LINK_TAP_WINDOW_MS) void Linking.openURL(message.url);
			}}
			// Only the page itself loads in the frame; a frame inside the page may load its own.
			onShouldStartLoadWithRequest={(request) => request.isTopFrame === false || isRenderPageLoad(request.url, uri)}
			onLoadEnd={() => {
				loaded.current = true;
				web.current?.injectJavaScript(renderThemeScript(themeRef.current));
			}}
			onHttpError={onFailed}
			onError={onFailed}
			setSupportMultipleWindows={false}
			javaScriptCanOpenWindowsAutomatically={false}
			allowsLinkPreview={false}
			// Inline, the page sits still in the thread; expanded, it scrolls and pinch-zooms.
			scrollEnabled={fullscreen}
			bounces={fullscreen}
			setBuiltInZoomControls={fullscreen}
			setDisplayZoomControls={false}
			overScrollMode={fullscreen ? "always" : "never"}
			showsVerticalScrollIndicator={fullscreen}
			showsHorizontalScrollIndicator={false}
			style={styles.web}
		/>
	</View>;
}

/** The page over the whole screen, scrollable and pinch-zoomable. */
function RenderViewer({ render, visible, onClose }: { render: RenderRef; visible: boolean; onClose: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const insets = useSafeAreaInsets();
	const [failed, setFailed] = useState(false);
	const close = () => { haptics.tap(); onClose(); };
	return <Modal visible={visible} animationType="fade" statusBarTranslucent onRequestClose={onClose} onShow={() => setFailed(false)}>
		<View style={[styles.viewer, { paddingTop: insets.top }]}>
			<View style={styles.viewerHeader}>
				<Text numberOfLines={1} accessibilityRole="header" style={styles.viewerTitle}>{render.title}</Text>
				<Pressable accessibilityRole="button" accessibilityLabel="Close" hitSlop={10} onPress={close} style={styles.viewerClose}>
					<Feather name="x" size={18} color={t.textPrimary} />
				</Pressable>
			</View>
			{failed
				? <View style={styles.viewerError}><Text style={styles.fallbackTitle}>Couldn't load this page.</Text></View>
				: visible ? <View style={styles.viewerBody}><RenderDocument path={render.path} displayMode="fullscreen" onFailed={() => setFailed(true)} /></View> : null}
		</View>
	</Modal>;
}

/**
 * An agent's HTML page inline in its turn, as desktop shows it; a tap expands
 * it. Only a frame in or near view holds a WebView: a page can run to megabytes
 * of inlined images, and a thread can carry many. Out of range, a placeholder
 * keeps the page's last height so the thread does not jump when it returns.
 */
export const RenderFrame = memo(function RenderFrame({ render }: { render: RenderRef }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const { config } = useApp();
	const frame = useRef<View>(null);
	const { height: viewportHeight } = useWindowDimensions();
	const [width, setWidth] = useState<number>();
	const [contentHeight, setContentHeight] = useState(() => rememberedRenderHeight(render.path));
	const [live, setLive] = useState(false);
	const [failed, setFailed] = useState(false);
	const [attempt, setAttempt] = useState(0);
	const [expanded, setExpanded] = useState(false);
	const measure = useCallback(() => {
		frame.current?.measureInWindow((_x, y, _width, height) => {
			// A cell the list has clipped measures as zero; leave it as it was.
			if (height > 0) setLive((current) => renderFrameLive(y, height, viewportHeight, current));
		});
	}, [viewportHeight]);
	useEffect(() => subscribeRenderViewport(measure), [measure]);
	const onHeight = useCallback((height: number) => {
		rememberRenderHeight(render.path, height);
		setContentHeight(height);
	}, [render.path]);

	if (!config || failed) {
		return <Pressable
			disabled={!config}
			accessibilityRole={config ? "button" : undefined}
			accessibilityLabel={config ? `Retry loading ${render.title}` : undefined}
			onPress={() => { haptics.tap(); setFailed(false); setAttempt((value) => value + 1); }}
			style={styles.fallback}
		>
			<Feather name={config ? "refresh-cw" : "file-text"} size={12} color={t.textTertiary} />
			<Text numberOfLines={1} style={styles.fallbackTitle}>{render.title}</Text>
			{config ? <Text style={styles.fallbackAction}>Couldn't load · Tap to retry</Text> : null}
		</Pressable>;
	}

	const height = clampRenderHeight(contentHeight ?? initialRenderHeight(render, width));
	return <View ref={frame} onLayout={(event) => { setWidth(Math.round(event.nativeEvent.layout.width)); measure(); }} style={[styles.frame, { height }]}>
		{width && live
			? <RenderDocument key={attempt} path={render.path} displayMode="inline" onHeight={onHeight} onFailed={() => setFailed(true)} />
			: <View style={styles.placeholder}><Feather name="file-text" size={12} color={t.textFaint} /><Text numberOfLines={1} style={styles.placeholderTitle}>{render.title}</Text></View>}
		{/* Inline, a tap means "show me this larger"; links and zoom live in the expanded view. */}
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`Expand ${render.title}`}
			onPress={() => { haptics.tap(); setExpanded(true); }}
			style={StyleSheet.absoluteFill}
		>
			<View style={styles.expandChip}><Feather name="maximize" size={13} color={t.textSecondary} /></View>
		</Pressable>
		<RenderViewer render={render} visible={expanded} onClose={() => setExpanded(false)} />
	</View>;
}, (previous, next) => sameRender(previous.render, next.render));

/** The timeline re-reads each row on every update; the same page must not re-render. */
function sameRender(a: RenderRef, b: RenderRef): boolean {
	return a.id === b.id && a.path === b.path && a.title === b.title && a.height === b.height && JSON.stringify(a.heights) === JSON.stringify(b.heights);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	frame: { marginVertical: space.xs, overflow: "hidden", backgroundColor: t.bgBase },
	document: { flex: 1 },
	web: { flex: 1, backgroundColor: t.bgBase },
	placeholder: { flexDirection: "row", alignItems: "center", gap: space.xs, padding: space.sm },
	placeholderTitle: { fontFamily: "Geist_400Regular", flexShrink: 1, color: t.textFaint, fontSize: type.caption1.fontSize },
	expandChip: { position: "absolute", top: space.xs, right: space.xs, width: 28, height: 28, borderRadius: 14, alignItems: "center", justifyContent: "center", backgroundColor: t.bgElevated, borderWidth: 1, borderColor: t.borderSubtle },
	viewer: { flex: 1, backgroundColor: t.bgBase },
	viewerHeader: { flexDirection: "row", alignItems: "center", gap: space.sm, paddingLeft: space.lg, paddingRight: space.sm, paddingVertical: space.xs },
	viewerTitle: { flex: 1, fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.subheadline.fontSize, fontWeight: "600" },
	viewerClose: { width: 36, height: 36, borderRadius: 18, alignItems: "center", justifyContent: "center", backgroundColor: t.bgElevated },
	// The thread's own inset, so the page sits where it did inline.
	viewerBody: { flex: 1, paddingHorizontal: space.xl },
	viewerError: { flex: 1, alignItems: "center", justifyContent: "center" },
	fallback: { alignSelf: "flex-start", marginVertical: space.xs, flexDirection: "row", alignItems: "center", gap: space.xs, borderRadius: 8, borderWidth: 1, borderColor: t.borderSubtle, paddingHorizontal: space.sm, paddingVertical: space.xs },
	fallbackTitle: { fontFamily: "Geist_400Regular", flexShrink: 1, color: t.textSecondary, fontSize: type.caption1.fontSize },
	fallbackAction: { fontFamily: "Geist_600SemiBold", color: t.accent, fontSize: type.caption2.fontSize, fontWeight: "600" },
});
