// A cloud project's repositories and layout, read from its config the way the
// control plane does (cloud/internal/domain/project_repos.go):
// - the main repository is the project's repositoryUrl;
// - additional repositories are config.repos, or the legacy coder dev kit's
//   config.coder.extraRepos when config.repos is absent;
// - every session clones them side by side, each additional one into the
//   folder repoDirName gives it beside the main checkout;
// - a standalone project has no repository (a scratch.ao.local placeholder).

import type { CloudCpAgentStartsIn, CloudCpProject, CloudCpProjectRepo, CloudCpProjectScriptStep } from "./cloud-cp/types";

export const STANDALONE_REPOSITORY_HOST = "scratch.ao.local";
export const STANDALONE_PROJECT_SOURCE = "standalone";
export const MAX_PROJECT_ADDITIONAL_REPOS = 10;
export const MAX_PROJECT_RULES_BYTES = 16 * 1024;

/** Folders beside the main checkout that AO itself uses. */
const RESERVED_REPO_DIR_NAMES = ["repository", ".ao"];

type ConfigLike = Record<string, unknown> | null | undefined;

/** True for a project created without a repository. */
export function isStandaloneCloudProject(project: Pick<CloudCpProject, "repositoryUrl" | "config"> | undefined): boolean {
	if (!project) return false;
	if ((project.config as ConfigLike)?.source === STANDALONE_PROJECT_SOURCE) return true;
	try {
		return new URL(project.repositoryUrl).hostname.toLowerCase() === STANDALONE_REPOSITORY_HOST;
	} catch {
		return false;
	}
}

/**
 * The folder a repository checks out into beside the main repository:
 * "https://github.com/owner/ChartSnip" gives "ChartSnip". Mirrors the control
 * plane's domain.RepoDirName.
 */
export function repoDirName(repoUrl: string): string {
	let trimmed = repoUrl.trim().replace(/^\/+|\/+$/g, "");
	if (trimmed.endsWith(".git")) trimmed = trimmed.slice(0, -4);
	const name = (trimmed.split("/").pop() ?? "").replace(/[^a-zA-Z0-9._-]/g, "-");
	return name === "" || name === "." || name === ".." ? "repo" : name;
}

/** owner/name of a GitHub repository URL, or undefined when it is not one. */
export function gitHubFullName(repoUrl: string): string | undefined {
	try {
		const url = new URL(repoUrl.trim());
		if (url.protocol !== "https:" || !/^(www\.)?github\.com$/i.test(url.hostname)) return undefined;
		const parts = url.pathname.replace(/^\/+|\/+$/g, "").split("/");
		if (parts.length !== 2 || !parts[0] || !parts[1]) return undefined;
		return `${parts[0]}/${parts[1].replace(/\.git$/, "")}`;
	} catch {
		return undefined;
	}
}

/** https://github.com/<owner>/<name>, the form the control plane stores. */
export function canonicalGitHubUrl(fullName: string): string {
	return `https://github.com/${fullName}`;
}

/** Whether two repository URLs name the same GitHub repository. */
export function sameRepository(left: string, right: string): boolean {
	const a = gitHubFullName(left) ?? left.trim();
	const b = gitHubFullName(right) ?? right.trim();
	return a.toLowerCase() === b.toLowerCase();
}

function nonEmptyRepos(value: unknown): CloudCpProjectRepo[] {
	if (!Array.isArray(value)) return [];
	return value.flatMap((item) => {
		if (!item || typeof item !== "object") return [];
		const { url, branch } = item as { url?: unknown; branch?: unknown };
		if (typeof url !== "string" || url.trim() === "") return [];
		return [{ url: url.trim(), ...(typeof branch === "string" && branch.trim() !== "" ? { branch: branch.trim() } : {}) }];
	});
}

/** The project's additional repositories (config.repos, else the legacy config.coder.extraRepos). */
export function projectAdditionalRepos(config: ConfigLike): CloudCpProjectRepo[] {
	if (config && Array.isArray(config.repos)) return nonEmptyRepos(config.repos);
	const coder = config?.coder;
	if (coder && typeof coder === "object") return nonEmptyRepos((coder as { extraRepos?: unknown }).extraRepos);
	return [];
}

/** Where the project's agent starts; "repository" (the main checkout) unless set to "parent". */
export function projectAgentStartsIn(config: ConfigLike): CloudCpAgentStartsIn {
	return config?.agentStartsIn === "parent" ? "parent" : "repository";
}

/** A string config value, trimmed; "" when absent. */
export function projectConfigText(config: ConfigLike, key: "workingDirectory" | "agentRules" | "orchestratorRules"): string {
	const value = config?.[key];
	return typeof value === "string" ? value : "";
}

/**
 * Checks a set of additional repositories against the main one the way the
 * control plane's NormalizeProjectRepos does: no duplicates, not the main
 * repository, and each in its own folder. Returns the first problem, if any.
 */
export function additionalReposProblem(mainUrl: string | undefined, urls: readonly string[]): { kind: "duplicate" | "main" | "folder" | "reserved" | "limit"; url: string; folder?: string } | undefined {
	const filled = urls.filter((url) => url.trim() !== "");
	if (filled.length > MAX_PROJECT_ADDITIONAL_REPOS) return { kind: "limit", url: filled[MAX_PROJECT_ADDITIONAL_REPOS] ?? "" };
	const folders = new Map<string, string>();
	for (const reserved of RESERVED_REPO_DIR_NAMES) folders.set(reserved, "");
	if (mainUrl) folders.set(repoDirName(mainUrl).toLowerCase(), mainUrl);
	const seen = new Set<string>();
	for (const url of filled) {
		const key = (gitHubFullName(url) ?? url).toLowerCase();
		if (mainUrl && sameRepository(url, mainUrl)) return { kind: "main", url };
		if (seen.has(key)) return { kind: "duplicate", url };
		seen.add(key);
		const folder = repoDirName(url);
		const owner = folders.get(folder.toLowerCase());
		if (owner !== undefined) return { kind: owner === "" ? "reserved" : "folder", url, folder };
		folders.set(folder.toLowerCase(), url);
	}
	return undefined;
}

/** One Setup or Cleanup step as the editor holds it; repository "" is the main repository. */
export type ScriptStepDraft = { command: string; repository: string };

/** Reads stored steps, each a plain command or {command, repository}. */
export function parseScriptSteps(value: unknown): ScriptStepDraft[] | undefined {
	if (!Array.isArray(value)) return undefined;
	return value.flatMap((item): ScriptStepDraft[] => {
		if (typeof item === "string") return [{ command: item, repository: "" }];
		if (item && typeof item === "object" && typeof (item as { command?: unknown }).command === "string") {
			const { command, repository } = item as { command: string; repository?: unknown };
			return [{ command, repository: typeof repository === "string" ? repository.trim() : "" }];
		}
		return [];
	});
}

/**
 * The stored form of the steps: a step in the main repository stays a plain
 * string (as before per-repository steps), any other is {command, repository}.
 * A step whose repository is no longer one of the project's falls back to the
 * main repository. Blank commands are dropped.
 */
export function serializeScriptSteps(steps: readonly ScriptStepDraft[], additional: readonly string[] = []): CloudCpProjectScriptStep[] {
	return steps
		.filter((step) => step.command.trim() !== "")
		.map((step) => {
			const repository = additional.find((url) => step.repository !== "" && sameRepository(url, step.repository));
			return repository ? { command: step.command, repository } : step.command;
		});
}

/** UTF-8 byte length, for the 16 KB rules limit. */
export function utf8Bytes(text: string): number {
	return new TextEncoder().encode(text).length;
}
