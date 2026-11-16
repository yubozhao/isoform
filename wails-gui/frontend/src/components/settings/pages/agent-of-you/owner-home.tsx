// The owner's Agent of You page once Bo approves: the four steps and the
// optional fifth (o03), then On (o10) and Paused (o11) with the switch, Needs
// you, three figures and six rows. Pause asks only when jobs wait (o28).
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Ban, BookOpen, Check, Clock, Gauge, Landmark, ListChecks, Plus, Shield } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import { NativeClipboardSetText, OpenExternalURL } from "@/lib/wails-shim-app";
import {
    agentOwnerAction,
    agreeToAgentOwnerPaymentTerms,
    openAgentOwnerPayoutOnboarding,
    type AgentOwnerJob,
    type AgentOwnerStatus,
} from "@/lib/agent-owner";
import {
    AOY_FULL_TERMS_URL,
    AOY_MINIMUM_CHARGE_CENTS,
    AOY_PAGE_PREFIX,
    AOY_TERMS_VERSION,
    clientName,
    effortLabel,
    formatDay,
    formatDeadline,
    formatWhen,
    minsText,
    money,
    secondsToMinutes,
    speedLabel,
    useOwnerSnapshot,
    useWorkProfile,
} from "./aoy-shared";
import { AOY_LINK, AoyDialog, AoyNavRow, AoyTile } from "./aoy-ui";
import type { AoyView } from "./agent-of-you-page";

const PAUSED_AT_KEY = "aoy.pausedAt";
const READ_PAID_KEY = "aoy.readHowPaid";

function readLocal(key: string): string {
    try {
        return window.localStorage.getItem(key) ?? "";
    } catch {
        return "";
    }
}
function writeLocal(key: string, value: string) {
    try {
        if (value) window.localStorage.setItem(key, value);
        else window.localStorage.removeItem(key);
    } catch {
        // A per-computer convenience only.
    }
}

/** How you're paid (o09): four paragraphs, then Agree and turn on. */
export function HowPaidDialog({ open, onOpenChange, canTurnOn, onAgree, busy }: { open: boolean; onOpenChange: (open: boolean) => void; canTurnOn: boolean; onAgree: () => void; busy: boolean }) {
    const { t } = useTranslation("settings");
    return (
        <AoyDialog
            open={open}
            onOpenChange={onOpenChange}
            title={t("aoy.payT")}
            testId="aoy-how-paid"
            footer={
                <>
                    <Button variant="ghost" onClick={() => onOpenChange(false)}>{t("aoy.notNow")}</Button>
                    {canTurnOn && <Button disabled={busy} onClick={onAgree}>{t("aoy.payAgree")}</Button>}
                </>
            }
        >
            <p>{t("aoy.pay1", { min: money(AOY_MINIMUM_CHARGE_CENTS) })}</p>
            <p>{t("aoy.pay2")}</p>
            <p>{t("aoy.pay3")}</p>
            <p>{t("aoy.pay4")}</p>
            {AOY_FULL_TERMS_URL && (
                <button type="button" className={`text-sm ${AOY_LINK}`} onClick={() => void OpenExternalURL(AOY_FULL_TERMS_URL)}>{t("aoy.fullTerms")}</button>
            )}
        </AoyDialog>
    );
}

/** Before Stripe (o22): three paragraphs, then Agree and continue to Stripe, which opens the browser. */
export function PayoutTermsDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
    const { t } = useTranslation("settings");
    const failure = useSettingsFailure();
    const [busy, setBusy] = useState(false);
    const go = async () => {
        failure.clear();
        setBusy(true);
        try {
            await openAgentOwnerPayoutOnboarding(AOY_TERMS_VERSION);
            onOpenChange(false);
        } catch {
            failure.fail(t("yourAgent.browserFailed"), () => void go());
        } finally {
            setBusy(false);
        }
    };
    return (
        <AoyDialog
            open={open}
            onOpenChange={onOpenChange}
            title={t("aoy.ptT")}
            testId="aoy-payout-terms"
            footer={
                <>
                    <Button variant="ghost" onClick={() => onOpenChange(false)}>{t("aoy.notNow")}</Button>
                    <Button disabled={busy} onClick={() => void go()}>{t("aoy.ptBtn")}</Button>
                </>
            }
        >
            <p>{t("aoy.pt1")}</p>
            <p>{t("aoy.pt2")}</p>
            <p>{t("aoy.pt3")}</p>
            {AOY_FULL_TERMS_URL && (
                <button type="button" className={`text-sm ${AOY_LINK}`} onClick={() => void OpenExternalURL(AOY_FULL_TERMS_URL)}>{t("aoy.fullTerms")}</button>
            )}
            <SettingsFailureLine failure={failure.failure} />
        </AoyDialog>
    );
}

function StepRow({ n, title, line, done, control }: { n: number | "plus"; title: string; line: string; done?: boolean; control: React.ReactNode }) {
    return (
        <div className="settings-row items-start" data-testid={`aoy-step-${n}`}>
            <div className="flex min-w-0 items-start gap-3">
                <span aria-hidden className="mt-0.5 grid h-6 w-6 shrink-0 place-items-center rounded-full bg-accent/10 text-xs font-semibold text-accent">
                    {done ? <Check className="h-3.5 w-3.5" /> : n === "plus" ? <Plus className="h-3.5 w-3.5" /> : n}
                </span>
                <div className="flex min-w-0 flex-col">
                    <span className="settings-row-title">{title}</span>
                    <span className="settings-row-desc">{line}</span>
                </div>
            </div>
            <div className="shrink-0">{control}</div>
        </div>
    );
}

export function OwnerHome({ status, open }: { status: AgentOwnerStatus; open: (view: AoyView) => void }) {
    const { t, i18n } = useTranslation("settings");
    const lang = i18n.language;
    const jobs = useOwnerSnapshot("jobs");
    const balance = useOwnerSnapshot("balance");
    const payouts = useOwnerSnapshot("payouts");
    const blocked = useOwnerSnapshot("blocked");
    const profile = useWorkProfile();
    const failure = useSettingsFailure();
    const [busy, setBusy] = useState(false);
    const [paidOpen, setPaidOpen] = useState(false);
    const [pauseOpen, setPauseOpen] = useState(false);
    const [payoutOpen, setPayoutOpen] = useState(false);
    const [copied, setCopied] = useState(false);
    const [readPaid, setReadPaid] = useState(() => readLocal(READ_PAID_KEY) === "1");

    const name = status.name;
    const settings = status.settings;
    const agreed = !!status.paymentAgreement;
    const step1 = !!status.profile;
    const step2 = settings.rateCentsPerMinute != null;
    const step3 = agreed || readPaid;
    const payoutStatus = payouts.data?.status ?? status.payout?.status ?? "not_started";
    const payoutsDone = payoutStatus === "enabled";

    const run = async (work: () => Promise<unknown>, retry: () => void) => {
        failure.clear();
        setBusy(true);
        try {
            await work();
        } catch {
            failure.fail(t("overview.changeFailed"), retry);
        } finally {
            setBusy(false);
        }
    };

    const turnOn = () => run(async () => {
        if (!agreed) await agreeToAgentOwnerPaymentTerms(AOY_TERMS_VERSION);
        await agentOwnerAction("on");
        writeLocal(PAUSED_AT_KEY, "");
        setPaidOpen(false);
    }, () => void turnOn());
    const pause = () => run(async () => {
        await agentOwnerAction("pause");
        writeLocal(PAUSED_AT_KEY, new Date().toISOString());
        setPauseOpen(false);
    }, () => void pause());

    const waiting = (jobs.data ?? []).filter((job) => job.state === "waiting_owner");
    const onSwitch = (checked: boolean) => {
        if (checked) {
            if (!agreed) setPaidOpen(true);
            else void turnOn();
            return;
        }
        if (waiting.length > 0) setPauseOpen(true);
        else void pause();
    };

    const closePaid = (next: boolean) => {
        setPaidOpen(next);
        if (!next && !readPaid) {
            setReadPaid(true);
            writeLocal(READ_PAID_KEY, "1");
        }
    };

    const dialogs = (
        <>
            <HowPaidDialog open={paidOpen} onOpenChange={closePaid} canTurnOn={step1 && step2} busy={busy} onAgree={() => void turnOn()} />
            <PayoutTermsDialog open={payoutOpen} onOpenChange={setPayoutOpen} />
            <AoyDialog
                open={pauseOpen}
                onOpenChange={setPauseOpen}
                title={t("aoy.pauseT")}
                description={t("aoy.pauseL")}
                testId="aoy-pause-dialog"
                footer={
                    <>
                        <Button variant="ghost" onClick={() => setPauseOpen(false)}>{t("aoy.cancel")}</Button>
                        <Button disabled={busy} onClick={() => void pause()}>{t("aoy.pause")}</Button>
                    </>
                }
            />
        </>
    );

    if (status.state === "setting_up") {
        return (
            <div className="flex flex-col gap-4" data-testid="aoy-setup">
                <div className="flex flex-col gap-1">
                    <h2 className="settings-section-heading">{t("aoy.inT")}</h2>
                    <p className="settings-card-desc">{t("aoy.inL", { name })}</p>
                </div>
                <div className="settings-card px-5 py-1">
                    <StepRow
                        n={1}
                        done={step1}
                        title={t("aoy.st1")}
                        line={step1 ? t("aoy.st1Done", { v: status.profile?.version, when: formatDay(status.profile?.approvedAt, lang) }) : t("aoy.st1L")}
                        control={<Button size="sm" variant={step1 ? "ghost" : "default"} onClick={() => open({ kind: "profile" })}>{t("aoy.review")}</Button>}
                    />
                    <StepRow
                        n={2}
                        done={step2}
                        title={t("aoy.st2")}
                        line={step2 ? t("aoy.st2Done", { rate: money(settings.rateCentsPerMinute), speed: speedLabel(t, settings.speed), effort: effortLabel(t, settings.effort) }) : t("aoy.st2L")}
                        control={<Button size="sm" variant={step1 && !step2 ? "default" : "ghost"} onClick={() => open({ kind: "rate" })}>{t("aoy.set")}</Button>}
                    />
                    <StepRow
                        n={3}
                        done={step3}
                        title={t("aoy.st3")}
                        line={t("aoy.st3L")}
                        control={<Button size="sm" variant={step1 && step2 && !step3 ? "default" : "ghost"} onClick={() => setPaidOpen(true)}>{t("aoy.read")}</Button>}
                    />
                    <StepRow
                        n={4}
                        title={t("aoy.st4")}
                        line={t("aoy.st4L")}
                        control={<Switch aria-label={t("aoy.st4")} checked={false} disabled={!(step1 && step2 && step3) || busy} onCheckedChange={onSwitch} />}
                    />
                    <SettingsFailureLine failure={failure.failure} />
                </div>
                {!payoutsDone && (
                    <div className="settings-card px-5 py-1">
                        <StepRow n="plus" title={t("aoy.stPay")} line={t("aoy.stPayL")} control={<Button size="sm" variant="ghost" onClick={() => setPayoutOpen(true)}>{t("aoy.setUp")}</Button>} />
                    </div>
                )}
                {dialogs}
            </div>
        );
    }

    const on = status.state === "on";
    const pausedAt = readLocal(PAUSED_AT_KEY);
    const needs = (jobs.data ?? []).filter((job) => job.state === "waiting_owner" || job.state === "review");
    const lines = balance.data?.lines ?? [];
    const month = new Date();
    const thisMonth = lines.filter((line) => {
        const at = new Date(line.createdAt);
        return at.getFullYear() === month.getFullYear() && at.getMonth() === month.getMonth();
    });
    const earned = thisMonth.reduce((sum, line) => sum + (line.ownerShareCents ?? 0), 0);
    const jobLines = thisMonth.filter((line) => line.passId);
    const jobCount = new Set(jobLines.map((line) => line.passId)).size;
    const minutes = (jobs.data ?? []).reduce((sum, job) => sum + secondsToMinutes(job.review?.workedSeconds), 0);
    const available = balance.data?.availableCents ?? balance.data?.balanceCents ?? 0;
    const draftReady = !!profile.view?.pending && (profile.view?.approvedVersion ?? 0) > 0;
    const blockedCount = blocked.data?.length ?? 0;

    return (
        <div className="flex flex-col gap-4" data-testid={on ? "aoy-on" : "aoy-paused"}>
            <div className="settings-card settings-card--padded">
                <div className="flex items-center justify-between gap-6">
                    <div className="flex min-w-0 flex-col gap-1">
                        <span className="settings-row-title">{t("aoy.takeJobs")}</span>
                        <span className="settings-row-desc">
                            {on ? t("aoy.onL", { name }) : pausedAt ? t("aoy.pausedL", { time: formatWhen(pausedAt, lang) }) : null}
                        </span>
                        <button
                            type="button"
                            className={`w-fit text-sm font-medium ${AOY_LINK}`}
                            onClick={() => {
                                NativeClipboardSetText(`https://${AOY_PAGE_PREFIX}${name}`)
                                    .then(() => {
                                        setCopied(true);
                                        window.setTimeout(() => setCopied(false), 1600);
                                    })
                                    .catch(() => failure.fail(t("yourAgent.copyLinkFailed")));
                            }}
                        >
                            {copied ? t("yourAgent.copiedLink") : t("aoy.copyLink")}
                        </button>
                    </div>
                    <Switch aria-label={t("aoy.takeJobs")} checked={on} disabled={busy} onCheckedChange={onSwitch} />
                </div>
                <SettingsFailureLine failure={failure.failure} />
            </div>

            {needs.length > 0 && (
                <div className="flex flex-col gap-2" data-testid="aoy-needs">
                    <h2 className="flex items-center gap-2 settings-section-heading">
                        <span aria-hidden className="h-2 w-2 rounded-full bg-copper" />
                        {t("aoy.needs")}
                    </h2>
                    <div className="settings-card px-5 py-1">
                        {needs.map((job) => (
                            <NeedRow key={job.id} job={job} onOpen={() => open({ kind: "job", id: job.id })} />
                        ))}
                    </div>
                </div>
            )}

            <div className="grid grid-cols-3 gap-3">
                <AoyTile value={`$${money(earned)}`} label={t("aoy.mEarned")} />
                <AoyTile value={String(jobCount)} label={t("aoy.mJobs", { mins: minsText(t, minutes) })} />
                <AoyTile value={`$${money(available)}`} label={balance.data?.availableCents != null ? t("aoy.balAvail") : t("aoy.balYours")} />
            </div>

            <div className="settings-card px-5 py-1">
                <AoyNavRow
                    icon={BookOpen}
                    testId="aoy-row-profile"
                    title={t("aoy.wp")}
                    tag={draftReady ? t("aoy.draftReady") : undefined}
                    value={status.profile ? t("aoy.st1Done", { v: status.profile.version, when: formatDay(status.profile.approvedAt, lang) }) : undefined}
                    onClick={() => open({ kind: "profile" })}
                />
                <AoyNavRow
                    icon={Gauge}
                    title={t("aoy.rse")}
                    value={settings.rateCentsPerMinute != null ? t("aoy.st2Done", { rate: money(settings.rateCentsPerMinute), speed: speedLabel(t, settings.speed), effort: effortLabel(t, settings.effort) }) : undefined}
                    onClick={() => open({ kind: "rate" })}
                />
                <AoyNavRow
                    icon={Shield}
                    title={t("aoy.opts")}
                    value={t("aoy.optsVal", { a: settings.approveJobs ? t("aoy.on") : t("aoy.off"), r: settings.reviewResults ? t("aoy.on") : t("aoy.off") })}
                    onClick={() => open({ kind: "opts" })}
                />
                <AoyNavRow icon={ListChecks} title={t("aoy.jobs")} onClick={() => open({ kind: "jobs" })} />
                <AoyNavRow icon={Landmark} title={t("aoy.bal")} onClick={() => open({ kind: "balance" })} />
                <AoyNavRow
                    icon={Ban}
                    title={t("aoy.blocked")}
                    trailing={blocked.loaded && blockedCount === 0 ? t("aoy.none") : blockedCount > 0 ? String(blockedCount) : undefined}
                    onClick={() => open({ kind: "blocked" })}
                />
            </div>
            {dialogs}
        </div>
    );
}

/** One Needs you row: the job's title, the client and what it waits on. */
function NeedRow({ job, onOpen }: { job: AgentOwnerJob; onOpen: () => void }) {
    const { t, i18n } = useTranslation("settings");
    const review = job.state === "review";
    const deadline = (review ? job.review?.deadline : job.approval?.deadline) ?? "";
    const status = deadline
        ? t(review ? "aoy.jReview" : "aoy.jNeeds", { time: formatDeadline(deadline, i18n.language) })
        : t(review ? "aoy.rvT" : "aoy.apT");
    return (
        <AoyNavRow
            icon={Clock}
            testId={`aoy-need-${job.id}`}
            title={job.title || t(review ? "aoy.rvT" : "aoy.apT")}
            value={
                <>
                    {clientName(t, job)} · <span className="font-semibold text-copper">{status}</span>
                </>
            }
            onClick={onOpen}
        />
    );
}
