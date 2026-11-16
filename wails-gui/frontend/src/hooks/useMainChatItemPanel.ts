import { useCallback, useSyncExternalStore } from "react";
import { LoadMainChatProactiveItem, RecordMainChatProactiveReceipt } from "@/lib/wails-shim-app";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import { onPagePrivacyWithdrawal } from "@/lib/page-privacy-withdrawal";
import { fromStreamItemData, getMainChatProactive } from "@/lib/stream-helpers";
import type { StreamItem } from "@/types";

export type MainChatItemPanelSurface = "chat" | "today" | "tasks";

type PanelState = {
    surface: MainChatItemPanelSurface;
    item: StreamItem | null;
    openId: string;
    placeFailed: boolean;
    revalidating: boolean;
};

let panelState: PanelState | null = null;
type PendingPanelOpen = {
    surface: MainChatItemPanelSurface;
    momentID: string;
    placeFailed: boolean;
    onOpened?: () => void;
    onFailed?: () => void;
};

let pendingPanelOpen: PendingPanelOpen | null = null;
const listeners = new Set<() => void>();
let requestID = 0;

function subscribe(listener: () => void) {
    listeners.add(listener);
    return () => listeners.delete(listener);
}

function getSnapshot() {
    return panelState;
}

function publish(next: PanelState | null) {
    panelState = next;
    for (const listener of listeners) listener();
}

function resetForPrivacyWithdrawal() {
    requestID += 1;
    pendingPanelOpen = null;
    publish(null);
}

function loadPendingPanelOpen(pending: PendingPanelOpen, request: number) {
    let loading: ReturnType<typeof LoadMainChatProactiveItem>;
    try {
        loading = LoadMainChatProactiveItem(pending.momentID);
    } catch {
        if (request === requestID && pendingPanelOpen === pending) { pendingPanelOpen = null; pending.onFailed?.(); }
        return;
    }
    void loading.then((data) => {
        if (request !== requestID || pendingPanelOpen !== pending) return;
        pendingPanelOpen = null;
        const item = data ? fromStreamItemData(data) : null;
        const message = item ? getMainChatProactive(item) : null;
        if (!item || !message || message.momentId !== pending.momentID) { pending.onFailed?.(); return; }
        recordOpened(item);
        publish({
            surface: pending.surface,
            item,
            openId: message.momentId,
            placeFailed: pending.placeFailed,
            revalidating: false,
        });
        pending.onOpened?.();
    }).catch(() => {
        if (request === requestID && pendingPanelOpen === pending) { pendingPanelOpen = null; pending.onFailed?.(); }
    });
}

function revalidateForActivityUpdate() {
    if (pendingPanelOpen) {
        const pending = pendingPanelOpen;
        const request = ++requestID;
        loadPendingPanelOpen(pending, request);
        return;
    }
    const current = panelState;
    if (!current) return;
    const request = ++requestID;
    publish({ ...current, item: null, revalidating: true });
    let loading: ReturnType<typeof LoadMainChatProactiveItem>;
    try {
        loading = LoadMainChatProactiveItem(current.openId);
    } catch {
        if (request === requestID && panelState?.openId === current.openId) publish(null);
        return;
    }
    void loading.then((data) => {
        if (request !== requestID || !panelState || panelState.openId !== current.openId) return;
        const item = data ? fromStreamItemData(data) : null;
        const message = item ? getMainChatProactive(item) : null;
        if (!item || !message || message.momentId !== current.openId) {
            publish(null);
            return;
        }
        publish({ ...panelState, item, revalidating: false });
    }).catch(() => {
        if (request !== requestID || !panelState || panelState.openId !== current.openId) return;
        publish(null);
    });
}

onPagePrivacyWithdrawal(resetForPrivacyWithdrawal);
EventsOn("activity:activities-updated", revalidateForActivityUpdate);
EventsOn("session:deleted", resetForPrivacyWithdrawal);

function recordOpened(item: StreamItem) {
    const message = getMainChatProactive(item);
    if (message) {
        void Promise.resolve(RecordMainChatProactiveReceipt(message.momentId, "opened")).catch(() => undefined);
    }
}

export function useMainChatItemPanel(surface: MainChatItemPanelSurface) {
    const snapshot = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
    const openItem = useCallback((item: StreamItem, placeFailed = false) => {
        const message = getMainChatProactive(item);
        if (!message) return;
        requestID += 1;
        pendingPanelOpen = null;
        recordOpened(item);
        publish({ surface, item, openId: message.momentId, placeFailed, revalidating: false });
    }, [surface]);
    const openMoment = useCallback((momentID: string, placeFailed = false, onOpened?: () => void, onFailed?: () => void) => {
        const request = ++requestID;
        const pending: PendingPanelOpen = { surface, momentID, placeFailed, onOpened, onFailed };
        pendingPanelOpen = pending;
        if (panelState) publish(null);
        loadPendingPanelOpen(pending, request);
    }, [surface]);
    const setPlaceFailed = useCallback((placeFailed: boolean) => {
        if (panelState?.surface === surface) {
            publish({ ...panelState, placeFailed });
        }
    }, [surface]);
    const close = useCallback(() => {
        const closesPanel = panelState?.surface === surface;
        const closesPending = pendingPanelOpen?.surface === surface;
        if (closesPanel || closesPending) {
            requestID += 1;
            if (closesPending) pendingPanelOpen = null;
            if (closesPanel) publish(null);
        }
    }, [surface]);
    return {
        item: snapshot?.surface === surface ? snapshot.item : null,
        openId: snapshot?.surface === surface ? snapshot.openId : null,
        placeFailed: snapshot?.surface === surface ? snapshot.placeFailed : false,
        revalidating: snapshot?.surface === surface ? snapshot.revalidating : false,
        openItem,
        openMoment,
        setPlaceFailed,
        close,
    };
}
