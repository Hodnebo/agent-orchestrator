import { Coffee } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useStayAwake } from "../hooks/useStayAwake";
import { isMacPlatform } from "../lib/platform";
import { cn } from "../lib/utils";
import { NAV_ROW_HIGHLIGHT_HOST_CLASS, NavRowHighlight } from "./NavRowHighlight";
import { Button } from "./ui/button";

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
			<Button
				variant="ghost"
				size="icon-sm"
				type="button"
				tabIndex={tabIndex}
				aria-label={title}
				aria-pressed={stay.on}
				aria-busy={stay.starting || undefined}
				disabled={!stay.supported || stay.busy}
				data-testid="awake-button"
				onClick={() => void stay.set(!stay.on)}
				className={cn(
					NAV_ROW_HIGHLIGHT_HOST_CLASS,
					"size-9 rounded-lg border-0 focus-visible:ring-2 focus-visible:ring-ring",
					// The shared navigation highlight owns both the selected fill and hover pill.
					stay.on
						? "text-foreground"
						: stay.error ? "text-error" : "text-muted-foreground",
					className,
				)}
			>
				<NavRowHighlight active={stay.on} disabled={!stay.supported || stay.busy} />
				<span className="relative z-[1] grid place-items-center">
					<Coffee className="size-icon-md" aria-hidden="true" />
				</span>
			</Button>
			{stay.error ? (
				<span className="sr-only" role="alert">
					{stay.error}
				</span>
			) : null}
		</>
	);
}
