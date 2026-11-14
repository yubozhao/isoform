import { memo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { EventsEmit } from '../../wailsjs/runtime/runtime';
import { OpenAssistantSignIn } from '@/lib/wails-shim-app';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import type { ChatError } from '@/lib/chat-error';

interface ChatErrorCardProps {
    error: ChatError;
    /** The AI the line names ("Claude Code", "Codex", a provider); "your AI" when empty. */
    assistant?: string;
    /** Kept for callers; the error is always one line now. */
    compact?: boolean;
    /** Resends the same message with its dedupe key. Try again shows only when set. */
    onRetry?: () => void;
}

type Tone = 'copper' | 'gray';
type Link = 'set_up' | 'sign_in' | 'see_usage' | 'try_again' | 'send_again';

export interface ChatCauseLine {
    line: string;
    tone: Tone;
    link?: Link;
    /** Lines that already name the fix show no Details. */
    details: boolean;
}

/** Set up, Sign in and See usage open Settings › Your AI, as Tasks' Set up does. */
export function openYourAI() {
    EventsEmit('settings:open', 'ai-provider');
}

/** The assistant's id for the sign-in command, from the name the cause line shows; undefined for other AIs. */
export function signInAssistantID(name?: string): string | undefined {
    switch (name?.trim()) {
        case 'Claude Code': return 'claude-code';
        case 'Codex': return 'codex';
        default: return undefined;
    }
}

/** R75: Sign in opens the assistant's own sign-in in Terminal; Settings is the fallback for other AIs. */
function openAssistantSignIn(name?: string) {
    const id = signInAssistantID(name);
    if (!id) {
        openYourAI();
        return;
    }
    void OpenAssistantSignIn(id).catch(() => openYourAI());
}

export function chatCauseLine(error: ChatError, assistant: string | undefined, t: TFunction<'chat'>): ChatCauseLine {
    const named = assistant?.trim();
    const ai = named || t('errors.cause.yourAI');
    const leadAI = named || t('errors.cause.yourAICapital');
    switch (error.kind) {
        case 'no_assistant':
            return { line: t('errors.cause.noAssistantKept'), tone: 'copper', link: 'set_up', details: false };
        case 'signed_out':
            return { line: t('errors.cause.signedOut', { ai: leadAI }), tone: 'copper', link: 'sign_in', details: false };
        case 'quota_period': {
            const line = error.period === 'daily'
                ? t('errors.cause.usageDaily', { ai: leadAI })
                : error.period === 'monthly'
                    ? t('errors.cause.usageMonthly', { ai: leadAI })
                    : t('errors.cause.usageWeekly', { ai: leadAI });
            return { line, tone: 'copper', link: 'see_usage', details: false };
        }
        case 'quota_generic':
        case 'subscription':
            return { line: t('errors.cause.usageLimit', { ai: leadAI }), tone: 'copper', link: 'see_usage', details: false };
        case 'runner_unavailable':
            return { line: t('errors.cause.runnerUnavailable', { ai }), tone: 'copper', link: 'set_up', details: false };
        case 'no_reply':
            // The hub stored this row: the turn ended with no answer, or the app quit mid-turn.
            return { line: t('errors.cause.noReply'), tone: 'gray', link: 'send_again', details: false };
        case 'network':
            return { line: t('errors.cause.network', { ai }), tone: 'gray', link: 'try_again', details: false };
        case 'server':
            return { line: t('errors.cause.server', { ai: leadAI }), tone: 'gray', link: 'try_again', details: false };
        case 'request_too_large':
            return {
                line: t('errors.line', { title: t('errors.requestTooLarge.title'), body: t('errors.requestTooLarge.body') }),
                tone: 'gray',
                details: false,
            };
        case 'assistant_error':
        case 'unknown':
        default:
            return {
                line: named ? t('errors.cause.assistantError', { ai: named }) : t('errors.cause.assistantErrorUnknown'),
                tone: 'gray',
                link: 'try_again',
                details: true,
            };
    }
}

function linkLabel(link: Link, t: TFunction<'chat'>): string {
    switch (link) {
        case 'set_up': return t('errors.actions.setUp');
        case 'sign_in': return t('errors.actions.signIn');
        case 'see_usage': return t('errors.actions.seeUsage');
        case 'try_again': return t('errors.actions.tryAgain');
        case 'send_again': return t('errors.actions.sendAgain');
    }
}

const LINK_CLASS = 'cursor-pointer font-medium underline-offset-2 hover:underline focus-visible:underline';

/** One line where the answer would go: copper when the person can fix it, gray otherwise. Never red. */
export const ChatErrorCard = memo(function ChatErrorCard({ error, assistant, onRetry }: ChatErrorCardProps) {
    const { t } = useTranslation('chat');
    const [detailOpen, setDetailOpen] = useState(false);
    const cause = chatCauseLine(error, assistant, t);
    const link = (cause.link === 'try_again' || cause.link === 'send_again') && !onRetry ? undefined : cause.link;
    const showDetails = cause.details && Boolean(error.detail);
    const onLink = link === 'try_again' || link === 'send_again' ? onRetry : link === 'sign_in' ? () => openAssistantSignIn(assistant) : openYourAI;

    return (
        <div className="animate-rise-in max-w-md" data-chat-cause={error.kind}>
            <p role="status" className={cn('m-0 text-xs leading-relaxed', cause.tone === 'copper' ? 'text-warning' : 'text-subtle')}>
                <span>{cause.line}</span>
                {/* R75: a signed-out answer's actions follow the sentence with a space, and a separator
                    only between two actions, so a wrapped line never leaves a dangling separator. */}
                {error.kind === 'signed_out' && link === 'sign_in' ? (
                    <>
                        {' '}
                        {/* The design system's link Button (text-accent): Sign in and Send again. */}
                        <Button type="button" variant="link" size="sm" className="h-auto p-0 align-baseline text-xs font-medium" onClick={onLink}>{linkLabel(link, t)}</Button>
                        {onRetry && (
                            <>
                                <span aria-hidden="true">{t('errors.cause.separator')}</span>
                                <Button type="button" variant="link" size="sm" className="h-auto p-0 align-baseline text-xs font-medium" onClick={onRetry}>{t('errors.actions.sendAgain')}</Button>
                            </>
                        )}
                    </>
                ) : link && (
                    <>
                        <span aria-hidden="true">{t('errors.cause.separator')}</span>
                        <button type="button" className={LINK_CLASS} onClick={onLink}>{linkLabel(link, t)}</button>
                    </>
                )}
                {showDetails && (
                    <>
                        <span aria-hidden="true">{t('errors.cause.separator')}</span>
                        <button type="button" className={LINK_CLASS} aria-expanded={detailOpen} onClick={() => setDetailOpen((open) => !open)}>
                            {detailOpen ? t('errors.actions.hide') : t('errors.actions.details')}
                        </button>
                    </>
                )}
            </p>
            {showDetails && detailOpen && (
                <pre className="m-0 mt-1.5 max-h-40 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border bg-panel px-2 py-1.5 font-mono text-[11px] leading-relaxed text-muted">
                    {error.detail}
                </pre>
            )}
        </div>
    );
});
