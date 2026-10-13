import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";

describe("SettingsRow", () => {
	it("renders a node description under the label and passes row attributes through", () => {
		render(
			<SettingsSection title="Version" grouped>
				<SettingsRow
					data-agent="codex"
					id="row"
					label={<span id="row-label">v1.2.3</span>}
					leading={<span data-testid="avatar" />}
					description={<span role="status">Checked 2 minutes ago</span>}
				>
					<button type="button">Check for updates</button>
				</SettingsRow>
			</SettingsSection>,
		);
		const row = document.getElementById("row");
		expect(row).toHaveAttribute("data-agent", "codex");
		expect(row).toHaveClass("settings-row-bar");
		expect(row?.parentElement).toHaveClass("settings-grouped-rows");
		expect(screen.getByRole("status")).toHaveTextContent("Checked 2 minutes ago");
		expect(screen.getByTestId("avatar")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Check for updates" })).toBeInTheDocument();
	});
});

describe("SettingsSection", () => {
	it("renders the toolbar between the heading and the card", () => {
		render(
			<SettingsSection title="Downloads" grouped toolbar={<div data-testid="toolbar" />}>
				<SettingsRow label="Row" />
			</SettingsSection>,
		);
		const heading = screen.getByRole("heading", { name: "Downloads" });
		const toolbar = screen.getByTestId("toolbar");
		const card = toolbar.nextElementSibling;
		expect(heading.compareDocumentPosition(toolbar) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
		expect(card).toHaveClass("settings-grouped-rows");
		expect(card).not.toContainElement(toolbar);
	});
});
