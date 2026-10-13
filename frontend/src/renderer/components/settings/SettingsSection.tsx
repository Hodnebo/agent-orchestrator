import { createContext, useContext, type ReactNode } from "react";
import { cn } from "../../lib/utils";

/**
 * Title of the settings page the section renders on (null outside a single
 * page). A section heading that only repeats it ("Harness" on Harness,
 * "Keyboard shortcuts" on Shortcuts) is hidden; any other heading shows.
 */
export const SettingsPageTitleContext = createContext<string | null>(null);

function repeatsPageTitle(title: string, pageTitle: string | null) {
	if (!pageTitle) return false;
	const heading = title.trim().toLocaleLowerCase();
	const page = pageTitle.trim().toLocaleLowerCase();
	return heading === page || heading.split(/\s+/).includes(page);
}

/**
 * Figma section: column, gap 12px between heading and rows.
 * `sectionId` (optional) marks the section for the renderer smoke suite as
 * [data-testid="settings-section"][data-section=<id>].
 * `grouped` puts the children in one card (.settings-grouped-rows): direct
 * children are SettingsRow or `.settings-content-row` blocks, divided by the
 * card's own hairline.
 * `toolbar` sits under the heading and above the card — a SettingsSearch, for
 * instance. Its sticky wrapper's bottom padding is the gap to the card.
 */
export function SettingsSection({
	title,
	sectionId,
	titleHidden,
	grouped,
	toolbar,
	children,
}: {
	title: string;
	sectionId?: string;
	titleHidden?: boolean;
	grouped?: boolean;
	toolbar?: ReactNode;
	children: ReactNode;
}) {
	const pageTitle = useContext(SettingsPageTitleContext);
	const body = (
		<div
			className={cn(
				"w-full",
				grouped
					? "settings-grouped-rows flex w-full flex-col"
					: "flex w-full flex-col gap-1.5",
			)}
		>
			{children}
		</div>
	);
	return (
		<section
			className="flex w-full flex-col items-stretch gap-(--size-settings-section-inner-gap)"
			data-testid={sectionId ? "settings-section" : undefined}
			data-section={sectionId}
		>
			{!titleHidden && !repeatsPageTitle(title, pageTitle) && (
				<h2 className="px-3 text-sm font-medium leading-5 text-foreground">{title}</h2>
			)}
			{toolbar ? (
				<div className="flex w-full flex-col">
					{toolbar}
					{body}
				</div>
			) : body}
		</section>
	);
}
