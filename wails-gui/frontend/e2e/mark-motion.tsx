// Harness for mark motion v1.0.1 (the sidebar's animated mark), no app or
// live store. URL params:
//   mode=strip&seq=<sequence|pose:<name>>  every second frame at 20 px on the
//                                          sidebar's own background, and 4x
//   mode=identity                          today's still logo beside the new
//                                          mark at rest, open and collapsed,
//                                          and the kit's drawing at rest
//   mode=corner&collapsed=1                the real Sidebar; window.__mark
//                                          emits the app's own events
//   theme=paper|onedark
import React, { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import '@/index.css';
import i18n from '@/i18n';
import { ThemeProvider } from '@/hooks/useTheme';
import { Sidebar } from '@/components/sidebar';
import { LogoIcon } from '@/assets/icons/LogoIcon';
import { LogoWithFontIcon } from '@/assets/icons/LogoWithFontIcon';
import { MarkMotion } from '@/components/mark-motion/MarkMotion';
import { MarkSvg } from '@/components/mark-motion/MarkSvg';
import { MarkPainter } from '@/components/mark-motion/mark-paint';
import { pose, REST, sequence, type MarkState, type SequenceName } from '@/components/mark-motion/mark-frames';

const params = new URLSearchParams(location.search);
const theme = params.get('theme') ?? 'paper';
const mode = params.get('mode') ?? 'strip';
const noop = () => {};
localStorage.setItem('yansu.theme', theme);
document.documentElement.dataset.theme = theme;

// The app's events, emitted by the spec through window.__mark.emit.
const handlers = new Map<string, Set<(...args: unknown[]) => void>>();
const reads: Record<string, unknown> = {
    GetThemePreference: theme, GetTelemetryTier: 'detailed', LoadUIState: '', ListHandoffs: [],
    GetDoingNow: { kind: 'watching' },
    GetLatestBrief: { brief: { briefId: '2026-11-09-evening', slot: 'evening', asOf: '2026-11-09T18:00:00-08:00', localDate: '2026-11-09', version: 1, coldStart: false }, building: false, enabled: true, nextAt: '', nextSlot: '' },
    GetUserProfile: { name: 'Bo' }, GetProfile: { name: 'Bo' },
};
const fixture = window as unknown as Record<string, any>;
fixture.go = { main: { App: new Proxy({}, { get: (_, method: string) => async () => reads[method] ?? null }) } };
fixture.runtime = new Proxy({}, {
    get: (_, name: string) => {
        if (name === 'EventsOn' || name === 'EventsOnMultiple') return (event: string, cb: (...args: unknown[]) => void) => {
            const set = handlers.get(event) ?? new Set();
            set.add(cb);
            handlers.set(event, set);
            return () => set.delete(cb);
        };
        return () => noop;
    },
});

function Cell({ st, size, kitOnly }: { st: MarkState; size: number; kitOnly?: boolean }) {
    const ref = useRef<SVGSVGElement>(null);
    const id = useRef(`c${Math.random().toString(36).slice(2)}`).current;
    useLayoutEffect(() => { if (ref.current) new MarkPainter(ref.current, id, { kitOnly }).paint(st); }, [st, id, kitOnly]);
    return <MarkSvg ref={ref} size={size} clipId={id} />;
}

function frameList(seq: string): MarkState[] {
    if (seq.startsWith('pose:')) return [pose(seq.slice(5) as never)];
    const frames = sequence(seq as SequenceName);
    return frames.filter((_, i) => i % 2 === 0 || i === frames.length - 1);
}

function Strip() {
    const seq = params.get('seq') ?? 'blink';
    const frames = frameList(seq);
    return (
        <div data-strip className="inline-block bg-panel dark:bg-background-accent p-2">
            <div className="flex">
                {frames.map((st, i) => (
                    <div key={i} className="w-[48px] h-[48px] flex items-center justify-center"><Cell st={st} size={20} /></div>
                ))}
            </div>
            <div className="flex">
                {frames.map((st, i) => (
                    <div key={i} className="w-[48px] h-[48px] flex items-center justify-center"><Cell st={st} size={40} /></div>
                ))}
            </div>
        </div>
    );
}

function Identity() {
    return (
        <div className="bg-panel dark:bg-background-accent p-4 flex flex-col gap-4">
            <div className="flex gap-6">
                <div data-id="open-before" className="w-[128px] h-[48px] flex items-center justify-center"><LogoWithFontIcon height={20} className="text-accent" blinkOnHover /></div>
                <div data-id="open-control" className="w-[128px] h-[48px] flex items-center justify-center"><LogoWithFontIcon height={20} className="text-accent" blinkOnHover /></div>
                <div data-id="open-after" className="w-[128px] h-[48px] flex items-center justify-center"><LogoWithFontIcon height={20} className="text-accent" blinkOnHover mark={<MarkMotion size={19} hoverBlink />} /></div>
            </div>
            <div className="flex gap-6">
                <div data-id="collapsed-before" className="w-[128px] h-[48px] flex items-center justify-center"><LogoIcon size={19} /></div>
                <div data-id="collapsed-after" className="w-[128px] h-[48px] flex items-center justify-center"><MarkMotion size={19} /></div>
            </div>
            <div className="flex gap-6">
                <div data-id="kit-20" className="w-[128px] h-[48px] flex items-center justify-center"><Cell st={REST} size={19} kitOnly /></div>
                <div data-id="logo-160" className="w-[192px] h-[192px] flex items-center justify-center"><LogoIcon size={160} /></div>
                <div data-id="kit-160" className="w-[192px] h-[192px] flex items-center justify-center"><Cell st={REST} size={160} kitOnly /></div>
            </div>
        </div>
    );
}

function Corner() {
    const [waiting, setWaiting] = useState(1);
    const collapsed = params.get('collapsed') === '1';
    useEffect(() => {
        fixture.__mark = {
            emit: (event: string, ...args: unknown[]) => handlers.get(event)?.forEach((cb) => cb(...args)),
            setWaiting,
            setBrief: (view: unknown) => { reads.GetLatestBrief = view; },
        };
    }, []);
    return (
        <div className="h-screen flex flex-row overflow-hidden">
            <Sidebar
                projects={[]} activeProjectId={null} collapsed={collapsed} width={232}
                onToggleCollapse={noop} onOpenSearch={noop} onOpenSettings={noop}
                waitingCount={waiting} chatUnread={false}
                onOpenActivity={noop} onOpenKnowledge={noop} onOpenMemory={noop} onOpenPeople={noop}
                onOpenToday={noop} onOpenTasks={noop} tasksOpen appMode={'tasks'}
                onOpenMainChat={noop} onSwitchSession={noop} onCloseSession={noop} onTogglePin={noop}
                onRenameSession={noop} onMarkSessionRead={noop} onMarkSessionUnread={noop} onForkSession={noop}
            />
            <main className="patina-ground flex-1 min-w-0 h-full bg-background" />
        </div>
    );
}

await i18n.changeLanguage('en');
const View = mode === 'corner' ? Corner : mode === 'identity' ? Identity : Strip;
createRoot(document.getElementById('root')!).render(<ThemeProvider><View /></ThemeProvider>);
