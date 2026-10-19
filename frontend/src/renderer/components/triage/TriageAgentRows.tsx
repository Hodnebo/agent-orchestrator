import { CircleHelp } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { AgentListRow } from "../../lib/triage";
import { isTriageSessionRun, triageHref } from "../../lib/triage";
import { SidebarMenuSub, SidebarMenuSubItem } from "../ui/sidebar";
import { useTriageStateLabel } from "./TriageConversation";

export function TriageAgentRows({ rows }: { rows: AgentListRow[] }) {
	const triageRows = rows.filter((row): row is Extract<AgentListRow, { kind: "triage" }> => row.kind === "triage" && !row.run.preparation && !isTriageSessionRun(row.run));
	if (!triageRows.length) return null;
	return <SidebarMenuSub className="mx-0 ml-0 translate-x-0 gap-0.5 border-l-0 px-0 py-0.5" data-testid="triage-agent-rows">
		{triageRows.map((row) => <TriageAgentRow key={row.id} row={row} />)}
	</SidebarMenuSub>;
}

function TriageAgentRow({ row }: { row: Extract<AgentListRow, { kind: "triage" }> }) {
	const { t } = useTranslation();
	const label = useTriageStateLabel(row.run);
	return <SidebarMenuSubItem data-agent-kind="triage">
		<a href={triageHref(row.run.id)} className="flex min-h-8 items-center gap-2 rounded-md px-2.5 text-xs hover:bg-sidebar-accent">
			<CircleHelp className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
			<span className="min-w-0 flex-1 truncate">{t("triage.title")}</span>
			<span className="truncate text-caption text-muted-foreground">{label}</span>
		</a>
	</SidebarMenuSubItem>;
}
