import { LoaderCircle } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { agentAccountProviderFor, useAgentAccountActions, useAgentAccounts, useSessionAgentAccounts } from "../hooks/useAgentAccounts";
import type { WorkspaceSession } from "../types/workspace";
import { ConfirmDialog } from "./ConfirmDialog";
import { AccountLimits } from "./settings/AgentAccountsSection";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

/** The account the session's agent runs under, its limits, and the move to
 * another account. A move restarts an agent that is between turns in the same
 * conversation; moving a working agent stops its turn after a confirmation. */
export function SessionAccountSummary({ session }: { session: WorkspaceSession }) {
	const { t } = useTranslation();
	const provider = agentAccountProviderFor(session.provider);
	const accounts = useAgentAccounts();
	const current = useSessionAgentAccounts(provider ? session.id : undefined);
	const actions = useAgentAccountActions();
	const [moving, setMoving] = useState(false);
	const [message, setMessage] = useState("");
	const [confirming, setConfirming] = useState<string | null>(null);
	if (!provider || session.cloud || session.isTerminated) return null;
	const options = (accounts.data ?? []).filter((account) => account.provider === provider);
	const currentId = current.data?.[provider];
	const account = options.find((option) => option.id === currentId);
	const working = session.activity?.state === "active" || session.activity?.state === "blocked";
	const name = (email: string | undefined) => email || t("settings.agentAccounts.thisComputer");

	const move = async (accountId: string, stopTurn = false) => {
		if (accountId === currentId) return;
		// A working agent may only be waiting on the old account's limit; moving
		// stops that turn, so ask first.
		if (working && !stopTurn) {
			setConfirming(accountId);
			return;
		}
		setConfirming(null);
		setMoving(true);
		setMessage("");
		try {
			await actions.switchSession(session.id, accountId, stopTurn);
			setMessage(t("settings.agentAccounts.moved", { email: name(options.find((option) => option.id === accountId)?.email) }));
		} catch (err) {
			setMessage(err instanceof Error ? err.message : String(err));
		} finally {
			setMoving(false);
		}
	};

	return <section className="mt-3 border-t border-(--color-border-settings-input) pt-3" data-testid="session-account-summary">
		<div className="flex items-center justify-between gap-3">
			<p className="text-xs text-muted-foreground">{t("settings.agentAccounts.sessionAccount")}</p>
			<div className="flex min-w-0 items-center gap-2">
				{moving ? <LoaderCircle className="size-3.5 animate-spin text-muted-foreground" aria-hidden="true" /> : null}
				<Select value={currentId ?? ""} disabled={moving || options.length < 2} onValueChange={(value) => void move(value)}>
					<SelectTrigger size="sm" className="max-w-56" aria-label={t("settings.agentAccounts.sessionAccount")}><SelectValue /></SelectTrigger>
					<SelectContent align="end">
						{options.map((option) => <SelectItem key={option.id} value={option.id} disabled={!option.signedIn}>{name(option.email)}</SelectItem>)}
					</SelectContent>
				</Select>
			</div>
		</div>
		{message ? <p className="mt-2 text-xs text-muted-foreground" role="status">{message}</p> : null}
		{account ? <AccountLimits account={account} /> : null}
		{confirming ? <ConfirmDialog open
			title={t("settings.agentAccounts.stopAndMoveTitle")}
			description={t("settings.agentAccounts.stopAndMoveDescription", { email: name(options.find((option) => option.id === confirming)?.email) })}
			confirmLabel={t("settings.agentAccounts.stopAndMove")}
			onConfirm={() => void move(confirming, true)}
			onOpenChange={(open) => { if (!open) setConfirming(null); }}
		/> : null}
	</section>;
}
