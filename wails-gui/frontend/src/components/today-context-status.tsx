import { useTranslation } from 'react-i18next';
import type { TodayChatPreview, TodayContextCounts } from '@/lib/today-chat-navigation';

function parseCounts(content: string | undefined): TodayContextCounts | null {
    try {
        const counts: unknown = JSON.parse(content ?? '');
        if (!counts || typeof counts !== 'object' || Array.isArray(counts)) return null;
        const { memories, activities, people } = counts as Record<string, unknown>;
        if (![memories, activities, people].every((count) => typeof count === 'number' && Number.isSafeInteger(count) && count >= 0)) {
            return null;
        }
        return { memories, activities, people } as TodayContextCounts;
    } catch {
        return null;
    }
}

/** What the message was attached to (phone-chat v1.0.0 rule 10): the title
 *  the draft showed, saved on the receipt. */
export function parseAbout(toolInput: unknown): { title: string; brief: boolean } | null {
    let raw = toolInput;
    if (typeof raw === 'string') {
        try { raw = JSON.parse(raw); } catch { return null; }
    }
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
    const { about, aboutKind } = raw as Record<string, unknown>;
    if (typeof about !== 'string' || !about.trim()) return null;
    return { title: about.trim(), brief: typeof aboutKind === 'string' && aboutKind.startsWith('brief') };
}

/** Only counts and what it was about are rendered: malformed or additional
 *  payload fields stay hidden. */
export function TodayContextStatus({ content, toolInput }: { content?: string; toolInput?: unknown }) {
    const { t } = useTranslation('chat');
    const counts = parseCounts(content);
    if (!counts) return null;
    const about = parseAbout(toolInput);

    return (
        <>
        {about ? (
            <p data-chat-about className="m-0 text-center text-[12px] leading-relaxed text-muted">
                {about.brief ? t('about.brief', { sentence: about.title }) : t('about.item', { title: about.title })}
            </p>
        ) : null}
        <p data-today-context-status className="m-0 text-center text-[12px] leading-relaxed text-muted/70">
            {t('todayContext.status', {
                memories: t('todayContext.memories', { count: counts.memories }),
                activities: t('todayContext.activities', { count: counts.activities }),
                people: t('todayContext.people', { count: counts.people }),
            })}
        </p>
        </>
    );
}

/** A draft preview is separate from the receipt of context actually sent. */
export function TodayContextPreview({ preview }: { preview?: TodayChatPreview }) {
    const { t } = useTranslation('chat');
    const { t: tToday } = useTranslation('today');
    if (!preview) return null;
    if (preview.status === 'paused') {
        return (
            <p data-today-context-preview role="status" className="m-0 mb-2 text-[12px] leading-relaxed text-muted/70">
                {tToday('contextNote.paused')}
            </p>
        );
    }
    if (preview.status === 'fallback') {
        // The item's own context could not be prepared: say so quietly and
        // name what the draft carries instead.
        return (
            <p data-today-context-preview role="status" className="m-0 mb-2 text-[12px] leading-relaxed text-muted/70">
                {tToday(preview.counts ? 'contextNote.withBrief' : 'contextNote.withoutContext')}
            </p>
        );
    }
    const counts = preview.status === 'ready' ? preview.counts : null;
    const message = counts
        ? counts.memories + counts.activities + counts.people === 0
            ? t('todayContext.empty')
            : t('todayContext.preview', {
                memories: t('todayContext.memories', { count: counts.memories }),
                activities: t('todayContext.activities', { count: counts.activities }),
                people: t('todayContext.people', { count: counts.people }),
            })
        : t(preview.status === 'loading' ? 'todayContext.loading' : 'todayContext.unavailable');
    return <p data-today-context-preview role="status" className="m-0 mb-2 text-[12px] leading-relaxed text-muted/70">{message}</p>;
}
