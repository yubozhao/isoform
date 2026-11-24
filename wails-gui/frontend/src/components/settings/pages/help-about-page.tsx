import { AboutUpdates } from "./about-updates";
import { ModelsPanel } from "@/components/onboarding/models-panel";
import {
    AboutModels,
    AppearanceSettings,
    LanguageSettings,
    StartAtLoginSettings,
    YourNameSettings,
} from "./about-settings";
import { useCallback, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, Copy, Mail } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
    GetPerfSnapshot,
    GetVersion,
    OpenExternalURL,
} from "../../../../wailsjs/go/main/App";
import { DiscordIcon } from "@/components/icons/app-icons";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";
import {
    SettingsCard,
    SettingsRow,
} from "@/components/settings/settings-primitives";
import { SettingsAdvanced } from "@/components/settings/settings-advanced";
import {
    writePerfReport,
    type PerfSnapshotShape,
} from "@/components/settings/perf-report";

const DISCORD_INVITE_URL = "https://discord.gg/6XaU6hGKda";
const SUPPORT_EMAIL = "bo@isoform.ai";

// ── Get help ──

function GetHelp() {
    const { t } = useTranslation("settings");
    const [copyingReport, setCopyingReport] = useState(false);
    const [copied, setCopied] = useState(false);
    const { failure, fail, clear } = useSettingsFailure();
    // The support report is the text Perf Analysis produces, without
    // subprocess command lines, so it is safe to paste into an email. Its
    // result shows in place, under the rows.
    const handleCopyReport = useCallback(async () => {
        setCopyingReport(true);
        setCopied(false);
        clear();
        try {
            const snap = (await GetPerfSnapshot()) as unknown as PerfSnapshotShape;
            if (!(await writePerfReport(snap))) throw new Error("copy");
            setCopied(true);
        } catch {
            fail(t("about.supportReport.copyFailed"), () => void handleCopyReport());
        } finally {
            setCopyingReport(false);
        }
    }, [clear, fail, t]);

    const handleEmail = useCallback(async () => {
        clear();
        try {
            const version = await GetVersion().catch(() => "");
            const url = `mailto:${SUPPORT_EMAIL}?subject=${encodeURIComponent(t("support.feedbackSubject"))}&body=${encodeURIComponent(t("support.feedbackBody", { version }))}`;
            await OpenExternalURL(url);
        } catch {
            fail(t("about.emailSupport.openFailed"));
        }
    }, [clear, fail, t]);

    return (
        <SettingsCard
            title={t("about.helpTitle")}
            description={t("support.description")}
        >
            <SettingsRow
                title={t("about.emailSupport.title")}
                description={t("about.emailSupport.description")}
                control={
                    <Button size="sm" variant="outline" onClick={() => void handleEmail()}>
                        <Mail className="h-3.5 w-3.5 mr-1.5" />
                        {t("support.emailButton")}
                    </Button>
                }
            />
            <SettingsRow
                title={t("support.discordTitle")}
                description={t("support.discordDescription")}
                control={
                    <Button
                        size="sm"
                        variant="secondary"
                        className="border-border text-ink"
                        onClick={() => {
                            void OpenExternalURL(DISCORD_INVITE_URL);
                        }}
                    >
                        <DiscordIcon monochrome className="h-3.5 w-3.5 mr-1.5" />
                        {t("support.discordButton")}
                    </Button>
                }
            />
            <SettingsRow
                title={t("about.supportReport.title")}
                description={t("about.supportReport.description")}
                control={
                    <Button
                        variant="outline"
                        size="sm"
                        onClick={() => void handleCopyReport()}
                        disabled={copyingReport}
                    >
                        {copyingReport ? (
                            <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" />
                        ) : (
                            <Copy className="h-3.5 w-3.5 mr-1.5" />
                        )}
                        {copyingReport
                            ? t("about.collecting")
                            : t("about.supportReport.copy")}
                    </Button>
                }
            />
            {copied && (
                <p role="status" className="text-[12px] text-ink">
                    {t("about.supportReport.copiedToast")}
                </p>
            )}
            <SettingsFailureLine failure={failure} />
        </SettingsCard>
    );
}

// ── What the support report includes (Advanced) ──

function SupportReportDetails() {
    const { t } = useTranslation("settings");
    return (
        <SettingsCard
            title={t("about.supportReport.details.title")}
            description={t("about.supportReport.details.description")}
        >
            <SettingsRow
                title={t("about.supportReport.details.includedTitle")}
                description={t("about.supportReport.details.included")}
            />
            <SettingsRow
                title={t("about.supportReport.details.excludedTitle")}
                description={t("about.supportReport.details.excluded")}
            />
        </SettingsCard>
    );
}

// ── About page ──

export interface HelpAboutPageProps {
    /** Reopens the first-run setup (moved here from Developer tools). */
    onReplayOnboarding: () => void;
}

/**
 * About (page id "about"), in computer-conformance-answers v1.0.0 T2's order:
 * Your name, Open at login, Language, Appearance, Version and updates, Help
 * and Send feedback; the models on this computer (answers v1.2.0 S11) sit
 * after the updates, and Share diagnostic data under Help (settings batch D2,
 * from Privacy, until Your data's Usage counts row ships). Running setup again stays under Help's Advanced. The old
 * "support", "general", "profile", "appearance" and "language" ids resolve
 * here. Developer tools have no switch here: Cmd+Shift+Option+D turns them on
 * and off (useDevToolsUnlock).
 */
export function HelpAboutPage({ onReplayOnboarding }: HelpAboutPageProps) {
    const { t } = useTranslation("settings");
    return (
        <>
            <YourNameSettings />
            <StartAtLoginSettings />
            <LanguageSettings />
            <AppearanceSettings />
            <AboutUpdates />
            <AboutModels />
            <GetHelp />
            <SettingsAdvanced pageId="about" hint={t("about.advancedHint")}>
                {/* Each model by job, its downloads, sources and mirror: from
                    Settings › Storage (settings batch D2), under the models
                    line it details. */}
                <ModelsPanel settings />
                <SettingsCard>
                    <SettingsRow
                        title={t("onboarding.title")}
                        description={t("onboarding.description")}
                        control={
                            <Button
                                size="sm"
                                variant="outline"
                                onClick={onReplayOnboarding}
                            >
                                {t("onboarding.replayButton")}
                            </Button>
                        }
                    />
                </SettingsCard>
                <SupportReportDetails />
                <SettingsCard
                    title={t("privacy.diagnostics.detailsTitle")}
                    description={t("privacy.diagnostics.detailsDescription")}
                >
                    {null}
                </SettingsCard>
            </SettingsAdvanced>
        </>
    );
}
