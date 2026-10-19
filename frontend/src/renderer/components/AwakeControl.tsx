import { useId } from "react";
import { Coffee, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useStayAwake } from "../hooks/useStayAwake";
import { isMacPlatform } from "../lib/platform";
import { cn } from "../lib/utils";
import { NAV_ROW_HIGHLIGHT_HOST_CLASS, NavRowHighlight } from "./NavRowHighlight";
import { Button } from "./ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type AwakeControlProps = { className?: string; tabIndex?: number };

/** The coffee fill reflects a wake hold; inactive requests remain visible and clearable. */
export function AwakeControl(props: AwakeControlProps) {
	if (!isMacPlatform()) return null;
	return <MacAwakeControl {...props} />;
}

function MacAwakeControl({ className, tabIndex }: AwakeControlProps) {
	const { t } = useTranslation();
	const stay = useStayAwake(true);
	const title = t("awake.title", "Stay Awake");
	const descriptionId = useId();
	const reason = stay.reason
		? t(`mobile.keepAwake.lidReason.${stay.reason}`, stay.reason.replaceAll("_", " "))
		: "";
	const description = stay.error ?? (stay.starting
		? t("awake.startingTooltip", "Starting Stay Awake…")
		: stay.active
			? t("awake.activeTooltip", "This Mac is being kept awake. Click to turn off.")
			: stay.on
				? reason
					? t("awake.inactiveReasonTooltip", "Stay Awake is configured but inactive. {{reason}} Click to turn off.", { reason })
					: t("awake.inactiveTooltip", "Stay Awake is configured, but this Mac is not being kept awake. Click to turn off.")
				: t("awake.offTooltip", "Stay Awake is off. Click to keep this Mac awake."));
	return (
		<>
			<Tooltip>
				<TooltipTrigger asChild>
					<Button
						variant="ghost"
						size="icon-sm"
						type="button"
						tabIndex={tabIndex}
						aria-label={title}
						aria-describedby={descriptionId}
						aria-pressed={stay.on}
						aria-busy={stay.starting || undefined}
						disabled={!stay.supported || stay.busy}
						data-testid="awake-button"
						onClick={() => void stay.set(!stay.on)}
						className={cn(
							NAV_ROW_HIGHLIGHT_HOST_CLASS,
							"size-9 rounded-lg border-0 focus-visible:ring-2 focus-visible:ring-ring",
							// The shared navigation highlight owns both the selected fill and hover pill.
							stay.error ? "text-error" : stay.attention ? "text-status-needs-you"
								: stay.active ? "text-foreground" : "text-muted-foreground",
							className,
						)}
					>
						<NavRowHighlight active={stay.active} disabled={!stay.supported || stay.busy} />
						<span className="relative z-[1] grid place-items-center">
							{stay.attention
								? <TriangleAlert data-testid="awake-inactive-indicator" className="size-icon-md" aria-hidden="true" />
								: <Coffee className="size-icon-md" aria-hidden="true" />}
						</span>
					</Button>
				</TooltipTrigger>
				<TooltipContent className="max-w-72">{description}</TooltipContent>
			</Tooltip>
			<span id={descriptionId} className="sr-only">{description}</span>
			{stay.error ? (
				<span className="sr-only" role="alert">
					{stay.error}
				</span>
			) : null}
		</>
	);
}
