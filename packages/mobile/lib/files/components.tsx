import { memo, useEffect, useMemo, useState } from "react";
import {
	ActivityIndicator,
	FlatList,
	Modal,
	Platform,
	Pressable,
	StyleSheet,
	Text,
	TextInput,
	View,
	type AccessibilityActionEvent,
} from "react-native";
import { KeyboardStickyView } from "react-native-keyboard-controller";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { highlightCode, type SyntaxTokenKind } from "../chat/syntaxHighlight";
import { haptics } from "../haptics";
import { Feather, type FeatherIconName } from "../icons";
import type { Theme } from "../theme";
import { textSelectionColors } from "../textSelection";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { font, fontScaleCap, iconSize, press, radius, space, touchTarget, type } from "../tokens";
import type { WorkspaceFileStatus } from "./api";
import { changeAccessibilityLabel, plural, sectionLabel, splitPath, statusGlyph, statusLabel, type ChangeEntry, type DisplayRow, type LineRow } from "./model";
import type { ReviewComment } from "./reviewStore";

// ---- Segmented tabs -----------------------------------------------------------

export function SegmentedTabs<K extends string>({ value, options, onChange }: {
	value: K;
	options: Array<{ key: K; label: string; icon?: FeatherIconName; badge?: number }>;
	onChange: (key: K) => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<View accessibilityRole="tablist" style={styles.segmented}>
			{options.map((option) => {
				const selected = option.key === value;
				return (
					<Pressable
						key={option.key}
						accessibilityRole="tab"
						accessibilityState={{ selected }}
						accessibilityLabel={option.badge ? `${option.label}, ${option.badge}` : option.label}
						onPress={() => { if (!selected) { haptics.select(); onChange(option.key); } }}
						style={({ pressed }) => [styles.segment, selected && styles.segmentOn, pressed && !selected && { opacity: press.opacity }]}
					>
						{option.icon ? <Feather name={option.icon} size={iconSize.sm} color={selected ? t.textPrimary : t.textTertiary} /> : null}
						<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={[styles.segmentLabel, selected && styles.segmentLabelOn]}>{option.label}</Text>
						{option.badge ? <Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.segmentBadge}>{option.badge}</Text> : null}
					</Pressable>
				);
			})}
		</View>
	);
}

// ---- Status + counts ------------------------------------------------------------

export function statusColor(status: WorkspaceFileStatus, t: Theme): string {
	switch (status) {
		case "added": return t.green;
		case "deleted": return t.red;
		case "renamed": return t.blue;
		default: return t.textSecondary;
	}
}

export function StatusGlyph({ status }: { status: WorkspaceFileStatus }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const color = statusColor(status, t);
	return (
		<View accessible={false} style={[styles.statusGlyph, { borderColor: color }]}>
			<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={[styles.statusGlyphText, { color }]}>{statusGlyph[status]}</Text>
		</View>
	);
}

export function LineCounts({ additions, deletions, binary }: { additions: number; deletions: number; binary?: boolean }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	if (binary) return <Text maxFontSizeMultiplier={fontScaleCap.chrome} style={[styles.counts, { color: t.textTertiary }]}>binary</Text>;
	return (
		<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.counts}>
			{additions ? <Text style={{ color: t.green }}>+{additions}</Text> : null}
			{additions && deletions ? " " : null}
			{deletions ? <Text style={{ color: t.red }}>−{deletions}</Text> : null}
			{!additions && !deletions ? <Text style={{ color: t.textTertiary }}>±0</Text> : null}
		</Text>
	);
}

// ---- Changed-file row -------------------------------------------------------------

export const ChangeRow = memo(function ChangeRow({ file, comments, onPress }: { file: ChangeEntry; comments: number; onPress: (file: ChangeEntry) => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const { name, dir } = splitPath(file.path);
	const badges = file.sections.filter((section) => section !== "unstaged" && section !== "committed").map((section) => sectionLabel[section]);
	const detail = [file.previousPath ? `from ${file.previousPath}` : dir, ...badges].filter(Boolean).join(" · ");
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${changeAccessibilityLabel(file)}${comments ? `, ${plural(comments, "queued comment")}` : ""}`}
			accessibilityHint="Opens the diff"
			onPress={() => { haptics.tap(); onPress(file); }}
			style={({ pressed }) => [styles.fileRow, pressed && { backgroundColor: t.bgElevatedHover }]}
		>
			<StatusGlyph status={file.status} />
			<View style={styles.fileRowText}>
				<Text numberOfLines={1} maxFontSizeMultiplier={fontScaleCap.body} style={[styles.fileName, file.status === "deleted" && styles.fileNameDeleted]}>{name}</Text>
				{detail ? <Text numberOfLines={1} ellipsizeMode="head" maxFontSizeMultiplier={fontScaleCap.body} style={styles.fileDir}>{detail}</Text> : null}
			</View>
			{comments ? (
				<View style={styles.commentCount}>
					<Feather name="message-square" size={iconSize.xs} color={t.textSecondary} />
					<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.commentCountText}>{comments}</Text>
				</View>
			) : null}
			<LineCounts additions={file.additions} deletions={file.deletions} binary={file.binary} />
			<Feather name="chevron-right" size={iconSize.sm} color={t.textFaint} />
		</Pressable>
	);
});

// ---- Diff lines ---------------------------------------------------------------------

/** Monospace metrics for the code rows: fixed, so unwrapped rows can be measured without layout. */
export const CODE_FONT_SIZE = 12.5;
export const CODE_LINE_HEIGHT = 19;
const CODE_CHAR_WIDTH = CODE_FONT_SIZE * 0.6;

export function codeContentWidth(rows: DisplayRow[], gutterDigits: number): number {
	let longest = 0;
	for (const row of rows) if (row.type === "line" && row.row.text.length > longest) longest = row.row.text.length;
	return gutterWidth(gutterDigits) + MARKER_WIDTH + space.lg + Math.ceil(longest * CODE_CHAR_WIDTH);
}

const MARKER_WIDTH = 16;
export function gutterWidth(digits: number): number {
	return Math.max(2, digits) * CODE_CHAR_WIDTH + space.md;
}

function tokenColor(kind: SyntaxTokenKind, t: Theme): string {
	switch (kind) {
		case "comment": return t.textFaint;
		case "string": return t.green;
		case "number": return t.orange;
		case "keyword": return t.amber;
		case "type":
		case "meta": return t.textSecondary;
		default: return t.textPrimary;
	}
}

function lineWord(row: LineRow): string {
	return row.kind === "add" ? "Added" : row.kind === "del" ? "Removed" : "Line";
}

export const DiffLine = memo(function DiffLine({
	row,
	index,
	language,
	wrap,
	digits,
	selected,
	commented,
	selectionActive,
	onSelect,
	onExtend,
}: {
	row: LineRow;
	index: number;
	language?: string;
	wrap: boolean;
	digits: number;
	selected: boolean;
	commented: boolean;
	selectionActive: boolean;
	onSelect: (index: number) => void;
	onExtend: (index: number) => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const tokens = useMemo(() => row.segments ? undefined : highlightCode(row.text, language), [language, row.segments, row.text]);
	const number = row.kind === "del" ? row.oldNo : row.newNo;
	const marker = row.kind === "add" ? "+" : row.kind === "del" ? "−" : " ";
	const tint = row.kind === "add" ? t.tintGreen : row.kind === "del" ? t.tintRed : undefined;
	const strong = row.kind === "add" ? withStrongTint(t.tintGreen) : withStrongTint(t.tintRed);
	const onAction = (event: AccessibilityActionEvent) => {
		if (event.nativeEvent.actionName === "extend") onExtend(index);
		else onSelect(index);
	};
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${lineWord(row)} ${number ?? ""}${commented ? ", has a comment" : ""}: ${row.text.trim() || "blank line"}`}
			accessibilityHint={selectionActive ? "Selects this line. Use the extend action to grow the selection." : "Selects this line to comment or ask about it"}
			accessibilityState={{ selected }}
			accessibilityActions={[{ name: "activate" }, { name: "extend", label: "Extend selection to this line" }]}
			onAccessibilityAction={onAction}
			onPress={() => { haptics.select(); onSelect(index); }}
			onLongPress={() => { haptics.select(); onExtend(index); }}
			delayLongPress={300}
			style={[styles.line, tint ? { backgroundColor: tint } : null, selected && styles.lineSelected]}
		>
			<View style={[styles.gutter, { width: gutterWidth(digits) }]}>
				{commented ? <View style={styles.commentDot} /> : null}
				<Text maxFontSizeMultiplier={1} style={styles.gutterText}>{number ?? ""}</Text>
			</View>
			<Text maxFontSizeMultiplier={1} style={[styles.marker, { color: row.kind === "add" ? t.green : row.kind === "del" ? t.red : t.textFaint }]}>{marker}</Text>
			<Text maxFontSizeMultiplier={1.4} numberOfLines={wrap ? undefined : 1} style={[styles.code, wrap ? styles.codeWrap : null]}>
				{row.segments
					? row.segments.map((segment, at) => (
						<Text key={at} style={segment.changed ? { backgroundColor: strong } : undefined}>{segment.text}</Text>
					))
					: tokens
						? tokens.map((token, at) => <Text key={at} style={{ color: tokenColor(token.kind, t) }}>{token.text}</Text>)
						: row.text || " "}
			</Text>
		</Pressable>
	);
});

/** The word-level highlight: the line's own tint, roughly doubled. */
function withStrongTint(tint: string): string {
	const rgba = /^rgba\((\d+),\s*(\d+),\s*(\d+),\s*([\d.]+)\)$/.exec(tint);
	if (rgba) return `rgba(${rgba[1]}, ${rgba[2]}, ${rgba[3]}, ${Math.min(0.6, Number(rgba[4]) * 2.4)})`;
	const hex = /^#([0-9a-f]{6})([0-9a-f]{2})$/i.exec(tint);
	if (hex) return `#${hex[1]}${Math.min(0x99, parseInt(hex[2], 16) * 2 + 0x20).toString(16).padStart(2, "0")}`;
	return tint;
}

export const FoldRow = memo(function FoldRow({ hidden, section, expandable, onExpand }: { hidden: number; section?: string; expandable: boolean; onExpand: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const step = Math.min(hidden, 20);
	const label = expandable
		? hidden > step ? `Show ${step} of ${plural(hidden, "hidden line")}` : `Show ${plural(hidden, "hidden line")}`
		: `${plural(hidden, "unchanged line")}`;
	return (
		<Pressable
			disabled={!expandable}
			accessibilityRole={expandable ? "button" : "text"}
			accessibilityLabel={section ? `${label}, ${section}` : label}
			onPress={() => { haptics.tap(); onExpand(); }}
			style={({ pressed }) => [styles.fold, pressed && { opacity: press.opacity }]}
		>
			<Feather name={expandable ? "unfold-vertical" : "more-horizontal"} size={iconSize.sm} color={t.textTertiary} />
			<Text maxFontSizeMultiplier={fontScaleCap.chrome} numberOfLines={1} style={styles.foldText}>
				{label}
				{section ? <Text style={styles.foldSection}>{`  ${section}`}</Text> : null}
			</Text>
		</Pressable>
	);
});

// ---- Selection bar + comment composer ---------------------------------------------

export type ComposerAction = "comment" | "send" | "explain" | "change";

export function SelectionComposer({
	label,
	busy,
	error,
	initialText,
	onAction,
	onCopy,
	onClose,
}: {
	label: string;
	busy: boolean;
	error?: string;
	initialText?: string;
	onAction: (action: ComposerAction, text: string) => void;
	onCopy: () => void;
	onClose: () => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const insets = useSafeAreaInsets();
	// Extending the range keeps what was typed; a new selection remounts the composer.
	const [text, setText] = useState(initialText ?? "");
	const trimmed = text.trim();
	return (
		<KeyboardStickyView offset={{ closed: 0, opened: insets.bottom }} style={styles.composerDock}>
			<View accessibilityViewIsModal={false} style={[styles.composer, { paddingBottom: Math.max(insets.bottom, space.md) }]}>
				<View style={styles.composerHead}>
					<Feather name="message-square-text" size={iconSize.sm} color={t.textSecondary} />
					<Text accessibilityRole="header" maxFontSizeMultiplier={fontScaleCap.chrome} numberOfLines={1} style={styles.composerLabel}>{label}</Text>
					<Pressable accessibilityRole="button" accessibilityLabel="Copy selected lines" hitSlop={8} onPress={() => { haptics.success(); onCopy(); }} style={styles.composerIcon}>
						<Feather name="copy" size={iconSize.md} color={t.textSecondary} />
					</Pressable>
					<Pressable accessibilityRole="button" accessibilityLabel="Clear selection" hitSlop={8} onPress={() => { haptics.tap(); onClose(); }} style={styles.composerIcon}>
						<Feather name="x" size={iconSize.md} color={t.textSecondary} />
					</Pressable>
				</View>
				<TextInput
					value={text}
					onChangeText={setText}
					multiline
					editable={!busy}
					placeholder="Comment for the agent…"
					{...textSelectionColors(t)}
					placeholderTextColor={t.textTertiary}
					accessibilityLabel={`Comment on ${label}`}
					style={styles.composerInput}
					maxFontSizeMultiplier={fontScaleCap.body}
				/>
				{error ? <Text accessibilityRole="alert" style={styles.composerError}>{error}</Text> : null}
				<View style={styles.composerActions}>
					<QuickAction icon="sparkles" label="Explain" disabled={busy} onPress={() => onAction("explain", text)} />
					<QuickAction icon="edit-3" label="Make change" disabled={busy || !trimmed} onPress={() => onAction("change", text)} />
					<View style={{ flex: 1 }} />
					<Pressable
						accessibilityRole="button"
						accessibilityLabel="Add comment to review"
						accessibilityHint="Queues the comment so several can be sent together"
						accessibilityState={{ disabled: busy || !trimmed }}
						disabled={busy || !trimmed}
						onPress={() => { haptics.tap(); onAction("comment", text); }}
						style={({ pressed }) => [styles.composerSecondary, (!trimmed || busy) && styles.disabled, pressed && { opacity: press.opacity }]}
					>
						<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.composerSecondaryText}>Add</Text>
					</Pressable>
					<Pressable
						accessibilityRole="button"
						accessibilityLabel="Send comment now"
						accessibilityState={{ disabled: busy || !trimmed, busy }}
						disabled={busy || !trimmed}
						onPress={() => { haptics.tap(); onAction("send", text); }}
						style={({ pressed }) => [styles.composerPrimary, (!trimmed || busy) && styles.disabled, pressed && { opacity: press.opacity }]}
					>
						{busy ? <ActivityIndicator size="small" color={t.onAccent} /> : <Feather name="arrow-up" size={iconSize.md} color={t.onAccent} />}
					</Pressable>
				</View>
			</View>
		</KeyboardStickyView>
	);
}

function QuickAction({ icon, label, disabled, onPress }: { icon: FeatherIconName; label: string; disabled?: boolean; onPress: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			accessibilityState={{ disabled }}
			disabled={disabled}
			onPress={() => { haptics.tap(); onPress(); }}
			style={({ pressed }) => [styles.quick, disabled && styles.disabled, pressed && { opacity: press.opacity }]}
		>
			<Feather name={icon} size={iconSize.sm} color={t.textSecondary} />
			<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.quickText}>{label}</Text>
		</Pressable>
	);
}

// ---- Review tray ---------------------------------------------------------------------

/**
 * The floating "Send N comments" pill and the sheet behind it. It stays above
 * the content without covering the line being reviewed, the way GitHub's
 * Finish Review pill does, and is reachable from every Files screen.
 */
export function ReviewTray({
	comments,
	sending,
	error,
	bottomOffset = 0,
	onSend,
	onRemove,
}: {
	comments: readonly ReviewComment[];
	sending: boolean;
	error?: string;
	bottomOffset?: number;
	onSend: () => void;
	onRemove: (id: string) => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const insets = useSafeAreaInsets();
	const [open, setOpen] = useState(false);
	useEffect(() => { if (!comments.length) setOpen(false); }, [comments.length]);
	if (!comments.length) return null;
	const count = plural(comments.length, "comment");
	return (
		<>
			<View pointerEvents="box-none" style={[styles.trayDock, { bottom: Math.max(insets.bottom, space.md) + bottomOffset }]}>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={`Review ${count} before sending`}
					onPress={() => { haptics.tap(); setOpen(true); }}
					style={({ pressed }) => [styles.trayPill, pressed && { opacity: press.opacity }]}
				>
					<Feather name="message-square" size={iconSize.sm} color={t.onAccent} />
					<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.trayPillText}>{`Send ${count}`}</Text>
				</Pressable>
			</View>
			<Modal visible={open} animationType="slide" presentationStyle={Platform.OS === "ios" ? "pageSheet" : undefined} onRequestClose={() => setOpen(false)}>
				<View style={[styles.sheet, { paddingBottom: Math.max(insets.bottom, space.lg) }]}>
					<View style={styles.sheetHead}>
						<Text accessibilityRole="header" maxFontSizeMultiplier={fontScaleCap.title} style={styles.sheetTitle}>Review comments</Text>
						<Pressable accessibilityRole="button" accessibilityLabel="Close" hitSlop={10} onPress={() => setOpen(false)} style={styles.composerIcon}>
							<Feather name="x" size={iconSize.lg} color={t.textSecondary} />
						</Pressable>
					</View>
					<Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.sheetLead}>Sent to the agent as one message, with the file and line for each.</Text>
					<FlatList
						data={comments}
						keyExtractor={(comment) => comment.id}
						contentContainerStyle={{ paddingBottom: space.lg }}
						renderItem={({ item }) => (
							<View style={styles.queued}>
								<View style={{ flex: 1 }}>
									<Text maxFontSizeMultiplier={fontScaleCap.chrome} numberOfLines={1} style={styles.queuedLabel}>{item.label}</Text>
									<Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.queuedText}>{item.feedback}</Text>
								</View>
								<Pressable accessibilityRole="button" accessibilityLabel={`Remove comment on ${item.label}`} hitSlop={8} onPress={() => { haptics.tap(); onRemove(item.id); }} style={styles.composerIcon}>
									<Feather name="trash-2" size={iconSize.md} color={t.textTertiary} />
								</Pressable>
							</View>
						)}
					/>
					{error ? <Text accessibilityRole="alert" style={styles.composerError}>{error}</Text> : null}
					<Pressable
						accessibilityRole="button"
						accessibilityLabel={`Send ${count} to the agent`}
						accessibilityState={{ busy: sending, disabled: sending }}
						disabled={sending}
						onPress={() => { haptics.tap(); onSend(); }}
						style={({ pressed }) => [styles.sheetSend, sending && styles.disabled, pressed && { opacity: press.opacity }]}
					>
						{sending ? <ActivityIndicator size="small" color={t.onAccent} /> : <Feather name="send" size={iconSize.md} color={t.onAccent} />}
						<Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.sheetSendText}>{`Send ${count}`}</Text>
					</Pressable>
				</View>
			</Modal>
		</>
	);
}

// ---- Styles ------------------------------------------------------------------------

const makeStyles = (t: Theme) => StyleSheet.create({
	segmented: { flexDirection: "row", padding: space.hair, borderRadius: radius.sm + space.hair, backgroundColor: t.bgElevated, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderSubtle },
	segment: { flex: 1, minHeight: 34, flexDirection: "row", alignItems: "center", justifyContent: "center", gap: space.xs, borderRadius: radius.sm, paddingHorizontal: space.sm },
	segmentOn: { backgroundColor: t.bgElevatedHover, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault },
	segmentLabel: { fontFamily: font.medium, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, color: t.textTertiary },
	segmentLabelOn: { color: t.textPrimary },
	segmentBadge: { fontFamily: font.medium, fontSize: type.caption2.fontSize, color: t.textSecondary, backgroundColor: t.bgSubtle, borderRadius: radius.pill, paddingHorizontal: space.xs, overflow: "hidden" },

	statusGlyph: { width: 20, height: 20, borderRadius: radius.xs, borderWidth: 1, alignItems: "center", justifyContent: "center" },
	statusGlyphText: { fontFamily: t.fontMono, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, fontWeight: "600" },
	counts: { fontFamily: t.fontMono, fontSize: type.caption1.fontSize, color: t.textSecondary },

	fileRow: { minHeight: touchTarget + space.sm, flexDirection: "row", alignItems: "center", gap: space.md, paddingHorizontal: space.lg, paddingVertical: space.sm },
	fileRowText: { flex: 1, minWidth: 0 },
	fileName: { ...type.subheadline, fontFamily: font.medium, color: t.textPrimary },
	fileNameDeleted: { color: t.textSecondary, textDecorationLine: "line-through" },
	fileDir: { ...type.footnote, color: t.textTertiary },
	commentCount: { flexDirection: "row", alignItems: "center", gap: space.hair },
	commentCountText: { fontFamily: font.medium, fontSize: type.caption2.fontSize, color: t.textSecondary },

	line: { flexDirection: "row", alignItems: "flex-start", minHeight: CODE_LINE_HEIGHT, paddingRight: space.lg },
	lineSelected: { backgroundColor: t.accentTint, borderLeftWidth: 3, borderLeftColor: t.accent },
	gutter: { flexDirection: "row", justifyContent: "flex-end", alignItems: "center", paddingRight: space.xs, minHeight: CODE_LINE_HEIGHT },
	gutterText: { fontFamily: t.fontMono, fontSize: CODE_FONT_SIZE - 1.5, lineHeight: CODE_LINE_HEIGHT, color: t.textFaint },
	commentDot: { width: 6, height: 6, borderRadius: 3, backgroundColor: t.accent, marginRight: space.xxs },
	marker: { width: MARKER_WIDTH, textAlign: "center", fontFamily: t.fontMono, fontSize: CODE_FONT_SIZE, lineHeight: CODE_LINE_HEIGHT },
	code: { fontFamily: t.fontMono, fontSize: CODE_FONT_SIZE, lineHeight: CODE_LINE_HEIGHT, color: t.textPrimary },
	codeWrap: { flex: 1, flexShrink: 1 },

	fold: { minHeight: touchTarget - space.sm, flexDirection: "row", alignItems: "center", gap: space.sm, paddingHorizontal: space.md, backgroundColor: t.bgSubtle, borderTopWidth: StyleSheet.hairlineWidth, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: t.borderSubtle },
	foldText: { flex: 1, fontFamily: font.medium, fontSize: type.caption1.fontSize, color: t.textSecondary },
	foldSection: { fontFamily: t.fontMono, color: t.textTertiary },

	composerDock: { position: "absolute", left: 0, right: 0, bottom: 0 },
	composer: { backgroundColor: t.bgSurface, borderTopWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault, paddingHorizontal: space.lg, paddingTop: space.md, gap: space.sm },
	composerHead: { flexDirection: "row", alignItems: "center", gap: space.sm },
	composerLabel: { flex: 1, fontFamily: font.medium, fontSize: type.footnote.fontSize, color: t.textSecondary },
	composerIcon: { minWidth: touchTarget - space.sm, minHeight: touchTarget - space.sm, alignItems: "center", justifyContent: "center" },
	composerInput: { ...type.callout, minHeight: 44, maxHeight: 132, color: t.textPrimary, backgroundColor: t.bgElevated, borderRadius: radius.md, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault, paddingHorizontal: space.md, paddingTop: space.sm, paddingBottom: space.sm, textAlignVertical: "top" },
	composerError: { ...type.footnote, color: t.red },
	composerActions: { flexDirection: "row", alignItems: "center", gap: space.sm },
	composerSecondary: { minHeight: 36, paddingHorizontal: space.md, borderRadius: radius.pill, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderStrong, alignItems: "center", justifyContent: "center" },
	composerSecondaryText: { fontFamily: font.medium, fontSize: type.footnote.fontSize, color: t.textPrimary },
	composerPrimary: { width: 36, height: 36, borderRadius: 18, backgroundColor: t.accent, alignItems: "center", justifyContent: "center" },
	quick: { minHeight: 36, flexDirection: "row", alignItems: "center", gap: space.xxs, paddingHorizontal: space.sm, borderRadius: radius.pill, backgroundColor: t.bgElevated },
	quickText: { fontFamily: font.medium, fontSize: type.caption1.fontSize, color: t.textSecondary },
	disabled: { opacity: 0.45 },

	trayDock: { position: "absolute", left: 0, right: 0, alignItems: "center" },
	trayPill: { minHeight: touchTarget, flexDirection: "row", alignItems: "center", gap: space.sm, paddingHorizontal: space.xl, borderRadius: radius.pill, backgroundColor: t.accent, shadowColor: "#000", shadowOpacity: 0.25, shadowRadius: 12, shadowOffset: { width: 0, height: 4 }, elevation: 6 },
	trayPillText: { fontFamily: font.semibold, fontSize: type.subheadline.fontSize, color: t.onAccent },

	sheet: { flex: 1, backgroundColor: t.bgBase, paddingHorizontal: space.lg, paddingTop: space.xl },
	sheetHead: { flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
	sheetTitle: { ...type.title3, color: t.textPrimary },
	sheetLead: { ...type.footnote, color: t.textTertiary, marginTop: space.xxs, marginBottom: space.md },
	queued: { flexDirection: "row", alignItems: "flex-start", gap: space.sm, paddingVertical: space.md, borderBottomWidth: StyleSheet.hairlineWidth, borderColor: t.borderSubtle },
	queuedLabel: { fontFamily: t.fontMono, fontSize: type.caption1.fontSize, color: t.textTertiary, marginBottom: space.xxs },
	queuedText: { ...type.subheadline, color: t.textPrimary },
	sheetSend: { minHeight: touchTarget + space.xxs, flexDirection: "row", alignItems: "center", justifyContent: "center", gap: space.sm, borderRadius: radius.md, backgroundColor: t.accent },
	sheetSendText: { fontFamily: font.semibold, fontSize: type.body.fontSize, color: t.onAccent },
});
