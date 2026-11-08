import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Download, Loader2, RefreshCw } from "lucide-react";
import {
    CheckDependencies,
    InstallCLITool,
    type CLIToolStatus,
    type DependencyStatus,
    type MagpieInstallView,
} from "@/lib/wails-shim-app";
import { Button } from "@/components/ui/button";
import { SettingsCard } from "@/components/settings/settings-primitives";
import { ToolDownloadSheet } from "@/components/onboarding/tool-download-sheet";

// ── Tools on this Mac ──
//
// The Settings view of what onboarding's DependenciesStep checks: the
// connection Isoform thinks through (the bundled magpie, or the user's own)
// and whether Claude Code and Codex are installed and signed in, with Install
// when npm is on the login shell's PATH. Same bindings and the same install
// commands as the onboarding step, laid out as settings rows. This is an
// everyday card, so it says "magpie" only for a magpie the user installed;
// the gateway's version, address and group sit under Advanced > Status details.

type Tool = "claude" | "codex";

const TOOLS: Array<{ id: Tool; signIn: string; install: string }> = [
    { id: "claude", signIn: "claude", install: "npm install -g @anthropic-ai/claude-code" },
    { id: "codex", signIn: "codex login", install: "npm install -g @openai/codex" },
];

// The same chip as the permission rows on the Privacy page; a missing piece
// uses the warning tone so it stands out from the settled ones.
type Tone = "neutral" | "warn";

const CHIP_TONE: Record<Tone, string> = {
    neutral: "bg-ink/[0.05] text-ink/55",
    warn: "bg-warning-bg text-warning",
};

function StatusChip({ tone, label }: { tone: Tone; label: string }) {
    return (
        <span
            className={`inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium ${CHIP_TONE[tone]}`}
        >
            {label}
        </span>
    );
}

function ToolRow({
    title,
    description,
    error,
    control,
    testId,
}: {
    title: string;
    description: string;
    error?: string;
    control: ReactNode;
    testId: string;
}) {
    return (
        <div className="settings-row" data-testid={testId}>
            <div className="flex min-w-0 flex-col">
                <span className="settings-row-title">{title}</span>
                <span className="settings-row-desc">{description}</span>
                {error && (
                    <span role="alert" className="settings-row-desc break-words text-copper">
                        {error}
                    </span>
                )}
            </div>
            <div className="flex shrink-0 items-center gap-2">{control}</div>
        </div>
    );
}

export function ToolsOnThisMacCard({
    gatewayInUse = true,
}: {
    /** False while the user's own API key or model is on, so the gateway is not used. */
    gatewayInUse?: boolean;
}) {
    const { t } = useTranslation(["settings", "onboarding"]);
    const [status, setStatus] = useState<DependencyStatus | null>(null);
    const [checking, setChecking] = useState(false);
    const [checkFailed, setCheckFailed] = useState(false);
    const [installing, setInstalling] = useState<Tool | null>(null);
    const [errors, setErrors] = useState<Partial<Record<Tool, string>>>({});
    const [gitSheetOpen, setGitSheetOpen] = useState(false);
    const mounted = useRef(false);

    const nameOf = (tool: Tool) =>
        tool === "claude" ? t("onboarding:agent.names.claude") : t("onboarding:agent.names.codex");

    const check = useCallback(async () => {
        setChecking(true);
        try {
            const next = await CheckDependencies();
            if (mounted.current) {
                setStatus(next);
                setCheckFailed(false);
            }
        } catch {
            if (mounted.current) setCheckFailed(true);
        } finally {
            if (mounted.current) setChecking(false);
        }
    }, []);

    useEffect(() => {
        mounted.current = true;
        void check();
        return () => {
            mounted.current = false;
        };
    }, [check]);

    const install = async (tool: Tool) => {
        if (installing) return;
        setInstalling(tool);
        setErrors((current) => ({ ...current, [tool]: undefined }));
        const failed = (error: string) =>
            setErrors((current) => ({
                ...current,
                [tool]: t("toolsOnThisMac.installFailed", { name: nameOf(tool), error }),
            }));
        try {
            const result = await InstallCLITool(tool);
            if (!mounted.current) return;
            setStatus((current) => (current ? { ...current, [tool]: result.status } : current));
            if (!result.ok) failed(result.output.split("\n").slice(-3).join(" "));
        } catch (error) {
            if (mounted.current) failed(error instanceof Error ? error.message : String(error));
        } finally {
            if (mounted.current) setInstalling(null);
        }
    };

    const gatewayRow = (magpie: MagpieInstallView) => {
        const user = magpie.kind === "user";
        let chip: ReactNode;
        if (!gatewayInUse) {
            chip = <StatusChip tone="neutral" label={t("toolsOnThisMac.notInUse")} />;
        } else if (magpie.healthy) {
            chip = <StatusChip tone="neutral" label={t("toolsOnThisMac.running")} />;
        } else if (user) {
            chip = <StatusChip tone="warn" label={t("toolsOnThisMac.notResponding")} />;
        } else {
            chip = <StatusChip tone="neutral" label={t("toolsOnThisMac.starting")} />;
        }
        return (
            <ToolRow
                testId="tools-gateway"
                title={t("toolsOnThisMac.gatewayTitle")}
                description={
                    !gatewayInUse
                        ? t("toolsOnThisMac.gatewayNotInUse")
                        : t("toolsOnThisMac.gatewayDescription")
                }
                control={chip}
            />
        );
    };

    const toolRow = (tool: (typeof TOOLS)[number], cli: CLIToolStatus, npm: boolean) => {
        let description: string;
        let chip: ReactNode;
        if (cli.installed && cli.logged_in) {
            description = cli.version
                ? t("toolsOnThisMac.version", { version: cli.version })
                : t("toolsOnThisMac.signedIn");
            chip = <StatusChip tone="neutral" label={t("toolsOnThisMac.signedIn")} />;
        } else if (cli.installed) {
            description = t("toolsOnThisMac.signInHint", { command: tool.signIn });
            chip = <StatusChip tone="warn" label={t("toolsOnThisMac.notSignedIn")} />;
        } else {
            description = npm
                ? t("toolsOnThisMac.installHint")
                : t("toolsOnThisMac.npmMissing", { command: tool.install });
            chip = <StatusChip tone="neutral" label={t("toolsOnThisMac.notInstalled")} />;
        }
        const busy = installing === tool.id;
        return (
            <ToolRow
                key={tool.id}
                testId={`tools-${tool.id}`}
                title={nameOf(tool.id)}
                description={description}
                error={errors[tool.id]}
                control={
                    !cli.installed && npm ? (
                        <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            disabled={installing !== null}
                            onClick={() => void install(tool.id)}
                        >
                            {busy ? (
                                <Loader2 className="size-3.5 animate-spin" aria-hidden />
                            ) : (
                                <Download className="size-3.5" aria-hidden />
                            )}
                            {busy ? t("toolsOnThisMac.installing") : t("toolsOnThisMac.install")}
                        </Button>
                    ) : (
                        chip
                    )
                }
            />
        );
    };

    const gitRow = (git: DependencyStatus["git"]) => {
        const recoverable = !git.available && git.recoverable;
        const chip = git.available ? (
            <StatusChip tone="neutral" label={t("toolsOnThisMac.available")} />
        ) : recoverable ? (
            <StatusChip tone="warn" label={t("toolsOnThisMac.notInstalled")} />
        ) : (
            <StatusChip tone="neutral" label={t("toolsOnThisMac.notInstalled")} />
        );
        return (
            <ToolRow
                testId="tools-git"
                title={t("toolsOnThisMac.gitTitle")}
                description={
                    git.available
                        ? t("toolsOnThisMac.gitAvailable")
                        : t("toolsOnThisMac.gitMissing")
                }
                control={
                    recoverable ? (
                        <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            onClick={() => setGitSheetOpen(true)}
                        >
                            <Download className="size-3.5" aria-hidden />
                            {t("toolsOnThisMac.gitRecover")}
                        </Button>
                    ) : (
                        chip
                    )
                }
            />
        );
    };

    return (
        <>
        <SettingsCard
            title={t("toolsOnThisMac.title")}
            description={t("toolsOnThisMac.description")}
            headerRight={
                <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={checking || installing !== null}
                    onClick={() => void check()}
                >
                    {checking ? (
                        <Loader2 className="size-3.5 animate-spin" aria-hidden />
                    ) : (
                        <RefreshCw className="size-3.5" aria-hidden />
                    )}
                    {t("toolsOnThisMac.checkAgain")}
                </Button>
            }
        >
            {status === null ? (
                checkFailed ? (
                    <p role="alert" className="py-2 text-xs text-copper">
                        {t("toolsOnThisMac.checkFailed")}
                    </p>
                ) : (
                    <p role="status" className="settings-row-desc py-2">
                        {t("toolsOnThisMac.checking")}
                    </p>
                )
            ) : (
                <div className="flex flex-col" data-testid="tools-on-this-mac">
                    {checkFailed && (
                        <p role="alert" className="pb-2 text-xs text-copper">
                            {t("toolsOnThisMac.checkFailed")}
                        </p>
                    )}
                    {gatewayRow(status.magpie)}
                    {TOOLS.map((tool) => toolRow(tool, status[tool.id], status.npm))}
                    {gitRow(status.git)}
                </div>
            )}
        </SettingsCard>
        <ToolDownloadSheet
            open={gitSheetOpen}
            onOpenChange={setGitSheetOpen}
            onInstalled={() => void check()}
        />
        </>
    );
}
