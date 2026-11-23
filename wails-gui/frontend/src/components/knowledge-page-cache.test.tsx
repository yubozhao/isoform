import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { resetPageQueries } from '@/lib/page-queries';
import { pageQueryCache } from '@/lib/query-cache';
import type { main } from '../../wailsjs/go/models';
import { KnowledgePage } from './knowledge-page';

const mocks = vi.hoisted(() => ({
    listKnowHow: vi.fn(),
    getMatch: vi.fn(),
    dismiss: vi.fn(),
    getCandidate: vi.fn(),
    getEntries: vi.fn(),
    remove: vi.fn(),
    save: vi.fn(),
    listVersions: vi.fn(),
    getVersion: vi.fn(),
    translate: vi.fn(),
    translateVersion: vi.fn(),
    translateAll: vi.fn(),
    toast: vi.fn(),
    listSops: vi.fn(),
    getPlaybooks: vi.fn(),
    getSopAuthor: vi.fn(),
}));

// The tab's learning line has its own test (you-learning-line.test.tsx).
vi.mock('@/components/you/you-learning-line', () => ({ YouLearningLine: () => null }));
vi.mock('@/lib/wails-shim-app', () => ({
    IdeaMeaningGroups: () => Promise.resolve(null),
    ListKnowHow: mocks.listKnowHow,
    GetKnowHowMatch: mocks.getMatch,
    DismissKnowHowMatch: mocks.dismiss,
    GetKnowHowCandidate: mocks.getCandidate,
    GetKnowledgeEntries: mocks.getEntries,
    DeleteKnowledgeEntry: mocks.remove,
    SaveKnowledgeEntry: mocks.save,
    ListKnowledgeVersions: mocks.listVersions,
    GetKnowledgeVersion: mocks.getVersion,
    TranslateKnowledgeEntryToCurrentLanguage: mocks.translate,
    TranslateKnowledgeVersionToCurrentLanguage: mocks.translateVersion,
    StartTranslateAllCurrentKnowledgeEntries: mocks.translateAll,
    ListSOPs: mocks.listSops,
    GetPlaybooksState: mocks.getPlaybooks,
    GetSOPAuthorStatus: mocks.getSopAuthor,
}));
const events = vi.hoisted(() => new Map<string, Array<(...args: any[]) => unknown>>());
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: (name: string, handler: (...args: any[]) => unknown) => {
    const list = events.get(name) ?? [];
    list.push(handler);
    events.set(name, list);
    return () => {};
} }));
vi.mock('@/lib/notify', () => ({ notifyToast: mocks.toast }));

async function emit(name: string, data?: unknown) {
    await act(async () => {
        await Promise.all([...(events.get(name) ?? [])].map((handler) => handler(data)));
    });
}

const CARD = {
    id: 'kh-1',
    title: 'Fixture know-how',
    kind: 'reference',
    takeaway: 'A public reference.',
    points: [],
    apps: [],
    sessionCount: 0,
    savedAt: Date.now(),
    reuseCount: 0,
    topicId: '',
    topicName: '',
    formatVersion: '0.1',
    source: 'activity',
    moments: [],
    mergedIds: [],
} as unknown as main.KnowHowCard;

const ENTRY = {
    ...CARD,
    content: '# Fixture know-how\n\nA public reference.\n',
    date: '2026-10-22',
    sessionIds: [],
} as unknown as main.KnowledgeEntry;

const UPDATED_CARD = { ...CARD, title: 'Updated know-how' } as main.KnowHowCard;

function seedCache(cards: main.KnowHowCard[] = [CARD], entries: main.KnowledgeEntry[] = [ENTRY]) {
    pageQueryCache.set('ListKnowHow', [], cards);
    pageQueryCache.set('GetKnowledgeEntries', [], entries);
    pageQueryCache.set('GetKnowHowMatch', [], null);
}

describe('How you do things page (cache)', () => {
    beforeEach(async () => {
        vi.resetAllMocks();
        resetPageQueries();
        await i18n.changeLanguage('en');
        mocks.listKnowHow.mockResolvedValue([CARD]);
        mocks.listSops.mockResolvedValue([]);
        mocks.getPlaybooks.mockResolvedValue({ plans: [], channels: null, defaultChannels: [] });
        mocks.getSopAuthor.mockResolvedValue(null);
        mocks.getEntries.mockResolvedValue([ENTRY]);
        mocks.getMatch.mockResolvedValue(null);
        mocks.getCandidate.mockResolvedValue(null);
    });

    afterEach(() => {
        cleanup();
        resetPageQueries();
    });

    it('a warm cache paints the saved cards synchronously, with no skeleton', async () => {
        seedCache();
        render(<KnowledgePage />);
        expect(screen.getByText('Fixture know-how')).toBeInTheDocument();
        expect(screen.queryByRole('status')).toBeNull();
        expect(document.querySelector('[aria-busy="true"]')).toBeNull();
        // A fresh cache entry needs no read; revalidation is event-driven.
        expect(mocks.listKnowHow).not.toHaveBeenCalled();
        await act(async () => {});
        expect(screen.getByText('Fixture know-how')).toBeInTheDocument();
        expect(screen.queryByRole('status')).toBeNull();
    });

    it('a cold cache shows the skeleton until the first read lands', async () => {
        render(<KnowledgePage />);
        const busy = screen.getByRole('status');
        expect(busy).toHaveAttribute('aria-busy', 'true');
        expect(busy).toHaveTextContent('Loading your know-how');
        expect(screen.queryByText('Fixture know-how')).toBeNull();
        await act(async () => {});
        expect(screen.queryByRole('status')).toBeNull();
        expect(screen.getByText('Fixture know-how')).toBeInTheDocument();
    });

    it('an invalidation keeps the cached cards on screen until the re-read lands', async () => {
        seedCache();
        render(<KnowledgePage />);
        await act(async () => {});

        let resolveRead!: (cards: main.KnowHowCard[]) => void;
        mocks.listKnowHow.mockImplementationOnce(() => new Promise((resolve) => { resolveRead = resolve; }));
        act(() => {
            pageQueryCache.invalidate(['ListKnowHow']);
        });
        // Stale-while-revalidate: the cached card stays, no skeleton returns.
        expect(screen.getByText('Fixture know-how')).toBeInTheDocument();
        expect(screen.queryByRole('status')).toBeNull();

        await waitFor(() => expect(mocks.listKnowHow).toHaveBeenCalledTimes(1));
        await act(async () => {
            resolveRead([UPDATED_CARD]);
        });
        expect(await screen.findByText('Updated know-how')).toBeInTheDocument();
        expect(screen.queryByText('Fixture know-how')).toBeNull();
    });

    it('a privacy sweep withdraws the hidden retained page before the re-read lands, then a permitted read recovers', async () => {
        seedCache();
        const view = render(<KnowledgePage active={false} />);
        await act(async () => {});
        expect(screen.getByText('Fixture know-how')).toBeInTheDocument();

        // The sweep's re-read hangs: nothing may paint pre-transition text.
        mocks.listKnowHow.mockImplementation(() => new Promise(() => {}));
        mocks.getEntries.mockImplementation(() => new Promise(() => {}));
        mocks.getMatch.mockImplementation(() => new Promise(() => {}));
        await emit('memory:withdrawn');
        expect(screen.queryByText('Fixture know-how')).not.toBeInTheDocument();

        // Reopening while the reads are still held shows the skeleton, never
        // the withdrawn cards.
        view.rerender(<KnowledgePage active={true} />);
        expect(screen.queryByText('Fixture know-how')).not.toBeInTheDocument();
        expect(screen.getByRole('status')).toHaveAttribute('aria-busy', 'true');

        // A later ordinary change re-reads: what is still permitted returns.
        mocks.listKnowHow.mockResolvedValue([UPDATED_CARD]);
        mocks.getEntries.mockResolvedValue([{ ...ENTRY, title: 'Updated know-how' }]);
        await emit('knowledge:updated');
        expect(await screen.findByText('Updated know-how')).toBeInTheDocument();
        expect(screen.queryByText('Fixture know-how')).not.toBeInTheDocument();
    });

    it('a delete re-reads from the backend, not the warm cache', async () => {
        seedCache();
        render(<KnowledgePage />);
        await act(async () => {});
        expect(screen.getByText('Fixture know-how')).toBeInTheDocument();
        expect(mocks.listKnowHow).not.toHaveBeenCalled();

        // Delete through the entry's More menu; the backend removes the entry.
        fireEvent.click(screen.getByText('Fixture know-how'));
        const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
        mocks.remove.mockResolvedValue(undefined);
        mocks.listKnowHow.mockResolvedValue([]);
        mocks.getEntries.mockResolvedValue([]);
        fireEvent.pointerDown(within(detail).getByRole('button', { name: 'More for Fixture know-how' }), { button: 0, pointerType: 'mouse' });
        fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete' }));
        fireEvent.click(await screen.findByRole('button', { name: 'Delete' }));

        await act(async () => {});
        expect(mocks.remove).toHaveBeenCalledWith('kh-1');
        // The reload hit the wire: the deleted card does not come back.
        expect(mocks.listKnowHow).toHaveBeenCalled();
        expect(screen.queryByText('Fixture know-how')).not.toBeInTheDocument();
    });

    it('sign-out withdraws the hidden retained page like the other privacy transitions', async () => {
        seedCache();
        const view = render(<KnowledgePage active={false} />);
        await act(async () => {});
        expect(screen.getByText('Fixture know-how')).toBeInTheDocument();

        // Establish the signed-in baseline, then withdraw the account.
        await emit('model-accounts:updated', { accounts: [{ provider: 'claude', kind: 'subscription', name: 'work', on: true }] });
        expect(screen.getByText('Fixture know-how')).toBeInTheDocument();

        mocks.listKnowHow.mockImplementation(() => new Promise(() => {}));
        mocks.getEntries.mockImplementation(() => new Promise(() => {}));
        mocks.getMatch.mockImplementation(() => new Promise(() => {}));
        await emit('model-accounts:updated', { accounts: [] });
        expect(screen.queryByText('Fixture know-how')).not.toBeInTheDocument();
        view.rerender(<KnowledgePage active={true} />);
        expect(screen.queryByText('Fixture know-how')).not.toBeInTheDocument();
        expect(screen.getByRole('status')).toHaveAttribute('aria-busy', 'true');
    });

    it('a privacy sweep with failing reads shows the failed state, never the withdrawn text', async () => {
        seedCache();
        render(<KnowledgePage active={false} />);
        await act(async () => {});
        expect(screen.getByText('Fixture know-how')).toBeInTheDocument();

        mocks.listKnowHow.mockRejectedValue(new Error('gone'));
        mocks.getEntries.mockRejectedValue(new Error('gone'));
        mocks.getMatch.mockRejectedValue(new Error('gone'));
        await emit('closed-eyes:changed');
        expect(screen.queryByText('Fixture know-how')).not.toBeInTheDocument();
        expect(await screen.findByRole('alert')).toBeInTheDocument();
        expect(screen.queryByText('Fixture know-how')).not.toBeInTheDocument();
    });

    it('a privacy sweep withdraws the open detail and the remembered search text', async () => {
        seedCache();
        render(<KnowledgePage />);
        await act(async () => {});
        // Open the entry's detail (its markdown) and type a filter.
        fireEvent.click(screen.getByText('Fixture know-how'));
        const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
        expect(within(detail).getByText('A public reference.')).toBeInTheDocument();
        const search = screen.getByLabelText('Search know-how');
        fireEvent.change(search, { target: { value: 'fixture' } });
        expect(search).toHaveValue('fixture');

        mocks.listKnowHow.mockImplementation(() => new Promise(() => {}));
        mocks.getEntries.mockImplementation(() => new Promise(() => {}));
        mocks.getMatch.mockImplementation(() => new Promise(() => {}));
        await emit('sensitive-apps:updated');
        expect(screen.queryByText('Fixture know-how')).not.toBeInTheDocument();
        expect(screen.queryByRole('complementary', { name: 'Know-how detail' })).not.toBeInTheDocument();
        // The filter bar unmounts with the cards; the remembered text is gone.
        expect(screen.queryByLabelText('Search know-how')).not.toBeInTheDocument();
    });
});
