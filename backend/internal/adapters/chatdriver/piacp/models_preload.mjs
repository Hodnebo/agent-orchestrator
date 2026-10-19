// AO preloads this into pi-acp (BUN_OPTIONS=--preload), which carries it to
// pi-acp's daemon too. pi-acp bundles an older Pi runtime whose built-in model
// list predates the models providers serve now. A newer Pi keeps the current
// list in <agent dir>/models-store.json; this hands those models to the old
// runtime as custom models, written to AO_PI_MODELS_JSON, and leaves out the
// built-in models of a provider the store no longer lists. Pi's own files are
// only read. Any failure leaves pi-acp as it was.
import { existsSync, mkdirSync, readFileSync, realpathSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const out = process.env.AO_PI_MODELS_JSON;

// Only the fields the old runtime's models.json schema knows: a file it
// rejects is ignored whole, the user's own custom models with it.
const thinkingLevels = ["off", "minimal", "low", "medium", "high", "xhigh"];
function definition(model) {
	const kept = { id: model.id };
	for (const key of ["name", "api", "baseUrl", "reasoning", "contextWindow", "maxTokens"]) {
		if (model[key] !== undefined) kept[key] = model[key];
	}
	if (Array.isArray(model.input)) kept.input = model.input.filter((kind) => kind === "text" || kind === "image");
	const cost = model.cost;
	if (cost && ["input", "output", "cacheRead", "cacheWrite"].every((key) => typeof cost[key] === "number")) {
		kept.cost = { input: cost.input, output: cost.output, cacheRead: cost.cacheRead, cacheWrite: cost.cacheWrite };
	}
	if (model.thinkingLevelMap && typeof model.thinkingLevelMap === "object") {
		kept.thinkingLevelMap = Object.fromEntries(
			Object.entries(model.thinkingLevelMap).filter(([level]) => thinkingLevels.includes(level)),
		);
	}
	return kept;
}

// listed holds, per provider in the store, the model ids it serves now.
const listed = new Map();

function mergedModelsPath(modelsJsonPath) {
	const store = join(dirname(modelsJsonPath), "models-store.json");
	if (!existsSync(store)) return undefined;
	const current = JSON.parse(readFileSync(store, "utf-8"));
	// A models.json Pi cannot read is left for Pi to report.
	const config = existsSync(modelsJsonPath) ? JSON.parse(readFileSync(modelsJsonPath, "utf-8")) : { providers: {} };
	config.providers ??= {};
	for (const [provider, entry] of Object.entries(current)) {
		const models = Array.isArray(entry?.models) ? entry.models.filter((model) => model?.id) : [];
		if (models.length === 0) continue;
		const declared = config.providers[provider] ?? {};
		const own = Array.isArray(declared.models) ? declared.models : [];
		const ids = new Set(own.map((model) => model.id));
		declared.models = [...own, ...models.filter((model) => !ids.has(model.id)).map(definition)];
		config.providers[provider] = declared;
		listed.set(provider, new Set(declared.models.map((model) => model.id)));
	}
	mkdirSync(dirname(out), { recursive: true });
	writeFileSync(out, JSON.stringify(config));
	return out;
}

if (out && process.argv[1]) {
	try {
		const entry = realpathSync(process.argv[1]);
		const pi = await import(Bun.resolveSync("@earendil-works/pi-coding-agent", dirname(entry)));
		const { ModelRegistry, getAgentDir } = pi;
		if (ModelRegistry && typeof ModelRegistry.create === "function" && typeof getAgentDir === "function") {
			const create = ModelRegistry.create;
			ModelRegistry.create = function (authStorage, modelsJsonPath = join(getAgentDir(), "models.json")) {
				let path = modelsJsonPath;
				try {
					path = mergedModelsPath(modelsJsonPath) ?? modelsJsonPath;
				} catch (error) {
					console.error("ao: pi-acp models preload:", error instanceof Error ? error.message : error);
				}
				return create.call(this, authStorage, path);
			};
			for (const name of ["getAll", "getAvailable"]) {
				const original = ModelRegistry.prototype[name];
				if (typeof original !== "function") continue;
				ModelRegistry.prototype[name] = function (...args) {
					const models = original.apply(this, args);
					if (!Array.isArray(models) || listed.size === 0) return models;
					return models.filter((model) => !listed.has(model.provider) || listed.get(model.provider).has(model.id));
				};
			}
		}
	} catch (error) {
		console.error("ao: pi-acp models preload:", error instanceof Error ? error.message : error);
	}
}
