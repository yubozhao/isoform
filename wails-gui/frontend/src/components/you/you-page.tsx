import { createContext, useContext, useId, useState, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { YouAgentTab, useAgentOfYouOpen } from './you-agent-tab';

// You (computer-you v1.0.0): one page with tabs About you, How you do things,
// Who you know and Agent of You (Soon), in place of three sidebar rows. The
// page under each tab is the one that was there; every line on it is a claim
// to judge with Yes, No and Edit (Y, N and E). Agent of You is its own tab
// (you-agent-tab.tsx): the Soon line until the owner is approved, then the
// way to Settings › Agent of You.

export type YouTab = 'about' | 'howYouDo' | 'people' | 'agent';

/** True under You's tabs: the page there drops its own title, since You's
 *  header is the one header (computer-you v1.0.0). */
const InsideYouContext = createContext(false);
export function useInsideYou(): boolean {
    return useContext(InsideYouContext);
}
/** The tabs App routes (each is its own retained page); Agent of You is the page's own. */
export type YouPageTab = Exclude<YouTab, 'agent'>;

export const YOU_TABS: readonly YouTab[] = ['about', 'howYouDo', 'people', 'agent'];

const LABEL = {
    about: 'you.tabAbout',
    howYouDo: 'you.tabHowYouDo',
    people: 'you.tabPeople',
    agent: 'you.tabAgent',
} as const;

export function YouPage({ tab: routed, onTabChange, children }: {
    tab: YouPageTab;
    onTabChange: (tab: YouPageTab) => void;
    /** The page under the tab; Agent of You brings its own. */
    children?: ReactNode;
}) {
    const { t } = useTranslation('memory');
    // Agent of You is marked Soon until it is open to this owner.
    const aoyOpen = useAgentOfYouOpen();
    const ids = useId();
    const [agent, setAgent] = useState(false);
    const tab: YouTab = agent ? 'agent' : routed;
    const pick = (next: YouTab) => {
        setAgent(next === 'agent');
        if (next !== 'agent' && next !== routed) onTabChange(next);
    };
    const move = (from: YouTab, step: 1 | -1) => {
        const next = YOU_TABS[(YOU_TABS.indexOf(from) + step + YOU_TABS.length) % YOU_TABS.length];
        pick(next);
        document.getElementById(`${ids}-tab-${next}`)?.focus();
    };
    return (
        <div data-testid="you-page" className="flex h-full min-h-0 w-full flex-col bg-background text-ink">
            <header className="flex flex-col gap-2 px-10 pt-9 pb-4">
                <h1 className="m-0 text-[26px] font-normal leading-[1.3] text-ink">{t('you.title')}</h1>
                <p className="m-0 text-sm text-muted">{t('you.sub')}</p>
                <div
                    role="tablist"
                    aria-label={t('you.tabsLabel')}
                    className="mt-3 flex w-fit max-w-full flex-wrap gap-0.5 rounded-[10px] border border-border bg-panel-warm p-[3px]"
                >
                    {YOU_TABS.map((id) => (
                        <button
                            key={id}
                            id={`${ids}-tab-${id}`}
                            type="button"
                            role="tab"
                            data-you-tab={id}
                            aria-selected={tab === id}
                            aria-controls={`${ids}-panel`}
                            tabIndex={tab === id ? 0 : -1}
                            onClick={() => pick(id)}
                            onKeyDown={(event) => {
                                if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
                                event.preventDefault();
                                move(id, event.key === 'ArrowRight' ? 1 : -1);
                            }}
                            className={cn(
                                'flex h-[30px] items-center gap-1.5 rounded-lg px-3 text-[13px] font-medium text-ink transition-colors duration-[120ms]',
                                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus',
                                tab === id ? 'bg-surface-active' : 'hover:bg-surface-hover',
                            )}
                        >
                            {t(LABEL[id])}
                            {id === 'agent' && !aoyOpen && (
                                <span data-testid="you-agent-tag" className="rounded-full border border-border px-1.5 text-[11px] font-normal text-muted">{t('you.soon')}</span>
                            )}
                        </button>
                    ))}
                </div>
            </header>
            <div id={`${ids}-panel`} role="tabpanel" aria-labelledby={`${ids}-tab-${tab}`} className="flex min-h-0 flex-1">
                {tab === 'agent' ? (
                    <YouAgentTab />
                ) : (
                    <InsideYouContext.Provider value>{children}</InsideYouContext.Provider>
                )}
            </div>
        </div>
    );
}
