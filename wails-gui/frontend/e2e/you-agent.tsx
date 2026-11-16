// Fixture: You › Agent of You with synthetic owner bindings (design's fictional people).
// ?noreservation=1, ?state= none|asked|setting_up|setting_up_done|on|paused, ?profile= first|weekly|learning|approved,
// ?payouts= not_started|restricted|enabled|unsupported, ?balance= owed, ?jobs= empty, ?redo= none,
// ?lang=, ?theme=paper|onedark
import { createRoot } from 'react-dom/client';
import '@/index.css';
import {
    approvalJob, deliveredJob, fixtureUpload, ownerBalance, ownerBlocked, ownerJobs, ownerPayouts, ownerSpeeds, ownerStatus, profileView, reviewJob,
} from '@/components/settings/pages/agent-of-you/aoy-fixtures';

const params = new URLSearchParams(location.search);
const state = params.get('state') ?? 'on';
const listeners = new Map<string, Set<(value: unknown) => void>>();
const reservation = params.get('noreservation') ? null : { name: 'alex.rivera', email: 'alex@example.com', reservedAt: '2026-10-27T12:00:00Z', status: 'verified', place: 214 };
const status = state === 'setting_up_done'
    ? ownerStatus('setting_up', { profile: { version: 1, approvedAt: '2026-11-09T12:00:00Z' }, settings: { ...ownerStatus('on').settings } })
    : ownerStatus(state);
const profile = profileView((params.get('profile') as 'first') ?? (state === 'on' ? 'weekly' : 'first'));
const review = reviewJob();
const snapshots: Record<string, unknown> = {
    status,
    speeds: ownerSpeeds,
    jobs: params.get('jobs') === 'empty' ? [] : ownerJobs(),
    balance: params.get('balance') === 'owed' ? ownerBalance({ balanceCents: -210, availableCents: undefined, onTheWayCents: undefined }) : ownerBalance(),
    payouts: ownerPayouts(params.get('payouts') ?? (state === 'on' || state === 'paused' ? 'enabled' : 'not_started'), params.get('balance') === 'owed' ? -210 : 4767),
    blocked: params.get('blocked') === 'some' ? ownerBlocked() : [],
    [`job:${review.id}`]: params.get('redo') === 'none' ? reviewJob({ review: { ...review.review!, canRedo: false } }) : review,
    [`job:${approvalJob().id}`]: approvalJob(),
    [`job:${deliveredJob().id}`]: deliveredJob(),
};
const reads: Record<string, unknown> = {
    GetAgentOwnerSnapshot: (kind: string, id: string) => {
        const data = snapshots[id ? `${kind}:${id}` : kind];
        return { available: data !== undefined, data: data ?? null };
    },
    GetWorkProfile: profile,
    PreviewWorkProfileUpload: profile.pending ? fixtureUpload(profile.pending) : null,
    GetAgentOwnerNeeds: [],
    GetYourAgentPage: { specialties: [{ name: 'Client SOWs', seen: 14 }, { name: 'Weekly updates', seen: 22 }, { name: 'Hiring plans', seen: 6 }], taste: ['Lead the weekly update with what changed, not what happened.', 'Put the price and the date in the first paragraph of an SOW.', 'Name one owner for every open item.'], stillLearning: false, nameCandidates: [], reservation, online: true, computed: true },
    GetYourAgentReservation: { online: true, reservation },
    LoadUIState: '',
};
(window as any).go = { main: { App: new Proxy({}, { get: (_, method: string) => async (...args: unknown[]) => {
    const value = reads[method];
    if (typeof value === 'function') return value(...args);
    if (value !== undefined) return value;
    if (/^(List|Search)/.test(method)) return [];
    if (/^(Is|Has|Can|Should)/.test(method)) return false;
    return {};
} }) } };
(window as any).runtime = new Proxy({}, { get: (_, name: string) => {
    if (name === 'EventsOn' || name === 'EventsOnMultiple') return (event: string, callback: (value: unknown) => void) => {
        const set = listeners.get(event) ?? new Set();
        set.add(callback);
        listeners.set(event, set);
        return () => { set.delete(callback); };
    };
    return () => () => {};
} });

if (params.get('theme')) localStorage.setItem('yansu.theme', params.get('theme')!);
else localStorage.removeItem('yansu.theme');
const [{ default: i18n }, { ThemeProvider }, { YouPage }] = await Promise.all([
    import('@/i18n'), import('@/hooks/useTheme'), import('@/components/you/you-page'),
]);
await i18n.changeLanguage(params.get('lang') ?? 'en');
const noop = () => {};
function Harness() {
    return (
        <div className="h-screen w-screen overflow-hidden bg-background">
            <YouPage tab="about" onTabChange={noop}><div /></YouPage>
        </div>
    );
}
createRoot(document.getElementById('root')!).render(<ThemeProvider><Harness /></ThemeProvider>);
