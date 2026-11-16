import { useCallback, useEffect, useMemo, useRef } from 'react';
import { GetInterfaceZoom, SetInterfaceZoom } from '../../wailsjs/go/main/App';

// Interface zoom (pass-access v1.0.0 rule 6): the window's own page zoom,
// 90% to 150% in 10% steps, saved per user by the Go side
// (interface_zoom.go). The browser preview has no window to zoom; the calls
// fail quietly there.
export const INTERFACE_ZOOM_STEP = 10;

export function useInterfaceZoom() {
    const zoom = useRef(100);
    useEffect(() => {
        let cancelled = false;
        Promise.resolve(GetInterfaceZoom())
            .then((value) => { if (!cancelled && typeof value === 'number') zoom.current = value; })
            .catch(() => {});
        return () => { cancelled = true; };
    }, []);
    const set = useCallback((percent: number) => {
        Promise.resolve(SetInterfaceZoom(percent))
            .then((value) => { if (typeof value === 'number') zoom.current = value; })
            .catch(() => {});
    }, []);
    return useMemo(() => ({
        zoomIn: () => set(zoom.current + INTERFACE_ZOOM_STEP),
        zoomOut: () => set(zoom.current - INTERFACE_ZOOM_STEP),
        reset: () => set(100),
    }), [set]);
}
