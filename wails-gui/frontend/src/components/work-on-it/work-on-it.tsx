import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Dialog, DialogCloseButton, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import * as appBindings from '@/lib/wails-shim-app';
import type { WorkOnItAgentView, WorkOnItInput, WorkOnItPrepared, WorkOnItSent } from '@/lib/wails-shim-app';

// The bindings are read through the module namespace, so a surface whose
// test mocks the app module without them still renders (the action is off).
type Bindings = Pick<typeof appBindings, 'GetWorkOnItEnabled' | 'PrepareWorkOnIt' | 'SendWorkOnIt' | 'SetWorkOnItHandle'>;
function binding<K extends keyof Bindings>(name: K): Bindings[K] {
    try {
        const fn = (appBindings as Bindings)[name];
        if (typeof fn === 'function') return fn;
    } catch {
        // A mocked module without this export.
    }
    return (() => Promise.reject(new Error('work on it unavailable'))) as unknown as Bindings[K];
}
const GetWorkOnItEnabled = () => binding('GetWorkOnItEnabled')();
const PrepareWorkOnIt = (input: WorkOnItInput) => binding('PrepareWorkOnIt')(input);
const SendWorkOnIt = (taskId: string, agentId: string, method: string) => binding('SendWorkOnIt')(taskId, agentId, method);
const SetWorkOnItHandle = (agentId: string, handle: string) => binding('SetWorkOnItHandle')(agentId, handle);

// Work on it, v1 (bare minimum; design's board replaces the look). The
// action prepares the task from what the surface shows, shows it word for
// word, and hands it to the chosen agent only on the person's tap.

let enabledRead: Promise<boolean> | null = null;

/** Reads the work_on_it_enabled switch once per window. */
export function useWorkOnItEnabled(): boolean {
    const [enabled, setEnabled] = useState(false);
    useEffect(() => {
        let alive = true;
        enabledRead ??= GetWorkOnItEnabled().catch(() => false);
        void enabledRead.then((value) => { if (alive) setEnabled(value === true); });
        return () => { alive = false; };
    }, []);
    return enabled;
}

/** Test hook: forget the cached switch. */
export function resetWorkOnItEnabled() {
    enabledRead = null;
}

export function WorkOnItButton({ input, look = 'link' }: { input: WorkOnItInput; look?: 'link' | 'button' }) {
    const { t } = useTranslation('app');
    const enabled = useWorkOnItEnabled();
    const [open, setOpen] = useState(false);
    if (!enabled || !input.title.trim()) return null;
    return (
        <>
            {look === 'link' ? (
                <button type="button" data-work-on-it className="font-medium text-muted transition-colors hover:text-ink" onClick={() => setOpen(true)}>
                    {t('workOnIt.action')}
                </button>
            ) : (
                <Button type="button" data-work-on-it size="sm" variant="quiet" className="text-[12.5px]" onClick={() => setOpen(true)}>
                    {t('workOnIt.action')}
                </Button>
            )}
            {open && <WorkOnItDialog input={input} onClose={() => setOpen(false)} />}
        </>
    );
}

function WorkOnItDialog({ input, onClose }: { input: WorkOnItInput; onClose: () => void }) {
    const { t } = useTranslation('app');
    const [prepared, setPrepared] = useState<WorkOnItPrepared | null>(null);
    const [error, setError] = useState<string | null>(null);
    const [agentId, setAgentId] = useState<string>('');
    const [busy, setBusy] = useState(false);
    const [sent, setSent] = useState<WorkOnItSent | null>(null);
    const [handle, setHandle] = useState('');

    const load = () => {
        PrepareWorkOnIt(input).then((value) => {
            setPrepared(value);
            setAgentId((current) => current || value.agents.find((agent) => agent.default)?.id || value.agents[0]?.id || '');
        }).catch((err: unknown) => {
            setError(String(err).includes('closed eyes') ? t('workOnIt.closedEyes') : t('workOnIt.prepareFailed'));
        });
    };
    // Prepare once when the sheet opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    useEffect(load, []);

    const agent: WorkOnItAgentView | undefined = prepared?.agents.find((candidate) => candidate.id === agentId);
    const others = (prepared?.agents ?? []).filter((candidate) => candidate.id !== agentId);
    const method = agent?.methods[0];

    const send = () => {
        if (!prepared || !agent || !method) return;
        setBusy(true);
        setError(null);
        SendWorkOnIt(prepared.taskId, agent.id, method)
            .then(setSent)
            .catch((err: unknown) => setError(String(err).includes('closed eyes') ? t('workOnIt.closedEyes') : t('workOnIt.failed')))
            .finally(() => setBusy(false));
    };
    const saveHandle = () => {
        if (!agent || !handle.trim()) return;
        setBusy(true);
        SetWorkOnItHandle(agent.id, handle.trim())
            .then(() => { setHandle(''); load(); })
            .catch(() => setError(t('workOnIt.failed')))
            .finally(() => setBusy(false));
    };

    return (
        <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
            <DialogContent className="w-[480px]">
                <DialogHeader>
                    <DialogTitle className="text-sm">{t('workOnIt.title')}</DialogTitle>
                    <DialogCloseButton />
                </DialogHeader>
                <div className="flex flex-col gap-3 px-6 py-4">
                    {prepared && agent && (
                        <>
                            <DialogDescription className="text-[13px] text-muted">{t('workOnIt.whatGoes', { agent: agent.name })}</DialogDescription>
                            <pre data-work-on-it-text className="max-h-64 overflow-auto whitespace-pre-wrap rounded-lg border border-border bg-panel px-3 py-2 font-sans text-[13px] leading-relaxed text-ink">{prepared.text}</pre>
                            {agent.termsNote && <p data-work-on-it-terms className="text-[12px] text-muted">{t(agent.termsNote as never)}</p>}
                            {agent.needsHandle && (
                                <label className="flex flex-col gap-1.5 text-[12.5px] text-muted">
                                    {t('workOnIt.handleLabel', { agent: agent.name })}
                                    <div className="flex gap-2">
                                        <input
                                            className="flex-1 rounded-lg border border-border bg-panel px-2.5 py-1.5 text-[13px] text-ink focus:border-border-focus focus:outline-none"
                                            value={handle}
                                            onChange={(event) => setHandle(event.target.value)}
                                        />
                                        <Button size="sm" variant="secondary" disabled={busy || !handle.trim()} onClick={saveHandle}>{t('workOnIt.handleSave')}</Button>
                                    </div>
                                </label>
                            )}
                        </>
                    )}
                    {sent && agent && <p role="status" className="text-[13px] text-ink">{t(`workOnIt.result.${sent.method === 'task_link' ? `task_link_${sent.via ?? 'open_copy'}` : sent.method}` as never, { agent: agent.name })}</p>}
                    {error && <p role="alert" className="text-[13px] text-copper">{error}</p>}
                </div>
                {prepared && agent && !sent && (
                    <div className="flex items-center justify-end gap-2 px-6 pb-5">
                        {others.length > 0 && (
                            <DropdownMenu>
                                <DropdownMenuTrigger asChild>
                                    <Button size="sm" variant="quiet" disabled={busy}>{t('workOnIt.sendToOther')}</Button>
                                </DropdownMenuTrigger>
                                <DropdownMenuContent align="end">
                                    {others.map((candidate) => (
                                        <DropdownMenuItem key={candidate.id} onSelect={() => setAgentId(candidate.id)}>{candidate.name}</DropdownMenuItem>
                                    ))}
                                </DropdownMenuContent>
                            </DropdownMenu>
                        )}
                        <Button size="sm" disabled={busy || !method} onClick={send}>
                            {busy ? t('workOnIt.sending') : t('workOnIt.sendTo', { agent: agent.name })}
                        </Button>
                    </div>
                )}
            </DialogContent>
        </Dialog>
    );
}
