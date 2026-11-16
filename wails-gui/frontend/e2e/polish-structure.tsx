// Browser component fixture: production layout and styles, synthetic read data.
// This is a layout guard, not a native/backend integration test.
import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import i18n from '@/i18n';
import '@/index.css';
import { todayNextBrief, dayShapeFixture } from '@/components/today/today-next.fixture';
import { TodayDayStrip } from '@/components/today/TodayDayStrip';
import { TodayPage } from '@/components/today/TodayPage';
import { Composer } from '@/components/composer';
import { MainChatPanel } from '@/components/today/MainChatPanel';
import { TopicPanel } from '@/components/today/TopicPanel';
import { TopicComposer } from '@/components/today/TopicComposer';
import { topicPanelWidth, type TodayTopic } from '@/components/today/topics';
import { MainChatPageColumn, MainChatStreamBody } from '@/components/main-chat/main-chat-stream-body';
import { useMainChatView } from '@/hooks/useMainChatView';
import { ActivityStitchedView, stitchThreads } from '@/components/activity-stitched';
import { stitchedFixture } from '@/components/activity-stitched.fixture';
import { localDayKey } from '@/lib/main-chat-view';
import { MAIN_CHAT_PROACTIVE_TOOL } from '@/lib/stream-helpers';
import type { TodayActionHandlers } from '@/components/today/types';
import type { StreamItem } from '@/types';

const params = new URLSearchParams(location.search);
const noop = () => {};
const topic = 'Turn the dictated UI cleanup and color feedback into a Claude Code task list';
const message = `Where do I stand on “${topic}”, and what should I consider before continuing?`;
const brief = {
    version: 1, briefId: 'layout-evening', slot: 'evening', localDate: '2026-10-22', asOf: '2026-10-22T20:05:00-07:00', timezone: 'America/Los_Angeles', coldStart: false,
    narrativeStatus: 'ok', narrative: [{ text: 'The task list is ready for review.', refs: ['e1'], topicId: 'cleanup', topicName: topic }],
    cards: ['q1', 'q2'].map((id) => ({ id, kind: 'question', mode: 'send', message, paragraph: 0, refs: ['e1'] })),
    sections: { moved: [], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] }, evidence: [],
};
const partialOverlapShape = { ...dayShapeFixture, hold: null, events: [
    { ...dayShapeFixture.events[0], id: 'planning', title: 'Planning', shortName: 'Planning', shortestName: 'Planning', start: '2026-10-23T10:00:00-07:00', end: '2026-10-23T11:00:00-07:00' },
    { ...dayShapeFixture.events[0], id: 'alexandra', title: 'Alexandra', shortName: 'Alexandra', shortestName: 'Alexandra', start: '2026-10-23T10:40:00-07:00', end: '2026-10-23T11:20:00-07:00' },
] };
const reads: Record<string, unknown> = {
    GetLatestBrief: { brief: params.has('today-next') ? todayNextBrief : brief, building: false, enabled: true, nextAt: '', nextSlot: '', empty: false, missing: [] },
    GetTodayDayShape: params.has('today-next') ? params.has('disconnected') ? { ...dayShapeFixture, calendarConnected: false } : params.has('empty') ? { ...dayShapeFixture, events: [], hold: null } : params.has('overlap') ? { ...dayShapeFixture, events: [...dayShapeFixture.events, { ...dayShapeFixture.events[0], id: 'overlap', start: '2026-10-23T09:15:00-07:00' }] } : dayShapeFixture : null,
    GetMobileBriefHealth: params.has('today-next') ? { enabled: true, lastRunAt: todayNextBrief.asOf, nextRunAt: '2026-10-23T18:15:00-07:00', missedRuns: 2 } : null,
    GetCaptureCounts: params.has('today-next') ? { sources: [{ source: 'screens', today: 110 }, { source: 'audioMinutes', today: 2 }] } : null,
    GetTodayNews: { date: '', status: 'ready', items: [], updatedAt: 0 }, GetTodayNewsHistory: [],
    GetTodaySuggestion: { available: false }, GetGoalCheckIn: null, GetTopicFinds: { status: 'off', items: [], topics: [] },
    GetTodayTopics: params.get('page') === 'topics' || params.has('many-topics') || params.has('today-next') ? Array.from({ length: params.has('many-topics') ? 12 : 6 }, (_, i) => ({ id: `topic-${i}`, name: `Topic ${i}`, kind: 'work', status: i === 8 ? 'waiting' : 'moving', latest: { text: 'Updated', at: new Date(2026, 9, i + 1).toISOString() }, pinnedToSidebar: false })) : [], GetTodayQuietLines: [], IsClosedEyesModeEnabled: false,
    GetMainChat: { session: { localId: 'main' } }, LoadStreamItemsPage: { items: [], hasMore: false },
    ListMainChatAnswerChips: {}, GetMainChatOverview: params.get('summary') === 'full'
        ? { conversations: 3, waiting: 1, finished: 1, routine: 9 }
        : params.get('summary') === 'zero'
            ? { conversations: 0, waiting: 0, finished: 0, routine: 0 }
            : params.has('summary')
                ? { conversations: 0, waiting: 4, finished: 0, routine: 0 }
                : { conversations: 1, waiting: 2, finished: 3, routine: 4 },
};
if (params.has('tomorrow')) {
    reads.GetLatestBrief = { ...(reads.GetLatestBrief as object), brief: { ...todayNextBrief, slot: 'evening' } };
    reads.GetTodayDayShape = { ...JSON.parse(JSON.stringify(dayShapeFixture).replaceAll('2026-10-23T', '2026-10-24T')), now: dayShapeFixture.now };
}
if (params.has('brief-retry')) {
    const pending = { pendingSlot: 'morning', pendingAt: '2026-10-23T07:00:00-07:00', retryAt: '2026-10-23T08:45:00-07:00', pendingAttempts: 6, pendingCause: 'timeout' };
    reads.GetLatestBrief = { brief: null, building: params.has('building'), enabled: true, empty: false, missing: [], ...pending };
    reads.GetMobileBriefHealth = { enabled: true, nextRunAt: '2026-10-23T18:00:00-07:00', nextSlot: 'evening', ...pending };
}
if (params.has('late')) reads.GetTodayDayShape = { ...dayShapeFixture, now: '2026-10-23T22:00:00-07:00' };
if (params.has('overnight')) reads.GetTodayDayShape = { ...dayShapeFixture, rangeStart: '2026-10-22T22:00:00-07:00' };
if (params.has('paused-capture')) reads.GetCaptureCounts = { paused: true, sources: [] };
const calls: string[] = [];
(window as any).fixtureCalls = calls;
if (params.has('today-next')) {
    reads.PrepareTodayChatDraft = null;
}
// Provide in-memory reads before React mounts; the fixture never contacts the
// running desktop, its browser bridge, or its persisted state.
(window as any).go = { main: { App: new Proxy({}, { get: (_, method: string) => async (...args: any[]) => { calls.push(method); if (method === 'PrepareTodayChatDraft') return { message: args[0]?.generatedMessage, fingerprint: 'fixture', prefill: { kind: 'brief_card', title: 'Brief' }, counts: {} }; if (method === 'SendMainChatWithContext') return { sessionId: 'main', userItemId: 'sent', sortOrder: 1 }; return reads[method] ?? null; } }) } };
(window as any).runtime = { EventsOn: () => noop };
const handlers: TodayActionHandlers = {
    onStartChat: noop, onRunNow: noop, onOpenMemory: noop, onOpenAutomations: noop, onOpenActivity: noop, onOpenPeople: noop, onOpenSettings: noop, onOpenTopic: noop, onOpenCaptureCounts: noop, onOpenBriefConversation: async () => true,
};
const items: StreamItem[] = params.has('brand-violet') ? [
    { id: 'violet-user', kind: 'user', content: 'Prepare the meeting notes.', rowClass: 'conversation', localDay: localDayKey(new Date()) },
    { id: 'violet-answer', kind: 'assistant', content: '### Prepared notes\n\nI prepared the notes from your meeting. You can review the draft here.\n\n#### Sources\n\nFrom the meeting transcript.', rowClass: 'conversation', localDay: localDayKey(new Date()) },
] : Array.from({ length: 40 }, (_, i) => ({ id: `user-${i}`, kind: 'user', content: `Review the task list, question ${i}.`, rowClass: 'conversation', localDay: localDayKey(new Date()) }));
const summaryItems: StreamItem[] = Array.from({ length: 4 }, (_, i) => ({
    id: `commitment-${i}`, kind: 'system', toolName: MAIN_CHAT_PROACTIVE_TOOL, rowClass: 'waiting', localDay: localDayKey(new Date()),
    toolInput: {
        momentId: `commitment-${i}`, kind: 'commitment_slipping', headline: `Review the release commitment ${i + 1}.`,
        body: 'The release checklist is waiting for your review.', draft: false,
        actionIds: ['open', 'mute_kind'], actionLabels: { open: 'Open', mute_kind: 'Mute this kind' },
        target: { kind: 'commitment', id: `commitment-${i}` }, state: 'pending', evidence: [],
    },
}));
function Chat() {
    const [chatItems, setChatItems] = useState(params.has('summary') ? summaryItems : items);
    useEffect(() => {
        const append = () => setChatItems((current) => [...current, { id: `layout-reply-${current.length}`, kind: 'assistant', content: 'A new reply for this layout check.', rowClass: 'conversation', localDay: localDayKey(new Date()) }]);
        window.addEventListener('fixture:new-reply', append);
        return () => window.removeEventListener('fixture:new-reply', append);
    }, []);
    const crowded = params.has('crowded');
    const view = useMainChatView({ enabled: true, items: chatItems });
    const [topicAttached, setTopicAttached] = useState(params.has('topic'));
    return <MainChatPageColumn enabled={!params.has("legacy")}>
        <MainChatStreamBody mainChat={view} items={chatItems} hasProject streaming={false} wideMode onOpenReportTarget={noop} />
        {params.has('summary') ? <div data-testid="composer-space" className="h-[176px] shrink-0" /> : topicAttached ? <div className="px-6 pb-4"><TopicComposer draft="" prepared={false} onDraftChange={noop} onSend={noop} sending={false}
            inputLabel={i18n.t('today:topicPanel.inputLabel')} placeholder={i18n.t('today:mainChatPanel.placeholder')} sendLabel={i18n.t('today:topicPanel.send')}
            note={i18n.t('today:topicPanel.noteTopic')}
            chip={{ name: 'Halcyon ERP sync', removeLabel: i18n.t('chat:mainChat.view.detachTopic', { name: 'Halcyon ERP sync' }), onRemove: () => setTopicAttached(false) }} />
        </div> : <Composer prompt={crowded ? "An unsent draft" : ""} onPromptChange={noop} onSend={noop} onCancel={noop} onKeyDown={noop}
            model={crowded ? "codex" : "sonnet"} onModelChange={noop} executorGroups={[]} streaming={crowded} ready hasProject
            images={[]} onImagesChange={noop} files={[]} onFilesChange={noop} executorType={crowded ? "codex" : "claude"}
            thinkingLevel={crowded ? "xhigh" : ""} onThinkingLevelChange={noop} thinkingEnabled={false} onToggleThinking={noop}
            planMode={false} onTogglePlanMode={noop} queuedMessages={[]} onRemoveQueuedMessage={noop} onEditQueuedMessage={noop}
            availableCommands={[]} showSuggestions={false} wideMode onVoiceStart={crowded ? noop : undefined} voiceState="idle" />}
    </MainChatPageColumn>;
}
let panelItems = items;
function panelReads() {
    reads.LoadStreamItemsPage = { items: panelItems, hasMore: false };
    reads.ListMainChatTopicItems = { sessionId: 'main', topicId: 'fixture-topic', hasMore: false, nextCursor: '',
        items: panelItems.map(item => ({ id: item.id, kind: 'message', at: Date.now(), message: item })) };
}
if (params.get('page') === 'reply-panel') panelReads();
function ReplyPanel() {
    useEffect(() => {
        const append = () => {
            panelItems = [...panelItems, { id: `panel-reply-${panelItems.length}`, kind: 'assistant', content: 'A new panel reply.' }];
            panelReads();
            window.dispatchEvent(new Event('focus'));
        };
        window.addEventListener('fixture:new-reply', append);
        return () => window.removeEventListener('fixture:new-reply', append);
    }, []);
    const common = { draft: '', prepared: false, onDraftChange: noop, handlers };
    return <div className="h-full" style={{ width: Number(params.get('column') ?? 420) }}>
        {params.get('kind') === 'topic'
            ? <TopicPanel {...common} topic={{ id: 'fixture-topic', name: 'Layout topic', kind: 'work', pinned: false }} variant="pinned" observationPaused={false} />
            : <MainChatPanel {...common} topicsFailed={false} onRetryTopics={noop} preview={params.has('preview') ? { status: 'ready', counts: { memories: 2, activities: 3, people: 1 } } : undefined} />}
    </div>;
}
function Activity() {
    const data = stitchedFixture(new Date(2026, 9, 19));
    return <div className="w-full p-8"><ActivityStitchedView items={stitchThreads(data.threads, data.activities, 0, false)} threads={data.threads}
        status="ready" query="" selectedThreadId={null} onSelectThread={noop} scrollThreadId={null} onScrolled={noop} pickUp={null} onPickUpShow={noop} onPickUpLater={noop} onRetry={noop} /></div>;
}
await i18n.changeLanguage(params.get('lang') ?? 'en');
document.documentElement.dataset.theme = params.get('theme') ?? 'onedark';
createRoot(document.getElementById('root')!).render(
    <div className="flex h-screen w-screen">
        <aside aria-hidden style={{ width: params.get('page') === 'topics' ? 0 : Number(params.get('sidebar') ?? 240), flexShrink: 0 }} />
        <main data-testid="page-area" className="flex min-w-0 flex-1">{params.get('page') === 'day-strip' ? <div className="p-6"><div style={{ width: 1100 }}><TodayDayStrip shape={partialOverlapShape} requestedDate="2026-10-23" timeZone="America/Los_Angeles" /></div></div> : params.get('page') === 'reply-panel' ? <ReplyPanel /> : params.get('page') === 'chat' ? params.has('panel') || params.has('column') ? <div data-testid="narrow-chat-column" className="flex min-w-0" style={{ width: params.has('panel') ? topicPanelWidth(Number(params.get('panel'))) : Number(params.get('column')) }}><Chat /></div> : <Chat /> : params.get('page') === 'activity' ? <Activity /> : <TodayPage handlers={handlers} wideMode />}</main>
    </div>,
);
