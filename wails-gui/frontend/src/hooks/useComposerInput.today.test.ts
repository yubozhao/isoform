import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { createSession, createStandaloneChatProject } from '@/lib/stream-helpers';
import i18n from '@/i18n';
import zhToday from '@/i18n/locales/zh-CN/today.json';
import type { AgentStatus, FileAttachment, ImageAttachment, Project, Session, StreamItemData } from '@/types';
import { useComposerInput } from './useComposerInput';
import type { TodayChatContextRef, TodayChatDraft, TodayChatDraftResult } from '@/lib/today-chat-navigation';
import { TODAY_DRAFT_FRESH_MS, peekTodayChatDraft, resetTodayChatDrafts, warmTodayChatDrafts } from '@/lib/today-chat-prepared';

const calls = vi.hoisted(() => ({
    today: vi.fn(async (..._args: unknown[]) => 1),
    offer: vi.fn(async (..._args: unknown[]) => 1),
    inlineError: vi.fn(),
    plain: vi.fn(async (..._args: unknown[]) => {}),
    attachments: vi.fn(async (..._args: unknown[]) => {}),
    persist: vi.fn(async (_tag: string, _item: { id: string; content?: string }) => 1),
    save: vi.fn(async () => {}),
    prepare: vi.fn(async (_tag: string, text: string, _context?: unknown) => text),
    draft: vi.fn<(_context: unknown) => Promise<TodayChatDraft>>(),
    drafts: vi.fn<(_refs: TodayChatContextRef[]) => Promise<TodayChatDraftResult[]>>(),
    structure: vi.fn(async () => {}),
    cancel: vi.fn(),
    toastError: vi.fn(),
    toast: vi.fn(),
}));
vi.mock('sonner', () => ({ toast: Object.assign(calls.toast, { error: calls.toastError }) }));
vi.mock('../../wailsjs/go/main/App', () => ({
    SendPrompt: calls.plain,
    SendPromptWithTodayContext: calls.today,
    SendPromptWithOfferReferences: calls.offer,
    PrepareTodayChatTurn: calls.prepare,
    PrepareTodayChatDraft: calls.draft,
    PrepareTodayChatDrafts: calls.drafts,
    SendPromptWithImages: vi.fn(), SendPromptWithAttachments: calls.attachments,
    CancelStream: vi.fn(), CancelByTag: calls.cancel,
    PersistStreamItem: calls.persist, SaveSessionState: calls.save,
    EnsureProjectStructure: calls.structure,
    RenameWorkspaceBranch: vi.fn(), SaveWorkspaceState: vi.fn(),
}));

const context = { sourceKind: 'brief_item', sourceId: 'purpose-launch', briefId: 'brief-1' };
const prompt = 'Help me with the launch plan.';
const authoredImages: ImageAttachment[] = [{ mediaType: 'image/png', dataBase64: 'AUTHORED_IMAGE' }];
const authoredFiles: FileAttachment[] = [{ id: 'authored-file', fileName: 'launch-notes.txt', mediaType: 'text/plain', dataBase64: 'AUTHORED_FILE', fileSize: 13, category: 'text' }];
const todayUserItem = (index = 0) => calls.today.mock.calls[index].at(-2) as StreamItemData;

function mount(streaming = false, isFirstLogin = false) {
    const project = createStandaloneChatProject('sonnet');
    const workspace = project.workspaces[0];
    const main = { ...createSession('sonnet', workspace.id, 'Isoform', { kind: 'main' }), streaming };
    workspace.sessions = [main];
    const updateProject = vi.fn();
    const onRespondToPermission = vi.fn(async () => true);
    const onDismissPlanReview = vi.fn(async () => {});
    type Props = { session: Session; hasPendingPermission?: boolean; awaitingPlanApproval?: boolean };
    return {
        main,
        project,
        updateProject,
        onRespondToPermission,
        onDismissPlanReview,
        hook: renderHook(({ session, hasPendingPermission, awaitingPlanApproval }: Props) => useComposerInput({
            activeProject: project, activeWorkspace: workspace, activeSession: session,
            updateProject, status: {} as AgentStatus, model: 'sonnet', setError: calls.inlineError,
            isFirstLogin,
            hasPendingPermission, awaitingPlanApproval, onRespondToPermission, onDismissPlanReview,
            sendPromptCombo: { meta: false, ctrl: false, alt: false, shift: false, key: 'enter' },
        }), { initialProps: { session: main } as Props }),
    };
}

describe('Today composer context', () => {
    it.each(['persistence', 'send'] as const)('retries an ordinary %s failure using the same user item and turn identity', async (failure) => {
        const { hook, project, updateProject } = mount();
        if (failure === 'persistence') calls.persist.mockRejectedValueOnce(new Error('offline'));
        else calls.plain.mockRejectedValueOnce(new Error('offline'));
        act(() => hook.result.current.setPrompt('Keep this message'));
        await act(async () => hook.result.current.sendPrompt());
        const failed = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        const item = failed.workspaces[0].sessions[0].items[0];
        expect(item).toMatchObject({ content: 'Keep this message', sendFailed: true });
        act(() => hook.result.current.retryFailedSend(item.id));
        await waitFor(() => expect(calls.plain).toHaveBeenCalledTimes(failure === 'send' ? 2 : 1));
        const afterRetry = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        expect(afterRetry.workspaces[0].sessions[0].items).toHaveLength(1);
        expect(afterRetry.workspaces[0].sessions[0].items[0]).toMatchObject({ id: item.id, sendFailed: false });
        expect(calls.persist.mock.calls.map((call) => call[1].id)).toEqual([item.id, item.id]);
        if (failure === 'send') expect(calls.plain.mock.calls[0].at(-1)).toBe(calls.plain.mock.calls[1].at(-1));
    });

    // K9: Settings › Assistant owns the model and effort. A chat that stored
    // its own model, effort, plan or thinking switch from before still sends
    // with the app's model and no per-chat switches.
    it('sends with the Settings model and no per-chat plan, thinking or effort (K9)', async () => {
        const { hook, main } = mount();
        const stored = { ...main, model: 'gpt-5.2-codex:xhigh', thinkingLevel: 'xhigh', planMode: true, thinkingEnabled: true };
        hook.rerender({ session: stored });
        act(() => hook.result.current.setPrompt('What changed?'));
        await act(async () => hook.result.current.sendPrompt());
        await waitFor(() => expect(calls.plain).toHaveBeenCalled());
        const args = calls.plain.mock.calls.at(-1)!;
        expect(args[2]).toBe('sonnet');
        expect(args.slice(5, 8)).toEqual([false, false, '']);
    });

    it('stores no plan, thinking or effort choice on a new chat (K9)', () => {
        for (const model of ['sonnet', 'gpt-5.2-codex']) {
            const session = createSession(model, 'workspace', 'Chat');
            expect(session).toMatchObject({ thinkingLevel: '', thinkingEnabled: false, planMode: false });
        }
    });

    // K1 box 1 + 3: Go keeps a send when no AI is set up; the row stays, there is no toast,
    // nothing resends, and Edit takes the words back into the box.
    it('keeps a no-AI send without a toast or a resend, and Edit returns it to the box', async () => {
        const { hook, project, updateProject } = mount();
        calls.plain.mockRejectedValueOnce(new Error('no_assistant'));
        act(() => hook.result.current.setPrompt('Plan my week'));
        await act(async () => hook.result.current.sendPrompt());
        const kept = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        const item = kept.workspaces[0].sessions[0].items[0];
        expect(item).toMatchObject({ content: 'Plan my week', sendFailed: true, sendFailedCode: 'no_assistant' });
        expect(calls.toastError).not.toHaveBeenCalled();
        expect(calls.plain).toHaveBeenCalledTimes(1);
        act(() => hook.result.current.editFailedSend(item.id));
        const edited = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        expect(edited.workspaces[0].sessions[0].items).toHaveLength(0);
        expect(hook.result.current.prompt).toBe('Plan my week');
        expect(calls.plain).toHaveBeenCalledTimes(1);
    });

    it('retains attachment data when retrying an ordinary failed send', async () => {
        const { hook, project, updateProject } = mount();
        calls.attachments.mockRejectedValueOnce(new Error('offline'));
        act(() => { hook.result.current.setPrompt('Use these notes'); hook.result.current.setFiles(authoredFiles); });
        await act(async () => hook.result.current.sendPrompt());
        const failed = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        const item = failed.workspaces[0].sessions[0].items[0];
        act(() => hook.result.current.retryFailedSend(item.id));
        await waitFor(() => expect(calls.attachments).toHaveBeenCalledTimes(2));
        expect(calls.attachments.mock.calls[1][6]).toEqual([expect.objectContaining({ file_name: 'launch-notes.txt', data_base64: 'AUTHORED_FILE' })]);
        expect(calls.persist.mock.calls.map((call) => call[1].id)).toEqual([item.id, item.id]);
    });

    it('drops failed-send retry data when its session is deleted', async () => {
        const { main, hook, project, updateProject } = mount();
        calls.plain.mockRejectedValueOnce(new Error('offline'));
        act(() => hook.result.current.setPrompt('Private draft'));
        await act(async () => hook.result.current.sendPrompt());
        const failed = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        const item = failed.workspaces[0].sessions[0].items[0];
        act(() => hook.result.current.forgetFailedSends(main.localId));
        act(() => hook.result.current.retryFailedSend(item.id));
        expect(calls.plain).toHaveBeenCalledTimes(1);
    });

    it('keeps queued messages when the current run is stopped', async () => {
        const { main, hook } = mount(true);
        act(() => hook.result.current.setPrompt('Keep this queued'));
        await act(async () => hook.result.current.sendPrompt());
        act(() => hook.result.current.handleCancel());
        await act(async () => hook.rerender({ session: { ...main, streaming: false } }));
        expect(hook.result.current.queuedMessages.map((message) => message.prompt)).toEqual(['Keep this queued']);
        expect(calls.plain).not.toHaveBeenCalled();
    });

    it('retains offer identity through edits, clearing and Undo without sending the prefill', async () => {
        const { main, hook } = mount();
        const reference = { id: 'suggestion.idea', version: 'v1' };
        act(() => hook.result.current.prefillOfferPrompt(main.localId, 'Source prompt', reference));
        expect(calls.offer).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
        act(() => hook.result.current.setPrompt('Edited source prompt'));
        act(() => hook.result.current.setPrompt(''));
        act(() => hook.result.current.setPrompt('Edited source prompt'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.offer.mock.calls.at(-1)?.[0]).toBe('Edited source prompt');
        expect(calls.offer.mock.calls.at(-1)?.at(-1)).toEqual([reference]);
        expect(calls.prepare).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.persist).not.toHaveBeenCalled();
    });

    it('preserves offer references through queue editing and restores rejected text inline', async () => {
        const { main, hook } = mount(true);
        const reference = { id: 'suggestion.idea', version: 'v1' };
        act(() => hook.result.current.prefillOfferPrompt(main.localId, 'Source prompt', reference));
        await act(async () => hook.result.current.sendPrompt());
        expect(hook.result.current.queuedMessages[0].todayContext).toMatchObject({ offerReferences: [reference] });
        act(() => hook.result.current.editQueuedMessage(0));
        act(() => hook.result.current.setPrompt('Edited queued source'));
        await act(async () => hook.rerender({ session: { ...main, streaming: false } }));
        calls.offer.mockRejectedValueOnce(new Error('TASK_OFFER_UNAVAILABLE'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.offer.mock.calls.at(-1)?.at(-1)).toEqual([reference]);
        expect(hook.result.current.prompt).toBe('Edited queued source');
        expect(calls.inlineError).toHaveBeenLastCalledWith(i18n.t('today:actions.offerUnavailable'));
        expect(calls.persist).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
    });

    it('never transfers an offer draft to another session', async () => {
        const { main, hook } = mount();
        act(() => hook.result.current.prefillOfferPrompt(main.localId, 'Source prompt', { id: 'A', version: '1' }));
        await act(async () => hook.rerender({ session: { ...main, localId: 'other-session' } }));
        expect(hook.result.current.prompt).toBe('');
        expect(calls.offer).not.toHaveBeenCalled();
    });

    it('sends a queued offer with its references and restores it on withdrawal without retrying', async () => {
        const { main, hook } = mount(true);
        const reference = { id: 'A', version: '1' };
        act(() => hook.result.current.prefillOfferPrompt(main.localId, 'Source prompt', reference));
        await act(async () => hook.result.current.sendPrompt());
        calls.offer.mockRejectedValueOnce(new Error('TASK_OFFER_UNAVAILABLE'));
        await act(async () => hook.rerender({ session: { ...main, streaming: false } }));
        await waitFor(() => expect(hook.result.current.prompt).toBe('Source prompt'));
        expect(calls.offer.mock.calls.at(-1)?.at(-1)).toEqual([reference]);
        expect(calls.inlineError).toHaveBeenLastCalledWith(i18n.t('today:actions.offerUnavailable'));
        expect(hook.result.current.queuedMessages).toEqual([]);
        expect(calls.offer).toHaveBeenCalledOnce();
        expect(calls.persist).not.toHaveBeenCalled();
    });

    it('keeps offer association after a full text replacement, but not after a completed send', async () => {
        const { main, hook } = mount();
        const reference = { id: 'A', version: '1' };
        act(() => hook.result.current.prefillOfferPrompt(main.localId, 'Source prompt', reference));
        act(() => hook.result.current.setPrompt(''));
        act(() => hook.result.current.setPrompt('Completely rewritten'));
        act(() => hook.result.current.replacePrompt('Replacement'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.offer.mock.calls.at(-1)?.at(-1)).toEqual([reference]);
        expect(calls.offer.mock.calls.at(-1)?.[0]).toBe('Replacement');
        act(() => hook.result.current.setPrompt('A new ordinary question'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.plain).toHaveBeenCalledOnce();
        expect(calls.offer).toHaveBeenCalledOnce();
    });

    beforeEach(async () => {
        await i18n.changeLanguage('en');
        vi.clearAllMocks();
        vi.restoreAllMocks();
        resetTodayChatDrafts();
        calls.prepare.mockImplementation(async (_tag: string, text: string, _context?: unknown) => text);
        calls.draft.mockResolvedValue({ prefill: { kind: 'purpose', title: 'Current launch plan' }, counts: { memories: 3, activities: 2, people: 1 }, fingerprint: 'prepared-launch-facts' });
    });

    it('prepares the current item-specific editable draft and counts without sending', async () => {
        const { main, hook } = mount();
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, 'Cached generic card prompt', context));
        expect(hook.result.current.prompt).toBe('Where do I stand on "Current launch plan" and what is the next step?');
        expect(hook.result.current.todayPreview).toEqual({ status: 'ready', counts: { memories: 3, activities: 2, people: 1 } });
        expect(calls.persist).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
        act(() => hook.result.current.setPrompt('What is the smallest next step for "Current launch plan"?'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today.mock.calls[0][0]).toBe('What is the smallest next step for "Current launch plan"?');
        expect(calls.today.mock.calls[0].at(-1)).toEqual({ ...context, generatedTitle: 'Current launch plan', generatedFingerprint: 'prepared-launch-facts' });
        expect(hook.result.current.todayPreview).toBeUndefined();
    });

    it('preserves edits while preparation is pending, and waits for counts before sending', async () => {
        const { main, hook } = mount();
        let finish!: (draft: TodayChatDraft) => void;
        calls.draft.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
        let preparing!: Promise<void>;
        act(() => { preparing = hook.result.current.prepareTodayPrompt(main.localId, prompt, context); });
        expect(hook.result.current.todayPreview).toEqual({ status: 'loading' });
        act(() => hook.result.current.setPrompt('My own question.'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare).not.toHaveBeenCalled();
        await act(async () => { finish({ prefill: { kind: 'file', title: 'Notes' }, counts: { memories: 0, activities: 0, people: 0 }, fingerprint: 'prepared-notes-facts' }); await preparing; });
        expect(hook.result.current.prompt).toBe('My own question.');
        expect(hook.result.current.todayPreview).toEqual({ status: 'ready', counts: { memories: 0, activities: 0, people: 0 } });
    });

    it('keeps prepared provenance when Undo restores edits made while preparation was pending', async () => {
        const { main, hook } = mount();
        let finish!: (draft: TodayChatDraft) => void;
        calls.draft.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
        let preparing!: Promise<void>;
        act(() => { preparing = hook.result.current.prepareTodayPrompt(main.localId, prompt, context); });
        const editedPrompt = 'What should I do next for the launch plan?';
        act(() => hook.result.current.setPrompt(editedPrompt));
        await act(async () => {
            finish({ prefill: { kind: 'purpose', title: 'Launch plan' }, counts: { memories: 1, activities: 0, people: 0 }, fingerprint: 'launch-facts' });
            await preparing;
        });
        act(() => hook.result.current.setPrompt(''));
        act(() => hook.result.current.setPrompt(editedPrompt));
        calls.prepare.mockRejectedValue(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare).toHaveBeenCalledOnce();
        expect(calls.prepare.mock.calls[0].at(-1)).toEqual({ ...context, generatedTitle: 'Launch plan', generatedFingerprint: 'launch-facts' });
        expect(calls.persist).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
        expect(hook.result.current.prompt).toBe(editedPrompt);
    });

    it('keeps newly attached images and files while applying the prepared item question', async () => {
        const { main, hook } = mount();
        let finish!: (draft: TodayChatDraft) => void;
        calls.draft.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
        let preparing!: Promise<void>;
        act(() => { preparing = hook.result.current.prepareTodayPrompt(main.localId, prompt, context); });
        act(() => {
            hook.result.current.setImages(authoredImages);
            hook.result.current.setFiles(authoredFiles);
        });
        await act(async () => {
            finish({ prefill: { kind: 'file', title: 'Launch notes' }, counts: { memories: 1, activities: 0, people: 0 }, fingerprint: 'prepared-notes-facts' });
            await preparing;
        });
        expect(hook.result.current.prompt).toBe('Walk me through "Launch notes" and what is left to do.');
        expect(hook.result.current.images).toEqual(authoredImages);
        expect(hook.result.current.files).toEqual(authoredFiles);
        expect(calls.today).not.toHaveBeenCalled();
    });

    it('carries prepared attendee names separately for send-time revalidation', async () => {
        const { main, hook } = mount();
        calls.draft.mockResolvedValueOnce({ prefill: { kind: 'meeting', title: 'Design review', people: ['Maya Chen'] }, counts: { memories: 0, activities: 0, people: 1 }, fingerprint: 'prepared-meeting-facts' });
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, prompt, context));
        expect(hook.result.current.prompt).toBe('Help me prepare for "Design review" with Maya Chen.');
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare.mock.calls[0].at(-1)).toEqual({ ...context, generatedTitle: 'Design review', generatedPeople: ['Maya Chen'], generatedFingerprint: 'prepared-meeting-facts' });
        expect(calls.today.mock.calls[0].at(-1)).toEqual({ ...context, generatedTitle: 'Design review', generatedPeople: ['Maya Chen'], generatedFingerprint: 'prepared-meeting-facts' });
    });

    it.each(['clear', 'replace', 'session', 'navigation', 'new-item'] as const)('ignores a stale prepared response after %s', async (change) => {
        const { main, hook } = mount();
        let finish!: (draft: TodayChatDraft) => void;
        calls.draft.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
        let current = true;
        let preparing!: Promise<void>;
        act(() => { preparing = hook.result.current.prepareTodayPrompt(main.localId, prompt, context, () => current); });
        if (change === 'clear') act(() => hook.result.current.setPrompt(''));
        if (change === 'replace') act(() => hook.result.current.replacePrompt('A different topic.'));
        if (change === 'session') hook.rerender({ session: { ...main, localId: 'other-session' } });
        if (change === 'navigation') current = false;
        if (change === 'new-item') await act(async () => hook.result.current.prepareTodayPrompt(main.localId, prompt, { ...context, sourceId: 'new-item' }));
        const expected = change === 'replace' ? 'A different topic.' : change === 'new-item'
            ? 'Where do I stand on "Current launch plan" and what is the next step?' : '';
        await act(async () => { finish({ prefill: { kind: 'file', title: 'Stale title' }, counts: { memories: 99, activities: 0, people: 0 }, fingerprint: 'stale-facts' }); await preparing; });
        expect(hook.result.current.prompt).toBe(expected);
        expect(hook.result.current.todayPreview).toEqual(change === 'new-item'
            ? { status: 'ready', counts: { memories: 3, activities: 2, people: 1 } } : undefined);
        expect(calls.today).not.toHaveBeenCalled();
    });

    it('attaches the draft when main-chat selection renders after navigation resolves', async () => {
        const { main, hook } = mount();
        await act(async () => hook.result.current.prepareTodayPrompt('target-main', prompt, context));
        expect(hook.result.current.todayPreview).toBeUndefined();
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
        hook.rerender({ session: { ...main, localId: 'target-main' } });
        expect(hook.result.current.prompt).toContain('Current launch plan');
        expect(hook.result.current.todayPreview?.status).toBe('ready');
    });

    it('keeps counts with an edited queued Today draft and a failed send', async () => {
        const { main, hook } = mount(true);
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, prompt, context));
        const preview = hook.result.current.todayPreview;
        await act(async () => hook.result.current.sendPrompt());
        expect(hook.result.current.todayPreview).toBeUndefined();
        expect(hook.result.current.queuedMessages[0].todayPreview).toEqual(preview);
        act(() => hook.result.current.editQueuedMessage(0));
        expect(hook.result.current.todayPreview).toEqual(preview);
        hook.rerender({ session: { ...main, streaming: false } });
        calls.prepare.mockRejectedValueOnce(new Error('Unavailable'));
        await act(async () => hook.result.current.sendPrompt());
        expect(hook.result.current.todayPreview).toEqual(preview);
        expect(hook.result.current.prompt).toContain('Current launch plan');
    });

    it('keeps the card prompt with a quiet note when the item cannot be prepared, never an empty composer', async () => {
        const { main, hook } = mount();
        calls.draft.mockRejectedValueOnce(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE'));
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, 'Help me decide what to do next about this item.', context));
        expect(hook.result.current.prompt).toBe('Help me decide what to do next about this item.');
        expect(hook.result.current.todayPreview).toEqual({ status: 'fallback' });
        expect(calls.persist).not.toHaveBeenCalled();
        // Nothing is left to refuse: the request goes as an ordinary message.
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.plain).toHaveBeenCalledOnce();
        expect(calls.plain.mock.calls[0][0]).toBe('Help me decide what to do next about this item.');
    });

    it('prefills without sending and sends the reference separately from the visible bubble once', async () => {
        const { main, hook } = mount();
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        expect(hook.result.current.prompt).toBe(prompt);
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today).toHaveBeenCalledTimes(1);
        expect(calls.today.mock.calls[0][0]).toBe(prompt);
        expect(calls.today.mock.calls[0].at(-1)).toEqual(context);
        expect(calls.persist).not.toHaveBeenCalled();
        expect(todayUserItem().content).toBe(prompt);
        expect(calls.save).not.toHaveBeenCalled();
        act(() => hook.result.current.setPrompt('A separate question.'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today).toHaveBeenCalledTimes(1);
        expect(calls.plain).toHaveBeenCalledTimes(1);
    });

    it('keeps the reference with a queued turn through editing and dispatch', async () => {
        const { main, hook } = mount(true);
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today).not.toHaveBeenCalled();
        expect(hook.result.current.queuedMessages[0].todayContext).toEqual(context);
        act(() => hook.result.current.editQueuedMessage(0));
        act(() => hook.result.current.setPrompt('Help me prioritize the launch plan.'));
        await act(async () => hook.result.current.sendPrompt());
        await act(async () => hook.rerender({ session: { ...main, streaming: false } }));
        expect(calls.today).toHaveBeenCalledTimes(1);
        expect(calls.today.mock.calls[0][0]).toBe('Help me prioritize the launch plan.');
        expect(calls.today.mock.calls[0].at(-1)).toEqual(context);
    });

    it.each([false, true])('does not attach the active Today source when editing an ordinary queued message (withdrawn=%s)', async (withdrawn) => {
        const { main, hook } = mount(true);
        const ordinaryPrompt = 'An unrelated question queued before Today.';
        act(() => hook.result.current.setPrompt(ordinaryPrompt));
        await act(async () => hook.result.current.sendPrompt());
        expect(hook.result.current.queuedMessages[0].todayContext).toBeUndefined();
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, prompt, context));
        expect(hook.result.current.todayPreview?.status).toBe('ready');
        act(() => hook.result.current.editQueuedMessage(0));
        const editedPreview = hook.result.current.todayPreview;
        hook.rerender({ session: { ...main, streaming: false } });
        if (withdrawn) calls.prepare.mockRejectedValue(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE'));
        await act(async () => hook.result.current.sendPrompt());
        expect({
            sourceReads: calls.prepare.mock.calls.length,
            persisted: calls.persist.mock.calls.map((call) => call[1].content),
            ordinary: calls.plain.mock.calls.map((call) => call[0]),
            today: calls.today.mock.calls.length,
        }).toEqual({ sourceReads: 0, persisted: [ordinaryPrompt], ordinary: [ordinaryPrompt], today: 0 });
        expect(editedPreview).toBeUndefined();
    });

    it.each(['unchanged', 'clear-undo', 'edited-undo', 'other-topic-undo', 'replacement-undo'] as const)(
        'revalidates a withdrawn source after %s restores its generated draft', async (history) => {
            const { main, hook } = mount();
            calls.draft.mockResolvedValueOnce({
                prefill: { kind: 'purpose', title: 'Withdrawn launch plan' },
                counts: { memories: 1, activities: 0, people: 0 },
                fingerprint: 'withdrawn-launch-facts',
            });
            await act(async () => hook.result.current.prepareTodayPrompt(main.localId, prompt, context));
            let restoredPrompt = hook.result.current.prompt;
            const preview = hook.result.current.todayPreview;
            if (history === 'edited-undo') {
                restoredPrompt = 'What is the next step for "Withdrawn launch plan"?';
                act(() => hook.result.current.setPrompt(restoredPrompt));
            }
            if (history === 'replacement-undo') {
                act(() => hook.result.current.replacePrompt('An unrelated suggestion.'));
            } else if (history !== 'unchanged') {
                // Native textarea Delete and Undo produce separate onChange values.
                act(() => hook.result.current.setPrompt(''));
                expect(hook.result.current.todayPreview).toBeUndefined();
                if (history === 'other-topic-undo') {
                    act(() => hook.result.current.setPrompt('A different question.'));
                    act(() => hook.result.current.setPrompt(''));
                }
            }
            if (history !== 'unchanged') act(() => hook.result.current.setPrompt(restoredPrompt));

            // The source has since been deleted or withdrawn. Neither ordinary
            // persistence nor ordinary sending may bypass its current read gate.
            calls.prepare.mockRejectedValue(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE'));
            await act(async () => hook.result.current.sendPrompt());
            expect({
                sourceReads: calls.prepare.mock.calls.length,
                persisted: calls.persist.mock.calls.map((call) => call[1].content),
                ordinary: calls.plain.mock.calls.map((call) => call[0]),
                admitted: calls.today.mock.calls.length,
            }).toEqual({ sourceReads: 1, persisted: [], ordinary: [], admitted: 0 });
            expect(calls.prepare.mock.calls[0].at(-1)).toEqual({
                ...context,
                generatedTitle: 'Withdrawn launch plan',
                generatedFingerprint: 'withdrawn-launch-facts',
            });
            expect(hook.result.current.prompt).toBe(restoredPrompt);
            expect(hook.result.current.todayPreview).toEqual(preview);
        },
    );

    it.each([false, true])('allows an unrelated authored draft after clearing a withdrawn Today draft (redo=%s)', async (redo) => {
        const { main, hook } = mount();
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, prompt, context));
        const generatedPrompt = hook.result.current.prompt;
        calls.prepare.mockRejectedValue(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE'));
        act(() => hook.result.current.setPrompt(''));
        act(() => hook.result.current.setPrompt('Another topic.'));
        if (redo) {
            act(() => hook.result.current.setPrompt(generatedPrompt));
            expect(hook.result.current.todayPreview?.status).toBe('ready');
            act(() => hook.result.current.setPrompt('Another topic.'));
        }
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.plain).toHaveBeenCalledTimes(1);
        expect(calls.persist.mock.calls[0][1].content).toBe('Another topic.');
        expect(hook.result.current.todayPreview).toBeUndefined();
    });

    it('clears the Today draft when navigating to another chat', async () => {
        const { main, hook } = mount();
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        hook.rerender({ session: { ...main, localId: 'side', kind: 'side' } });
        expect(hook.result.current.prompt).toBe('');
        act(() => hook.result.current.setPrompt('A side topic.'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.plain).toHaveBeenCalledTimes(1);
    });

    it('does not send a generated draft restored by Undo in a different chat', async () => {
        const { main, hook } = mount();
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, prompt, context));
        const generatedPrompt = hook.result.current.prompt;
        hook.rerender({ session: { ...main, localId: 'side', kind: 'side' } });
        act(() => hook.result.current.setPrompt(generatedPrompt));
        calls.prepare.mockRejectedValue(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare).not.toHaveBeenCalled();
        expect(calls.persist).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
        expect(hook.result.current.prompt).toBe(generatedPrompt);

        hook.rerender({ session: main });
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare).toHaveBeenCalledOnce();
        expect(calls.persist).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
    });

    it('replaces a Today draft without carrying its reference into another suggestion', async () => {
        const { main, hook } = mount();
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        act(() => hook.result.current.replacePrompt('A different suggestion.'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.plain).toHaveBeenCalledTimes(1);
    });

    it('does not persist or send a draft when the item fails read-time privacy validation', async () => {
        const { main, hook } = mount();
        calls.prepare.mockRejectedValueOnce(new Error('private source'));
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.persist).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
    });

    it.each(['private source', 'context deadline exceeded'])(
        'restores the authored Today prompt and attachments after Prepare fails: %s',
        async (failure) => {
            const { main, hook } = mount();
            const authoredPrompt = '  My authored launch question.\nKeep the caveats.  ';
            calls.prepare.mockRejectedValueOnce(new Error(failure));
            act(() => {
                hook.result.current.prefillTodayPrompt(main.localId, authoredPrompt, context);
                hook.result.current.setImages(authoredImages);
                hook.result.current.setFiles(authoredFiles);
            });

            await act(async () => hook.result.current.sendPrompt());

            expect(calls.persist).not.toHaveBeenCalled();
            expect(calls.today).not.toHaveBeenCalled();
            expect(hook.result.current.prompt).toBe(authoredPrompt);
            expect(hook.result.current.images).toEqual(authoredImages);
            expect(hook.result.current.files).toEqual(authoredFiles);
            expect(hook.result.current.queuedMessages).toEqual([]);
            await act(async () => hook.result.current.sendPrompt());
            expect(calls.today).toHaveBeenCalledOnce();
            expect(calls.today.mock.calls[0][0]).toBe(authoredPrompt.trim());
            expect(calls.today.mock.calls[0].at(-1)).toEqual(context);
        },
    );

    it.each(['private source', 'context deadline exceeded'])(
        'requeues a dequeued Today draft at the front after Prepare fails: %s',
        async (failure) => {
            const { main, hook } = mount(true);
            calls.prepare.mockRejectedValueOnce(new Error(failure));
            act(() => {
                hook.result.current.prefillTodayPrompt(main.localId, prompt, context);
                hook.result.current.setImages(authoredImages);
                hook.result.current.setFiles(authoredFiles);
            });
            await act(async () => hook.result.current.sendPrompt());
            act(() => hook.result.current.setPrompt('Later queued question.'));
            await act(async () => hook.result.current.sendPrompt());
            act(() => hook.result.current.setPrompt('Still composing a third question.'));

            await act(async () => hook.rerender({ session: { ...main, streaming: false } }));

            expect(calls.prepare).toHaveBeenCalledOnce();
            expect(calls.persist).not.toHaveBeenCalled();
            expect(calls.today).not.toHaveBeenCalled();
            expect(hook.result.current.prompt).toBe('Still composing a third question.');
            expect(hook.result.current.queuedMessages).toEqual([
                { prompt, images: authoredImages, files: authoredFiles, todayContext: context, awaitingReview: true },
                { prompt: 'Later queued question.', images: [], files: [], todayContext: undefined },
            ]);
            await act(async () => hook.rerender({ session: { ...main, streaming: false } }));
            expect(calls.prepare).toHaveBeenCalledOnce();
        },
    );

    it('preserves new composer edits and recovers the original Today draft when pending Prepare fails', async () => {
        const { main, hook } = mount();
        let failPrepare!: (error: Error) => void;
        calls.prepare.mockImplementationOnce(() => new Promise<string>((_resolve, reject) => { failPrepare = reject; }));
        act(() => {
            hook.result.current.prefillTodayPrompt(main.localId, prompt, context);
            hook.result.current.setImages(authoredImages);
            hook.result.current.setFiles(authoredFiles);
        });
        let sending!: Promise<void>;
        act(() => { sending = hook.result.current.sendPrompt(); });
        const newImages: ImageAttachment[] = [{ mediaType: 'image/jpeg', dataBase64: 'NEW_IMAGE' }];
        const newFiles: FileAttachment[] = [{ ...authoredFiles[0], id: 'new-file', fileName: 'new-question.txt' }];
        act(() => {
            hook.result.current.setPrompt('A newly authored question.');
            hook.result.current.setImages(newImages);
            hook.result.current.setFiles(newFiles);
        });

        await act(async () => { failPrepare(new Error('context deadline exceeded')); await sending; });

        expect(calls.persist).not.toHaveBeenCalled();
        expect(hook.result.current.prompt).toBe('A newly authored question.');
        expect(hook.result.current.images).toEqual(newImages);
        expect(hook.result.current.files).toEqual(newFiles);
        expect(hook.result.current.queuedMessages).toEqual([
            { prompt, images: authoredImages, files: authoredFiles, todayContext: context, awaitingReview: true },
        ]);
    });

    it('localizes the stable missing-context send error in Chinese after Prepare succeeded', async () => {
        await i18n.changeLanguage('zh-CN');
        const { main, hook } = mount();
        calls.today.mockRejectedValueOnce(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE'));
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));

        await act(async () => hook.result.current.sendPrompt());

        expect(calls.prepare).toHaveBeenCalledOnce();
        expect(calls.persist).not.toHaveBeenCalled();
        expect(calls.today).toHaveBeenCalledOnce();
        expect(calls.toastError).not.toHaveBeenCalled();
        expect(calls.inlineError).toHaveBeenCalledWith(zhToday.actions.contextUnavailable);
        expect(calls.toastError.mock.calls.flat().join(' ')).not.toContain('TODAY_CHAT_CONTEXT_UNAVAILABLE');
    });

    it.each([false, true])('removes the rejected optimistic turn and recovers authored content after final privacy validation fails (new edits=%s)', async (editWhileSending) => {
        const { main, hook, project, updateProject } = mount();
        let rejectSend!: (error: Error) => void;
        calls.today.mockImplementationOnce(() => new Promise<number>((_resolve, reject) => { rejectSend = reject; }));
        const authoredPrompt = '  My authored launch question.\nKeep the caveats.  ';
        act(() => {
            hook.result.current.prefillTodayPrompt(main.localId, authoredPrompt, context);
            hook.result.current.setImages(authoredImages);
            hook.result.current.setFiles(authoredFiles);
        });
        let sending!: Promise<void>;
        act(() => { sending = hook.result.current.sendPrompt(); });
        await waitFor(() => expect(calls.today).toHaveBeenCalledOnce());
        expect(calls.persist).not.toHaveBeenCalled();
        const duringSend = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        expect(duringSend.workspaces[0].sessions[0].items).toHaveLength(1);
        if (editWhileSending) {
            act(() => {
                hook.result.current.setPrompt('My next question.');
                hook.result.current.setImages([]);
                hook.result.current.setFiles([]);
            });
        }
        await act(async () => { rejectSend(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE')); await sending; });

        const afterSend = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        const session = afterSend.workspaces[0].sessions[0];
        expect(session.items).toEqual([]);
        expect(session.streamItemCount).toBe(0);
        expect(session.streaming).toBe(false);
        expect(calls.save).not.toHaveBeenCalled();
        expect(hook.result.current.prompt).toBe(editWhileSending ? 'My next question.' : authoredPrompt);
        expect(hook.result.current.images).toEqual(editWhileSending ? [] : authoredImages);
        expect(hook.result.current.files).toEqual(editWhileSending ? [] : authoredFiles);
        expect(hook.result.current.queuedMessages).toEqual(editWhileSending
            ? [{ prompt: authoredPrompt, images: authoredImages, files: authoredFiles, todayContext: context, awaitingReview: true }]
            : []);
        await act(async () => hook.rerender({ session: { ...session, streaming: false } }));
        expect(calls.today).toHaveBeenCalledOnce();
        expect(calls.plain).not.toHaveBeenCalled();

        act(() => {
            hook.result.current.replacePrompt('An ordinary next question.');
            hook.result.current.setImages([]);
            hook.result.current.setFiles([]);
        });
        await act(async () => hook.result.current.sendPrompt());
        const afterNextSend = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        const nextSession = afterNextSend.workspaces[0].sessions[0];
        expect(nextSession.items.map((item) => item.content)).toEqual(['An ordinary next question.']);
        expect(nextSession.streamItemCount).toBe(1);
        await act(async () => hook.rerender({ session: { ...nextSession, streaming: true } }));
        await act(async () => hook.rerender({ session: { ...nextSession, streaming: false } }));
        expect(calls.today).toHaveBeenCalledOnce();
        expect(calls.plain).toHaveBeenCalledOnce();

        if (editWhileSending) {
            act(() => hook.result.current.editQueuedMessage(0));
            expect(hook.result.current.queuedMessages).toEqual([]);
            expect(hook.result.current.prompt).toBe(authoredPrompt);
            await act(async () => hook.result.current.sendPrompt());
            expect(calls.today).toHaveBeenCalledTimes(2);
        }
    });

    it('passes exactly the visible user text for admission on a Today first-login send', async () => {
        const { main, hook } = mount(false, true);
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.persist).not.toHaveBeenCalled();
        expect(calls.today.mock.calls[0][0]).toBe(todayUserItem().content);
        expect(calls.today.mock.calls[0][0]).not.toContain('<onboarding-context>');
    });

    it('removes a nonadmitted optimistic turn after Stop without restoring or retrying the canceled draft', async () => {
        const { main, hook, project, updateProject } = mount();
        let rejectSend!: (error: Error) => void;
        calls.today.mockImplementationOnce(() => new Promise<number>((_resolve, reject) => { rejectSend = reject; }));
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        let sending!: Promise<void>;
        act(() => { sending = hook.result.current.sendPrompt(); });
        await waitFor(() => expect(calls.today).toHaveBeenCalledOnce());
        act(() => hook.result.current.handleCancel());
        await act(async () => { rejectSend(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE')); await sending; });
        const afterStop = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        expect(afterStop.workspaces[0].sessions[0].items).toEqual([]);
        expect(afterStop.workspaces[0].sessions[0].streamItemCount).toBe(0);
        expect(hook.result.current.prompt).toBe('');
        expect(hook.result.current.queuedMessages).toEqual([]);
        expect(calls.toastError).not.toHaveBeenCalled();
    });

    it('submits only the redacted draft returned by Go for admission', async () => {
        const { main, hook } = mount();
        calls.prepare.mockResolvedValueOnce('Help with [REDACTED:api_key].');
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.persist).not.toHaveBeenCalled();
        expect(todayUserItem().content).toBe('Help with [REDACTED:api_key].');
        expect(calls.today.mock.calls[0][0]).toBe('Help with [REDACTED:api_key].');
    });

    it('starts the Today draft without attachments from the previous composer', () => {
        const { main, hook } = mount();
        act(() => {
            hook.result.current.setImages([{ mediaType: 'image/png', dataBase64: 'AAA' }]);
            hook.result.current.setFiles([{ id: 'file', fileName: 'private.txt', mediaType: 'text/plain', fileSize: 3, category: 'text' }]);
        });
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        expect(hook.result.current.images).toEqual([]);
        expect(hook.result.current.files).toEqual([]);
    });

    it('queues a second message while Go prepares the Today turn, even before streaming state renders', async () => {
        const { main, hook, project, updateProject } = mount();
        let finishPrepare!: (text: string) => void;
        calls.prepare.mockImplementationOnce(() => new Promise<string>((resolve) => { finishPrepare = resolve; }));
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        let sending!: Promise<void>;
        act(() => { sending = hook.result.current.sendPrompt(); });
        const reserved = updateProject.mock.calls[0][1](project);
        expect(reserved.workspaces[0].sessions[0].streaming).toBe(true);
        expect(calls.persist).not.toHaveBeenCalled();
        act(() => hook.result.current.setPrompt('Then review the launch checklist.'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.plain).not.toHaveBeenCalled();
        expect(hook.result.current.queuedMessages).toHaveLength(1);
        hook.rerender({ session: { ...main, streaming: true } });
        await act(async () => { finishPrepare(prompt); await sending; });
        expect(calls.today).toHaveBeenCalledOnce();
        expect(calls.plain).not.toHaveBeenCalled();
        await act(async () => hook.rerender({ session: { ...main, streaming: false } }));
        expect(calls.plain).toHaveBeenCalledOnce();
        expect(calls.plain.mock.calls[0][0]).toBe('Then review the launch checklist.');
    });

    it.each([false, true])('queues Today behind a pending permission without answering or dismissing it (plan: %s)', async (isPlan) => {
        const { main, hook, onRespondToPermission, onDismissPlanReview } = mount();
        hook.rerender({ session: { ...main, streaming: false, awaitingPlanApproval: isPlan, permissionOptions: [{ id: 'q0_other', name: 'Other', kind: 'allow_once' }] }, hasPendingPermission: true, awaitingPlanApproval: isPlan });
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        await act(async () => hook.result.current.sendPrompt());
        expect(hook.result.current.queuedMessages[0].todayContext).toEqual(context);
        expect(calls.prepare).not.toHaveBeenCalled();
        expect(calls.persist).not.toHaveBeenCalled();
        expect(onRespondToPermission).not.toHaveBeenCalled();
        expect(onDismissPlanReview).not.toHaveBeenCalled();
        // A denial can finish the old turn without ever resuming streaming.
        await act(async () => hook.rerender({ session: { ...main, streaming: false }, hasPendingPermission: false, awaitingPlanApproval: false }));
        expect(calls.today).toHaveBeenCalledOnce();
        expect(calls.today.mock.calls[0].at(-1)).toEqual(context);
    });

    it('keeps an already queued Today turn waiting when the current turn requests permission', async () => {
        const { main, hook } = mount(true);
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        await act(async () => hook.result.current.sendPrompt());
        hook.rerender({ session: { ...main, streaming: false }, hasPendingPermission: true });
        expect(calls.prepare).not.toHaveBeenCalled();
        expect(hook.result.current.queuedMessages).toHaveLength(1);
        await act(async () => hook.rerender({ session: { ...main, streaming: false }, hasPendingPermission: false }));
        expect(calls.today).toHaveBeenCalledOnce();
    });

    it('preserves queue order when editing Today during permission review and clears the edit index', async () => {
        const { main, hook } = mount();
        hook.rerender({ session: main, hasPendingPermission: true });
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        await act(async () => hook.result.current.sendPrompt());
        act(() => hook.result.current.prefillTodayPrompt(main.localId, 'Review the release notes.', { ...context, sourceId: 'release-notes' }));
        await act(async () => hook.result.current.sendPrompt());
        act(() => hook.result.current.editQueuedMessage(0));
        act(() => hook.result.current.setPrompt('Prioritize the launch plan.'));
        await act(async () => hook.result.current.sendPrompt());
        expect(hook.result.current.queuedMessages.map((entry) => entry.prompt)).toEqual(['Prioritize the launch plan.', 'Review the release notes.']);
        expect(hook.result.current.queuedMessages[0].todayContext).toEqual(context);

        hook.rerender({ session: { ...main, streaming: true }, hasPendingPermission: false });
        act(() => hook.result.current.setPrompt('Then write a checklist.'));
        await act(async () => hook.result.current.sendPrompt());
        expect(hook.result.current.queuedMessages.map((entry) => entry.prompt)).toEqual(['Prioritize the launch plan.', 'Review the release notes.', 'Then write a checklist.']);
        expect(calls.prepare).not.toHaveBeenCalled();
    });

    it('does not persist or send after cancellation while Today context is being checked', async () => {
        const { main, hook } = mount();
        let finishPrepare!: (text: string) => void;
        calls.prepare.mockImplementationOnce(() => new Promise<string>((resolve) => { finishPrepare = resolve; }));
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        let sending!: Promise<void>;
        act(() => { sending = hook.result.current.sendPrompt(); });
        act(() => hook.result.current.handleCancel());
        await act(async () => { finishPrepare(prompt); await sending; });
        expect(calls.persist).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
        expect(hook.result.current.queuedMessages).toEqual([]);
    });

    it('clears only local state after Stop while project structure is being prepared', async () => {
        const { main, hook, project, updateProject } = mount();
        let finish!: () => void;
        calls.structure.mockImplementationOnce(() => new Promise<void>((resolve) => { finish = resolve; }));
        act(() => hook.result.current.prefillTodayPrompt(main.localId, prompt, context));
        let sending!: Promise<void>;
        act(() => { sending = hook.result.current.sendPrompt(); });
        await waitFor(() => expect(calls.structure).toHaveBeenCalledOnce());
        expect(calls.prepare).toHaveBeenCalledOnce();
        act(() => hook.result.current.handleCancel());
        await act(async () => { finish(); await sending; });

        expect(calls.cancel).toHaveBeenCalledWith(main.localId);
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
        expect(calls.persist).not.toHaveBeenCalled();
        expect(hook.result.current.queuedMessages).toEqual([]);
        const afterStop = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        expect(afterStop.workspaces[0].sessions[0].items).toEqual([]);
        expect(afterStop.workspaces[0].sessions[0].streamItemCount).toBe(0);

        act(() => hook.result.current.prefillTodayPrompt(main.localId, 'Review another Today item.', context));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today).toHaveBeenCalledOnce();
        expect(calls.today.mock.calls[0][0]).toBe('Review another Today item.');
        const afterNext = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        expect(afterNext.workspaces[0].sessions[0].items.map((item) => item.content)).toEqual(['Review another Today item.']);
    });

    it('keeps an admitted Today message and its returned sort order when Stop arrives before the acknowledgement', async () => {
        const { main, hook, project, updateProject } = mount();
        let finishSend!: (sortOrder: number) => void;
        calls.today.mockImplementationOnce(() => new Promise<number>((resolve) => { finishSend = resolve; }));
        act(() => {
            hook.result.current.prefillTodayPrompt(main.localId, prompt, context);
            hook.result.current.setImages(authoredImages);
            hook.result.current.setFiles(authoredFiles);
        });
        let sending!: Promise<void>;
        act(() => { sending = hook.result.current.sendPrompt(); });
        await waitFor(() => expect(calls.today).toHaveBeenCalledOnce());
        expect(calls.persist).not.toHaveBeenCalled();
        expect(todayUserItem()).toMatchObject({ kind: 'user', content: prompt, images: JSON.stringify(authoredImages) });
        expect(JSON.parse(todayUserItem().files!)).toEqual(authoredFiles.map(({ dataBase64: _, ...metadata }) => metadata));
        act(() => hook.result.current.handleCancel());
        await act(async () => { finishSend(42); await sending; });
        const afterStop = updateProject.mock.calls.reduce<Project>((snapshot, [, update]) => update(snapshot), project);
        expect(afterStop.workspaces[0].sessions[0].items).toMatchObject([{ id: todayUserItem().id, content: prompt, sortOrder: 42 }]);
        expect(afterStop.workspaces[0].sessions[0].streamItemCount).toBe(1);
        expect(afterStop.workspaces[0].sessions[0].streaming).toBe(false);
        expect(hook.result.current.queuedMessages).toEqual([]);
        expect(hook.result.current.prompt).toBe('');
        expect(calls.toastError).not.toHaveBeenCalled();
    });
});

describe('Today actions prepared before the click', () => {
    const counts = { memories: 1, activities: 2, people: 0 };
    const draftFor = (kind: string, title: string): TodayChatDraft => ({ prefill: { kind, title }, counts, fingerprint: `${kind}-facts` });
    const warm = async (ref: TodayChatContextRef, draft: TodayChatDraft) => {
        calls.drafts.mockResolvedValueOnce([{ draft }]);
        warmTodayChatDrafts('test', [{ ref, signature: 'v1' }]);
        await vi.waitFor(() => expect(peekTodayChatDraft(ref)?.status).toBe('ready'));
    };

    beforeEach(async () => {
        await i18n.changeLanguage('en');
        vi.clearAllMocks();
        vi.restoreAllMocks();
        resetTodayChatDrafts();
        calls.prepare.mockImplementation(async (_tag: string, text: string, _context?: unknown) => text);
    });

    it.each([
        ['Chat about this', { sourceKind: 'purpose', sourceId: 'payroll' }, draftFor('purpose', 'Run payroll'), 'Where do I stand on "Run payroll" and what is the next step?'],
        ['Talk about this brief', { sourceKind: 'brief', sourceId: 'revision-1', briefId: 'brief-1' }, draftFor('brief', '2026-10-19'), "Go through today's brief with me: what needs my attention first?"],
    ] as const)('puts the prepared text and counts in the composer on the click itself for %s', async (_action, ref, draft, text) => {
        const { main, hook } = mount();
        await warm(ref, draft);
        let opening!: Promise<void>;
        act(() => { opening = hook.result.current.prepareTodayPrompt(main.localId, 'Card prompt', ref); });
        // Synchronously, before any further work: no wait on sources.
        expect(hook.result.current.prompt).toBe(text);
        expect(hook.result.current.todayPreview).toEqual({ status: 'ready', counts });
        await act(async () => opening);
        expect(calls.draft).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
    });

    it('shows the card prompt at once while the action is still being prepared, then its prepared question', async () => {
        const { main, hook } = mount();
        let finish!: (draft: TodayChatDraft) => void;
        calls.draft.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
        let opening!: Promise<void>;
        act(() => { opening = hook.result.current.prepareTodayPrompt(main.localId, 'Card prompt', context); });
        expect(hook.result.current.prompt).toBe('Card prompt');
        expect(hook.result.current.todayPreview).toEqual({ status: 'loading' });
        await act(async () => { finish(draftFor('purpose', 'Run payroll')); await opening; });
        expect(hook.result.current.prompt).toBe('Where do I stand on "Run payroll" and what is the next step?');
        expect(hook.result.current.todayPreview).toEqual({ status: 'ready', counts });
    });

    it('shows an older prepared draft at once and checks it again before it can be sent', async () => {
        const { main, hook } = mount();
        await warm(context, draftFor('purpose', 'Run payroll'));
        const later = Date.now() + TODAY_DRAFT_FRESH_MS + 1;
        vi.spyOn(Date, 'now').mockReturnValue(later);
        let finish!: (draft: TodayChatDraft) => void;
        calls.draft.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
        let opening!: Promise<void>;
        act(() => { opening = hook.result.current.prepareTodayPrompt(main.localId, 'Card prompt', context); });
        expect(hook.result.current.prompt).toBe('Where do I stand on "Run payroll" and what is the next step?');
        expect(hook.result.current.todayPreview).toEqual({ status: 'loading' });
        await act(async () => { finish(draftFor('purpose', 'Run payroll, renamed')); await opening; });
        expect(hook.result.current.prompt).toBe('Where do I stand on "Run payroll, renamed" and what is the next step?');
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today.mock.calls[0].at(-1)).toEqual({ ...context, generatedTitle: 'Run payroll, renamed', generatedFingerprint: 'purpose-facts' });
    });

    it('keeps the item reference when the user edited a draft whose item became unreadable', async () => {
        const { main, hook } = mount();
        await warm(context, draftFor('purpose', 'Run payroll'));
        vi.spyOn(Date, 'now').mockReturnValue(Date.now() + TODAY_DRAFT_FRESH_MS + 1);
        let fail!: (err: Error) => void;
        calls.draft.mockImplementationOnce(() => new Promise((_resolve, reject) => { fail = reject; }));
        let opening!: Promise<void>;
        act(() => { opening = hook.result.current.prepareTodayPrompt(main.localId, 'Card prompt', context); });
        act(() => hook.result.current.setPrompt('Where do I stand on "Run payroll" this week?'));
        await act(async () => { fail(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE')); await opening; });
        expect(hook.result.current.prompt).toBe('Where do I stand on "Run payroll" this week?');
        expect(hook.result.current.todayPreview).toEqual({ status: 'unavailable' });
        // Send goes through Go's read-time check, never as a plain message.
        calls.prepare.mockRejectedValueOnce(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare.mock.calls[0].at(-1)).toEqual({ ...context, generatedTitle: 'Run payroll', generatedFingerprint: 'purpose-facts' });
        expect(calls.plain).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
    });

    it('replaces an older draft that named the item with the card prompt when the item became unreadable', async () => {
        const { main, hook } = mount();
        await warm(context, draftFor('purpose', 'Run payroll'));
        vi.spyOn(Date, 'now').mockReturnValue(Date.now() + TODAY_DRAFT_FRESH_MS + 1);
        calls.draft.mockRejectedValueOnce(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE'));
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, 'Help me decide what to do next about this item.', context));
        expect(hook.result.current.prompt).toBe('Help me decide what to do next about this item.');
        expect(hook.result.current.todayPreview).toEqual({ status: 'fallback' });
    });

});

describe('Today drafts while observation is paused', () => {
    beforeEach(async () => {
        vi.clearAllMocks();
        vi.restoreAllMocks();
        resetTodayChatDrafts();
        calls.prepare.mockImplementation(async (_tag: string, text: string, _context?: unknown) => text);
    });

    it.each([
        ['en', 'Observation is paused'],
        ['zh-CN', '观察已暂停'],
    ])('opens with the card words, reads nothing and sends without observed context in %s', async (language) => {
        await i18n.changeLanguage(language);
        const { main, hook } = mount();
        const card = language === 'en' ? 'Help me decide what to do next about this item.' : '帮我想想这件事接下来该怎么做。';
        act(() => { void hook.result.current.prepareTodayPrompt(main.localId, card, context, undefined, { paused: true }); });
        expect(hook.result.current.prompt).toBe(card);
        expect(hook.result.current.todayPreview).toEqual({ status: 'paused' });
        expect(calls.draft).not.toHaveBeenCalled();
        expect(calls.drafts).not.toHaveBeenCalled();
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today).toHaveBeenCalledOnce();
        expect(calls.today.mock.calls[0].at(-1)).toEqual({ sourceKind: 'paused', sourceId: 'observation' });
    });

    it('switches to the paused draft when observation was paused after Today rendered', async () => {
        await i18n.changeLanguage('en');
        const { main, hook } = mount();
        calls.drafts.mockResolvedValueOnce([{ draft: { prefill: { kind: 'purpose', title: 'Run payroll' }, counts: { memories: 1, activities: 0, people: 0 }, fingerprint: 'payroll' } }]);
        warmTodayChatDrafts('test', [{ ref: context, signature: 'v1' }]);
        await vi.waitFor(() => expect(peekTodayChatDraft(context)?.status).toBe('ready'));
        vi.spyOn(Date, 'now').mockReturnValue(Date.now() + TODAY_DRAFT_FRESH_MS + 1);
        calls.draft.mockRejectedValueOnce(new Error('TODAY_CHAT_OBSERVATION_PAUSED'));
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, 'Help me decide what to do next about this item.', context));
        expect(hook.result.current.prompt).toBe('Help me decide what to do next about this item.');
        expect(hook.result.current.todayPreview).toEqual({ status: 'paused' });
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today.mock.calls[0].at(-1)).toEqual({ sourceKind: 'paused', sourceId: 'observation' });
    });
});

describe('brief cards in the composer', () => {
    // A card click puts the card's words in the message box at once and sends
    // nothing; Send goes through the card's route (backlog item 64).
    const counts = { memories: 0, activities: 1, people: 0 };
    const shown = 'Release memo is blocked and waiting for me. What do you need from me to continue?';
    const card = { sourceKind: 'brief_card', sourceId: 'offer-1', briefId: 'brief-1', generatedMessage: shown };
    const prepared: TodayChatDraft = { prefill: { kind: 'card_action', title: 'Release memo' }, counts, fingerprint: 'memo-facts', message: shown };

    beforeEach(async () => {
        await i18n.changeLanguage('en');
        vi.clearAllMocks();
        vi.restoreAllMocks();
        resetTodayChatDrafts();
        calls.prepare.mockImplementation(async (_tag: string, text: string, _context?: unknown) => text);
    });

    it("puts the card's words in the box before the computer answers, and sends nothing", async () => {
        let answer!: (draft: TodayChatDraft) => void;
        calls.draft.mockReturnValue(new Promise((resolve) => { answer = resolve; }));
        const { main, hook } = mount();
        let pending!: Promise<void>;
        act(() => { pending = hook.result.current.prepareTodayPrompt(main.localId, shown, card); });
        expect(hook.result.current.prompt).toBe(shown);
        expect(hook.result.current.todayPreview).toEqual({ status: 'loading' });
        answer(prepared);
        await act(async () => pending);
        expect(hook.result.current.prompt).toBe(shown);
        expect(hook.result.current.todayPreview).toEqual({ status: 'ready', counts });
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.prepare).not.toHaveBeenCalled();
    });

    it("sends through the card's route at Send, the user's words as written", async () => {
        calls.draft.mockResolvedValue(prepared);
        const { main, hook } = mount();
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, shown, card));
        act(() => hook.result.current.setPrompt(`${shown} Start with legal.`));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare).toHaveBeenCalledOnce();
        expect(calls.prepare.mock.calls[0][2]).toEqual({ ...card, generatedTitle: 'Release memo', generatedFingerprint: 'memo-facts', generatedMessage: shown });
        expect(calls.today).toHaveBeenCalledOnce();
        expect(calls.today.mock.calls[0][0]).toBe(`${shown} Start with legal.`);
        expect(calls.today.mock.calls[0].at(-1)).toEqual({ ...card, generatedTitle: 'Release memo', generatedFingerprint: 'memo-facts', generatedMessage: shown });
        expect(calls.plain).not.toHaveBeenCalled();
    });

    it("replaces untouched words with the card's current ones", async () => {
        const current = 'Release checklist is blocked and waiting for me. What do you need from me to continue?';
        calls.draft.mockResolvedValue({ ...prepared, prefill: { kind: 'card_action', title: 'Release checklist' }, message: current });
        const { main, hook } = mount();
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, shown, card));
        expect(hook.result.current.prompt).toBe(current);
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today.mock.calls[0][0]).toBe(current);
        expect(calls.today.mock.calls[0].at(-1)).toMatchObject({ generatedMessage: current, generatedTitle: 'Release checklist' });
    });

    it('keeps the words the user was shown once they edited them', async () => {
        let answer!: (draft: TodayChatDraft) => void;
        calls.draft.mockReturnValue(new Promise((resolve) => { answer = resolve; }));
        const { main, hook } = mount();
        let pending!: Promise<void>;
        act(() => { pending = hook.result.current.prepareTodayPrompt(main.localId, shown, card); });
        act(() => hook.result.current.setPrompt(`${shown}!`));
        act(() => hook.result.current.setPrompt(shown));
        answer({ ...prepared, message: 'Release checklist is blocked. What do you need from me to continue?' });
        await act(async () => pending);
        expect(hook.result.current.prompt).toBe(shown);
        await act(async () => hook.result.current.sendPrompt());
        // Sent back unchanged, the shown words are what Go compares and refuses.
        expect(calls.prepare.mock.calls[0][2]).toMatchObject({ sourceKind: 'brief_card', generatedMessage: shown });
    });

    it('keeps the card attached while observation is paused, so Send asks the computer', async () => {
        calls.draft.mockRejectedValue(new Error('TODAY_CHAT_OBSERVATION_PAUSED'));
        const { main, hook } = mount();
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, shown, card));
        expect(hook.result.current.prompt).toBe(shown);
        expect(hook.result.current.todayPreview).toEqual({ status: 'paused' });
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare.mock.calls[0][2]).toMatchObject({ sourceKind: 'brief_card', sourceId: 'offer-1' });
        expect(calls.plain).not.toHaveBeenCalled();
    });

    it('keeps edited card words attached to the card after leaving the chat', async () => {
        let answer!: (draft: TodayChatDraft) => void;
        calls.draft.mockReturnValue(new Promise((resolve) => { answer = resolve; }));
        const { main, hook } = mount();
        let current = true;
        let pending!: Promise<void>;
        act(() => { pending = hook.result.current.prepareTodayPrompt(main.localId, shown, card, () => current); });
        act(() => hook.result.current.setPrompt(`${shown} Start with legal.`));
        current = false;
        answer(prepared);
        await act(async () => pending);
        expect(hook.result.current.prompt).toBe(`${shown} Start with legal.`);
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.prepare.mock.calls[0][2]).toMatchObject({ sourceKind: 'brief_card', sourceId: 'offer-1', generatedMessage: shown });
        expect(calls.plain).not.toHaveBeenCalled();
    });

    it('clears a card that left the brief, says so, and sends nothing', async () => {
        calls.draft.mockRejectedValue(new Error('TODAY_CHAT_CONTEXT_UNAVAILABLE'));
        const { main, hook } = mount();
        await act(async () => hook.result.current.prepareTodayPrompt(main.localId, shown, card));
        expect(hook.result.current.prompt).toBe('');
        expect(hook.result.current.todayPreview).toBeUndefined();
        expect(calls.inlineError).toHaveBeenCalledWith(i18n.t('today:todayBrief.cardGone'));
        expect(calls.toast).not.toHaveBeenCalled();
        expect(calls.today).not.toHaveBeenCalled();
    });

    it('keeps typed words and attachments while opening a brief, and waits before sending', async () => {
        const { main, hook } = mount();
        let finish!: (opened?: import('@/lib/today-chat-navigation').TodayBriefOpened) => void;
        act(() => { const opening = hook.result.current.beginTodayBrief(() => true); opening.select(main.localId); finish = opening.finish; });
        act(() => { hook.result.current.setPrompt('First'); hook.result.current.setPrompt(''); hook.result.current.setPrompt('My question'); hook.result.current.setImages(authoredImages); hook.result.current.setFiles(authoredFiles); });
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today).not.toHaveBeenCalled();
        expect(calls.plain).not.toHaveBeenCalled();
        const ref = { sourceKind: 'brief', sourceId: 'revision-1', briefId: 'brief-1' };
        act(() => finish({ ref, draft: { prefill: { kind: 'brief', title: 'Today' }, counts, fingerprint: 'brief-facts' } }));
        expect(hook.result.current.prompt).toBe('My question');
        expect(hook.result.current.images).toEqual(authoredImages);
        expect(hook.result.current.files).toEqual(authoredFiles);
        expect(hook.result.current.todayPreview).toEqual({ status: 'ready', counts });
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today.mock.calls[0].at(-1)).toEqual({ ...ref, generatedTitle: 'Today', generatedFingerprint: 'brief-facts' });
    });

    it('releases a failed brief check without erasing typed words', () => {
        const { main, hook } = mount();
        let finish!: (opened?: import('@/lib/today-chat-navigation').TodayBriefOpened) => void;
        act(() => { const opening = hook.result.current.beginTodayBrief(() => true); opening.select(main.localId); finish = opening.finish; });
        act(() => hook.result.current.setPrompt('My question'));
        act(() => finish());
        expect(hook.result.current.prompt).toBe('My question');
        expect(hook.result.current.todayPreview).toEqual({ status: 'unavailable' });
    });

    it("keeps the brief attached to the user's answer after Isoform spoke first", async () => {
        const { main, hook } = mount();
        const brief = { sourceKind: 'brief', sourceId: 'revision-1', briefId: 'brief-1' };
        act(() => hook.result.current.attachTodayBrief(main.localId, brief, { prefill: { kind: 'brief', title: '2026-10-19' }, counts, fingerprint: 'brief-facts' }));
        expect(hook.result.current.prompt).toBe('');
        expect(hook.result.current.todayPreview).toEqual({ status: 'ready', counts });
        act(() => hook.result.current.setPrompt('Yes, start with the Citi payment.'));
        await act(async () => hook.result.current.sendPrompt());
        expect(calls.today).toHaveBeenCalledOnce();
        expect(calls.today.mock.calls[0][0]).toBe('Yes, start with the Citi payment.');
        expect(calls.today.mock.calls[0].at(-1)).toEqual({ ...brief, generatedTitle: '2026-10-19', generatedFingerprint: 'brief-facts' });
    });
});

// R75: after a signed-out answer, Send again resends the chat's last message
// with no retyping, on the same message row and with the same prompt.
describe('Send again after a signed-out answer (R75)', () => {
    it('resends the last message on the same row, only while a signed-out answer ends the chat', async () => {
        const { hook, main } = mount();
        act(() => hook.result.current.resendLastSend());
        expect(calls.plain).not.toHaveBeenCalled();

        act(() => hook.result.current.setPrompt('Plan the launch'));
        await act(async () => hook.result.current.sendPrompt());
        await waitFor(() => expect(calls.plain).toHaveBeenCalledTimes(1));

        // The signed-out answer is the chat's last item, so Send again is live.
        hook.rerender({ session: { ...main, items: [{ id: 'err-1', kind: 'error', content: 'signed out', toolInput: { code: 'signed_out' } }] } as never });
        expect(hook.result.current.resendItemId).toBe('err-1');
        act(() => hook.result.current.resendLastSend());
        await waitFor(() => expect(calls.plain).toHaveBeenCalledTimes(2));
        expect(calls.plain.mock.calls[1][0]).toBe(calls.plain.mock.calls[0][0]);
        const rowIds = calls.persist.mock.calls.map((call) => call[1].id);
        expect(rowIds.length).toBeGreaterThanOrEqual(2);
        expect(new Set(rowIds).size).toBe(1);

        // A later answer that is not an error does not resend the message again.
        hook.rerender({ session: { ...main, items: [{ id: 'ans-1', kind: 'assistant', content: 'Done.' }] } as never });
        expect(hook.result.current.resendItemId).toBeNull();
        act(() => hook.result.current.resendLastSend());
        expect(calls.plain).toHaveBeenCalledTimes(2);
    });
});

// A no-reply row the hub stored (window closed, or the app quit mid-turn) has
// no send in memory: Send again sends the message it answers, on that row.
describe('Send again after no answer came back', () => {
    it('resends the stored message on its own row when nothing was sent in this window', async () => {
        const { hook, main } = mount();
        const asked = { id: 'user-1', kind: 'user', content: 'What is on today?' };
        hook.rerender({ session: { ...main, items: [asked, { id: 'err-1', kind: 'error', content: 'No answer came back.', toolInput: { code: 'no_reply' } }] } as never });
        expect(hook.result.current.resendItemId).toBe('err-1');
        act(() => hook.result.current.resendLastSend());
        await waitFor(() => expect(calls.plain).toHaveBeenCalledTimes(1));
        expect(calls.plain.mock.calls[0][0]).toBe('What is on today?');
        expect(calls.persist.mock.calls.every((call) => call[1].id === 'user-1')).toBe(true);
    });
});
