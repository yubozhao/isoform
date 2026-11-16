import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { getChatSources, type MainChatReportTarget } from '@/lib/stream-helpers';
import { mainChatTargetOpens } from '@/lib/main-chat-navigation';
import type { StreamItem } from '@/types';

/**
 * Under an answer (phone-chat v1.0.0 rule 4): where it came from, one chip per
 * source, "From your notes · Pricing with Sam ›", opening that source.
 */
export function ChatSourcesLine({ item, onOpenTarget, compact = false }: { item: StreamItem; onOpenTarget?: (target: MainChatReportTarget) => void; compact?: boolean }) {
    const { t } = useTranslation('chat');
    const payload = getChatSources(item);
    if (!payload) return null;
    return (
        <div data-chat-sources className={cn('flex flex-wrap gap-x-3 gap-y-1 text-[12.5px] text-muted', compact ? '' : 'max-w-[90%] mx-auto')}>
            {payload.sources.map((source) => {
                const label = t('mainChat.sources.from', { source: t(`mainChat.sources.${source.kind}`), title: source.title });
                // Named "Source: …" for screen readers (pass-access v1.0.0, Chat 3).
                const name = t('mainChat.sources.aria', { source: t(`mainChat.sources.${source.kind}`), title: source.title });
                return onOpenTarget && mainChatTargetOpens(source.target) ? (
                    <button key={`${source.kind}:${source.id}`} type="button" aria-label={name} data-chat-source={source.kind} className="text-left transition-colors hover:text-ink" onClick={() => onOpenTarget(source.target)}>
                        {label}
                    </button>
                ) : (
                    <span key={`${source.kind}:${source.id}`} aria-label={name} data-chat-source={source.kind}>{label}</span>
                );
            })}
        </div>
    );
}
