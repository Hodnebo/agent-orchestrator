import { act, fireEvent, render } from "@testing-library/react";
import { useEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PairingQr, QR_FADE_MS, QR_SETTLE_FALLBACK_MS } from "./PairingQr";

// jsdom cannot draw qr-code-styling's SVG, so stand in a code that reports
// itself drawn as soon as it mounts — the moment the real one resolves its
// drawing promise.
vi.mock("./StyledQRCode", () => ({
	PAIRING_QR_VERSION: 17,
	StyledQRCode: ({ value, onReady, ...rest }: { value: string; onReady?: () => void; "data-qr-value"?: string }) => {
		useEffect(() => {
			onReady?.();
		}, [value]);
		return <div data-code={value} data-qr-value={rest["data-qr-value"]} />;
	},
}));

const A = "aomobile://pair#a";
const B = "aomobile://pair#b";

function layerFor(container: HTMLElement, code: string) {
	return container.querySelector(`[data-code="${code}"]`)?.parentElement ?? null;
}

async function flushFrames() {
	await act(async () => {
		vi.advanceTimersByTime(40);
	});
}

beforeEach(() => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "requestAnimationFrame", "cancelAnimationFrame"] });
});

afterEach(() => {
	vi.useRealTimers();
});

describe("PairingQr", () => {
	// A code withheld because the connector is not advertisable must not be
	// scannable: one scanned early carries no tunnel endpoint and would pair
	// here then fail from every other network.
	it("exposes no scannable code before one exists", () => {
		const { container } = render(<PairingQr value={null} size={240} />);
		expect(container.querySelector("[data-qr-value]")).toBeNull();
	});

	it("renders the real code once it arrives", async () => {
		const { container } = render(<PairingQr value={A} size={240} />);
		await flushFrames();
		expect(container.querySelector("[data-qr-value]")).toHaveAttribute("data-qr-value", A);
		expect(layerFor(container, A)).toHaveClass("opacity-100");
	});

	// The glitch this replaces: the old code was unmounted the moment the value
	// changed, leaving a blank card until the new one drew. Now the old code
	// stays fully visible underneath until the new one has faded in over it.
	it("keeps the previous code on screen until the next one has faded in", async () => {
		const { container, rerender } = render(<PairingQr value={A} size={240} />);
		await flushFrames();

		rerender(<PairingQr value={B} size={240} animate />);
		// Both mounted; only the new one is the scannable value.
		expect(layerFor(container, A)).toHaveClass("opacity-100");
		expect(layerFor(container, B)).toHaveClass("opacity-0");
		expect(container.querySelectorAll("[data-qr-value]")).toHaveLength(1);
		expect(container.querySelector("[data-qr-value]")).toHaveAttribute("data-qr-value", B);

		await flushFrames();
		expect(layerFor(container, B)).toHaveClass("opacity-100");
		expect(layerFor(container, A)).toHaveClass("opacity-100");

		// Not on a timer racing the fade: the old code goes only once the new
		// one has finished fading in.
		await act(async () => {
			vi.advanceTimersByTime(QR_FADE_MS);
		});
		expect(layerFor(container, A)).not.toBeNull();

		fireEvent.transitionEnd(layerFor(container, B)!);
		expect(layerFor(container, A)).toBeNull();
		expect(layerFor(container, B)).toHaveClass("opacity-100");
	});

	// Reduced motion and hidden tabs fire no transitionend.
	it("still drops the covered code when no transition runs", async () => {
		const { container, rerender } = render(<PairingQr value={A} size={240} />);
		await flushFrames();
		rerender(<PairingQr value={B} size={240} animate />);
		await flushFrames();

		await act(async () => {
			vi.advanceTimersByTime(QR_SETTLE_FALLBACK_MS);
		});
		expect(layerFor(container, A)).toBeNull();
		expect(layerFor(container, B)).toHaveClass("opacity-100");
	});

	it("fades the code out when it is withdrawn, then drops it", async () => {
		const { container, rerender } = render(<PairingQr value={A} size={240} />);
		await flushFrames();

		rerender(<PairingQr value={null} size={240} animate />);
		expect(container.querySelector("[data-qr-value]")).toBeNull();
		expect(layerFor(container, A)).toHaveClass("opacity-0");

		fireEvent.transitionEnd(layerFor(container, A)!);
		expect(layerFor(container, A)).toBeNull();
	});

	// Opening the page (or coming back to it) is not a change: a code that
	// already exists is on screen in the first frame, with no blur or fade.
	it("shows a code that exists at mount at once, without a transition", () => {
		const { container, unmount } = render(<PairingQr value={A} size={240} />);
		const layer = layerFor(container, A);
		expect(layer).toHaveClass("opacity-100", "transition-none");
		expect(layer).not.toHaveClass("blur-[4px]");
		expect(container.querySelector("[data-qr-value]")).toHaveAttribute("data-qr-value", A);

		unmount();
		const again = render(<PairingQr value={A} size={240} />);
		expect(layerFor(again.container, A)).toHaveClass("opacity-100", "transition-none");
	});

	// A code that arrives after mount without the user asking (the status
	// landing, a poll picking up the tunnel) swaps in without a fade.
	it("swaps a change the user did not trigger without a fade", async () => {
		const { container, rerender } = render(<PairingQr value={null} size={240} />);
		rerender(<PairingQr value={A} size={240} />);
		await flushFrames();
		expect(layerFor(container, A)).toHaveClass("opacity-100", "transition-none");

		rerender(<PairingQr value={B} size={240} />);
		await flushFrames();
		expect(layerFor(container, A)).toBeNull();
		expect(layerFor(container, B)).toHaveClass("opacity-100", "transition-none");
		expect(layerFor(container, B)).not.toHaveClass("blur-[4px]");
	});

	// Generate / Regenerate arm the fade: the new code starts blurred and hidden.
	it("blurs a code in once the user has triggered it", async () => {
		const { container, rerender } = render(<PairingQr value={null} size={240} />);
		rerender(<PairingQr value={A} size={240} animate />);
		expect(layerFor(container, A)).toHaveClass("opacity-0", "blur-[4px]", "transition-[opacity,filter]");
		await flushFrames();
		expect(layerFor(container, A)).toHaveClass("opacity-100", "blur-[0px]", "transition-[opacity,filter]");
	});
});
