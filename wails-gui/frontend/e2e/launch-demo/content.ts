// The demo's sample content: Bo's morning, Lena's 2:00, Priya's reply and the
// Halcyon tests. Every value is made up; no real account data.
import { MAIN_CHAT_PROACTIVE_TOOL, MAIN_CHAT_REPORT_TOOL } from '@/lib/stream-helpers';

const TZ = '-08:00';
export const DAY = '2026-11-03';
const at = (time: string) => `${DAY}T${time}:00${TZ}`;
const ms = (time: string) => new Date(at(time)).getTime();
export const NOW = ms('07:02');
const YESTERDAY = NOW - 24 * 3_600_000;

export const LENA_CHIP = 'Prep me for my 2:00 with Lena';
export const PRIYA_CHIP = 'Reply to Priya about Thursday';

export const brief = {
    version: 1, briefId: `${DAY}-morning`, slot: 'morning', localDate: DAY, asOf: at('07:00'), timezone: 'America/Los_Angeles', coldStart: false,
    narrativeStatus: 'ok',
    narrative: [
        { text: 'Good morning, Bo. Three meetings today, and the one to prepare for is your 2:00 with Lena. Last time she asked whether the Halcyon import can take the full customer file, and that question is still open.', refs: [], topicId: 'lena', topicName: 'Lena' },
        { text: 'Halcyon is close. Yesterday’s session left two tests failing, both in the date parser, and the fix you sketched before you stopped is still on the branch. Finishing it before 2:00 gives you a clear answer for Lena.', refs: [], topicId: 'halcyon', topicName: 'Halcyon' },
        { text: 'Priya wrote last night asking to move Thursday’s review to the afternoon. You usually answer her within the hour, short and direct; a reply in that voice is ready for you to look at.', refs: [], topicId: 'priya', topicName: 'Priya' },
    ],
    cards: [
        { id: 'lena', kind: 'question', mode: 'send', paragraph: 0, label: LENA_CHIP, message: `${LENA_CHIP}.`, refs: [] },
        { id: 'priya', kind: 'question', mode: 'send', paragraph: 2, label: PRIYA_CHIP, message: `${PRIYA_CHIP}.`, refs: [] },
    ],
    sections: { moved: [], doing: [], watching: [], meetings: [], people: [], openLoops: [], learned: [], deliverables: [], schedules: [] },
    evidence: [], dayShape: null,
    limits: { itemsPerSection: 8, evidenceRefs: 60, truncatedSections: [], excludedSensitiveItems: 0 },
};

export const dayShape = {
    rangeStart: at('08:00'), rangeEnd: at('18:00'), now: at('07:02'), calendarConnected: true,
    events: [
        { id: 'standup', start: at('09:00'), end: at('09:15'), title: 'Team standup', shortName: 'Standup', shortestName: 'Standup', initial: 'S', kind: 'meeting' },
        { id: 'lena', start: at('14:00'), end: at('14:45'), title: 'Halcyon with Lena', shortName: 'Lena', shortestName: 'Lena', initial: 'L', kind: 'meeting' },
        { id: 'design', start: at('16:00'), end: at('16:30'), title: 'Design review', shortName: 'Design review', shortestName: 'Design', initial: 'D', kind: 'meeting' },
    ],
    hold: null,
};

const proactive = (id: string, sortOrder: number, input: Record<string, unknown>) => ({
    id, kind: 'system', toolName: MAIN_CHAT_PROACTIVE_TOOL, rowClass: 'waiting', localDay: DAY, sortOrder, createdAt: NOW - 40 * 60_000,
    content: `${input.headline} ${input.body}`,
    toolInput: { draft: false, state: 'pending', evidence: [], ...input },
});

export const priyaCard = proactive('priya-waiting', 3, {
    momentId: 'moment-priya', kind: 'waiting_on_reply',
    headline: 'Priya is waiting on your reply.',
    body: 'She asked to move Thursday’s review to the afternoon.',
    draft: true,
    draftContent: {
        to: 'priya@halcyon.example', subject: 'Re: Thursday review',
        text: 'Hi Priya,\n\nThursday afternoon works. Let’s do 2:30, same room. I’ll bring the import numbers.\n\nBo',
        channel: 'mail', recipient: 'Priya', principalId: 'priya', revision: 'priya-1',
    },
    actionIds: ['send', 'copy_draft', 'open', 'not_now'],
    actionLabels: { send: 'Send', copy_draft: 'Copy draft', open: 'Open Mail', not_now: 'Not now' },
    target: { kind: 'mail', id: 'priya-thread' },
    evidence: [{ kind: 'mail', id: 'priya-thread', at: new Date(YESTERDAY + 14.5 * 3_600_000).toISOString(), target: { kind: 'mail', id: 'priya-thread' } }],
});

export const sentReport = (sentAt: number) => ({
    id: 'priya-sent', kind: 'system', toolName: MAIN_CHAT_REPORT_TOOL, rowClass: 'conversation', localDay: DAY, sortOrder: 10, createdAt: sentAt,
    content: 'Sent via Mail',
    toolInput: { source: 'message_send', key: 'priya-thread', action: '', target: { kind: 'none' }, channel: 'mail', sentAt, principalId: 'priya', title: 'Re: Thursday review' },
});

export const lenaAnswer = [
    '**Your 2:00 with Lena**',
    '',
    '- **What she asked last time:** whether the Halcyon import can take the full customer file, about 40,000 rows, not the sample.',
    '- **What’s still open:** the answer to that, and the import timing she wanted for the board deck.',
    '- **What to know first:** two Halcyon tests are still failing, both in the date parser. The fix you sketched yesterday is on the branch.',
    '',
    'If those two pass before 2:00, you can tell her the full file works.',
].join('\n');

// About you: what Isoform learned, each with the moments it saw.
const HOUR = 3_600_000;
const belief = (id: string, title: string, area: string, points: string[], days: number, extra: Record<string, unknown> = {}) => ({
    view: { id, title, area, status: 'inferred', confidence: 3, firstLearnedAt: NOW - days * 24 * HOUR, updatedAt: NOW - 14 * HOUR, sessionCount: 6, dayCount: days, confirmedAt: 0, ...extra },
    entry: { id, title, content: `# ${title}\n\n${points.map((p) => `- ${p}`).join('\n')}\n`, dimension: 'user_insight', source: 'activity', modTime: NOW - 14 * HOUR, updatedAt: NOW - 14 * HOUR, sizeBytes: 200, apps: ['Mail'], activitySessionIds: [] },
});
export const REPLY_BELIEF = 'Replies to clients within the hour; Priya gets the short version';
export const beliefs = [
    belief('reply-fast', REPLY_BELIEF, 'communication', ['Answers client mail within an hour, mornings included.', 'Keeps replies to Priya to two or three lines, with a time in them.'], 12, { status: 'confirmed', confirmedAt: NOW - 3 * 24 * HOUR }),
    belief('plans-first', 'Plans the whole thing, then builds the first version', 'work', ['Writes the plan before opening the editor.'], 20),
    belief('mornings', 'Does deep work before the first meeting', 'work', ['Blocks the first free stretch of the day for Halcyon.'], 9),
    belief('priya-phone', 'Priya reads on her phone between calls', 'people', ['Priya reads on her phone between calls.'], 14, { sessionCount: 9, dayCount: 7 }),
    belief('priya-long', 'The long version never got answered faster', 'communication', ['The long version never got answered faster.'], 18, { sessionCount: 6, dayCount: 5 }),
    belief('others-day', 'Replies to everyone else wait about a day', 'communication', ['Replies to everyone else wait about a day.'], 16),
    belief('thursday-five', 'Thursdays end at five for Lily’s pickup at 5:30', 'routines', ['Thursdays end at five; Lily’s pickup is at 5:30.'], 42, { sessionCount: 6, dayCount: 6 }),
];

// Your portrait (portrait v2.0.0), fully written. The name comes from the
// app's fixed set of words; "Morning" is not one of them.
const part = (text: string, memoryIds?: string[], confirmed = false) => ({ text, memoryIds, confirmed });
export const PORTRAIT_PHRASE = 'two sentences';
export const portrait = {
    state: 'ready',
    name: ['blueprint', 'builder'],
    nameAreas: [['work', 'routines'], ['communication']],
    sides: [0.18, 0.2, 0.12, 0.1],
    sentences: [
        { role: 'connect', changed: false, parts: [part('You plan the whole thing before you touch it', ['plans-first']), part(', then build the first version yourself, and the week bends around that: '), part('mornings stay free, meetings land after one', ['mornings']), part('.')] },
        { role: 'insight', changed: false, parts: [part('Your replies to Priya run '), part(PORTRAIT_PHRASE, ['priya-phone', 'priya-long']), part(' and '), part('go out within the hour', ['reply-fast'], true), part('. '), part('Replies to everyone else wait about a day', ['others-day']), part('.')] },
        { role: 'life', changed: false, parts: [part('Thursdays end at five because '), part('Lily’s pickup is at 5:30', ['thursday-five']), part(', and nothing has moved that in six weeks.')] },
        { role: 'action', changed: false, parts: [part('So Isoform drafts the short version first, keeps your mornings clear, and puts the Halcyon test status in front of you before standup.')] },
    ],
    lately: null,
    workWithMe: [
        { text: 'Send the number, not the deck.', memoryIds: ['priya-long'], area: 'communication', confirmed: false },
        { text: 'Mornings are for building. Book after one.', memoryIds: ['mornings'], area: 'routines', confirmed: false },
        { text: 'A two-sentence reply is a complete reply.', memoryIds: ['reply-fast'], area: 'communication', confirmed: true },
        { text: 'If it’s already decided, say who decided it.', memoryIds: ['plans-first'], area: 'work', confirmed: false },
    ],
    beliefCount: beliefs.length,
    generatedAt: NOW - 2 * HOUR,
    writing: false, rewriting: false, forgotten: false,
    assistant: 'claude-code',
    candidates: [], confirming: false,
};
export const evidence: Record<string, unknown[]> = {
    'reply-fast': [
        { at: YESTERDAY + 16.25 * HOUR, durationMs: 4 * 60_000, appName: 'Mail', text: 'Answered Priya’s question about the import file 22 minutes after it arrived, in three lines.', activitySessionId: 's1' },
        { at: YESTERDAY - 24 * HOUR + 9.6 * HOUR, durationMs: 6 * 60_000, appName: 'Mail', text: 'Replied to Lena’s schedule change within the hour and proposed a time.', activitySessionId: 's2' },
    ],
};

export const topics = [
    { id: 'halcyon', kind: 'project', name: 'Halcyon import', status: 'moving', latest: { text: 'Two date parser tests still failing', at: new Date(YESTERDAY + 11 * HOUR).toISOString() }, pinnedToSidebar: false },
    { id: 'board-deck', kind: 'project', name: 'Board deck', status: 'moving', latest: { text: 'Import timing slide waits on Halcyon', at: new Date(YESTERDAY + 8 * HOUR).toISOString() }, pinnedToSidebar: false },
    { id: 'thursday-review', kind: 'work', name: 'Thursday review', status: 'waiting', latest: { text: 'Priya asked to move it to the afternoon', at: new Date(YESTERDAY + 14.5 * HOUR).toISOString() }, pinnedToSidebar: false },
];

export const nineAnswer = [
    'Your 9:00 is the team standup, 15 minutes with the Halcyon team.',
    '',
    'The update to give: the import is close. Two tests still fail, both in the date parser, and the fix you sketched yesterday is on the branch. Lena will ask whether the import can take the full customer file at your 2:00, so it helps to say when the fix lands.',
].join('\n');
