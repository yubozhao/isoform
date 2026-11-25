import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { assistantAgentId } from "@/components/agents/agents-model";
import { useFeatureSwitches } from "@/hooks/useFeatureSwitches";
import { ModelAccountsSettings } from "@/components/model-accounts-card";
import { SettingsCard, SettingsRow } from "@/components/settings/settings-primitives";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import {
    GetAIProviderConfig,
    GetFindingProvider,
    GetModelAccountsStatus,
    GetYourAgents,
    ListPlugins,
    SetBuiltinOn,
    SetFindingProvider,
    type PluginInfo,
    type FindingProviderView,
    type WorkOnItKind,
    type YourAgents,
} from "@/lib/wails-shim-app";
import { AIProviderSettingsCard } from "./assistant-page";

/**
 * Which coding agent is also Isoform's own assistant, or "" (G1). The two
 * reads load on their own; one that fails leaves the answer "".
 */
export function useIsoformAssistant(): string {
    const [mode, setMode] = useState<{ mode?: string; order?: string[] }>({});
    const [provider, setProvider] = useState<string | undefined>(undefined);
    useEffect(() => {
        let live = true;
        GetModelAccountsStatus().then(
            (status) => live && setMode({ mode: status.mode, order: status.accountOrder }),
            () => {},
        );
        GetAIProviderConfig().then(
            (config) => live && setProvider(config.provider),
            () => {},
        );
        return () => {
            live = false;
        };
    }, []);
    return assistantAgentId(mode.mode, mode.order, provider);
}

/** The job an agent holds in Your agents, if any. */
export function jobHeldBy(agents: YourAgents | null, agentId: string): WorkOnItKind | undefined {
    if (!agents || !agentId) return undefined;
    return (Object.keys(agents.choices) as WorkOnItKind[]).find((kind) => agents.choices[kind] === agentId);
}

/**
 * Settings › Your AI (computer conformance answers v1.0.0 G1): the AI that
 * Isoform itself reads your day with, apart from the agents that do your
 * work. It holds the model accounts; when the same app is also one of Your
 * agents, it says so. Rendered by the Settings registry's "Your AI" entry
 * (the Settings owner's), so it takes no props.
 */
export function YourAISection() {
    const { t } = useTranslation("settings");
    const assistantId = useIsoformAssistant();
    const [agents, setAgents] = useState<YourAgents | null>(null);
    useEffect(() => {
        let live = true;
        GetYourAgents().then(
            (next) => live && setAgents(next),
            () => {},
        );
        return () => {
            live = false;
        };
    }, []);
    const assistant = agents?.agents.find((agent) => agent.id === assistantId);
    const job = jobHeldBy(agents, assistantId);
    // With Chat off (scope cut 2026-11), Isoform writes no answers.
    const chatOn = useFeatureSwitches().chat;
    return (
        <>
            {assistant && (
                <SettingsCard>
                    <p className="settings-row-desc text-ink" data-your-ai-lead>
                        {t(chatOn ? "yourAI.lead" : "yourAI.leadNoChat", { assistant: assistant.name })}
                    </p>
                    {job && (
                        <p className="settings-row-desc" data-your-ai-also>
                            {t("yourAI.alsoAgent", { app: assistant.name, job: job === "code" ? t("yourAI.jobCode") : t("yourAI.jobElse") })}
                        </p>
                    )}
                </SettingsCard>
            )}
            <FindingRow />
            <OvernightReviewRow assistant={assistant?.name ?? ""} />
            <ModelAccountsSettings ownKeyForm={<AIProviderSettingsCard flat />} />
        </>
    );
}

type FindingChoice = FindingProviderView["choice"];
const FINDING_CHOICES: readonly FindingChoice[] = ["local", "ollama", "openai"];
/** Ollama's own default address, filled in when Ollama is picked. */
export const OLLAMA_DEFAULT_ADDRESS = "http://localhost:11434";
const INPUT_CLASS =
    "w-full rounded-md border border-border bg-panel px-3 py-1.5 text-xs text-ink placeholder:text-muted focus:outline-none focus:ring-1 focus:ring-accent";

/**
 * Finding things in what Isoform keeps (computer conformance answers v1.2.0
 * T8): one row, the service that makes what Isoform keeps findable, with
 * Change. On this computer is the default; "Automatic" is gone, so nothing
 * sends text away unless the person picks OpenAI. The key field shows only
 * once OpenAI is picked, and the binding never returns the key.
 */
export function FindingRow() {
    const { t } = useTranslation("settings");
    const [view, setView] = useState<FindingProviderView | null>(null);
    const [failed, setFailed] = useState(false);
    const [picking, setPicking] = useState<FindingChoice | null>(null);
    const [key, setKey] = useState("");
    const [address, setAddress] = useState(OLLAMA_DEFAULT_ADDRESS);
    const [saving, setSaving] = useState(false);

    const read = () => {
        GetFindingProvider().then(
            (next) => {
                setView(next);
                setFailed(false);
            },
            () => setFailed(true),
        );
    };
    useEffect(read, []);

    const save = async (choice: FindingChoice, apiKey = "", baseURL = "") => {
        setSaving(true);
        try {
            setView(await SetFindingProvider(choice, apiKey, baseURL));
            setPicking(null);
            setKey("");
            setFailed(false);
        } catch {
            setFailed(true);
        } finally {
            setSaving(false);
        }
    };

    const pick = (choice: FindingChoice) => {
        setFailed(false);
        switch (choice) {
            case "local":
                void save("local");
                return;
            case "ollama":
                setAddress(view?.choice === "ollama" && view.address ? view.address : OLLAMA_DEFAULT_ADDRESS);
                setPicking("ollama");
                return;
            case "openai":
                setKey("");
                setPicking("openai");
                return;
        }
    };

    const current = (): string => {
        switch (view?.choice) {
            case "ollama":
                return view.onThisComputer || !view.address ? t("yourAI.finding.ollamaHere") : t("yourAI.finding.ollamaAt", { address: view.address });
            case "openai":
                return t("yourAI.finding.openai");
            default:
                return t("yourAI.finding.here");
        }
    };

    return (
        <div className="flex flex-col gap-2" data-your-ai-finding>
            <SettingsRow
                title={t("yourAI.finding.title")}
                description={view ? current() : undefined}
                failure={failed ? { text: t("yourAI.finding.failed"), retry: view ? undefined : read } : null}
                control={
                    <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                            <Button variant="outline" size="sm" disabled={!view || saving} data-your-ai-finding-change>
                                {t("yourAI.finding.change")}
                            </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                            {FINDING_CHOICES.map((choice) => (
                                <DropdownMenuItem key={choice} onSelect={() => pick(choice)} data-your-ai-finding-pick={choice}>
                                    {t(`yourAI.finding.choice.${choice}`)}
                                </DropdownMenuItem>
                            ))}
                        </DropdownMenuContent>
                    </DropdownMenu>
                }
            />
            {picking === "openai" && (
                <div className="flex flex-col gap-1.5" data-your-ai-finding-openai>
                    <label htmlFor="your-ai-finding-key" className="text-xs font-medium text-ink">
                        {t("yourAI.finding.keyLabel")}
                    </label>
                    <input
                        id="your-ai-finding-key"
                        type="password"
                        autoComplete="off"
                        className={`${INPUT_CLASS} font-mono`}
                        placeholder={view?.hasOpenAIKey ? t("yourAI.finding.keySaved") : ""}
                        value={key}
                        onChange={(e) => setKey(e.target.value)}
                    />
                    <p className="text-[11px] text-muted" data-your-ai-finding-leaves>
                        {t("yourAI.finding.leaves")}
                    </p>
                    <div className="flex gap-2">
                        <Button size="sm" disabled={saving || (!key.trim() && !view?.hasOpenAIKey)} onClick={() => void save("openai", key.trim())}>
                            {t("yourAI.finding.save")}
                        </Button>
                        <Button size="sm" variant="outline" onClick={() => setPicking(null)}>
                            {t("yourAI.finding.cancel")}
                        </Button>
                    </div>
                </div>
            )}
            {picking === "ollama" && (
                <div className="flex flex-col gap-1.5" data-your-ai-finding-ollama>
                    <label htmlFor="your-ai-finding-address" className="text-xs font-medium text-ink">
                        {t("yourAI.finding.addressLabel")}
                    </label>
                    <input
                        id="your-ai-finding-address"
                        type="url"
                        className={INPUT_CLASS}
                        value={address}
                        onChange={(e) => setAddress(e.target.value)}
                    />
                    <div className="flex gap-2">
                        <Button size="sm" disabled={saving || !address.trim()} onClick={() => void save("ollama", "", address.trim())}>
                            {t("yourAI.finding.save")}
                        </Button>
                        <Button size="sm" variant="outline" onClick={() => setPicking(null)}>
                            {t("yourAI.finding.cancel")}
                        </Button>
                    </div>
                </div>
            )}
        </div>
    );
}

/** The two builtins Overnight review is made of: the review on this computer, and its pass with Your AI. */
export const OVERNIGHT_IDS = ["memory-dream", "memory-dream-llm"] as const;

/**
 * Overnight review of what it knows (computer conformance answers v1.4.0
 * S17): one row and one switch over both memory-dream builtins. The part that
 * runs on this computer alone has no switch of its own; turning the row off
 * turns both off, and on turns both on (the review first, since its Your AI
 * pass depends on it).
 */
export function OvernightReviewRow({ assistant }: { assistant: string }) {
    const { t } = useTranslation("settings");
    const [plugins, setPlugins] = useState<PluginInfo[] | null>(null);
    const [failed, setFailed] = useState(false);
    const [readFailed, setReadFailed] = useState(false);
    const [saving, setSaving] = useState(false);

    const read = () => {
        setReadFailed(false);
        ListPlugins().then(
            (next) => setPlugins(next ?? []),
            () => setReadFailed(true),
        );
    };
    useEffect(read, []);

    const mine = (plugins ?? []).filter((p) => (OVERNIGHT_IDS as readonly string[]).includes(p.id));
    const on = mine.some((p) => p.enabled);

    const toggle = async (next: boolean) => {
        setSaving(true);
        try {
            const order = next ? OVERNIGHT_IDS : [...OVERNIGHT_IDS].reverse();
            let latest: PluginInfo[] = plugins ?? [];
            for (const id of order) latest = await SetBuiltinOn(id, next);
            setPlugins(latest);
            setFailed(false);
        } catch {
            setFailed(true);
            // A first setter may have saved before the second failed: show what is saved now.
            ListPlugins().then((next) => setPlugins(next ?? []), () => {});
        } finally {
            setSaving(false);
        }
    };

    return (
        <SettingsRow
            title={t("yourAI.overnight.title")}
            description={t("yourAI.overnight.line", { assistant: assistant || t("yourAI.overnight.yourAI") })}
            failure={
                readFailed
                    ? { text: t("yourAI.overnight.readFailed"), retry: read }
                    : failed
                      ? { text: t("yourAI.overnight.failed") }
                      : null
            }
            control={
                // Unread is never shown as off: no switch until the state is known.
                plugins === null ? null : <Switch
                    checked={on}
                    disabled={mine.length === 0 || saving}
                    onCheckedChange={(next) => void toggle(next)}
                    aria-label={t("yourAI.overnight.title")}
                    data-your-ai-overnight
                />
            }
        />
    );
}
