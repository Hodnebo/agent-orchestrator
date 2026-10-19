import type { TriageOrigin } from "../../../shared/triage";
import { TriageLauncher } from "./TriageLauncher";

export function TriageReportButton({ origin, observation, className }: { origin?: TriageOrigin; observation?: string; className?: string }) {
	return <div className={className}><TriageLauncher origin={origin} observation={observation} /></div>;
}
