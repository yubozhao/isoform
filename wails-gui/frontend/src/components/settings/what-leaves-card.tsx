import { useCallback, useEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { SettingsFailureLine, useSettingsFailure, type SettingsFailure } from "@/components/settings/settings-failure";
import { AssistantGivenSheet } from "@/components/settings/assistant-given-sheet";
import { DisconnectButton } from "@/components/settings/pages/chat-apps/disconnect-button";
import { useIsoformAssistant } from "@/components/settings/pages/your-ai-section";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { useTelemetryPreference } from "@/hooks/useTelemetryPreference";
import {
    DisconnectFeishu,
    DisconnectSlack,
    DisconnectTelegram,
    DisconnectWeChat,
    ListMcpServers,
    SetMcpServerEnabled,
    UnbindDiscordAccount,
} from "../../../wailsjs/go/main/App";
import {
    GetUserTopics,
    GetYourAgents,
    SetTopicFindsEnabled,
    type YourDataSummary,
} from "@/lib/wails-shim-app";

// ── What leaves this computer (computer-data v1.0.0 rule 5, data-leaves
// v1.0.0, answers v1.1.0 T5-T7, v1.2.0 T8, v1.3.0 T13, v1.4.0 S17-S19) ──
//
// One row per destination the build has, in design's words, each with this
// week's count. A row shows only while its path is on or sent something this
// week (GetYourDataSummary decides from the destinations list). Where design
// gives one, the row has its stop: the topic-finds switch, Disconnect on
// each chat app and tool server, the usage-counts switch.
//
// A count that couldn't be read shows its failure with Try again, never 0.
// A count that may have dropped a call this week reads "at least {n}".

type Weekly = { thisWeek?: number; incomplete?: true };

/** "{n}", "at least {n}", or null when the count wasn't read. */
export function weekCount(week: Weekly | undefined, t: TFunction<"settings">): string | null {
    if (!week || typeof week.thisWeek !== "number") return null;
    return week.incomplete ? t("leaves.atLeast", { count: week.thisWeek }) : String(week.thisWeek);
}

const CHAT_APP_TITLE: Record<string, string> = {
    slack: "connectors.slack.title",
    telegram: "connectors.telegram.title",
    discord: "connectors.discord.title",
    feishu: "connectors.feishu.title",
    lark: "leaves.lark",
    wechat: "connectors.wechat.title",
};

function disconnectChatApp(app: string): Promise<unknown> {
    switch (app) {
        case "slack":
            return DisconnectSlack();
        case "telegram":
            return DisconnectTelegram();
        case "discord":
            return UnbindDiscordAccount();
        case "feishu":
        case "lark":
            return DisconnectFeishu(app);
        case "wechat":
            return DisconnectWeChat();
        default:
            return Promise.reject(new Error(`unknown chat app ${app}`));
    }
}

type ToolServer = { id: string; name: string; enabled: boolean };

function Row({ id, title, children, control, failure }: {
    id: string;
    title: string;
    children?: ReactNode;
    control?: ReactNode;
    failure?: SettingsFailure | null;
}) {
    return (
        <div data-your-data-row={id}>
            <SettingsRow title={title} description={children} control={control} failure={failure} />
        </div>
    );
}

/** The assistant's name as Your AI shows it; "the AI you chose" until read. */
function useAssistantName(): string {
    const { t } = useTranslation("settings");
    const id = useIsoformAssistant();
    const [name, setName] = useState<string | null>(null);
    useEffect(() => {
        if (!id) return;
        let live = true;
        GetYourAgents().then(
            (agents) => live && setName(agents?.agents.find((agent) => agent.id === id)?.name ?? null),
            () => {},
        );
        return () => {
            live = false;
        };
    }, [id]);
    return name ?? t("leaves.yourAI");
}

export function WhatLeavesCard({ summary, reload, agentsLine, agentNames, extraRows }: {
    summary: YourDataSummary | null;
    reload: () => void;
    /** The hand-off line, the agents row's first line. */
    agentsLine: string;
    /** An agent's display name ("claude-code" → "Claude Code"). */
    agentNames: (agent: string) => string;
    /** Phone and watch, notifications, Agent of You: kept as they were. */
    extraRows: ReactNode;
}) {
    const { t } = useTranslation("settings");
    const assistant = useAssistantName();
    const [seeing, setSeeing] = useState(false);
    const countFailure: SettingsFailure = { text: t("leaves.countFailed"), retry: reload };
    const leaves = summary?.leaves;

    // Topic finds: the switch on the Web searches row (S17).
    const [findsOn, setFindsOn] = useState<boolean | null>(null);
    const [findsFailed, setFindsFailed] = useState(false);
    const finds = useSettingsFailure();
    const readFinds = useCallback(() => {
        setFindsFailed(false);
        GetUserTopics().then(
            (view) => setFindsOn(view.enabled),
            () => {
                setFindsOn(null);
                setFindsFailed(true);
            },
        );
    }, []);
    useEffect(() => {
        if (leaves?.webSearches) readFinds();
    }, [leaves?.webSearches, readFinds]);
    const changeFinds = (next: boolean) => {
        finds.clear();
        SetTopicFindsEnabled(next).then(
            (view) => {
                setFindsOn(view.enabled);
                reload();
            },
            () => finds.fail(t("leaves.webSwitchFailed"), () => changeFinds(next)),
        );
    };

    // Tool servers Chat uses (S19): the connected ones, each with Disconnect.
    const [servers, setServers] = useState<ToolServer[] | null>(null);
    const [serversFailed, setServersFailed] = useState(false);
    const readServers = useCallback(() => {
        setServersFailed(false);
        ListMcpServers().then(
            (list) => setServers(((list ?? []) as ToolServer[]).filter((server) => server.enabled)),
            () => {
                setServers(null);
                setServersFailed(true);
            },
        );
    }, []);
    useEffect(() => {
        readServers();
    }, [readServers]);

    const disconnect = useSettingsFailure();
    const disconnectApp = (app: string, name: string) => {
        disconnect.clear();
        disconnectChatApp(app).then(reload, () => disconnect.fail(t("leaves.disconnectFailed", { app: name }), () => disconnectApp(app, name)));
    };
    const disconnectServer = (server: ToolServer) => {
        disconnect.clear();
        SetMcpServerEnabled(server.id, false).then(readServers, () =>
            disconnect.fail(t("leaves.disconnectFailed", { app: server.name }), () => disconnectServer(server)),
        );
    };

    // Usage counts (T6, T13): the one switch, moved here from About.
    const usage = useTelemetryPreference();
    const usageChange = useSettingsFailure();

    const briefs = summary?.assistantBriefs;
    const answers = summary?.assistantAnswers;
    const assistantCounted = typeof briefs === "number" && typeof answers === "number";

    const web = leaves?.webSearches;
    const webN = weekCount(web, t);
    const email = leaves?.email;
    const emailN = weekCount(email, t);
    const texts = leaves?.texts;
    const textsN = weekCount(texts, t);
    const ask = leaves?.agentsAsk;
    const askN = weekCount(ask, t);
    const chat = leaves?.chatApps;
    const chatN = weekCount(chat, t);
    const chatNames = (chat?.apps ?? []).map((app) => t((CHAT_APP_TITLE[app] ?? "leaves.chatAppsTitle") as never) as string);
    const finding = summary?.findingLeaves;
    const findingN = typeof finding?.thisWeek === "number" ? String(finding.thisWeek) : null;

    return (
        <SettingsCard title={t("yourData.leaves")}>
            <Row
                id="assistant"
                title={t("leaves.assistant", { assistant })}
                failure={summary && !assistantCounted ? countFailure : null}
                control={
                    <Button size="sm" variant="outline" onClick={() => setSeeing(true)}>
                        {t("leaves.seeThem")}
                    </Button>
                }
            >
                {assistantCounted ? t("leaves.assistantSub", { n: briefs, m: answers }) : t("leaves.assistantSubPlain")}
            </Row>
            <AssistantGivenSheet open={seeing} onOpenChange={setSeeing} assistant={assistant} />

            {web && (
                <Row
                    id="web"
                    title={t("leaves.web", { service: t(`leaves.searchService.${web.service || "none"}` as never) })}
                    failure={webN === null ? countFailure : findsFailed ? { text: t("leaves.readFailed"), retry: readFinds } : finds.failure}
                    control={findsOn !== null && (
                        <Switch aria-label={t("leaves.webSwitch")} checked={findsOn} onCheckedChange={changeFinds} />
                    )}
                >
                    {webN === null ? t("leaves.webSubPlain") : t("leaves.webSub", { n: webN })}
                </Row>
            )}

            {email && (
                <Row id="email" title={t("leaves.email")} failure={emailN === null ? countFailure : null}>
                    {(() => {
                        const address = email.addressMasked || t("leaves.emailYourAddress");
                        return emailN === null ? t("leaves.emailSubPlain", { address }) : t("leaves.emailSub", { address, n: emailN });
                    })()}
                </Row>
            )}

            {texts && (
                <Row id="texts" title={t("leaves.texts")} failure={texts.namesOn && textsN === null ? countFailure : null}>
                    {!texts.namesOn ? t("leaves.textsFixed") : textsN === null ? t("leaves.textsNamesPlain") : t("leaves.textsNames", { n: textsN })}
                </Row>
            )}

            <Row id="agents" title={t("yourData.agents")} failure={ask && askN === null ? countFailure : null}>
                <span className="block">{agentsLine}</span>
                {ask && (
                    <span className="block" data-your-data-agents-ask>
                        {askN === null ? t("leaves.agentsAskPlain") : t("leaves.agentsAsk", { n: askN })}
                        {(ask.byAgent ?? []).length > 0 && (
                            <>
                                {": "}
                                {(ask.byAgent ?? [])
                                    .map((row) => t("leaves.agentAsked", { agent: agentNames(row.agent), n: weekCount(row, t) ?? "" }))
                                    .join(", ")}
                            </>
                        )}
                    </span>
                )}
            </Row>

            {chat && chat.apps.length > 0 && (
                <div data-your-data-row="chatApps">
                    <SettingsRow
                        title={t("leaves.chatAppsTitle")}
                        description={
                            chatN === null
                                ? t("leaves.chatAppsPlain", { apps: chatNames.join(", "), app: chatNames.join(", ") })
                                : t("leaves.chatApps", { apps: chatNames.join(", "), app: chatNames.join(", "), n: chatN })
                        }
                        failure={chatN === null ? countFailure : disconnect.failure}
                    />
                    {chat.apps.map((app, i) => (
                        <SettingsRow
                            key={app}
                            title={chatNames[i]}
                            control={<DisconnectButton appName={chatNames[i]} onConfirm={() => disconnectApp(app, chatNames[i])} />}
                        />
                    ))}
                </div>
            )}

            {serversFailed && (
                <div data-your-data-row="toolServers">
                    <SettingsFailureLine failure={{ text: t("leaves.toolServersFailed"), retry: readServers }} />
                </div>
            )}
            {servers && servers.length > 0 && (
                <div data-your-data-row="toolServers">
                    <SettingsRow title={t("leaves.toolServers", { names: servers.map((server) => server.name).join(", ") })} failure={disconnect.failure} />
                    {servers.map((server) => (
                        <SettingsRow
                            key={server.id}
                            title={server.name}
                            control={<DisconnectButton appName={server.name} onConfirm={() => disconnectServer(server)} />}
                        />
                    ))}
                </div>
            )}

            {finding && (
                <Row
                    id="finding"
                    title={finding.service === "ollama" && finding.address ? t("leaves.ollamaAt", { address: finding.address }) : t("leaves.openai")}
                    failure={findingN === null ? countFailure : null}
                >
                    {findingN === null ? t("leaves.openaiSubPlain") : t("leaves.openaiSub", { n: findingN })}
                </Row>
            )}

            <Row
                id="usage"
                title={t("leaves.usageTo")}
                failure={usage.readFailed ? { text: t("leaves.readFailed"), retry: usage.read } : usageChange.failure}
                control={!usage.readFailed && !usage.loading && (
                    <Switch
                        aria-label={t("leaves.usageTo")}
                        checked={usage.enabled}
                        disabled={usage.saving}
                        onCheckedChange={(next) => {
                            usageChange.clear();
                            void usage.setEnabled(next).catch(() => usageChange.fail(t("leaves.usageFailed")));
                        }}
                    />
                )}
            />

            {extraRows}
        </SettingsCard>
    );
}
