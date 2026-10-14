import { memo, useLayoutEffect, useRef } from "react";
import QRCodeStyling, { type Options, type TypeNumber } from "qr-code-styling";
import aoLogo from "../../assets/ao-logo.svg";

/**
 * Rounded-dot QR (qr-code-styling) with the AO logo in the middle, always drawn
 * as dark modules on a light card.
 *
 * The polarity is a correctness constraint, not a style choice. This used to
 * take its colour from `--color-text-settings-title` over a transparent
 * background, which in the dark theme produced light modules on dark — an
 * inverted code. Apple's AVFoundation and Google Lens both decode inverted
 * codes; the bundled ML Kit scanner that expo-camera uses on Android does not.
 * So the pairing QR scanned everywhere *except* our own Android app, which is
 * exactly the shape the bug reports took.
 *
 * Confirmed by a single-variable scan sweep on a device that reproduced it:
 * with module size, logo size and finder shape each held constant, every
 * inverted variant failed and every dark-on-light variant passed — including
 * one that kept the full-size logo and the round finders. Polarity was the
 * only factor that predicted the outcome.
 *
 * Keep the background opaque. A transparent background inherits whatever sits
 * behind it, which is how the inversion got in.
 */
const INK = "#111113";
const PAPER = "#ffffff";

/**
 * Quiet zone the spec requires, in modules.
 *
 * Measured in modules rather than pixels or a fraction of the rendered size,
 * because that is the unit a scanner reads it in. A short payload produces
 * fewer, larger modules, so a fixed percentage that looks right on the long
 * pairing code silently leaves the small store codes under a third of the
 * clearance they need.
 */
const QUIET_ZONE_MODULES = 3;

/** Margin in px that yields QUIET_ZONE_MODULES around a code of `modules`
 *  modules drawn at `size` px, given the margin eats into that same size:
 *  margin / ((size - 2·margin) / modules) = QUIET_ZONE_MODULES. */
function quietZonePx(size: number, modules: number): number {
	return Math.ceil((QUIET_ZONE_MODULES * size) / (modules + 2 * QUIET_ZONE_MODULES));
}

/**
 * The QR version every pairing code (and its blurred decoy) is drawn at.
 *
 * Left to itself the encoder picks the smallest version that fits, and a
 * pairing offer's length varies with its endpoints and names: LAN only is
 * version 12, LAN plus a tunnel 15, LAN, Tailscale and a tunnel 17. Each step
 * changed the module count, and with it the drawn size, so the code visibly
 * grew or shrank between generations. Pinning the version keeps the density
 * the same; 17 holds LAN, Tailscale and a tunnel with a typical tunnel name.
 * A longer offer still gets the version it needs (a code that cannot hold its
 * payload is worse than a denser one), and keeps the same outer size because
 * the margin is fixed in px and the dots are not snapped to whole pixels.
 */
export const PAIRING_QR_VERSION = 17;

function modulesFor(version: number): number {
	return 4 * version + 17;
}

/** Last drawn markup per code, so a code that was already on screen paints
 *  in the same frame it remounts (reopening the page) instead of after the
 *  logo reloads. Small: the pairing code, its decoy and the two store codes. */
const drawnCodes = new Map<string, string>();
const DRAWN_CODES_MAX = 8;

function rememberDrawn(key: string, markup: string) {
	drawnCodes.delete(key);
	drawnCodes.set(key, markup);
	if (drawnCodes.size > DRAWN_CODES_MAX) drawnCodes.delete(drawnCodes.keys().next().value as string);
}

export const StyledQRCode = memo(function StyledQRCode({
	value,
	size,
	showLogo = true,
	version,
	className,
	onReady,
	"data-qr-value": dataQrValue,
}: {
	value: string;
	size: number;
	showLogo?: boolean;
	/** Pin the QR version (see PAIRING_QR_VERSION) so codes of different
	 *  payload lengths draw at the same density and outer size. Omitted, the
	 *  encoder picks the smallest version and the quiet zone follows it. */
	version?: number;
	className?: string;
	/** Called once the code is fully drawn. qr-code-styling draws the dots
	 *  only after the logo has loaded, so the SVG it appends is empty for a
	 *  frame or more; a caller fading a code in waits for this, not for mount. */
	onReady?: () => void;
	"data-qr-value"?: string;
}) {
	const ref = useRef<HTMLDivElement>(null);
	// Read through a ref so a new callback identity does not redraw the code.
	const onReadyRef = useRef(onReady);
	onReadyRef.current = onReady;

	// Layout effect, so a code drawn before shows in the first painted frame.
	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return undefined;
		const cacheKey = `${size}|${showLogo ? 1 : 0}|${version ?? 0}|${value}`;
		const cached = drawnCodes.get(cacheKey);
		if (cached !== undefined) {
			el.innerHTML = cached;
			onReadyRef.current?.();
			return undefined;
		}
		try {
			const options = (typeNumber: TypeNumber, margin: number): Options => ({
				width: size,
				height: size,
				type: "svg",
				data: value,
				image: showLogo ? aoLogo : undefined,
				margin,
				// Lower error correction = fewer modules = chunkier dots.
				qrOptions: { typeNumber, errorCorrectionLevel: "M" },
				backgroundOptions: { color: PAPER },
				// A pinned version fills its fixed box exactly: whole-pixel dots
				// would leave a leftover that changes with the module count.
				dotsOptions: { color: INK, type: "rounded", roundSize: version === undefined },
				cornersSquareOptions: { color: INK, type: "extra-rounded" },
				cornersDotOptions: { color: INK, type: "dot" },
				imageOptions: { imageSize: 0.45, margin: 8, hideBackgroundDots: true, crossOrigin: "anonymous" },
			});
			let qr: QRCodeStyling;
			if (version !== undefined) {
				// The margin is the pinned version's quiet zone in px, kept even
				// when a longer payload needs a bigger version, so the code's
				// outer box never moves.
				const margin = quietZonePx(size, modulesFor(version));
				try {
					qr = new QRCodeStyling(options(version as TypeNumber, margin));
				} catch {
					// Payload too long for the pinned version.
					qr = new QRCodeStyling(options(0, margin));
				}
			} else {
				// Margin replaced below once the module count is known.
				qr = new QRCodeStyling(options(0, 0));
				// The module count is only known once the payload has been encoded,
				// so the quiet zone is applied as a second pass rather than guessed
				// from the rendered size.
				const modules = qr._qr?.getModuleCount() ?? 0;
				if (modules > 0) qr.update({ margin: quietZonePx(size, modules) });
			}
			el.replaceChildren();
			qr.append(el);
			let cancelled = false;
			// A failed logo load still leaves a scannable code, so reveal it
			// either way rather than leave the caller waiting on it.
			void Promise.resolve(qr._svgDrawingPromise)
				.then(() => rememberDrawn(cacheKey, el.innerHTML))
				.catch(() => undefined)
				.then(() => {
					if (!cancelled) onReadyRef.current?.();
				});
			return () => {
				cancelled = true;
			};
		} catch {
			// jsdom / non-browser environments can't render the SVG; the QR is
			// purely visual, so fail silent rather than crash the settings page.
			return undefined;
		}
	}, [value, size, showLogo, version]);

	return <div ref={ref} className={className} data-qr-value={dataQrValue} />;
});
