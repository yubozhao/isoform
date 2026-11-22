import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { pageQueryCache } from '@/lib/query-cache';
import type { main } from '../../wailsjs/go/models';
import { DevToolsContext } from '@/lib/dev-tools-context';
import { KnowledgePage } from './knowledge-page';
import { resetRenderTierStores } from '@/lib/privacy-reset';
import { Dialog, DialogTitle } from '@/components/ui/dialog';
import { RetainedDialogContent } from '@/components/retained-dialog-content';

const mocks = vi.hoisted(() => ({
    listKnowHow: vi.fn(),
    getMatch: vi.fn(),
    dismiss: vi.fn(),
    getCandidate: vi.fn(),
    confirmCandidate: vi.fn(),
    declineCandidate: vi.fn(),
    undoDeclineCandidate: vi.fn(),
    undoConfirmCandidate: vi.fn(),
    saveCandidate: vi.fn(),
    getEntries: vi.fn(),
    remove: vi.fn(),
    save: vi.fn(),
    listVersions: vi.fn(),
    getVersion: vi.fn(),
    translate: vi.fn(),
    translateVersion: vi.fn(),
    translateAll: vi.fn(),
    getAccountsStatus: vi.fn(),
    toast: vi.fn(),
    listSops: vi.fn(),
    getPlaybooks: vi.fn(),
    getSopAuthor: vi.fn(),
    runSop: vi.fn(),
    generate: vi.fn(),
    generateRange: vi.fn(),
    runNow: vi.fn(),
}));
const chat = vi.hoisted(() => ({
    send: vi.fn(),
    items: [] as { id: string; kind: string; content?: string }[],
    lastUserItemId: '',
    pending: null as null | { state: string },
    waiting: false,
}));
vi.mock('@/hooks/useMainChatPanel', () => ({ useMainChatPanel: () => chat }));

vi.mock('@/lib/wails-shim-app', () => ({
    ListKnowHow: mocks.listKnowHow,
    GetKnowHowMatch: mocks.getMatch,
    DismissKnowHowMatch: mocks.dismiss,
    GetKnowHowCandidate: mocks.getCandidate,
    ConfirmKnowHowCandidate: mocks.confirmCandidate,
    DeclineKnowHowCandidate: mocks.declineCandidate,
    UndoDeclineKnowHowCandidate: mocks.undoDeclineCandidate,
    UndoConfirmKnowHowCandidate: mocks.undoConfirmCandidate,
    SaveKnowHowCandidate: mocks.saveCandidate,
    GetKnowledgeEntries: mocks.getEntries,
    DeleteKnowledgeEntry: mocks.remove,
    SaveKnowledgeEntry: mocks.save,
    ListKnowledgeVersions: mocks.listVersions,
    GetKnowledgeVersion: mocks.getVersion,
    TranslateKnowledgeEntryToCurrentLanguage: mocks.translate,
    TranslateKnowledgeVersionToCurrentLanguage: mocks.translateVersion,
    StartTranslateAllCurrentKnowledgeEntries: mocks.translateAll,
    GetModelAccountsStatus: mocks.getAccountsStatus,
    ListSOPs: mocks.listSops,
    GetPlaybooksState: mocks.getPlaybooks,
    GetSOPAuthorStatus: mocks.getSopAuthor,
    RunSOP: mocks.runSop,
    GenerateSOPFromDescription: mocks.generate,
    GenerateSOPFromRange: mocks.generateRange,
    RunPlaybookNow: mocks.runNow,
    IdeaMeaningGroups: () => Promise.resolve(null),
}));
const events = vi.hoisted(() => new Map<string, Set<(...args: any[]) => unknown>>());
vi.mock('../../wailsjs/runtime/runtime', () => ({
    EventsOn: (name: string, handler: (...args: any[]) => unknown) => {
        const set = events.get(name) ?? new Set();
        set.add(handler);
        events.set(name, set);
        return () => set.delete(handler);
    },
}));
vi.mock('@/lib/notify', () => ({ notifyToast: mocks.toast }));

const DAY = 86_400_000;
const now = Date.now();
const fixSavedAt = now - 3 * DAY;

function card(overrides: Partial<main.KnowHowCard>): main.KnowHowCard {
    return {
        id: 'k',
        title: 'Entry',
        kind: 'reference',
        takeaway: '',
        trigger: '',
        points: [],
        apps: [],
        sessionCount: 0,
        savedAt: now,
        reuseCount: 0,
        topicId: '',
        topicName: '',
        formatVersion: '0.1',
        source: 'activity',
        scope: 'mine',
        ...overrides,
    } as main.KnowHowCard;
}

const CARDS = [
    card({
        id: 'k-fix',
        kind: 'fix',
        title: 'Docker cleanup fails while OrbStack is running',
        takeaway: 'Stop OrbStack, prune, then restart it.',
        points: ['Quit OrbStack from the menu bar.', 'Run the prune command again.'],
        apps: ['Terminal'],
        sessionCount: 2,
        savedAt: fixSavedAt,
        reuseCount: 2,
        topicId: 'dev-env',
        topicName: 'Dev environment',
    }),
    card({
        id: 'k-decision',
        kind: 'decision',
        title: 'Isoform keeps the business logic',
        takeaway: 'Sub-agents get only the task.',
        points: ['Only Claude Code or Codex act as execution agents.'],
        apps: ['Claude', 'Isoform'],
        reuseCount: 4,
    }),
    card({
        id: 'k-reference',
        kind: 'reference',
        title: 'Agent picker scope',
        takeaway: 'Which settings apply per agent.',
        apps: ['GitHub'],
        source: 'slack',
        savedAt: now - 40 * DAY,
    }),
];

function entry(id: string, hasHistory = false): main.KnowledgeEntry {
    return {
        id,
        title: id,
        date: '2026-10-19T19:40:00Z',
        apps: '',
        sessionIds: [],
        content: `---\ntype: Isoform Knowledge\ntitle: ${id}\ntimestamp: 2026-10-19T19:40:00Z\nokf_version: "0.1"\n---\n## Description\n\nRaw body for ${id}\n`,
        hasHistory,
    } as main.KnowledgeEntry;
}

const ENTRIES = [entry('k-fix', true), entry('k-decision'), entry('k-reference')];

const MATCH = {
    entryId: 'k-fix',
    kind: 'fix',
    title: 'Docker cleanup fails while OrbStack is running',
    currentApp: 'Terminal',
    previousAt: fixSavedAt,
    signature: 'sig-1',
} as main.KnowHowMatch;

const CANDIDATE = {
    id: 'knowhow-abc123def4',
    title: 'Fresh-install QA before a release',
    description: 'The same 4 steps in Terminal and Isoform, 4 times.',
    trigger: 'When you open release checklist again.',
    steps: ['Remove the app.', 'Install the signed build.', 'Walk through onboarding.', 'Confirm the first brief.'],
    apps: ['Terminal', 'Isoform'],
    sessionIds: ['s-1', 's-2', 's-3', 's-4'],
    times: 4,
    occurredAtMs: [now - 14 * DAY, now - 7 * DAY, now - DAY, new Date(now).setHours(0, 0, 0, 0)],
    avgDurationMs: 40 * 60_000,
    scope: 'mine',
} as main.KnowHowCandidate;

async function renderPage(
    props: Partial<React.ComponentProps<typeof KnowledgePage>> = {},
    { devTools = false }: { devTools?: boolean } = {},
) {
    const escapeRef: { current: (() => boolean) | null } = { current: null };
    await act(async () => {
        render(
            <DevToolsContext.Provider value={devTools}>
                <KnowledgePage escapeRef={escapeRef} {...props} />
            </DevToolsContext.Provider>,
        );
    });
    const escape = async () => {
        let handled = false;
        await act(async () => {
            handled = escapeRef.current!();
        });
        return handled;
    };
    return { escape };
}

function cardButton(title: string) {
    const found = screen.getAllByRole('button', { name: new RegExp(title) }).find((el) => el.hasAttribute('data-know-how-id'));
    if (!found) throw new Error(`no card for ${title}`);
    return found;
}

async function openMenu(name: string) {
    await act(async () => {
        fireEvent.keyDown(screen.getByRole('button', { name }), { key: 'Enter' });
    });
}

async function chooseMenuItem(name: string) {
    await act(async () => {
        const item = (await screen.findByText(name)).closest('[role^="menuitem"]');
        if (!item) throw new Error(`no menu item ${name}`);
        fireEvent.click(item);
    });
}

async function selectCard(title: string) {
    await act(async () => {
        fireEvent.click(cardButton(title));
    });
}

const RAW_TIMESTAMP = /\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/;

describe('How you do things page', () => {
    beforeEach(async () => {
        vi.resetAllMocks();
        pageQueryCache.clear();
        await i18n.changeLanguage('en');
        mocks.listKnowHow.mockResolvedValue(CARDS);
        mocks.getEntries.mockResolvedValue(ENTRIES);
        mocks.listSops.mockResolvedValue([]);
        mocks.getPlaybooks.mockResolvedValue({ plans: [], channels: null, defaultChannels: [] });
        mocks.getSopAuthor.mockResolvedValue(null);
        mocks.getMatch.mockResolvedValue(null);
        mocks.getCandidate.mockResolvedValue(null);
        mocks.dismiss.mockResolvedValue(undefined);
        mocks.remove.mockResolvedValue(undefined);
        mocks.listVersions.mockResolvedValue([]);
        mocks.translate.mockImplementation(async (id: string) => entry(id));
    });

    afterEach(() => {
        cleanup();
    });

    it('shows cards by kind with reuse counts and times in words, never raw timestamps or format versions', async () => {
        await renderPage();
        expect(screen.getByRole('heading', { level: 1, name: 'Things you’ve figured out, ready to reuse' })).toBeInTheDocument();
        expect(screen.getByText(/3 pieces of know-how from your work, 2 of them this week\./)).toBeInTheDocument();

        const fix = cardButton('Docker cleanup');
        expect(within(fix).getByText('Fix')).toBeInTheDocument();
        expect(within(fix).getByText('Used twice')).toBeInTheDocument();
        expect(within(cardButton('Isoform keeps')).getByText('Used 4 times')).toBeInTheDocument();
        expect(within(cardButton('Isoform keeps')).getByText('Today · Claude, Isoform')).toBeInTheDocument();
        // W15: the reuse line shows only from 1.
        expect(within(cardButton('Agent picker')).queryByText(/Used|Not used yet/)).toBeNull();

        await selectCard('Docker cleanup');
        const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
        expect(within(detail).getByRole('heading', { level: 2, name: CARDS[0].title })).toBeInTheDocument();
        expect(within(detail).getByText('Learned from your work on this computer.')).toBeInTheDocument();
        expect(within(detail).getByText('Topic: Dev environment')).toBeInTheDocument();
        expect(document.body.textContent).not.toMatch(RAW_TIMESTAMP);
        expect(document.body.textContent).not.toContain('Format version');
        expect(document.body.textContent).not.toContain('2 sessions');
        expect(document.body.textContent).not.toContain('source: activity');
    });

    it('keeps the format version, source, sessions and saved time in Technical details', async () => {
        await renderPage({}, { devTools: true });
        await selectCard('Docker cleanup');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Technical details' }));
        });
        const iso = new Date(fixSavedAt).toISOString().slice(0, 16);
        expect(screen.getByText(`Format version 0.1 · source: activity · 2 sessions · saved ${iso}Z · scope: mine`)).toBeInTheDocument();
        expect(screen.getByText('ID k-fix · topic dev-env')).toBeInTheDocument();
        expect(screen.getByText('Full entry')).toBeInTheDocument();
        expect(screen.getByText('Raw body for k-fix')).toBeInTheDocument();
    });

    it('lists every entry when reuse counts are unknown, without a count', async () => {
        mocks.listKnowHow.mockResolvedValue(CARDS.map((c) => ({ ...c, reuseCount: -1 })));
        await renderPage();
        for (const c of CARDS) {
            expect(within(cardButton(c.title.split(' ')[0])).queryByText(/Used|Not used yet/)).toBeNull();
        }
        await selectCard('Docker cleanup');
        const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
        expect(within(detail).queryByText(/Used|Not used yet/)).toBeNull();
        expect(screen.queryByText('Could not load your know-how.')).toBeNull();
    });

    it('marks the selected kind and card with the surface-active token', async () => {
        await renderPage();
        const group = screen.getByRole('group', { name: 'Filter by kind' });
        expect(within(group).getByRole('button', { name: 'All' })).toHaveClass('bg-surface-active');
        expect(within(group).getByRole('button', { name: 'Fix' })).not.toHaveClass('bg-surface-active');
        await selectCard('Docker cleanup');
        expect(cardButton('Docker cleanup')).toHaveClass('bg-surface-active');
        expect(cardButton('Docker cleanup')).not.toHaveClass('border-ink');
        expect(cardButton('Isoform keeps')).not.toHaveClass('bg-surface-active');
    });

    it('filters by kind', async () => {
        await renderPage();
        const group = screen.getByRole('group', { name: 'Filter by kind' });
        expect(within(group).getAllByRole('button').map((b) => b.textContent)).toEqual(['All', 'Decision', 'How-to', 'Fix', 'Gotcha', 'Reference']);
        await act(async () => {
            fireEvent.click(within(group).getByRole('button', { name: 'Fix' }));
        });
        expect(within(group).getByRole('button', { name: 'Fix' })).toHaveAttribute('aria-pressed', 'true');
        expect(cardButton('Docker cleanup')).toBeInTheDocument();
        expect(document.querySelector('[data-know-how-id="k-decision"]')).toBeNull();
        await act(async () => {
            fireEvent.click(within(group).getByRole('button', { name: 'Gotcha' }));
        });
        expect(screen.getByText('No Gotcha know-how yet.')).toBeInTheDocument();
    });

    it("shows You've hit this before; Show the fix opens the entry", async () => {
        mocks.getMatch.mockResolvedValue(MATCH);
        await renderPage();
        const banner = screen.getByTestId('know-how-match');
        expect(within(banner).getByText('You’ve hit this before: Docker cleanup fails while OrbStack is running')).toBeInTheDocument();
        const date = new Date(fixSavedAt).toLocaleDateString('en', { month: 'short', day: 'numeric' });
        expect(within(banner).getByText(`You’re in Terminal with the same error you fixed on ${date}.`)).toBeInTheDocument();

        // The fix opens even while another kind is filtered.
        await act(async () => {
            fireEvent.click(within(screen.getByRole('group', { name: 'Filter by kind' })).getByRole('button', { name: 'Decision' }));
        });
        await act(async () => {
            fireEvent.click(within(banner).getByRole('button', { name: 'Show the fix' }));
        });
        expect(screen.getByRole('heading', { level: 2, name: MATCH.title })).toBeInTheDocument();
        expect(cardButton('Docker cleanup')).toHaveAttribute('aria-pressed', 'true');
        expect(mocks.dismiss).not.toHaveBeenCalled();
    });

    it('Not the same dismisses the match, and says so when that fails', async () => {
        mocks.getMatch.mockResolvedValue(MATCH);
        mocks.dismiss.mockRejectedValueOnce(new Error('disk full'));
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Not the same' }));
        });
        expect(mocks.dismiss).toHaveBeenCalledWith('k-fix', 'sig-1');
        expect(screen.getByRole('alert')).toHaveTextContent('Didn’t save.');
        expect(screen.getByTestId('know-how-match')).toBeInTheDocument();

        // The dismissal is now recorded; the re-read after a successful
        // dismiss returns no match.
        mocks.getMatch.mockResolvedValue(null);
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Not the same' }));
        });
        expect(mocks.dismiss).toHaveBeenCalledTimes(2);
        expect(screen.queryByTestId('know-how-match')).toBeNull();
    });

    it('W2: no Add to agent rules, one-click fix or Remind me; a reference has only Ask', async () => {
        await renderPage();
        for (const title of ['Docker cleanup', 'Isoform keeps', 'Agent picker']) {
            await selectCard(title);
            const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
            expect(within(detail).queryByRole('button', { name: /one-click|agent rules|Remind me/ })).toBeNull();
        }
        const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
        expect(within(detail).getAllByRole('button', { name: 'Ask about this' })).toHaveLength(1);
        expect(within(detail).getByText('Learned from your Slack messages.')).toBeInTheDocument();
        expect(within(detail).queryByRole('button', { name: /playbook/ })).toBeNull();
    });

    it('shows a busy state, then a failure with Try again', async () => {
        let reject!: (err: Error) => void;
        mocks.listKnowHow.mockReturnValueOnce(new Promise((_, r) => { reject = r; }));
        await renderPage();
        const busy = screen.getByRole('status');
        expect(busy).toHaveAttribute('aria-busy', 'true');
        expect(busy).toHaveTextContent('Loading your know-how');
        await act(async () => reject(new Error('boom')));
        expect(screen.getByRole('alert')).toHaveTextContent('Could not load your know-how.');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        });
        expect(mocks.listKnowHow).toHaveBeenCalledTimes(2);
        expect(cardButton('Docker cleanup')).toBeInTheDocument();
    });

    it('keeps the cards when a later re-read fails', async () => {
        await renderPage();
        mocks.listKnowHow.mockRejectedValueOnce(new Error('later'));
        await selectCard('Docker cleanup');
        mocks.save.mockResolvedValue(entry('k-fix'));
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
        });
        // The mount read left a fresh cached value; only an invalidation (the
        // backend's insight:saved/knowledge:updated) makes the re-read reach
        // the binding again. The failed re-read keeps the painted cards.
        await act(async () => {
            pageQueryCache.invalidate(['ListKnowHow', 'GetKnowledgeEntries']);
        });
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        });
        // The save's own invalidate-then-reload always re-reads fresh.
        expect(mocks.listKnowHow).toHaveBeenCalledTimes(3);
        expect(cardButton('Docker cleanup')).toBeInTheDocument();
        expect(screen.queryByText('Could not load your know-how.')).toBeNull();
    });

    it('Escape steps back through history, edit and the open entry, then lets the dialog close', async () => {
        mocks.listVersions.mockResolvedValue([]);
        const { escape } = await renderPage();
        expect(await escape()).toBe(false);

        await selectCard('Docker cleanup');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
        });
        expect(await escape()).toBe(true);
        expect(screen.queryByRole('textbox', { name: 'Steps, one per line' })).toBeNull();
        expect(screen.getByRole('heading', { level: 2, name: CARDS[0].title })).toBeInTheDocument();

        await openMenu(`More for ${CARDS[0].title}`);
        await chooseMenuItem('Version history');
        expect(screen.getByRole('button', { name: 'Current version' })).toBeInTheDocument();
        expect(await escape()).toBe(true);
        expect(screen.queryByRole('button', { name: 'Current version' })).toBeNull();

        expect(await escape()).toBe(true);
        expect(cardButton('Docker cleanup')).toHaveAttribute('aria-pressed', 'false');
        expect(await escape()).toBe(false);
    });

    it('inside the dialog, Escape closes an open menu first, then the entry, then the dialog', async () => {
        const escapeRef: { current: (() => boolean) | null } = { current: null };
        const onOpenChange = vi.fn();
        await act(async () => {
            render(
                <Dialog open onOpenChange={onOpenChange}>
                    <RetainedDialogContent
                        open
                        retain={false}
                        onEscapeKeyDown={(e) => {
                            if (escapeRef.current?.()) e.preventDefault();
                        }}
                    >
                        <DialogTitle>Know-how</DialogTitle>
                        <KnowledgePage escapeRef={escapeRef} />
                    </RetainedDialogContent>
                </Dialog>,
            );
        });
        const pressEscape = async () => {
            await act(async () => {
                fireEvent.keyDown(document.activeElement ?? document.body, { key: 'Escape' });
            });
        };
        await selectCard('Docker cleanup');
        await openMenu(`More for ${CARDS[0].title}`);
        expect(await screen.findByRole('menu')).toBeInTheDocument();
        await pressEscape();
        expect(screen.queryByRole('menu')).toBeNull();
        expect(cardButton('Docker cleanup')).toHaveAttribute('aria-pressed', 'true');
        expect(onOpenChange).not.toHaveBeenCalled();

        await pressEscape();
        expect(cardButton('Docker cleanup')).toHaveAttribute('aria-pressed', 'false');
        expect(onOpenChange).not.toHaveBeenCalled();

        await pressEscape();
        expect(onOpenChange).toHaveBeenCalledWith(false);
    });

    it('shows the empty state when there is no know-how yet', async () => {
        mocks.listKnowHow.mockResolvedValue([]);
        mocks.getEntries.mockResolvedValue([]);
        await renderPage();
        expect(screen.getByText('No know-how yet')).toBeInTheDocument();
        expect(screen.queryByRole('group', { name: 'Filter by kind' })).toBeNull();
    });

    it('reads in Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        mocks.getMatch.mockResolvedValue(MATCH);
        await renderPage();
        expect(screen.getByRole('heading', { level: 1, name: '你摸索出来的办法，随时可以再用' })).toBeInTheDocument();
        expect(within(cardButton('Docker cleanup')).getByText('修复')).toBeInTheDocument();
        expect(within(cardButton('Docker cleanup')).getByText('用过 2 次')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: '查看解决办法' })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: '不是同一个问题' })).toBeInTheDocument();
    });
});

// Decision 11: everything the How you do things page had stays reachable from a page
// More menu, the entry's More menu or Technical details.
describe('How you do things keeps every Knowledge action', () => {
    beforeEach(async () => {
        vi.resetAllMocks();
        pageQueryCache.clear();
        await i18n.changeLanguage('en');
        mocks.listKnowHow.mockResolvedValue(CARDS);
        mocks.getEntries.mockResolvedValue(ENTRIES);
        mocks.listSops.mockResolvedValue([]);
        mocks.getPlaybooks.mockResolvedValue({ plans: [], channels: null, defaultChannels: [] });
        mocks.getSopAuthor.mockResolvedValue(null);
        mocks.getMatch.mockResolvedValue(null);
        mocks.getCandidate.mockResolvedValue(null);
        mocks.remove.mockResolvedValue(undefined);
        mocks.listVersions.mockResolvedValue([{ timestampMs: fixSavedAt - DAY, sizeBytes: 10 }]);
        mocks.getVersion.mockResolvedValue('## Description\n\nAn older version\n');
        mocks.translate.mockImplementation(async (id: string) => entry(id, true));
        mocks.translateAll.mockResolvedValue({ total: 3, targetLanguage: 'English' });
    });

    afterEach(() => {
        cleanup();
    });

    it('delete: the entry More menu deletes after confirming', async () => {
        await renderPage();
        await selectCard('Docker cleanup');
        await openMenu(`More for ${CARDS[0].title}`);
        await chooseMenuItem('Delete');
        const dialog = await screen.findByRole('dialog');
        expect(within(dialog).getByText('Delete this know-how')).toBeInTheDocument();
        mocks.listKnowHow.mockResolvedValue(CARDS.slice(1));
        await act(async () => {
            fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
            // DeleteKnowledgeEntry emits knowledge:updated, which invalidates
            // these reads; the mocked runtime never fires it, so invalidate
            // directly for the re-read to reach the binding.
            pageQueryCache.invalidate(['ListKnowHow', 'GetKnowledgeEntries']);
        });
        expect(mocks.remove).toHaveBeenCalledWith('k-fix');
        expect(document.querySelector('[data-know-how-id="k-fix"]')).toBeNull();
        expect(screen.queryByRole('complementary', { name: 'Know-how detail' })).toBeNull();
    });

    it('copy raw markdown: the entry More menu copies the file as written', async () => {
        const writeText = vi.fn().mockResolvedValue(undefined);
        Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
        await renderPage();
        await selectCard('Docker cleanup');
        await openMenu(`More for ${CARDS[0].title}`);
        await chooseMenuItem('Copy markdown');
        expect(writeText).toHaveBeenCalledWith(ENTRIES[0].content);
    });

    it('W14: Edit opens Title, When to use it and Steps; the header is kept and never shown', async () => {
        mocks.save.mockImplementation(async (id: string, content: string) => ({ ...entry(id), content }));
        await renderPage();
        await selectCard('Docker cleanup');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
        });
        expect(screen.getByRole('heading', { level: 2, name: 'Edit this know-how' })).toBeInTheDocument();
        expect(screen.getByRole('textbox', { name: 'Title' })).toHaveValue('k-fix');
        expect(screen.getByRole('textbox', { name: 'When to use it' })).toBeInTheDocument();
        const steps = screen.getByRole('textbox', { name: 'Steps, one per line' });
        expect(document.body.textContent).not.toContain('okf_version');
        expect(screen.queryByDisplayValue(/type: Isoform Knowledge/)).toBeNull();
        fireEvent.change(steps, { target: { value: 'Quit OrbStack\nRun the cleanup again' } });
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        });
        const written = mocks.save.mock.calls.at(-1)![1] as string;
        expect(mocks.save.mock.calls.at(-1)![0]).toBe('k-fix');
        expect(written).toContain('type: Isoform Knowledge');
        expect(written).toContain('okf_version: "0.1"');
        expect(written).toMatch(/- Quit OrbStack\n- Run the cleanup again/);
        expect(screen.queryByRole('textbox', { name: 'Steps, one per line' })).toBeNull();
        expect(mocks.toast).not.toHaveBeenCalled();
    });

    it('W9: a failed edit save stays in the panel, copper, and Try again saves again', async () => {
        mocks.save.mockRejectedValueOnce(new Error('disk full')).mockImplementation(async (id: string, content: string) => ({ ...entry(id), content }));
        await renderPage();
        await selectCard('Docker cleanup');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
        });
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        });
        expect(screen.getByText('Didn’t save.')).toBeInTheDocument();
        expect(document.body.textContent).not.toContain('disk full');
        expect(mocks.toast).not.toHaveBeenCalled();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        });
        expect(mocks.save).toHaveBeenCalledTimes(2);
        expect(screen.queryByRole('textbox', { name: 'Steps, one per line' })).toBeNull();
    });

    it('no At a glance: the list takes the full width until a card is picked (design review v1.0.1 W39)', async () => {
        mocks.listKnowHow.mockResolvedValue([
            ...CARDS,
            card({ id: 'k-prompt', title: 'Hub notes', apps: ['you-are-the-isoform-hub-for'], savedAt: now }),
        ]);
        await renderPage();
        expect(screen.queryByRole('complementary', { name: 'Know-how detail' })).toBeNull();
        for (const gone of ['At a glance', 'When it was saved', 'Top sources']) {
            expect(screen.queryByText(gone)).toBeNull();
        }
        await openMenu('More for know-how');
        expect(screen.queryByRole('menuitem', { name: 'At a glance' })).toBeNull();
        await chooseMenuItem('Group by when it was saved');
        expect(screen.getByRole('region', { name: 'Today' })).toBeInTheDocument();
        expect(within(screen.getByRole('region', { name: 'This week' })).getByText(CARDS[0].title)).toBeInTheDocument();
        expect(screen.queryByRole('complementary', { name: 'Know-how detail' })).toBeNull();

        await selectCard('Docker cleanup');
        const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
        expect(within(detail).getByText(CARDS[0].title)).toBeInTheDocument();
        expect(within(detail).queryByText('Top sources')).toBeNull();
    });

    it('one lesson, once: a folded card lists every moment under Where it came from (design review v1.0.1 W40)', async () => {
        const at = (day: number, hour: number) => new Date(2026, 10, day, hour, 10).getTime();
        const lesson = card({
            id: 'k-rename',
            kind: 'fix',
            title: 'Juniper Bakery deploys broke after repo rename',
            takeaway: 'Point the site at the new repo.',
            savedAt: at(3, 14),
            mergedIds: ['k-rename-2', 'k-rename-3'],
            moments: [
                { at: at(1, 9), assistant: '', app: 'Terminal', sessionId: 's-1' },
                { at: at(2, 11), assistant: 'codex', app: '', sessionId: '' },
                { at: at(3, 14), assistant: 'claude', app: '', sessionId: '' },
            ] as main.KnowHowMoment[],
        });
        mocks.listKnowHow.mockResolvedValue([lesson, ...CARDS]);
        const onOpenActivitySession = vi.fn();
        await renderPage({ onOpenActivitySession });
        expect(screen.getAllByText('Juniper Bakery deploys broke after repo rename')).toHaveLength(1);
        await selectCard('Juniper Bakery');
        const moments = screen.getByRole('list', { name: 'Where it came from' });
        const items = within(moments).getAllByRole('listitem');
        expect(items).toHaveLength(3);
        expect(items[0]).toHaveTextContent(/Nov 1.*Terminal/);
        expect(items[1]).toHaveTextContent(/Nov 2.*Codex/);
        expect(items[2]).toHaveTextContent(/Nov 3, 2:10\s?PM · Claude Code/);
        fireEvent.click(within(items[0]).getByRole('button'));
        expect(onOpenActivitySession).toHaveBeenCalledWith('s-1');

        await openMenu('More for Juniper Bakery deploys broke after repo rename');
        await chooseMenuItem('Delete');
        const dialog = await screen.findByRole('dialog');
        await act(async () => {
            fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
        });
        expect(mocks.remove.mock.calls.map(([id]) => id)).toEqual(['k-rename', 'k-rename-2', 'k-rename-3']);
    });

    it('translate: one entry from its More menu, all from the page More menu', async () => {
        await renderPage();
        await selectCard('Docker cleanup');
        await openMenu(`More for ${CARDS[0].title}`);
        await chooseMenuItem('Translate to the app’s language');
        expect(mocks.translate).toHaveBeenCalledWith('k-fix');

        await openMenu('More for know-how');
        await chooseMenuItem('Translate all know-how');
        const dialog = await screen.findByRole('dialog');
        await act(async () => {
            fireEvent.click(within(dialog).getByRole('button', { name: 'Translate' }));
        });
        expect(mocks.translateAll).toHaveBeenCalledWith({ target: 'follow-ui', language: '' });
        expect(screen.getByRole('status')).toHaveTextContent('Translating all know-how…');
        expect(screen.getByRole('status')).not.toHaveTextContent('0 /');
    });

    it('translate all says why it could not start, with no entry open', async () => {
        mocks.translateAll.mockRejectedValueOnce(new Error('No model is set up.'));
        await renderPage();
        await openMenu('More for know-how');
        await chooseMenuItem('Translate all know-how');
        const dialog = await screen.findByRole('dialog');
        await act(async () => {
            fireEvent.click(within(dialog).getByRole('button', { name: 'Translate' }));
        });
        expect(within(dialog).getByRole('alert')).toHaveTextContent('Could not start the translation. No model is set up.');
        expect(within(dialog).getByRole('button', { name: 'Translate' })).toBeEnabled();
    });

    it('history: the entry More menu opens version history', async () => {
        await renderPage();
        await selectCard('Docker cleanup');
        await openMenu(`More for ${CARDS[0].title}`);
        await chooseMenuItem('Version history');
        await waitFor(() => expect(mocks.listVersions).toHaveBeenCalledWith('k-fix'));
        expect(screen.getByText('Version history')).toBeInTheDocument();
        expect(await screen.findByText('An older version')).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Current version' })).toBeInTheDocument();
    });

    it('history: a failed load is copper with a working Try again, never the raw error', async () => {
        mocks.listVersions.mockRejectedValueOnce(new Error('open /Users/fictional/.isoform/knowledge: EACCES'));
        await renderPage();
        await selectCard('Docker cleanup');
        await openMenu(`More for ${CARDS[0].title}`);
        await chooseMenuItem('Version history');
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent('Couldn’t load the versions.');
        expect(alert.className).toContain('text-copper');
        expect(alert.className).not.toMatch(/text-error|bg-error/);
        expect(screen.queryByText(/EACCES/)).toBeNull();
        expect(screen.queryByText(/No history yet/)).toBeNull();
        await act(async () => {
            fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
        });
        expect(await screen.findByText('An older version')).toBeInTheDocument();
        expect(screen.queryByRole('alert')).toBeNull();
    });

    it('history shows a version in words; its raw metadata stays in Technical details', async () => {
        const onOpenActivitySession = vi.fn();
        mocks.getVersion.mockResolvedValue(
            '---\ntype: Isoform Knowledge\ntimestamp: 2026-10-18T10:00:00Z\nsource: activity\nactivity_sessions: ["s-9"]\n---\n## Description\n\nAn older version\n',
        );
        await renderPage({ onOpenActivitySession }, { devTools: true });
        await selectCard('Docker cleanup');
        await openMenu(`More for ${CARDS[0].title}`);
        await chooseMenuItem('Version history');
        expect(await screen.findByText('An older version')).toBeInTheDocument();
        const panel = screen.getByRole('complementary', { name: 'Know-how detail' });
        expect(panel.textContent).not.toMatch(RAW_TIMESTAMP);
        expect(panel.textContent).not.toMatch(/Metadata|Activity Session\b/);
        expect(within(panel).queryByText('activity')).toBeNull();

        await act(async () => {
            fireEvent.click(within(panel).getByRole('button', { name: 'Technical details' }));
        });
        expect(within(panel).getByText('Saved')).toBeInTheDocument();
        expect(within(panel).getByText('2026-10-18T10:00:00Z')).toBeInTheDocument();
        expect(within(panel).getByText('activity')).toBeInTheDocument();
        await act(async () => {
            fireEvent.click(within(panel).getByRole('button', { name: 'Session 1' }));
        });
        expect(onOpenActivitySession).toHaveBeenCalledWith('s-9');
    });

    it('search: finds know-how by its words', async () => {
        await renderPage({}, { devTools: true });
        fireEvent.change(screen.getByRole('searchbox', { name: 'Search know-how' }), { target: { value: 'orbstack' } });
        expect(cardButton('Docker cleanup')).toBeInTheDocument();
        expect(document.querySelector('[data-know-how-id="k-decision"]')).toBeNull();
        fireEvent.change(screen.getByRole('searchbox', { name: 'Search know-how' }), { target: { value: 'nothing like this' } });
        expect(screen.getByText('No know-how matches your search.')).toBeInTheDocument();
    });

    it('Activity sessions stay one click away in Technical details', async () => {
        const onOpenActivitySession = vi.fn();
        mocks.getEntries.mockResolvedValue([
            {
                ...ENTRIES[0],
                content: '---\ntype: Isoform Knowledge\ntitle: k-fix\nactivity_sessions: ["session-7"]\n---\n## Description\n\nBody\n',
            },
            ...ENTRIES.slice(1),
        ]);
        await renderPage({ onOpenActivitySession }, { devTools: true });
        await selectCard('Docker cleanup');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Technical details' }));
        });
        const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
        expect(within(detail).getByText('Activity sessions')).toBeInTheDocument();
        await act(async () => {
            fireEvent.click(within(detail).getByRole('button', { name: 'Session 1' }));
        });
        expect(onOpenActivitySession).toHaveBeenCalledWith('session-7');
    });
});

// RC 3: the confirm moment. One observed-repetition proposal at the top of
// the page; review opens the draft; Confirm writes it down, Not a how-to
// suppresses that exact repetition, and both answers keep Undo for a few
// seconds.
describe('the confirm moment', () => {
    beforeEach(async () => {
        vi.resetAllMocks();
        pageQueryCache.clear();
        await i18n.changeLanguage('en');
        mocks.listKnowHow.mockResolvedValue(CARDS);
        mocks.getEntries.mockResolvedValue(ENTRIES);
        mocks.listSops.mockResolvedValue([]);
        mocks.getPlaybooks.mockResolvedValue({ plans: [], channels: null, defaultChannels: [] });
        mocks.getSopAuthor.mockResolvedValue(null);
        mocks.getMatch.mockResolvedValue(null);
        mocks.getCandidate.mockResolvedValue(CANDIDATE);
        mocks.confirmCandidate.mockResolvedValue({ knowledgeId: CANDIDATE.id });
        mocks.declineCandidate.mockResolvedValue(undefined);
        mocks.undoDeclineCandidate.mockResolvedValue(undefined);
        mocks.undoConfirmCandidate.mockResolvedValue(undefined);
        mocks.saveCandidate.mockImplementation(async () => CANDIDATE);
    });

    afterEach(() => {
        cleanup();
        vi.useRealTimers();
    });

    it('proposes above the match banner, with the title, dates and duration', async () => {
        // This assertion needs an occurrence on the same local day, including
        // when the suite runs in the first hour after midnight.
        vi.useFakeTimers({ toFake: ['Date'] });
        vi.setSystemTime(now);
        mocks.getCandidate.mockResolvedValue({ ...CANDIDATE, occurredAtMs: [...CANDIDATE.occurredAtMs.slice(0, -1), now] });
        mocks.getMatch.mockResolvedValue(MATCH);
        await renderPage({}, { devTools: true });
        const banner = screen.getByTestId('know-how-candidate');
        expect(within(banner).getByText('You’ve done this the same way 4 times. Make it a how-to?')).toBeInTheDocument();
        expect(
            within(banner).getByText(/Fresh-install QA before a release: the same 4 steps on .*, and today, about 40 min each\./),
        ).toBeInTheDocument();
        expect(within(banner).getByRole('button', { name: 'See the steps' })).toBeInTheDocument();
        expect(within(banner).getByRole('button', { name: 'Not how' })).toBeInTheDocument();
        // Above "You've hit this before" when both exist.
        const matchBanner = screen.getByTestId('know-how-match');
        expect(banner.compareDocumentPosition(matchBanner) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        // No proposal yet: nothing was written.
        expect(mocks.confirmCandidate).not.toHaveBeenCalled();
    });

    it('W16: no scope chip while there is one scope', async () => {
        await renderPage();
        expect(within(cardButton('Docker cleanup')).queryByText('Mine')).toBeNull();
        expect(within(screen.getByTestId('know-how-candidate')).queryByText('Mine')).toBeNull();
        await selectCard('Docker cleanup');
        const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
        expect(within(detail).queryByText('Mine')).toBeNull();
    });

    it('review opens the draft: label, scope, when to use it, the steps, and what gets written down', async () => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
        expect(within(detail).getByText('Draft how-to')).toBeInTheDocument();
        // W3, W16: no "Proposed today", no scope chip.
        expect(within(detail).queryByText('Mine')).toBeNull();
        expect(detail.textContent).not.toContain('Proposed today');
        expect(detail.textContent).not.toContain('Nothing leaves this computer');
        expect(within(detail).getByRole('heading', { level: 2, name: CANDIDATE.title })).toBeInTheDocument();
        expect(within(detail).getByText('When to use it')).toBeInTheDocument();
        expect(within(detail).getByText(CANDIDATE.trigger)).toBeInTheDocument();
        expect(within(detail).getAllByRole('listitem')).toHaveLength(4);
        expect(within(detail).getByText('Where it came from')).toBeInTheDocument();
        expect(within(detail).getByText('The same steps, in the same order, in 4 sessions.')).toBeInTheDocument();
        expect(within(detail).getByText(/Terminal, Isoform · about 40 min each/)).toBeInTheDocument();
        expect(within(detail).getByText('What gets written down')).toBeInTheDocument();
        expect(
            within(detail).getByText('The title, when to use it, and the 4 steps above, saved on this computer.'),
        ).toBeInTheDocument();
        expect(within(detail).getByText('What stays where it was')).toBeInTheDocument();
        expect(within(detail).getByRole('button', { name: 'That’s how' })).toBeInTheDocument();
        expect(within(detail).getByRole('button', { name: 'Edit steps' })).toBeInTheDocument();
        expect(within(detail).getByRole('button', { name: 'Not how' })).toBeInTheDocument();
        // W12: the raw session ids and source enum are developer tools only.
        expect(detail.textContent).not.toContain('source: observed');
        expect(within(detail).queryByRole('button', { name: 'Technical details' })).toBeNull();
        expect(detail.textContent).not.toContain(CANDIDATE.id);
    });

    it('confirm saves the how-to, says where it went, and lands the card at the top', async () => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        // The saved how-to comes back at the end of the list; it lands first.
        mocks.listKnowHow.mockResolvedValue([...CARDS, { ...CARDS[0], id: CANDIDATE.id, title: 'Friday invoices' }]);
        await act(async () => {
            fireEvent.click(screen.getAllByRole('button', { name: 'That’s how' }).at(-1)!);
        });
        expect(mocks.confirmCandidate).toHaveBeenCalledWith(CANDIDATE.id);
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        const result = screen.getByRole('status');
        // Answer feedback section 8: where it went, never "Scope: mine.".
        expect(result).toHaveTextContent('Saved in How you do things.');
        expect(result).not.toHaveTextContent('Scope');
        expect(within(result).getByRole('button', { name: 'Undo' })).toBeInTheDocument();
        // The grid reloaded so the new card can show.
        expect(mocks.listKnowHow).toHaveBeenCalledTimes(2);
        await waitFor(() => expect(document.querySelector('[data-know-how-id]')?.getAttribute('data-know-how-id')).toBe(CANDIDATE.id));
        const landed = document.querySelector(`[data-know-how-id="${CANDIDATE.id}"]`)!;
        expect(landed).toHaveAttribute('data-landed', 'true');
        expect(landed).toHaveClass('answer-wash');
    });

    it('undo after confirm restores the draft review', async () => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        await act(async () => {
            fireEvent.click(screen.getAllByRole('button', { name: 'That’s how' }).at(-1)!);
        });
        mocks.getCandidate.mockResolvedValue(CANDIDATE);
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
        });
        expect(mocks.undoConfirmCandidate).toHaveBeenCalledWith(CANDIDATE.id);
        expect(screen.getByText('Draft how-to')).toBeInTheDocument();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
    });

    it('not a how-to declines for good, says so, and Undo brings the proposal back', async () => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Not how' }));
        });
        expect(mocks.declineCandidate).toHaveBeenCalledWith(CANDIDATE.id);
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        const result = screen.getByRole('status');
        expect(result).toHaveTextContent('Not how. Isoform won’t offer this one again.');

        await act(async () => {
            fireEvent.click(within(result).getByRole('button', { name: 'Undo' }));
        });
        expect(mocks.undoDeclineCandidate).toHaveBeenCalledWith(CANDIDATE.id);
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
    });

    it('Undo stays until the person leaves the page, not on a timer', async () => {
        vi.useFakeTimers();
        try {
            await renderPage();
            await act(async () => {
                fireEvent.click(screen.getByRole('button', { name: 'Not how' }));
            });
            const result = screen.getByRole('status');
            expect(within(result).getByRole('button', { name: 'Undo' })).toBeInTheDocument();
            await act(async () => {
                vi.advanceTimersByTime(60_000);
            });
            expect(within(result).getByRole('button', { name: 'Undo' })).toBeInTheDocument();
            expect(result).toHaveTextContent('Not how. Isoform won’t offer this one again.');
        } finally {
            vi.useRealTimers();
        }
    });

    it('edit steps keeps the draft a draft', async () => {
        mocks.saveCandidate.mockImplementation(async (_id: string, title: string, trigger: string, steps: string[]) => ({
            ...CANDIDATE,
            title,
            trigger,
            steps,
        }));
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Edit steps' }));
        });
        const titleField = screen.getByRole('textbox', { name: 'Title' });
        expect(titleField).toHaveValue(CANDIDATE.title);
        const stepsField = screen.getByRole('textbox', { name: 'Steps, one per line' });
        fireEvent.change(stepsField, { target: { value: 'Remove the app.\nInstall the signed build.' } });
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        });
        expect(mocks.saveCandidate).toHaveBeenCalledWith(CANDIDATE.id, CANDIDATE.title, CANDIDATE.trigger, [
            'Remove the app.',
            'Install the signed build.',
        ]);
        // Still the draft, with the edited steps; nothing was confirmed.
        expect(screen.getByText('Draft how-to')).toBeInTheDocument();
        expect(screen.getAllByRole('listitem')).toHaveLength(2);
        expect(mocks.confirmCandidate).not.toHaveBeenCalled();
    });

    it('escape leaves the edit, then the review, before the dialog', async () => {
        const { escape } = await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Edit steps' }));
        });
        expect(await escape()).toBe(true);
        expect(screen.queryByRole('textbox', { name: 'Steps, one per line' })).toBeNull();
        expect(screen.getByText('Draft how-to')).toBeInTheDocument();
        expect(await escape()).toBe(true);
        expect(screen.queryByText('Draft how-to')).toBeNull();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        expect(await escape()).toBe(false);
    });

    it('a failed action says so and keeps the proposal', async () => {
        mocks.declineCandidate.mockRejectedValueOnce(new Error('disk full'));
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Not how' }));
        });
        expect(screen.getByRole('alert')).toHaveTextContent('Didn’t save.');
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        expect(screen.queryByText(/won’t propose this one again/)).toBeNull();
    });

    it('reads in Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        await renderPage();
        const banner = screen.getByTestId('know-how-candidate');
        expect(within(banner).getByText('这套同样的做法你已经做了 4 次。要记成做法吗？')).toBeInTheDocument();
        expect(within(banner).getByRole('button', { name: '就是这么做' })).toBeInTheDocument();
        expect(within(banner).getByRole('button', { name: '查看步骤' })).toBeInTheDocument();
        expect(within(banner).getByRole('button', { name: '不是这么做' })).toBeInTheDocument();
        await act(async () => {
            fireEvent.click(within(banner).getByRole('button', { name: '查看步骤' }));
        });
        const detail = screen.getByRole('complementary', { name: '经验详情' });
        expect(within(detail).getByText('做法草稿')).toBeInTheDocument();
        expect(within(detail).queryByText('我的')).toBeNull();
        expect(within(detail).getByText('标题、什么时候用它，以及上面的 4 个步骤，保存在这台电脑上。')).toBeInTheDocument();
        expect(within(detail).getByText('会记下什么')).toBeInTheDocument();
        expect(within(detail).getByRole('button', { name: '就是这么做' })).toBeInTheDocument();
        await act(async () => {
            fireEvent.click(within(detail).getByRole('button', { name: '就是这么做' }));
        });
        expect(screen.getByRole('status')).toHaveTextContent('已存进「你的方法」。');
    });
});

// Round 1 review guards (PR #219): the proposal projects observed sessions, so
// every privacy transition withdraws it from the page at once — banner, draft
// detail, edit fields and the Undo line — and supersedes Get/Save/Undo
// completions that land afterwards. Only a fresh allowed read may bring it back.
describe('candidate privacy transitions', () => {
    const PRIVACY_EVENTS: [string, unknown][] = [
        ['closed-eyes:changed', true],
        ['memory:updated', undefined],
        ['sensitive-apps:updated', undefined],
        ['model-accounts:updated', { accounts: [] }],
    ];

    // The shared identity-withdrawal fence (lib/privacy-reset) compares against
    // this baseline: two signed-in accounts.
    const ACCOUNT_A = { provider: 'claude', kind: 'subscription', name: 'Bo', on: true, windows: [{ used: 10 }] };
    const ACCOUNT_B = { provider: 'codex', kind: 'subscription', name: 'Bo Codex', on: true };

    const emit = async (name: string, payload?: unknown) => {
        await act(async () => {
            for (const handler of events.get(name) ?? []) handler(payload);
        });
    };

    beforeEach(async () => {
        vi.resetAllMocks();
        pageQueryCache.clear();
        await i18n.changeLanguage('en');
        mocks.listKnowHow.mockResolvedValue(CARDS);
        mocks.getEntries.mockResolvedValue(ENTRIES);
        mocks.listSops.mockResolvedValue([]);
        mocks.getPlaybooks.mockResolvedValue({ plans: [], channels: null, defaultChannels: [] });
        mocks.getSopAuthor.mockResolvedValue(null);
        mocks.getMatch.mockResolvedValue(null);
        mocks.getCandidate.mockResolvedValue(CANDIDATE);
        mocks.confirmCandidate.mockResolvedValue({ knowledgeId: CANDIDATE.id });
        mocks.declineCandidate.mockResolvedValue(undefined);
        mocks.undoDeclineCandidate.mockResolvedValue(undefined);
        mocks.undoConfirmCandidate.mockResolvedValue(undefined);
        mocks.saveCandidate.mockImplementation(async () => CANDIDATE);
        mocks.getAccountsStatus.mockResolvedValue({ accounts: [ACCOUNT_A, ACCOUNT_B] });
        // Re-seed the shared fence's signed-in baseline for this scenario. The
        // module-level subscription persists across tests, so the event
        // registry is never cleared here.
        resetRenderTierStores();
        await new Promise((resolve) => setTimeout(resolve, 0));
    });

    afterEach(() => cleanup());

    it.each(PRIVACY_EVENTS)('withdraws the open review on %s while re-reads are held', async (event, payload) => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        expect(screen.getByText(CANDIDATE.steps[0])).toBeInTheDocument();
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit(event, payload);
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        expect(screen.queryByText(CANDIDATE.steps[0])).toBeNull();
        expect(screen.queryByText('Draft how-to')).toBeNull();
    });

    it.each(PRIVACY_EVENTS)('withdraws the edit panel and its fields on %s', async (event, payload) => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Edit steps' }));
        });
        expect(screen.getByRole('textbox', { name: 'Title' })).toHaveValue(CANDIDATE.title);
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit(event, payload);
        expect(screen.queryByRole('textbox', { name: 'Title' })).toBeNull();
        expect(screen.queryByRole('textbox', { name: 'Steps, one per line' })).toBeNull();
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
    });

    it.each(PRIVACY_EVENTS)('withdraws the Undo line on %s', async (event, payload) => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Not how' }));
        });
        expect(screen.getByRole('status')).toHaveTextContent('Not how.');
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit(event, payload);
        // The page's own privacy reload shows a role="status" skeleton until
        // its card list re-reads, so wait for that reload to settle first.
        await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
        expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull();
    });

    it.each(PRIVACY_EVENTS)('a failed re-read after %s keeps the withdrawal', async (event, payload) => {
        await renderPage();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit(event, payload);
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        let fail!: (err: Error) => void;
        mocks.getCandidate.mockImplementation(() => new Promise((_, reject) => { fail = reject; }));
        await emit('knowhow:candidate-updated', { action: 'proposed', id: CANDIDATE.id });
        await act(async () => fail(new Error('store unavailable')));
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        expect(screen.queryByText(CANDIDATE.steps[0])).toBeNull();
    });

    it.each(PRIVACY_EVENTS)('a late Get result after %s cannot repopulate the proposal', async (event, payload) => {
        await renderPage();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        let finishLate!: (value: main.KnowHowCandidate) => void;
        mocks.getCandidate.mockImplementationOnce(() => new Promise((resolve) => { finishLate = resolve; }));
        await emit('knowhow:candidate-updated', { action: 'withdrawn', id: CANDIDATE.id });
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit(event, payload);
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        await act(async () => finishLate(CANDIDATE));
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        expect(screen.queryByText(CANDIDATE.steps[0])).toBeNull();
    });

    it.each(PRIVACY_EVENTS)('a late Save result after %s cannot repopulate the edit', async (event, payload) => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Edit steps' }));
        });
        let finishSave!: (value: main.KnowHowCandidate) => void;
        mocks.saveCandidate.mockImplementation(() => new Promise((resolve) => { finishSave = resolve; }));
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        });
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit(event, payload);
        await act(async () => finishSave({ ...CANDIDATE, title: 'Edited after the event' }));
        expect(screen.queryByRole('textbox', { name: 'Title' })).toBeNull();
        expect(screen.queryByText('Edited after the event')).toBeNull();
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
    });

    it.each(PRIVACY_EVENTS)('a late Undo result after %s cannot restore the proposal', async (event, payload) => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        await act(async () => {
            fireEvent.click(screen.getAllByRole('button', { name: 'That’s how' }).at(-1)!);
        });
        expect(screen.getByRole('status')).toHaveTextContent('Saved in How you do things.');
        let finishUndo!: () => void;
        mocks.undoConfirmCandidate.mockImplementation(() => new Promise<void>((resolve) => { finishUndo = resolve; }));
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
        });
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit(event, payload);
        // Wait for the page's privacy reload to settle (its role="status"
        // skeleton clears); the late Undo result is then delivered below.
        await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
        await act(async () => finishUndo());
        await waitFor(() => expect(screen.queryByRole('status')).toBeNull());
        expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull();
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        expect(screen.queryByText('Draft how-to')).toBeNull();
    });

    it.each(PRIVACY_EVENTS)('a late Confirm result after %s cannot restore the result or selection', async (event, payload) => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        let finishConfirm!: (value: { knowledgeId: string }) => void;
        mocks.confirmCandidate.mockImplementation(() => new Promise((resolve) => { finishConfirm = resolve; }));
        await act(async () => {
            fireEvent.click(screen.getAllByRole('button', { name: 'That’s how' }).at(-1)!);
        });
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit(event, payload);
        await act(async () => finishConfirm({ knowledgeId: CANDIDATE.id }));
        expect(screen.queryByRole('status')).toBeNull();
        expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull();
        expect(screen.queryByText('Draft how-to')).toBeNull();
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
    });

    it('keeps confirm and Undo through its own write and the subsequent index notification', async () => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        let finishConfirm!: (value: { knowledgeId: string }) => void;
        mocks.confirmCandidate.mockImplementation(() => new Promise((resolve) => { finishConfirm = resolve; }));
        await act(async () => {
            fireEvent.click(screen.getAllByRole('button', { name: 'That’s how' }).at(-1)!);
        });
        mocks.getCandidate.mockResolvedValue(null);
        await emit('memory:updated', { reason: 'knowhow-confirm' });
        await act(async () => finishConfirm({ knowledgeId: CANDIDATE.id }));
        expect(screen.getByRole('status')).toHaveTextContent('Saved in How you do things.');
        await emit('memory:updated', { reason: 'index' });
        expect(screen.getByRole('button', { name: 'Undo' })).toBeInTheDocument();
        // An actual Forget still retires that Undo window.
        await emit('memory:updated', { reason: 'forget' });
        expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull();
    });

    it('immediately withdraws a revoked draft while its replacement read is pending', async () => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit('knowhow:candidate-updated', { action: 'withdrawn', id: CANDIDATE.id });
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        expect(screen.queryByText(CANDIDATE.steps[0])).toBeNull();
    });

    it('Closed Eyes hides the proposal and resume restores it from a fresh allowed read', async () => {
        await renderPage();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit('closed-eyes:changed', true);
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        mocks.getCandidate.mockResolvedValue(CANDIDATE);
        await emit('closed-eyes:changed', false);
        expect(await screen.findByTestId('know-how-candidate')).toBeInTheDocument();
    });

    it('an ordinary refresh and a same-identity quota update keep the proposal', async () => {
        await renderPage();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        // The 60-second quota poll: same signed-in identities, new numbers.
        // The shared identity fence must not treat it as a withdrawal — the
        // proposal stays and no replacement read is even started.
        const readsBefore = mocks.getCandidate.mock.calls.length;
        await emit('model-accounts:updated', { accounts: [{ ...ACCOUNT_A, windows: [{ used: 95 }] }, ACCOUNT_B] });
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        expect(mocks.getCandidate.mock.calls.length).toBe(readsBefore);
        mocks.getCandidate.mockResolvedValue(CANDIDATE);
        await emit('knowhow:candidate-updated', { action: 'edited', id: CANDIDATE.id });
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        expect(screen.getByText(/Fresh-install QA before a release/)).toBeInTheDocument();
    });

    // Round 2 review guards (PR #219): sign-out is the shared identity
    // withdrawal fence, not a payload length check.
    it('withdraws the open review when every remaining account is switched off, with reads held', async () => {
        await renderPage();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'See the steps' }));
        });
        expect(screen.getByText(CANDIDATE.steps[0])).toBeInTheDocument();
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit('model-accounts:updated', { accounts: [{ ...ACCOUNT_A, on: false }, { ...ACCOUNT_B, on: false }] });
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        expect(screen.queryByText(CANDIDATE.steps[0])).toBeNull();
        expect(screen.queryByText('Draft how-to')).toBeNull();
    });

    it('withdraws when one of two signed-in accounts is removed, and a failed re-read keeps the withdrawal', async () => {
        await renderPage();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit('model-accounts:updated', { accounts: [ACCOUNT_A] });
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        let fail!: (err: Error) => void;
        mocks.getCandidate.mockImplementation(() => new Promise((_, reject) => { fail = reject; }));
        await emit('knowhow:candidate-updated', { action: 'proposed', id: CANDIDATE.id });
        await act(async () => fail(new Error('store unavailable')));
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
    });

    it('withdraws a hidden retained proposal and keeps it withdrawn on reopen while reads are held', async () => {
        let view!: ReturnType<typeof render>;
        await act(async () => {
            view = render(<KnowledgePage active={false} />);
        });
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit('model-accounts:updated', { accounts: [ACCOUNT_A] });
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        await act(async () => {
            view.rerender(<KnowledgePage active={true} />);
        });
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        expect(screen.queryByText(CANDIDATE.title)).toBeNull();
    });

    it('fails closed while the account baseline is unknown, even with an account on in the payload', async () => {
        mocks.getAccountsStatus.mockReturnValue(new Promise(() => {}));
        resetRenderTierStores();
        await renderPage();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit('model-accounts:updated', { accounts: [ACCOUNT_A, ACCOUNT_B] });
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
    });

    it('withdraws on an unreadable account payload', async () => {
        await renderPage();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit('model-accounts:updated', null);
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
    });

    it('withdraws a proposal that was never opened, and a fresh allowed read reopens it', async () => {
        await renderPage();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit('model-accounts:updated', { accounts: [] });
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        mocks.getCandidate.mockResolvedValue(CANDIDATE);
        await emit('knowhow:candidate-updated', { action: 'proposed', id: CANDIDATE.id });
        expect(await screen.findByTestId('know-how-candidate')).toBeInTheDocument();
    });

    it.each(['memory:updated', 'sensitive-apps:updated'])('a routine %s withdraws, then the still-allowed read restores the proposal', async (event) => {
        await renderPage();
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        mocks.getCandidate.mockResolvedValue(CANDIDATE);
        await emit(event);
        expect(await screen.findByTestId('know-how-candidate')).toBeInTheDocument();
    });

    it('a privacy event also withdraws the open entry edit', async () => {
        await renderPage();
        await selectCard('Docker cleanup');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
        });
        const editor = screen.getByRole('textbox', { name: 'Steps, one per line' });
        fireEvent.change(editor, { target: { value: 'unsaved owner words' } });
        mocks.getCandidate.mockImplementation(() => new Promise(() => {}));
        await emit('sensitive-apps:updated');
        expect(screen.queryByTestId('know-how-candidate')).toBeNull();
        expect(screen.queryByRole('textbox', { name: 'Steps, one per line' })).toBeNull();
        expect(screen.queryByDisplayValue('unsaved owner words')).toBeNull();
    });
});

describe('design review v1.0.0 (Thursday)', () => {
    const emit = async (name: string, payload?: unknown) => {
        await act(async () => {
            for (const handler of events.get(name) ?? []) handler(payload);
        });
    };
    const HOW_TO = card({
        id: 'k-howto',
        kind: 'how_to',
        title: 'Ship the weekly report',
        takeaway: 'Pull the numbers, write it up, send it.',
        trigger: 'Friday afternoon',
        points: ['Pull the numbers', 'Write the summary', 'Send it to the team'],
        savedAt: now - DAY,
    });

    beforeEach(async () => {
        vi.resetAllMocks();
        pageQueryCache.clear();
        await i18n.changeLanguage('en');
        chat.items = [];
        chat.lastUserItemId = '';
        chat.pending = null;
        mocks.listKnowHow.mockResolvedValue([...CARDS, HOW_TO]);
        mocks.getEntries.mockResolvedValue([...ENTRIES, entry('k-howto')]);
        mocks.listSops.mockResolvedValue([]);
        mocks.getPlaybooks.mockResolvedValue({ plans: [], channels: null, defaultChannels: [] });
        mocks.getSopAuthor.mockResolvedValue(null);
        mocks.getMatch.mockResolvedValue(null);
        mocks.getCandidate.mockResolvedValue(CANDIDATE);
        mocks.confirmCandidate.mockResolvedValue({ knowledgeId: CANDIDATE.id });
        mocks.declineCandidate.mockResolvedValue(undefined);
        mocks.undoConfirmCandidate.mockResolvedValue(undefined);
        mocks.listVersions.mockResolvedValue([]);
        mocks.generate.mockResolvedValue(undefined);
        mocks.generateRange.mockResolvedValue(undefined);
        chat.send.mockResolvedValue('sent');
    });

    afterEach(() => {
        cleanup();
    });

    it('W5: the banner says That’s how, See the steps, Not how; That’s how saves at once', async () => {
        await renderPage();
        const banner = screen.getByTestId('know-how-candidate');
        expect(within(banner).getAllByRole('button').map((b) => b.textContent)).toEqual(
            expect.arrayContaining(['That’s how', 'See the steps', 'Not how']),
        );
        await act(async () => {
            fireEvent.click(within(banner).getByRole('button', { name: 'That’s how' }));
        });
        expect(mocks.confirmCandidate).toHaveBeenCalledWith(CANDIDATE.id);
        expect(screen.getByRole('status')).toHaveTextContent('Saved in How you do things.');
        expect(mocks.toast).not.toHaveBeenCalled();
    });

    it('W5: Not how answers in place with the decided words', async () => {
        await renderPage();
        await act(async () => {
            fireEvent.click(within(screen.getByTestId('know-how-candidate')).getByRole('button', { name: 'Not how' }));
        });
        expect(mocks.declineCandidate).toHaveBeenCalledWith(CANDIDATE.id);
        expect(screen.getByRole('status')).toHaveTextContent('Not how. Isoform won’t offer this one again.');
    });

    it('W6: a failed save is copper and in place, and Try again saves', async () => {
        mocks.confirmCandidate.mockRejectedValueOnce(new Error('disk full'));
        await renderPage();
        await act(async () => {
            fireEvent.click(within(screen.getByTestId('know-how-candidate')).getByRole('button', { name: 'That’s how' }));
        });
        expect(screen.getByText('Didn’t save.')).toBeInTheDocument();
        expect(document.body.textContent).not.toContain('disk full');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        });
        expect(mocks.confirmCandidate).toHaveBeenCalledTimes(2);
        expect(screen.getByRole('status')).toHaveTextContent('Saved in How you do things.');
    });

    it('W6, W19: Undo that fails says so; a failed re-read after Undo says Couldn’t load this how-to, with Try again', async () => {
        await renderPage();
        await act(async () => {
            fireEvent.click(within(screen.getByTestId('know-how-candidate')).getByRole('button', { name: 'That’s how' }));
        });
        mocks.undoConfirmCandidate.mockRejectedValueOnce(new Error('locked'));
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
        });
        expect(screen.getByText('Didn’t undo.')).toBeInTheDocument();

        mocks.getCandidate.mockRejectedValueOnce(new Error('read failed'));
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        });
        expect(mocks.undoConfirmCandidate).toHaveBeenCalledTimes(2);
        expect(await screen.findByText('Couldn’t load this how-to.')).toBeInTheDocument();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        });
        expect(await screen.findByTestId('know-how-candidate')).toBeInTheDocument();
        expect(screen.queryByText('Couldn’t load this how-to.')).toBeNull();
    });

    it('W19: a failed re-read keeps the proposal on screen', async () => {
        await renderPage();
        mocks.getCandidate.mockRejectedValue(new Error('read failed'));
        await emit('knowhow:candidate-updated');
        expect(screen.getByTestId('know-how-candidate')).toBeInTheDocument();
        expect(screen.getByText(CANDIDATE.title, { exact: false })).toBeInTheDocument();
    });

    it('W20: a failed re-read keeps the match banner', async () => {
        mocks.getCandidate.mockResolvedValue(null);
        mocks.getMatch.mockResolvedValue(MATCH);
        await renderPage();
        expect(screen.getByTestId('know-how-match')).toBeInTheDocument();
        mocks.getMatch.mockRejectedValue(new Error('read failed'));
        await emit('knowledge:updated');
        await waitFor(() => expect(mocks.getMatch.mock.calls.length).toBeGreaterThanOrEqual(2), { timeout: 3000 });
        expect(screen.getByTestId('know-how-match')).toBeInTheDocument();
    });

    it('W17: the saved line and its Undo clear when you leave the page', async () => {
        const escapeRef = { current: null as (() => boolean) | null };
        let view!: ReturnType<typeof render>;
        await act(async () => {
            view = render(<KnowledgePage escapeRef={escapeRef} active />);
        });
        await act(async () => {
            fireEvent.click(within(screen.getByTestId('know-how-candidate')).getByRole('button', { name: 'That’s how' }));
        });
        expect(screen.getByText('Saved in How you do things.')).toBeInTheDocument();
        await act(async () => {
            view.rerender(<KnowledgePage escapeRef={escapeRef} active={false} />);
        });
        await act(async () => {
            view.rerender(<KnowledgePage escapeRef={escapeRef} active />);
        });
        expect(screen.queryByText('Saved in How you do things.')).toBeNull();
        expect(screen.queryByRole('button', { name: 'Undo' })).toBeNull();
    });

    it('W7: Ask about this asks in place under the detail and never opens Chat', async () => {
        mocks.getCandidate.mockResolvedValue(null);
        await renderPage();
        await selectCard('Docker cleanup');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Ask about this' }));
        });
        const detail = screen.getByRole('complementary', { name: 'Know-how detail' });
        const field = within(detail).getByRole('textbox', { name: 'Ask anything about this' });
        fireEvent.change(field, { target: { value: 'Does this work on Linux?' } });
        await act(async () => {
            fireEvent.keyDown(field, { key: 'Enter' });
        });
        expect(chat.send).toHaveBeenCalledWith('Does this work on Linux?', { sourceKind: 'knowledge', sourceId: 'k-fix' }, true);
    });

    it('W8: Turn into a playbook drafts in place, through the author, and says where it shows', async () => {
        mocks.getCandidate.mockResolvedValue(null);
        mocks.generate.mockRejectedValueOnce(new Error('no model'));
        await renderPage();
        await selectCard('Ship the weekly report');
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Turn into a playbook' }));
        });
        expect(mocks.generate).toHaveBeenCalledWith(expect.stringContaining('Ship the weekly report'));
        expect(screen.getByText('Didn’t start the playbook.')).toBeInTheDocument();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        });
        expect(mocks.generate).toHaveBeenCalledTimes(2);
        expect(screen.getByText('Drafting a playbook. It shows in Tasks, under Up next.')).toBeInTheDocument();
        expect(chat.send).not.toHaveBeenCalled();
    });

    it('W9: copy and delete failures are copper in place with Try again; nothing toasts', async () => {
        mocks.getCandidate.mockResolvedValue(null);
        const writeText = vi.fn().mockRejectedValueOnce(new Error('denied')).mockResolvedValue(undefined);
        Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
        await renderPage();
        await selectCard('Docker cleanup');
        await openMenu(`More for ${CARDS[0].title}`);
        await chooseMenuItem('Copy markdown');
        expect(await screen.findByText('Didn’t copy.')).toBeInTheDocument();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        });
        expect(writeText).toHaveBeenCalledTimes(2);
        await waitFor(() => expect(screen.queryByText('Didn’t copy.')).toBeNull());

        mocks.remove.mockRejectedValueOnce(new Error('locked'));
        await openMenu(`More for ${CARDS[0].title}`);
        await chooseMenuItem('Delete');
        const dialog = await screen.findByRole('dialog');
        await act(async () => {
            fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
        });
        expect(await screen.findByText('Didn’t delete.')).toBeInTheDocument();
        expect(mocks.toast).not.toHaveBeenCalled();
    });

    it('offers no playbook actions while playbooks are off', async () => {
        (globalThis as Record<string, unknown>).__isoformFeatureSwitchesForTest = { automations: true, playbooks: false, proactiveVoice: true, calendarHolds: true };
        mocks.getCandidate.mockResolvedValue(null);
        await renderPage();
        expect(screen.queryByRole('button', { name: 'Generate from recent work' })).toBeNull();
    });

    it('W1: Generate says it drafts into Tasks › Up next; a failure has Try again', async () => {
        mocks.getCandidate.mockResolvedValue(null);
        mocks.generateRange.mockRejectedValueOnce(new Error('busy'));
        await renderPage();
        await openMenu('Generate from recent work');
        await chooseMenuItem('Last 3 days');
        expect(await screen.findByText('Didn’t start the playbook.')).toBeInTheDocument();
        await act(async () => {
            fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
        });
        expect(mocks.generateRange).toHaveBeenCalledTimes(2);
        expect(await screen.findByText('Drafting a playbook from your recent work. It shows in Tasks, under Up next.')).toBeInTheDocument();
        expect(mocks.toast).not.toHaveBeenCalled();
    });

    it('W1: the Generate line reads in Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        mocks.getCandidate.mockResolvedValue(null);
        await renderPage();
        await openMenu('从最近的工作生成');
        await chooseMenuItem('最近 3 天');
        expect(await screen.findByText('正在根据你最近的工作起草操作手册。起草好后会出现在「任务」的「接下来」里。')).toBeInTheDocument();
    });

    const authorStatus = async (status: Record<string, unknown>) => {
        await act(async () => {
            for (const handler of events.get('sop:author-status') ?? []) handler(status);
        });
    };

    it('W01: a draft says where it goes under the actions, then that it is in Tasks › Up next', async () => {
        mocks.getCandidate.mockResolvedValue(null);
        mocks.getSopAuthor.mockResolvedValue(null);
        mocks.generateRange.mockResolvedValue(undefined);
        await renderPage();
        await openMenu('Generate from recent work');
        await chooseMenuItem('Last 3 days');
        const drafting = await screen.findByText('Drafting a playbook from your recent work. It shows in Tasks, under Up next.');
        expect(drafting).toHaveAttribute('role', 'status');
        expect(screen.queryByText('Drafting a playbook…')).toBeNull();
        await authorStatus({ running: true, updatedAt: Date.now(), lastEvent: 'agent_log' });
        expect(screen.getByText('Drafting a playbook from your recent work. It shows in Tasks, under Up next.')).toBeInTheDocument();
        await authorStatus({ running: false, updatedAt: Date.now(), lastEvent: 'saved' });
        expect(screen.getByText('Drafted. It’s in Tasks, under Up next.')).toBeInTheDocument();
        expect(screen.queryByText(/Drafting a playbook/)).toBeNull();
        expect(mocks.toast).not.toHaveBeenCalled();
    });

    it('W01: a draft that matches a playbook you have says nothing new was made', async () => {
        mocks.getCandidate.mockResolvedValue(null);
        mocks.getSopAuthor.mockResolvedValue({ running: true, updatedAt: Date.now(), lastEvent: 'started' });
        await renderPage();
        expect(await screen.findByText('Drafting a playbook from your recent work. It shows in Tasks, under Up next.')).toBeInTheDocument();
        await authorStatus({ running: false, updatedAt: Date.now(), lastEvent: 'skipped' });
        expect(screen.getByText('This matches a playbook you already have, so nothing new was made.')).toBeInTheDocument();
    });

    it('W01: a draft that fails is copper with a working Try again', async () => {
        mocks.getCandidate.mockResolvedValue(null);
        mocks.getSopAuthor.mockResolvedValue(null);
        mocks.generateRange.mockResolvedValue(undefined);
        await renderPage();
        await openMenu('Generate from recent work');
        await chooseMenuItem('Last 7 days');
        await authorStatus({ running: true, updatedAt: Date.now(), lastEvent: 'agent_log' });
        await authorStatus({ running: false, updatedAt: Date.now(), lastEvent: 'failed', lastError: 'runner exploded' });
        const alert = screen.getByRole('alert');
        expect(alert).toHaveTextContent('Didn’t draft the playbook.');
        expect(alert.className).toContain('text-copper');
        expect(screen.queryByText(/runner exploded/)).toBeNull();
        await act(async () => {
            fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
        });
        expect(mocks.generateRange).toHaveBeenCalledTimes(2);
        expect(screen.getByText('Drafting a playbook from your recent work. It shows in Tasks, under Up next.')).toBeInTheDocument();
    });

    it('W01: the drafted and matched lines read in Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        mocks.getCandidate.mockResolvedValue(null);
        mocks.getSopAuthor.mockResolvedValue({ running: true, updatedAt: Date.now(), lastEvent: 'started' });
        await renderPage();
        await screen.findByText('正在根据你最近的工作起草操作手册。起草好后会出现在「任务」的「接下来」里。');
        await authorStatus({ running: false, updatedAt: Date.now(), lastEvent: 'saved' });
        expect(screen.getByText('起草好了，在「任务」的「接下来」里。')).toBeInTheDocument();
        await authorStatus({ running: true, updatedAt: Date.now(), lastEvent: 'started' });
        await authorStatus({ running: false, updatedAt: Date.now(), lastEvent: 'skipped' });
        expect(screen.getByText('这和你已有的一个操作手册相同，所以没有新建。')).toBeInTheDocument();
    });

    it('W18: a drafting playbook shows a plain line; the agent log is developer tools only', async () => {
        mocks.getCandidate.mockResolvedValue(null);
        mocks.getSopAuthor.mockResolvedValue({ running: true });
        await renderPage();
        expect(await screen.findByText('Drafting a playbook from your recent work. It shows in Tasks, under Up next.')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: /Drafting a playbook/ })).toBeNull();
        cleanup();
        await renderPage({}, { devTools: true });
        expect(await screen.findByRole('button', { name: /Drafting a playbook/ })).toBeInTheDocument();
    });

    it('W12: Technical details are developer tools only', async () => {
        mocks.getCandidate.mockResolvedValue(null);
        await renderPage();
        await selectCard('Docker cleanup');
        expect(screen.queryByRole('button', { name: 'Technical details' })).toBeNull();
        expect(document.body.textContent).not.toContain('k-fix');
        cleanup();
        await renderPage({}, { devTools: true });
        await selectCard('Docker cleanup');
        expect(screen.getByRole('button', { name: 'Technical details' })).toBeInTheDocument();
    });
});

describe('How you do things shows one card per idea (design review v1.0.2 W40, Y33)', () => {
    const TITLE = 'Docker cleanup needs prune first';
    const copies = [1, 2, 3, 4, 5].map((n) => card({
        id: `dup-${n}`,
        kind: 'fix',
        title: n % 2 ? TITLE : 'docker cleanup needs prune first.',
        savedAt: now - n * DAY,
        moments: [{ at: now - n * DAY, assistant: '', app: `App ${n}`, sessionId: `s-${n}` }] as main.KnowHowMoment[],
    }));
    const other = card({ id: 'other', kind: 'decision', title: 'Ship the brief before nine', savedAt: now - DAY });

    beforeEach(async () => {
        vi.resetAllMocks();
        pageQueryCache.clear();
        await i18n.changeLanguage('en');
        mocks.listKnowHow.mockResolvedValue([...copies, other]);
        mocks.getEntries.mockResolvedValue(ENTRIES);
        mocks.listSops.mockResolvedValue([]);
        mocks.getPlaybooks.mockResolvedValue({ plans: [], channels: null, defaultChannels: [] });
        mocks.getSopAuthor.mockResolvedValue(null);
        mocks.getMatch.mockResolvedValue(null);
        mocks.getCandidate.mockResolvedValue(null);
        mocks.dismiss.mockResolvedValue(undefined);
        mocks.remove.mockResolvedValue(undefined);
        mocks.listVersions.mockResolvedValue([]);
    });

    afterEach(() => {
        cleanup();
    });

    it('shows five copies of one lesson as one card that says Seen 5 times, and counts ideas', async () => {
        await renderPage();
        expect(document.querySelectorAll('[data-know-how-id]')).toHaveLength(2);
        expect(within(cardButton('needs prune first')).getByText('Seen 5 times')).toBeInTheDocument();
        expect(screen.getByText(/2 pieces of know-how from your work/)).toBeInTheDocument();
    });

    it('lists the sources of every copy in the opened card', async () => {
        await renderPage();
        await selectCard('needs prune first');
        const moments = screen.getByRole('list', { name: 'Where it came from' });
        const items = within(moments).getAllByRole('listitem');
        expect(items).toHaveLength(5);
        for (const n of [1, 2, 3, 4, 5]) expect(within(moments).getByText(new RegExp(`App ${n}`))).toBeInTheDocument();
    });

    it('deletes every copy when the card is deleted', async () => {
        await renderPage();
        await selectCard('needs prune first');
        await openMenu(`More for ${TITLE}`);
        await chooseMenuItem('Delete');
        const dialog = await screen.findByRole('dialog');
        await act(async () => {
            fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
        });
        expect(mocks.remove.mock.calls.map(([id]) => id).sort()).toEqual(['dup-1', 'dup-2', 'dup-3', 'dup-4', 'dup-5']);
    });
});
