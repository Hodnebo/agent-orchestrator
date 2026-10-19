// Private support-host protocol. Keep these fields aligned with
// backend/internal/triage/protocol.go; this is not a daemon API.
export const TRIAGE_CHANNELS = {
	catalog: "triage:catalog",
	configure: "triage:configure",
	getPreference: "triage:getPreference",
	setPreference: "triage:setPreference",
	list: "triage:list",
	get: "triage:get",
	prepare: "triage:prepare",
	start: "triage:start",
	reply: "triage:reply",
	cancel: "triage:cancel",
	publish: "triage:publish",
	signInGitHub: "triage:github-sign-in",
	nativeInput: "triage:native-input",
	run: "triage:run",
} as const;

export const TRIAGE_APPROVAL_REQUIRED = "Unreal Agent requires a permission choice for a normal session. Choose Start without approvals to continue, or select another configured agent.";

export type TriageChoice = {
	harness: string;
	label: string;
	models: Array<{ id: string; label: string; efforts?: string[]; default?: boolean }>;
	modelsMissing?: boolean;
	accounts?: Array<{ id: string; label: string; default?: boolean }>;
	mode?: string;
	permissions?: string;
	available: boolean;
	unavailableReason?: string;
	source?: string;
	updatedAt: string;
};

export type TriageSelection = {
	harness: string;
	model?: string;
	effort?: string;
	accountId?: string;
	mode?: string;
	permissions?: string;
};

export type TriageOrigin = {
	appVersion?: string;
	buildMode?: string;
	hostId?: string;
	surface?: string;
	sessionId?: string;
	daemonState?: string;
	daemonCode?: string;
	daemonOutput?: string;
	machine?: Record<string, unknown>;
	session?: Record<string, unknown>;
};

export type TriageStartRequest = TriageSelection & {
	preparationId?: string;
	observation?: string;
	origin?: TriageOrigin;
	publishAuthorized: boolean;
};

export type TriageRun = {
	id: string;
	version: number;
	createdAt: string;
	updatedAt: string;
	request: TriageStartRequest;
	state: string;
	preparation?: boolean;
	execution?: "session";
	sessionId?: string;
	sessionLaunchStarted?: boolean;
	searchAuthRequired?: boolean;
	nativeControlsAvailable?: boolean;
	activity: string;
	messages: Array<{ id: string; role: string; text: string; at: string }>;
	evidence?: Array<{
		id: string;
		kind: string;
		source: string;
		collectedAt: string;
		status: string;
		text?: string;
		values?: Record<string, unknown>;
	}>;
	pendingInput?: {
		id: string;
		origin?: "native" | "triage";
		questions: Array<{ id: string; prompt: string; options?: string[]; multiple?: boolean; allowText?: boolean }>;
	};
	draft?: { title: string; body: string; evidence?: string };
	publication: {
		state: string;
		repository?: string;
		marker?: string;
		identity?: string;
		issueUrl?: string;
		evidenceUrl?: string;
		title?: string;
		body?: string;
		evidence?: string;
		error?: string;
	};
	providerConversationId?: string;
	providerTurnId?: string;
	error?: string;
};

export type TriageBridge = {
	catalog(): Promise<TriageChoice[]>;
	configure(choices: TriageChoice[]): Promise<void>;
	getPreference(): Promise<TriageSelection | null>;
	setPreference(selection: TriageSelection): Promise<void>;
	list(): Promise<TriageRun[]>;
	get(runId: string): Promise<TriageRun>;
	prepare(origin: TriageOrigin): Promise<TriageRun>;
	start(request: TriageStartRequest): Promise<TriageRun>;
	reply(runId: string, text: string, requestId?: string, answers?: Record<string, string[]>): Promise<TriageRun>;
	cancel(runId: string): Promise<TriageRun>;
	publish(runId: string): Promise<TriageRun>;
	signInGitHub(runId: string): Promise<TriageRun>;
	nativeInput(runId: string): Promise<TriageRun>;
	subscribe(listener: (run: TriageRun) => void): () => void;
};
