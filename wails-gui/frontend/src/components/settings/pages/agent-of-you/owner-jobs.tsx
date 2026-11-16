// Jobs (o13), one job (o14 to o20) and Blocked clients (o25). A decision names
// the pass and its version; a stale one (409) comes back with the current
// state, and the page shows that state instead.
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { FileText, MoreHorizontal } from "lucide-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Textarea } from "@/components/ui/textarea";
import { SettingsCard } from "@/components/settings/settings-primitives";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import {
    agentOwnerAction,
    decideAgentOwnerJob,
    downloadAgentOwnerReviewFile,
    type AgentOwnerDecisionInput,
    type AgentOwnerJob,
    type AgentOwnerPass,
} from "@/lib/agent-owner";
import {
    clientName,
    effortLabel,
    formatDay,
    formatDeadline,
    formatWhen,
    minsText,
    money,
    secondsToMinutes,
    setAoyHeader,
    speedLabel,
    splitFor,
    useOwnerSnapshot,
    type AoyCrumb,
} from "./aoy-shared";
import { AOY_LABEL, AOY_LINE, AOY_LINK, AoyDialog, AoyNavRow, AoyPlaceholder, AoyWaiting } from "./aoy-ui";

const OPEN_STATES = new Set(["waiting_owner", "review", "working", "checking", "delivering", "holding", "quote_ready", "waiting_client", "clarifying", "queued", "running"]);
const ASKING_STATES = new Set(["waiting_client", "clarifying", "quote_ready", "holding", "queued"]);

function lastPass(job: AgentOwnerJob): AgentOwnerPass | undefined {
    return job.passes?.[job.passes.length - 1];
}
/** The approval card: the server's, or the waiting pass when the server sends only the passes. */
function approvalCard(job: AgentOwnerJob): { passId: string; decisionVersion?: number; deadline: string } | undefined {
    if (job.approval) return job.approval;
    const pass = [...(job.passes ?? [])].reverse().find((item) => item.state === "waiting_owner");
    return pass ? { passId: pass.id, decisionVersion: pass.decisionVersion, deadline: "" } : undefined;
}
function workedMinutes(job: AgentOwnerJob): number | null {
    const receipt = [...(job.messages ?? [])].reverse().find((message) => message.receipt)?.receipt;
    if (receipt) return secondsToMinutes(receipt.workedSeconds);
    if (job.review?.workedSeconds != null) return secondsToMinutes(job.review.workedSeconds);
    return null;
}

/** The status line of a job row, from what the server gave (missing parts fall back to plain words). */
export function jobStatus(t: TFunction<"settings">, job: AgentOwnerJob, language: string): { text: string; waiting: boolean } {
    const split = lastPass(job)?.split;
    const minutes = workedMinutes(job);
    switch (job.state) {
        case "waiting_owner":
            return { text: job.approval?.deadline ? t("aoy.jNeeds", { time: formatDeadline(job.approval.deadline, language) }) : t("aoy.apT"), waiting: true };
        case "review":
            return { text: job.review?.deadline ? t("aoy.jReview", { time: formatDeadline(job.review.deadline, language) }) : t("aoy.rvT"), waiting: true };
        case "working":
        case "checking":
        case "delivering":
        case "running": {
            const elapsed = minutes ?? Math.max(1, Math.round((Date.now() - new Date(job.updatedAt).getTime()) / 60000));
            return { text: t("aoy.jWorking", { min: elapsed, mins: minsText(t, elapsed) }), waiting: false };
        }
        case "delivered":
            if (split && minutes != null) return { text: t("aoy.jDelivered", { min: minutes, mins: minsText(t, minutes), amt: money(split.ownerShareCents) }), waiting: false };
            return { text: t("aoy.jDone"), waiting: false };
        case "declined":
            return { text: t("aoy.jDeclined"), waiting: false };
        case "expired":
            if (split && split.ownerShareCents < 0) return { text: t("aoy.jExpired", { amt: money(split.ownerShareCents) }), waiting: false };
            return { text: t("aoy.jNoStart"), waiting: false };
        case "stopped":
            if (split) return { text: t("aoy.jStopped", { amt: money(split.ownerShareCents) }), waiting: false };
            return { text: t("aoy.jDone"), waiting: false };
        default:
            if (ASKING_STATES.has(job.state)) return { text: t("aoy.jAsking"), waiting: false };
            return { text: OPEN_STATES.has(job.state) ? t("aoy.jOpen") : t("aoy.jDone"), waiting: false };
    }
}

export function JobsView({ openJob }: { openJob: (id: string) => void }) {
    const { t, i18n } = useTranslation("settings");
    const jobs = useOwnerSnapshot("jobs");
    if (!jobs.loaded) return <AoyPlaceholder rows={4} testId="aoy-jobs-placeholder" />;
    const list = jobs.data ?? [];
    if (list.length === 0) return <p className="settings-card-desc" data-testid="aoy-jobs-empty">{t("aoy.noJobs")}</p>;
    const open = list.filter((job) => OPEN_STATES.has(job.state));
    const done = list.filter((job) => !OPEN_STATES.has(job.state));
    const group = (title: string, items: AgentOwnerJob[]) =>
        items.length > 0 && (
            <section className="flex flex-col gap-2">
                <h2 className="settings-section-heading">{title}</h2>
                <div className="settings-card px-5 py-1">
                    {items.map((job) => {
                        const status = jobStatus(t, job, i18n.language);
                        return (
                            <AoyNavRow
                                key={job.id}
                                testId={`aoy-job-${job.id}`}
                                title={job.title || t("aoy.jobs")}
                                value={
                                    <>
                                        {clientName(t, job)} · <span className={status.waiting ? "font-semibold text-copper" : undefined}>{status.text}</span>
                                    </>
                                }
                                onClick={() => openJob(job.id)}
                            />
                        );
                    })}
                </div>
            </section>
        );
    return (
        <div className="flex flex-col gap-5" data-testid="aoy-jobs">
            {group(t("aoy.jOpen"), open)}
            {group(t("aoy.jDone"), done)}
        </div>
    );
}

type Dialog = "decline" | "declineReview" | "block" | null;

export function JobView({ id, crumbs, onBlocked }: { id: string; crumbs: AoyCrumb[]; onBlocked: () => void }) {
    const { t, i18n } = useTranslation("settings");
    const lang = i18n.language;
    const snapshot = useOwnerSnapshot("job", id);
    const speeds = useOwnerSnapshot("speeds");
    const failure = useSettingsFailure();
    const [busy, setBusy] = useState(false);
    const [dialog, setDialog] = useState<Dialog>(null);
    const [note, setNote] = useState("");
    const [redo, setRedo] = useState(false);
    const [redoNote, setRedoNote] = useState("");
    const [stale, setStale] = useState(false);
    const job = snapshot.data;
    const client = clientName(t, job);

    useEffect(() => {
        if (!job) return;
        setAoyHeader({
            crumbs,
            title: job.title || t("aoy.jobs"),
            trailing: job.clientId ? (
                <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                        <Button variant="quiet" size="icon" aria-label={t("aoy.block", { client })}>
                            <MoreHorizontal className="h-4 w-4" />
                        </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                        <DropdownMenuItem onSelect={() => setDialog("block")}>{t("aoy.block", { client })}</DropdownMenuItem>
                    </DropdownMenuContent>
                </DropdownMenu>
            ) : undefined,
        });
        // The crumbs array is rebuilt by the parent each render; the title and client are what change.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [job?.title, job?.clientId, client, t]);

    if (!snapshot.loaded) return <AoyPlaceholder rows={4} testId="aoy-job-placeholder" />;
    if (!job) return <p className="settings-card-desc">{t("aoy.noJobs")}</p>;

    const decide = async (action: AgentOwnerDecisionInput["action"], text = "") => {
        const card = action === "accept" || (action === "decline" && job.state === "waiting_owner") ? approvalCard(job) : job.review;
        if (!card?.passId || card.decisionVersion == null) {
            setStale(true);
            snapshot.reload();
            return;
        }
        failure.clear();
        setBusy(true);
        try {
            const result = await decideAgentOwnerJob({ action, jobId: job.id, passId: card.passId, decisionVersion: card.decisionVersion, note: text || undefined });
            if (!result.accepted) setStale(true);
            setDialog(null);
            setRedo(false);
            snapshot.reload();
        } catch {
            failure.fail(t("overview.changeFailed"), () => void decide(action, text));
        } finally {
            setBusy(false);
        }
    };
    const block = async () => {
        if (!job.clientId) return;
        failure.clear();
        setBusy(true);
        try {
            await agentOwnerAction("block", job.clientId);
            setDialog(null);
            onBlocked();
        } catch {
            failure.fail(t("overview.changeFailed"), () => void block());
        } finally {
            setBusy(false);
        }
    };

    const messages = job.messages ?? [];
    const asked = messages.find((message) => message.kind === "client")?.text ?? "";
    const plan = [...messages].reverse().find((message) => message.deliverable);
    const agentNote = [...messages].reverse().find((message) => message.kind === "agent" || message.kind === "delivery")?.text ?? "";
    const pass = lastPass(job);
    const split = pass?.split ?? null;
    const minutes = workedMinutes(job);
    const reviewCard = job.state === "review" ? job.review : undefined;
    const approval = job.state === "waiting_owner" ? approvalCard(job) : undefined;
    const rate = pass?.rateCentsPerMinute ?? plan?.rateCentsPerMinute ?? 0;
    const cost = reviewCard ? (minutes ?? 0) * rate : 0;
    const status = jobStatus(t, job, lang);
    const typical = (speeds.data ?? []).find((item) => item.speed === pass?.speed && item.effort === pass?.effort)?.typicalCentsPerMinute ?? 0;
    const modelCost = split?.modelCostCents ?? (reviewCard && typical ? (minutes ?? 0) * typical : undefined);
    const earnAbout = split?.ownerShareCents ?? splitFor(cost, modelCost ?? 0).keep;
    const extra = (approval ?? {}) as { limitCents?: number; estimatedMinutes?: number };
    const estimate = extra.estimatedMinutes ?? plan?.estimatedMinutes;

    return (
        <div className="flex flex-col gap-4" data-testid={`aoy-job-view-${job.state}`}>
            {status.waiting ? <AoyWaiting>{status.text}</AoyWaiting> : <p className="text-sm text-muted">{job.state === "delivered" ? t("aoy.sent", { time: formatWhen(job.updatedAt, lang) }) : status.text}</p>}
            {stale && <p className={AOY_LINE} role="status">{status.text}</p>}

            {approval && (
                <div className="settings-card settings-card--padded flex flex-col gap-3" data-testid="aoy-approval-card">
                    <h2 className="settings-card-title">{t("aoy.apT")}</h2>
                    {asked && (
                        <div className="flex flex-col gap-1">
                            <span className="text-[11px] font-semibold tracking-wide text-muted">{t("aoy.apAsk", { client })}</span>
                            <p className="text-sm text-ink">{asked}</p>
                        </div>
                    )}
                    {plan?.deliverable && (
                        <div className="flex flex-col gap-1">
                            <span className={AOY_LABEL}>{t("aoy.apPlan")}</span>
                            <p className="text-sm text-ink">{plan.deliverable}</p>
                        </div>
                    )}
                    {estimate != null && extra.limitCents != null && (
                        <p className="text-sm text-muted">
                            {t("aoy.apFacts", { mins: minsText(t, estimate), limit: money(extra.limitCents), amt: money(splitFor(estimate * rate, estimate * typical).keep) })}
                        </p>
                    )}
                    {approval.deadline && <p className={AOY_LINE}>{t("aoy.apBy", { time: formatDeadline(approval.deadline, lang), client })}</p>}
                    <div className="flex gap-2">
                        <Button disabled={busy} onClick={() => void decide("accept")}>{t("aoy.accept")}</Button>
                        <Button variant="ghost" disabled={busy} onClick={() => setDialog("decline")}>{t("aoy.decline")}</Button>
                    </div>
                </div>
            )}

            {reviewCard && (
                <>
                    <div className="settings-card settings-card--padded flex flex-col gap-3" data-testid="aoy-review-card">
                        <span className={AOY_LABEL}>{t("aoy.rvNote")}</span>
                        {agentNote && <p className="text-sm text-ink">{agentNote}</p>}
                        {reviewCard.files.map((file) => (
                            <div key={file.path} className="flex items-center justify-between gap-3 rounded-lg border border-border px-3 py-2">
                                <span className="flex min-w-0 items-center gap-2">
                                    <FileText className="h-4 w-4 shrink-0 text-muted" aria-hidden />
                                    <span className="truncate text-sm font-semibold text-ink">{file.path.split("/").pop()}</span>
                                    <span className="text-xs text-muted">{Math.max(1, Math.round(file.size / 1024))} KB</span>
                                </span>
                                <button type="button" className={`text-sm font-medium ${AOY_LINK}`} onClick={() => void downloadAgentOwnerReviewFile(job.id, file.path).catch(() => failure.fail(t("overview.changeFailed")))}>
                                    {t("aoy.save")}
                                </button>
                            </div>
                        ))}
                    </div>
                    {minutes != null && (
                        <p className="text-sm text-muted">{t("aoy.rvFacts", { mins: minsText(t, minutes), client, cost: money(cost), amt: money(earnAbout) })}</p>
                    )}
                    {modelCost != null && <p className={AOY_LINE}>{t("aoy.rvBy", { time: formatDeadline(reviewCard.deadline, lang), client, model: money(modelCost) })}</p>}
                    {redo ? (
                        <div className="flex flex-col gap-2" data-testid="aoy-redo">
                            <label className="settings-row-title" htmlFor="aoy-redo-note">{t("aoy.rdT")}</label>
                            <Textarea id="aoy-redo-note" value={redoNote} onChange={(event) => setRedoNote(event.target.value)} rows={3} />
                            {reviewCard.minutesLeft != null && reviewCard.centsLeft != null && (
                                <p className={AOY_LINE}>{t("aoy.rdL", { mins: minsText(t, reviewCard.minutesLeft), amt: money(reviewCard.centsLeft), client })}</p>
                            )}
                            <div className="flex gap-2">
                                <Button disabled={busy || !redoNote.trim()} onClick={() => void decide("redo", redoNote.trim())}>{t("aoy.rdBtn")}</Button>
                                <Button variant="ghost" onClick={() => setRedo(false)}>{t("aoy.cancel")}</Button>
                            </div>
                        </div>
                    ) : (
                        <div className="flex flex-col gap-2">
                            <div className="flex items-center gap-2">
                                <Button disabled={busy} onClick={() => void decide("send")}>{t("aoy.send")}</Button>
                                {reviewCard.canRedo !== false && <Button variant="ghost" disabled={busy} onClick={() => setRedo(true)}>{t("aoy.redo")}</Button>}
                                <Button variant="quiet" disabled={busy} onClick={() => setDialog("declineReview")}>{t("aoy.decline")}</Button>
                            </div>
                            {reviewCard.canRedo === false && <p className={AOY_LINE} data-testid="aoy-redo-none">{t("aoy.rdNone", { client })}</p>}
                        </div>
                    )}
                </>
            )}

            {split && !reviewCard && !approval && (
                <SettingsCard title={t("aoy.split")}>
                    <div className="grid grid-cols-[1fr_auto] gap-y-1.5 text-sm tabular-nums" data-testid="aoy-split">
                        <span className="text-ink">{t("aoy.spClient", { client })}</span>
                        <span className="text-right text-ink">${money(split.clientChargeCents)}</span>
                        <span className="text-muted">{t("aoy.spModel")}</span>
                        <span className="text-right text-muted">−${money(split.modelCostCents)}</span>
                        <span className="text-muted">{t("aoy.spFee")}</span>
                        <span className="text-right text-muted">−${money(split.cardFeeCents)}</span>
                        <span className="text-muted">{t("aoy.spIso")}</span>
                        <span className="text-right text-muted">−${money(split.isoformCents)}</span>
                        <span className="border-t border-border pt-2 font-semibold text-ink">{t("aoy.spYou")}</span>
                        <span className="border-t border-border pt-2 text-right font-semibold text-ink">{split.ownerShareCents < 0 ? "−" : ""}${money(split.ownerShareCents)}</span>
                    </div>
                    {pass && (
                        <p className={`mt-3 ${AOY_LINE}`}>
                            {t("aoy.profV", { v: pass.profileVersion, speed: speedLabel(t, pass.speed), effort: effortLabel(t, pass.effort), rate: money(pass.rateCentsPerMinute) })}
                        </p>
                    )}
                </SettingsCard>
            )}
            <SettingsFailureLine failure={failure.failure} />

            <AoyDialog
                open={dialog === "decline"}
                onOpenChange={(open) => !open && setDialog(null)}
                title={t("aoy.dcT")}
                description={t("aoy.dcL", { client })}
                testId="aoy-decline-dialog"
                footer={
                    <>
                        <Button variant="ghost" onClick={() => setDialog(null)}>{t("aoy.cancel")}</Button>
                        <Button disabled={busy} onClick={() => void decide("decline", note.trim())}>{t("aoy.decline")}</Button>
                    </>
                }
            >
                <Textarea aria-label={t("aoy.dcPh", { client })} placeholder={t("aoy.dcPh", { client })} value={note} onChange={(event) => setNote(event.target.value)} rows={3} />
            </AoyDialog>
            <AoyDialog
                open={dialog === "declineReview"}
                onOpenChange={(open) => !open && setDialog(null)}
                title={t("aoy.dvT")}
                description={t("aoy.dvL", { client, cost: money(cost), mins: minsText(t, minutes ?? 0), amt: money(earnAbout) })}
                testId="aoy-decline-review-dialog"
                footer={
                    <>
                        <Button variant="ghost" onClick={() => setDialog(null)}>{t("aoy.cancel")}</Button>
                        <Button disabled={busy} onClick={() => void decide("decline")}>{t("aoy.dvBtn")}</Button>
                    </>
                }
            />
            <AoyDialog
                open={dialog === "block"}
                onOpenChange={(open) => !open && setDialog(null)}
                title={t("aoy.blT", { client })}
                description={t("aoy.blL")}
                testId="aoy-block-dialog"
                footer={
                    <>
                        <Button variant="ghost" onClick={() => setDialog(null)}>{t("aoy.cancel")}</Button>
                        <Button disabled={busy} onClick={() => void block()}>{t("aoy.blBtn")}</Button>
                    </>
                }
            />
        </div>
    );
}

export function BlockedView() {
    const { t, i18n } = useTranslation("settings");
    const blocked = useOwnerSnapshot("blocked");
    const failure = useSettingsFailure();
    const [busy, setBusy] = useState<string | null>(null);
    if (!blocked.loaded) return <AoyPlaceholder rows={2} />;
    const list = blocked.data ?? [];
    if (list.length === 0) return <p className="settings-card-desc" data-testid="aoy-blocked-empty">{t("aoy.blNone")}</p>;
    const unblock = async (id: string) => {
        failure.clear();
        setBusy(id);
        try {
            await agentOwnerAction("unblock", id);
            blocked.reload();
        } catch {
            failure.fail(t("overview.changeFailed"), () => void unblock(id));
        } finally {
            setBusy(null);
        }
    };
    return (
        <div className="flex flex-col gap-2" data-testid="aoy-blocked">
            <SettingsCard>
                {list.map((client) => (
                    <div key={client.clientId} className="settings-row">
                        <div className="flex min-w-0 flex-col">
                            <span className="settings-row-title">{client.clientDisplayName?.trim() || t("aoy.noName")}</span>
                            {client.blockedAt && <span className="settings-row-desc">{t("aoy.blSince", { when: formatDay(client.blockedAt, i18n.language) })}</span>}
                        </div>
                        <Button size="sm" variant="ghost" disabled={busy === client.clientId} onClick={() => void unblock(client.clientId)}>{t("aoy.unblock")}</Button>
                    </div>
                ))}
            </SettingsCard>
            <SettingsFailureLine failure={failure.failure} />
        </div>
    );
}
