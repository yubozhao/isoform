import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';

/**
 * Chat's header (computer-chat v1.0.0 rules 1 and 2, phone-chat v1.0.0
 * rule 1): the title and what Chat is for. Chat is one thread, so there are
 * no filters, topic chips or view toggles here.
 */
export function MainChatHeader() {
    const { t } = useTranslation('chat');
    return (
        <header className="window-drag shrink-0 px-6 pt-6 pb-3" data-testid="main-chat-header">
            <h1 className="text-[26px] font-normal leading-[1.3] text-ink">{t('mainChat.view.title')}</h1>
            <p className="mt-1 text-[14px] text-muted">{t('mainChat.view.sub')}</p>
        </header>
    );
}

/** Today's sourced counts: conversations, finished, routine. The summary
 *  scrolls with the cards. */
export function MainChatSummary({
    counts,
    failed,
    onRetry,
}: {
    counts: { conversations: number | null; finished: number | null; routine: number | null } | null;
    failed: boolean;
    onRetry: () => void;
}) {
    const { t } = useTranslation('chat');
    if (!counts) {
        if (!failed) return null;
        return (
            <div className="flex flex-nowrap items-center gap-2 overflow-x-auto whitespace-nowrap rounded-lg bg-panel-warm px-3.5 py-2.5 text-[12.5px] text-muted" data-testid="main-chat-summary">
                <span role="alert" className="shrink-0">{t('mainChat.view.summaryFailed')}</span>
                <Button variant="link" className="h-auto shrink-0 p-0 text-[12.5px] font-medium" onClick={onRetry}>
                    {t('mainChat.view.retry')}
                </Button>
            </div>
        );
    }
    const items = [
        { key: 'conversations', text: counts.conversations !== null && counts.conversations > 0 ? t('mainChat.view.summaryConversations', { count: counts.conversations }) : null },
        { key: 'finished', text: counts.finished !== null && counts.finished > 0 ? t('mainChat.view.summaryFinished', { count: counts.finished }) : null },
        { key: 'routine', text: counts.routine !== null && counts.routine > 0 ? t('mainChat.view.summaryRoutine', { count: counts.routine }) : null },
    ].filter((item) => item.text !== null);
    return (
        <div
            className="overflow-x-auto whitespace-nowrap rounded-lg bg-panel-warm px-3.5 py-2.5 text-[12.5px] text-muted"
            data-testid="main-chat-summary"
        >
            <span className="font-semibold text-ink">{t('mainChat.view.summaryTitle')}</span>
            <span data-testid="main-chat-summary-counts">
                {items.map((item) => (
                    <span key={item.key}>
                        {t('mainChat.view.summarySeparator')}
                        <span>{item.text}</span>
                    </span>
                ))}
            </span>
        </div>
    );
}
