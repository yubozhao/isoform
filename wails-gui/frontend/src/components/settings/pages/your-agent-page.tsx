import { useEffect, useRef, useState, type ReactNode } from "react";
import { Share2, ExternalLink } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuTrigger, DropdownMenuContent, DropdownMenuItem } from "@/components/ui/dropdown-menu";
import { SettingsCard } from "@/components/settings/settings-primitives";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import {
    CheckYourAgentName,
    VerifyYourAgentCode,
    ResendYourAgentCode,
    ChooseYourAgentPage,
    CancelYourAgentRequest,
    GetYourAgentPage,
    NativeClipboardSetText,
    OpenExternalURL,
    GetYourAgentReservation,
    ReserveYourAgent,
    type YourAgentPageData,
    type YourAgentReservation,
} from "@/lib/wails-shim-app";
import {
    YOUR_AGENT_RESERVED_NAMES,
    formatYourAgentClock,
    isUsableYourAgentName,
    isValidYourAgentEmail,
    isValidYourAgentNameFormat,
    nextUtcHour,
    normalizeYourAgentName,
    sanitizeYourAgentNameInput,
    sanitizeYourAgentCode,
} from "@/lib/your-agent";

/** Stands in for a link inside a translated sentence, so the link keeps its own element. */
const LINK_MARK = "";
const NAME_PREFIX = "isoform.ai/agents/";
const BO_ADDRESS = "bo@isoform.ai";
/** While the first computation runs, the page re-reads the cache this often, this many
 *  times (30 seconds), then says it couldn't work out the specialties (S019). */
const POLL_MS = 500;
const POLL_LIMIT = 60;

type NameStatus = "idle" | "checking" | "free" | "taken" | "not_allowed" | "invalid";

const FIELD_CLASS =
    "h-8 min-w-0 rounded-md border border-border bg-panel px-2 text-sm text-ink placeholder:text-ink/40 focus:outline-none focus:ring-1 focus:ring-accent disabled:opacity-50";
const CHIP_CLASS =
    "inline-flex items-center rounded-full bg-copper/10 px-2 py-0.5 text-[11px] font-semibold text-copper";
const LINE_CLASS = "text-xs leading-relaxed text-ink/55";
const LINK_BUTTON_CLASS = "text-accent underline-offset-2 hover:underline";

/** Splits a sentence that holds LINK_MARK into the text before and after the link. */
function splitAroundLink(sentence: string): [string, string] {
    const index = sentence.indexOf(LINK_MARK);
    if (index < 0) return [sentence, ""];
    return [sentence.slice(0, index), sentence.slice(index + LINK_MARK.length)];
}

/** Wordless rows the size of the real rows, shown under the real headings. */
function PlaceholderRows({ count, rowClass, gapClass, testId }: { count: number; rowClass: string; gapClass: string; testId: string }) {
    return (
        <div data-testid={testId} aria-hidden="true" className={`flex flex-col ${gapClass}`}>
            {Array.from({ length: count }, (_, index) => (
                <div key={index} className={rowClass} />
            ))}
        </div>
    );
}

/**
 * Agent of You (coming soon). The Mac computes the specialties and the taste
 * from How you do things. The person requests a page name with their email;
 * Isoform's server holds the request and emails them to confirm. Nothing is
 * sent by the email line until the person presses Send in their mail app.
 */
export function YourAgentPage({ ownerCard }: { ownerCard?: (reservation: YourAgentReservation) => ReactNode } = {}) {
    const { t, i18n } = useTranslation("settings");
    const [page, setPage] = useState<YourAgentPageData | null>(null);
    const [reservation, setReservation] = useState<YourAgentReservation | null>(null);
    const [online, setOnline] = useState(true);
    const [editing, setEditing] = useState(false);
    const [name, setName] = useState("");
    const [email, setEmail] = useState("");
    const [nameStatus, setNameStatus] = useState<NameStatus>("idle");
    const [suggestion, setSuggestion] = useState("");
    const [busy, setBusy] = useState(false);
    const [sendFailed, setSendFailed] = useState(false);
    /** Epoch milliseconds before which Send is dimmed after a too-many-tries answer. */
    const [rateUntil, setRateUntil] = useState<number | null>(null);
    const [emailRejected, setEmailRejected] = useState(false);
    const [copiedLink, setCopiedLink] = useState(false);
    const [linkedInCopied, setLinkedInCopied] = useState(false);
    const [code, setCode] = useState("");
    const [codeError, setCodeError] = useState<"wrong_code" | "expired" | null>(null);
    const [sentAgain, setSentAgain] = useState(false);
    const [ranOut, setRanOut] = useState(false);
    /** The first computation gave up after POLL_LIMIT reads, or the page read failed. */
    const [stalled, setStalled] = useState(false);
    /** Bumped to start the computation's reads again (Try again, window focus). */
    const [computeRun, setComputeRun] = useState(0);
    const share = useSettingsFailure();
    const mail = useSettingsFailure();
    const codeInput = useRef<HTMLInputElement>(null);

    // First paint: the cached page. Then the reservation read updates in place.
    useEffect(() => {
        let cancelled = false;
        GetYourAgentPage()
            .then((data) => {
                if (cancelled) return;
                setPage(data);
                setReservation(data.reservation ?? null);
                // The empty form starts with the first form of the name this computer worked out (S018).
                if (!data.reservation && data.nameCandidates?.[0]) setName((current) => current || data.nameCandidates[0]);
                GetYourAgentReservation()
                    .then((read) => {
                        if (cancelled) return;
                        setOnline(read.online);
                        setReservation(read.reservation ?? null);
                        if (read.ranOut) {
                            setName(read.ranOut.name);
                            setEmail(read.ranOut.email);
                            setRanOut(true);
                            setEditing(true);
                            checkName(read.ranOut.name);
                        }
                    })
                    .catch(() => {
                        if (!cancelled) setOnline(false);
                    });
            })
            .catch(() => {
                if (cancelled) return;
                setPage({ specialties: [], taste: [], stillLearning: false, nameCandidates: [], online: false, computed: false } as unknown as YourAgentPageData);
                setOnline(false);
                setStalled(true);
            });
        return () => {
            cancelled = true;
        };
    }, []);

    // The first computation runs in the background. Until it lands, re-read the cache.
    const pendingCompute = page !== null && !page.computed;
    useEffect(() => {
        if (!pendingCompute || stalled) return;
        let cancelled = false;
        let attempts = 0;
        const timer = window.setInterval(() => {
            attempts += 1;
            if (attempts > POLL_LIMIT) {
                window.clearInterval(timer);
                setStalled(true);
                return;
            }
            GetYourAgentPage()
                .then((next) => {
                    if (cancelled || !next.computed) return;
                    window.clearInterval(timer);
                    setPage(next);
                })
                .catch(() => undefined);
        }, POLL_MS);
        return () => {
            cancelled = true;
            window.clearInterval(timer);
        };
    }, [pendingCompute, stalled, computeRun]);

    /** Reads the page again, which starts the computation again, and waits up to 30 seconds more. */
    const computeAgain = () => {
        setStalled(false);
        GetYourAgentPage()
            .then((next) => {
                setPage((current) => (next.computed || !current ? next : current));
                setComputeRun((run) => run + 1);
            })
            .catch(() => setStalled(true));
    };
    // Coming back to the window reads again while the specialties aren't worked out.
    const computeAgainRef = useRef(computeAgain);
    computeAgainRef.current = computeAgain;
    useEffect(() => {
        if (!pendingCompute) return;
        const onFocus = () => computeAgainRef.current();
        window.addEventListener("focus", onFocus);
        return () => window.removeEventListener("focus", onFocus);
    }, [pendingCompute]);

    // Send comes back when the too-many-tries time passes.
    useEffect(() => {
        if (rateUntil === null) return;
        const remaining = rateUntil - Date.now();
        if (remaining <= 0) {
            setRateUntil(null);
            return;
        }
        const timer = window.setTimeout(() => setRateUntil(null), remaining);
        return () => window.clearTimeout(timer);
    }, [rateUntil]);

    const showForm = !reservation || editing;
    const normalizedName = normalizeYourAgentName(name);
    const emailValid = isValidYourAgentEmail(email);
    const nameValid = isUsableYourAgentName(normalizedName);
    const rateWaiting = rateUntil !== null;
    const canSend = nameValid && emailValid && online && !busy && !rateWaiting;

    /**
     * Taken is checked when the person leaves the name field, and again on
     * Send. The rules line (length or a dot at either end) shows here too.
     */
    const checkName = (value: string) => {
        setNameStatus("checking");
        CheckYourAgentName(value)
            .then((result) => {
                if (result.reason === "offline") { setOnline(false); setNameStatus("idle"); }
                else if (result.available) setNameStatus("free");
                else if (result.reason === "taken") { setSuggestion(result.suggestion ?? ""); setNameStatus("taken"); }
                else if (result.reason === "not_allowed") setNameStatus("not_allowed");
                else setNameStatus("invalid");
            })
            .catch(() => setOnline(false));
    };
    const onNameBlur = () => {
        if (!showForm) return;
        if (!normalizedName) { setNameStatus("idle"); return; }
        if (!isValidYourAgentNameFormat(normalizedName)) { setNameStatus("invalid"); return; }
        if (YOUR_AGENT_RESERVED_NAMES.includes(normalizedName)) { setNameStatus("not_allowed"); return; }
        if (reservation && reservation.name === normalizedName) { setNameStatus("free"); return; }
        checkName(normalizedName);
    };

    const onSend = async () => {
        if (!canSend) return;
        setBusy(true);
        setSendFailed(false);
        setEmailRejected(false);
        try {
            const result = await ReserveYourAgent(normalizedName, email.trim());
            switch (result.status) {
                case "reserved":
                case "pending":
                case "choice":
                case "unconfirmed":
                    setReservation(result.reservation ?? null);
                    setEditing(false);
                    setRanOut(false);
                    setNameStatus("idle");
                    setOnline(true);
                    break;
                case "taken":
                    setSuggestion(result.suggestion ?? "");
                    setNameStatus("taken");
                    break;
                case "not_allowed":
                    setNameStatus("not_allowed");
                    break;
                case "invalid_name":
                    setNameStatus("invalid");
                    break;
                case "invalid_email":
                    setEmailRejected(true);
                    break;
                case "rate": {
                    const parsed = result.retryAt ? Date.parse(result.retryAt) : NaN;
                    setRateUntil(Number.isNaN(parsed) ? nextUtcHour(new Date()).getTime() : parsed);
                    break;
                }
                case "offline":
                    setOnline(false);
                    break;
                default:
                    setSendFailed(true);
            }
        } catch {
            setSendFailed(true);
        } finally {
            setBusy(false);
        }
    };

    const onChange = () => {
        if (!reservation) return;
        setEditing(true);
        setName(reservation.name);
        setEmail(reservation.email);
        setNameStatus("free");
    };

    const applyCodeResult = (result: import("@/lib/wails-shim-app").YourAgentReserveResult, original: YourAgentReservation) => {
        switch (result.status) {
            case "reserved": case "pending": case "choice": case "unconfirmed":
                setReservation(result.reservation ?? null);
                setCodeError(null);
                setOnline(true);
                break;
            case "wrong_code":
                setCode(""); setCodeError("wrong_code");
                window.setTimeout(() => codeInput.current?.focus(), 0);
                break;
            case "expired":
                if (original.status === "choice") {
                    setReservation(result.reservation ?? null);
                    if (!result.reservation) {
                        setName(original.name); setEmail(original.email); setEditing(true); setRanOut(true);
                        checkName(original.name);
                    }
                } else setCodeError("expired");
                break;
            case "taken":
                setName(original.name); setEmail(original.email); setReservation(null); setEditing(true);
                setSuggestion(result.suggestion ?? ""); setNameStatus("taken"); break;
            case "rate": {
                const parsed = result.retryAt ? Date.parse(result.retryAt) : NaN;
                setRateUntil(Number.isNaN(parsed) ? nextUtcHour(new Date()).getTime() : parsed);
                break;
            }
            case "offline": setOnline(false); break;
            default: setSendFailed(true);
        }
    };
    const onVerify = async (value: string) => {
        if (!reservation || busy || rateWaiting) return;
        setBusy(true); setCodeError(null); setSendFailed(false);
        try { applyCodeResult(await VerifyYourAgentCode(value), reservation); }
        catch { setSendFailed(true); }
        finally { setBusy(false); }
    };
    const onResend = async () => {
        if (!reservation || busy || rateWaiting) return;
        setBusy(true); setSendFailed(false);
        try {
            let result = await ResendYourAgentCode();
            if (result.status === "none") result = await ReserveYourAgent(reservation.name, reservation.email);
            applyCodeResult(result, reservation);
            if (result.status === "pending") {
                setCode(""); setSentAgain(true);
                window.setTimeout(() => setSentAgain(false), 1600);
            }
        } catch { setSendFailed(true); }
        finally { setBusy(false); }
    };
    const onCancel = async () => {
        if (!reservation || busy) return;
        const current = reservation;
        setBusy(true); setSendFailed(false);
        try {
            const result = await CancelYourAgentRequest();
            if (result.status === "released" || result.status === "reserved") {
                setReservation(result.reservation ?? null);
                setName(current.name); setEmail(current.email); setEditing(true);
                setNameStatus("idle"); setCode(""); setCodeError(null);
            } else applyCodeResult(result, current);
        } catch { setSendFailed(true); }
        finally { setBusy(false); }
    };
    const onChoose = async (keep: boolean) => {
        if (!reservation || busy) return;
        setBusy(true); setSendFailed(false);
        try { applyCodeResult(await ChooseYourAgentPage(keep), reservation); }
        catch { setSendFailed(true); }
        finally { setBusy(false); }
    };
    const codeStep = reservation?.status === "pending" || (reservation?.status === "unconfirmed" && !!reservation.expiresAt && Date.parse(reservation.expiresAt) > Date.now());

    const shareName = reservation?.name ?? "";
    const shareUrl = `https://isoform.ai/agents/${encodeURIComponent(shareName)}`;
    const isChinese = i18n.resolvedLanguage?.startsWith("zh") ?? i18n.language.startsWith("zh");
    const items = (page?.computed && !page.stillLearning ? page.specialties : []).slice(0, 3).map((item: { name: string }) => item.name.trim()).filter(Boolean);
    const work = (isChinese ? items.length > 1 ? `${items.slice(0, -1).join("、")}和${items[items.length - 1]}` : items[0] ?? "" : items.length > 1 ? `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}` : items[0] ?? "")
        .replace(/[\p{P}\s]+$/gu, "").slice(0, isChinese ? 30 : 60);
    const post = (network: "x" | "threads" | "linkedin") => t(`yourAgent.sharePost.${network}${work ? "With" : "Without"}` as const, { work });
    const onCopyLink = async () => {
        share.clear();
        try { await NativeClipboardSetText(shareUrl); setCopiedLink(true); window.setTimeout(() => setCopiedLink(false), 1600); }
        catch { setCopiedLink(false); share.fail(t("yourAgent.copyLinkFailed"), () => void onCopyLink()); }
    };
    const openBrowser = async (url: string, retry: () => void) => {
        try { await OpenExternalURL(url); return true; }
        catch { share.fail(t("yourAgent.browserFailed"), retry); return false; }
    };
    const onShare = async (network: "x" | "threads" | "linkedin") => {
        share.clear();
        const url = encodeURIComponent(shareUrl);
        const retry = () => void onShare(network);
        if (network === "linkedin") {
            setLinkedInCopied(false);
            try { await NativeClipboardSetText(post(network)); }
            catch { share.fail(t("yourAgent.linkedinCopyFailed"), retry); return; }
            if (await openBrowser(`https://www.linkedin.com/sharing/share-offsite/?url=${url}`, retry)) setLinkedInCopied(true);
            return;
        }
        const base = network === "x" ? "https://x.com/intent/tweet" : "https://www.threads.com/intent/post";
        await openBrowser(`${base}?text=${encodeURIComponent(post(network))}&url=${url}`, retry);
    };

    const subject = reservation
        ? t("yourAgent.emailSubject", { name: reservation.name })
        : t("yourAgent.emailSubjectBare");
    const mailto = `mailto:${BO_ADDRESS}?subject=${encodeURIComponent(subject)}`;

    const [writeBefore, writeAfter] = splitAroundLink(t("yourAgent.writeBo", { address: LINK_MARK }));
    const writeLine = (
        <p className={LINE_CLASS}>
            {writeBefore}
            <a
                className={LINK_BUTTON_CLASS}
                href={mailto}
                onClick={(event) => {
                    event.preventDefault();
                    mail.clear();
                    OpenExternalURL(mailto).catch(() => mail.fail(t("yourAgent.mailFailed")));
                }}
            >
                {BO_ADDRESS}
            </a>
            {writeAfter}
        </p>
    );
    const writeLines = (
        <>
            {writeLine}
            <SettingsFailureLine failure={mail.failure} />
        </>
    );

    /** The line under the field: the rules line, Taken with its suggestion, or Not allowed. */
    const nameMessage = ((): ReactNode => {
        switch (nameStatus) {
            case "taken": {
                if (!suggestion) return <p className={LINE_CLASS}>{t("yourAgent.takenNoSuggestion")}</p>;
                const [before, after] = splitAroundLink(t("yourAgent.taken", { suggestion: LINK_MARK }));
                return (
                    <p className={LINE_CLASS}>
                        {before}
                        <button type="button" className={LINK_BUTTON_CLASS} onClick={() => setName(suggestion)}>
                            {suggestion}
                        </button>
                        {after}
                    </p>
                );
            }
            case "not_allowed":
                return <p className={LINE_CLASS}>{t("yourAgent.notAllowed")}</p>;
            case "invalid":
                return <p className={LINE_CLASS}>{t("yourAgent.invalidName")}</p>;
            default:
                return null;
        }
    })();

    /** The send-failed line or the too-many-tries line; the Email Bo line sits right under it. */
    const sendLine = ((): ReactNode => {
        if (rateUntil !== null) {
            const time = formatYourAgentClock(new Date(rateUntil), i18n.language);
            return <p className={LINE_CLASS}>{t("yourAgent.rateLimited", { time })}</p>;
        }
        if (sendFailed) {
            return <p className={LINE_CLASS}>{t("yourAgent.sendFailed")}</p>;
        }
        return null;
    })();

    // Agent of You (v2.1.3 o01, o02): once the name is confirmed, the join card sits on top.
    const confirmed = reservation && !showForm && !["pending", "choice", "unconfirmed"].includes(reservation.status ?? "") ? reservation : null;

    return (
        <div className="flex flex-col gap-4" data-testid="your-agent-page">
            {confirmed && ownerCard?.(confirmed)}
            <div className="flex flex-col gap-2">
                <span className={CHIP_CLASS}>{t("yourAgent.comingSoon")}</span>
                {/* The header carries the first sentence of the lead; the rest sits here. */}
                <p className="text-sm leading-relaxed text-ink/55">{t("yourAgent.leadRest")} {t("yourAgent.reserveLead")}</p>
            </div>

            <SettingsCard title={t("yourAgent.specialtiesTitle")} description={t("yourAgent.specialtiesFrom")}>
                {stalled && (!page || !page.computed) ? (
                    <SettingsFailureLine failure={{ text: t("yourAgent.specialtiesFailed"), retry: computeAgain }} />
                ) : !page || !page.computed ? (
                    <PlaceholderRows
                        count={3}
                        rowClass="h-9 rounded-md bg-ink/[0.04]"
                        gapClass="gap-0"
                        testId="your-agent-specialties-placeholder"
                    />
                ) : page.stillLearning ? (
                    <p className="settings-row-desc">{t("yourAgent.stillLearning")}</p>
                ) : (
                    <ul className="flex flex-col divide-y divide-border" data-testid="your-agent-specialties">
                        {page.specialties.map((specialty) => (
                            <li key={specialty.name} className="py-2">
                                <span className="settings-row-title">{specialty.name}</span>
                            </li>
                        ))}
                    </ul>
                )}
            </SettingsCard>

            {(!page || !page.computed) && !stalled && (
                <SettingsCard title={t("yourAgent.tasteTitle")}>
                    <PlaceholderRows
                        count={3}
                        rowClass="h-5 rounded bg-ink/[0.04]"
                        gapClass="gap-1.5"
                        testId="your-agent-taste-placeholder"
                    />
                </SettingsCard>
            )}

            {page && page.computed && !page.stillLearning && (
                <SettingsCard title={t("yourAgent.tasteTitle")}>
                    <ul className="flex list-disc flex-col gap-1.5 pl-5 text-sm text-ink" data-testid="your-agent-taste">
                        {page.taste.map((line) => (
                            <li key={line}>{line}</li>
                        ))}
                    </ul>
                </SettingsCard>
            )}

            <SettingsCard title={t("yourAgent.requestTitle")}>
                {!page ? (
                    <PlaceholderRows count={2} rowClass="h-8 rounded-md bg-ink/[0.04]" gapClass="gap-2" testId="your-agent-request-placeholder" />
                ) : showForm ? (
                    <div className="flex flex-col gap-2">
                        <div className="flex flex-wrap items-center gap-2">
                            <label className={`${FIELD_CLASS} flex min-w-[220px] flex-1 items-center gap-1`}>
                                <span className="shrink-0 text-ink/45">{NAME_PREFIX}</span>
                                <input
                                    aria-label={t("yourAgent.nameLabel")}
                                    className="min-w-0 flex-1 bg-transparent focus:outline-none"
                                    value={name}
                                    placeholder={t("yourAgent.namePlaceholder")}
                                    autoCapitalize="none"
                                    autoCorrect="off"
                                    spellCheck={false}
                                    onChange={(event) => {
                                        setName(sanitizeYourAgentNameInput(event.target.value));
                                        setNameStatus("idle");
                                    }}
                                    onBlur={onNameBlur}
                                />
                            </label>
                            <input
                                aria-label={t("yourAgent.emailLabel")}
                                type="email"
                                className={`${FIELD_CLASS} min-w-[180px] flex-1`}
                                value={email}
                                placeholder={t("yourAgent.emailPlaceholder")}
                                onChange={(event) => {
                                    setEmail(event.target.value);
                                    setEmailRejected(false);
                                }}
                            />
                            <Button size="sm" disabled={!canSend} onClick={() => void onSend()}>
                                {t("yourAgent.sendButton")}
                            </Button>
                        </div>
                        {editing && reservation && (
                            <p className={LINE_CLASS}>
                                <button type="button" className={LINK_BUTTON_CLASS} onClick={() => { setEditing(false); setNameStatus("idle"); setSendFailed(false); }}>
                                    {t("yourAgent.keepCurrent", { name: reservation.name })}
                                </button>
                            </p>
                        )}
                        {ranOut && <p className={LINE_CLASS}>{t("yourAgent.ranOut")}</p>}
                        {nameMessage}
                        {email.trim() !== "" && (!emailValid || emailRejected) && (
                            <p className={LINE_CLASS}>{t("yourAgent.invalidEmail")}</p>
                        )}
                        {!online && <p className={LINE_CLASS}>{t("yourAgent.offline")}</p>}
                        <p className={LINE_CLASS}>{t("yourAgent.freeNote")}</p>
                        <p className={LINE_CLASS}>{t("yourAgent.reserveNote")}</p>
                        {sendLine}
                        {writeLines}
                    </div>
                ) : reservation && codeStep ? (
                    <div className="flex flex-col gap-2">
                        <p className="text-sm font-semibold text-ink">{t("yourAgent.codeTitle")}</p>
                        <p className="text-sm text-muted">{t("yourAgent.codeLine", { name: reservation.name, email: reservation.email })}</p>
                        <div className={`relative flex w-fit gap-1.5 ${busy ? "opacity-45" : ""}`}>
                            {Array.from({ length: 6 }, (_, index) => (
                                <span key={index} aria-hidden="true" className={`grid h-10 w-[34px] place-items-center rounded-lg border bg-panel-warm text-lg font-semibold text-ink ${index === code.length ? "border-accent" : "border-border"}`}>{code[index] ?? ""}</span>
                            ))}
                            <input ref={codeInput} aria-label={t("yourAgent.codeLabel")} autoComplete="one-time-code" inputMode="numeric" maxLength={6} value={code} disabled={busy || rateWaiting} className="absolute inset-0 h-full w-full cursor-text opacity-0" onChange={(event) => {
                                const value = sanitizeYourAgentCode(event.target.value);
                                setCode(value); setCodeError(null);
                                if (value.length === 6) void onVerify(value);
                            }} />
                        </div>
                        {codeError && <p className="text-xs text-copper">{t(codeError === "wrong_code" ? "yourAgent.wrongCode" : "yourAgent.expiredCode")}</p>}
                        {sendLine}
                        {!online && <p className={LINE_CLASS}>{t("yourAgent.offline")}</p>}
                        <p className={LINE_CLASS}>{t("yourAgent.noCode")} <button type="button" disabled={busy || rateWaiting} className={LINK_BUTTON_CLASS} onClick={() => void onResend()}>{t(sentAgain ? "yourAgent.sent" : "yourAgent.sendAgain")}</button> · <button type="button" disabled={busy} className={LINK_BUTTON_CLASS} onClick={() => void onCancel()}>{t("yourAgent.changeDetails")}</button></p>
                        {writeLines}
                    </div>
                ) : reservation && reservation.status === "choice" ? (
                    <div className="flex flex-col gap-2">
                        <p className="text-sm font-semibold text-ink">{t("yourAgent.choiceTitle", { email: reservation.email, old: reservation.oldName })}</p>
                        <p className="text-sm text-muted">{t("yourAgent.choiceLine", { old: reservation.oldName, new: reservation.name })}</p>
                        <div className="flex gap-2"><Button variant="secondary" size="sm" disabled={busy} onClick={() => void onChoose(true)}>{t("yourAgent.keep", { old: reservation.oldName })}</Button><Button size="sm" disabled={busy} onClick={() => void onChoose(false)}>{t("yourAgent.switch", { new: reservation.name })}</Button></div>
                        {sendLine}{writeLines}
                    </div>
                ) : reservation && reservation.status === "unconfirmed" ? (
                    <div className="flex flex-col gap-2">
                        <p className="text-sm font-semibold text-ink">{t("yourAgent.confirmTitle")}</p>
                        <p className="text-sm text-muted">{t("yourAgent.confirmLine", { name: reservation.name, email: reservation.email })}</p>
                        <div className="flex items-center gap-2"><Button size="sm" disabled={busy || rateWaiting} onClick={() => void onResend()}>{t("yourAgent.sendCode")}</Button><button type="button" className={`text-xs ${LINK_BUTTON_CLASS}`} onClick={onChange}>{t("yourAgent.changeButton")}</button></div>
                        {sendLine}{writeLines}
                    </div>
                ) : reservation && (
                    <div className="flex flex-col gap-2" data-testid="your-agent-sent">
                        <p className="text-sm font-semibold text-ink">{t("yourAgent.yoursTitle", { name: reservation.name })}</p>
                        <p className="text-sm text-muted">{t("yourAgent.yoursLine", { email: reservation.email })} <button type="button" className={LINK_BUTTON_CLASS} onClick={onChange}>{t("yourAgent.changeButton")}</button></p>
                        {reservation.place != null && <p className="text-sm font-semibold text-ink">{t("yourAgent.place", { place: reservation.place })}</p>}
                        <div className="flex flex-col gap-1 rounded-xl bg-accent/10 p-3">
                            <p className="text-sm font-semibold text-ink">{t("yourAgent.moveUp")}</p>
                            <p className="text-xs text-muted">{t("yourAgent.moveUpLine")}</p>
                            <div className="mt-1 flex flex-wrap items-center gap-2">
                                <Button size="sm" onClick={() => void onCopyLink()}>{t(copiedLink ? "yourAgent.copiedLink" : "yourAgent.copyLink")}</Button>
                                <DropdownMenu>
                                    <DropdownMenuTrigger asChild><Button variant="secondary" size="sm"><Share2 aria-hidden className="h-4 w-4" />{t("yourAgent.shareMenu")}</Button></DropdownMenuTrigger>
                                    <DropdownMenuContent align="start">{(["x", "threads", "linkedin"] as const).map((network) => <DropdownMenuItem key={network} onSelect={() => void onShare(network)}><ExternalLink aria-hidden />{t(`yourAgent.shareAction.${network}`)}</DropdownMenuItem>)}</DropdownMenuContent>
                                </DropdownMenu>
                                {linkedInCopied && <p className="text-xs text-ink" role="status">{t("yourAgent.linkedinCopied")}</p>}
                            </div>
                            <SettingsFailureLine failure={share.failure} />
                        </div>
                        {!online && <p className={LINE_CLASS}>{t("yourAgent.offline")}</p>}{writeLines}
                    </div>
                )}
            </SettingsCard>

            <ul className="flex list-disc flex-col gap-1.5 pl-5 text-sm text-ink/60">
                <li>{t("yourAgent.howClients")}</li>
                <li>{t("yourAgent.howRate")}</li>
            </ul>
        </div>
    );
}
