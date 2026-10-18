/**
 * A dotted ring that breathes: the live turn's thinking mark.
 *
 * Soft dots (sixteen at 20px, more as the ring grows) sit on a ring whose radius carries two slow waves — a
 * three-lobed one travelling one way and a fainter five-lobed one the other — so
 * the ring wobbles out of round and back rather than spinning like a loader.
 * Three near-coincident lanes, a little out of phase and fainter at the edges,
 * soften each dot into a small cluster. Drawn on a 2D canvas at device resolution;
 * the ink is the element's own `color`, so it follows the theme. It pauses when
 * scrolled offscreen or in a hidden tab, and under reduced motion it draws one
 * still frame.
 */

import { useEffect, useRef } from "react";

/** Dots round the ring at 20px; larger rings get more so the spacing stays even. */
const DOTS_AT_20 = 16;
const LANES = 3;
/** Time runs at this rate: the three-lobed wave makes a full turn every ~3s. */
const SPEED = 3.78;

export function BreathingOrb({ size = 20, className }: { size?: number; className?: string }) {
	const ref = useRef<HTMLCanvasElement>(null);

	useEffect(() => {
		const canvas = ref.current;
		if (!canvas) return;
		const ctx = canvas.getContext("2d");
		// A stub context (jsdom) has no drawing API: there is nothing to show there.
		if (!ctx || typeof ctx.arc !== "function") return;
		const dpr = Math.min(2, window.devicePixelRatio || 1);
		canvas.width = Math.round(size * dpr);
		canvas.height = Math.round(size * dpr);
		ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

		const reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
		const centre = size / 2;
		// The resting radius leaves room for the wave's crests inside the box.
		const radius = ((size / 2) * 0.78) / 1.11;
		const scale = size / 20;
		const dots = Math.round(DOTS_AT_20 * Math.sqrt(scale));
		const started = performance.now();
		let ink = "";
		let frames = 0;
		let raf = 0;
		let onscreen = true;

		const draw = (now: number) => {
			raf = 0;
			// Read the ink twice a second rather than every frame, so a theme switch
			// lands without a style recalculation per frame.
			if (frames++ % 30 === 0) ink = getComputedStyle(canvas).color;
			const t = ((now - started) / 1000) * SPEED;
			ctx.clearRect(0, 0, size, size);
			ctx.fillStyle = ink;
			for (let lane = 0; lane < LANES; lane++) {
				const edge = Math.abs(lane - (LANES - 1) / 2) / ((LANES - 1) / 2);
				ctx.globalAlpha = 0.7 - 0.2 * edge;
				const dot = Math.max(0.35, 0.62 * (1 - 0.25 * edge) * scale);
				for (let i = 0; i < dots; i++) {
					const a = (i / dots) * Math.PI * 2;
					const wobble = 0.09 * Math.sin(3 * a - 1.7 * t + 0.22 * lane) + 0.04 * Math.sin(5 * a + 1.1 * t);
					const r = radius * (1 + wobble) * (1 - 0.015 * edge);
					ctx.beginPath();
					ctx.arc(centre + Math.cos(a) * r, centre + Math.sin(a) * r, dot, 0, Math.PI * 2);
					ctx.fill();
				}
			}
			ctx.globalAlpha = 1;
			if (!reduce && onscreen && !document.hidden) raf = requestAnimationFrame(draw);
		};
		const resume = () => {
			if (!raf && onscreen && !document.hidden) raf = requestAnimationFrame(draw);
		};
		const observer =
			typeof IntersectionObserver === "undefined"
				? undefined
				: new IntersectionObserver(([entry]) => {
						onscreen = entry?.isIntersecting ?? true;
						resume();
					});
		observer?.observe(canvas);
		document.addEventListener("visibilitychange", resume);
		raf = requestAnimationFrame(draw);
		return () => {
			if (raf) cancelAnimationFrame(raf);
			observer?.disconnect();
			document.removeEventListener("visibilitychange", resume);
		};
	}, [size]);

	return <canvas ref={ref} aria-hidden="true" className={className} style={{ width: size, height: size }} />;
}
