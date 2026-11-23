import { useRef, useState } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import en from '@/i18n/locales/en/memory.json';
import zh from '@/i18n/locales/zh-CN/memory.json';
import type { GoalItemView, GoalsPageView, MemoryForgetPreview } from '@/lib/wails-shim-app';
import { resetPageQueries } from '@/lib/page-queries';
import { pageQueryCache } from '@/lib/query-cache';
import { YouPage } from '@/components/you/you-page';
import { MemoryPage } from './memory-page';
import { Dialog, DialogTitle } from '@/components/ui/dialog';
import { RetainedDialogContent } from '@/components/retained-dialog-content';

const mocks = vi.hoisted(() => ({
    getEntries: vi.fn(),
    getStatus: vi.fn(),
    getEvidence: vi.fn(),
    listVersions: vi.fn(),
    search: vi.fn(),
    saveEntry: vi.fn(),
    rewordEntry: vi.fn(),
    getPreview: vi.fn(),
    forgetEntry: vi.fn(),
    rejectWithPreview: vi.fn(),
    notify: vi.fn(),
    getAbout: vi.fn(),
    getPortrait: vi.fn(),
    confirm: vi.fn(),
    undoConfirm: vi.fn(),
    notQuite: vi.fn(),
    undoNotQuite: vi.fn(),
    undoWording: vi.fn(),
    getUses: vi.fn(),
    getExamples: vi.fn(),
    getVersion: vi.fn(),
    sync: vi.fn(),
    listGoals: vi.fn(),
    getGoal: vi.fn(),
    moveGoal: vi.fn(),
    createGoal: vi.fn(),
    translateAll: vi.fn(),
    notMine: vi.fn(),
    getWhy: vi.fn(),
    notThis: vi.fn(),
    confirmPortrait: vi.fn(),
    notQuitePortrait: vi.fn(),
    undoConfirmPortrait: vi.fn(),
    events: new Map<string, Set<(value: unknown) => void>>(),
}));

// The tab's learning line has its own test (you-learning-line.test.tsx).
vi.mock('@/components/you/you-learning-line', () => ({ YouLearningLine: () => null }));
vi.mock('@/lib/wails-shim-app', () => ({
    GetMemoryEntries: mocks.getEntries,
    GetMemoryStatus: mocks.getStatus,
    SaveMemoryEntry: mocks.saveEntry,
    RewordMemoryEntry: mocks.rewordEntry,
    GetMemoryForgetPreview: mocks.getPreview,
    ForgetMemoryEntry: mocks.forgetEntry,
    SearchMemory: mocks.search,
    SyncMemoryIndex: mocks.sync,
    ListMemoryVersions: mocks.listVersions,
    GetMemoryEvidenceReadable: mocks.getEvidence,
    GetEntryUses: mocks.getUses,
    GetCommunicationDraftExamples: mocks.getExamples,
    GetAboutYou: mocks.getAbout,
    GetAboutYouPortrait: mocks.getPortrait,
    ConfirmMemoryEntry: mocks.confirm,
    UndoConfirmMemoryEntry: mocks.undoConfirm,
    NotQuiteMemoryEntry: mocks.notQuite,
    UndoNotQuiteMemoryEntry: mocks.undoNotQuite,
    UndoMemoryWording: mocks.undoWording,
    NotMineMemoryEntry: mocks.notMine,
    GetWhy: mocks.getWhy,
    NotThis: mocks.notThis,
    ConfirmAboutYouPortrait: mocks.confirmPortrait,
    NotQuiteAboutYouPortrait: mocks.notQuitePortrait,
    UndoConfirmAboutYouPortrait: mocks.undoConfirmPortrait,
    GetMemoryVersion: mocks.getVersion,
    TranslateMemoryEntryToCurrentLanguage: vi.fn(),
    TranslateMemoryVersionToCurrentLanguage: vi.fn(),
    StartTranslateAllCurrentMemoryEntries: (...args: unknown[]) => mocks.translateAll(...args),
    ReassignMemoryAttribution: vi.fn(),
    RejectMemoryAttributionWithPreview: mocks.rejectWithPreview,
    // The Goals group reads these; with no answer staged, the list is empty.
    ListGoals: (...args: unknown[]) => mocks.listGoals(...args) ?? Promise.resolve({ tracking: [], done: [], held: [] }),
    GetGoal: (...args: unknown[]) => mocks.getGoal(...args),
    MoveGoal: (...args: unknown[]) => mocks.moveGoal(...args),
    CreateGoal: (...args: unknown[]) => mocks.createGoal(...args),
    IdeaMeaningGroups: () => Promise.resolve(null),
}));
vi.mock('../../wailsjs/runtime/runtime', () => ({
    EventsOn: (name: string, callback: (value: unknown) => void) => {
        const listeners = mocks.events.get(name) ?? new Set<(value: unknown) => void>();
        listeners.add(callback);
        mocks.events.set(name, listeners);
        return () => { listeners.delete(callback); };
    },
}));

async function emitEvent(name: string, payload?: unknown) {
    await act(async () => {
        for (const handler of [...(mocks.events.get(name) ?? [])]) await handler(payload);
    });
}
vi.mock('@/lib/notify', () => ({ notifyToast: mocks.notify }));
vi.mock('@/components/ai-memory-import-dialog', () => ({ AIMemoryImportDialog: ({ open }: { open: boolean }) => (open ? <div>Import dialog open</div> : null) }));
vi.mock('@/components/memory-dream-dialog', () => ({ MemoryDreamDialog: ({ open }: { open: boolean }) => (open ? <div>Dream dialog open</div> : null) }));
vi.mock('./contacts/principal-picker', () => ({ PrincipalPicker: () => null }));
vi.mock('@/components/entry-content', async (importOriginal) => {
    const actual = await importOriginal<typeof import('@/components/entry-content')>();
    return {
        ...actual,
        EntryDetail: ({ content }: { content: string }) => <pre>{actual.splitHeaderAndBody(content).body}</pre>,
        EntryMetadataList: () => null,
    };
});

// The page reads through the shared query cache now; each test starts cold.
beforeEach(() => {
    resetPageQueries();
});

const entry = {
    id: 'writing-style',
    title: 'Writing style',
    content: '# Writing style\n- **Dimension**: user_insight\n- **Source**: activity\n\nYou prefer detailed replies.\n',
    dimension: 'user_insight',
    source: 'activity',
    modTime: Date.now(),
    updatedAt: Date.now(),
    sizeBytes: 120,
    hasHistory: false,
    activitySessionIds: [],
    apps: [],
};

function preview(overrides: Partial<MemoryForgetPreview> = {}): MemoryForgetPreview {
    return {
        memoryId: entry.id,
        title: entry.title,
        items: [
            { kind: 'memory', id: entry.id, title: entry.title },
            { kind: 'memory_archive', id: 'archive-1', title: 'Earlier writing style' },
            { kind: 'self_dream', id: 'self-1', title: 'Detailed communication preference' },
            { kind: 'purpose_map', id: 'purpose-1', title: 'Prepare a detailed weekly report' },
            { kind: 'work_profile', id: 'profile-1', title: 'Writing habits snapshot' },
        ],
        embeddingCount: 3,
        historyCount: 2,
        retentionDays: 30,
        confirmationToken: 'reviewed-scope-v1',
        ...overrides,
    };
}

type Entry = Omit<typeof entry, 'apps' | 'activitySessionIds'> & { apps: string[]; activitySessionIds: string[] };

/** The About you read for these entries; nothing new to check by default. */
function aboutFor(entries: Entry[], overrides: { newThisWeek?: string[]; status?: 'confirmed' | 'learned_today' | 'inferred' | 'not_quite'; portraitCandidate?: boolean; confirmedAt?: number; notQuiteAt?: number; confidence?: number; firstLearnedAt?: number; area?: string; sentMessageCount?: number; evidenceFirstAt?: number; evidenceLastAt?: number } = {}) {
    return {
        beliefs: entries.map((row) => ({
            id: row.id,
            title: row.title,
            area: overrides.area,
            status: overrides.status ?? 'inferred',
            confidence: overrides.confidence ?? 2,
            firstLearnedAt: overrides.firstLearnedAt ?? 0,
            updatedAt: row.updatedAt,
            sessionCount: 2,
            dayCount: 1,
            confirmedAt: overrides.confirmedAt ?? 0,
            notQuiteAt: overrides.notQuiteAt ?? 0,
            portraitCandidate: overrides.portraitCandidate ?? false,
            sentMessageCount: overrides.sentMessageCount,
            evidenceFirstAt: overrides.evidenceFirstAt,
            evidenceLastAt: overrides.evidenceLastAt,
        })),
        newThisWeek: overrides.newThisWeek ?? [],
        totals: { beliefs: entries.length, confirmed: overrides.status === 'confirmed' ? entries.length : 0 },
        storage: { files: entries.length, bytes: 120 * entries.length, chunks: 3, vectorReady: true },
    };
}

function learningPortrait() {
    return { state: 'learning', generatedAt: 0, beliefCount: 0, writing: false, rewriting: false, forgotten: false, assistant: '', progress: { beliefs: 2, beliefsNeeded: 20, days: 1, daysNeeded: 5, groups: 1, groupsNeeded: 3, moreDays: 4, met: false } };
}

function readyPortrait(parts: { text: string; memoryIds?: string[]; confirmed?: boolean }[]) {
    return {
        state: 'ready', name: ['blueprint', 'builder'], nameAreas: [['work'], ['communication']], sides: [0.2, 0.3, 0.2, 0.3],
        sentences: [{ role: 'connect', changed: false, parts: parts.map((part) => ({ memoryIds: [], confirmed: false, ...part })) }],
        workWithMe: [], beliefCount: 2, generatedAt: Date.now(), writing: false, rewriting: false, forgotten: false, assistant: 'claude-code',
    };
}

function resetDefaults(entries: Entry[]) {
    mocks.getEntries.mockResolvedValue(entries);
    mocks.getAbout.mockImplementation(async () => aboutFor(entries));
    mocks.getPortrait.mockResolvedValue(learningPortrait());
    mocks.getUses.mockResolvedValue({ total: 0, lastAt: 0, surfaces: [] });
    mocks.getExamples.mockResolvedValue([]);
    mocks.getEvidence.mockResolvedValue([]);
    mocks.listVersions.mockResolvedValue([]);
    mocks.search.mockResolvedValue([]);
    mocks.getStatus.mockResolvedValue({ files: entries.length, chunks: 3, vectorReady: true });
}

async function openPageMenu(name = 'More About you actions') {
    fireEvent.keyDown(await screen.findByRole('button', { name }), { key: 'Enter' });
}

async function openDetailMenu(name = 'More memory actions') {
    fireEvent.keyDown(screen.getByRole('button', { name }), { key: 'Enter' });
}

async function renderSelected() {
    render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
    await screen.findByRole('heading', { name: entry.title });
}

async function openForget() {
    fireEvent.click(screen.getByRole('button', { name: 'Forget this' }));
    const dialog = screen.getByRole('dialog', { name: 'Forget this?' });
    await within(dialog).findByText('Detailed communication preference');
    return dialog;
}

describe('MemoryPage editing and forgetting', () => {
    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([entry]);
        mocks.saveEntry.mockImplementation(async (id: string, content: string) => ({ ...entry, id, content, source: 'user_edit' }));
        mocks.getPreview.mockResolvedValue(preview());
        mocks.forgetEntry.mockResolvedValue(undefined);
        mocks.rejectWithPreview.mockResolvedValue(undefined);
    });

    it.each([
        { records: 1, embeddings: 0, versions: 0, expected: 'Removed 1 record, 0 search embeddings and 0 saved versions.' },
        { records: 1, embeddings: 1, versions: 1, expected: 'Removed 1 record, 1 search embedding and 1 saved version.' },
        { records: 2, embeddings: 2, versions: 2, expected: 'Removed 2 records, 2 search embeddings and 2 saved versions.' },
    ])('pluralizes forgotten counts: $expected', async ({ records, embeddings, versions, expected }) => {
        mocks.getPreview.mockResolvedValue(preview({
            items: Array.from({ length: records }, (_, index) => ({ kind: 'memory' as const, id: `memory-${index}`, title: `Memory ${index}` })),
            embeddingCount: embeddings,
            historyCount: versions,
        }));
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: 'Forget this' }));
        const dialog = screen.getByRole('dialog');
        await within(dialog).findByText('Memory 0');
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget this' }));
        await waitFor(() => expect(mocks.forgetEntry).toHaveBeenCalled());
        // About you v1.0.0 Y6: no toast; the belief leaving is the answer.
        expect(mocks.notify).not.toHaveBeenCalled();
        void expected;
    });

    it.each([
        ['en', 'User edited'],
        ['zh-CN', '用户编辑'],
    ])('localizes the user-edit source in Technical details, never in the belief list, in %s', async (language, expected) => {
        await i18n.changeLanguage(language);
        mocks.getEntries.mockResolvedValue([{ ...entry, source: 'user_edit' }]);
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        const detail = await screen.findByRole('complementary', { name: language === 'en' ? 'Belief detail' : '认识详情' });
        expect(screen.queryByText(expected)).not.toBeInTheDocument();
        fireEvent.click(within(detail).getByRole('button', { name: language === 'en' ? 'Technical details' : '技术细节' }));
        expect(within(detail).getByText(expected)).toBeInTheDocument();
        expect(screen.queryByText('User_edit')).not.toBeInTheDocument();
    });

    it('removes obsolete delete-dialog copy from both memory locales', () => {
        for (const locale of [en, zh]) {
            expect(locale.sidebar).not.toHaveProperty('deleteFile');
            expect(locale).not.toHaveProperty('deleteDialog');
        }
    });

    it.each([
        ['en', 'Forget this', 'Analysis'],
        ['zh-CN', '忘记这条', '分析记录'],
    ])('uses a readable localized label for an untitled derived record in %s', async (language, action, label) => {
        await i18n.changeLanguage(language);
        mocks.getPreview.mockResolvedValue(preview({ items: [{ kind: 'self_dream', id: 'opaque-derived-record-id', title: '', recordType: 'analysis' }] }));
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: action }));
        const dialog = screen.getByRole('dialog');
        expect(await within(dialog).findByText(label)).toBeInTheDocument();
        expect(within(dialog).queryByText('opaque-derived-record-id')).not.toBeInTheDocument();
    });

    it.each([
        ['en', 'More memory actions', 'Wrong subject', 'Wrong subject?', 'This is actually about another person, not me.', 'Review removal', 'Correct this attribution?', 'Confirm correction', 'Attribution corrected'],
        ['zh-CN', '更多记忆操作', '搞错主体了', '搞错主体了？', '这条其实是另一个人的事,不是我的。', '查看移除范围', '更正这条记忆的归属？', '确认更正', '已更正记忆归属'],
    ])('uses correction-specific preview and confirmation after rejecting attribution in %s', async (language, menu, rejectAction, rejectTitle, reason, reviewAction, correctionTitle, confirmAction, successTitle) => {
        await i18n.changeLanguage(language);
        await renderSelected();
        // The row's attribution button went with the card grid; Wrong subject lives in the detail's More menu.
        await openDetailMenu(menu);
        fireEvent.click(await screen.findByRole('menuitem', { name: rejectAction }));
        const rejectDialog = screen.getByRole('dialog', { name: rejectTitle });
        fireEvent.click(within(rejectDialog).getByRole('radio', { name: reason }));
        fireEvent.click(within(rejectDialog).getByRole('button', { name: reviewAction }));
        const correctionDialog = await screen.findByRole('dialog', { name: correctionTitle });
        await within(correctionDialog).findByText('Detailed communication preference');
        expect(within(correctionDialog).getByText('Prepare a detailed weekly report')).toBeInTheDocument();
        expect(mocks.rejectWithPreview).not.toHaveBeenCalled();
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
        fireEvent.click(within(correctionDialog).getByRole('button', { name: confirmAction }));
        await waitFor(() => expect(mocks.rejectWithPreview).toHaveBeenCalledWith(entry.id, reason, 'reviewed-scope-v1'));
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
        expect(mocks.notify).not.toHaveBeenCalled();
        void successTitle;
        await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    });

    it.each([
        ['en', 'Forget this', 'Forgetting works offline using local fact hashes. Optional AI matching can also recognize reworded facts when available.'],
        ['zh-CN', '忘记这条', '忘记操作可离线完成，使用本地事实哈希进行匹配。AI 匹配可用时，还能帮助识别换一种说法的事实。'],
    ])('explains offline forgetting and optional semantic matching in %s', async (language, action, explanation) => {
        await i18n.changeLanguage(language);
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: action }));
        const dialog = screen.getByRole('dialog');
        await within(dialog).findByText('Detailed communication preference');
        expect(within(dialog).getByText(explanation)).toBeInTheDocument();
    });

    it.each([
        ['en', 'Forget this', 'Shared learning analyses and their supporting records are removed when listed. Memory files are removed only when listed as memory records.'],
        ['zh-CN', '忘记这条', '列表中的共享学习分析及其支持记录会被移除。记忆文件仅在列为记忆记录时才会被移除。'],
    ])('explains the shared-learning removal scope in %s', async (language, action, explanation) => {
        await i18n.changeLanguage(language);
        mocks.getPreview.mockResolvedValue(preview({ items: [{ kind: 'shadow_learning', id: 'learning-run-1', title: 'Writing preference analysis', recordType: 'analysis' }] }));
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: action }));
        const dialog = screen.getByRole('dialog');
        await within(dialog).findByText('Writing preference analysis');
        expect(within(dialog).getByText(explanation)).toBeInTheDocument();
    });

    it.each([
        ['en', 'Forget this', 'Saved memory version'],
        ['zh-CN', '忘记这条', '已保存的记忆版本'],
    ])('labels an independently removed memory history version in %s', async (language, action, label) => {
        await i18n.changeLanguage(language);
        mocks.getPreview.mockResolvedValue(preview({ items: [{ kind: 'memory_history', id: 'writing-version-1', title: 'Previous writing preference' }] }));
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: action }));
        const dialog = screen.getByRole('dialog');
        await within(dialog).findByText('Previous writing preference');
        expect(within(dialog).getByText(label)).toBeInTheDocument();
    });

    it('edits the full plain text within its fixed area group and saves it', async () => {
        await renderSelected();
        expect(screen.getByRole('button', { name: /^Other\s*1$/ })).toBeInTheDocument();
        // No kind labels on claim lines any more (the detail keeps its own).
        expect(within(screen.getByTestId('belief-groups')).queryByText('A preference')).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: 'Edit wording' }));
        const editor = screen.getByRole('textbox', { name: 'Memory point' });
        expect(editor).toHaveValue('You prefer detailed replies.');
        expect(screen.getByText('Your changes are saved as user edits and take priority over observed memories.')).toBeInTheDocument();
        const edited = entry.content.replace('detailed', 'concise');
        fireEvent.change(editor, { target: { value: 'You prefer concise replies.' } });
        mocks.getEntries.mockResolvedValue([{ ...entry, content: edited, source: 'user_edit' }]);
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        await waitFor(() => expect(mocks.saveEntry).toHaveBeenCalledWith(entry.id, edited));
        await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Memory point' })).not.toBeInTheDocument());
        const detail = screen.getByRole('complementary', { name: 'Belief detail' });
        expect(within(detail).getByRole('button', { name: 'You prefer concise replies.' })).toBeInTheDocument();
        // The claim line in the list reads the saved words too.
        expect(within(screen.getByTestId('belief-groups')).getByRole('button', { name: 'You prefer concise replies.' })).toBeInTheDocument();
        expect(mocks.notify).not.toHaveBeenCalled();
        expect(screen.getByRole('button', { name: /^Other\s*1$/ })).toBeInTheDocument();
    });

    it('cancels an edit without saving', async () => {
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: 'Edit wording' }));
        fireEvent.change(screen.getByRole('textbox', { name: 'Memory point' }), { target: { value: 'Discard this draft' } });
        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
        expect(mocks.saveEntry).not.toHaveBeenCalled();
        expect(screen.queryByRole('textbox', { name: 'Memory point' })).not.toBeInTheDocument();
        expect(within(screen.getByRole('complementary', { name: 'Belief detail' })).getByRole('button', { name: 'You prefer detailed replies.' })).toBeInTheDocument();
    });

    it('clears the active draft when its memory is forgotten and starts the next edit as a new record', async () => {
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: 'Edit wording' }));
        fireEvent.change(screen.getByRole('textbox', { name: 'Memory point' }), { target: { value: 'Draft that must be discarded' } });
        fireEvent.click(screen.getByRole('button', { name: 'Forget this' }));
        const dialog = screen.getByRole('dialog');
        await within(dialog).findByText('Detailed communication preference');
        mocks.getEntries.mockResolvedValue([]);
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget this' }));
        await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
        expect(screen.queryByRole('textbox', { name: 'Memory point' })).not.toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
        expect(mocks.saveEntry).not.toHaveBeenCalled();
        await openPageMenu();
        fireEvent.click(await screen.findByRole('menuitem', { name: 'Add a note about you' }));
        const newDialog = await screen.findByRole('dialog', { name: 'Add a note about you' });
        expect(within(newDialog).getByRole('textbox', { name: 'Add a note about you' })).toHaveValue('');
        fireEvent.change(within(newDialog).getByRole('textbox', { name: 'Add a note about you' }), { target: { value: 'You keep emails to three sentences.' } });
        fireEvent.click(within(newDialog).getByRole('button', { name: 'Save' }));
        await waitFor(() => expect(mocks.saveEntry).toHaveBeenCalledWith('', '# You keep emails to three sentences.\n- **Source**: user_edit\n\nYou keep emails to three sentences.\n'));
    });

    it('keeps an unsaved edit after failure and allows a retry', async () => {
        mocks.saveEntry.mockRejectedValueOnce(new Error('disk full'));
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: 'Edit wording' }));
        const editor = screen.getByRole('textbox', { name: 'Memory point' });
        fireEvent.change(editor, { target: { value: 'Keep replies concise.' } });
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        // About you v1.0.0 Y6/Y7: the failure is a copper line in place, not a toast.
        expect((await screen.findByText("Your change didn't save. It's still here.")).closest('[role="alert"]')).toHaveClass('text-copper');
        expect(mocks.notify).not.toHaveBeenCalled();
        expect(editor).toHaveValue('Keep replies concise.');
        expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled();
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        await waitFor(() => expect(mocks.saveEntry).toHaveBeenCalledTimes(2));
        await waitFor(() => expect(screen.queryByRole('textbox', { name: 'Memory point' })).not.toBeInTheDocument());
    });

    it('previews every removed derivative, stored copies and scope before confirming with its token', async () => {
        await renderSelected();
        const dialog = await openForget();
        expect(mocks.getPreview).toHaveBeenCalledWith(entry.id);
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
        expect(within(dialog).getAllByText('Writing style').length).toBeGreaterThan(0);
        expect(within(dialog).getByText('Archived memory')).toBeInTheDocument();
        expect(within(dialog).getByText('Earlier writing style')).toBeInTheDocument();
        expect(within(dialog).getByText('Self Dream')).toBeInTheDocument();
        expect(within(dialog).getByText('Purpose Map')).toBeInTheDocument();
        expect(within(dialog).getByText('Work profile snapshot')).toBeInTheDocument();
        expect(within(dialog).getByText('Prepare a detailed weekly report')).toBeInTheDocument();
        expect(within(dialog).getByText('Stored search embeddings: 3. Saved versions: 2.')).toBeInTheDocument();
        expect(within(dialog).getByText('For 30 days, matching re-observations are blocked from memory, Dream and chat context.')).toBeInTheDocument();
        expect(within(dialog).getByText('Source activities and conversations, unrelated logs and external backups are not deleted.')).toBeInTheDocument();
        expect(within(dialog).queryByText('Shared learning analyses and their supporting records are removed when listed. Memory files are removed only when listed as memory records.')).not.toBeInTheDocument();
        mocks.getEntries.mockResolvedValue([]);
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget this' }));
        await waitFor(() => expect(mocks.forgetEntry).toHaveBeenCalledWith(entry.id, 'reviewed-scope-v1'));
        await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
        expect(screen.queryByRole('heading', { name: entry.title })).not.toBeInTheDocument();
        expect(mocks.notify).not.toHaveBeenCalled();
    });

    it('cannot forget before the preview has loaded', async () => {
        let resolvePreview!: (value: MemoryForgetPreview) => void;
        mocks.getPreview.mockReturnValue(new Promise((resolve) => { resolvePreview = resolve; }));
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: 'Forget this' }));
        const dialog = screen.getByRole('dialog');
        const confirm = within(dialog).getByRole('button', { name: 'Forget this' });
        expect(confirm).toBeDisabled();
        // About you v1.0.0 Y8: no loading words while the preview loads.
        expect(within(dialog).queryByRole('status')).not.toBeInTheDocument();
        fireEvent.click(confirm);
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
        await act(async () => resolvePreview(preview()));
        expect(confirm).toBeEnabled();
    });

    it.each([
        ['en', 'Forget this', 'Nothing has been forgotten', 'Make sure your selected AI model is available, then retry the preview.', 'Refresh preview'],
        ['zh-CN', '忘记这条', '尚未忘记任何内容', '请确认所选 AI 模型可用，然后重新加载预览。', '刷新预览'],
    ])('recovers from a storage preview failure without a model-availability hint in %s', async (language, action, error, hint, refresh) => {
        await i18n.changeLanguage(language);
        mocks.getPreview.mockRejectedValueOnce(new Error('storage unavailable'));
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: action }));
        const dialog = screen.getByRole('dialog');
        expect(await within(dialog).findByRole('alert')).toHaveTextContent(error);
        expect(within(dialog).queryByText(hint)).not.toBeInTheDocument();
        expect(within(dialog).queryByRole('button', { name: action })).not.toBeInTheDocument();
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole('button', { name: refresh }));
        await within(dialog).findByText('Detailed communication preference');
        expect(within(dialog).getByRole('button', { name: action })).toBeEnabled();
    });

    it('requires reviewing a fresh scope after forget fails or its token is stale', async () => {
        mocks.forgetEntry.mockRejectedValueOnce(new Error('scope changed'));
        await renderSelected();
        const dialog = await openForget();
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget this' }));
        expect(await within(dialog).findByRole('alert')).toHaveTextContent('Refresh the preview');
        expect(within(dialog).queryByText('Detailed communication preference')).not.toBeInTheDocument();
        expect(within(dialog).queryByRole('button', { name: 'Forget this' })).not.toBeInTheDocument();
        mocks.getPreview.mockResolvedValue(preview({ confirmationToken: 'reviewed-scope-v2', historyCount: 4 }));
        fireEvent.click(within(dialog).getByRole('button', { name: 'Refresh preview' }));
        await within(dialog).findByText('Stored search embeddings: 3. Saved versions: 4.');
        expect(mocks.forgetEntry).toHaveBeenCalledTimes(1);
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget this' }));
        await waitFor(() => expect(mocks.forgetEntry).toHaveBeenLastCalledWith(entry.id, 'reviewed-scope-v2'));
        await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    });

    it('spaces the composed Chinese success counts correctly', async () => {
        await i18n.changeLanguage('zh-CN');
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: '忘记这条' }));
        const dialog = screen.getByRole('dialog');
        await within(dialog).findByText('Detailed communication preference');
        fireEvent.click(within(dialog).getByRole('button', { name: '忘记这条' }));
        await waitFor(() => expect(mocks.forgetEntry).toHaveBeenCalled());
        expect(mocks.notify).not.toHaveBeenCalled();
    });

    it('renders the editing and forget controls in Chinese', async () => {
        await i18n.changeLanguage('zh-CN');
        await renderSelected();
        fireEvent.click(screen.getByRole('button', { name: '修改措辞' }));
        expect(screen.getByRole('textbox', { name: '记忆要点' })).toHaveValue('You prefer detailed replies.');
        expect(screen.getByText('你的修改会保存为用户编辑，优先于观察到的记忆。')).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: '取消' }));
        fireEvent.click(screen.getByRole('button', { name: '忘记这条' }));
        const dialog = screen.getByRole('dialog', { name: '忘记这一条？' });
        await within(dialog).findByText('自我梦境');
        expect(within(dialog).getByText('已归档的记忆')).toBeInTheDocument();
        expect(within(dialog).getByText('目标图谱')).toBeInTheDocument();
        expect(within(dialog).getByText('工作画像快照')).toBeInTheDocument();
        expect(within(dialog).getByText('原始活动和对话、无关日志以及外部备份不会被删除。')).toBeInTheDocument();
        expect(within(dialog).getByText('忘记操作可离线完成，使用本地事实哈希进行匹配。AI 匹配可用时，还能帮助识别换一种说法的事实。')).toBeInTheDocument();
        expect(within(dialog).getByRole('button', { name: '忘记这条' })).toBeEnabled();
    });
});


describe('MemoryPage memory-first browsing', () => {
    const id = 'b4c8e1a9-4f72-4b18-a19d-6f143d66e98b';
    const untitled = { ...entry, id, title: id, content: '---\nuuid: ' + id + '\ntype: memory\ndimension: user_insight\n---\nYou prefer concise replies.' };

    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([untitled]);
        mocks.getPreview.mockResolvedValue(preview({ memoryId: id, items: [{ kind: 'memory', id, title: id }] }));
    });

    it.each([
        ['en', 'More memory actions', 'Copy memory ID'],
        ['zh-CN', '更多记忆操作', '复制记忆 ID'],
    ])('keeps the ID entirely out of the DOM and copies it only through the menu in %s', async (language, menu, copy) => {
        await i18n.changeLanguage(language);
        const writeText = vi.fn().mockResolvedValue(undefined);
        Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
        const { container } = render(<MemoryPage devToolsUnlocked initialSelectedId={id} />);
        await screen.findByRole('heading', { name: 'You prefer concise replies.' });
        expect(container.innerHTML).not.toContain(id);
        expect(screen.queryByRole('button', { name: copy })).not.toBeInTheDocument();
        fireEvent.keyDown(screen.getByRole('button', { name: menu }), { key: 'Enter' });
        const copyAction = await screen.findByRole('menuitem', { name: copy });
        expect(document.body.innerHTML).not.toContain(id);
        fireEvent.click(copyAction);
        expect(writeText).toHaveBeenCalledWith(id);
    });
});

describe('MemoryPage human-first detail', () => {
    const detailed = { ...entry, content: entry.content + '- You use examples when helpful. You keep meetings short.\n', apps: ['ChatGPT', 'loginwindow', 'Safari', 'chatgpt', 'Isoform', 'Terminal'], activitySessionIds: ['hidden-session'] };
    const yesterday = Date.now() - 86400000;
    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([detailed]);
        mocks.getStatus.mockResolvedValue({ files: 1, chunks: 0, vectorReady: false });
        mocks.getEvidence.mockResolvedValue([{ at: yesterday, durationMs: 2 * 3600000, appName: 'ChatGPT', text: 'Prepared a weekly update.', activitySessionId: 'hidden-session' }]);
        mocks.saveEntry.mockImplementation(async (id: string, content: string) => ({ ...detailed, id, content, source: 'user_edit' }));
    });

    it.each([
        ['en', 'Memory points', 'Why Isoform believes this', 'Where Isoform uses it', 'Technical details', 'Not right', 'Seen yesterday · 2 hours', 'From your ChatGPT: Prepared a weekly update.'],
        ['zh-CN', '记忆要点', 'Isoform 为什么这样认为', 'Isoform 在哪里用到它', '技术细节', '不准确', '昨天看到 · 2 小时', '来自你的 ChatGPT：Prepared a weekly update.'],
    ])('orders points, provenance, readable evidence, uses and closed technical details in %s', async (language, pointsLabel, why, uses, technical, reject, seen, source) => {
        await i18n.changeLanguage(language);
        const { container } = render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        await screen.findByRole('heading', { name: entry.title });
        const points = screen.getByRole('region', { name: pointsLabel });
        const provenance = screen.getByTestId('memory-provenance');
        const whyHeading = screen.getByRole('heading', { name: why });
        const usesHeading = screen.getByRole('heading', { name: uses });
        const technicalButton = screen.getAllByRole('button', { name: technical }).at(-1)!;
        for (const [before, after] of [[points, provenance], [provenance, whyHeading], [whyHeading, usesHeading], [usesHeading, technicalButton]]) {
            expect(before.compareDocumentPosition(after) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        }
        expect(technicalButton).toHaveAttribute('aria-expanded', 'false');
        // Evidence is the source in words (v1.0.2 Y35): where, then what Isoform saw.
        expect(await screen.findByText(source)).toBeVisible();
        expect(screen.getByText(seen)).toBeInTheDocument();
        expect(container.innerHTML).not.toContain('hidden-session');
        expect(provenance).toHaveTextContent('ChatGPT');
        expect(provenance).not.toHaveTextContent('loginwindow');
        expect(provenance).not.toHaveTextContent('Terminal');
        expect(within(points).getAllByRole('button', { name: /^You / })).toHaveLength(3);
        fireEvent.click(screen.getByRole('button', { name: reject }));
        expect(await screen.findByRole('dialog')).toBeInTheDocument();
    });

    it('opens the newest moment in Activity without putting its ID on the page', async () => {
        const onOpen = vi.fn();
        const { container } = render(<MemoryPage devToolsUnlocked onOpenActivitySession={onOpen} initialSelectedId={entry.id} />);
        fireEvent.click(await screen.findByRole('button', { name: 'See these moments in Activity' }));
        expect(onOpen).toHaveBeenCalledWith('hidden-session');
        expect(container.innerHTML).not.toContain('hidden-session');
    });

    it('says where Isoform used the belief, in words', async () => {
        mocks.getUses.mockResolvedValue({ total: 4, lastAt: yesterday, surfaces: [
            { surface: 'brief', count: 1, lastAt: Date.now() - 3 * 86400000 },
            { surface: 'chat', count: 3, lastAt: yesterday },
        ] });
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        const items = await screen.findAllByRole('listitem');
        expect(items.map((item) => item.textContent)).toEqual([
            'Used 3 times in chat answers · last yesterday',
            'Used once in your briefs · last 3 days ago',
        ]);
        expect(mocks.getUses).toHaveBeenCalledWith('memory', entry.id);
    });

    it('says when Isoform has not used a belief, and offers a retry when evidence fails', async () => {
        mocks.getEvidence.mockRejectedValueOnce(new Error('store'));
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        expect(await screen.findByText('Isoform has not used this yet.')).toBeInTheDocument();
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent('Could not load why Isoform believes this.');
        fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
        expect(await screen.findByText('From your ChatGPT: Prepared a weekly update.')).toBeInTheDocument();
    });

    it('keeps older provenance relative and handles evidence without app names', async () => {
        mocks.getEntries.mockResolvedValue([{ ...detailed, updatedAt: Date.now() - 4 * 86400000 }]);
        mocks.getEvidence.mockResolvedValue([{ at: Date.now() - 5 * 86400000, durationMs: 0, appName: 'Notes', text: 'A planning note.', activitySessionId: '' }]);
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        await waitFor(() => expect(screen.getByTestId('memory-provenance')).toHaveTextContent(/^Learned from your work over the last 5 days, mostly in [^.]+\. Updated 4 days ago\.$/));
        expect(await screen.findByText('From your Notes: A planning note.')).toBeVisible();
        expect(screen.getByText('Seen 5 days ago')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'See these moments in Activity' })).not.toBeInTheDocument();
    });

    it('says one true English date line and drops Why when there is no source (v1.0.2 Y35, Y36)', async () => {
        const daysAgo = (count: number) => { const day = new Date(); day.setDate(day.getDate() - count); day.setHours(12, 0, 0, 0); return day.getTime(); };
        const plain = { ...entry, apps: [], activitySessionIds: [], updatedAt: daysAgo(3), modTime: daysAgo(3) };
        resetDefaults([plain]);
        mocks.getAbout.mockResolvedValue(aboutFor([plain], { status: 'learned_today', firstLearnedAt: daysAgo(12) }));
        mocks.getEvidence.mockResolvedValue([]);
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        await waitFor(() => expect(screen.getByTestId('memory-provenance')).toHaveTextContent(/^Learned from your work over the last 12 days\. Updated 3 days ago\.$/));
        await waitFor(() => expect(mocks.getEvidence).toHaveBeenCalled());
        await act(async () => {});
        expect(screen.queryByRole('heading', { name: 'Why Isoform believes this' })).not.toBeInTheDocument();
        expect(screen.queryByText('Learned today')).not.toBeInTheDocument();
    });

    it('edits just one sentence, preserves metadata and neighboring points, and retains drafts after a failure', async () => {
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        const point = await screen.findByRole('button', { name: 'You keep meetings short.' });
        fireEvent.click(point);
        const editor = screen.getByRole('textbox', { name: 'Memory point' });
        fireEvent.change(editor, { target: { value: 'You keep meetings focused.' } });
        mocks.saveEntry.mockRejectedValueOnce(new Error('disk full'));
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        expect(await screen.findByRole('alert')).toBeInTheDocument();
        expect(editor).toHaveValue('You keep meetings focused.');
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        await waitFor(() => expect(mocks.saveEntry).toHaveBeenLastCalledWith(entry.id, detailed.content.replace('You keep meetings short.', 'You keep meetings focused.')));
        await screen.findByRole('button', { name: 'You keep meetings focused.' });
        expect(screen.queryByRole('textbox', { name: 'Memory point' })).not.toBeInTheDocument();
    });

    it('adds a point, cancels without saving and removes only the chosen point', async () => {
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Add a point' }));
        fireEvent.change(screen.getByRole('textbox', { name: 'Memory point' }), { target: { value: 'You work on the computer and review on my phone.' } });
        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
        expect(mocks.saveEntry).not.toHaveBeenCalled();
        fireEvent.click(screen.getByRole('button', { name: 'Add a point' }));
        fireEvent.change(screen.getByRole('textbox', { name: 'Memory point' }), { target: { value: 'You review on my phone.' } });
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        await screen.findByRole('button', { name: 'You review on my phone.' });
        fireEvent.click(screen.getByRole('button', { name: 'Forget point: You use examples when helpful.' }));
        await waitFor(() => expect(mocks.saveEntry).toHaveBeenCalledTimes(2));
        const content = mocks.saveEntry.mock.calls[1][1];
        expect(content).not.toContain('You use examples when helpful.');
        expect(content).toContain('You keep meetings short.');
        expect(content).toContain('- **Dimension**: user_insight');
    });

    it('does not carry an inline draft or open sections into another memory', async () => {
        resetDefaults([detailed, { ...entry, id: 'second', title: 'Planning', content: '# Planning\n- You plan on paper.' }]);
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Edit wording' }));
        fireEvent.change(screen.getByRole('textbox', { name: 'Memory point' }), { target: { value: 'Unsaved sentence' } });
        const detail = screen.getByRole('complementary', { name: 'Belief detail' });
        fireEvent.click(within(detail).getByRole('button', { name: 'Technical details' }));
        // The claim's words open its detail.
        fireEvent.click(within(screen.getByTestId('belief-groups')).getByRole('button', { name: 'You plan on paper.' }));
        expect(await screen.findByRole('heading', { name: 'Planning' })).toBeInTheDocument();
        expect(screen.queryByRole('textbox', { name: 'Memory point' })).not.toBeInTheDocument();
        expect(within(screen.getByRole('complementary', { name: 'Belief detail' })).getByRole('button', { name: 'Technical details' })).toHaveAttribute('aria-expanded', 'false');
    });

    it('closes the detail on Escape, then the dialog, but a menu or an editor keeps its own Escape', async () => {
        const onOpenChange = vi.fn();
        function Host() {
            const escapeRef = useRef<(() => boolean) | null>(null);
            const [open, setOpen] = useState(true);
            return (
                <Dialog open={open} onOpenChange={(next) => { onOpenChange(next); setOpen(next); }}>
                    <RetainedDialogContent open={open} retain={false} onEscapeKeyDown={(e) => { if (escapeRef.current?.()) e.preventDefault(); }}>
                        <DialogTitle>About you</DialogTitle>
                        <MemoryPage devToolsUnlocked initialSelectedId={entry.id} escapeRef={escapeRef} />
                    </RetainedDialogContent>
                </Dialog>
            );
        }
        render(<Host />);
        await screen.findByRole('heading', { name: entry.title });
        // A menu closes alone.
        await openDetailMenu();
        const copy = await screen.findByRole('menuitem', { name: 'Copy memory ID' });
        fireEvent.keyDown(copy, { key: 'Escape' });
        await waitFor(() => expect(screen.queryByRole('menuitem', { name: 'Copy memory ID' })).not.toBeInTheDocument());
        expect(screen.getByRole('heading', { name: entry.title })).toBeInTheDocument();
        // An open point editor cancels itself and keeps the belief open.
        fireEvent.click(screen.getByRole('button', { name: 'Edit wording' }));
        const editor = screen.getByRole('textbox', { name: 'Memory point' });
        editor.focus();
        fireEvent.keyDown(editor, { key: 'Escape' });
        expect(screen.queryByRole('textbox', { name: 'Memory point' })).not.toBeInTheDocument();
        expect(screen.getByRole('heading', { name: entry.title })).toBeInTheDocument();
        // Then the belief closes, then the dialog.
        fireEvent.keyDown(screen.getByRole('heading', { name: 'What Isoform believes about you' }), { key: 'Escape' });
        expect(screen.queryByRole('heading', { name: entry.title })).not.toBeInTheDocument();
        expect(onOpenChange).not.toHaveBeenCalled();
        fireEvent.keyDown(screen.getByRole('heading', { name: 'What Isoform believes about you' }), { key: 'Escape' });
        expect(onOpenChange).toHaveBeenCalledWith(false);
    });

    it('keeps the Activity link when the evidence read fails', async () => {
        mocks.getEvidence.mockRejectedValue(new Error('store'));
        const onOpen = vi.fn();
        render(<MemoryPage devToolsUnlocked onOpenActivitySession={onOpen} initialSelectedId={entry.id} />);
        expect(await screen.findByText('Could not load why Isoform believes this.')).toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: 'Open where this was learned in Activity' }));
        expect(onOpen).toHaveBeenCalledWith('hidden-session');
    });

    it('offers the linked session in Activity when no readable moment is left', async () => {
        mocks.getEvidence.mockResolvedValue([]);
        const onOpen = vi.fn();
        render(<MemoryPage devToolsUnlocked onOpenActivitySession={onOpen} initialSelectedId={entry.id} />);
        // About you v1.0.0 Y20: no placeholder line for missing evidence.
        await waitFor(() => expect(mocks.getEvidence).toHaveBeenCalled());
        expect(screen.queryByText('No activity evidence is available for this memory.')).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: 'Open where this was learned in Activity' }));
        expect(onOpen).toHaveBeenCalledWith('hidden-session');
    });
});

describe('About you overview', () => {
    const second = { ...entry, id: 'second', title: 'Planning', dimension: 'user_insight', content: '# Planning\n- You plan the week on Sunday evening.\n' };
    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([entry, second]);
    });

    it.each([
        ['en', 'What Isoform believes about you', '2 beliefs from 3 weeks of your work. You haven’t confirmed any yet. Stored on your computer. You can correct or forget any of it.'],
        ['zh-CN', 'Isoform 对你的认识', '2 条认识，来自你 3 周的工作。你还没有确认任何一条。存储在你的电脑上。你可以更正或忘记其中任何一条。'],
    ])('leads with what Isoform believes, over how long, in %s', async (language, title, summary) => {
        await i18n.changeLanguage(language);
        mocks.getAbout.mockResolvedValue(aboutFor([entry, second], { firstLearnedAt: Date.now() - 21 * 86400000 }));
        render(<MemoryPage devToolsUnlocked />);
        expect(await screen.findByRole('heading', { level: 1, name: title })).toBeInTheDocument();
        expect(await screen.findByText(summary)).toBeInTheDocument();
    });

    it('puts agent-practice beliefs in one collapsed section when the agent section is on', async () => {
        mocks.getAbout.mockResolvedValue({ ...aboutFor([entry, second]), agentSection: true, beliefs: [
            { ...aboutFor([entry]).beliefs[0], area: 'work' },
            { ...aboutFor([second]).beliefs[0], area: 'work', agentPractice: true },
        ] });
        render(<MemoryPage devToolsUnlocked />);
        const agents = await screen.findByRole('button', { name: /^How you work with your agents\s*1$/ });
        expect(screen.getByRole('button', { name: /^Work\s*1$/ })).toBeInTheDocument();
        // Collapsed by default: the belief is not listed until it is opened.
        expect(screen.queryByRole('button', { name: 'You plan the week on Sunday evening.' })).not.toBeInTheDocument();
        fireEvent.click(agents);
        expect(await screen.findByRole('button', { name: 'You plan the week on Sunday evening.' })).toBeInTheDocument();
        expect(screen.getByText('How you run your coding agents. Kept apart from what Isoform learns about you.')).toBeInTheDocument();
    });

    it('keeps agent-practice beliefs in their area while the agent section is off', async () => {
        mocks.getAbout.mockResolvedValue({ ...aboutFor([entry, second]), beliefs: [
            { ...aboutFor([entry]).beliefs[0], area: 'work' },
            { ...aboutFor([second]).beliefs[0], area: 'work', agentPractice: true },
        ] });
        render(<MemoryPage devToolsUnlocked />);
        expect(await screen.findByRole('button', { name: /^Work\s*2$/ })).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: /How you work with your agents/ })).not.toBeInTheDocument();
    });

    // computer-you v1.0.0 rule 4: the rest by area, each a claim line; no
    // confidence bars, kind labels or status chips in the list any more.
    it('groups unjudged claims by fixed area as claim lines, with no confidence bar, kind label or chip', async () => {
        mocks.getAbout.mockResolvedValue({ ...aboutFor([entry, second]), beliefs: [
            { ...aboutFor([entry]).beliefs[0], area: 'work', confidence: 4 },
            { ...aboutFor([second]).beliefs[0], area: 'people', status: 'learned_today', confidence: 1 },
        ] });
        render(<MemoryPage devToolsUnlocked />);
        const groups = await screen.findByTestId('belief-groups');
        expect(within(groups).getByRole('button', { name: /^Work\s*1$/ })).toBeInTheDocument();
        expect(within(groups).getByRole('button', { name: /^People\s*1$/ })).toBeInTheDocument();
        const lines = within(groups).getAllByRole('article');
        expect(lines).toHaveLength(2);
        for (const line of lines) {
            expect(line).toHaveAttribute('data-claim-row');
            expect(line).toHaveTextContent('Right?');
        }
        expect(screen.queryByRole('img', { name: /^Confidence \d of 4$/ })).not.toBeInTheDocument();
        expect(document.querySelector('[data-status]')).toBeNull();
        expect(screen.queryByText('Inferred')).not.toBeInTheDocument();
        expect(screen.queryByText('A preference')).not.toBeInTheDocument();
        expect(screen.queryByText('Something I learned about you')).not.toBeInTheDocument();
        expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument();
    });

    // jsdom has no layout, so this guards the layout classes: area groups
    // stack at full width (never grid cells side by side) and each group's
    // claim lines stack too.
    it('stacks belief groups and their claim lines at full width', async () => {
        mocks.getAbout.mockImplementation(async () => aboutFor([entry, second], { newThisWeek: [second.id] }));
        render(<MemoryPage devToolsUnlocked />);
        const groups = await screen.findByTestId('belief-groups');
        expect(groups).toHaveClass('flex', 'flex-col');
        expect(groups.className).not.toMatch(/\bgrid\b|grid-template-columns/);
        expect(groups.children).toHaveLength(1);
        for (const rows of screen.getAllByTestId('belief-group-rows')) {
            expect(rows).toHaveClass('flex', 'flex-col');
            expect(rows.className).not.toMatch(/\bgrid\b/);
        }
        const cards = screen.getByTestId('review-cards');
        expect(cards).toHaveClass('flex', 'flex-col');
        expect(within(cards).getAllByRole('article')).toHaveLength(1);
    });

    it('keeps the storage numbers folded under Technical details', async () => {
        render(<MemoryPage devToolsUnlocked />);
        const technical = await screen.findByRole('button', { name: 'Technical details' });
        expect(screen.queryByText(/chunks indexed/)).not.toBeInTheDocument();
        fireEvent.click(technical);
        expect(screen.getByText('2 files · 240 B · 3 chunks indexed · vector search active')).toBeInTheDocument();
    });

    it('shows a skeleton while loading and a retry when the beliefs fail to load', async () => {
        // A published sibling read re-runs the load, so the failure persists
        // until the store recovers and the user retries.
        mocks.getEntries.mockRejectedValue(new Error('disk'));
        const { container } = render(<MemoryPage devToolsUnlocked />);
        expect(container.querySelector('[aria-busy="true"]')).not.toBeNull();
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent('Could not load what Isoform believes about you.');
        mocks.getEntries.mockResolvedValue([entry, second]);
        fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
        expect(await screen.findByRole('button', { name: 'You plan the week on Sunday evening.' })).toBeInTheDocument();
    });

    it('says when confidence and new beliefs cannot be read, keeps the beliefs, and retries', async () => {
        mocks.getAbout.mockRejectedValue(new Error('store'));
        render(<MemoryPage devToolsUnlocked />);
        const alert = await screen.findByRole('alert');
        expect(alert).toHaveTextContent('Could not load what is new this week or how sure Isoform is.');
        expect(screen.getByRole('button', { name: 'You plan the week on Sunday evening.' })).toBeInTheDocument();
        mocks.getAbout.mockResolvedValue(aboutFor([entry, second], { newThisWeek: [second.id] }));
        fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
        // The read is back: the new claim moves up to To check.
        const toCheck = await screen.findByRole('region', { name: 'To check' });
        expect(within(toCheck).getByRole('button', { name: 'You plan the week on Sunday evening.' })).toBeInTheDocument();
        expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    });

    it('builds no area-of-life switch and no "Only for work"', async () => {
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        await screen.findByRole('heading', { name: entry.title });
        expect(screen.queryByText(/Area of life/i)).not.toBeInTheDocument();
        expect(screen.queryByText(/Only for work/i)).not.toBeInTheDocument();
    });
});

describe('About you To check', () => {
    const fresh = { ...entry, id: 'fresh', title: 'Short calls', apps: ['Zoom', 'loginwindow'], content: '# Short calls\n- You keep calls short before 11 AM.\n' };
    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([entry, fresh]);
        mocks.getAbout.mockImplementation(async () => aboutFor([entry, fresh], { newThisWeek: ['fresh'] }));
        mocks.confirm.mockResolvedValue({ ...fresh });
        mocks.getPreview.mockResolvedValue(preview({ memoryId: 'fresh', items: [{ kind: 'memory', id: 'fresh', title: 'Short calls' }, { kind: 'self_dream', id: 'd', title: 'Calls' }] }));
        mocks.forgetEntry.mockResolvedValue(undefined);
    });
    afterEach(() => vi.useRealTimers());

    async function card() {
        const section = await screen.findByRole('region', { name: 'To check' });
        return within(section).getByRole('article', { name: /You keep calls short before 11 AM\./ });
    }

    it('lists each new belief with how often it was seen and where, never a system app', async () => {
        render(<MemoryPage devToolsUnlocked />);
        const article = await card();
        expect(article).toHaveTextContent('Seen 2 times · Zoom');
        expect(article).not.toHaveTextContent('loginwindow');
        expect(article).toHaveTextContent('You keep calls short before 11 AM.');
        // About you v1.0.0 Y17: no count of what is left.
        expect(screen.queryByText('1 to check')).not.toBeInTheDocument();
    });

    it('says Learned today once on a To check line, as the line, not as a chip too (Y32)', async () => {
        mocks.getAbout.mockResolvedValue(aboutFor([entry, fresh], { newThisWeek: ['fresh'], status: 'learned_today' }));
        render(<MemoryPage devToolsUnlocked />);
        const article = await card();
        expect(article.querySelector('[data-status="learned_today"]')).toBeNull();
        expect(within(article).queryAllByText(/Learned today/).length).toBeLessThanOrEqual(1);
    });

    it('reads a subjectless belief in second person, without quotation marks (Y28)', async () => {
        const subjectless = { ...fresh, content: '# Short calls\n- Keeps calls short before 11 AM.\n' };
        mocks.getAbout.mockResolvedValue(aboutFor([entry, subjectless], { newThisWeek: ['fresh'] }));
        render(<MemoryPage devToolsUnlocked />);
        const article = await card();
        expect(article).toHaveTextContent('You keep calls short before 11 AM.');
        expect(article).not.toHaveTextContent('“');
    });

    it('confirms in one click with Yes: the line says what it means, Undo takes it back', async () => {
        render(<MemoryPage devToolsUnlocked />);
        const article = await card();
        // No status chip on a claim line.
        expect(article.querySelector('[data-status]')).toBeNull();
        fireEvent.click(within(article).getByRole('button', { name: 'Yes' }));
        await waitFor(() => expect(mocks.confirm).toHaveBeenCalledWith('fresh'));
        const line = await within(article).findByText('Confirmed. Isoform treats this as fact now.');
        expect(line).toHaveAttribute('role', 'status');
        expect(line).toHaveFocus();
        // The answered line keeps its place, without its answer buttons.
        expect(within(article).queryByRole('button', { name: 'No' })).not.toBeInTheDocument();
        expect(screen.queryByText('All checked')).not.toBeInTheDocument();
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
        // No toast: the answer is the line on the card.
        expect(mocks.notify).not.toHaveBeenCalled();

        fireEvent.click(within(article).getByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(mocks.undoConfirm).toHaveBeenCalledWith('fresh'));
        expect(await within(article).findByRole('button', { name: 'Yes' })).toBeInTheDocument();
    });

    // computer-you v1.0.0 rule 3: every line is a claim, "Right?" with Yes,
    // No, Not mine and Edit, and Y, N, M and E answer the focused one.
    it('judges a claim with the keyboard: Y confirms, N stops using it, E opens Edit', async () => {
        mocks.notQuite.mockResolvedValue(0);
        render(<MemoryPage devToolsUnlocked />);
        const article = await card();
        expect(article).toHaveTextContent('Right?');
        expect(article).toHaveAttribute('tabindex', '0');
        article.focus();
        fireEvent.keyDown(article, { key: 'e' });
        expect(within(article).getByRole('textbox', { name: 'Editing' })).toHaveFocus();
        fireEvent.keyDown(within(article).getByRole('textbox'), { key: 'Escape' });
        expect(await within(article).findByRole('button', { name: 'Yes' })).toBeInTheDocument();
        fireEvent.keyDown(article, { key: 'n' });
        await waitFor(() => expect(mocks.notQuite).toHaveBeenCalledWith('fresh'));
        expect(await within(article).findByText('Isoform stopped using this.')).toBeInTheDocument();
        fireEvent.click(within(article).getByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(mocks.undoNotQuite).toHaveBeenCalledWith('fresh', 0));
        const reopened = await card();
        fireEvent.keyDown(reopened, { key: 'y' });
        await waitFor(() => expect(mocks.confirm).toHaveBeenCalledWith('fresh'));
        expect(mocks.saveEntry).not.toHaveBeenCalled();
    });

    // computer-you v1.0.0: You's header is the one header under its tabs.
    it('drops its own title under You, keeping its summary', async () => {
        render(<YouPage tab="about" onTabChange={() => {}}><MemoryPage devToolsUnlocked /></YouPage>);
        await card();
        expect(screen.getAllByRole('heading', { level: 1 }).map((h) => h.textContent)).toEqual(['You']);
        expect(screen.queryByText('About you', { selector: 'h1' })).toBeNull();
    });

    it('says the portrait can use a confirm-row belief now', async () => {
        mocks.getAbout.mockImplementation(async () => aboutFor([entry, fresh], { newThisWeek: ['fresh'], portraitCandidate: true }));
        render(<MemoryPage devToolsUnlocked />);
        const article = await card();
        fireEvent.click(within(article).getByRole('button', { name: 'Yes' }));
        expect(await within(article).findByText('Confirmed. Your portrait can use this now.')).toBeInTheDocument();
    });

    it('keeps the card open with a retry message when confirming fails', async () => {
        mocks.confirm.mockRejectedValueOnce(new Error('suppressed'));
        render(<MemoryPage devToolsUnlocked />);
        const article = await card();
        fireEvent.click(within(article).getByRole('button', { name: 'Yes' }));
        expect(await within(article).findByRole('alert')).toHaveTextContent("Didn't save.");
        expect(within(article).getByRole('button', { name: 'Yes' })).toBeEnabled();
    });

    // redesign v1.0.0 section 1: No stops using it at once; Edit is the words path.
    it('answers No at once, with no words field, and Undo puts it back', async () => {
        mocks.notQuite.mockResolvedValue(0);
        render(<MemoryPage devToolsUnlocked />);
        const article = await card();
        fireEvent.click(within(article).getByRole('button', { name: 'No' }));
        await waitFor(() => expect(mocks.notQuite).toHaveBeenCalledWith('fresh'));
        expect(await within(article).findByText('Isoform stopped using this.')).toBeInTheDocument();
        expect(within(article).queryByRole('textbox')).not.toBeInTheDocument();
        expect(screen.queryByText('What’s closer? You can leave it blank.')).not.toBeInTheDocument();
        expect(mocks.rewordEntry).not.toHaveBeenCalled();
        expect(mocks.saveEntry).not.toHaveBeenCalled();
        fireEvent.click(within(article).getByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(mocks.undoNotQuite).toHaveBeenCalledWith('fresh', 0));
        expect(await within(article).findByRole('button', { name: 'Yes' })).toBeInTheDocument();
    });

    it('forgets in one click after 10 seconds with the reviewed preview token, and Undo cancels it', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        render(<MemoryPage devToolsUnlocked />);
        let article = await card();
        fireEvent.click(within(article).getByRole('button', { name: /^Forget: / }));
        // Announced once; the visible countdown is silent.
        expect(within(article).getByRole('status')).toHaveTextContent('Forgetting in 10 seconds.');
        expect(within(article).getByRole('button', { name: 'Undo' })).toHaveFocus();
        await act(async () => { vi.advanceTimersByTime(4000); });
        expect(within(article).getByText(/Forgetting in [56] seconds\./)).toHaveAttribute('aria-hidden', 'true');
        expect(within(article).getByRole('status')).toHaveTextContent('Forgetting in 10 seconds.');
        fireEvent.click(within(article).getByRole('button', { name: 'Undo' }));
        await act(async () => { vi.advanceTimersByTime(10_000); });
        expect(mocks.getPreview).not.toHaveBeenCalled();
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
        article = await card();
        expect(within(article).getByRole('button', { name: 'Yes' })).toBeInTheDocument();

        fireEvent.click(within(article).getByRole('button', { name: /^Forget: / }));
        mocks.getEntries.mockResolvedValue([entry]);
        mocks.getAbout.mockImplementation(async () => aboutFor([entry]));
        // The backend emits memory:withdrawn on a forget; the mocked runtime
        // swallows events, so the test performs the invalidation it drives.
        mocks.forgetEntry.mockImplementation(async () => {
            pageQueryCache.invalidate(['GetMemoryEntries', 'GetAboutYou', 'GetMemoryStatus', 'GetAboutYouPortrait']);
        });
        await act(async () => { vi.advanceTimersByTime(10_000); });
        await waitFor(() => expect(mocks.forgetEntry).toHaveBeenCalledWith('fresh', 'reviewed-scope-v1'));
        expect(mocks.getPreview).toHaveBeenCalledWith('fresh');
        expect(await within(article).findByText('Forgotten. Isoform won’t learn it again for 30 days.')).toBeInTheDocument();
        expect(within(screen.getByTestId('belief-groups')).queryByRole('button', { name: 'You keep calls short before 11 AM.' })).not.toBeInTheDocument();
    });

    it('says nothing was removed when the one-click forget fails, and retries', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        mocks.forgetEntry.mockRejectedValueOnce(new Error('scope changed'));
        render(<MemoryPage devToolsUnlocked />);
        const article = await card();
        fireEvent.click(within(article).getByRole('button', { name: /^Forget: / }));
        await act(async () => { vi.advanceTimersByTime(10_000); });
        expect(await within(article).findByText('Could not forget this. Nothing was removed.')).toBeInTheDocument();
        fireEvent.click(within(article).getByRole('button', { name: 'Try again' }));
        await waitFor(() => expect(mocks.forgetEntry).toHaveBeenCalledTimes(2));
        expect(mocks.getPreview).toHaveBeenCalledTimes(2);
    });

    it('cancels a pending forget when the page closes, so nothing is forgotten', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        const { unmount } = render(<MemoryPage devToolsUnlocked />);
        const article = await card();
        fireEvent.click(within(article).getByRole('button', { name: /^Forget: / }));
        expect(within(article).getByRole('button', { name: 'Undo' })).toHaveFocus();
        unmount();
        await act(async () => { vi.advanceTimersByTime(15_000); });
        expect(mocks.getPreview).not.toHaveBeenCalled();
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
    });

    it('cancels a pending one-click forget when the reviewed Forget this runs first', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        render(<MemoryPage devToolsUnlocked initialSelectedId="fresh" />);
        const article = await card();
        fireEvent.click(within(article).getByRole('button', { name: /^Forget: / }));
        const detail = screen.getByRole('complementary', { name: 'Belief detail' });
        fireEvent.click(within(detail).getByRole('button', { name: 'Forget this' }));
        const dialog = await screen.findByRole('dialog', { name: 'Forget this?' });
        await within(dialog).findByText('Calls');
        mocks.getEntries.mockResolvedValue([entry]);
        mocks.getAbout.mockImplementation(async () => aboutFor([entry]));
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget this' }));
        await waitFor(() => expect(mocks.forgetEntry).toHaveBeenCalledTimes(1));
        await act(async () => { vi.advanceTimersByTime(15_000); });
        expect(mocks.forgetEntry).toHaveBeenCalledTimes(1);
        expect(screen.queryByText('Could not forget this. Nothing was removed.')).not.toBeInTheDocument();
    });

    it('returns Edit to the open line on Cancel or Esc, and with words saves the wording with Undo', async () => {
        mocks.rewordEntry.mockImplementation(async (id: string, content: string) => ({ ...fresh, id, content, source: 'user_edit' }));
        mocks.undoWording.mockResolvedValue({ ...fresh });
        render(<MemoryPage devToolsUnlocked />);
        let article = await card();
        fireEvent.click(within(article).getByRole('button', { name: 'Edit' }));
        fireEvent.click(within(article).getByRole('button', { name: 'Cancel' }));
        article = await card();
        expect(within(article).getByRole('button', { name: 'Yes' })).toBeInTheDocument();
        fireEvent.click(within(article).getByRole('button', { name: 'Edit' }));
        fireEvent.keyDown(within(article).getByRole('textbox'), { key: 'Escape' });
        expect(within(article).getByRole('button', { name: 'Yes' })).toBeInTheDocument();
        fireEvent.click(within(article).getByRole('button', { name: 'Edit' }));
        const field = within(article).getByRole('textbox', { name: 'Editing' });
        // The field starts with the claim; blank words don't save (No is that answer).
        expect(field).toHaveValue('You keep calls short before 11 AM.');
        expect(within(article).getByText('Isoform keeps your change and learns from it.')).toBeInTheDocument();
        fireEvent.change(field, { target: { value: '   ' } });
        expect(within(article).getByRole('button', { name: 'Save' })).toBeDisabled();
        fireEvent.keyDown(field, { key: 'Enter' });
        expect(mocks.rewordEntry).not.toHaveBeenCalled();
        fireEvent.change(field, { target: { value: 'You keep calls short before noon.' } });
        // Return saves.
        fireEvent.keyDown(field, { key: 'Enter' });
        // The words are kept as an Edit in the answers ledger, beside the save.
        await waitFor(() => expect(mocks.rewordEntry).toHaveBeenCalledWith('fresh', fresh.content.replace('You keep calls short before 11 AM.', 'You keep calls short before noon.'), 'You keep calls short before noon.'));
        expect(await within(article).findByText('Corrected. Isoform will use your wording.')).toBeInTheDocument();
        fireEvent.click(within(article).getByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(mocks.undoWording).toHaveBeenCalledWith('fresh', 'You keep calls short before noon.'));
    });

    it('keeps the confirmation when the belief is edited after Yes', async () => {
        const confirmedContent = '---\nconfirmed_at: 2026-10-22T09:00:00Z\n---\n' + fresh.content;
        mocks.confirm.mockResolvedValue({ ...fresh, content: confirmedContent });
        mocks.saveEntry.mockImplementation(async (id: string, content: string) => ({ ...fresh, id, content }));
        render(<MemoryPage devToolsUnlocked initialSelectedId="fresh" />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        fireEvent.click(await within(detail).findByRole('button', { name: 'Yes' }));
        await waitFor(() => expect(mocks.confirm).toHaveBeenCalledWith('fresh'));
        await waitFor(() => expect(within(detail).getByRole('button', { name: 'Edit wording' })).toBeEnabled());
        fireEvent.click(within(detail).getByRole('button', { name: 'Edit wording' }));
        fireEvent.change(screen.getByRole('textbox', { name: 'Memory point' }), { target: { value: 'You keep calls short before noon.' } });
        fireEvent.click(screen.getByRole('button', { name: 'Save' }));
        await waitFor(() => expect(mocks.saveEntry).toHaveBeenCalledWith('fresh', confirmedContent.replace('You keep calls short before 11 AM.', 'You keep calls short before noon.')));
    });

    it('says so in the detail when confirming fails', async () => {
        mocks.confirm.mockRejectedValueOnce(new Error('suppressed'));
        render(<MemoryPage devToolsUnlocked initialSelectedId="fresh" />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        fireEvent.click(await within(detail).findByRole('button', { name: 'Yes' }));
        expect(await within(detail).findByRole('alert')).toHaveTextContent("Didn't save.");
        expect(within(detail).getByRole('button', { name: 'Yes' })).toBeEnabled();
    });

    it('cancels a pending forget when the page jumps to Activity and stays mounted behind it', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        mocks.getEvidence.mockResolvedValue([{ at: Date.now(), durationMs: 0, appName: 'Zoom', text: 'Planned the week.', activitySessionId: 's-9' }]);
        const onOpen = vi.fn();
        function Host() {
            const [open, setOpen] = useState(true);
            return (
                <Dialog open={open} onOpenChange={setOpen}>
                    <RetainedDialogContent open={open} retain>
                        <DialogTitle>About you</DialogTitle>
                        <MemoryPage
                            initialSelectedId="fresh"
                            active={open}
                            onOpenActivitySession={(id) => { onOpen(id); setOpen(false); }}
                        />
                    </RetainedDialogContent>
                </Dialog>
            );
        }
        render(<Host />);
        const article = await card();
        fireEvent.click(within(article).getByRole('button', { name: /^Forget: / }));
        fireEvent.click(await screen.findByRole('button', { name: 'See these moments in Activity' }));
        expect(onOpen).toHaveBeenCalledWith('s-9');
        await act(async () => { vi.advanceTimersByTime(15_000); });
        expect(mocks.getPreview).not.toHaveBeenCalled();
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
    });

    it('cancels a pending forget when the page is closed but kept mounted', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        const { rerender } = render(<MemoryPage devToolsUnlocked active />);
        const article = await card();
        fireEvent.click(within(article).getByRole('button', { name: /^Forget: / }));
        rerender(<MemoryPage devToolsUnlocked active={false} />);
        await act(async () => { vi.advanceTimersByTime(15_000); });
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
    });

    it('answers in the detail with Yes, No and Edit, keeps Edit wording and Forget, and says when later', async () => {
        render(<MemoryPage devToolsUnlocked initialSelectedId="fresh" />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        expect(within(detail).getByRole('button', { name: 'No' })).toBeInTheDocument();
        expect(within(detail).getByRole('button', { name: 'Edit' })).toBeInTheDocument();
        // Not mine and the old names are only on the line, never in the detail.
        expect(within(detail).queryByRole('button', { name: 'Not mine' })).not.toBeInTheDocument();
        expect(within(detail).queryByRole('button', { name: 'That’s me' })).not.toBeInTheDocument();
        expect(within(detail).queryByRole('button', { name: 'Not quite' })).not.toBeInTheDocument();
        fireEvent.click(await within(detail).findByRole('button', { name: 'Yes' }));
        expect(await within(detail).findByText('Confirmed. Isoform treats this as fact now.')).toBeInTheDocument();
        expect(within(detail).getByRole('button', { name: 'Undo' })).toBeInTheDocument();
        expect(within(detail).getByRole('button', { name: 'Edit wording' })).toBeInTheDocument();
        expect(within(detail).getByRole('button', { name: 'Forget this' })).toBeInTheDocument();
        expect(within(detail).queryByRole('button', { name: 'No' })).not.toBeInTheDocument();
        cleanup();
        pageQueryCache.clear();

        // Later: a confirmed belief says when, and offers No.
        mocks.getAbout.mockImplementation(async () => aboutFor([entry, fresh], { status: 'confirmed', confirmedAt: Date.now() }));
        render(<MemoryPage devToolsUnlocked initialSelectedId="fresh" />);
        let later = await screen.findByRole('complementary', { name: 'Belief detail' });
        expect(await within(later).findByText('You confirmed this today.')).toBeInTheDocument();
        expect(within(later).getByRole('button', { name: 'No' })).toBeInTheDocument();
        expect(within(later).queryByRole('button', { name: 'Yes' })).not.toBeInTheDocument();
        cleanup();
        pageQueryCache.clear();

        // A No belief says Isoform isn't using it, and offers Yes.
        mocks.getAbout.mockImplementation(async () => aboutFor([entry, fresh], { status: 'not_quite', notQuiteAt: Date.now() - 86_400_000 }));
        render(<MemoryPage devToolsUnlocked initialSelectedId="fresh" />);
        later = await screen.findByRole('complementary', { name: 'Belief detail' });
        expect(await within(later).findByText('You said not quite yesterday. Isoform isn’t using it.')).toBeInTheDocument();
        fireEvent.click(within(later).getByRole('button', { name: 'Yes' }));
        await waitFor(() => expect(mocks.confirm).toHaveBeenCalledWith('fresh'));
        // Undo puts No back.
        mocks.notQuite.mockResolvedValue(0);
        fireEvent.click(await within(later).findByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(mocks.notQuite).toHaveBeenCalledWith('fresh'));
        expect(mocks.undoConfirm).toHaveBeenCalledWith('fresh');
    });

    it('edits from the detail with the same Edit field, saving the words', async () => {
        mocks.rewordEntry.mockImplementation(async (id: string, content: string) => ({ ...fresh, id, content, source: 'user_edit' }));
        render(<MemoryPage devToolsUnlocked initialSelectedId="fresh" />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        fireEvent.click(within(detail).getByRole('button', { name: 'Edit' }));
        const field = within(detail).getByRole('textbox', { name: 'Editing' });
        expect(field).toHaveValue('You keep calls short before 11 AM.');
        fireEvent.change(field, { target: { value: 'You keep calls short before noon.' } });
        fireEvent.click(within(detail).getByRole('button', { name: 'Save' }));
        await waitFor(() => expect(mocks.rewordEntry).toHaveBeenCalledWith('fresh', fresh.content.replace('You keep calls short before 11 AM.', 'You keep calls short before noon.'), 'You keep calls short before noon.'));
        expect(await within(detail).findByText('Corrected. Isoform will use your wording.')).toBeInTheDocument();
    });

    it('keeps Yes in the detail when the About you read fails, and hides it for a confirmed file', async () => {
        mocks.getAbout.mockRejectedValue(new Error('store'));
        const confirmedFile = { ...entry, content: '---\nconfirmed_at: 2026-10-22T09:00:00Z\n---\n' + entry.content };
        resetDefaults([confirmedFile, fresh]);
        mocks.getAbout.mockRejectedValue(new Error('store'));
        const { unmount } = render(<MemoryPage devToolsUnlocked initialSelectedId="fresh" />);
        let detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        fireEvent.click(within(detail).getByRole('button', { name: 'Yes' }));
        await waitFor(() => expect(mocks.confirm).toHaveBeenCalledWith('fresh'));
        unmount();
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        await within(detail).findByRole('button', { name: 'Edit wording' });
        expect(within(detail).queryByRole('button', { name: 'Yes' })).not.toBeInTheDocument();
    });

    it('confirms from the detail with Yes too', async () => {
        render(<MemoryPage devToolsUnlocked initialSelectedId="fresh" />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        fireEvent.click(await within(detail).findByRole('button', { name: 'Yes' }));
        await waitFor(() => expect(mocks.confirm).toHaveBeenCalledWith('fresh'));
    });
});

describe('About you communication', () => {
    const communicationEntry = {
        ...entry,
        id: 'communication',
        title: 'Language with Jia',
        apps: ['WeChat', 'Mail'],
        activitySessionIds: ['private-session'],
        content: '# Language with Jia\n- You write to Jia in Chinese.\n',
    };
    const example = {
        id: 'draft-1',
        to: '加一',
        subject: '',
        text: '加一，周五前给你确认。',
        app: 'WeChat',
        recipientName: '加一',
        language: 'Chinese',
        sentences: 2,
        greeting: false,
        beliefIds: ['another-belief', 'communication'],
        createdAt: Date.now(),
    };

    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([communicationEntry]);
        mocks.getAbout.mockImplementation(async () => aboutFor([communicationEntry], {
            area: 'communication',
            sentMessageCount: 14,
            evidenceFirstAt: new Date(2026, 8, 30).getTime(),
            evidenceLastAt: Date.now(),
        }));
        mocks.getEvidence.mockResolvedValue([{ at: Date.now(), durationMs: 0, appName: 'WeChat', text: 'Sent a reply.', activitySessionId: 'private-session' }]);
        mocks.confirm.mockResolvedValue(communicationEntry);
    });

    it('orders every fixed area, hides empty groups and puts unknown or missing areas in Other', async () => {
        const areas = ['communication', 'work', 'people', 'tools', 'routines', 'interests', 'other'];
        const entries = areas.map((_, index) => ({
            ...entry,
            id: `area-${index}`,
            title: `Belief ${index}`,
            content: `# Belief ${index}\n- Fact ${index}.\n`,
        }));
        entries.push({ ...entry, id: 'area-unknown', title: 'Unknown area', content: '# Unknown area\n- Another fact.\n' });
        const assigned = ['interests', 'other', 'communication', 'unknown-area', 'tools', 'work', 'people', 'routines'];
        resetDefaults(entries);
        mocks.getAbout.mockResolvedValue({
            ...aboutFor(entries),
            beliefs: aboutFor(entries).beliefs.map((belief, index) => ({ ...belief, area: assigned[index] })),
        });
        render(<MemoryPage devToolsUnlocked />);
        const groups = await screen.findByTestId('belief-groups');
        const headings = within(groups).getAllByRole('heading', { level: 2 });
        expect(headings.map((heading) => heading.textContent)).toEqual([
            'How you communicate1', 'Work1', 'People1', 'Tools and apps1', 'Routines1', 'Interests1', 'Other2',
        ]);
        expect(within(groups).getByText('Learned only from messages you sent. Drafts follow these.')).toBeInTheDocument();
        // Every claim is one line; kind labels went with the card grid.
        expect(within(groups).getAllByRole('article')).toHaveLength(8);
        expect(within(groups).queryByText('Something I learned about you')).not.toBeInTheDocument();
        expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument();
    });

    it('shows sent-message evidence on To check and confirms that drafts will follow it', async () => {
        mocks.getAbout.mockImplementation(async () => aboutFor([communicationEntry], {
            newThisWeek: ['communication'],
            area: 'communication',
            sentMessageCount: 14,
            evidenceFirstAt: new Date(2026, 8, 30).getTime(),
            evidenceLastAt: Date.now(),
        }));
        render(<MemoryPage devToolsUnlocked />);
        const toCheck = await screen.findByRole('region', { name: 'To check' });
        const article = within(toCheck).getByRole('article', { name: /You write to Jia in Chinese\./ });
        expect(article).toHaveTextContent('How you communicate');
        expect(article).toHaveTextContent('14 messages you sent');
        expect(article).toHaveTextContent('Sep 30, 2026 to today');
        expect(article).toHaveTextContent('WeChat and Mail');
        fireEvent.click(within(article).getByRole('button', { name: 'Yes' }));
        expect(await within(article).findByText('Confirmed. Drafts will follow this.')).toBeInTheDocument();
        expect(mocks.confirm).toHaveBeenCalledWith('communication');
    });

    it('omits the reason heading for a communication belief without specific evidence', async () => {
        mocks.getEvidence.mockResolvedValue([]);
        render(<MemoryPage devToolsUnlocked initialSelectedId="communication" />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        await waitFor(() => expect(mocks.getEvidence).toHaveBeenCalled());
        await waitFor(() => expect(within(detail).queryByRole('heading', { name: 'Why Isoform believes this' })).not.toBeInTheDocument());
    });

    it('shows sent evidence and draft examples, hides the Activity jump and keeps Change on the inspected belief when another ID comes first', async () => {
        mocks.getExamples.mockResolvedValue([example]);
        render(<MemoryPage devToolsUnlocked initialSelectedId="communication" onOpenActivitySession={vi.fn()} />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        expect(within(detail).getByText('How you communicate')).toBeInTheDocument();
        expect(await within(detail).findByText('From your WeChat: Sent a reply.')).toBeInTheDocument();
        expect(within(detail).queryByText(/never guesses your tone/)).not.toBeInTheDocument();
        expect(within(detail).queryByRole('button', { name: 'See these moments in Activity' })).not.toBeInTheDocument();
        expect(within(detail).queryByRole('button', { name: 'Open where this was learned in Activity' })).not.toBeInTheDocument();
        expect(await within(detail).findByText('How a draft shows it')).toBeInTheDocument();
        expect(within(detail).getByText('To 加一 · WeChat')).toBeInTheDocument();
        expect(within(detail).getByText(example.text)).toBeInTheDocument();
        expect(within(detail).getByText('Written the way you write to 加一: Chinese, 2 sentences, no greeting.')).toBeInTheDocument();
        fireEvent.click(within(detail).getByRole('button', { name: 'Change' }));
        expect(within(screen.getByRole('complementary', { name: 'Belief detail' })).getByRole('heading', { name: 'Language with Jia' })).toBeInTheDocument();
        expect(mocks.getExamples).toHaveBeenCalledWith('communication');
    });

    it('keeps draft examples loading, offers retry after failure and then renders the example', async () => {
        let reject!: (reason: unknown) => void;
        mocks.getExamples.mockReturnValueOnce(new Promise((_, fail) => { reject = fail; }));
        render(<MemoryPage devToolsUnlocked initialSelectedId="communication" />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        expect(within(detail).getByText('How a draft shows it')).toBeInTheDocument();
        expect(within(detail).getByText('How a draft shows it').parentElement!.querySelector('[aria-busy="true"]')).not.toBeNull();
        await act(async () => reject(new Error('unavailable')));
        const alert = await within(detail).findByRole('alert');
        expect(alert).toHaveTextContent('Could not load drafts that used this belief.');
        mocks.getExamples.mockResolvedValue([example]);
        fireEvent.click(within(alert).getByRole('button', { name: 'Try again' }));
        expect(await within(detail).findByText(example.text)).toBeInTheDocument();
        expect(mocks.getExamples).toHaveBeenCalledTimes(2);
    });

    it.each(['memory:withdrawn', 'closed-eyes:changed'])('reloads draft examples after %s and hides a withdrawn example', async (eventName) => {
        mocks.getExamples.mockResolvedValue([example]);
        render(<MemoryPage devToolsUnlocked initialSelectedId="communication" />);
        expect(await screen.findByText(example.text)).toBeInTheDocument();
        expect(mocks.getExamples).toHaveBeenCalledTimes(1);
        mocks.getExamples.mockResolvedValue([]);
        await act(async () => {
            for (const listener of [...(mocks.events.get(eventName) ?? [])]) listener(undefined);
        });
        expect(screen.queryByText(example.text)).not.toBeInTheDocument();
        const readsBeforeReopen = mocks.getExamples.mock.calls.length;
        fireEvent.click(await within(screen.getByTestId('belief-groups')).findByRole('button', { name: 'You write to Jia in Chinese.' }));
        await waitFor(() => expect(mocks.getExamples.mock.calls.length).toBeGreaterThan(readsBeforeReopen));
        await waitFor(() => expect(screen.queryByText('How a draft shows it')).not.toBeInTheDocument());
        expect(screen.queryByText(example.text)).not.toBeInTheDocument();
    });

    it('does not restore a stale example response after a newer withdrawal read', async () => {
        let resolveFirst!: (rows: Array<typeof example>) => void;
        mocks.getExamples.mockReturnValueOnce(new Promise((resolve) => { resolveFirst = resolve; })).mockResolvedValue([]);
        render(<MemoryPage devToolsUnlocked initialSelectedId="communication" />);
        await waitFor(() => expect(mocks.getExamples).toHaveBeenCalledTimes(1));
        await act(async () => {
            for (const listener of [...(mocks.events.get('memory:withdrawn') ?? [])]) listener(undefined);
        });
        const readsBeforeReopen = mocks.getExamples.mock.calls.length;
        fireEvent.click(await within(screen.getByTestId('belief-groups')).findByRole('button', { name: 'You write to Jia in Chinese.' }));
        await waitFor(() => expect(mocks.getExamples.mock.calls.length).toBeGreaterThan(readsBeforeReopen));
        await waitFor(() => expect(screen.queryByText('How a draft shows it')).not.toBeInTheDocument());
        await act(async () => resolveFirst([example]));
        expect(screen.queryByText(example.text)).not.toBeInTheDocument();
        expect(screen.queryByText('How a draft shows it')).not.toBeInTheDocument();
    });

    it('hides How a draft shows it when there are no examples and never reads examples for other areas', async () => {
        const { unmount } = render(<MemoryPage devToolsUnlocked initialSelectedId="communication" />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        await waitFor(() => expect(mocks.getExamples).toHaveBeenCalledWith('communication'));
        expect(within(detail).queryByText('How a draft shows it')).not.toBeInTheDocument();
        unmount();

        resetPageQueries();
        resetDefaults([entry]);
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        await screen.findByRole('complementary', { name: 'Belief detail' });
        expect(mocks.getExamples).toHaveBeenCalledTimes(1);
        expect(screen.queryByText('How a draft shows it')).not.toBeInTheDocument();
    });

    it('keeps communication locale keys and the exact English concept note in parity', () => {
        expect(Object.keys(zh.aboutYou.areas)).toEqual(Object.keys(en.aboutYou.areas));
        expect(Object.keys(zh.aboutYou.communication)).toEqual(Object.keys(en.aboutYou.communication));
        expect(Object.keys(zh.aboutYou.communication.languages)).toEqual(Object.keys(en.aboutYou.communication.languages));
        expect(en.aboutYou.communication.note).toBe('Learned only from messages you sent. Drafts follow these.');
        expect(en.aboutYou.review.communicationConfirmed).toBe('Confirmed. Drafts will follow this.');
        expect(zh.aboutYou.review).toHaveProperty('communicationWhere');
        expect(zh.aboutYou.review).toHaveProperty('communicationConfirmed');
    });
});

describe('Decision 11: everything the Memory page had stays reachable', () => {
    const versioned = { ...entry, hasHistory: true, source: 'user_edit', activitySessionIds: ['s-1'] };
    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([versioned]);
        mocks.getEvidence.mockResolvedValue([{ at: Date.now(), durationMs: 0, appName: 'Mail', text: 'Wrote a detailed reply.', activitySessionId: 's-1' }]);
        mocks.getPreview.mockResolvedValue(preview());
        mocks.listVersions.mockResolvedValue([{ timestampMs: Date.now() - 86400000, sizeBytes: 100 }]);
        mocks.getVersion.mockResolvedValue('# Writing style\n- **Dimension**: user_insight\n\nYou prefer short replies.\n');
    });

    // Design answer A7: About you has no search box and no kind filter (Find's Claims group finds a claim).
    it('builds no search box or kind filter, and keeps the source in Technical details', async () => {
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        expect(screen.queryByRole('textbox', { name: 'Search what Isoform believes' })).toBeNull();
        expect(screen.queryByRole('searchbox')).toBeNull();
        expect(screen.queryByRole('combobox')).toBeNull();
        expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument();
        expect(mocks.search).not.toHaveBeenCalled();
        fireEvent.click(within(detail).getByRole('button', { name: 'Technical details' }));
        expect(within(detail).getByText('User edited')).toBeInTheDocument();
    });

    it.each([
        ['Add a note about you', 'Add a note about you'],
        ['Memory Dream', 'Dream dialog open'],
        ['Translate memories', 'Translate memories'],
    ])('opens %s from the page More menu', async (item, opened) => {
        render(<MemoryPage devToolsUnlocked />);
        await openPageMenu();
        fireEvent.click(await screen.findByRole('menuitem', { name: item }));
        if (opened === 'Add a note about you' || opened === 'Translate memories') {
            expect(await screen.findByRole('dialog', { name: opened })).toBeInTheDocument();
        } else {
            expect(await screen.findByText(opened)).toBeInTheDocument();
        }
    });

    // About you v1.0.0 Y26: Translate all keeps its result in its dialog, never a toast.
    it('keeps the Translate all result in its dialog, with Try again when some fail', async () => {
        const done = async (payload: unknown) => {
            await act(async () => {
                for (const listener of [...(mocks.events.get('global-translation:done') ?? [])]) await listener(payload);
            });
        };
        mocks.translateAll.mockResolvedValue({ total: 2, targetLanguage: 'English' });
        render(<MemoryPage devToolsUnlocked />);
        await openPageMenu();
        fireEvent.click(await screen.findByRole('menuitem', { name: 'Translate memories' }));
        const dialog = await screen.findByRole('dialog', { name: 'Translate memories' });
        expect(dialog).toHaveTextContent('Translates every belief. Each keeps its earlier version.');
        await act(async () => { fireEvent.click(within(dialog).getByRole('button', { name: en['globalTranslate']['confirm'] })); });
        expect(mocks.translateAll).toHaveBeenCalledOnce();
        expect(screen.getByRole('dialog', { name: 'Translate memories' })).toBeInTheDocument();
        await done({ scope: 'memory', summary: { translated: 1, failed: 1, total: 2, targetLanguage: 'English' } });
        const failed = within(dialog).getByText("Some couldn't be translated.").closest('[role="alert"]');
        expect(failed).toHaveClass('text-copper');
        await act(async () => { fireEvent.click(within(dialog).getByRole('button', { name: 'Try again' })); });
        expect(mocks.translateAll).toHaveBeenCalledTimes(2);
        await done({ scope: 'memory', summary: { translated: 2, failed: 0, total: 2, targetLanguage: 'English' } });
        expect(within(dialog).getByText('Translated into English.')).toBeInTheDocument();
        expect(mocks.notify).not.toHaveBeenCalled();
    });

    it('re-indexes from the page More menu', async () => {
        render(<MemoryPage devToolsUnlocked />);
        await openPageMenu();
        mocks.sync.mockResolvedValue(undefined);
        fireEvent.click(await screen.findByRole('menuitem', { name: 'Re-index memory files' }));
        await waitFor(() => expect(mocks.sync).toHaveBeenCalledOnce());
    });

    // About you v1.0.0 Y4: Dream, Re-index, Copy memory ID and Technical
    // details are for developer tools only.
    it('keeps developer items out of About you until developer tools are on', async () => {
        render(<MemoryPage />);
        await openPageMenu();
        expect(await screen.findByRole('menuitem', { name: 'Add a note about you' })).toBeInTheDocument();
        // Import moved out of the menu to the link under To check.
        expect(screen.queryByRole('menuitem', { name: 'Import from AI tools' })).not.toBeInTheDocument();
        expect(screen.queryByRole('menuitem', { name: 'Memory Dream' })).not.toBeInTheDocument();
        expect(screen.queryByRole('menuitem', { name: 'Re-index memory files' })).not.toBeInTheDocument();
    });

    it('keeps Copy memory ID and Technical details out of the detail until developer tools are on', async () => {
        render(<MemoryPage initialSelectedId={entry.id} />);
        await screen.findByRole('heading', { name: entry.title });
        expect(screen.queryByRole('button', { name: 'Technical details' })).not.toBeInTheDocument();
        await openDetailMenu();
        await screen.findAllByRole('menuitem');
        expect(screen.queryByRole('menuitem', { name: 'Copy memory ID' })).not.toBeInTheDocument();
    });

    it('keeps copy ID, version history with Compare, translate and Wrong subject in the detail More menu', async () => {
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        await screen.findByRole('heading', { level: 2, name: entry.title });
        await openDetailMenu();
        expect(await screen.findByRole('menuitem', { name: 'Copy memory ID' })).toBeInTheDocument();
        expect(screen.getByRole('menuitem', { name: 'Translate' })).toBeInTheDocument();
        expect(screen.getByRole('menuitem', { name: 'Wrong subject' })).toBeInTheDocument();
        fireEvent.click(screen.getByRole('menuitem', { name: 'Version history' }));
        const history = await screen.findByRole('dialog', { name: 'Version history' });
        await waitFor(() => expect(within(history).getByRole('button', { name: 'Compare' })).toBeEnabled());
        fireEvent.click(within(history).getByRole('button', { name: 'Compare' }));
        expect(await within(history).findByText('Diff')).toBeInTheDocument();
    });

    it('keeps Wrong subject from the menu, per-session Not right, sentence edits and the Forget preview counts', async () => {
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} />);
        await screen.findByRole('heading', { level: 2, name: entry.title });
        await openDetailMenu();
        fireEvent.click(await screen.findByRole('menuitem', { name: 'Wrong subject' }));
        expect(await screen.findByRole('dialog', { name: 'Wrong subject?' })).toBeInTheDocument();
        // About you v1.0.0 Y24: plain words, no forget-scope internals.
        expect(screen.getByRole('dialog', { name: 'Wrong subject?' })).toHaveTextContent(/Tell Isoform who ".+" is really about, or forget it\. Your reason isn't saved\./);
        fireEvent.click(within(screen.getByRole('dialog', { name: 'Wrong subject?' })).getByRole('button', { name: 'Cancel' }));
        fireEvent.click(await screen.findByRole('button', { name: 'Not right' }));
        expect(await screen.findByRole('dialog', { name: 'Wrong subject?' })).toBeInTheDocument();
        fireEvent.click(within(screen.getByRole('dialog', { name: 'Wrong subject?' })).getByRole('button', { name: 'Cancel' }));
        fireEvent.click(within(screen.getByRole('complementary', { name: 'Belief detail' })).getByRole('button', { name: 'You prefer detailed replies.' }));
        expect(screen.getByRole('textbox', { name: 'Memory point' })).toHaveValue('You prefer detailed replies.');
        fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
        fireEvent.click(screen.getByRole('button', { name: 'Forget this' }));
        const dialog = await screen.findByRole('dialog', { name: 'Forget this?' });
        expect(await within(dialog).findByText('Stored search embeddings: 3. Saved versions: 2.')).toBeInTheDocument();
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
    });
});


describe('MemoryPage with the page query cache', () => {
    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([entry]);
    });

    it('paints the cached beliefs and portrait at once on a warm reopen, without the skeleton', async () => {
        pageQueryCache.set('GetMemoryEntries', [], [entry]);
        pageQueryCache.set('GetAboutYou', [], aboutFor([entry]));
        pageQueryCache.set('GetMemoryStatus', [], { files: 1, chunks: 3, vectorReady: true });
        pageQueryCache.set('GetAboutYouPortrait', [], readyPortrait([{ text: 'You want detailed replies', memoryIds: [entry.id] }]));
        // A warm reopen has the goal lists cached too; the Goals group paints them at once.
        pageQueryCache.set('ListGoals', [], { tracking: [], done: [], held: [] });
        const { container } = render(<MemoryPage devToolsUnlocked />);
        expect(screen.getByRole('button', { name: 'You prefer detailed replies.' })).toBeInTheDocument();
        expect(screen.getByTestId('in-one-line')).toHaveTextContent('You want detailed replies');
        expect(container.querySelector('[aria-busy="true"]')).toBeNull();
        await act(async () => {});
    });

    it('shows the skeleton on a true first load', () => {
        mocks.getEntries.mockReturnValue(new Promise(() => undefined));
        const { container } = render(<MemoryPage devToolsUnlocked />);
        expect(container.querySelector('[aria-busy="true"]')).not.toBeNull();
        expect(screen.queryByRole('button', { name: /You prefer detailed replies/ })).toBeNull();
    });

    it('About you v1.0.0 Y12: on a cold open the portrait paints in the same frame as the beliefs', async () => {
        let resolvePortrait!: (value: unknown) => void;
        mocks.getEntries.mockResolvedValue([entry]);
        mocks.getPortrait.mockReturnValue(new Promise((resolve) => { resolvePortrait = resolve; }));
        const { container } = render(<MemoryPage devToolsUnlocked />);
        await act(async () => {});
        expect(container.querySelector('[aria-busy="true"]')).not.toBeNull();
        expect(screen.queryByRole('button', { name: /You prefer detailed replies/ })).toBeNull();
        await act(async () => {
            resolvePortrait(readyPortrait([{ text: 'You want detailed replies', memoryIds: [entry.id] }]));
        });
        expect(screen.getByRole('button', { name: 'You prefer detailed replies.' })).toBeInTheDocument();
        expect(await screen.findByTestId('in-one-line')).toHaveTextContent('You want detailed replies');
        expect(container.querySelector('[aria-busy="true"]')).toBeNull();
    });

    function dayOne() {
        const second = { ...entry, id: 'calendar', title: 'You put every plan on the calendar' };
        mocks.getEntries.mockResolvedValue([entry, second]);
        mocks.getAbout.mockImplementation(async () => {
            const about = aboutFor([entry, second]);
            return { ...about, beliefs: about.beliefs.map((belief) => ({ ...belief, source: 'assistant-history' })) };
        });
        pageQueryCache.set('GetFirstWeekDay', [], 0);
        pageQueryCache.set('GetAssistantHistory', [], { enabled: true, state: 'ready', assistants: ['claude'], projects: [], beliefCount: 2, knowHowCount: 0 });
    }

    it('About you v1.0.0 Y2: on day one Check these first is the first section, and its line never says three', async () => {
        dayOne();
        const { container } = render(<MemoryPage />);
        const heading = await screen.findByRole('heading', { level: 2, name: 'Check these first' });
        expect(screen.getByText('From your Claude Code history')).toBeInTheDocument();
        expect(container.textContent).not.toMatch(/three/i);
        // In one line waits for a written portrait, so Check these first leads.
        expect(screen.queryByTestId('in-one-line')).toBeNull();
        expect(screen.getAllByRole('heading', { level: 2 })[0]).toBe(heading);
        expect(screen.getByText(/^2 beliefs from your Claude Code history\. You haven't confirmed any yet\. Stored on your computer\./)).toBeInTheDocument();
    });

    it('About you v1.0.0 Y2: the zh-CN day-one line joins its sentences without a space', async () => {
        await i18n.changeLanguage('zh-CN');
        dayOne();
        render(<MemoryPage />);
        expect(await screen.findByText('先确认这几条')).toBeInTheDocument();
        expect(screen.getByText(/你还没有确认任何一条。存储在你的电脑上。/)).toBeInTheDocument();
    });

    it('drops every cached About you read on the privacy invalidation', () => {
        pageQueryCache.set('GetMemoryEntries', [], [entry]);
        pageQueryCache.set('GetAboutYou', [], aboutFor([entry]));
        pageQueryCache.set('GetMemoryStatus', [], { files: 1, chunks: 3, vectorReady: true });
        pageQueryCache.set('GetAboutYouPortrait', [], learningPortrait());
        pageQueryCache.invalidate(['GetMemoryEntries', 'GetAboutYou', 'GetMemoryStatus', 'GetAboutYouPortrait']);
        for (const name of ['GetMemoryEntries', 'GetAboutYou', 'GetMemoryStatus', 'GetAboutYouPortrait']) {
            expect(pageQueryCache.peek(name)).toBeUndefined();
        }
    });

    it('a privacy sweep withdraws the hidden retained page — beliefs and its one line — before the re-read lands, then a permitted read recovers', async () => {
        mocks.getPortrait.mockResolvedValue(readyPortrait([{ text: 'You want detailed replies', memoryIds: [entry.id] }]));
        const view = render(<MemoryPage devToolsUnlocked active={false} />);
        expect(await screen.findByRole('button', { name: 'You prefer detailed replies.' })).toBeInTheDocument();
        expect(await screen.findByTestId('in-one-line')).toHaveTextContent('You want detailed replies');

        // The sweep's re-read hangs: nothing may paint pre-transition text.
        mocks.getEntries.mockImplementation(() => new Promise(() => {}));
        mocks.getAbout.mockImplementation(() => new Promise(() => {}));
        mocks.getStatus.mockImplementation(() => new Promise(() => {}));
        mocks.getPortrait.mockImplementation(() => new Promise(() => {}));
        await emitEvent('memory:withdrawn');
        expect(screen.queryByRole('button', { name: 'You prefer detailed replies.' })).not.toBeInTheDocument();
        expect(screen.queryByTestId('in-one-line')).toBeNull();
        expect(document.body).not.toHaveTextContent('You want detailed replies');

        // Reopening while the reads are still held shows the skeleton, never
        // the withdrawn beliefs.
        view.rerender(<MemoryPage devToolsUnlocked active={true} />);
        expect(screen.queryByRole('button', { name: 'You prefer detailed replies.' })).not.toBeInTheDocument();

        // A later ordinary change re-reads: what is still permitted returns.
        resetDefaults([entry]);
        await emitEvent('memory:withdrawn');
        expect(await screen.findByRole('button', { name: 'You prefer detailed replies.' })).toBeInTheDocument();
    });

    it('a privacy sweep with failing reads shows the retry, never the withdrawn beliefs', async () => {
        render(<MemoryPage devToolsUnlocked />);
        expect(await screen.findByRole('button', { name: 'You prefer detailed replies.' })).toBeInTheDocument();
        mocks.getEntries.mockRejectedValue(new Error('gone'));
        mocks.getAbout.mockRejectedValue(new Error('gone'));
        mocks.getStatus.mockRejectedValue(new Error('gone'));
        await emitEvent('closed-eyes:changed');
        expect(screen.queryByRole('button', { name: 'You prefer detailed replies.' })).not.toBeInTheDocument();
        expect(await screen.findByRole('alert')).toHaveTextContent('Could not load what Isoform believes about you.');
    });

    it('the portrait follows every privacy sweep, not just Forget', async () => {
        mocks.getPortrait.mockResolvedValue(readyPortrait([{ text: 'You want detailed replies', memoryIds: [entry.id] }]));
        render(<MemoryPage devToolsUnlocked />);
        expect(await screen.findByTestId('in-one-line')).toHaveTextContent('You want detailed replies');
        mocks.getPortrait.mockImplementation(() => new Promise(() => {}));
        mocks.getEntries.mockImplementation(() => new Promise(() => {}));
        mocks.getAbout.mockImplementation(() => new Promise(() => {}));
        mocks.getStatus.mockImplementation(() => new Promise(() => {}));
        await emitEvent('permissions:changed');
        expect(document.body).not.toHaveTextContent('You want detailed replies');
    });
});

describe('MemoryPage goals group', () => {
    const at = (day: number) => new Date(2026, 9, day, 9).toISOString();
    function goal(overrides: Partial<GoalItemView>): GoalItemView {
        return {
            id: 'g', eventId: 'e1', kind: 'goal', title: 'Goal', state: 'active', group: 'tracking', slipping: false,
            addedAt: at(20), updatedAt: at(20), lastEvent: 'discovered', origin: 'chat', ...overrides,
        };
    }
    const GOAL_PAGE: GoalsPageView = {
        tracking: [
            goal({ id: 'passport', eventId: 'e1', title: 'Renew passport', state: 'waiting', waitingOn: 'Ana', lastEvent: 'waiting' }),
            goal({ id: 'follow', eventId: 'e2', title: 'Send Dana the notes', origin: 'activity' }),
        ],
        done: [goal({ id: 'dentist', eventId: 'e3', title: 'Book the dentist', state: 'resolved', group: 'done', lastEvent: 'resolved' })],
        held: [goal({ id: 'kept', eventId: 'e4', title: 'Answer Dana about the renewal', group: 'held', origin: 'noticed' })],
    };

    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([entry]);
        mocks.getAbout.mockImplementation(async () => aboutFor([entry], { newThisWeek: [entry.id] }));
    });

    it('keeps a marked goal in place with Undo', async () => {
        mocks.listGoals.mockResolvedValue(GOAL_PAGE);
        mocks.moveGoal.mockResolvedValueOnce({ id: 'follow', eventId: 'e5', state: 'resolved' });
        render(<MemoryPage devToolsUnlocked />);
        const group = await screen.findByRole('region', { name: 'Goals' });
        const mark = await within(group).findByRole('button', { name: 'Mark done, Send Dana the notes' });
        await act(async () => { fireEvent.click(mark); });
        const row = within(group).getByText('Send Dana the notes').closest('li')!;
        expect(within(row).getByText('Marked done')).toBeInTheDocument();
        mocks.moveGoal.mockResolvedValueOnce({ id: 'follow', eventId: 'e6', state: 'active' });
        await act(async () => { fireEvent.click(within(row).getByRole('button', { name: 'Undo' })); });
        expect(mocks.moveGoal).toHaveBeenLastCalledWith(expect.objectContaining({ goalId: 'follow', action: 'restore', expectedEventId: 'e5' }));
        expect(within(group).getByRole('button', { name: 'Mark done, Send Dana the notes' })).toBeInTheDocument();
    });

    it('shows no Goals group while goals are off', async () => {
        (globalThis as Record<string, unknown>).__isoformFeatureSwitchesForTest = { automations: true, playbooks: false, proactiveVoice: true, calendarHolds: true };
        mocks.listGoals.mockResolvedValue(GOAL_PAGE);
        render(<MemoryPage devToolsUnlocked />);
        await screen.findByRole('region', { name: 'To check' });
        expect(screen.queryByRole('region', { name: 'Goals' })).toBeNull();
    });

    // computer-you v1.0.0 rule 4: new claims first, then the rest.
    it('shows To check first, then the Goals group and the belief groups', async () => {
        mocks.listGoals.mockResolvedValue(GOAL_PAGE);
        render(<MemoryPage devToolsUnlocked />);
        const goalsGroup = await screen.findByRole('region', { name: 'Goals' });
        const review = await screen.findByRole('region', { name: 'To check' });
        const groups = await screen.findByTestId('belief-groups');
        expect(review.compareDocumentPosition(goalsGroup) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(goalsGroup.compareDocumentPosition(groups) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        expect(within(goalsGroup).getByText('Renew passport')).toBeInTheDocument();
        expect(within(goalsGroup).getByText('Waiting on someone')).toHaveClass('text-copper');
        expect(within(goalsGroup).getAllByText('Tracking')).toHaveLength(2);
        expect(within(goalsGroup).getByText('Only on this computer')).toBeInTheDocument();
    });

    // About you v1.0.0 Y3: no Goals section without goals; Add a goal is in More
    // and opens a field in place, which adds the goal under Tracking at once.
    it.each([
        ['en', 'Goals', 'Add a goal', 'What are you working toward?', 'For example: Run a half marathon in March', 'Add', 'You added it today.'],
        ['zh-CN', '目标', '添加目标', '你在为什么努力？', '例如：三月跑一个半马', '添加', '你今天添加的。'],
    ])('adds a goal in place from More in %s', async (language, title, add, label, placeholder, addButton, added) => {
        await i18n.changeLanguage(language);
        mocks.listGoals.mockResolvedValue({ tracking: [], done: [], held: [] });
        mocks.createGoal.mockResolvedValueOnce({ id: 'run', eventId: 'e1', state: 'active', title: 'Run a half marathon in March' });
        render(<MemoryPage devToolsUnlocked />);
        await waitFor(() => expect(mocks.listGoals).toHaveBeenCalled());
        await screen.findByTestId('belief-groups');
        expect(screen.queryByRole('region', { name: title })).not.toBeInTheDocument();
        await openPageMenu(language === 'en' ? 'More About you actions' : '更多「关于你」操作');
        fireEvent.click(await screen.findByRole('menuitem', { name: add }));
        const field = await screen.findByRole('textbox', { name: label });
        expect(field).toHaveAttribute('placeholder', placeholder);
        fireEvent.change(field, { target: { value: 'Run a half marathon in March' } });
        await act(async () => { fireEvent.click(screen.getByRole('button', { name: addButton })); });
        const group = await screen.findByRole('region', { name: title });
        expect(within(group).getByText('Run a half marathon in March')).toBeInTheDocument();
        expect(within(group).getByText(added)).toBeInTheDocument();
        expect(mocks.createGoal).toHaveBeenCalledWith({ text: 'Run a half marathon in March', key: expect.any(String) });
        expect(mocks.notify).not.toHaveBeenCalled();
    });

    it('keeps a goal that did not save in its field, in copper, with Try again', async () => {
        mocks.listGoals.mockResolvedValue({ tracking: [], done: [], held: [] });
        mocks.createGoal.mockRejectedValueOnce(new Error('goal_unavailable'));
        render(<MemoryPage devToolsUnlocked />);
        await screen.findByTestId('belief-groups');
        await openPageMenu();
        fireEvent.click(await screen.findByRole('menuitem', { name: 'Add a goal' }));
        fireEvent.change(await screen.findByRole('textbox', { name: 'What are you working toward?' }), { target: { value: 'Learn to bake bread' } });
        await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Add' })); });
        const line = await screen.findByText("Your goal didn't save. It's still here.");
        expect(line.closest('[role="alert"]') ?? line).toHaveClass('text-copper');
        expect(screen.getByRole('textbox', { name: 'What are you working toward?' })).toHaveValue('Learn to bake bread');
        const key = mocks.createGoal.mock.calls[0][0].key;
        mocks.createGoal.mockResolvedValueOnce({ id: 'bread', eventId: 'e2', state: 'active', title: 'Learn to bake bread' });
        await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Try again' })); });
        expect(mocks.createGoal).toHaveBeenLastCalledWith({ text: 'Learn to bake bread', key });
        expect(await screen.findByText('You added it today.')).toBeInTheDocument();
    });

    it('does not offer Add a goal in More once there are goals', async () => {
        mocks.listGoals.mockResolvedValue(GOAL_PAGE);
        render(<MemoryPage devToolsUnlocked />);
        const group = await screen.findByRole('region', { name: 'Goals' });
        await within(group).findByText('Renew passport');
        await openPageMenu();
        expect(screen.queryByRole('menuitem', { name: 'Add a goal' })).not.toBeInTheDocument();
    });

    it('folds done goals under Done until they are opened', async () => {
        mocks.listGoals.mockResolvedValue(GOAL_PAGE);
        render(<MemoryPage devToolsUnlocked />);
        const group = await screen.findByRole('region', { name: 'Goals' });
        const fold = await within(group).findByRole('button', { name: 'Done' });
        expect(fold).toHaveAttribute('aria-expanded', 'false');
        expect(within(group).queryByText('Book the dentist')).not.toBeInTheDocument();
        fireEvent.click(fold);
        expect(fold).toHaveAttribute('aria-expanded', 'true');
        expect(within(group).getByText('Book the dentist')).toBeInTheDocument();
    });

    it('opens the goal Search or a notice asked for, and hands the request back', async () => {
        mocks.listGoals.mockResolvedValue(GOAL_PAGE);
        mocks.getGoal.mockResolvedValue({ goal: GOAL_PAGE.tracking[0], history: [], sources: [] });
        const onOpenGoalHandled = vi.fn();
        const onTalkAboutGoal = vi.fn();
        render(<MemoryPage devToolsUnlocked initialSelectedId={entry.id} openGoalId="passport" openGoalSignal={1}
            onOpenGoalHandled={onOpenGoalHandled} onTalkAboutGoal={onTalkAboutGoal} />);
        fireEvent.click(await screen.findByRole('button', { name: 'Talk about this goal' }));
        expect(mocks.getGoal).toHaveBeenCalledWith('passport');
        expect(onOpenGoalHandled).toHaveBeenCalledTimes(1);
        expect(onTalkAboutGoal).toHaveBeenCalledWith(GOAL_PAGE.tracking[0], "Let's talk about my goal: Renew passport");
        expect(screen.queryByRole('heading', { name: entry.title })).not.toBeInTheDocument();
    });
});

describe('About you design review v1.0.0', () => {
    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([entry]);
        mocks.getPreview.mockResolvedValue(preview());
    });

    // Y1: with no beliefs, only the header line.
    it.each([
        ['en', "Isoform hasn't seen enough to say anything about you yet. Claims show here as it sees your work, usually after a day."],
        ['zh-CN', 'Isoform 还没看到足够的内容来说说你。随着它看到你的工作，看法会出现在这里，通常需要一天左右。'],
    ])('reads one line on an empty page in %s, with no sample claims', async (language, line) => {
        await i18n.changeLanguage(language);
        resetDefaults([]);
        render(<MemoryPage />);
        expect(await screen.findByText(line)).toBeInTheDocument();
        expect(screen.queryByTestId('memory-empty-state')).not.toBeInTheDocument();
        expect(document.querySelector('[data-claim-row]')).toBeNull();
        expect(screen.queryByTestId('you-nothing-new')).toBeNull();
    });

    // computer-activity v1.1.0, 15f: every claim judged says so, once, with the count.
    it('says nothing is new when every claim is judged', async () => {
        mocks.getAbout.mockImplementation(async () => aboutFor([entry], { status: 'confirmed', confirmedAt: Date.now() }));
        render(<MemoryPage />);
        const nothing = await screen.findByTestId('you-nothing-new');
        expect(nothing).toHaveTextContent('Nothing new to check this week.');
        expect(nothing).toHaveTextContent("You've judged the 1 thing Isoform thinks about you.");
        expect(document.querySelector('[data-claim-row]')).toBeNull();
    });

    // Y8: plain words for everyone; the itemized internals only with developer tools.
    it.each([
        ['en', 'Forget this', 'Forget this?'],
        ['zh-CN', '忘记这条', '忘记这一条？'],
    ])('keeps Forget plain and its internals for developer tools in %s', async (language, action, title) => {
        await i18n.changeLanguage(language);
        render(<MemoryPage initialSelectedId={entry.id} />);
        await screen.findByRole('heading', { name: entry.title });
        fireEvent.click(screen.getByRole('button', { name: action }));
        const dialog = await screen.findByRole('dialog', { name: title });
        await waitFor(() => expect(mocks.getPreview).toHaveBeenCalled());
        expect(within(dialog).queryByTestId('forget-dev-details')).not.toBeInTheDocument();
        expect(dialog).toHaveTextContent(entry.title);
    });
});

// One card per idea while copies wait for the merge (design review v1.0.2 W40, Y33).
function copyOf(id: string, savedAt: number, text: string): Entry {
    return {
        ...entry,
        id,
        content: `# Writing style\n- **Dimension**: user_insight\n- **Source**: activity\n\n${text}\n`,
        updatedAt: savedAt,
        modTime: savedAt,
    };
}

describe('MemoryPage one card per idea', () => {
    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        mocks.getPreview.mockImplementation(async (id: string) => preview({ memoryId: id }));
        mocks.forgetEntry.mockResolvedValue(undefined);
        mocks.getEvidence.mockResolvedValue([]);
        mocks.getUses.mockResolvedValue({ total: 0, lastAt: 0, surfaces: [] });
        mocks.listVersions.mockResolvedValue([]);
        mocks.search.mockResolvedValue([]);
        mocks.getPortrait.mockResolvedValue(learningPortrait());
        mocks.getExamples.mockResolvedValue([]);
        mocks.listGoals.mockResolvedValue({ tracking: [], done: [], held: [] });
    });

    const fiveCopies = [1, 2, 3, 4, 5].map((n) => copyOf(`copy-${n}`, n * 1000, n % 2 ? 'You prefer detailed replies.' : 'you prefer detailed replies'));

    it('shows five copies of one belief as one card that says Seen 5 times', async () => {
        resetDefaults(fiveCopies);
        mocks.getAbout.mockImplementation(async () => aboutFor(fiveCopies, { newThisWeek: fiveCopies.map((row) => row.id), area: 'communication' }));
        render(<MemoryPage devToolsUnlocked />);
        const review = await screen.findByRole('region', { name: 'To check' });
        const lines = within(review).getAllByRole('article');
        expect(lines).toHaveLength(1);
        expect(lines[0]).toHaveTextContent(/Seen 5 times/);
        // The idea is checked in To check, so the rest doesn't list it again.
        const groups = await screen.findByTestId('belief-groups');
        expect(within(groups).queryAllByRole('article')).toHaveLength(0);
    });

    it('counts ideas, not copies, in the group heading', async () => {
        resetDefaults(fiveCopies);
        mocks.getAbout.mockImplementation(async () => aboutFor(fiveCopies, { area: 'communication' }));
        render(<MemoryPage devToolsUnlocked />);
        await screen.findByTestId('belief-groups');
        const heading = screen.getByRole('button', { name: /How you communicate/ });
        expect(heading).toHaveTextContent(/How you communicate\s*1$/);
    });

    it('keeps different titles as separate cards and counts each idea', async () => {
        const rows = [copyOf('short', 2000, 'You prefer short replies.'), copyOf('detailed', 1000, 'You prefer detailed replies.')];
        resetDefaults(rows);
        mocks.getAbout.mockImplementation(async () => aboutFor(rows, { newThisWeek: ['short', 'detailed'], area: 'communication' }));
        render(<MemoryPage devToolsUnlocked />);
        const review = await screen.findByRole('region', { name: 'To check' });
        const lines = within(review).getAllByRole('article');
        expect(lines).toHaveLength(2);
        for (const line of lines) expect(line).not.toHaveTextContent(/Seen 5 times/);
        cleanup();
        resetPageQueries();
        // In the rest, the group counts each idea.
        mocks.getAbout.mockImplementation(async () => aboutFor(rows, { area: 'communication' }));
        render(<MemoryPage devToolsUnlocked />);
        expect(await screen.findByRole('button', { name: /How you communicate/ })).toHaveTextContent(/How you communicate\s*2$/);
        expect(within(screen.getByTestId('belief-groups')).getAllByRole('article')).toHaveLength(2);
    });

    it('forgets every copy when the card is forgotten from its detail', async () => {
        resetDefaults(fiveCopies);
        mocks.getAbout.mockImplementation(async () => aboutFor(fiveCopies, { area: 'communication' }));
        render(<MemoryPage devToolsUnlocked initialSelectedId="copy-3" />);
        await screen.findByRole('complementary', { name: 'Belief detail' });
        fireEvent.click(screen.getByRole('button', { name: 'Forget this' }));
        const dialog = await screen.findByRole('dialog', { name: 'Forget this?' });
        await within(dialog).findByText('Detailed communication preference');
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget this' }));
        await waitFor(() => expect(mocks.forgetEntry).toHaveBeenCalledTimes(5));
        expect(mocks.forgetEntry.mock.calls.map((call) => call[0]).sort()).toEqual(fiveCopies.map((row) => row.id).sort());
    });

    it('lists every copy from the To check line before forgetting them all', async () => {
        resetDefaults(fiveCopies);
        mocks.getAbout.mockImplementation(async () => aboutFor(fiveCopies, { newThisWeek: fiveCopies.map((row) => row.id), area: 'communication' }));
        render(<MemoryPage devToolsUnlocked />);
        const review = await screen.findByRole('region', { name: 'To check' });
        fireEvent.click(within(review).getByRole('button', { name: /^Forget: / }));
        const dialog = await screen.findByRole('dialog', { name: 'Forget this?' });
        expect(within(within(dialog).getByTestId('forget-copies')).getAllByRole('listitem')).toHaveLength(4);
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
        fireEvent.click(within(dialog).getByRole('button', { name: 'Forget this' }));
        await waitFor(() => expect(mocks.forgetEntry).toHaveBeenCalledTimes(5));
        expect(mocks.forgetEntry.mock.calls.map((call) => call[0]).sort()).toEqual(fiveCopies.map((row) => row.id).sort());
    });

    it('lists the evidence of every copy in the opened card', async () => {
        resetDefaults(fiveCopies);
        mocks.getAbout.mockImplementation(async () => aboutFor(fiveCopies, { area: 'communication' }));
        mocks.getEvidence.mockImplementation(async (id: string) => [{ at: Date.now() - 3600000, durationMs: 0, appName: 'Slack', text: `Evidence seen in ${id}.`, activitySessionId: '' }]);
        render(<MemoryPage devToolsUnlocked initialSelectedId="copy-1" />);
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        await waitFor(() => expect(within(detail).getByText(/Evidence seen in copy-5/)).toBeInTheDocument());
        for (const id of ['copy-1', 'copy-2', 'copy-3', 'copy-4']) {
            expect(within(detail).getByText(new RegExp(`Evidence seen in ${id}`))).toBeInTheDocument();
        }
    });
});

// redesign-you v1.0.0: every idea on About you is one claim line, and the
// page reads In one line, To check, Confirmed, the rest by area, Not right.
describe('About you claim lines', () => {
    const fresh = { ...entry, id: 'fresh', title: 'Short calls', content: '# Short calls\n- You keep calls short before 11 AM.\n' };
    const plans = { ...entry, id: 'plans', title: 'Plans', content: '# Plans\n- You plan the week on Sunday evening.\n' };
    const beliefOf = (row: Entry, overrides: Record<string, unknown> = {}) => ({ ...aboutFor([row]).beliefs[0], ...overrides });

    beforeEach(async () => {
        vi.resetAllMocks();
        window.localStorage.clear();
        await i18n.changeLanguage('en');
        resetDefaults([entry, fresh]);
        mocks.getAbout.mockImplementation(async () => aboutFor([entry, fresh], { newThisWeek: ['fresh'] }));
        mocks.confirm.mockImplementation(async (id: string) => (id === 'fresh' ? { ...fresh } : { ...entry }));
        mocks.notQuite.mockResolvedValue(0);
        mocks.notMine.mockResolvedValue(undefined);
        mocks.undoNotQuite.mockResolvedValue(undefined);
        mocks.getWhy.mockImplementation(async (kind: string, id: string) => ({ kind, id, summary: { total: 0, bySource: {}, thisWeek: 0 }, days: [] }));
    });
    afterEach(() => vi.useRealTimers());

    async function toCheckLine() {
        const section = await screen.findByRole('region', { name: 'To check' });
        return within(section).getByRole('article', { name: /You keep calls short before 11 AM\./ });
    }
    async function restLine() {
        const groups = await screen.findByTestId('belief-groups');
        return within(groups).findByRole('article', { name: /You prefer detailed replies\./ });
    }

    it('reads every claim as Right? with Yes, No, Not mine, Edit and Why, in To check and in the rest', async () => {
        render(<MemoryPage />);
        const lines = [await toCheckLine(), await restLine()];
        for (const line of lines) {
            expect(line).toHaveAttribute('data-claim-row');
            expect(line).toHaveAttribute('tabindex', '0');
            expect(line).toHaveAttribute('aria-keyshortcuts', 'Y N M E');
            expect(line).toHaveTextContent('Right?');
            for (const name of ['Yes', 'No', 'Not mine', 'Edit', 'Why']) {
                expect(within(line).getByRole('button', { name })).toBeInTheDocument();
            }
            expect(within(line).getByRole('button', { name: /^Forget: / })).toBeInTheDocument();
        }
        // No card grid: no pressed rows, confidence bars or chips.
        expect(screen.queryByRole('button', { pressed: true })).toBeNull();
        expect(screen.queryByRole('img', { name: /^Confidence/ })).toBeNull();
        // The claim's words open its detail.
        fireEvent.click(within(lines[1]).getByRole('button', { name: 'You prefer detailed replies.' }));
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        expect(within(detail).getByRole('heading', { name: entry.title })).toBeInTheDocument();
    });

    it('moves with the arrows and answers the focused line with Y and E; M is Not mine after the 10-second Undo', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        render(<MemoryPage />);
        const line = await toCheckLine();
        const rest = await restLine();
        line.focus();
        fireEvent.keyDown(line, { key: 'ArrowDown' });
        expect(rest).toHaveFocus();
        fireEvent.keyDown(rest, { key: 'ArrowUp' });
        expect(line).toHaveFocus();
        fireEvent.keyDown(line, { key: 'y' });
        await waitFor(() => expect(mocks.confirm).toHaveBeenCalledWith('fresh'));

        fireEvent.keyDown(rest, { key: 'E' });
        const field = within(rest).getByRole('textbox', { name: 'Editing' });
        expect(field).toHaveValue('You prefer detailed replies.');
        // A key typed into the field is left alone.
        fireEvent.keyDown(field, { key: 'y' });
        expect(mocks.confirm).toHaveBeenCalledTimes(1);
        fireEvent.keyDown(field, { key: 'Escape' });
        expect(within(rest).getByRole('button', { name: 'Not mine' })).toBeInTheDocument();

        fireEvent.keyDown(rest, { key: 'm' });
        // Only what the app does: the claim goes; no path takes its moments off you yet.
        expect(within(rest).getByRole('status')).toHaveTextContent("Not yours, so it's gone.");
        expect(within(rest).getByRole('status')).not.toHaveTextContent(/moments/);
        expect(within(rest).getByRole('button', { name: 'Undo' })).toHaveFocus();
        fireEvent.click(within(rest).getByRole('button', { name: 'Undo' }));
        await act(async () => { vi.advanceTimersByTime(10_000); });
        expect(mocks.notMine).not.toHaveBeenCalled();

        fireEvent.click(within(rest).getByRole('button', { name: 'Not mine' }));
        await act(async () => { vi.advanceTimersByTime(10_000); });
        await waitFor(() => expect(mocks.notMine).toHaveBeenCalledWith(entry.id));
        expect(mocks.notMine).toHaveBeenCalledTimes(1);
        // Not mine is not a Forget: no preview, no forget.
        expect(mocks.getPreview).not.toHaveBeenCalled();
        expect(mocks.forgetEntry).not.toHaveBeenCalled();
    });

    it('says the portrait in one line, with Yes, No and Edit but no Not mine, and without its name', async () => {
        mocks.getPortrait.mockResolvedValue(readyPortrait([
            { text: 'You build ' },
            { text: 'careful systems', memoryIds: [entry.id] },
            { text: ' and keep calls short.' },
        ]));
        mocks.confirmPortrait.mockResolvedValue([entry.id]);
        render(<MemoryPage />);
        const section = await screen.findByTestId('in-one-line');
        expect(within(section).getByRole('heading', { level: 2, name: 'In one line' })).toBeInTheDocument();
        const line = within(section).getByRole('article', { name: 'You build careful systems and keep calls short.' });
        expect(line).toHaveTextContent('Right?');
        for (const name of ['Yes', 'No', 'Edit']) expect(within(line).getByRole('button', { name })).toBeInTheDocument();
        expect(within(line).queryByRole('button', { name: 'Not mine' })).toBeNull();
        expect(line).toHaveAttribute('aria-keyshortcuts', 'Y N E');
        // In one line comes before To check.
        const toCheck = await screen.findByRole('region', { name: 'To check' });
        expect(section.compareDocumentPosition(toCheck) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
        // The named portrait card is gone.
        expect(screen.queryByText(/Blueprint/)).toBeNull();
        expect(screen.queryByRole('button', { name: 'That’s me' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'Not quite' })).toBeNull();
        // M does nothing here: the portrait is about you by definition.
        line.focus();
        fireEvent.keyDown(line, { key: 'm' });
        expect(mocks.notMine).not.toHaveBeenCalled();
        expect(within(line).getByRole('button', { name: 'Yes' })).toBeInTheDocument();
        fireEvent.keyDown(line, { key: 'y' });
        await waitFor(() => expect(mocks.confirmPortrait).toHaveBeenCalledOnce());
        expect(await within(line).findByText('Confirmed. Isoform treats this as fact now.')).toBeInTheDocument();
    });

    it('edits the one line in place, saving the words as the note', async () => {
        mocks.getPortrait.mockResolvedValue(readyPortrait([{ text: 'You build careful systems.' }]));
        mocks.notQuitePortrait.mockResolvedValue(undefined);
        render(<MemoryPage />);
        const section = await screen.findByTestId('in-one-line');
        fireEvent.click(within(section).getByRole('button', { name: 'Edit' }));
        const field = within(section).getByRole('textbox', { name: 'Editing' });
        expect(field).toHaveValue('You build careful systems.');
        fireEvent.change(field, { target: { value: ' ' } });
        expect(within(section).getByRole('button', { name: 'Save' })).toBeDisabled();
        fireEvent.change(field, { target: { value: 'You build careful systems, then ship fast.' } });
        fireEvent.click(within(section).getByRole('button', { name: 'Save' }));
        await waitFor(() => expect(mocks.notQuitePortrait).toHaveBeenCalledWith('You build careful systems, then ship fast.'));
        expect(await within(section).findByText('Rewriting with your note. This version stays until then.')).toBeInTheDocument();
    });

    it('hides In one line until the portrait is written, and says so with Try again when it cannot be read', async () => {
        const { unmount } = render(<MemoryPage />);
        await toCheckLine();
        await act(async () => {});
        expect(screen.queryByTestId('in-one-line')).toBeNull();
        unmount();
        resetPageQueries();

        mocks.getPortrait.mockRejectedValue(new Error('portrait_unavailable'));
        render(<MemoryPage />);
        const failed = await screen.findByTestId('in-one-line-failed');
        expect(failed).toHaveAttribute('role', 'alert');
        expect(failed).toHaveTextContent("Couldn't load your one line.");
        mocks.getPortrait.mockResolvedValue(readyPortrait([{ text: 'You build careful systems.' }]));
        fireEvent.click(within(failed).getByRole('button', { name: 'Try again' }));
        expect(await screen.findByTestId('in-one-line')).toHaveTextContent('You build careful systems.');
    });

    it('lists Confirmed with how each was confirmed and no answer buttons, with How to work with me inside it', async () => {
        const said = { ...entry, id: 'said', title: 'Said', content: '# Said\n- You write in the morning.\n' };
        const edited = { ...entry, id: 'edited', title: 'Edited', content: '# Edited\n- You keep calls short before noon.\n' };
        const work = { ...entry, id: 'work', title: 'Work', content: '# Work\n- Send me the short version first.\n' };
        const rows = [entry, said, edited, work];
        const confirmedAt = new Date(2026, 10, 20, 12).getTime();
        const date = new Intl.DateTimeFormat('en', { month: 'short', day: 'numeric' }).format(new Date(confirmedAt));
        resetDefaults(rows);
        mocks.getAbout.mockResolvedValue({ ...aboutFor(rows), beliefs: [
            beliefOf(entry, { status: 'confirmed', confirmedAt, confirmedHow: 'confirmed', confirmedHowAt: confirmedAt }),
            beliefOf(said, { status: 'confirmed', confirmedAt: confirmedAt - 1000, confirmedHow: 'said' }),
            beliefOf(edited, { status: 'confirmed', confirmedAt: confirmedAt - 2000, confirmedHow: 'edited' }),
            beliefOf(work, { status: 'confirmed', confirmedAt }),
        ] });
        mocks.getPortrait.mockResolvedValue({
            ...readyPortrait([{ text: 'You build careful systems.' }]),
            workWithMe: [{ text: 'Send me the short version first.', memoryIds: ['work'], area: 'work', confirmed: true }],
        });
        render(<MemoryPage />);
        const section = await screen.findByTestId('about-you-confirmed');
        await waitFor(() => expect(within(section).getAllByTestId('confirmed-row')).toHaveLength(4));
        expect(within(section).getByRole('heading', { level: 2, name: 'Confirmed' })).toBeInTheDocument();
        const group = within(section).getByTestId('work-with-me');
        expect(group).toHaveTextContent('How to work with me');
        expect(group).toHaveTextContent('Send me the short version first.');
        expect(within(section).getAllByTestId('confirmed-how').map((node) => node.textContent)).toEqual([
            `You confirmed · ${date}`, `You confirmed · ${date}`, 'You said', 'You edited',
        ]);
        // Judged already: no Right?, no answers, not a claim line.
        expect(section).not.toHaveTextContent('Right?');
        for (const name of ['Yes', 'No', 'Not mine', 'Edit']) expect(within(section).queryByRole('button', { name })).toBeNull();
        expect(section.querySelector('[data-claim-row]')).toBeNull();
        // Nor are they listed again in the rest.
        expect(within(screen.getByTestId('belief-groups')).queryAllByRole('article')).toHaveLength(0);
        fireEvent.click(within(section).getByRole('button', { name: 'You write in the morning.' }));
        const detail = await screen.findByRole('complementary', { name: 'Belief detail' });
        expect(within(detail).getByRole('heading', { name: 'Said' })).toBeInTheDocument();
    });

    it('folds Not right to one line with Show, and Undo sends a claim back to be checked', async () => {
        const at = Date.now() - 86_400_000;
        const rows = [
            { ...entry, id: 'nr-a', title: 'A', content: '# A\n- You skip breakfast.\n' },
            { ...entry, id: 'nr-b', title: 'B', content: '# B\n- You run at night.\n' },
            { ...entry, id: 'nr-c', title: 'C', content: '# C\n- You dislike meetings.\n' },
        ];
        resetDefaults(rows);
        mocks.getAbout.mockResolvedValue({ ...aboutFor(rows), beliefs: rows.map((row, index) => beliefOf(row, { status: 'not_quite', notQuiteAt: at - index * 1000 })) });
        render(<MemoryPage />);
        const section = await screen.findByTestId('about-you-not-right');
        expect(within(section).getByRole('heading', { level: 2, name: 'Not right' })).toBeInTheDocument();
        expect(within(section).getByTestId('not-right-folded')).toHaveTextContent('You skip breakfast. · You run at night. · and 1 more');
        expect(within(section).queryAllByTestId('not-right-row')).toHaveLength(0);
        fireEvent.click(within(section).getByRole('button', { name: 'Show' }));
        const lines = within(section).getAllByTestId('not-right-row');
        expect(lines).toHaveLength(3);
        expect(within(lines[0]).queryByRole('button', { name: 'Yes' })).toBeNull();
        fireEvent.click(within(lines[1]).getByRole('button', { name: 'Undo' }));
        await waitFor(() => expect(mocks.undoNotQuite).toHaveBeenCalledWith('nr-b', 0));
        expect(mocks.confirm).not.toHaveBeenCalled();
    });

    // computer-activity v1.1.0 rule 5: the count is the judged ideas, not every belief.
    it('counts only judged claims when nothing is new to check', async () => {
        const rows = [entry, fresh, plans];
        resetDefaults(rows);
        mocks.getAbout.mockResolvedValue({ ...aboutFor(rows), beliefs: [
            beliefOf(entry, { status: 'confirmed', confirmedAt: Date.now() }),
            beliefOf(fresh, { status: 'not_quite', notQuiteAt: Date.now() }),
            beliefOf(plans),
        ] });
        render(<MemoryPage />);
        const nothing = await screen.findByTestId('you-nothing-new');
        await waitFor(() => expect(nothing).toHaveTextContent("You've judged all 2 things Isoform thinks about you."));
        // The unjudged claim still waits in the rest.
        expect(within(screen.getByTestId('belief-groups')).getByRole('article', { name: /You plan the week on Sunday evening\./ })).toBeInTheDocument();
    });

    it('opens the import dialog from the link under To check, or under the header when nothing is to check', async () => {
        const importLink = /^Bring in what another AI knows about you/;
        const { unmount } = render(<MemoryPage />);
        const toCheck = await screen.findByRole('region', { name: 'To check' });
        await waitFor(() => expect(within(toCheck).getByRole('button', { name: importLink })).toBeInTheDocument());
        expect(screen.getAllByRole('button', { name: importLink })).toHaveLength(1);
        fireEvent.click(within(toCheck).getByRole('button', { name: importLink }));
        expect(await screen.findByText('Import dialog open')).toBeInTheDocument();
        unmount();
        resetPageQueries();

        mocks.getAbout.mockImplementation(async () => aboutFor([entry, fresh]));
        render(<MemoryPage />);
        await screen.findByTestId('you-nothing-new');
        expect(screen.queryByRole('region', { name: 'To check' })).toBeNull();
        fireEvent.click(screen.getByRole('button', { name: importLink }));
        expect(await screen.findByText('Import dialog open')).toBeInTheDocument();
    });

    it('opens Why on a claim line and reads where that claim came from', async () => {
        render(<MemoryPage />);
        const line = await restLine();
        fireEvent.click(within(line).getByRole('button', { name: 'Why' }));
        await waitFor(() => expect(mocks.getWhy).toHaveBeenCalledWith('claim', entry.id));
        expect(await screen.findByRole('dialog')).toBeInTheDocument();
        // Why only reads: the claim isn't answered.
        expect(mocks.confirm).not.toHaveBeenCalled();
        expect(mocks.notThis).not.toHaveBeenCalled();
    });
});
