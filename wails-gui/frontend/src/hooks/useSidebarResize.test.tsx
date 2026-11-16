import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { useSidebarResize } from './useSidebarResize';

const resize = (width: number) => act(() => {
    Object.defineProperty(window, 'innerWidth', { value: width, configurable: true });
    window.dispatchEvent(new Event('resize'));
});
const deps = { layoutRef: { current: null } };
describe('Sidebar frame UI state', () => {
    beforeEach(() => { sessionStorage.clear(); resize(1100); });
    it('opens at 1100 and collapses below it (computer-window v1.0.0 rule 3)', () => {
        const { result } = renderHook(() => useSidebarResize(deps));
        expect(result.current.sidebarWidth).toBe(214);
        expect(result.current.sidebarCollapsed).toBe(false);
        resize(1099);
        expect(result.current.sidebarCollapsed).toBe(true);
        resize(1100);
        expect(result.current.sidebarCollapsed).toBe(false);
    });
    it('remembers hand collapse after resizing and reloading this window', () => {
        const { result, unmount } = renderHook(() => useSidebarResize(deps));
        act(() => result.current.setSidebarCollapsed(true));
        resize(800); resize(1400);
        expect(result.current.sidebarCollapsed).toBe(true);
        unmount();
        const next = renderHook(() => useSidebarResize(deps));
        expect(next.result.current.sidebarCollapsed).toBe(true);
        act(() => next.result.current.setSidebarCollapsed(false));
        expect(next.result.current.sidebarCollapsed).toBe(false);
    });
    it('starts collapsed on narrow windows but the mark can open it', () => {
        resize(1000);
        const { result } = renderHook(() => useSidebarResize(deps));
        expect(result.current.sidebarCollapsed).toBe(true);
        act(() => result.current.setSidebarCollapsed(false));
        expect(result.current.sidebarCollapsed).toBe(false);
        resize(990);
        expect(result.current.sidebarCollapsed).toBe(true);
        resize(1100);
        expect(result.current.sidebarCollapsed).toBe(false);
    });
});
