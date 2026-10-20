// Shared chrome for the Files review's Changes and Branch pickers.

/**
 * Borderless until hovered or open, so the two pickers read as one quiet line
 * above the review rather than two filled buttons. Passed to
 * SettingsMenuTrigger, whose fill this replaces.
 */
export const REVIEW_MENU_TRIGGER = "h-control-sm gap-1 bg-transparent px-1.5 text-xs";

/** Denser than the app's action-menu rows: these lists of commits and branches run long. */
export const REVIEW_MENU_ROW = "gap-2 py-1 text-xs [&_svg]:size-icon-md";

/** How many rows a list's box shows; the rest scroll. A list that fits needs no search. */
export const REVIEW_MENU_VISIBLE_ROWS = 7;

/** Rows a list loads at first and adds with each Show more, which sits at the end of the scroll. */
export const REVIEW_MENU_PAGE = 50;

/**
 * A list's scroll box. The gutter is kept even before the list scrolls, so
 * Show more never shifts the rows' trailing marks sideways.
 */
export const REVIEW_MENU_LIST = "board-scrollbar flex flex-col gap-px overflow-y-auto pr-0.5 [scrollbar-gutter:stable]";

/** A row above a list, inset by the list's 4px scrollbar gutter and 2px padding so its trailing mark lines up with the rows'. */
export const REVIEW_MENU_LIST_ALIGN = "mr-1.5";

/** The Show more row at the end of a list, the same in both pickers. */
export const REVIEW_MENU_MORE = "gap-2 py-1 text-caption";
