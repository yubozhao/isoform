import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Check } from "lucide-react";
import {
    BuildSetupClaims,
    GetSetupPermissions,
    ConfirmMemoryEntry,
    NotQuiteMemoryEntry,
    ReviseSetupClaim,
    UndoNotQuiteMemoryEntry,
    type SetupClaim,
} from "@/lib/wails-shim-app";
import { Button } from "@/components/ui/button";

// Step 2 · Is this right? (computer setup v1.0.0 rule 5, redesign-shell
// v1.0.0 rule 3): three to five claims from the calendar and mail, read just
// now, each with where it came from, answered with Y, N or E. Each claim is a
// belief in About you, so the answers land in You and Your answers.
//
// With no claims (setup-answers answer 6) the step is never shown empty:
// onNothing tells the setup to mark the rail and go on to step 3. "needs"
// is when neither calendar nor mail was allowed in step 1, so nothing is read;
// "later" is when they were read and gave nothing yet, so Is this right?
// arrives on Today. A read that fails is shown with Try again, never skipped.

/** The reading line stays at least this long before an empty read skips the
 * step: a shorter flash reads as a glitch, and the rail's "Later · on Today"
 * needs a beat on this step to be seen as this step's answer. */
export const RIGHT_NOTHING_HOLD_MS = 800;

export type RightNothing = "later" | "needs";

/** The permissions the claims are read from: the calendar itself, and Mail. */
const CLAIM_SOURCES = ["calendars", "automation_mail"];

type Answer = { kind: "yes" } | { kind: "no"; confirmedAt: number } | { kind: "edit"; text: string } | { kind: "editing"; draft: string };

export function RightStep({ onNothing }: { onNothing: (why: RightNothing) => void }) {
    const { t, i18n } = useTranslation("onboarding");
    const [claims, setClaims] = useState<SetupClaim[] | null>(null);
    const [answers, setAnswers] = useState<Record<string, Answer>>({});
    const [failed, setFailed] = useState(false);
    const [readFailed, setReadFailed] = useState(false);
    const [attempt, setAttempt] = useState(0);

    useEffect(() => {
        let live = true;
        let timer: number | undefined;
        const shownAt = Date.now();
        const nothing = (why: RightNothing) => {
            timer = window.setTimeout(
                () => live && onNothing(why),
                Math.max(0, RIGHT_NOTHING_HOLD_MS - (Date.now() - shownAt)),
            );
        };
        setReadFailed(false);
        void (async () => {
            const perms = await GetSetupPermissions().catch(() => null);
            if (!live) return;
            if (perms) {
                const allowed = perms.answers.some(
                    (a) => CLAIM_SOURCES.includes(a.key) && (a.state === "on" || a.state === "onAfterReopen"),
                );
                if (!allowed) {
                    nothing("needs");
                    return;
                }
            }
            try {
                const read = await BuildSetupClaims();
                if (!live) return;
                if (read.status === "ready" && read.claims.length > 0) setClaims(read.claims);
                else nothing("later");
            } catch {
                if (live) setReadFailed(true);
            }
        })();
        return () => {
            live = false;
            window.clearTimeout(timer);
        };
        // onNothing is read once per attempt.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [attempt]);

    const answer = useCallback(async (claim: SetupClaim, kind: "yes" | "no" | "edit") => {
        setFailed(false);
        try {
            if (kind === "yes") {
                await ConfirmMemoryEntry(claim.id);
                setAnswers((a) => ({ ...a, [claim.id]: { kind: "yes" } }));
            } else if (kind === "no") {
                const confirmedAt = await NotQuiteMemoryEntry(claim.id);
                setAnswers((a) => ({ ...a, [claim.id]: { kind: "no", confirmedAt } }));
            } else {
                setAnswers((a) => ({ ...a, [claim.id]: { kind: "editing", draft: claim.text } }));
            }
        } catch {
            setFailed(true);
        }
    }, []);

    const undo = useCallback(async (claim: SetupClaim, confirmedAt: number) => {
        try {
            await UndoNotQuiteMemoryEntry(claim.id, confirmedAt);
            setAnswers((a) => {
                const next = { ...a };
                delete next[claim.id];
                return next;
            });
        } catch {
            setFailed(true);
        }
    }, []);

    const save = useCallback(async (claim: SetupClaim, text: string) => {
        try {
            await ReviseSetupClaim(claim.id, text);
            setAnswers((a) => ({ ...a, [claim.id]: { kind: "edit", text } }));
        } catch {
            setFailed(true);
        }
    }, []);

    // Y, N and E answer the first claim not answered yet.
    useEffect(() => {
        const onKey = (e: KeyboardEvent) => {
            const target = e.target instanceof Element ? e.target : null;
            if (e.metaKey || e.ctrlKey || e.altKey || target?.closest("input, textarea, [contenteditable]")) return;
            const next = claims?.find((c) => !answers[c.id]);
            if (!next) return;
            const kind = ({ y: "yes", n: "no", e: "edit" } as const)[e.key.toLowerCase() as "y" | "n" | "e"];
            if (!kind) return;
            e.preventDefault();
            void answer(next, kind);
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    }, [claims, answers, answer]);

    const day = (at: number) =>
        new Intl.DateTimeFormat(i18n.language, { month: "short", day: "numeric" }).format(new Date(at));

    return (
        <div className="sv3-step" data-testid="setup-v3-right">
            <h2 className="sv3-step-title">{t("setup.step2")}</h2>
            <p className="sv3-body">{t("setup.rightLead")}</p>
            {readFailed ? (
                <div className="sv3-read-failed" role="alert" data-testid="setup-v3-right-failed">
                    <p className="sv3-warn">{t("setup.rightReadFailed")}</p>
                    <Button size="sm" variant="outline" onClick={() => setAttempt((n) => n + 1)}>
                        {t("setup.tryAgain")}
                    </Button>
                </div>
            ) : claims === null ? (
                <p className="sv3-quiet" data-testid="setup-v3-right-reading">
                    {t("setup.rightReading")}
                </p>
            ) : (
                <ul className="sv3-rows">
                    {claims.map((claim) => {
                        const given = answers[claim.id];
                        return (
                            <li key={claim.id} className="sv3-row sv3-claim" data-testid={`setup-v3-claim-${claim.id}`}>
                                <div className="sv3-claim-words">
                                    {given?.kind === "editing" ? (
                                        <input
                                            className="sv3-input"
                                            autoFocus
                                            aria-label={t("setup.rightEdit")}
                                            value={given.draft}
                                            onChange={(e) =>
                                                setAnswers((a) => ({ ...a, [claim.id]: { kind: "editing", draft: e.target.value } }))
                                            }
                                            onKeyDown={(e) => {
                                                if (e.key === "Enter" && given.draft.trim()) {
                                                    e.preventDefault();
                                                    void save(claim, given.draft.trim());
                                                }
                                            }}
                                        />
                                    ) : (
                                        <p className="sv3-strong">{given?.kind === "edit" ? given.text : claim.text}</p>
                                    )}
                                    <p className="sv3-quiet">
                                        {t(claim.source === "calendar" ? "setup.whyCalendar" : "setup.whyMail", { day: day(claim.at) })}
                                    </p>
                                </div>
                                {!given ? (
                                    <span className="sv3-answers">
                                        <span className="sv3-quiet">{t("setup.rightAsk")}</span>
                                        <Button size="sm" onClick={() => void answer(claim, "yes")}>
                                            {t("setup.rightYes")}
                                        </Button>
                                        <Button size="sm" variant="outline" onClick={() => void answer(claim, "no")}>
                                            {t("setup.rightNo")}
                                        </Button>
                                        <Button size="sm" variant="outline" onClick={() => void answer(claim, "edit")}>
                                            {t("setup.rightEdit")}
                                        </Button>
                                    </span>
                                ) : given.kind === "editing" ? (
                                    <Button
                                        size="sm"
                                        disabled={!given.draft.trim()}
                                        onClick={() => void save(claim, given.draft.trim())}
                                    >
                                        {t("setup.rightSave")}
                                    </Button>
                                ) : given.kind === "no" ? (
                                    <span className="sv3-answers">
                                        <span className="sv3-quiet">{t("setup.rightNotQuite")}</span>
                                        <Button size="sm" variant="quiet" onClick={() => void undo(claim, given.confirmedAt)}>
                                            {t("setup.rightUndo")}
                                        </Button>
                                    </span>
                                ) : (
                                    <span className="sv3-allowed">
                                        <Check size={14} aria-hidden />
                                        {t("setup.rightKept")}
                                    </span>
                                )}
                            </li>
                        );
                    })}
                </ul>
            )}
            {failed && (
                <p className="sv3-warn" role="alert">
                    {t("setup.rightFailed")}
                </p>
            )}
        </div>
    );
}
