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
 */
export function SettingsSection({
	title,
	sectionId,
	titleHidden,
	grouped,
	children,
}: {
	title: string;
	sectionId?: string;
	titleHidden?: boolean;
	grouped?: boolean;
	children: ReactNode;
}) {
	const pageTitle = useContext(SettingsPageTitleContext);
	return (
		<section
			className="flex w-full flex-col items-stretch gap-(--size-settings-section-inner-gap)"
			data-testid={sectionId ? "settings-section" : undefined}
			data-section={sectionId}
		>
			{!titleHidden && !repeatsPageTitle(title, pageTitle) && (
				<h2 className="px-3 text-sm font-medium leading-5 text-foreground">{title}</h2>
			)}
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
		</section>
	);
}
