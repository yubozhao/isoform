// The work profile review (o04, o05, o06, o26). Three groups in order: What
// it's best at, How you do {specialty}, In all your work. Every line has Edit
// and Remove (with Undo); each group has Add a line. Preview what's sent shows
// exactly the lines and names that leave; Approve sends only those.
import { useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Check, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
    addWorkProfileLine,
    approveWorkProfile,
    buildWorkProfileDraft,
    discardWorkProfileDraft,
    editWorkProfileLine,
    keepWorkProfileLine,
    previewWorkProfileUpload,
    renameWorkProfileSpecialty,
    setWorkProfileLineRemoved,
    setWorkProfileSpecialties,
    type WorkProfileDraft,
    type WorkProfileLine,
    type WorkProfileUpload,
    type WorkProfileView as WorkProfileData,
} from "@/lib/work-profile";
import { formatWhen, useWorkProfile } from "./aoy-shared";
import { AOY_FIELD, AOY_LINE, AOY_LINK, AoyChip, AoyDialog, AoyPlaceholder } from "./aoy-ui";

const DAY_MS = 24 * 60 * 60 * 1000;
const MAX_SPECIALTIES = 3;

/** Splits a translated sentence around one {{chip}} so the chip keeps its own element. */
function withChip(sentence: string, chip: ReactNode): ReactNode {
    const marker = "\u0000";
    const [before, after = ""] = sentence.replace("{{chip}}", marker).split(marker);
    return (
        <>
            {before}
            {chip}
            {after}
        </>
    );
}

/** "Couldn't send your profile. Try again": the sentence, then the link (Try again / 重试). */
function retrySplit(sentence: string): [string, string] {
    const match = /^(.*[.。])\s*(.+)$/.exec(sentence);
    return match ? [match[1], match[2]] : [sentence, ""];
}

type Problem = { key: "sendFail" | "newer" | "needName" | "tooLong" | "draftFail"; n?: number } | null;

function problemOf(error: unknown): Problem {
    const text = error instanceof Error ? error.message : String(error ?? "");
    if (/name/i.test(text) && /confirm/i.test(text)) return { key: "needName" };
    const long = /too long[^0-9]*([0-9]+)/i.exec(text);
    if (long) return { key: "tooLong", n: Number(long[1]) };
    return { key: "sendFail" };
}

function InlineInput({ initial, label, onSave, onCancel }: { initial: string; label: string; onSave: (text: string) => void; onCancel: () => void }) {
    const [text, setText] = useState(initial);
    return (
        <input
            autoFocus
            aria-label={label}
            className={`${AOY_FIELD} h-8 w-full`}
            value={text}
            onChange={(event) => setText(event.target.value)}
            onBlur={() => (text.trim() && text.trim() !== initial ? onSave(text.trim()) : onCancel())}
            onKeyDown={(event) => {
                if (event.key === "Enter") (event.target as HTMLInputElement).blur();
                if (event.key === "Escape") {
                    event.preventDefault();
                    event.stopPropagation();
                    onCancel();
                }
            }}
        />
    );
}

export function WorkProfileView() {
    const { t, i18n } = useTranslation("settings");
    const lang = i18n.language;
    const profile = useWorkProfile();
    const [busy, setBusy] = useState(false);
    const [problem, setProblem] = useState<Problem>(null);
    const [preview, setPreview] = useState<WorkProfileUpload | null>(null);
    const [editing, setEditing] = useState<string | null>(null);
    const [adding, setAdding] = useState<string | null>(null);
    const [renaming, setRenaming] = useState<string | null>(null);
    const [shown, setShown] = useState<Set<string>>(new Set());

    const view = profile.view;
    if (!profile.loaded) return <AoyPlaceholder rows={5} testId="aoy-profile-placeholder" />;
    if (!view) return <p className="settings-card-desc">{t("aoy.wpLearning")}</p>;

    const draft: WorkProfileDraft | undefined = view.pending ?? undefined;
    const draftId = draft?.id ?? "";
    const firstRun = view.approvedVersion === 0;
    const weekly = !!draft && !firstRun;

    const act = async (work: () => Promise<WorkProfileData>) => {
        setBusy(true);
        setProblem(null);
        try {
            profile.set(await work());
        } catch (error) {
            setProblem(problemOf(error));
        } finally {
            setBusy(false);
        }
    };

    const openPreview = async () => {
        if (!draft) return;
        setBusy(true);
        setProblem(null);
        try {
            setPreview(await previewWorkProfileUpload(draftId));
        } catch (error) {
            setProblem(problemOf(error));
        } finally {
            setBusy(false);
        }
    };
    const approve = async (reviewed?: WorkProfileUpload) => {
        if (!draft) return;
        setBusy(true);
        setProblem(null);
        try {
            const upload = reviewed ?? (await previewWorkProfileUpload(draftId));
            const result = await approveWorkProfile(draftId, upload);
            profile.set(result.view);
            setPreview(null);
            if (result.conflictVersion != null) setProblem({ key: "newer" });
        } catch (error) {
            setProblem(problemOf(error));
        } finally {
            setBusy(false);
        }
    };

    const lastDraft = view.lastDraftAt ? new Date(view.lastDraftAt).getTime() : 0;
    const canDraftAt = lastDraft ? lastDraft + DAY_MS : 0;
    const draftLocked = canDraftAt > Date.now();
    const draftLine = (
        <p className={AOY_LINE} data-testid="aoy-draft-now">
            {!draft && view.nextDraftAt && <>{t("aoy.draftNext", { when: formatWhen(view.nextDraftAt, lang) })} · </>}
            {draftLocked ? (
                t("aoy.draftWait", { when: formatWhen(new Date(canDraftAt).toISOString(), lang) })
            ) : (
                <button type="button" className={AOY_LINK} disabled={busy || view.building} onClick={() => void act(buildWorkProfileDraft)}>
                    {t("aoy.draftNow")}
                </button>
            )}
        </p>
    );

    const source = draft ?? view.approved;
    const header = (
        <div className="flex flex-col gap-3">
            <p className="settings-card-desc">{t("aoy.wpSrc")}</p>
            {firstRun && draft && (
                <>
                    <p className="text-sm leading-relaxed text-ink">{t("aoy.wpFirst")}</p>
                    <p className={AOY_LINE}>{withChip(t("aoy.wpStrip"), <AoyChip>{t("aoy.noName").toLocaleLowerCase(lang)}</AoyChip>)}</p>
                </>
            )}
            {weekly && draft && (
                <div className="rounded-xl border border-accent/40 bg-panel px-4 py-3" data-testid="aoy-weekly-banner">
                    <p className="text-sm font-semibold text-ink">{t("aoy.wkT")}</p>
                    <p className="text-sm text-muted">
                        {t("aoy.wkL", {
                            a: draft.lines.filter((line) => line.status === "new" && !line.removed).length,
                            b: draft.lines.filter((line) => line.status === "changed" && !line.removed).length,
                            c: draft.lines.filter((line) => line.status === "suggested_removal" && !line.removed).length,
                            v: view.approvedVersion,
                        })}
                    </p>
                </div>
            )}
            {!draft && view.lastError && view.approvedVersion > 0 && <p className={AOY_LINE}>{t("aoy.draftFail", { v: view.approvedVersion })}</p>}
            {view.newerReady && <p className={AOY_LINE}>{t("aoy.newer")}</p>}
        </div>
    );

    // Still learning (o06): nothing drafted yet. One line of the owner's own turns the agent on.
    if (!source || (source.lines.length === 0 && source.specialties.length === 0)) {
        if (view.building) return <div className="flex flex-col gap-4">{header}<AoyPlaceholder rows={4} testId="aoy-profile-building" /></div>;
        return (
            <div className="flex flex-col gap-4" data-testid="aoy-profile-learning">
                <p className="settings-card-desc">{t("aoy.wpSrc")}</p>
                <p className="text-sm leading-relaxed text-ink">{t("aoy.wpLearning")}</p>
                <div className="settings-card settings-card--padded">
                    {adding === "" ? (
                        <InlineInput initial="" label={t("aoy.addLine")} onCancel={() => setAdding(null)} onSave={(text) => { setAdding(null); void act(() => addWorkProfileLine(draftId, "", text)); }} />
                    ) : (
                        <button type="button" className={`flex items-center gap-1 text-sm font-medium ${AOY_LINK}`} onClick={() => setAdding("")}>
                            <Plus className="h-4 w-4" aria-hidden />
                            {t("aoy.addLine")}
                        </button>
                    )}
                </div>
                {draftLine}
            </div>
        );
    }

    const editable = !!draft;
    const usable = (line: WorkProfileLine) => !line.removed && line.status !== "suggested_removal";
    const selected = source.specialties.filter((specialty) => specialty.selected);
    const withLines = source.specialties.filter((specialty) => source.lines.some((line) => line.group === specialty.id && usable(line)));
    const alsoSeen = source.specialties.filter((specialty) => !withLines.includes(specialty));
    const liveLines = source.lines.filter((line) => !line.removed);

    const lineRow = (line: WorkProfileLine) => {
        if (editing === line.id) {
            return (
                <div key={line.id} className="px-4 py-2.5">
                    <InlineInput initial={line.text} label={t("aoy.edit")} onCancel={() => setEditing(null)} onSave={(text) => { setEditing(null); void act(() => editWorkProfileLine(draftId, line.id, text)); }} />
                </div>
            );
        }
        return (
            <div key={line.id} className="group flex items-start justify-between gap-4 border-b border-border px-4 py-2.5 last:border-b-0 hover:bg-surface-hover/60" data-testid={`aoy-line-${line.id}`}>
                <div className="flex min-w-0 flex-col gap-1">
                    <p className={`text-sm ${line.removed ? "text-muted line-through" : "text-ink"}`}>
                        {weekly && !line.removed && line.status === "new" && <span className="mr-2 rounded-md bg-accent/10 px-1.5 py-0.5 text-xs font-semibold text-accent">{t("aoy.tagNew")}</span>}
                        {weekly && !line.removed && line.status === "changed" && <span className="mr-2 rounded-md bg-copper/10 px-1.5 py-0.5 text-xs font-semibold text-copper">{t("aoy.tagChanged")}</span>}
                        {line.text}
                    </p>
                    {weekly && line.status === "changed" && line.previous && <p className="text-xs text-muted line-through">{line.previous}</p>}
                    {weekly && line.status === "suggested_removal" && !line.removed && <span className="w-fit rounded-md bg-ink/[0.06] px-2 py-0.5 text-xs text-muted">{t("aoy.tagSuggest")}</span>}
                    {line.removed && <span className="text-xs text-muted">{t("aoy.removed")}</span>}
                </div>
                {editable && (
                    <div className="flex shrink-0 items-center gap-3 text-sm">
                        {line.removed ? (
                            <button type="button" className={AOY_LINK} disabled={busy} onClick={() => void act(() => setWorkProfileLineRemoved(draftId, line.id, false))}>{t("aoy.undo")}</button>
                        ) : (
                            <>
                                {weekly && line.status === "suggested_removal" ? (
                                    <button type="button" className={AOY_LINK} disabled={busy} onClick={() => void act(() => keepWorkProfileLine(draftId, line.id))}>{t("aoy.keep")}</button>
                                ) : (
                                    <button type="button" className={`${AOY_LINK} opacity-0 focus:opacity-100 group-hover:opacity-100`} disabled={busy} onClick={() => setEditing(line.id)}>{t("aoy.edit")}</button>
                                )}
                                <button type="button" className={`text-muted hover:text-ink ${weekly && line.status === "suggested_removal" ? "" : "opacity-0 focus:opacity-100 group-hover:opacity-100"}`} disabled={busy} onClick={() => void act(() => setWorkProfileLineRemoved(draftId, line.id, true))}>{t("aoy.remove")}</button>
                            </>
                        )}
                    </div>
                )}
            </div>
        );
    };

    const group = (key: string, title: string, lines: WorkProfileLine[]) => {
        const folded = weekly && !shown.has(key) ? lines.filter((line) => line.status === "unchanged" && !line.removed) : [];
        const visible = lines.filter((line) => !folded.includes(line));
        return (
            <section key={key} className="flex flex-col gap-2" data-testid={`aoy-group-${key || "all"}`}>
                <h2 className="settings-card-title">{title}</h2>
                <div className="settings-card overflow-hidden p-0">
                    {visible.map(lineRow)}
                    {folded.length > 0 && (
                        <button type="button" className="w-full px-4 py-2.5 text-left text-sm text-muted hover:text-ink" onClick={() => setShown(new Set([...shown, key]))}>
                            {t("aoy.unchanged", { n: folded.length })}
                        </button>
                    )}
                    {editable && (
                        <div className="border-t border-border px-4 py-2.5">
                            {adding === key ? (
                                <InlineInput initial="" label={t("aoy.addLine")} onCancel={() => setAdding(null)} onSave={(text) => { setAdding(null); void act(() => addWorkProfileLine(draftId, key, text)); }} />
                            ) : (
                                <button type="button" className={`flex items-center gap-1 text-sm font-medium ${AOY_LINK}`} disabled={busy} onClick={() => setAdding(key)}>
                                    <Plus className="h-4 w-4" aria-hidden />
                                    {t("aoy.addLine")}
                                </button>
                            )}
                        </div>
                    )}
                </div>
            </section>
        );
    };

    const toggle = (id: string) => {
        const ids = selected.map((specialty) => specialty.id);
        const next = ids.includes(id) ? ids.filter((other) => other !== id) : [...ids, id];
        if (next.length > MAX_SPECIALTIES) return;
        void act(() => setWorkProfileSpecialties(draftId, next));
    };

    const problemLine = problem && (
        <p className="text-sm text-copper" role="alert" data-testid="aoy-profile-problem">
            {problem.key === "sendFail" ? (
                <>
                    {retrySplit(t("aoy.sendFail"))[0]}{" "}
                    <button type="button" className={AOY_LINK} onClick={() => void approve(preview ?? undefined)}>{retrySplit(t("aoy.sendFail"))[1]}</button>
                </>
            ) : problem.key === "tooLong" ? (
                t("aoy.tooLong", { n: problem.n })
            ) : (
                t(`aoy.${problem.key}`, { v: view.approvedVersion })
            )}
        </p>
    );
    const noLines = !liveLines.some(usable);

    return (
        <div className="flex flex-col gap-5 pb-2" data-testid={weekly ? "aoy-profile-weekly" : firstRun ? "aoy-profile-first" : "aoy-profile-approved"}>
            {header}

            <section className="flex flex-col gap-2" data-testid="aoy-group-best">
                <h2 className="settings-card-title">{t("aoy.grpBest")}</h2>
                <p className="settings-card-desc">{t("aoy.grpBestL")}</p>
                <div className="flex flex-wrap gap-2">
                    {withLines.map((specialty) => (
                        <div key={specialty.id} className="flex items-center gap-2 rounded-xl border border-border bg-panel px-3 py-2">
                            {renaming === specialty.id ? (
                                <InlineInput initial={specialty.name} label={t("aoy.rename")} onCancel={() => setRenaming(null)} onSave={(name) => { setRenaming(null); void act(() => renameWorkProfileSpecialty(draftId, specialty.id, name)); }} />
                            ) : (
                                <>
                                    <button
                                        type="button"
                                        role="checkbox"
                                        aria-checked={specialty.selected}
                                        aria-label={specialty.name}
                                        disabled={!editable || busy || (!specialty.selected && selected.length >= MAX_SPECIALTIES)}
                                        onClick={() => toggle(specialty.id)}
                                        className={`grid h-4 w-4 place-items-center rounded-sm border ${specialty.selected ? "border-accent bg-accent text-solid-btn-fg" : "border-border-strong bg-panel"}`}
                                    >
                                        {specialty.selected && <Check className="h-3 w-3" aria-hidden />}
                                    </button>
                                    <span className="text-sm text-ink">{specialty.name}</span>
                                    {editable && <button type="button" className={`text-sm font-medium ${AOY_LINK}`} onClick={() => setRenaming(specialty.id)}>{t("aoy.rename")}</button>}
                                </>
                            )}
                        </div>
                    ))}
                </div>
                {alsoSeen.length > 0 && <p className={AOY_LINE}>{t("aoy.alsoSeen", { spec: alsoSeen.map((specialty) => specialty.name).join(lang.startsWith("zh") ? "、" : ", ") })}</p>}
            </section>

            {withLines.map((specialty) => group(specialty.id, t("aoy.grpHow", { spec: specialty.name }), source.lines.filter((line) => line.group === specialty.id)))}
            {group("", t("aoy.grpAll"), source.lines.filter((line) => !line.group || !source.specialties.some((specialty) => specialty.id === line.group)))}

            {!draft && draftLine}
            {problemLine}

            {draft && (
                <div className="sticky bottom-0 -mx-1 flex items-center justify-between gap-3 border-t border-border bg-background px-1 py-3" data-testid="aoy-profile-bar">
                    {weekly ? (
                        <Button variant="quiet" disabled={busy} onClick={() => void act(() => discardWorkProfileDraft(draftId))}>{t("aoy.discard")}</Button>
                    ) : (
                        <span className="text-sm text-muted">{noLines ? t("aoy.needLine") : t("aoy.wpCount", { n: liveLines.filter(usable).length })}</span>
                    )}
                    <div className="flex items-center gap-2">
                        <Button variant="ghost" disabled={busy} onClick={() => void openPreview()}>{t("aoy.preview")}</Button>
                        <Button disabled={busy || noLines} onClick={() => void approve()}>{weekly ? t("aoy.approveChanges") : t("aoy.approve")}</Button>
                    </div>
                </div>
            )}

            <AoyDialog
                open={preview !== null}
                onOpenChange={(open) => !open && setPreview(null)}
                title={t("aoy.pvT")}
                description={preview ? t("aoy.pvL", { n: preview.lines.length, s: preview.publicSpecialties.length }) : undefined}
                testId="aoy-preview"
                footer={
                    <>
                        <Button variant="ghost" onClick={() => setPreview(null)}>{t("aoy.back")}</Button>
                        <Button disabled={busy || (preview?.lines.length ?? 0) === 0} onClick={() => void approve(preview ?? undefined)}>{t("aoy.approveSend")}</Button>
                    </>
                }
            >
                {preview && (
                    <>
                        <ul className="flex max-h-[40vh] flex-col gap-1 overflow-y-auto rounded-xl bg-panel-warm px-4 py-3 text-sm text-ink" data-testid="aoy-preview-lines">
                            {preview.lines.map((line, index) => (
                                <li key={index}>{line}</li>
                            ))}
                        </ul>
                        <p className="flex flex-wrap gap-3 text-sm">
                            <span className="text-muted">{t("aoy.pvSpecs")}</span>
                            <span className="text-ink">{preview.publicSpecialties.join(" · ")}</span>
                        </p>
                        {problemLine}
                    </>
                )}
            </AoyDialog>
        </div>
    );
}
