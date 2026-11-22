import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';

/**
 * The line above a claim or a piece of work: where it came from, then Why
 * (computer-you v1.0.0 rule 3, "From 6 emails and a call this week · Why").
 * A result that is itself a moment (kind "moment") has no Why: its source
 * line already says where it's from (computer-conformance-answers S5).
 */
export function WhyLine({
    kind,
    where,
    onWhy,
    className,
}: {
    kind: 'claim' | 'work' | 'moment';
    /** Where it came from, in words; empty shows Why alone. */
    where?: string;
    onWhy?: () => void;
    className?: string;
}) {
    const { t } = useTranslation('common');
    const showWhy = kind !== 'moment' && !!onWhy;
    if (!where && !showWhy) return null;
    return (
        <span className={cn('inline-flex min-w-0 flex-wrap items-baseline gap-x-1.5 text-xs text-muted', className)}>
            {where && <span className="min-w-0">{where}</span>}
            {where && showWhy && <span aria-hidden="true">·</span>}
            {showWhy && (
                <button
                    type="button"
                    data-why-button
                    onClick={(event) => {
                        event.stopPropagation();
                        onWhy?.();
                    }}
                    className="cursor-pointer rounded-sm border-0 bg-transparent p-0 text-xs font-medium text-muted underline-offset-2 hover:text-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-border-focus"
                >
                    {t('why.title')}
                </button>
            )}
        </span>
    );
}
