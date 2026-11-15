import { GetGoalCheckIn, usePageQueryRefresh, PageEventsOn as EventsOn } from '@/lib/page-queries';
import { pageQueryCache } from '@/lib/query-cache';
import { useCallback, useEffect, useRef, useState } from "react";
import { type GoalCheckInView } from "@/lib/wails-shim-app";

/** Today's one check-in for a goal long past due (Go: GetGoalCheckIn), read
 *  again when a goal moves, when observation is paused or resumed (Go asks
 *  nothing while paused), and when the window comes back. */
export function useGoalCheckIn(enabled: boolean): { checkIn: GoalCheckInView | null; refresh: () => void } {
    const [checkIn, setCheckIn] = useState<GoalCheckInView | null>(() => pageQueryCache.peek('GetGoalCheckIn') ?? null);
    const version = useRef(0);
    const refresh = useCallback(() => {
        const mine = ++version.current;
        GetGoalCheckIn()
            .then((next) => {
                if (mine === version.current) setCheckIn(next ?? null);
            })
            // A failed read keeps the check-in shown; the next event or focus
            // reads again (one failed read no longer removes the card).
            .catch(() => {});
    }, []);
    usePageQueryRefresh(['GetGoalCheckIn'], refresh, enabled);

    useEffect(() => {
        if (!enabled) return;
        refresh();
        const offGoals = EventsOn("agency:updated", refresh);
        const offPaused = EventsOn("closed-eyes:changed", refresh);
        const onFocus = () => { pageQueryCache.invalidate(['GetGoalCheckIn']); refresh(); };
        window.addEventListener("focus", onFocus);
        return () => {
            version.current += 1;
            offGoals();
            offPaused();
            window.removeEventListener("focus", onFocus);
        };
    }, [enabled, refresh]);
    return { checkIn: enabled ? checkIn : null, refresh: () => { pageQueryCache.invalidate(['GetGoalCheckIn']); return refresh(); } };
}
