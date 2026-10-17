import { chromeApi } from "../chrome-api";
import { SITE_ACCESS } from "../consent";

const chrome = chromeApi();
const sessionId = new URLSearchParams(location.search).get("session") ?? "";
document.getElementById("session")!.textContent = sessionId;
const error = document.getElementById("error")!;
const allow = document.getElementById("allow") as HTMLButtonElement;
const deny = document.getElementById("deny") as HTMLButtonElement;
const trusted = document.getElementById("trusted") as HTMLInputElement;

async function decide(allowed: boolean): Promise<void> {
	allow.disabled = deny.disabled = true;
	try {
		let useTrusted = trusted.checked;
		if (allowed) {
			// Must run inside the click: Chrome only shows permission prompts
			// for a user gesture.
			const granted = await chrome.permissions.request({
				origins: SITE_ACCESS.origins,
				...(useTrusted ? { permissions: ["debugger"] } : {}),
			});
			if (!granted) {
				error.textContent = "Chrome permission was not granted, so the agent cannot work. Allow again to retry.";
				allow.disabled = deny.disabled = false;
				return;
			}
			if (useTrusted) useTrusted = await chrome.permissions.contains({ permissions: ["debugger"] });
		}
		await chrome.runtime.sendMessage({ type: "ao-consent-decision", sessionId, allow: allowed, trusted: useTrusted });
		window.close();
	} catch (cause) {
		error.textContent = String(cause);
		allow.disabled = deny.disabled = false;
	}
}

allow.addEventListener("click", () => void decide(true));
deny.addEventListener("click", () => void decide(false));
allow.focus();
