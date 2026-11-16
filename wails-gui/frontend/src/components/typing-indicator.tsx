import { useTranslation } from 'react-i18next';
import type { ExecutorType } from '@/lib/stream-helpers';

interface TypingIndicatorProps {
    executorType?: ExecutorType;
    /** The agent is still starting after launch: the turn waits for it and
     *  is answered once it is ready, so say so rather than look stuck. */
    startingUp?: boolean;
}

/**
 * Pre-response loading state for the chat stream — shown only in the gap
 * between the user sending a message and the first agent activity arriving.
 *
 * A single element: three dots carved out by a circular mask, with a
 * deeper-ink sheen flowing through them (`.typing-ink-dots` in index.css).
 * The dots never blink or move — the only motion is light passing through
 * still ink, which reads calmer than any blinking/bouncing loader.
 */
export function TypingIndicator({ executorType: _executorType = 'claude', startingUp = false }: TypingIndicatorProps) {
    const { t } = useTranslation('chat');
    if (!startingUp) return <div className="typing-ink-dots animate-fade-in" aria-hidden />;
    return (
        <div className="flex items-center gap-2 animate-fade-in">
            <div className="typing-ink-dots" aria-hidden />
            <span role="status" data-agent-starting className="text-[12.5px] text-muted">{t('startingUp')}</span>
        </div>
    );
}
