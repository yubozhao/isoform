import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useSettingsDevTools } from "@/components/settings/settings-dev-tools";
import { useFeatureSwitches } from "@/hooks/useFeatureSwitches";
import { AlertCircle, ChevronDown, Loader2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
    GetEngagementSettings,
    SetEngagementKindEnabled,
    SetEngagementKindQuiet,
    SetEngagementLevel,
    SetEngagementRules,
    type EngagementKindView,
    type EngagementLevel,
    type EngagementRulesInput,
    type EngagementSettingsView,
} from "@/lib/wails-shim-app";
import { SettingsCard } from "@/components/settings/settings-primitives";
import { settingsAnchor } from "@/components/settings/settings-advanced";
import { openSettingsPage, type OpenSettingsPage } from "@/components/settings/pages/capture-shared";
import { EngagementWeekLine, EngagementWeekSummary, useEngagementWeek } from "./engagement-week-summary";
import { publishEngagementSettings } from "@/lib/engagement-settings-events";
import { Button } from "./ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "./ui/collapsible";
import { Switch } from "./ui/switch";

// Same chip as the granted state in the Permissions list.
const CHIP_CLASS =
    "inline-flex items-center rounded-full bg-ink/[0.05] px-2 py-0.5 text-[10px] font-medium text-ink/55";
// Same field as the phone number in the text messages card.
const FIELD_CLASS =
    "rounded-md border border-border bg-panel px-2 py-1 text-xs tabular-nums text-ink focus:outline-none focus:ring-1 focus:ring-accent disabled:opacity-50";
// Mirrors engagementRulesBudgetMax in wails-gui/engagement_service.go.
const BUDGET_MAX = 24;
// The busy markers for the two rules rows and the dial, beside the kind keys.
const RULES_QUIET = "rules:quiet";
const RULES_BUDGET = "rules:budget";
const LEVEL_DIAL = "level";
// The dial positions the user can pick, quietest first. Mirrors
// engagement.Levels() in wails-gui/internal/engagement/level.go.
const LEVELS: readonly Exclude<EngagementLevel, "custom">[] = ["off", "fewer", "normal", "more"];
// Where "Quiet hours and limits" (under Advanced) is scrolled to.
export const QUIET_HOURS_ANCHOR = "quiet-hours";
// The one check-in kind that exists today: its notice carries answer buttons.
// Destinations in the 30-day stats (PROTOCOL.md 16.1), in display order.
const DESTINATIONS = ["computer", "phone", "text", "email"] as const;

type Tag = NonNullable<EngagementKindView["tag"]>;
const TAG_CLASS: Readonly<Record<Exclude<Tag, "">, string>> = {
    noisy: "border-warning-border bg-warning-bg text-warning",
    needs_you: "border-info-border bg-info-bg text-info",
    ignored: "border-border bg-panel-warm text-muted",
    rare: "border-border bg-panel-warm text-muted",
    quiet: "border-border bg-panel-warm text-muted",
};

/** A small neutral status pill, shared by the kind rows and the channel rows. */
export function NotificationChip({ children }: { children: ReactNode }) {
    return <span className={CHIP_CLASS}>{children}</span>;
}

/**
 * One row on the Notifications page. It uses the SettingsRow classes, and also
 * shows a state line (such as why a kind paused), a quiet secondary note and a
 * save error in the text column, beside the control. SettingsRow renders
 * either a control or children, so an error passed as children there would
 * never show.
 */
export function NotificationRow({
    title,
    description,
    state,
    note,
    error,
    control,
}: {
    title: string;
    description?: string;
    state?: string;
    note?: string;
    error?: string | null;
    control?: ReactNode;
}) {
    return (
        <div className="settings-row">
            <div className="flex flex-col min-w-0">
                <span className="settings-row-title truncate">{title}</span>
                {description && <span className="settings-row-desc">{description}</span>}
                {state && <span className="settings-row-desc">{state}</span>}
                {note && <span className="mt-1 text-[11px] leading-snug text-muted">{note}</span>}
                {error && (
                    <span role="alert" className="mt-1 text-xs text-error">
                        {error}
                    </span>
                )}
            </div>
            {control && <div className="shrink-0 flex items-center gap-2">{control}</div>}
        </div>
    );
}

// ── Shared state ──

export interface EngagementSettingsState {
    view: EngagementSettingsView | null;
    loadFailed: boolean;
    /** The kind or control whose write is running, or null. */
    busy: string | null;
    refresh: () => Promise<void>;
    /** One write at a time; the view the binding returns replaces the page's
     *  view. A failure says so in a toast and leaves the view as it was, so
     *  the control goes back to its old value. */
    write: (target: string, run: () => Promise<EngagementSettingsView>, failure: string) => Promise<void>;
}

/** The notification settings the page's cards share: one read, one writer. */
export function useEngagementSettingsState(): EngagementSettingsState {
    const [view, setView] = useState<EngagementSettingsView | null>(null);
    const [loadFailed, setLoadFailed] = useState(false);
    const [busy, setBusy] = useState<string | null>(null);

    const mounted = useRef(false);
    const busyRef = useRef(false);
    // Bumped by every read and write; a read whose version is stale when it resolves is dropped.
    const readVersion = useRef(0);

    const refresh = useCallback(async () => {
        if (busyRef.current) return;
        const version = ++readVersion.current;
        try {
            const next = await GetEngagementSettings();
            if (mounted.current && version === readVersion.current) {
                setView(next);
                setLoadFailed(false);
            }
        } catch {
            if (mounted.current && version === readVersion.current) setLoadFailed(true);
        }
    }, []);

    useEffect(() => {
        mounted.current = true;
        void refresh();
        const onFocus = () => void refresh();
        window.addEventListener("focus", onFocus);
        return () => {
            mounted.current = false;
            readVersion.current += 1;
            window.removeEventListener("focus", onFocus);
        };
    }, [refresh]);

    const write = useCallback(
        async (target: string, run: () => Promise<EngagementSettingsView>, failure: string) => {
            if (busyRef.current) return;
            busyRef.current = true;
            readVersion.current += 1;
            setBusy(target);
            try {
                const next = await run();
                publishEngagementSettings(next);
                if (mounted.current) setView(next);
            } catch {
                if (mounted.current) toast.error(failure);
            } finally {
                busyRef.current = false;
                if (mounted.current) setBusy(null);
            }
        },
        [],
    );

    return { view, loadFailed, busy, refresh, write };
}

// ── The last 30 days ──

/** Interruptions sent and useful in the stats window (RC 1 A5); older
 *  computers report only the all-channel lifecycle counts. */
function sentOf(kind: EngagementKindView): number {
    return kind.notificationSent ?? kind.sent;
}

function usefulOf(kind: EngagementKindView): number {
    return kind.notificationUseful ?? kind.notificationActed ?? kind.acted;
}

function quietOf(kind: EngagementKindView): boolean {
    return kind.quiet ?? kind.quietOnToday ?? false;
}

/** A kind keeps its place in the ranked list while it sent something or is
 *  quieted to Today; the rest fold into one line. */
function listed(kind: EngagementKindView): boolean {
    return sentOf(kind) > 0 || (kind.quietSent ?? 0) > 0 || quietOf(kind);
}

/** "Show on Today instead" is a suggestion: only for kinds tagged Noisy or
 *  Ignored that may be quieted and may be suggested (never the kinds that
 *  need your answer, never a reminder you set). */
function suggestsQuiet(kind: EngagementKindView): boolean {
    const canQuiet = kind.canQuiet ?? kind.quietAllowed;
    return (
        canQuiet &&
        kind.quietSuggestionAllowed &&
        !quietOf(kind) &&
        (kind.tag === "noisy" || kind.tag === "ignored")
    );
}

function TagChip({ tag, children }: { tag: Exclude<Tag, "">; children: ReactNode }) {
    return (
        <span
            data-tag={tag}
            className={`inline-flex h-5 items-center rounded-full border px-[7px] text-[11px] font-medium ${TAG_CLASS[tag]}`}
        >
            {children}
        </span>
    );
}

function KindRow({
    kind,
    maxSent,
    state,
}: {
    kind: EngagementKindView;
    maxSent: number;
    state: EngagementSettingsState;
}) {
    const { t, i18n } = useTranslation("settings");
    const label = t(`proactiveNotifications.kinds.${kind.kind}.label` as never) as string;
    const description = t(`proactiveNotifications.kinds.${kind.kind}.description` as never) as string;
    const quiet = quietOf(kind);
    const sent = sentOf(kind);
    const useful = usefulOf(kind);
    const busy = state.busy === kind.kind;
    const disabled = state.busy !== null;
    const labelId = `notification-kind-${kind.kind}`;
    const tag = (quiet ? "quiet" : (kind.tag ?? "")) as Tag;
    const sampleKey = kind.sampleCopyKey ? kind.sampleCopyKey.replace(".", "_") : "";
    const sample = sampleKey ? (t(`proactiveNotifications.samples.${sampleKey}` as never) as string) : "";

    const failure = t("proactiveNotifications.saveFailed");
    const setEnabled = (enabled: boolean) =>
        void state.write(kind.kind, () => SetEngagementKindEnabled(kind.kind, enabled), failure);
    const setQuiet = (next: boolean) =>
        void state.write(kind.kind, () => SetEngagementKindQuiet(kind.kind, next), failure);

    const statParts = [t("proactiveNotifications.ranked.stat", { sent, useful })];
    // Dismissed and muted are the all-channel lifecycle counts (the old
    // page's line); beside interruptions only, so a kind that only showed
    // quiet lines never reads "0 sent, 2 dismissed".
    if (sent > 0 && kind.dismissed > 0) statParts.push(t("proactiveNotifications.ranked.dismissed", { count: kind.dismissed }));
    if (sent > 0 && kind.muted > 0) statParts.push(t("proactiveNotifications.ranked.muted", { count: kind.muted }));
    const sentWidth = maxSent > 0 && sent > 0 ? Math.max(4, Math.round((sent / maxSent) * 100)) : 0;
    const usefulWidth = maxSent > 0 ? Math.round((Math.min(useful, sent) / maxSent) * 100) : 0;

    const destinations = DESTINATIONS.filter((destination) => (kind.channels ?? []).includes(destination));
    let where = "";
    if (quiet) {
        where = t("proactiveNotifications.ranked.whereQuiet");
    } else if (destinations.length > 0) {
        const locale = i18n.resolvedLanguage ?? "en";
        const list = new Intl.ListFormat(locale, { type: "conjunction" }).format(
            destinations.map((destination) => t(`proactiveNotifications.ranked.where.${destination}`)),
        );
        where = list.charAt(0).toLocaleUpperCase(locale) + list.slice(1);
    }

    let action: ReactNode = null;
    if (kind.paused) {
        action = (
            <Button variant="secondary" size="sm" aria-describedby={labelId} disabled={disabled} onClick={() => setEnabled(true)}>
                {t("proactiveNotifications.resume")}
            </Button>
        );
    } else if (quiet) {
        action = (
            <Button variant="quiet" size="sm" aria-describedby={labelId} disabled={disabled} onClick={() => setQuiet(false)}>
                {t("proactiveNotifications.ranked.notifyAgain")}
            </Button>
        );
    } else if (suggestsQuiet(kind)) {
        action = (
            <Button variant="secondary" size="sm" aria-describedby={labelId} disabled={disabled} onClick={() => setQuiet(true)}>
                {t("proactiveNotifications.ranked.showOnToday")}
            </Button>
        );
    }

    return (
        <div
            data-kind={kind.kind}
            className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-border-divider px-[18px] py-3 last:border-b-0"
        >
            <div className="flex min-w-0 flex-[1_1_220px] flex-col gap-[3px]">
                <div className="flex flex-wrap items-center gap-2">
                    <span id={labelId} className="text-[13.5px] font-semibold text-ink">
                        {label}
                    </span>
                    {tag && <TagChip tag={tag}>{t(`proactiveNotifications.ranked.tags.${tag}`)}</TagChip>}
                    {kind.paused && <NotificationChip>{t("proactiveNotifications.pausedChip")}</NotificationChip>}
                </div>
                {sample && (
                    <span className="text-[12.5px] text-muted">
                        {t("proactiveNotifications.ranked.sample", { text: sample })}
                    </span>
                )}
                <span className="text-xs text-muted">{description}</span>
                {kind.paused && (
                    <span className="text-[12.5px] text-muted">
                        {t("proactiveNotifications.pausedHint", { kind: label })}
                    </span>
                )}
                {quiet && <span className="text-[12.5px] text-muted">{t("proactiveNotifications.ranked.quietHint")}</span>}
            </div>
            {/* Counts and where it went share one column so the row keeps
                its switch on the same line at Settings' content width. */}
            <div className="flex flex-[0_0_160px] flex-col gap-1">
                <div
                    role="img"
                    aria-label={t("proactiveNotifications.ranked.statLabel", { sent, useful })}
                    className="relative h-1.5 overflow-hidden rounded-full bg-card-progress"
                >
                    <span className="absolute inset-y-0 left-0 bg-border-focus" style={{ width: `${sentWidth}%` }} />
                    <span className="absolute inset-y-0 left-0 bg-accent" style={{ width: `${usefulWidth}%` }} />
                </div>
                <span className="text-xs tabular-nums text-muted">{statParts.join(" · ")}</span>
                {(kind.quietSent ?? 0) > 0 && (
                    <span className="text-xs tabular-nums text-muted">
                        {t("proactiveNotifications.ranked.quietStat", { count: kind.quietSent })}
                    </span>
                )}
                {where && <span className="text-xs text-muted">{where}</span>}
            </div>
            <div className="ml-auto flex shrink-0 items-center gap-2">
                {busy && <Loader2 className="size-3.5 animate-spin" />}
                {/* A fixed slot keeps the counts column aligned across rows. */}
                <div className="flex w-[168px] justify-end">{action}</div>
                <Switch
                    aria-label={kind.paused ? t("proactiveNotifications.resumeKind", { kind: label }) : label}
                    checked={kind.enabled && !kind.paused}
                    disabled={disabled}
                    onCheckedChange={(checked) => setEnabled(checked)}
                />
            </div>
        </div>
    );
}

/** A kind that sent nothing in the window: its name, what it is and its switch. */
function FoldedKindRow({ kind, state }: { kind: EngagementKindView; state: EngagementSettingsState }) {
    const { t } = useTranslation("settings");
    const label = t(`proactiveNotifications.kinds.${kind.kind}.label` as never) as string;
    const failure = t("proactiveNotifications.saveFailed");
    return (
        <NotificationRow
            title={label}
            description={t(`proactiveNotifications.kinds.${kind.kind}.description` as never) as string}
            state={kind.paused ? t("proactiveNotifications.pausedHint", { kind: label }) : undefined}
            control={
                <>
                    {kind.paused && <NotificationChip>{t("proactiveNotifications.pausedChip")}</NotificationChip>}
                    {state.busy === kind.kind && <Loader2 className="size-3.5 animate-spin" />}
                    <Switch
                        aria-label={kind.paused ? t("proactiveNotifications.resumeKind", { kind: label }) : label}
                        checked={kind.enabled && !kind.paused}
                        disabled={state.busy !== null}
                        onCheckedChange={(checked) =>
                            void state.write(kind.kind, () => SetEngagementKindEnabled(kind.kind, checked), failure)
                        }
                    />
                </>
            }
        />
    );
}

function Disclosure({ label, children }: { label: string; children: ReactNode }) {
    const [open, setOpen] = useState(false);
    return (
        <Collapsible open={open} onOpenChange={setOpen} className="flex flex-col">
            <CollapsibleTrigger className="flex items-center gap-1.5 self-start px-[18px] py-3 text-left text-[13px] text-muted transition-colors hover:text-ink">
                <ChevronDown aria-hidden className={`h-3 w-3 shrink-0 transition-transform ${open ? "rotate-180" : ""}`} />
                {label}
            </CollapsibleTrigger>
            <CollapsibleContent>{children}</CollapsibleContent>
        </Collapsible>
    );
}

/** Kinds only Chat sends: an answer to a question asked in Chat (open answers
 *  start from a Chat turn). They leave the list while Chat is off. */
const CHAT_ONLY_KINDS: readonly string[] = ["answer_ready"];

function LastThirtyDays({ view, state }: { view: EngagementSettingsView; state: EngagementSettingsState }) {
    const { t } = useTranslation("settings");
    const devTools = useSettingsDevTools();
    const chatOn = useFeatureSwitches().chat;
    const kinds = (view.kinds ?? []).filter((kind) => chatOn || !CHAT_ONLY_KINDS.includes(kind.kind));
    // Ranked by interruptions; the catalog order breaks ties.
    const ranked = kinds
        .map((kind, index) => ({ kind, index }))
        .filter(({ kind }) => listed(kind))
        .sort((a, b) => sentOf(b.kind) - sentOf(a.kind) || a.index - b.index)
        .map(({ kind }) => kind);
    const folded = kinds.filter((kind) => !listed(kind));
    const maxSent = Math.max(0, ...ranked.map(sentOf));
    const sent = view.summary?.sent ?? kinds.reduce((sum, kind) => sum + sentOf(kind), 0);
    const useful = view.summary?.useful ?? kinds.reduce((sum, kind) => sum + usefulOf(kind), 0);
    const quietSent = view.summary?.quietSent ?? 0;
    // The week's line stays here beside the 30 days (Claude Design,
    // 2026-10-22: Today's header drops it), with the week's detail folded
    // under the list. One read feeds both.
    const week = useEngagementWeek();

    return (
        <section aria-labelledby="notifications-month" className="flex flex-col gap-3">
            <div className="flex flex-col gap-1">
                <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1.5">
                    <h2 id="notifications-month" className="text-base font-semibold text-ink">
                        {t("proactiveNotifications.ranked.title", { days: view.statsDays })}
                    </h2>
                    <span className="text-[13px] text-muted">
                        {t("proactiveNotifications.ranked.summary", { sent, useful })}
                        {quietSent > 0 && ` ${t("proactiveNotifications.ranked.summaryQuiet", { count: quietSent })}`}
                    </span>
                </div>
                <EngagementWeekLine week={week} />
                <p className="text-xs text-muted">{t("proactiveNotifications.kindsDescription")}</p>
            </div>
            <div className="settings-card flex flex-col !px-0 py-1.5">
                {ranked.map((kind) => (
                    <KindRow key={kind.kind} kind={kind} maxSent={maxSent} state={state} />
                ))}
                {folded.length > 0 && (
                    <div className={ranked.length > 0 ? "border-t border-border-divider" : undefined}>
                        <Disclosure
                            label={t("proactiveNotifications.ranked.fold", { count: folded.length, days: view.statsDays })}
                        >
                            <div className="flex flex-col px-[18px] pb-1">
                                {folded.map((kind) => (
                                    <FoldedKindRow key={kind.kind} kind={kind} state={state} />
                                ))}
                            </div>
                        </Disclosure>
                    </div>
                )}
                {devTools && (
                    <div className="border-t border-border-divider">
                        <Disclosure label={t("proactiveNotifications.ranked.weekDisclosure")}>
                            <div className="px-[18px] pb-3">
                                <EngagementWeekSummary week={week} />
                            </div>
                        </Disclosure>
                    </div>
                )}
            </div>
        </section>
    );
}

function LevelCard({ view, state }: { view: EngagementSettingsView; state: EngagementSettingsState }) {
    const { t } = useTranslation("settings");
    const title = t("proactiveNotifications.level.title");
    const setLevel = (level: EngagementLevel) => {
        if (level === "custom" || view.level === level) return;
        void state.write(LEVEL_DIAL, () => SetEngagementLevel(level), t("proactiveNotifications.level.saveFailed"));
    };
    return (
        <section aria-labelledby="notifications-level" className="settings-card settings-card--padded flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-x-4 gap-y-2.5">
                <h2 id="notifications-level" className="settings-card-title">
                    {title}
                </h2>
                <div className="ml-auto flex items-center gap-2">
                    {state.busy === LEVEL_DIAL && <Loader2 className="size-3.5 animate-spin" />}
                    {view.level === "custom" && <NotificationChip>{t("proactiveNotifications.level.custom")}</NotificationChip>}
                    {/* The raised segmented control every Settings page uses. */}
                    <div role="radiogroup" aria-label={title} className="settings-segment">
                        {LEVELS.map((level) => {
                            const selected = view.level === level;
                            return (
                                <button
                                    key={level}
                                    type="button"
                                    role="radio"
                                    aria-checked={selected}
                                    data-active={selected}
                                    disabled={state.busy !== null}
                                    title={t(`proactiveNotifications.level.options.${level}.description`)}
                                    onClick={() => setLevel(level)}
                                    className="disabled:cursor-wait"
                                >
                                    {t(`proactiveNotifications.level.options.${level}.label`)}
                                </button>
                            );
                        })}
                    </div>
                </div>
            </div>
            {/* settings-review ST72: never two lines saying the same thing. */}
            {view.level !== "custom" && (
                <p className="text-[13px] text-muted">
                    {t(`proactiveNotifications.level.options.${view.level}.description`)}
                </p>
            )}
            <p className="text-xs text-muted">{t("proactiveNotifications.level.description")}</p>
        </section>
    );
}

function WhereNoticesGo({ onOpenPage }: { onOpenPage?: OpenSettingsPage }) {
    const { t } = useTranslation("settings");
    return (
        <section className="settings-card settings-card--padded flex flex-wrap items-center gap-x-4 gap-y-2.5">
            <h2 className="settings-card-title flex-[1_1_240px]">{t("proactiveNotifications.whereNotices.title")}</h2>
            <span className="text-[13px] text-ink">{t("proactiveNotifications.whereNotices.body")}</span>
            <Button
                variant="link"
                className="h-auto px-0 py-1 text-[13px] font-medium"
                onClick={() =>
                    openSettingsPage("notifications", onOpenPage, { advanced: true, anchor: QUIET_HOURS_ANCHOR })
                }
            >
                {t("proactiveNotifications.whereNotices.link")}
            </Button>
        </section>
    );
}

/**
 * What Isoform may tell you about, ranked by whether it earned your attention
 * over the stats window (RC 1): the level dial, every kind that sent something
 * with its real push line, a sent and useful bar, where it went, a tag and
 * its switch (the rest fold into one line, each keeping its switch), the
 * week's counts folded under the list, the check-ins that exist and where
 * notices go. Quiet hours and limits sit under Advanced (QuietHoursCard).
 */
export function NotificationsSettings({
    state,
    onOpenPage,
}: {
    state: EngagementSettingsState;
    onOpenPage?: OpenSettingsPage;
}) {
    const { t } = useTranslation("settings");
    const { view, loadFailed } = state;

    if (loadFailed) {
        return (
            <SettingsCard>
                <div className="flex flex-col items-start gap-2.5">
                    <p role="alert" className="flex items-center gap-2 text-[13.5px] text-ink">
                        <AlertCircle aria-hidden className="size-[15px] shrink-0 text-error" />
                        {t("proactiveNotifications.loadFailed")}
                    </p>
                    <Button variant="secondary" size="sm" onClick={() => void state.refresh()}>
                        {t("proactiveNotifications.retry")}
                    </Button>
                </div>
            </SettingsCard>
        );
    }
    if (!view) {
        return (
            <SettingsCard>
                <p aria-busy="true" className="flex items-center gap-2 text-[13.5px] text-muted">
                    <Loader2 className="size-3.5 animate-spin" />
                    {t("proactiveNotifications.loading")}
                </p>
            </SettingsCard>
        );
    }
    if (!view.available) {
        return (
            <SettingsCard>
                <p className="text-xs text-muted">{t("proactiveNotifications.unavailable")}</p>
            </SettingsCard>
        );
    }

    return (
        <>
            <LevelCard view={view} state={state} />
            <LastThirtyDays view={view} state={state} />
            <WhereNoticesGo onOpenPage={onOpenPage} />
        </>
    );
}

/** Quiet hours, the daily interruption limit and the digest times, under the
 *  page's Advanced section (the link in "Where notices go" lands here). */
export function QuietHoursCard({ state }: { state: EngagementSettingsState }) {
    const { t } = useTranslation("settings");
    // The budget as typed, until it is saved on blur or Enter.
    const [budgetDraft, setBudgetDraft] = useState<string | null>(null);
    const { view } = state;
    if (!view || !view.available || state.loadFailed) return null;

    const failure = t("proactiveNotifications.rulesFailed");
    const setRules = (target: string, changes: Partial<EngagementRulesInput>) => {
        const input: EngagementRulesInput = {
            quietStart: view.quietStart,
            quietEnd: view.quietEnd,
            dailyInterruptBudget: view.dailyInterruptBudget,
            ...changes,
        };
        setBudgetDraft(null);
        return state.write(target, () => SetEngagementRules(input), failure);
    };

    const saveBudget = () => {
        if (budgetDraft === null) return;
        const parsed = Number.parseInt(budgetDraft, 10);
        if (!Number.isInteger(parsed) || parsed < 0 || parsed > BUDGET_MAX || parsed === view.dailyInterruptBudget) {
            setBudgetDraft(null);
            return;
        }
        void setRules(RULES_BUDGET, { dailyInterruptBudget: parsed });
    };

    const quietOff = view.quietStart === view.quietEnd;
    const disabled = state.busy !== null;

    return (
        <div {...settingsAnchor(QUIET_HOURS_ANCHOR)}>
            <SettingsCard
                title={t("proactiveNotifications.timingTitle")}
                description={t("proactiveNotifications.timingDescription")}
            >
                <NotificationRow
                    title={t("proactiveNotifications.quietTitle")}
                    description={t("proactiveNotifications.quietDescription")}
                    note={quietOff ? t("proactiveNotifications.quietOff") : t("proactiveNotifications.quietException")}
                    control={
                        <>
                            {state.busy === RULES_QUIET && <Loader2 className="size-3.5 animate-spin" />}
                            <input
                                type="time"
                                aria-label={t("proactiveNotifications.quietStartLabel")}
                                className={FIELD_CLASS}
                                value={view.quietStart}
                                disabled={disabled}
                                onChange={(event) => void setRules(RULES_QUIET, { quietStart: event.target.value })}
                            />
                            <span className="text-xs text-muted">{t("proactiveNotifications.quietTo")}</span>
                            <input
                                type="time"
                                aria-label={t("proactiveNotifications.quietEndLabel")}
                                className={FIELD_CLASS}
                                value={view.quietEnd}
                                disabled={disabled}
                                onChange={(event) => void setRules(RULES_QUIET, { quietEnd: event.target.value })}
                            />
                        </>
                    }
                />
                <NotificationRow
                    title={t("proactiveNotifications.budgetLabel")}
                    description={t("proactiveNotifications.budgetDescription")}
                    note={
                        view.digestTimes.length > 0
                            ? t("proactiveNotifications.digestTimes", { times: view.digestTimes.join(", ") })
                            : undefined
                    }
                    control={
                        <>
                            {state.busy === RULES_BUDGET && <Loader2 className="size-3.5 animate-spin" />}
                            <input
                                type="number"
                                aria-label={t("proactiveNotifications.budgetLabel")}
                                min={0}
                                max={BUDGET_MAX}
                                step={1}
                                className={`${FIELD_CLASS} w-14`}
                                value={budgetDraft ?? String(view.dailyInterruptBudget)}
                                disabled={disabled}
                                onChange={(event) => setBudgetDraft(event.target.value)}
                                onBlur={saveBudget}
                                onKeyDown={(event) => {
                                    if (event.key === "Enter") saveBudget();
                                }}
                            />
                        </>
                    }
                />
            </SettingsCard>
        </div>
    );
}
