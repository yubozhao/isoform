import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { getChatSources, type ChatSource, type MainChatReportTarget } from '@/lib/stream-helpers';
import { mainChatTargetOpens } from '@/lib/main-chat-navigation';
import { computerDayClock } from '@/lib/brief-time';
import type { StreamItem } from '@/types';

/** When a source happened, in the computer's clock and the person's
 *  language: "2:05 PM", "Yesterday, 2:05 PM". Null when it has no time. */
function sourceTime(source: ChatSource, language: string): string | null {
    if (typeof source.at !== 'number' || !Number.isFinite(source.at) || source.at <= 0) return null;
    return computerDayClock(new Date(source.at), language);
}

/** Where a source opens. A watch conversation opens on its own day, read
 *  from when it happened on this computer's clock. */
export function sourceTarget(source: ChatSource): MainChatReportTarget {
    if (source.target.kind !== 'watch' || typeof source.at !== 'number' || !Number.isFinite(source.at) || source.at <= 0) return source.target;
    const d = new Date(source.at);
    const day = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
    return { ...source.target, day };
}

/**
 * Under an answer (phone-chat v1.0.0 rule 4, computer-chat v1.2.0 rule 5):
 * where it came from, one chip per thing it read, "From your watch · Pricing
 * with Sam, 2:05 PM ›", opening that source. A note or a person has no time
 * and reads "From your notes · Pricing with Sam ›".
 */
export function ChatSourcesLine({ item, onOpenTarget, compact = false }: { item: StreamItem; onOpenTarget?: (target: MainChatReportTarget) => void; compact?: boolean }) {
    const { t, i18n } = useTranslation('chat');
    const payload = getChatSources(item);
    if (!payload) return null;
    return (
        <div data-chat-sources className={cn('flex flex-wrap gap-x-3 gap-y-1 text-[12.5px] text-muted', compact ? '' : 'max-w-[90%] mx-auto')}>
            {payload.sources.map((source) => {
                const sourceName = t(`mainChat.sources.${source.kind}`);
                const time = sourceTime(source, i18n.language);
                const label = time
                    ? t('mainChat.sources.fromAt', { source: sourceName, title: source.title, time })
                    : t('mainChat.sources.from', { source: sourceName, title: source.title });
                // Named "Source: …" for screen readers (pass-access v1.0.0, Chat 3).
                const name = time
                    ? t('mainChat.sources.ariaAt', { source: sourceName, title: source.title, time })
                    : t('mainChat.sources.aria', { source: sourceName, title: source.title });
                return onOpenTarget && mainChatTargetOpens(source.target) ? (
                    <button key={`${source.kind}:${source.id}`} type="button" aria-label={name} data-chat-source={source.kind} className="text-left transition-colors hover:text-ink" onClick={() => onOpenTarget(sourceTarget(source))}>
                        {label}
                    </button>
                ) : (
                    <span key={`${source.kind}:${source.id}`} aria-label={name} data-chat-source={source.kind}>{label}</span>
                );
            })}
        </div>
    );
}
