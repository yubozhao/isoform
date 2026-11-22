import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import type { ActivityFeedRow, WatchConversation, WatchRecording } from '@/lib/wails-shim-app';
import { setHostBridgeMediaToken } from '@/lib/host-bridge-media';
import { ActivityWatchRecordings, formatRecordingLength, watchRecordingStateKey } from './activity-watch-recordings';

const mocks = vi.hoisted(() => ({
    list: vi.fn(),
    transcript: vi.fn(),
    remove: vi.fn(),
    conversations: vi.fn(),
    lane: vi.fn(),
    rows: vi.fn(),
    preview: vi.fn(),
    forget: vi.fn(),
}));

vi.mock('@/lib/wails-shim-app', () => ({
    GetWatchRecordings: mocks.list,
    GetWatchRecordingTranscript: mocks.transcript,
    DeleteWatchRecording: mocks.remove,
    GetWatchConversations: mocks.conversations,
    GetWatchLane: mocks.lane,
    GetActivityWatchRows: mocks.rows,
    GetActivityForgetPreview: mocks.preview,
    ForgetActivityFeedRow: mocks.forget,
    GetActivityFeed: vi.fn().mockResolvedValue([]),
}));

const started = new Date();
started.setHours(9, 0, 0, 0);

function recording(overrides: Partial<WatchRecording> = {}): WatchRecording {
    return {
        id: '6f1c2a54-0000-4000-8000-000000000001',
        source: 'watch',
        state: 'transcribed',
        startedAt: started.toISOString(),
        durationSeconds: 3725,
        words: 8,
        ownerVoice: 'labelled',
        sessionId: 'rec-6f1c2a54-0000-4000-8000-000000000001',
        hasAudio: true,
        parts: [
            { part: 0, startedAt: started.toISOString(), durationSeconds: 3600, hasAudio: true, place: 'Mission District', heartRate: 'raised', heartRateAvg: 104, heartRateMax: 131 },
            { part: 1, startedAt: new Date(started.getTime() + 3_600_000).toISOString(), durationSeconds: 125, hasAudio: true, place: '', heartRate: '', heartRateAvg: 0, heartRateMax: 0 },
        ],
        ...overrides,
    };
}

const REC = '6f1c2a54-0000-4000-8000-000000000001';
const QUIET = '6f1c2a54-0000-4000-8000-000000000002';

function conversation(overrides: Partial<WatchConversation> = {}): WatchConversation {
    return {
        id: 'dev:1',
        name: 'Pricing with Sam',
        startAt: started.getTime() + 2000,
        endAt: started.getTime() + 36 * 60_000,
        recordingIds: [REC],
        words: 8,
        speakers: ['you', 'speaker_1'],
        lines: [
            { at: started.getTime() + 2000, speaker: 'you', text: 'Call Sam about the launch', marked: true },
            { at: started.getTime() + 9000, speaker: 'speaker_1', text: 'Friday works for me', marked: false },
        ],
        summary: 'Sam and you settled the launch.',
        decisions: ['Launch Friday'],
        followups: [{ text: 'Send the launch note', owner: 'me', person: '', due: 'Thursday' }],
        speakerNames: { speaker_1: 'Sam' },
        ...overrides,
    };
}

function conversationRow(overrides: Partial<ActivityFeedRow> = {}): ActivityFeedRow {
    return {
        id: 'dev:1', kind: 'watch', startMs: started.getTime(), endMs: started.getTime() + 36 * 60_000,
        title: 'Pricing with Sam', conversation: true, speakers: ['you', 'Sam'], otherVoices: 0, marks: 1, hasNote: true,
        device: 'watch', basis: { setting: 'watch' }, ...overrides,
    };
}

const quietRow: ActivityFeedRow = {
    id: QUIET, kind: 'watch', startMs: started.getTime() - 3_600_000, endMs: started.getTime(), words: 0,
    device: 'phone', recordingId: QUIET, basis: { setting: 'watch' },
};

describe('ActivityWatchRecordings', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        setHostBridgeMediaToken('tok');
        mocks.list.mockResolvedValue([recording(), recording({ id: QUIET, source: 'phone', words: 0, sessionId: '', parts: [] })]);
        mocks.conversations.mockResolvedValue([conversation()]);
        mocks.rows.mockResolvedValue([conversationRow(), quietRow]);
        mocks.lane.mockResolvedValue({ dayStart: 0, hours: [] });
        mocks.preview.mockResolvedValue({ kind: 'watch', id: 'dev:1', title: 'Pricing with Sam', goes: [{ kind: 'words' }, { kind: 'notes' }, { kind: 'followup', title: 'Send the launch note' }, { kind: 'copies' }] });
        mocks.forget.mockResolvedValue(undefined);
    });

    // computer-activity v1.0.0 rule 4: the Watch tab lists the timeline's
    // rows, conversations and quiet hours, newest first, each with Forget.
    it('lists the same rows as the timeline, by day, with Forget on each and no cards', async () => {
        const onCount = vi.fn();
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} onCount={onCount} />);
        expect(await screen.findByText('Pricing with Sam')).toBeTruthy();
        expect(screen.getByText('Today')).toBeTruthy();
        expect(screen.getByText('36m · You, Sam · 1 mark · note')).toBeTruthy();
        expect(screen.getByText('Nothing was said')).toBeTruthy();
        const rows = [...document.querySelectorAll('[data-feed-kind="watch"]')];
        expect(rows).toHaveLength(2);
        for (const row of rows) {
            expect(row.getAttribute('data-feed-basis')).toBe('watch');
            expect(within(row as HTMLElement).getByRole('button', { name: /^Forget: / })).toBeTruthy();
            expect(row.className).not.toMatch(/border-l|before:|after:/);
        }
        // No collapsible cards and no per-card delete.
        expect(screen.queryByRole('button', { expanded: false })).toBeNull();
        expect(screen.queryByText('Delete everywhere')).toBeNull();
        // Asked for the days the watch keeps.
        const [from, to] = mocks.rows.mock.calls[0];
        expect(to - from).toBeGreaterThanOrEqual(7 * 86_400_000);
        expect(onCount).toHaveBeenCalledWith(2);
    });

    // Opening a row shows the conversation: its notes, the words with marks
    // and the names given to speakers, its audio, and the Screen timeline.
    it('opens a conversation to its notes, words, marks, named speakers, audio and timeline', async () => {
        const onOpenSession = vi.fn();
        const { container } = render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={onOpenSession} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Open: Pricing with Sam' }));
        const page = await screen.findByTestId('watch-row-page');
        expect(within(page).getByRole('heading', { name: 'Pricing with Sam' })).toBeTruthy();
        expect(within(page).getByText('Sam and you settled the launch.')).toBeTruthy();
        expect(within(page).getByText('Launch Friday')).toBeTruthy();
        expect(within(page).getByText('Friday works for me')).toBeTruthy();
        // The name the person gave, not "Speaker 1".
        expect(within(page).getByText('Sam')).toBeTruthy();
        expect(within(page).queryByText('Speaker 1')).toBeNull();
        expect(within(page).getAllByText('Marked')).toHaveLength(1);
        expect(within(page).getByText('at Mission District · heart rate raised (avg 104, peak 131)')).toBeTruthy();
        const players = container.querySelectorAll('audio');
        expect(players).toHaveLength(2);
        expect(players[1].getAttribute('src')).toBe(
            `http://127.0.0.1:4000/api/activity/recording?id=${REC}&part=1&token=tok`,
        );
        fireEvent.click(within(page).getByText('Show in Screen timeline'));
        expect(onOpenSession).toHaveBeenCalledWith(`rec-${REC}`);
        fireEvent.click(within(page).getByRole('button', { name: 'Back to your watch' }));
        expect(await screen.findByRole('heading', { name: 'From your watch' })).toBeTruthy();
    });

    it('opens on a conversation the timeline asked for', async () => {
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} openConversationId="dev:1" />);
        const page = await screen.findByTestId('watch-row-page');
        expect(await within(page).findByText('Friday works for me')).toBeTruthy();
    });

    // B7 (#1418): a link to a conversation that is no longer kept shows the
    // list with a quiet line, never a blank page.
    it('shows the list with a quiet line when the asked-for conversation is gone', async () => {
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} openConversationId="dev:gone" />);
        expect(await screen.findByTestId('watch-conversation-gone')).toHaveTextContent('That conversation is no longer kept.');
        expect(screen.queryByTestId('watch-row-page')).toBeNull();
        expect(await screen.findByRole('button', { name: 'Open: Pricing with Sam' })).toBeTruthy();
    });

    it('scrolls an asked-for conversation into view once its row is read', async () => {
        const scroll = vi.fn();
        Element.prototype.scrollIntoView = scroll;
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} openConversationId="dev:1" />);
        await screen.findByTestId('watch-row-page');
        await waitFor(() => expect(scroll).toHaveBeenCalled());
        expect(screen.queryByTestId('watch-conversation-gone')).toBeNull();
    });

    // 15e: the page's Forget is the row's Forget; after it the row and its page go, with no Undo.
    it('forgets from the conversation page with the one confirmation, and the row goes', async () => {
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Open: Pricing with Sam' }));
        const page = await screen.findByTestId('watch-row-page');
        fireEvent.click(within(page).getByRole('button', { name: 'Forget: Pricing with Sam' }));
        const dialog = await screen.findByRole('dialog');
        expect(within(dialog).getByText('Forget "Pricing with Sam"?')).toBeTruthy();
        expect(within(dialog).getByText('Send the launch note · Your work')).toBeTruthy();
        mocks.rows.mockResolvedValue([quietRow]);
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget all 3' }));
        await waitFor(() => expect(mocks.forget).toHaveBeenCalledWith('watch', 'dev:1'));
        await waitFor(() => expect(screen.queryByTestId('watch-row-page')).toBeNull());
        expect(screen.queryByText('Pricing with Sam')).toBeNull();
        expect(screen.queryByText(/Undo/)).toBeNull();
    });

    it('opens a quiet hour to its audio state and says nothing was said', async () => {
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { name: /^Open: Conversation/ }));
        const page = await screen.findByTestId('watch-row-page');
        expect(within(page).getByText('Nothing was said in this hour.')).toBeTruthy();
        expect(within(page).getByText(/Nothing was said$/, { selector: 'span' })).toBeTruthy();
    });

    // A failed read is an error with Try again, never an empty list.
    it('says when the rows cannot be read, with Try again, and not that there are none', async () => {
        mocks.rows.mockRejectedValueOnce(new Error('store down')).mockResolvedValue([conversationRow()]);
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        expect(await screen.findByText("Your watch's conversations could not be loaded.")).toBeTruthy();
        expect(screen.queryByText('Recordings from your watch show here once your phone has sent them.')).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: /Try again/ }));
        expect(await screen.findByText('Pricing with Sam')).toBeTruthy();
    });

    it('says when a conversation page cannot be read, with Try again', async () => {
        mocks.conversations.mockRejectedValueOnce(new Error('store down')).mockResolvedValue([conversation()]);
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Open: Pricing with Sam' }));
        const page = await screen.findByTestId('watch-row-page');
        expect(within(page).getByText('This conversation could not be loaded.')).toBeTruthy();
        fireEvent.click(within(page).getAllByRole('button', { name: /Try again/ })[0]);
        expect(await within(page).findByText('Friday works for me')).toBeTruthy();
    });

    it('says a conversation is no longer kept when its words changed', async () => {
        mocks.conversations.mockResolvedValue([]);
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Open: Pricing with Sam' }));
        expect(await screen.findByText('This conversation is no longer kept.')).toBeTruthy();
    });

    it('says the audio is gone when only the words are kept, and offers no player', async () => {
        mocks.list.mockResolvedValue([
            recording({ hasAudio: false, parts: recording().parts.map((part) => ({ ...part, hasAudio: false })) }),
        ]);
        const { container } = render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Open: Pricing with Sam' }));
        expect(await screen.findByText('The audio is no longer kept. The words stay.')).toBeTruthy();
        expect(container.querySelectorAll('audio')).toHaveLength(0);
    });

    it('names each state and formats lengths', () => {
        expect(watchRecordingStateKey(recording())).toBe('transcribed');
        expect(watchRecordingStateKey(recording({ words: 0 }))).toBe('nothingSaid');
        expect(watchRecordingStateKey(recording({ state: 'failed' }))).toBe('failed');
        expect(watchRecordingStateKey(recording({ state: 'transcribing', sessionId: '' }))).toBe('onComputer');
        expect(formatRecordingLength(75)).toBe('1:15');
        expect(formatRecordingLength(3725)).toBe('1:02:05');
    });
});
