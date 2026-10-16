import { describe, expect, it } from "vitest";
import {
	additionalReposProblem,
	isStandaloneCloudProject,
	parseScriptSteps,
	projectAdditionalRepos,
	projectAgentStartsIn,
	repoDirName,
	serializeScriptSteps,
} from "./cloud-project-repos";

describe("cloud project repositories", () => {
	it("reads config.repos, falling back to the legacy coder extraRepos", () => {
		expect(projectAdditionalRepos({ repos: [{ url: "https://github.com/acme/11x" }, { url: " " }] })).toEqual([{ url: "https://github.com/acme/11x" }]);
		expect(projectAdditionalRepos({ coder: { extraRepos: [{ url: "https://github.com/acme/old", branch: "dev" }] } })).toEqual([{ url: "https://github.com/acme/old", branch: "dev" }]);
		// An explicit empty list wins over the legacy one.
		expect(projectAdditionalRepos({ repos: [], coder: { extraRepos: [{ url: "https://github.com/acme/old" }] } })).toEqual([]);
		expect(projectAdditionalRepos(undefined)).toEqual([]);
	});

	it("names folders like the control plane", () => {
		expect(repoDirName("https://github.com/acme/ChartSnip")).toBe("ChartSnip");
		expect(repoDirName("https://github.com/acme/app.git")).toBe("app");
		expect(repoDirName("https://github.com/acme/")).toBe("acme");
	});

	it("finds the first problem with a set of additional repositories", () => {
		const main = "https://github.com/acme/alice";
		expect(additionalReposProblem(main, ["https://github.com/acme/11x", ""])).toBeUndefined();
		expect(additionalReposProblem(main, ["https://github.com/ACME/alice"])?.kind).toBe("main");
		expect(additionalReposProblem(main, ["https://github.com/acme/11x", "https://github.com/acme/11x"])?.kind).toBe("duplicate");
		expect(additionalReposProblem(main, ["https://github.com/other/alice"])).toMatchObject({ kind: "folder", folder: "alice" });
		expect(additionalReposProblem(main, ["https://github.com/acme/repository"])?.kind).toBe("reserved");
		expect(additionalReposProblem(main, Array.from({ length: 11 }, (_, i) => `https://github.com/acme/r${i}`))?.kind).toBe("limit");
	});

	it("round-trips Setup steps, keeping main-repository steps as plain strings", () => {
		const steps = parseScriptSteps(["npm ci", { command: "make up", repository: "https://github.com/acme/11x" }, 5]);
		expect(steps).toEqual([{ command: "npm ci", repository: "" }, { command: "make up", repository: "https://github.com/acme/11x" }]);
		expect(serializeScriptSteps(steps ?? [], ["https://github.com/acme/11x"])).toEqual(["npm ci", { command: "make up", repository: "https://github.com/acme/11x" }]);
		// A step whose repository left the project runs in the main one.
		expect(serializeScriptSteps(steps ?? [], [])).toEqual(["npm ci", "make up"]);
	});

	it("recognizes standalone projects and where the agent starts", () => {
		expect(isStandaloneCloudProject({ repositoryUrl: "https://scratch.ao.local/p", config: {} })).toBe(true);
		expect(isStandaloneCloudProject({ repositoryUrl: "https://github.com/acme/alice", config: { source: "standalone" } })).toBe(true);
		expect(isStandaloneCloudProject({ repositoryUrl: "https://github.com/acme/alice", config: {} })).toBe(false);
		expect(projectAgentStartsIn({ agentStartsIn: "parent" })).toBe("parent");
		expect(projectAgentStartsIn({ agentStartsIn: "elsewhere" })).toBe("repository");
	});
});
