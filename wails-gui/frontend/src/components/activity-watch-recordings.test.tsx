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
}));

vi.mock('@/lib/wails-shim-app', () => ({
    GetWatchRecordings: mocks.list,
    GetWatchRecordingTranscript: mocks.transcript,
    DeleteWatchRecording: mocks.remove,
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
