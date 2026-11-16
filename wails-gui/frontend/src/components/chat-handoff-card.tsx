import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { LogoIcon } from '@/assets/icons/LogoIcon';
import { Button } from '@/components/ui/button';
import { WorkOnItDialog, useWorkOnItEnabled } from '@/components/work-on-it/work-on-it';
import * as appBindings from '@/lib/wails-shim-app';
import type { WorkOnItAgentView, WorkOnItKind } from '@/lib/wails-shim-app';
import { getChatHandoff } from '@/lib/stream-helpers';
import type { StreamItem } from '@/types';

// Read through the module namespace, so a surface whose test mocks the app
// module without these still renders the card.
type Bindings = Pick<typeof appBindings, 'GetWorkOnItAgents' | 'GetYourAgents' | 'AnswerChatHandoffCard' | 'NativeClipboardSetText'>;
function binding<K extends keyof Bindings>(name: K): Bindings[K] {
    try {
        const fn = (appBindings as Bindings)[name];
        if (typeof fn === 'function') return fn;
    } catch {
        // A mocked module without this export.
    }
    return (() => Promise.reject(new Error('unavailable'))) as unknown as Bindings[K];
}

const agentReads = new Map<WorkOnItKind, Promise<string>>();

/** Test hook: forget the cached agent names. */
export function resetChatHandoffAgents() {
    agentReads.clear();
}

/** The name of the person's agent for this kind of work: their choice in
 *  Your agents, else the Work on it menu's default (phone-map B4). */
async function readAgentName(kind: WorkOnItKind): Promise<string> {
    try {
        const yours = await binding('GetYourAgents')();
        const id = yours?.choices?.[kind];
        const chosen = id ? yours.agents.find((agent) => agent.id === id) : undefined;
        if (chosen?.name) return chosen.name;
    } catch {
        // Fall back to the menu's default.
    }
    try {
        const agents: WorkOnItAgentView[] = await binding('GetWorkOnItAgents')();
        return (agents.find((candidate) => candidate.default) ?? agents[0])?.name ?? '';
    } catch {
        return '';
    }
}

function useAgentName(enabled: boolean, kind: WorkOnItKind): string {
    const [name, setName] = useState('');
    useEffect(() => {
        if (!enabled) return;
        let alive = true;
        let read = agentReads.get(kind);
        if (!read) {
            read = readAgentName(kind);
            agentReads.set(kind, read);
        }
        void read.then((value) => { if (alive) setName(value); });
        return () => { alive = false; };
    }, [enabled, kind]);
    return name;
}

/**
 * Work asked for in Chat (computer-chat v1.0.0 rule 4): what would go to the
 * person's agent. Nothing runs from the card. Hand it to opens the hand-off
 * sheet with the card's words; No closes it.
 */
export function ChatHandoffCard({ item, compact = false }: { item: StreamItem; compact?: boolean }) {
    const { t, i18n } = useTranslation('chat');
    const card = getChatHandoff(item);
    const enabled = useWorkOnItEnabled();
    const agentName = useAgentName(enabled, card?.kind ?? 'else');
    const [status, setStatus] = useState(card?.status ?? '');
    const [sheet, setSheet] = useState(false);
    const [copied, setCopied] = useState(false);
    const [failed, setFailed] = useState(false);
    useEffect(() => { setStatus(card?.status ?? ''); }, [card?.status]);
    if (!card) return null;

    const answer = (value: 'declined' | 'handed') => {
        setFailed(false);
        setStatus(value);
        binding('AnswerChatHandoffCard')(item.id, value).then((saved) => {
            if (saved === 'declined' || saved === 'handed') setStatus(saved);
        }).catch(() => {
            if (value === 'declined') setStatus('');
            setFailed(true);
        });
    };
    const copy = () => {
        setFailed(false);
        const text = card.done ? `${card.title}\n\n${card.detail}\n\n${card.done}` : `${card.title}\n\n${card.detail}`;
        binding('NativeClipboardSetText')(text).then(() => setCopied(true)).catch(() => setFailed(true));
    };

    let what = '';
    if (card.gets.length > 0) {
        try {
            what = new Intl.ListFormat(i18n.resolvedLanguage || 'en', { style: 'long', type: 'conjunction' }).format(card.gets);
        } catch {
            what = card.gets.join(', ');
        }
    }

    return (
        <div data-chat-handoff={status || 'open'} className={cn('animate-rise-in', compact ? '' : 'max-w-[90%] mx-auto')}>
            <div className="flex flex-col gap-2 px-3.5 py-3 rounded-lg border border-border bg-panel-warm text-[13px] leading-relaxed text-ink/85">
                <div className="flex items-start gap-2.5">
                    <LogoIcon size={14} className="mt-[3px] shrink-0 text-accent" />
                    <p className="min-w-0 flex-1 break-words font-medium text-ink">{card.title}</p>
                </div>
                {agentName ? (
                    <p data-chat-handoff-for className="break-words text-muted">
                        {what ? t('mainChat.handoff.agentFor', { agent: agentName, what }) : t('mainChat.handoff.agentForBare', { agent: agentName })}
                    </p>
                ) : what ? (
                    <p data-chat-handoff-for className="break-words text-muted">{t('mainChat.handoff.gets', { what })}</p>
                ) : null}
                <p className="whitespace-pre-wrap break-words rounded-md bg-panel px-3 py-2 text-ink">{card.detail}</p>
                {card.done ? <p className="break-words text-muted">{t('mainChat.handoff.done', { done: card.done })}</p> : null}
                {failed ? <p role="alert" className="text-subtle">{t('mainChat.handoff.failed')}</p> : null}
                {status === 'handed' ? (
                    <p role="status" className="text-muted">{agentName ? t('mainChat.handoff.handedTo', { agent: agentName }) : t('mainChat.handoff.handed')}</p>
                ) : status === 'declined' ? (
                    <p role="status" className="text-muted">{t('mainChat.handoff.declined')}</p>
                ) : (
                    <div className="flex flex-wrap items-center gap-1.5">
                        {agentName ? (
                            <Button size="sm" onClick={() => setSheet(true)}>{t('mainChat.handoff.handIt', { agent: agentName })}</Button>
                        ) : (
                            <Button variant="secondary" size="sm" onClick={copy}>{t(copied ? 'mainChat.handoff.copied' : 'mainChat.handoff.copy')}</Button>
                        )}
                        <Button variant="quiet" size="sm" onClick={() => answer('declined')}>{t('mainChat.handoff.no')}</Button>
                    </div>
                )}
            </div>
            {sheet ? (
                <WorkOnItDialog
                    input={{ sourceKind: 'chat_card', sourceId: item.id, title: card.title, detail: card.detail, done: card.done, kind: card.kind }}
                    onClose={() => setSheet(false)}
                    onSent={() => answer('handed')}
                />
            ) : null}
        </div>
    );
}
