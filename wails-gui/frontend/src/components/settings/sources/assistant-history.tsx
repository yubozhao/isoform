import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Switch } from "@/components/ui/switch";
import { SettingsFailureLine, useSettingsFailure, type SettingsFailure } from "@/components/settings/settings-failure";
import { LoadUIState, SaveUIState } from "../../../../wailsjs/go/main/App";
import { GetReadAssistantHistory, SetReadAssistantHistory } from "@/lib/wails-shim-app";
import { SettingsControlCard } from "@/components/settings/pages/capture-shared";

/** How long the forget waits for Undo before it runs. */
export const ASSISTANT_HISTORY_FORGET_MS = 5000;

/** Where a forget that failed after Settings closed is remembered (S032). */
export const ASSISTANT_HISTORY_FAILURE_KEY = "settings.assistantHistory.failure";
type AssistantHistoryFailure = "stop" | "forget" | "start";

export interface AssistantHistoryState {
    /** null until read, and while a read failed. */
    enabled: boolean | null;
    readFailed: boolean;
    /** Turned off, waiting for Undo before it forgets. */
    stopping: boolean;
    read: () => void;
    change: (next: boolean) => void;
    undo: () => void;
    changeFailure: SettingsFailure | null;
}

// Reading the Claude Code and Codex history on this computer (first-day
// handoff §7). On by default. Turning it off forgets what came from it, after
// an Undo window shown in place; turning it on reads the history again at once.
// Leaving within the window runs the change at once, and a failure then is
// shown here the next time it opens. Backed by Get/SetReadAssistantHistory.
// Shared by What Isoform sees' Your agents' history row and Privacy's card.
export function useAssistantHistory(): AssistantHistoryState {
    const { t } = useTranslation("settings");
    const { failure: changeFailure, fail: failChange, clear: clearChange } = useSettingsFailure();
    const [enabled, setEnabled] = useState<boolean | null>(null);
    const [readFailed, setReadFailed] = useState(false);
    const [stopping, setStopping] = useState(false);
    const pending = useRef<ReturnType<typeof setTimeout> | null>(null);

    const showFailure = useCallback(
        (kind: AssistantHistoryFailure) => {
            const text =
                kind === "stop"
                    ? t("privacy.assistantHistory.stopFailed")
                    : kind === "forget"
                      ? t("privacy.assistantHistory.forgetFailed")
                      : t("privacy.assistantHistory.startFailed");
            const next = kind === "start";
            failChange(text, () => void saveRef.current(next));
        },
        [failChange, t],
    );

    const read = useCallback(() => {
        setEnabled(null);
        setReadFailed(false);
        GetReadAssistantHistory()
            .then((on) => {
                if (typeof on !== "boolean") throw new Error("Invalid setting");
                setEnabled(on);
            })
            .catch(() => setReadFailed(true));
        LoadUIState(ASSISTANT_HISTORY_FAILURE_KEY)
            .then((kind) => {
                if (kind === "stop" || kind === "forget") showFailure(kind);
            })
            .catch(() => {});
    }, [showFailure]);
    useEffect(() => { read(); }, []);

    const save = async (next: boolean) => {
        clearChange();
        try {
            await SetReadAssistantHistory(next);
            setEnabled(next);
            void SaveUIState(ASSISTANT_HISTORY_FAILURE_KEY, "").catch(() => {});
        } catch {
            // The preference may already be saved even if forgetting failed:
            // show the saved switch, and say which half failed.
            let on: boolean | null = null;
            try {
                on = await GetReadAssistantHistory();
            } catch {
                on = !next;
            }
            setEnabled(on);
            const kind: AssistantHistoryFailure = next ? "start" : on ? "stop" : "forget";
            showFailure(kind);
            if (kind !== "start") void SaveUIState(ASSISTANT_HISTORY_FAILURE_KEY, kind).catch(() => {});
        }
    };
    const saveRef = useRef(save);
    saveRef.current = save;

    // Leaving doesn't cancel the stop: it runs at once.
    useEffect(() => () => {
        if (!pending.current) return;
        clearTimeout(pending.current);
        pending.current = null;
        SetReadAssistantHistory(false)
            .then(() => SaveUIState(ASSISTANT_HISTORY_FAILURE_KEY, ""))
            .catch(async () => {
                const on = await GetReadAssistantHistory().catch(() => true);
                await SaveUIState(ASSISTANT_HISTORY_FAILURE_KEY, on ? "stop" : "forget");
            })
            .catch(() => {});
    }, []);

    const undo = () => {
        if (pending.current) clearTimeout(pending.current);
        pending.current = null;
        setStopping(false);
        setEnabled(true);
    };

    const change = (next: boolean) => {
        clearChange();
        setEnabled(next);
        if (next) {
            if (pending.current) {
                undo();
                return;
            }
            void save(true);
            return;
        }
        setStopping(true);
        pending.current = setTimeout(() => {
            pending.current = null;
            setStopping(false);
            void save(false);
        }, ASSISTANT_HISTORY_FORGET_MS);
    };

    return { enabled, readFailed, stopping, read, change, undo, changeFailure };
}

/** "Forgetting what came from it…" with Undo, while a turn-off waits. */
export function AssistantHistoryStopping({ history }: { history: AssistantHistoryState }) {
    const { t } = useTranslation("settings");
    if (!history.stopping) return null;
    return (
        <p className="text-[12px] text-muted">
            {t("privacy.assistantHistory.stopping")}{" "}
            <button type="button" className="text-accent hover:underline" onClick={history.undo}>
                {t("privacy.assistantHistory.undo")}
            </button>
        </p>
    );
}

/** Privacy's card for the same switch, until that page goes (batch D). */
export function AssistantHistoryCard() {
    const { t } = useTranslation("settings");
    const history = useAssistantHistory();
    const title = t("privacy.assistantHistory.title");
    const { enabled, readFailed } = history;

    return (
        <SettingsControlCard
            title={title}
            description={t("privacy.assistantHistory.description")}
            control={readFailed || enabled === null ? null : <Switch aria-label={title} checked={enabled} onCheckedChange={history.change} />}
        >
            {readFailed && <SettingsFailureLine failure={{ text: t("settingRead.failed"), retry: history.read }} />}
            <AssistantHistoryStopping history={history} />
            <SettingsFailureLine failure={history.changeFailure} />
        </SettingsControlCard>
    );
}
