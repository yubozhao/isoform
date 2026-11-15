import { useTranslation } from "react-i18next";
import {
    UseYansuAnywhereDetails,
    UseYansuAnywhereSettings,
} from "@/components/use-yansu-anywhere-settings";
import { SettingsAdvanced } from "@/components/settings/settings-advanced";
import { FrontSettings } from "@/components/settings/front-settings";

/** Other AI apps (page id "use-yansu-anywhere"): let other assistants recall
 *  what Isoform learned about your work. The card with the two actions comes
 *  first; what an assistant can recall, examples and the full install prompt
 *  sit under Advanced. */
export function OtherAIAppsPage() {
    const { t } = useTranslation("settings");
    return (
        <>
            <UseYansuAnywhereSettings />
            <FrontSettings />
            <SettingsAdvanced pageId="use-yansu-anywhere" hint={t("useYansuAnywhere.advancedHint")}>
                <UseYansuAnywhereDetails />
            </SettingsAdvanced>
        </>
    );
}
