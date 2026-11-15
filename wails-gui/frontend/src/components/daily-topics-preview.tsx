import { Loader2, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
    AnswerDailyTopicProposal,
    GetDailyTopics,
    RefreshDailyTopics,
    SetDailyTopicsEnabled,
    type DailyTopic,
    type DailyTopicsView,
} from "@/lib/wails-shim-app";
import { EventsOn } from "../../wailsjs/runtime/runtime";

type Verdict = "yes" | "no" | "not_mine";

// A minimal preview of the daily topics while design draws their screens:
// the switch, the day's topics and their proposed work with yes, no and
// "not mine". Shown with the developer tools only.
export function DailyTopicsPreview() {
    const { t } = useTranslation("settings");
    const [view, setView] = useState<DailyTopicsView | null>(null);
    const [busy, setBusy] = useState<string>("");

    const load = useCallback(async () => {
        try {
            setView(await GetDailyTopics());
        } catch (error) {
            toast.error(t("debug.dailyTopics.loadFailed"), { description: String(error) });
        }
    }, [t]);

    useEffect(() => {
        void load();
        const offTopics = EventsOn("topics-daily:updated", () => void load());
        const offTasks = EventsOn("handoff:updated", () => void load());
        return () => {
            offTopics();
            offTasks();
        };
    }, [load]);

    const toggle = async (enabled: boolean) => {
        setBusy("switch");
        try {
            setView(await SetDailyTopicsEnabled(enabled));
        } catch (error) {
            toast.error(t("debug.dailyTopics.saveFailed"), { description: String(error) });
        } finally {
            setBusy("");
        }
    };

    const refresh = async () => {
        setBusy("refresh");
        try {
            setView(await RefreshDailyTopics());
        } finally {
            setBusy("");
        }
    };

    const answer = async (topic: DailyTopic, verdict: Verdict) => {
        if (!topic.proposal) return;
        setBusy(topic.id);
        try {
            await AnswerDailyTopicProposal(topic.id, topic.proposal.id, verdict);
            await load();
        } catch (error) {
            toast.error(t("debug.dailyTopics.answerFailed"), { description: String(error) });
        } finally {
            setBusy("");
        }
    };

    return (
        <div className="space-y-3">
            <div className="flex items-center justify-between gap-3">
                <span className="text-xs text-foreground">{t("debug.dailyTopics.switch")}</span>
                <div className="flex items-center gap-2">
                    {view?.enabled && (
                        <Button variant="quiet" size="icon" onClick={() => void refresh()} disabled={busy !== ""} aria-label={t("debug.dailyTopics.refresh")}>
                            {busy === "refresh" ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />}
                        </Button>
                    )}
                    <Switch checked={Boolean(view?.enabled)} onCheckedChange={(on) => void toggle(on)} disabled={busy === "switch"} aria-label={t("debug.dailyTopics.switch")} />
                </div>
            </div>
            {view?.enabled && view.topics.length === 0 && (
                <p className="text-xs text-muted">{t("debug.dailyTopics.empty")}</p>
            )}
            {view?.enabled && view.topics.map((topic) => (
                <div key={topic.id} className="border-t border-border pt-3 first:border-t-0">
                    <div className="flex items-baseline gap-2">
                        <span className="text-xs font-medium text-foreground">{topic.name}</span>
                        <span className="text-[11px] text-muted">{t(`debug.dailyTopics.kind.${topic.kind}`)}</span>
                    </div>
                    {topic.proposal && (
                        <div className="mt-1.5 space-y-1.5">
                            <p className="text-xs text-foreground">{topic.proposal.title}</p>
                            {topic.proposal.why && <p className="text-[11px] text-muted">{topic.proposal.why}</p>}
                            {topic.proposal.state === "proposed" ? (
                                <div className="flex gap-2">
                                    {(["yes", "no", "not_mine"] as const).map((verdict) => (
                                        <Button key={verdict} variant={verdict === "yes" ? "default" : "outline"} size="sm" disabled={busy !== ""} onClick={() => void answer(topic, verdict)}>
                                            {t(`debug.dailyTopics.answer.${verdict}`)}
                                        </Button>
                                    ))}
                                </div>
                            ) : (
                                <p className="text-[11px] text-muted">
                                    {t(`debug.dailyTopics.state.${topic.proposal.state}`)}
                                    {topic.proposal.result ? ` · ${topic.proposal.result}` : ""}
                                </p>
                            )}
                        </div>
                    )}
                </div>
            ))}
        </div>
    );
}
