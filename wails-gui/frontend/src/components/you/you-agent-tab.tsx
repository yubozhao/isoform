import { useTranslation } from 'react-i18next';
import { isOwnerState, useOwnerSnapshot } from '@/components/settings/pages/agent-of-you/aoy-shared';
import { EventsEmit } from '@/lib/wails-shim-runtime';

// You › Agent of You (computer-you v1.0.0 YO1, changed by computer-settings
// v1.1.0 rules 4 and 5). Until Agent of You is open to this owner it says
// "Soon: ...". Once the owner is approved it points to Settings › Agent of
// You, where set-up and records live (the owner page, agent-of-you-beta
// v2.1.11). The claims Agent of You may say, each with Right? and its switch,
// come here once claims carry that tag (redesign-you v1.0.0 rules 10 and 11).

/** True once this person is an approved owner: setting up, on or paused. */
export function useAgentOfYouOpen(): boolean {
    const status = useOwnerSnapshot('status');
    return isOwnerState(status.data?.state);
}

export function YouAgentTab() {
    const { t } = useTranslation('memory');
    const open = useAgentOfYouOpen();
    return (
        <div className="flex min-h-0 w-full flex-1 flex-col overflow-y-auto">
            <div className="flex w-full max-w-[760px] flex-col gap-4 px-10 py-6">
                {open ? (
                    <button
                        type="button"
                        data-testid="you-agent-settings-link"
                        className="w-fit text-left text-sm text-accent hover:underline underline-offset-2"
                        onClick={() => {
                            try {
                                EventsEmit('settings:open', 'your-agent');
                            } catch {
                                // No runtime in this build or test.
                            }
                        }}
                    >
                        {t('you.aoySetupLink')}
                    </button>
                ) : (
                    <p data-testid="you-agent-soon" className="m-0 text-sm text-muted">{t('you.agentSoon')}</p>
                )}
            </div>
        </div>
    );
}
