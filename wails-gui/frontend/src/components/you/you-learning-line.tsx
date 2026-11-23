import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ActivityInlineNotice } from '@/components/activity-inline-notice';
import { ACTIVITY_LEARNING_FEATURE_KEYS } from '@/components/activity-learning-settings';
import { useIsoformAssistant } from '@/components/settings/pages/your-ai-section';
import { LoadUIState, SaveUIState } from '@/lib/wails-shim-app';

// The learning switch at the foot of a You tab (computer-conformance-answers
// v1.4.0 S18): "Learning from your day: on · new lines come from summaries
// sent to {assistant}", with Turn off. Off stops new lines; what's there
// stays. The switch is the same ui_state key Settings used (Go reads it in
// activity_triage_prompt.go); a missing value means on, as the Go side reads
// it. A failed read or save says so with Try again (failed-read rule).

export type YouLearningFeature = 'memory' | 'knowledge';

function assistantWords(t: (key: 'aboutYou.portrait.assistants.claudeCode' | 'aboutYou.portrait.assistants.codex' | 'aboutYou.portrait.assistants.fallback') => string, id: string): string {
    switch (id) {
        case 'claude-code':
            return t('aboutYou.portrait.assistants.claudeCode');
        case 'codex':
            return t('aboutYou.portrait.assistants.codex');
        default:
            return t('aboutYou.portrait.assistants.fallback');
    }
}

export function YouLearningLine({ feature }: { feature: YouLearningFeature }) {
    const { t } = useTranslation('memory');
    const assistant = useIsoformAssistant();
    const key = ACTIVITY_LEARNING_FEATURE_KEYS[feature];
    const [on, setOn] = useState<boolean | null>(null);
    const [readFailed, setReadFailed] = useState(false);
    const [saveFailed, setSaveFailed] = useState<boolean | null>(null);

    const read = useCallback(() => {
        setReadFailed(false);
        LoadUIState(key)
            .then((value) => {
                if (typeof value !== 'string') throw new Error('invalid setting');
                setOn(value !== 'false');
            })
            .catch(() => setReadFailed(true));
    }, [key]);

    useEffect(() => { read(); }, [read]);

    const change = (next: boolean) => {
        setSaveFailed(null);
        setOn(next);
        SaveUIState(key, next ? 'true' : 'false').catch(() => {
            setOn(!next);
            setSaveFailed(next);
        });
    };

    if (readFailed) {
        return (
            <div data-testid={`you-learning-${feature}`} className="mt-2">
                <ActivityInlineNotice notice={{ tone: 'error', text: t('you.learningReadFailed'), onAction: read }} />
            </div>
        );
    }
    if (on === null) return null;
    return (
        <div data-testid={`you-learning-${feature}`} className="mt-2 flex flex-col gap-1 border-t border-border-subtle pt-4">
            <p className="m-0 flex flex-wrap items-baseline gap-x-2 text-[12.5px] text-muted">
                <span>{on ? t('you.learning', { assistant: assistantWords(t, assistant) }) : t('you.learningOff')}</span>
                <button
                    type="button"
                    onClick={() => change(!on)}
                    className="rounded-sm text-accent hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                >
                    {on ? t('you.turnOff') : t('you.turnOn')}
                </button>
            </p>
            {saveFailed !== null && (
                <ActivityInlineNotice notice={{ tone: 'error', text: t('you.learningSaveFailed'), onAction: () => change(saveFailed) }} />
            )}
        </div>
    );
}
