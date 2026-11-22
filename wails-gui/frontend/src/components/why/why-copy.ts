import type { TFunction } from 'i18next';
import type { WhySource, WhySummary } from '@/lib/wails-shim-app';
import { DOT_CLASS } from '@/components/activity-feed';

/** Each source's dot: Activity's feed colours where the feed has the source. */
export const WHY_DOT_CLASS: Record<WhySource, string> = {
    screen: DOT_CLASS.screen,
    watch: DOT_CLASS.watch,
    // A session with one of your agents is Your agents' plum (computer-theme
    // rule 3); the feed's agent kind is a screen-seen run, which is indigo.
    agent: DOT_CLASS.handoff,
    mail: 'bg-app-indigo-text',
    calendar: 'bg-app-teal-text',
    call: 'bg-app-amber-text',
    messages: 'bg-app-sage-text',
    chat: 'bg-app-stone-text',
};

const SOURCE_ORDER: WhySource[] = ['mail', 'call', 'calendar', 'messages', 'screen', 'watch', 'agent', 'chat'];

/**
 * The source line's words from a Why's counts: "From 6 emails and a call this
 * week" (computer-you v1.0.0 rule 3). Empty when nothing is linked.
 */
export function whySummaryText(t: TFunction<'common'>, language: string, summary: WhySummary): string {
    if (summary.importedFrom && summary.total === 0) return t('why.imported', { label: summary.importedFrom });
    const parts = SOURCE_ORDER
        .filter((source) => (summary.bySource[source] ?? 0) > 0)
        .map((source) => t(`why.count.${source}`, { count: summary.bySource[source] ?? 0 }));
    if (parts.length === 0) return '';
    const list = new Intl.ListFormat(language, { style: 'long', type: 'conjunction' }).format(parts);
    return summary.thisWeek === summary.total ? t('why.fromThisWeek', { list }) : t('why.from', { list });
}
