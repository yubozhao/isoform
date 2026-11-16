import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import { Switch } from "@/components/ui/switch";
import { GetDailyTopics, SetDailyTopicsEnabled } from "@/lib/wails-shim-app";
import { SettingsControlCard } from "./capture-shared";

/** Settings › What Isoform sees › Proposals from your topics: the computer's
 *  switch for the daily topics' proposals, the one the phone has (#1231).
 *  Placeholder words until design answers. */
export function TopicProposalsCard() {
    const { t } = useTranslation("settings");
    const { failure, fail } = useSettingsFailure();
    const [enabled, setEnabled] = useState<boolean | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [saving, setSaving] = useState(false);

    const load = useCallback(async () => {
        try {
            const view = await GetDailyTopics();
            setEnabled(Boolean(view?.enabled));
            setLoadFailed(false);
        } catch {
            setLoadFailed(true);
        }
    }, []);

    useEffect(() => {
        void load();
    }, [load]);

    const change = async (next: boolean) => {
        setSaving(true);
        try {
            const view = await SetDailyTopicsEnabled(next);
            setEnabled(Boolean(view?.enabled));
        } catch {
            fail(t("topicProposals.toggleFailed"), () => void change(next));
        } finally {
            setSaving(false);
        }
    };

    const title = t("topicProposals.title");
    return (
        <SettingsControlCard
            title={title}
            description={t("topicProposals.description")}
            control={
                <Switch
                    aria-label={title}
                    checked={enabled === true}
                    disabled={enabled === null || saving}
                    onCheckedChange={(next) => void change(next)}
                />
            }
        >
            {loadFailed && <SettingsFailureLine failure={{ text: t("topicProposals.loadFailed"), retry: () => void load() }} />}
            <SettingsFailureLine failure={failure} />
        </SettingsControlCard>
    );
}
