// In-memory Wails bindings and runtime events for the launch demo. Nothing
// here reaches the desktop app, its browser bridge or ~/.isoform-agent.
import { typedDefaults } from './defaults';
import { beliefs, portrait, topics, brief, dayShape, DAY, evidence, lenaAnswer, nineAnswer, NOW, priyaCard, sentReport } from './content';

const ONBOARDING_KEY = 'yansu.onboarding.state';
const MAIN = 'main';
const REPLY_DELAY_MS = 2200;
const listeners = new Map<string, Set<(...data: any[]) => void>>();
export const calls: { method: string; args: unknown[] }[] = [];

const emit = (name: string, ...data: any[]) => listeners.get(name)?.forEach((cb) => cb(...data));
const now = () => Date.now();

// The main chat: the stream the panel beside Today and the Chat page read.
let stream: Record<string, unknown>[] = new URLSearchParams(location.search).has('empty') ? [] : [priyaCard];
let order = 20;
const append = (item: Record<string, unknown>) => {
    stream = [...stream, { localDay: DAY, sortOrder: ++order, createdAt: now(), ...item }];
    emit('session:items-appended', { sessionId: MAIN });
};
const page = () => ({ items: stream, hasMore: false, nextCursor: 0 });
const reply = (text: string) => {
    append({ id: `user-${order}`, kind: 'user', content: text, rowClass: 'conversation' });
    setTimeout(() => {
        append({ id: `answer-${order}`, kind: 'assistant', content: /lena/i.test(text) ? lenaAnswer : /9:00/.test(text) ? nineAnswer : 'Done.', rowClass: 'conversation' });
        emit('agent:event', { type: 'done', tag: MAIN });
    }, REPLY_DELAY_MS);
    return { sessionId: MAIN, userItemId: `user-${order}`, sortOrder: order };
};

export const reads: Record<string, unknown> = {
    GetStorageStartupStatus: { code: '' },
    AIReady: true,
    LoadUIState: (key: string) => key === ONBOARDING_KEY ? JSON.stringify({ completed: true }) : '',
    GetUserProfile: { name: 'Bo' },
    GetThemePreference: () => localStorage.getItem('yansu.theme') ?? 'onedark',
    GetTelemetryTier: 'off',
    GetEnvironment: { is_staging: false },
    StartAgent: { running: true, ready: true, port: 0 },
    GetVoiceInputStatus: { available: true, ready: true, downloading: false, building: false },
    GetSlackStatus: { connected: false, team_name: '' },
    GetFeishuStatus: { connected: false, team_name: '' },
    GetAboutInfo: { version: '1.0.0', update: { status: 'idle' } },
    GetPortraitTodayCard: null,
    GetAboutYouPortrait: portrait,
    // Observing in the sidebar, past the first week.
    GetPlatformPermissions: { platform: 'darwin', supported: true, items: [
        { key: 'screen_recording', toggleable: true, enabled: true, granted: true },
        { key: 'accessibility', toggleable: true, enabled: true, granted: true },
    ] },
    GetDoingNow: { kind: 'watching' },
    GetFirstWeekDay: 12,
    // Today
    GetLatestBrief: () => ({ brief, building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] }),
    GetTodayDayShape: dayShape,
    GetMobileBriefHealth: { enabled: true, lastRunAt: brief.asOf, nextRunAt: `${DAY}T17:00:00-08:00`, missedRuns: 0 },
    GetCaptureCounts: { paused: false, notReady: false, sources: [{ source: 'screens', today: 14 }] },
    GetTodayNews: { date: DAY, status: 'ready', items: [], updatedAt: NOW },
    GetTodayNewsHistory: [],
    GetTodaySuggestion: { available: false }, GetGoalCheckIn: null,
    GetTopicFinds: { status: 'off', items: [], topics: [] },
    GetTodayTopics: topics, GetTodayQuietLines: [], IsClosedEyesModeEnabled: false,
    GetEngagementWeekSummary: { available: false, sent: 0, actedOn: 0, days: 7, kinds: [] },
    PrepareTodayChatDraft: (request: { generatedMessage?: string }) => ({ message: request?.generatedMessage, fingerprint: 'demo', prefill: { kind: 'brief_card', title: 'Brief' }, counts: { memories: 4, activities: 3, people: 1 } }),
    // About you
    GetAboutYou: { beliefs: beliefs.map((b) => b.view), newThisWeek: [], totals: { beliefs: beliefs.length, confirmed: 1 }, storage: { files: beliefs.length, bytes: 4096, chunks: 12, vectorReady: true } },
    GetMemoryEntries: beliefs.map((b) => b.entry),
    GetMemoryEvidenceReadable: (id: string) => evidence[id] ?? [],
    GetEntryUses: { total: 3, lastAt: NOW - 3_600_000, surfaces: [{ surface: 'chat', count: 2, lastAt: NOW - 3_600_000 }, { surface: 'brief', count: 1, lastAt: NOW - 120_000 }] },
    GetMemoryStatus: {},
    // Settings overview
    GetSettingsOverview: {
        unavailable: [],
        sees: { watching: true, pausedUntil: null, ignoredAppsCount: 2, passwordManagersHidden: true },
        speaksUp: { level: 'normal', dailyCap: 12, quietStart: '22:00', quietEnd: '07:00', channels: ['desktop'] },
        thinksWith: { order: [{ assistant: 'claude', percentLeft: 82, windowLabel: '5 hours', resetsAt: '' }] },
        remembers: { awaitingConfirmCount: 0 },
        keeps: { retention: 30, limitBytes: 20 * 1024 ** 3, usedBytes: 2_400_000_000 },
        briefs: { morningAt: '07:00', eveningAt: '17:00', adaptive: false, phone: null },
        ignores: { apps: [{ name: '1Password', bundleId: 'com.1password.1password' }, { name: 'Messages', bundleId: 'com.apple.MobileSMS' }] },
        worthALook: [],
    },
    GetMobileBriefSchedule: { enabled: true, morning: '07:00', evening: '17:00', lastGeneratedAt: '', lastSlot: '', narrativeAvailable: true, feedEnabled: false, feedUrl: '' },
    GetTextChannelStatus: { supported: true, enabled: false, number: '', includeNames: false, consentAccepted: false, permissionGranted: false, sentToday: 0, lastSentAt: '', lastError: '', verified: false, verifying: false },
    GetSMSChannelStatus: { available: false, state: '', number: '', pendingNumber: '', codeExpiresAt: '', sentToday: 0, sentThisMonth: 0, dailyCap: 0, monthlyCap: 0, lastError: '' },
    // Main chat
    GetMainChat: { session: { localId: MAIN } },
    LoadStreamItemsPage: page,
    GetMainChatOverview: () => ({ conversations: 0, waiting: stream.filter((i) => i.rowClass === 'waiting').length, finished: 0, routine: 0 }),
    GetMainChatWaiting: () => ({ count: stream.filter((i) => i.rowClass === 'waiting').length }),
    ListMainChatWaitingItems: { items: [], hasMore: false, nextCursor: '' },
    ListMainChatAnswerChips: {},
    LoadMainChatProactiveItem: (momentId: string) => stream.find((i) => (i.toolInput as any)?.momentId === momentId) ?? null,
    SendMainChat: (text: string) => reply(text),
    SendMainChatWithContext: (text: string) => reply(text),
    ResolveMainChatProactiveAction: async (momentId: string, action: string) => {
        if (momentId === 'moment-priya' && action === 'send') {
            await new Promise((resolve) => setTimeout(resolve, 600));
            stream = stream.map((i) => i.id === priyaCard.id ? { ...i, rowClass: 'conversation', toolInput: { ...(i.toolInput as object), state: 'acted' } } : i);
            append(sentReport(now()));
        }
        return 'acted';
    },
};

export function installFixture() {
    (window as any).fixtureCalls = calls;
    (window as any).go = { main: { App: new Proxy({}, { get: (_, method: string) => async (...args: unknown[]) => {
        calls.push({ method, args });
        const value = reads[method];
        if (typeof value === 'function') return (value as any)(...args);
        if (value !== undefined) return structuredClone(value);
        const fallback = typedDefaults[method];
        return fallback === undefined ? null : structuredClone(fallback);
    } }) } };
    (window as any).runtime = new Proxy({
        EventsOn: (name: string, cb: (...d: any[]) => void) => { const s = listeners.get(name) ?? new Set(); s.add(cb); listeners.set(name, s); return () => s.delete(cb); },
        EventsOnMultiple: (name: string, cb: (...d: any[]) => void) => (window as any).runtime.EventsOn(name, cb),
        EventsEmit: emit,
    } as Record<string, unknown>, { get: (t, k: string) => t[k] ?? (() => undefined) });
    (window as any).fixtureEmit = emit;
}
