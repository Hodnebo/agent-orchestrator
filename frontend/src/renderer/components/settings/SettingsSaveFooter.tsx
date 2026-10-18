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
		{error ? <p role="alert" className="mb-3 text-sm text-error">{error}</p> : null}
		<div className="flex items-center justify-end gap-3">
			<Button disabled={!dirty || disabled} type="submit">{t("settings.project.saveChanges")}</Button>
		</div>
	</div>;
}
