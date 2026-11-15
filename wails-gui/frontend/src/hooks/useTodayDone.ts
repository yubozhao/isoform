import { useSyncExternalStore } from "react";
import { GetDoneForYou, type TodayDone } from "@/lib/wails-shim-app";
import { onPagePrivacyWithdrawal } from "@/lib/page-privacy-withdrawal";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import { waitForAppActive } from "@/lib/app-activity";

type Snapshot = { items: TodayDone[]; loading: boolean; failed: boolean };
let snapshot: Snapshot = { items: [], loading: true, failed: false };
let request = 0;
let cleanups: Array<() => void> = [];
const listeners = new Set<() => void>();
function publish(next: Snapshot) { snapshot = next; listeners.forEach((listener) => listener()); }
export function refreshTodayDone() {
    const current = ++request;
    void Promise.resolve().then(() => waitForAppActive()).then(() => current === request ? GetDoneForYou(0) : null).then((page) => {
        if (current !== request) return;
        if (!page || !Array.isArray(page.items)) throw new Error("Invalid finished projection");
        publish({ items: page.items, loading: false, failed: false });
    }).catch(() => {
        // A failed read keeps the finished work shown, marked failed so the
        // section can offer to try again; it is emptied only by a withdrawal.
        if (current === request) publish({ items: snapshot.items, loading: false, failed: true });
    });
}
function subscribe(listener: () => void) {
    listeners.add(listener);
    if (listeners.size === 1) {
        cleanups.push(EventsOn("today:done-changed", refreshTodayDone));
        cleanups.push(onPagePrivacyWithdrawal(() => {
            request++;
            publish({ items: [], loading: true, failed: false });
            refreshTodayDone();
        }));
        refreshTodayDone();
    }
    return () => {
        listeners.delete(listener);
        if (listeners.size === 0) {
            request++;
            cleanups.forEach((cleanup) => cleanup());
            cleanups = [];
            snapshot = { items: [], loading: true, failed: false };
        }
    };
}
export function useTodayDone(): Snapshot { return useSyncExternalStore(subscribe, () => snapshot); }
