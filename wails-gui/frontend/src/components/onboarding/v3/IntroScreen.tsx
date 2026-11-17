import { useTranslation } from "react-i18next";
import { CornerDownLeft, Mic, Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { SETUP_V3_INTRO, type SetupV3Intro } from "./steps";

// The four screens (computer setup v1.0.0 rules 1 to 3): the real thing on
// the left with "Sample" under it; the words, page dots and Continue on the
// right; Skip at the top right. Nothing is asked here.

/** Agents named on screen 2; product names stay untranslated. */
const SAMPLE_AGENTS = ["Claude", "ChatGPT", "Codex", "Muse", "Instinct"];

function BriefSample() {
    const { t } = useTranslation("onboarding");
    return (
        <div className="sv3-card" data-testid="setup-v3-sample-brief">
            <div className="sv3-card-head">
                <span>{t("setup.samples.brief.kind")}</span>
                <span className="sv3-quiet">{t("setup.samples.brief.time")}</span>
            </div>
            <p>{t("setup.samples.brief.line1")}</p>
            <p>{t("setup.samples.brief.line2")}</p>
            <span className="sv3-chip">
                <Play size={12} aria-hidden />
                {t("setup.samples.brief.listen")}
            </span>
        </div>
    );
}

function ItemSample() {
    const { t } = useTranslation("onboarding");
    return (
        <div className="sv3-card" data-testid="setup-v3-sample-item">
            <p className="sv3-strong">{t("setup.samples.item.title")}</p>
            <p className="sv3-quiet">{t("setup.samples.item.why")}</p>
            <div className="sv3-answers">
                <span className="sv3-key sv3-key-on">{t("setup.samples.item.yes")}</span>
                <span className="sv3-key">{t("setup.samples.item.no")}</span>
                <span className="sv3-key">{t("setup.samples.item.notMine")}</span>
            </div>
            <p className="sv3-quiet">
                {t("ob.2.agents")}: {SAMPLE_AGENTS.join(" · ")}
            </p>
        </div>
    );
}

function TalkSample() {
    const { t } = useTranslation("onboarding");
    return (
        <div className="sv3-card" data-testid="setup-v3-sample-talk">
            <div className="sv3-card-head">
                <span className="sv3-strong">
                    <Mic size={12} aria-hidden /> {t("setup.samples.talk.title")}
                </span>
                <span className="sv3-quiet">{t("setup.samples.talk.time")}</span>
            </div>
            <p>{t("setup.samples.talk.note")}</p>
            <span className="sv3-chip sv3-chip-wait">{t("setup.samples.talk.toAnswer")}</span>
        </div>
    );
}

function EyesSample() {
    const { t } = useTranslation("onboarding");
    return (
        <div className="sv3-card" data-testid="setup-v3-sample-eyes">
            <div className="sv3-card-head">
                <span className="sv3-strong">{t("setup.samples.eyes.name")}</span>
                <Switch checked={false} disabled aria-label={t("setup.samples.eyes.name")} />
            </div>
            <p className="sv3-quiet">{t("setup.samples.eyes.kept")}</p>
        </div>
    );
}

const WORDS = {
    1: { title: "ob.1.title", body: "ob.1.body" },
    2: { title: "ob.2.title", body: "ob.2.body" },
    3: { title: "ob.3.title", body: "ob.3.body" },
    4: { title: "ob.4.title", body: "ob.4.body" },
} as const;

const SAMPLES: Record<SetupV3Intro, () => JSX.Element> = {
    1: BriefSample,
    2: ItemSample,
    3: TalkSample,
    4: EyesSample,
};

export function IntroScreen({
    screen,
    onContinue,
    onSkip,
}: {
    screen: SetupV3Intro;
    onContinue: () => void;
    onSkip: () => void;
}) {
    const { t } = useTranslation("onboarding");
    const Sample = SAMPLES[screen];
    return (
        <section className="sv3-intro" data-testid={`setup-v3-intro-${screen}`}>
            <Button variant="quiet" size="sm" className="sv3-skip" onClick={onSkip} data-testid="setup-v3-skip">
                {t("ob.skip")}
            </Button>
            <div className="sv3-sample">
                <Sample />
                <span className="sv3-sample-label">{t("setup.sample")}</span>
            </div>
            <div className="sv3-words">
                {screen === 3 && <span className="sv3-label">{t("ob.3.label")}</span>}
                <h1 className="sv3-title display">{t(WORDS[screen].title)}</h1>
                <p className="sv3-body">{t(WORDS[screen].body)}</p>
                <div className="sv3-dots" aria-hidden>
                    {SETUP_V3_INTRO.map((n) => (
                        <i key={n} className={n === screen ? "on" : undefined} />
                    ))}
                </div>
                <Button onClick={onContinue} data-testid="setup-v3-continue">
                    {t("ob.continue")}
                    <CornerDownLeft size={14} aria-hidden />
                </Button>
            </div>
        </section>
    );
}
