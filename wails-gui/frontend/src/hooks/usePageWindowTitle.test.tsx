import { renderHook } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import i18n from '@/i18n';
import { usePageWindowTitle } from './usePageWindowTitle';
import type { AppMode } from '@/components/sidebar';
const title = vi.hoisted(() => vi.fn());
vi.mock('../../wailsjs/runtime/runtime', () => ({ WindowSetTitle: title }));
describe('Page window titles', () => {
    it.each(['en', 'zh-CN'])('sets each page title in %s', async (language) => {
        await i18n.changeLanguage(language);
        const { rerender } = renderHook(({ mode }) => usePageWindowTitle(mode, { kind: 'main', label: 'Ignored label' }), { initialProps: { mode: 'today' as AppMode } });
        const pages = {
            today: 'sidebar.today', chat: 'sidebar.mainChat', activity: 'sidebar.activity', agents: 'sidebar.agents', you: 'sidebar.you',
            memory: 'userMenu.memory', knowledge: 'userMenu.knowledge', people: 'userMenu.people',
        } as const satisfies Record<AppMode, string>;
        for (const [mode, key] of Object.entries(pages)) {
            rerender({ mode: mode as AppMode });
            expect(title).toHaveBeenLastCalledWith(`${i18n.t(`app:${key}`)} · Isoform`);
        }
    });
});


it.each(['en', 'zh-CN'])('titles ordinary and transcript chats by their actual name in %s', async (language) => {
    await i18n.changeLanguage(language);
    const { rerender } = renderHook(({ session }) => usePageWindowTitle('chat', session), {
        initialProps: { session: { kind: 'main', label: 'Ignored' } as { kind: 'main' | ''; label: string } },
    });
    expect(title).toHaveBeenLastCalledWith(`${i18n.t('app:sidebar.mainChat')} · Isoform`);
    rerender({ session: { kind: '', label: 'Planning' } });
    expect(title).toHaveBeenLastCalledWith('Planning · Isoform');
    rerender({ session: { kind: '', label: 'Transcript: Standup' } });
    expect(title).toHaveBeenLastCalledWith('Transcript: Standup · Isoform');
    rerender({ session: { kind: '', label: '' } });
    expect(title).toHaveBeenLastCalledWith(`${i18n.t('app:sidebar.chat')} · Isoform`);
});
