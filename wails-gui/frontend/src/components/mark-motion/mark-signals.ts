// The app's existing signals the mark listens to. No polling: each is an
// event the app already sends, or a read the app already caches.
import { useEffect, useRef, useState } from "react";
import { appActive, onAppActivity } from "@/lib/app-activity";
import { GetLatestBrief, PageEventsOn } from "@/lib/page-queries";
import { pageQueryCache } from "@/lib/query-cache";
import { briefSlotKey } from "@/lib/brief-time";
import type { TodayBriefView } from "@/lib/wails-shim-app";

/**
 * Whether the window is in front (rule 16, "nothing plays while the window is
 * hidden or behind"): shown and the Mac unlocked. WebKit reports the page
 * hidden when the window is minimized, hidden, on another Space or covered,
 * so "behind" is visibility, not focus. Keyboard focus is not asked for: a
 * WKWebView reports document.hasFocus() false whenever another app is
 * active, so the mark stayed still while Isoform sat on screen beside the app
 * Bo was using, and through every task it ran then (Bo, Nov 13).
 */
export function useWindowInFront(): boolean {
    const [inFront, setInFront] = useState(appActive);
    useEffect(() => {
        const update = () => setInFront(appActive());
        const off = onAppActivity(update);
        update();
        return off;
    }, []);
    return inFront;
}

const REDUCED_QUERY = "(prefers-reduced-motion: reduce)";

/** The system's reduced motion setting (rule 18), followed live. */
export function useReducedMotion(): boolean {
    const [reduced, setReduced] = useState(() => typeof window !== "undefined" && !!window.matchMedia?.(REDUCED_QUERY).matches);
    useEffect(() => {
        const query = window.matchMedia?.(REDUCED_QUERY);
        if (!query) return;
        const update = () => setReduced(query.matches);
        query.addEventListener?.("change", update);
        return () => query.removeEventListener?.("change", update);
    }, []);
    return reduced;
}

const briefId = (view: TodayBriefView | null | undefined) => view?.brief?.briefId ?? "";

/**
 * Calls onArrive when a new brief arrives: the brief service stores a brief
 * (brief:stored, with its id) that is not one already seen, and today's latest
 * brief, read through the page cache Today shares, is that brief and has
 * something to say. A brief stored again (an amend, a withdrawal) keeps its id
 * and plays nothing.
 */
export function useBriefArrivals(onArrive: (evening: boolean) => void): void {
    const arrive = useRef(onArrive);
    arrive.current = onArrive;
    useEffect(() => {
        let live = true;
        const seen = new Set<string>();
        const cached = briefId(pageQueryCache.peek("GetLatestBrief") as TodayBriefView | undefined);
        if (cached) seen.add(cached);
        // The brief that is already there when the app opens is not an arrival.
        let baseline: Promise<unknown> = Promise.resolve();
        if (!cached) {
            baseline = GetLatestBrief().then((view) => {
                const id = briefId(view);
                if (id) seen.add(id);
            }, () => {});
        }
        const off = PageEventsOn("brief:stored", (id?: unknown) => {
            if (typeof id !== "string" || !id || seen.has(id)) return;
            void baseline.then(() => {
                if (!live || seen.has(id)) return;
                return GetLatestBrief().then((view) => {
                    if (!live || seen.has(id)) return;
                    const brief = view?.brief;
                    if (!brief || brief.briefId !== id) return;
                    seen.add(id);
                    if (view.quiet || view.nothingNew) return;
                    arrive.current(briefSlotKey(brief.slot, brief.asOf, brief.timezone) === "evening");
                }, () => {});
            });
        });
        return () => {
            live = false;
            off();
        };
    }, []);
}
