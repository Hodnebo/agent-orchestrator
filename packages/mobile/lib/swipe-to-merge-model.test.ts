import { expect, it } from "vitest";
import { boundSwipeToMergeTranslation, shouldCommitSwipeToMerge, swipeToMergeTravel } from "./swipe-to-merge-model";

it("measures knob travel inside the track's inset", () => {
	expect(swipeToMergeTravel(300, 48, 4)).toBe(244);
	expect(swipeToMergeTravel(40, 48, 4)).toBe(0);
});

it("keeps the knob inside the track", () => {
	expect(boundSwipeToMergeTranslation(-30, 244)).toBe(0);
	expect(boundSwipeToMergeTranslation(120, 244)).toBe(120);
	expect(boundSwipeToMergeTranslation(400, 244)).toBe(244);
	expect(boundSwipeToMergeTranslation(50, 0)).toBe(0);
});

it("commits only once the knob is carried nearly to the end", () => {
	expect(shouldCommitSwipeToMerge(200, 244)).toBe(false);
	expect(shouldCommitSwipeToMerge(208, 244)).toBe(true);
	expect(shouldCommitSwipeToMerge(244, 244)).toBe(true);
	expect(shouldCommitSwipeToMerge(0, 0)).toBe(false);
});
