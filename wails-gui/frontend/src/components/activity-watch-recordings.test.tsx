import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import '@/i18n';
import type { WatchRecording } from '@/lib/wails-shim-app';
import { setHostBridgeMediaToken } from '@/lib/host-bridge-media';
import { ActivityWatchRecordings, formatRecordingLength, watchRecordingStateKey } from './activity-watch-recordings';

const mocks = vi.hoisted(() => ({
    list: vi.fn(),
    transcript: vi.fn(),
    remove: vi.fn(),
    conversations: vi.fn(),
    lane: vi.fn(),
}));

vi.mock('@/lib/wails-shim-app', () => ({
    GetWatchRecordings: mocks.list,
    GetWatchRecordingTranscript: mocks.transcript,
    DeleteWatchRecording: mocks.remove,
    GetWatchConversations: mocks.conversations,
    GetWatchLane: mocks.lane,
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

describe('ActivityWatchRecordings', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        setHostBridgeMediaToken('tok');
        mocks.list.mockResolvedValue([recording()]);
        mocks.transcript.mockResolvedValue([
            { at: started.getTime() + 2000, speaker: 'you', text: 'Call Sam about the launch', marked: true },
            { at: started.getTime() + 9000, speaker: 'speaker_1', text: 'Friday works for me', marked: false },
        ]);
        mocks.remove.mockResolvedValue(undefined);
        mocks.conversations.mockResolvedValue([]);
        mocks.lane.mockResolvedValue({ dayStart: 0, hours: [] });
    });

    it('lists the conversations found across the hours, above the hours, with who spoke', async () => {
        const at = (minutes: number) => started.getTime() + minutes * 60_000;
        mocks.conversations.mockResolvedValue([
            {
                id: 'dev:1',
                name: '',
                startAt: at(50),
                endAt: at(80),
                recordingIds: ['6f1c2a54-0000-4000-8000-000000000001', '6f1c2a54-0000-4000-8000-000000000002'],
                words: 9,
                speakers: ['you', 'speaker_1'],
                lines: [
                    { at: at(50), speaker: 'you', text: 'Sam, do you have a minute' },
                    { at: at(61), speaker: 'speaker_1', text: 'Sure, keep going' },
                ],
            },
        ]);
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        expect(await screen.findByText('Conversations')).toBeTruthy();
        expect(screen.getByText('Hours')).toBeTruthy();
        const card = screen.getByText('9 words · across 2 hours · You, Speaker 1');
        expect(card).toBeTruthy();
        // One conversation over two hours, opened to its words in order.
        fireEvent.click(card);
        expect(await screen.findByText('Sam, do you have a minute')).toBeTruthy();
        expect(screen.getByText('Sure, keep going')).toBeTruthy();
        // It holds no audio of its own and no delete: the hours below do.
        expect(screen.queryByText('Delete everywhere')).toBeNull();
    });

    it('titles a conversation by its name once it has one, with the time under it', async () => {
        const at = (minutes: number) => started.getTime() + minutes * 60_000;
        mocks.conversations.mockResolvedValue([
            { id: 'dev:1', name: 'Pricing with Sam', startAt: at(50), endAt: at(80), recordingIds: ['a'], words: 9, speakers: ['you'], lines: [] },
        ]);
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        const title = await screen.findByText('Pricing with Sam');
        expect(title.tagName).toBe('H5');
        expect(screen.getByText(/ to .* · 9 words · You$/)).toBeTruthy();
    });

    it('shows a longer conversation with its summary, decisions and follow-ups above its words', async () => {
        const at = (minutes: number) => started.getTime() + minutes * 60_000;
        mocks.conversations.mockResolvedValue([
            {
                id: 'dev:1',
                name: 'Pricing with Sam',
                startAt: at(50),
                endAt: at(80),
                recordingIds: ['a'],
                words: 9,
                speakers: ['you'],
                lines: [{ at: at(50), speaker: 'you', text: 'I will send it tonight' }],
                summary: 'Bo and Sam settled the pricing page.',
                decisions: ['Three tiers'],
                followups: [
                    { text: 'Send the pricing page', owner: 'me', person: '', due: 'tonight' },
                    { text: 'Review it', owner: 'other', person: 'Sam', due: '' },
                ],
            },
        ]);
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        fireEvent.click(await screen.findByText('Pricing with Sam'));
        expect(await screen.findByText('Bo and Sam settled the pricing page.')).toBeTruthy();
        expect(screen.getByText('Decisions')).toBeTruthy();
        expect(screen.getByText('Three tiers')).toBeTruthy();
        expect(screen.getByText('Follow-ups')).toBeTruthy();
        expect(screen.getByText('Send the pricing page')).toBeTruthy();
        expect(screen.getByText('· due tonight')).toBeTruthy();
        expect(screen.getByText('Sam:')).toBeTruthy();
        expect(screen.getByText('I will send it tonight')).toBeTruthy();
    });

    it('still lists the hours when conversations cannot be read', async () => {
        mocks.conversations.mockRejectedValue(new Error('no'));
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        expect(await screen.findByText('1:02:05 · Transcribed')).toBeTruthy();
        expect(screen.queryByText('Conversations')).toBeNull();
    });

    it('lists a recording under its day with its length and state', async () => {
        const onCount = vi.fn();
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} onCount={onCount} />);
        expect(await screen.findByText('Today')).toBeTruthy();
        expect(screen.getByText('1:02:05 · Transcribed')).toBeTruthy();
        expect(onCount).toHaveBeenCalledWith(1);
    });

    it('opens to parts with audio, the place and heart rate line, and who spoke', async () => {
        const onOpenSession = vi.fn();
        const { container } = render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={onOpenSession} />);
        fireEvent.click(await screen.findByRole('button', { expanded: false }));
        expect(await screen.findByText('Friday works for me')).toBeTruthy();
        expect(screen.getByText('You')).toBeTruthy();
        expect(screen.getByText('Speaker 1')).toBeTruthy();
        // The wearer marked the first line; only it carries the label.
        expect(screen.getAllByText('Marked')).toHaveLength(1);
        expect(screen.getByText('at Mission District · heart rate raised (avg 104, peak 131)')).toBeTruthy();
        const players = container.querySelectorAll('audio');
        expect(players).toHaveLength(2);
        expect(players[1].getAttribute('src')).toBe(
            'http://127.0.0.1:4000/api/activity/recording?id=6f1c2a54-0000-4000-8000-000000000001&part=1&token=tok',
        );
        fireEvent.click(screen.getByText('Show in Screen timeline'));
        expect(onOpenSession).toHaveBeenCalledWith('rec-6f1c2a54-0000-4000-8000-000000000001');
    });

    it('asks once, deletes everywhere and reloads the list', async () => {
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { expanded: false }));
        fireEvent.click(screen.getByText('Delete everywhere'));
        expect(screen.getByText('Delete everywhere?')).toBeTruthy();
        expect(mocks.remove).not.toHaveBeenCalled();
        mocks.list.mockResolvedValue([]);
        fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
        await waitFor(() => expect(mocks.remove).toHaveBeenCalledWith('6f1c2a54-0000-4000-8000-000000000001'));
        expect(await screen.findByText('Recordings from your watch show here once your phone has sent them.')).toBeTruthy();
    });

    it('keeps the dialog and says so when the delete fails', async () => {
        mocks.remove.mockRejectedValue(new Error('no'));
        render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { expanded: false }));
        fireEvent.click(screen.getByText('Delete everywhere'));
        fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
        expect(await screen.findByText("Didn't delete. Try again.")).toBeTruthy();
        expect(screen.getByText('1:02:05 · Transcribed')).toBeTruthy();
    });

    it('says the audio is gone when only the words are kept, and offers no player', async () => {
        mocks.list.mockResolvedValue([
            recording({ hasAudio: false, parts: recording().parts.map((part) => ({ ...part, hasAudio: false })) }),
        ]);
        const { container } = render(<ActivityWatchRecordings bridgePort={4000} onOpenSession={vi.fn()} />);
        fireEvent.click(await screen.findByRole('button', { expanded: false }));
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
