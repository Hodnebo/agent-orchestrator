import userEvent from "@testing-library/user-event";
import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { AwakeState } from "../../hooks/useAwake";
import { StayAwakeSection } from "./StayAwakeSection";
import { TooltipProvider } from "../ui/tooltip";
const test = vi.hoisted(() => ({
	act: vi.fn(),
	state: undefined as AwakeState | undefined,
}));
vi.mock("../../hooks/useAwake", async (importOriginal) => ({
	...(await importOriginal<typeof import("../../hooks/useAwake")>()),
	useAwake: () => ({
		state: test.state,
		act: test.act,
		busy: false,
		error: null,
		queryError: null,
	}),
}));
beforeEach(() => {
	test.act.mockReset();
	test.state = {
		settings: {
			lidClosed: true,
			displayAwake: false,
			stopBatteryPercent: 10,
			lowPowerBatteryPercent: 0,
			lowPowerOnSeriousHeat: false,
			muteAudio: false,
			darkenKeyboard: false,
			restoreDisplay: false,
			pauseApps: [],
			hotspotName: "",
			hotspotRecovery: false,
			agentReconnect: false,
			browserActions: false,
		},
		sessions: [],
		rules: [],
		activeRules: [],
		capabilities: [
			{ id: "idle", available: true, reason: "" },
			{ id: "lid", available: true, reason: "" },
			{ id: "low_power", available: false, reason: "Authorization required" },
		],
		observation: {
			applications: [],
			browsers: [],
			knownNetworks: [],
			devices: [],
			networkAddresses: [],
			uptimeSeconds: 3600,
			observedAt: "2026-10-16T12:00:00Z",
			thermal: "nominal",
			audioControl: false,
			appPause: false,
			displayControl: false,
			keyboardControl: false,
			externalDisplays: 0,
			lowPowerMode: false,
		},
		active: false,
		lidActive: false,
		reason: "",
		paused: false,
		agentOutcome: "",
		networkOutcome: "",
	} as AwakeState;
});
function show() {
	return render(
		<TooltipProvider>
			<StayAwakeSection />
		</TooltipProvider>,
	);
}
describe("General Stay Awake settings", () => {
	it("uses the grouped settings section and starts an independent indefinite session", () => {
		const view = show();
		expect(
			view.container.querySelector(
				'[data-section="stay-awake"] .settings-grouped-rows',
			),
		).not.toBeNull();
		fireEvent.click(screen.getByRole("button", { name: "Start" }));
		expect(test.act).toHaveBeenCalledWith(
			expect.objectContaining({
				kind: "start",
				request: expect.objectContaining({
					scope: "ao",
					mode: "indefinite",
					lidClosed: true,
				}),
			}),
		);
		expect(
			screen.getByText("Works on this Mac without a phone or mobile access."),
		).toBeInTheDocument();
	});
	it("offers timed sessions and sends the selected duration", async () => {
		show();
		await userEvent
			.setup()
			.click(screen.getByRole("button", { name: "Keep awake" }));
		fireEvent.click(
			await screen.findByRole("menuitem", { name: "For a duration" }),
		);
		fireEvent.change(
			screen.getByRole("spinbutton", { name: "Duration in minutes" }),
			{ target: { value: "90" } },
		);
		fireEvent.click(screen.getByRole("button", { name: "Start" }));
		expect(test.act).toHaveBeenCalledWith(
			expect.objectContaining({
				request: expect.objectContaining({
					mode: "duration",
					durationSeconds: 5400,
				}),
			}),
		);
	});
	it("only offers controls that work and leaves removed groups out", () => {
		show();
		fireEvent.click(screen.getByText("Battery and heat protection"));
		expect(screen.queryByRole("switch", { name: "Low Power Mode" })).toBeNull();
		expect(screen.queryByText("Display, keyboard and audio")).toBeNull();
		expect(screen.queryByText("Apps, network and agent actions")).toBeNull();
		expect(screen.queryByText("Recovery and history")).toBeNull();
	});
	it("does not silently enable a new automatic rule", () => {
		show();
		fireEvent.click(screen.getByText("Automatic activation"));
		fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
		const enabled = screen.getByRole("switch", {
			name: "Enable rule New rule",
		});
		expect(enabled).toHaveAttribute("aria-checked", "false");
		fireEvent.click(screen.getByRole("button", { name: "Save rules" }));
		expect(test.act).toHaveBeenCalledWith(
			expect.objectContaining({
				kind: "rules",
				rules: [
					expect.objectContaining({
						enabled: false,
						conditions: [expect.objectContaining({ kind: "agent_activity" })],
					}),
				],
			}),
			expect.anything(),
		);
	});
});
