import { useTranslation } from "react-i18next";
import { Button } from "../ui/button";

/**
 * The Save bar of a project settings page that saves on submit. It is pinned to
 * the bottom of the settings dialog's scrolling body (whose padding it spans) so
 * Save stays in view however long the page is.
 */
export function SettingsSaveFooter({ dirty, disabled, error }: { dirty: boolean; disabled: boolean; error?: string }) {
	const { t } = useTranslation();
	return <div className="sticky -bottom-(--size-modal-padding) z-[1] -mx-(--size-modal-padding) -mb-(--size-modal-padding) border-t border-border bg-card px-(--size-modal-padding) py-4" data-settings-save-footer="">
		{/* The error shares the Save row, clamped to the button's height, so the bar
		    (and its divider) never moves when validation comes and goes. */}
		<div className="flex items-center justify-end gap-3">
			{error ? <p role="alert" className="line-clamp-2 min-w-0 flex-1 text-xs leading-4 text-error" title={error}>{error}</p> : null}
			<Button disabled={!dirty || disabled} type="submit">{t("settings.project.saveChanges")}</Button>
		</div>
	</div>;
}
