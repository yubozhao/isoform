import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Check } from "lucide-react";
import { AnswerWork, GetWorkList, UpdateUserTopic, type UserTopicAction, type WorkItem } from "@/lib/wails-shim-app";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import { Button } from "@/components/ui/button";

// Step 3 · What it can do today (computer setup v1.0.0 rule 6, Topics v1.0.0
// rules 28 to 33): day-one work from what Isoform has already seen, answered
// with the same keys as Your work (Yes and No only: there's no topic yet to
// call not mine), and "What are you working on, or following?" with Add.
// No samples: with nothing to offer, one line says so and only the field shows.

/** A topic the person names (UpdateUserTopic's add, from the Today owner). */
const ADD_TOPIC = "add" as unknown as UserTopicAction;

export function TodayStep() {
    const { t } = useTranslation("onboarding");
    const [items, setItems] = useState<WorkItem[] | null>(null);
    const [answered, setAnswered] = useState<Record<string, "yes" | "no">>({});
    const [label, setLabel] = useState("");
    const [added, setAdded] = useState<string[]>([]);
    const [failed, setFailed] = useState<"answer" | "add" | null>(null);

    // Read again when the work list changes: day-one items arrive in the
    // background while the step is open.
    useEffect(() => {
        let live = true;
        const read = () =>
            GetWorkList()
                .then((list) => live && setItems((list.items ?? []).filter((item) => item.source === "day_one" && !item.answer)))
                .catch(() => live && setItems((cur) => cur ?? []));
        void read();
        const off = EventsOn("work:updated", () => void read());
        return () => {
            live = false;
            off();
        };
    }, []);

    const answer = useCallback(async (item: WorkItem, action: "yes" | "no") => {
        setFailed(null);
        try {
            await AnswerWork(item.id, { action });
            setAnswered((a) => ({ ...a, [item.id]: action }));
        } catch {
            setFailed("answer");
        }
    }, []);

    const add = useCallback(async () => {
        const name = label.trim();
        if (!name) return;
        setFailed(null);
        try {
            await UpdateUserTopic({ id: "", action: ADD_TOPIC, label: name });
            setAdded((list) => [...list, name]);
            setLabel("");
        } catch {
            setFailed("add");
        }
    }, [label]);

    // Y and N answer the first open item, as in Your work.
    useEffect(() => {
        const onKey = (e: KeyboardEvent) => {
            const target = e.target instanceof Element ? e.target : null;
            if (e.metaKey || e.ctrlKey || e.altKey || target?.closest("input, textarea, [contenteditable]")) return;
            const next = items?.find((item) => !answered[item.id]);
            const action = ({ y: "yes", n: "no" } as const)[e.key.toLowerCase() as "y" | "n"];
            if (!next || !action) return;
            e.preventDefault();
            void answer(next, action);
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [items, answered, answer]);

    return (
        <div className="sv3-step" data-testid="setup-v3-today">
            <h2 className="sv3-step-title">{t("setup.step3")}</h2>
            {items && items.length > 0 ? (
                <>
                    <p className="sv3-body">{t("setup.todayLead")}</p>
                    <ul className="sv3-rows">
                        {items.map((item) => (
                            <li key={item.id} className="sv3-row sv3-claim" data-testid={`setup-v3-work-${item.id}`}>
                                <div className="sv3-claim-words">
                                    <p className="sv3-strong">{item.work.title}</p>
                                    {item.noticed && <p className="sv3-quiet">{item.noticed}</p>}
                                </div>
                                {answered[item.id] ? (
                                    <span className="sv3-allowed">
                                        <Check size={14} aria-hidden />
                                        {answered[item.id] === "yes" ? t("setup.todayYes") : t("setup.todayNo")}
                                    </span>
                                ) : (
                                    <span className="sv3-answers">
                                        <Button size="sm" onClick={() => void answer(item, "yes")}>
                                            {t("setup.rightYes")}
                                        </Button>
                                        <Button size="sm" variant="outline" onClick={() => void answer(item, "no")}>
                                            {t("setup.rightNo")}
                                        </Button>
                                    </span>
                                )}
                            </li>
                        ))}
                    </ul>
                </>
            ) : items ? (
                <p className="sv3-quiet">{t("setup.todayNothing")}</p>
            ) : null}
            <div className="sv3-follow">
                <label className="sv3-strong" htmlFor="setup-v3-topic">
                    {t("setup.followTitle")}
                </label>
                <p className="sv3-quiet">{t("setup.followLine")}</p>
                <span className="sv3-answers">
                    <input
                        id="setup-v3-topic"
                        className="sv3-input"
                        value={label}
                        onChange={(e) => setLabel(e.target.value)}
                        onKeyDown={(e) => {
                            if (e.key === "Enter" && label.trim()) {
                                e.preventDefault();
                                void add();
                            }
                        }}
                    />
                    <Button size="sm" disabled={!label.trim()} onClick={() => void add()} data-testid="setup-v3-add-topic">
                        {t("setup.followAdd")}
                    </Button>
                </span>
                {added.map((name) => (
                    <p key={name} className="sv3-allowed">
                        <Check size={14} aria-hidden />
                        {t("setup.followAdded", { name })}
                    </p>
                ))}
            </div>
            {failed && (
                <p className="sv3-warn" role="alert">
                    {failed === "add" ? t("setup.followFailed") : t("setup.rightFailed")}
                </p>
            )}
        </div>
    );
}
