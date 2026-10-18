import { Check, Coffee } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "../lib/utils";
import { awakeCapability, useAwake } from "../hooks/useAwake";
import { isMacPlatform } from "../lib/platform";
import { NAV_ROW_HIGHLIGHT_HOST_CLASS, NavRowHighlight } from "./NavRowHighlight";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "./ui/dropdown-menu";

const DURATIONS = [
	{ key: "30m", seconds: 30 * 60, label: "30 minutes" },
	{ key: "1h", seconds: 60 * 60, label: "1 hour" },
	{ key: "2h", seconds: 2 * 60 * 60, label: "2 hours" },
	{ key: "4h", seconds: 4 * 60 * 60, label: "4 hours" },
	{ key: "8h", seconds: 8 * 60 * 60, label: "8 hours" },
] as const;

/** Square coffee button for the sidebar settings row: pick how long this Mac stays awake. */
export function AwakeControl({ className }: { className?: string }) {
	if (!isMacPlatform()) return null;
	return <MacAwakeControl className={className} />;
}

function MacAwakeControl({ className }: { className?: string }) {
	const { t } = useTranslation();
	const awake = useAwake(true);
	const active = awake.state?.sessions?.filter((s) => !s.endedAt && s.scope === "ao") ?? [];
	// Show the choice at once; the daemon round trip only confirms it.
	const [intent, setIntent] = useState<boolean | null>(null);
	const checked = intent ?? active.length > 0;
	const waiting = checked && !awake.state?.active;
	const supported = awakeCapability(awake.state, "idle")?.available ?? false;
	const error = awake.error instanceof Error ? awake.error.message : null;
	const indefinite = active.some((s) => s.mode === "indefinite");
	const run = async (next: boolean, request?: { mode: "indefinite" } | { mode: "duration"; durationSeconds: number }) => {
		setIntent(next);
		try {
			// End the running session first so the new length replaces it instead of stacking.
			for (const source of active) await awake.actAsync({ kind: "end", id: source.id });
			if (request)
				await awake.actAsync({
					kind: "start",
					request: { scope: "ao", sessionId: "", lidClosed: awake.state?.settings.lidClosed ?? true, ...request },
				});
		} catch {
			// surfaced through awake.error
		} finally {
			setIntent(null);
		}
	};
	const title = t("awake.title", "Stay Awake");
	return (
		<DropdownMenu>
			<DropdownMenuTrigger asChild>
				<button
					type="button"
					aria-label={title}
					aria-pressed={checked}
					aria-busy={waiting || undefined}
					disabled={!supported}
					data-testid="awake-button"
					className={cn(
						"relative grid size-9 shrink-0 place-items-center rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50 [&_svg]:size-icon-md",
						// On: filled with the selected-row fill, so it reads as engaged without glaring. Off: the Settings row's hover pill.
						checked
							? "bg-interactive-active text-foreground"
							: cn(NAV_ROW_HIGHLIGHT_HOST_CLASS, error ? "text-error" : "text-muted-foreground hover:text-foreground"),
						className,
					)}
				>
					<NavRowHighlight disabled={!supported || checked} />
					<span className="relative z-[1] grid place-items-center">
						<Coffee aria-hidden="true" />
					</span>
				</button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end" side="top">
				<DropdownMenuItem onSelect={() => void run(true, { mode: "indefinite" })}>
					{t("awake.indefinitely", "Indefinitely")}
					{indefinite ? <Check aria-hidden="true" className="ml-auto" /> : null}
				</DropdownMenuItem>
				{DURATIONS.map((d) => (
					<DropdownMenuItem
						key={d.key}
						onSelect={() => void run(true, { mode: "duration", durationSeconds: d.seconds })}
					>
						{t(`awake.duration.${d.key}`, d.label)}
					</DropdownMenuItem>
				))}
				{checked ? (
					<>
						<DropdownMenuSeparator />
						<DropdownMenuItem
								onSelect={() => void run(false)}
						>
							{t("awake.turnOff", "Turn off")}
						</DropdownMenuItem>
					</>
				) : null}
			</DropdownMenuContent>
			{error ? (
				<span className="sr-only" role="alert">
					{error}
				</span>
			) : null}
		</DropdownMenu>
	);
}
