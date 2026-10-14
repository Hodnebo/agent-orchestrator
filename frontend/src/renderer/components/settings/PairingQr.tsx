import { memo, useCallback, useEffect, useRef, useState } from "react";
import { cn } from "../../lib/utils";
import { forgetSecretCodes, PAIRING_QR_VERSION, StyledQRCode } from "./StyledQRCode";

/** Crossfade between codes: long enough to read as one code becoming the
 *  next, short enough that a fresh code is scannable at once. Matches the
 *  `duration-200` on each layer below. */
export const QR_FADE_MS = 200;

/** A finished fade is detected by its transitionend. This is only the backstop
 *  for when none fires (reduced motion, a hidden tab), so it sits well past
 *  the fade: dropping the old code early would show the placeholder through
 *  the new one while it is still fading in. */
export const QR_SETTLE_FALLBACK_MS = QR_FADE_MS * 5;

function prefersReducedMotion(): boolean {
	return typeof window !== "undefined" && window.matchMedia?.("(prefers-reduced-motion: reduce)").matches === true;
}

interface Layer {
	value: string;
	key: number;
	shown: boolean;
	/** Swapped in without a fade: it is revealed the moment it is drawn and
	 *  everything beneath it is dropped at once. */
	instant: boolean;
}

/**
 * The pairing code, crossfaded whenever it changes.
 *
 * Each code is its own opaque layer. A new code mounts hidden on top of the
 * current one and is revealed only once qr-code-styling has finished drawing
 * it, so the old code stays on screen underneath until the new one fully
 * covers it: no blank card between the two, and no half-drawn frame. Layers
 * beneath the newest shown one are dropped once its fade has finished
 * (transitionend), never on a timer racing the fade.
 *
 * `value` null fades every code out, revealing whatever the caller has placed
 * behind this component (the blurred placeholder).
 *
 * Fading is opt-in through `animate`, which the caller sets only once the user
 * has generated or regenerated a code. A code that already exists when the page
 * opens is on screen in the first frame, with no blur, fade or placeholder, and
 * a change nobody asked for swaps without a fade once the new code is drawn.
 * Reduced motion makes every change such a swap.
 *
 * Fills its parent, which owns the fixed square size, so a code change can
 * never shift the layout.
 */
export const PairingQr = memo(function PairingQr({
	value,
	size,
	animate = false,
	className,
}: {
	/** The real pairing code, or null when there is none to show. */
	value: string | null;
	size: number;
	/** Crossfade code changes from now on. Off at mount, so opening the page
	 *  never plays a transition. */
	animate?: boolean;
	className?: string;
}) {
	// A code present at mount is shown at once: it is not a change.
	const [layers, setLayers] = useState<Layer[]>(() =>
		value ? [{ value, key: 0, shown: true, instant: true }] : [],
	);
	const nextKey = useRef(0);
	const animateRef = useRef(animate);
	animateRef.current = animate;
	const valueRef = useRef(value);
	const hadValue = useRef(value !== null);

	useEffect(() => {
		valueRef.current = value;
		// The code was revoked (turned off, or dropped by the status poll): it
		// must not be repainted from the redraw cache later.
		if (!value && hadValue.current) forgetSecretCodes();
		hadValue.current = value !== null;
		const instant = !animateRef.current || prefersReducedMotion();
		setLayers((prev) => {
			if (!value) {
				if (instant) return prev.length > 0 ? [] : prev;
				// Fade out what is on screen; a code still drawing never shows.
				return prev.length > 0 ? prev.filter((l) => l.shown).map((l) => ({ ...l, shown: false })) : prev;
			}
			const top = prev[prev.length - 1];
			if (top?.value === value) return top.shown ? prev : prev.map((l) => (l === top ? { ...l, shown: true } : l));
			nextKey.current += 1;
			return [...prev, { value, key: nextKey.current, shown: false, instant }];
		});
	}, [value]);

	// Two frames, so the hidden state is painted before the transition starts;
	// flipping it in the frame the layer mounted in would skip the fade. An
	// instant layer replaces everything beneath it as soon as it is drawn. A
	// layer whose code stopped being current while it drew is never revealed.
	const reveal = useCallback((key: number, instant: boolean) => {
		if (instant) {
			setLayers((prev) => {
				const index = prev.findIndex((l) => l.key === key);
				if (index < 0 || prev[index].value !== valueRef.current || (index === 0 && prev[0].shown)) return prev;
				return [{ ...prev[index], shown: true }, ...prev.slice(index + 1)];
			});
			return;
		}
		requestAnimationFrame(() =>
			requestAnimationFrame(() =>
				setLayers((prev) =>
					prev.map((l) => (l.key === key && l.value === valueRef.current ? { ...l, shown: true } : l)),
				),
			),
		);
	}, []);

	// A shown layer has finished fading in: everything beneath it is covered.
	// A hidden one has finished fading out: it can go.
	const settle = useCallback((key: number) => {
		setLayers((prev) => {
			const index = prev.findIndex((l) => l.key === key);
			if (index < 0) return prev;
			if (prev[index].shown) return index > 0 ? prev.slice(index) : prev;
			return prev.filter((l) => l.key !== key || l.value === value);
		});
	}, [value]);

	useEffect(() => {
		let lastShown = -1;
		layers.forEach((l, i) => {
			if (l.shown) lastShown = i;
		});
		const pending = lastShown > 0 || (!value && layers.length > 0 && lastShown < 0);
		if (!pending) return undefined;
		const timer = setTimeout(
			() => (lastShown >= 0 ? settle(layers[lastShown].key) : setLayers((prev) => prev.filter((l) => l.shown))),
			prefersReducedMotion() ? 0 : QR_SETTLE_FALLBACK_MS,
		);
		return () => clearTimeout(timer);
	}, [layers, value, settle]);

	return (
		<div className={cn("absolute inset-0", className)}>
			{layers.map((layer) => (
				<div
					key={layer.key}
					aria-hidden={layer.value !== value}
					onTransitionEnd={(event) => {
						if (event.target === event.currentTarget) settle(layer.key);
					}}
					className={cn(
						// Opaque, so a layer that has finished fading in hides the one
						// beneath it completely.
						"absolute inset-0 bg-white",
						layer.instant
							? "transition-none"
							: "transition-[opacity,filter] duration-200 motion-reduce:transition-none",
						layer.shown ? "opacity-100 blur-[0px] ease-out" : layer.instant ? "opacity-0" : "opacity-0 blur-[4px] ease-in",
					)}
				>
					<StyledQRCode
						value={layer.value}
						data-qr-value={layer.value === value ? layer.value : undefined}
						size={size}
						version={PAIRING_QR_VERSION}
						secret
						onReady={() => reveal(layer.key, layer.instant)}
						className="block size-full [&_svg]:size-full"
					/>
				</div>
			))}
		</div>
	);
});
