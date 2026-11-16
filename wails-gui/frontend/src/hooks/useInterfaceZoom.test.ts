import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useInterfaceZoom } from './useInterfaceZoom';
import { useKeybindings } from './useKeybindings';
import '@/i18n';

const mocks = vi.hoisted(() => ({
    saved: 120,
    get: vi.fn(async () => mocks.saved),
    set: vi.fn(async (percent: number) => {
        mocks.saved = Math.min(150, Math.max(90, percent));
        return mocks.saved;
    }),
}));
vi.mock('../../wailsjs/go/main/App', () => ({
    GetInterfaceZoom: mocks.get,
    SetInterfaceZoom: mocks.set,
}));

// Pass-access v1.0.0 rule 6: Cmd+ and Cmd- from 90% to 150%, saved per user.
describe('useInterfaceZoom', () => {
    beforeEach(() => {
        mocks.saved = 120;
        mocks.set.mockClear();
    });

    it('steps from the saved zoom in tens and stops at the Go side\'s range', async () => {
        const { result } = renderHook(() => useInterfaceZoom());
        await waitFor(() => expect(mocks.get).toHaveBeenCalled());
        await act(async () => { result.current.zoomIn(); });
        expect(mocks.set).toHaveBeenLastCalledWith(130);
        for (let i = 0; i < 5; i++) await act(async () => { result.current.zoomIn(); });
        expect(mocks.saved).toBe(150);
        expect(mocks.set).toHaveBeenLastCalledWith(160);
        await act(async () => { result.current.zoomOut(); });
        expect(mocks.set).toHaveBeenLastCalledWith(140);
        await act(async () => { result.current.reset(); });
        expect(mocks.set).toHaveBeenLastCalledWith(100);
    });

    it('keeps the same functions across renders so the key listener stays put', async () => {
        const { result, rerender } = renderHook(() => useInterfaceZoom());
        const first = result.current;
        rerender();
        expect(result.current).toBe(first);
    });

    it('binds Cmd+= / Cmd+- / Cmd+0 (Ctrl elsewhere) with labels in Settings', () => {
        const { result } = renderHook(() => useKeybindings());
        for (const [id, key] of [['zoom-in', '='], ['zoom-out', '-'], ['zoom-reset', '0']] as const) {
            const combo = result.current.getCombo(id)!;
            expect(combo.key).toBe(key);
            expect(combo.meta || combo.ctrl).toBe(true);
            const event = new KeyboardEvent('keydown', { key, metaKey: combo.meta, ctrlKey: combo.ctrl });
            expect(result.current.findMatchingAction(event)).toBe(id);
            expect(result.current.actions.find((a) => a.id === id)!.label).not.toMatch(/keybindings\.commands/);
        }
    });
});
