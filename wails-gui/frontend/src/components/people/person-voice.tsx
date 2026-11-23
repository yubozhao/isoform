// A person's Next and voice on Who you know (redesign-you v1.0.0 rule 17;
// watch-voice v1.0.0). Next reads Today's own Before you talk, read-only.
// The voice is only what you named: the conversations where you said a
// speaker was them (RY.18). Nothing of it leaves the computer.
import { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ForgetPersonVoice, GetPersonVoice, type PersonVoice } from '@/lib/wails-shim-app';
import { useTodayBeforeYouTalk } from '@/hooks/useTodayBeforeYouTalk';
import { Button } from '@/components/ui/button';
import { dayWords, readWithRetry, type Translate } from './people-format';
import { PeopleFailureLine } from './people-failure';

/** Next · {meeting}: their next meeting with its prep, when Today has one for them. */
export function PersonNext({ personId }: { personId: string }) {
    const { t, i18n } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const { card } = useTodayBeforeYouTalk(true);
    if (!card || card.personId !== personId) return null;
    const time = new Date(card.startAt).toLocaleTimeString(i18n.language, { hour: 'numeric', minute: '2-digit' });
    return (
        <section aria-labelledby={`person-next-${personId}`} className="flex flex-col gap-1.5">
            <h3 id={`person-next-${personId}`} className="m-0 text-[13px] font-semibold text-ink">
                {tr('who.next', { meeting: card.meetingTitle })}
            </h3>
            <span className="text-[12px] text-muted">{tr('who.nextAt', { time })}</span>
            <ul className="m-0 flex list-none flex-col gap-1 p-0 text-[13px] leading-relaxed text-ink">
                {card.youOwe && <li>{tr('who.prepYouOwe', { text: card.youOwe })}</li>}
                {card.theyAsked && <li>{tr('who.prepTheyAsked', { text: card.theyAsked })}</li>}
                {card.lastTime && <li>{tr('who.prepLastTime', { text: card.lastTime })}</li>}
            </ul>
        </section>
    );
}

/**
 * Their voice: "Isoform knows Lena's voice from 2 conversations you named.",
 * Which conversations, and Forget their voice. Shows nothing until a
 * conversation names them; a failed read says so with Try again.
 */
export function PersonVoiceLine({ personId, name }: { personId: string; name: string }) {
    const { t, i18n } = useTranslation('people');
    const tr: Translate = (key, options) => t(key as never, options as never) as unknown as string;
    const [voice, setVoice] = useState<PersonVoice | null>(null);
    const [failed, setFailed] = useState(false);
    const [attempt, setAttempt] = useState(0);
    const [showWhich, setShowWhich] = useState(false);
    const [forgetting, setForgetting] = useState(false);
    const [forgetFailed, setForgetFailed] = useState(false);
    const [forgotten, setForgotten] = useState<number | null>(null);

    useEffect(() => {
        let alive = true;
        setFailed(false);
        readWithRetry(() => GetPersonVoice(personId))
            .then((next) => {
                if (alive) setVoice(next ?? null);
            })
            .catch(() => {
                if (alive) setFailed(true);
            });
        return () => {
            alive = false;
        };
    }, [personId, attempt]);

    const forget = useCallback(async () => {
        setForgetting(true);
        setForgetFailed(false);
        try {
            const changed = await ForgetPersonVoice(personId);
            setForgotten(changed);
            setVoice({ conversations: [], samples: 0 });
        } catch {
            setForgetFailed(true);
        } finally {
            setForgetting(false);
        }
    }, [personId]);

    if (failed) {
        return (
            <section className="flex flex-col gap-1.5">
                <h3 className="m-0 text-[13px] font-semibold text-ink">{tr('who.voice')}</h3>
                <PeopleFailureLine text={tr('who.voiceFailed')} onRetry={() => setAttempt((n) => n + 1)} />
            </section>
        );
    }
    if (forgotten !== null) {
        return (
            <section className="flex flex-col gap-1.5">
                <h3 className="m-0 text-[13px] font-semibold text-ink">{tr('who.voice')}</h3>
                <p role="status" className="m-0 text-[13px] text-muted">{tr('who.voiceForgotten', { count: forgotten })}</p>
            </section>
        );
    }
    const count = voice?.conversations?.length ?? 0;
    if (!voice || (count === 0 && !voice.samples)) return null;
    return (
        <section aria-labelledby={`person-voice-${personId}`} className="flex flex-col gap-1.5">
            <h3 id={`person-voice-${personId}`} className="m-0 text-[13px] font-semibold text-ink">{tr('who.voice')}</h3>
            {count > 0 && <p className="m-0 text-[13px] leading-relaxed text-ink">{tr('who.voiceBody', { name, count })}</p>}
            <span className="text-[12px] text-muted">{tr('who.voiceLocal')}</span>
            {showWhich && count > 0 && (
                <ul className="m-0 flex list-none flex-col p-0">
                    {voice.conversations.map((conversation) => (
                        <li key={conversation.id} className="border-b border-border-divider py-[7px] text-[13px] text-ink">
                            {tr('who.voiceConversation', { day: dayWords(tr, i18n.language, conversation.startAt) })}
                        </li>
                    ))}
                </ul>
            )}
            <div className="flex flex-wrap items-center gap-1.5">
                {count > 0 && (
                    <Button size="sm" variant="outline" aria-expanded={showWhich} onClick={() => setShowWhich((v) => !v)}>
                        {showWhich ? tr('who.voiceHide') : tr('who.voiceWhich')}
                    </Button>
                )}
                <Button size="sm" variant="secondary" disabled={forgetting} aria-busy={forgetting} onClick={() => void forget()}>
                    {tr('who.voiceForget', { pronoun: tr('who.pronounTheir') })}
                </Button>
            </div>
            {forgetFailed && <PeopleFailureLine text={tr('who.voiceForgetFailed')} onRetry={forget} />}
        </section>
    );
}
