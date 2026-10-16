import { expect, test } from "@playwright/test";

test("@P0 artifacts decode images in the viewer", async ({ page }) => {
	await page.goto("/e2e/media-preview/harness.html?type=image");
	const image = page.getByRole("img", { name: "logo.png" });
	await expect(image).toBeVisible();
	await expect.poll(() => image.evaluate((element: HTMLImageElement) => element.naturalWidth)).toBeGreaterThan(0);
});

for (const type of ["video", "audio"] as const) {
	test(`@P0 artifacts play and seek ${type} with native controls`, async ({ page }) => {
		await page.goto(`/e2e/media-preview/harness.html?type=${type}`);
		const media = page.locator(type);
		await expect(media).toHaveAttribute("controls");
		await expect.poll(() => media.evaluate((element: HTMLMediaElement) => element.duration)).toBeGreaterThan(0);
		await expect(media).toHaveJSProperty("paused", true);
		await media.evaluate((element: HTMLMediaElement) => {
			element.muted = true;
			return element.play();
		});
		await expect.poll(() => media.evaluate((element: HTMLMediaElement) => element.currentTime)).toBeGreaterThan(0);
		await media.evaluate((element: HTMLMediaElement) => {
			element.pause();
			element.currentTime = element.duration / 2;
		});
		await expect.poll(() => media.evaluate((element: HTMLMediaElement) => Math.abs(element.currentTime - element.duration / 2))).toBeLessThan(0.1);
	});
}
