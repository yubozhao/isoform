import { useEffect, useMemo, type ComponentProps, type ReactNode } from 'react';
import { MainChatSidePanelView } from './main-chat-side-panel';
import { useMainChatSidePanel } from '@/lib/main-chat-side-panel';
import { ChatFailureLine } from '@/components/chat-failure-line';
import { useChatPlaceFailure } from '@/lib/chat-failure';
import { useTranslation } from 'react-i18next';
import { FirstDayChatStarters } from './first-day-starters';
import { StreamBody } from '@/components/stream-body';
import type { MainChatView } from '@/hooks/useMainChatView';
import { MainChatHeader } from './main-chat-header';
import type { MainChatStreamView } from './main-chat-view-context';
import { MainChatItemPanel } from '@/components/main-chat-item-panel';
import { useMainChatItemPanel } from '@/hooks/useMainChatItemPanel';
import { getMainChatProactive, type MainChatReportTarget } from '@/lib/stream-helpers';
import { chatThreadUpdates } from '@/lib/main-chat-view';
import type { StreamItem } from '@/types';

type StreamBodyProps = ComponentProps<typeof StreamBody>;

/** Main chat keeps a readable column regardless of the other readers' width preference. */
export function MainChatPageColumn({ enabled, children, onOpenTarget }: { enabled: boolean; children: ReactNode; onOpenTarget?: (target: MainChatReportTarget) => void }) {
    const panel = useMainChatItemPanel('chat');
    const side = useMainChatSidePanel();
    const placeFailure = useChatPlaceFailure();
    const { t } = useTranslation('chat');
    const panelOpen = enabled && Boolean(panel.openId);
    const sideOpen = enabled && !panelOpen && side !== null;
    return (
        <div
            data-testid={enabled ? 'main-chat-column' : undefined}
            className="mx-auto flex min-w-0 min-h-0 w-full max-w-[1188px] flex-1 flex-row"
        >
            <div className="mx-auto flex min-w-0 min-h-0 w-full max-w-[768px] flex-1 flex-col">
                {enabled && placeFailure && (
                    <ChatFailureLine testId="place" className="px-6 pt-3" text={t(placeFailure.key)} onRetry={placeFailure.retry} />
                )}
                {children}
            </div>
            {panelOpen && <MainChatItemPanel surface="chat" onOpenTarget={onOpenTarget} />}
            {sideOpen && side && <MainChatSidePanelView panel={side} />}
        </div>
    );
}

/**
 * The chat's stream. For the main chat it adds Chat's header (title and
 * what it's for) above the scroll area, the summary line, day dividers and
 * folds. Chat is one thread: no filter or topic view (computer-chat v1.0.0
 * rule 2). Any other chat is the plain StreamBody.
 */
export function MainChatStreamBody({ mainChat, onPrepare, ...props }: StreamBodyProps & {
    /** The main chat's view; absent for every other chat. */
    mainChat?: MainChatView;
    /** Sends words as the person's message at once (an answer chip or a starter, Chat K4). */
    onPrepare?: (text: string) => void | boolean | Promise<boolean>;
}) {
    const view = mainChat;
    const today = view?.today ?? '';
    const enabled = Boolean(view);
    const hideRoutine = view?.hideRoutine ?? false;
    const topicNames = view?.topicNames;
    const chips = view?.chips;
    const firstReadId = props.items?.find((item) => getMainChatProactive(item)?.kind === 'first_read')?.id;
    const streamView = useMemo<MainChatStreamView | undefined>(() => enabled && today && topicNames && chips ? {
        today,
        hideRoutine,
        topicNames,
        chips,
        onFillChip: onPrepare,
        chipsSend: Boolean(onPrepare),
        firstReadStarter: firstReadId ? { itemId: firstReadId, items: props.items } : undefined,
    } : undefined, [enabled, today, hideRoutine, topicNames, chips, onPrepare, firstReadId, props.items]);
    // Chat opens on the person's conversation (chatThreadUpdates): when the
    // newest page holds only Isoform's updates, which Chat does not show, the
    // older pages load until a conversation row arrives or none are left.
    const quiet = enabled && !chatThreadUpdates.shown;
    const conversationLoaded = Boolean(props.items?.some(isConversationRow));
    const { hasMore, loadingMore, onLoadMore } = props;
    useEffect(() => {
        if (quiet && !conversationLoaded && hasMore && !loadingMore) onLoadMore?.();
    }, [quiet, conversationLoaded, hasMore, loadingMore, onLoadMore, props.items]);

    if (!view || !streamView) return <StreamBody {...props} />;

    // The first day: Isoform's starters under the messages (handoff §4).
    const starters = enabled && !firstReadId ? <FirstDayChatStarters items={props.items} onPrepare={onPrepare} /> : null;
    return (
        <StreamBody
            {...props}
            toolbar={<MainChatHeader />}
            mainChatView={streamView}
            fillViewport={quiet}
            footer={props.footer || starters ? <>{props.footer}{starters}</> : undefined}
        />
    );
}

/** A row of the person's own conversation, as buildMainChatDays reads one. */
function isConversationRow(item: StreamItem): boolean {
    return item.rowClass === 'conversation' || item.kind === 'user' || (item.kind === 'assistant' && !item.toolName);
}
