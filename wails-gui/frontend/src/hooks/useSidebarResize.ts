import { useCallback, useEffect, useState } from 'react';

// computer-window v1.0.0 rules 2 and 3: the sidebar is 214 wide (five places
// and the foot, no list to resize), full at a window of 1100 and wider, the
// 56-wide rail below. A choice made by hand is kept.
export const SIDEBAR_OPEN_WIDTH = 214;
export const SIDEBAR_COLLAPSED_WIDTH = 56;
export const SIDEBAR_COLLAPSE_BREAKPOINT = 1100;
const COLLAPSED_KEY = 'isoform.ui.sidebar.handCollapsed';

// Session storage is window-scoped UI state. An automatic collapse never
// overwrites the person's choice, including across a WebView reload.
export function useSidebarResize(_deps: { layoutRef: React.RefObject<HTMLDivElement | null> }) {
    const [handCollapsed, setHandCollapsed] = useState(() => {
        try { return sessionStorage.getItem(COLLAPSED_KEY) === 'true'; } catch { return false; }
    });
    const [narrow, setNarrow] = useState(() => window.innerWidth < SIDEBAR_COLLAPSE_BREAKPOINT);
    const [openedWhileNarrow, setOpenedWhileNarrow] = useState(false);
    const sidebarCollapsed = handCollapsed || (narrow && !openedWhileNarrow);
    const setSidebarCollapsed = useCallback<React.Dispatch<React.SetStateAction<boolean>>>((value) => {
        const next = typeof value === 'function' ? value(sidebarCollapsed) : value;
        setHandCollapsed(next);
        setOpenedWhileNarrow(!next && narrow);
        try { sessionStorage.setItem(COLLAPSED_KEY, String(next)); } catch { /* Storage may be unavailable. */ }
    }, [sidebarCollapsed, narrow]);

    useEffect(() => {
        const onResize = () => {
            setNarrow(window.innerWidth < SIDEBAR_COLLAPSE_BREAKPOINT);
            setOpenedWhileNarrow(false);
        };
        window.addEventListener('resize', onResize);
        return () => window.removeEventListener('resize', onResize);
    }, []);

    return { sidebarCollapsed, setSidebarCollapsed, sidebarWidth: SIDEBAR_OPEN_WIDTH };
}
