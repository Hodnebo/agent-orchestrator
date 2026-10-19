import { Coffee } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useStayAwake } from "../hooks/useStayAwake";
import { isMacPlatform } from "../lib/platform";
import { cn } from "../lib/utils";
import { NAV_ROW_HIGHLIGHT_HOST_CLASS, NavRowHighlight } from "./NavRowHighlight";

type AwakeControlProps = { className?: string; tabIndex?: number };

/** Square coffee toggle beside the sidebar Connect mobile row: keeps this Mac awake while on. */
export function AwakeControl(props: AwakeControlProps) {
	if (!isMacPlatform()) return null;
	return <MacAwakeControl {...props} />;
}

function MacAwakeControl({ className, tabIndex }: AwakeControlProps) {
	const { t } = useTranslation();
	const stay = useStayAwake(true);
	const title = t("awake.title", "Stay Awake");
	return (
		<>
			<button
				type="button"
				tabIndex={tabIndex}
				aria-label={title}
				aria-pressed={stay.on}
				aria-busy={stay.starting || undefined}
				disabled={!stay.supported || stay.busy}
				data-testid="awake-button"
				onClick={() => void stay.set(!stay.on)}
				className={cn(
					"relative grid size-9 shrink-0 place-items-center rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50 [&_svg]:size-icon-md",
					// On: filled with the selected-row fill, so it reads as engaged without glaring. Off: the Settings row's hover pill.
					stay.on
						? "bg-interactive-active text-foreground"
						: cn(NAV_ROW_HIGHLIGHT_HOST_CLASS, stay.error ? "text-error" : "text-muted-foreground hover:text-foreground"),
					className,
				)}
			>
				<NavRowHighlight disabled={!stay.supported || stay.busy || stay.on} />
				<span className="relative z-[1] grid place-items-center">
					<Coffee aria-hidden="true" />
				</span>
			</button>
			{stay.error ? (
				<span className="sr-only" role="alert">
					{stay.error}
				</span>
			) : null}
		</>
	);
}
