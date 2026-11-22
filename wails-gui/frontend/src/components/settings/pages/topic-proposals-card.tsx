import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import { Switch } from "@/components/ui/switch";
import { GetAssistantChoice, GetDailyTopics, SetDailyTopicsEnabled } from "@/lib/wails-shim-app";
import { SettingsControlCard } from "./capture-shared";

/** Settings › Briefs and notifications › Proposals from your topics, at the
 *  top of the page (computer-settings v1.3.0 rules 1-3): the one switch the
 *  phone has too (PROTOCOL.md 15.9.1). Its line names the assistant the topic
 *  names go to, because writing a proposal sends them there. */
/** The assistant the topic names go to, by its product name; "the AI you
 *  chose" when the choice can't be read or routes through Isoform's gateway. */
export function assistantName(answering: string | null, t: TFunction<"settings">): string {
    switch (answering) {
        case "claude-code":
            return t("assistantChoice.agents.claudeCode");
        case "codex":
            return t("assistantChoice.agents.codex");
        case "deepseek":
            return t("assistantChoice.agents.deepseek");
        default:
            return t("topicProposals.yourAI");
    }
}

export function TopicProposalsCard() {
    const { t } = useTranslation("settings");
    const { failure, fail } = useSettingsFailure();
    const [enabled, setEnabled] = useState<boolean | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [saving, setSaving] = useState(false);
    const [answering, setAnswering] = useState<string | null>(null);

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
        GetAssistantChoice()
            .then((view) => setAnswering(view?.answering ?? null))
            .catch(() => setAnswering(null));
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
            description={t("topicProposals.description", { assistant: assistantName(answering, t) })}
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
