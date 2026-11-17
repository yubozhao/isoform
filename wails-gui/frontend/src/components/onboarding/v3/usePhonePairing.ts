import { useCallback, useEffect, useState } from "react";
import {
    ConfirmMobilePairing,
    EnableMobileConnectionUsingRelay,
    GenerateMobileConnectionCode,
    GetMobileConnectionStatus,
    type MobileConnectionStatus,
} from "@/lib/wails-shim-app";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import { cardFor, PAIRING_CODE_REFRESH_MS } from "../v2/scenes/ReachesScene";

// The phone pairing of step 5, the same connection setup v2's Reaches you
// uses: turn the connection on when it is off, keep a fresh code, follow a
// pairing live, and approve or decline it here.
export function usePhonePairing() {
    const [status, setStatus] = useState<MobileConnectionStatus | null>(null);
    const [relayUnreachable, setRelayUnreachable] = useState(false);
    const [pairFailed, setPairFailed] = useState(false);

    useEffect(() => {
        let live = true;
        const take = (next: MobileConnectionStatus) => {
            if (live) setStatus(next);
            return next;
        };
        const fresh = async () => {
            try {
                take(await GenerateMobileConnectionCode());
                if (live) setRelayUnreachable(false);
            } catch (error) {
                if (live && String(error).includes("remote access is not ready")) setRelayUnreachable(true);
            }
        };
        void (async () => {
            try {
                let current = take(await GetMobileConnectionStatus());
                if (!current.enabled) current = take(await EnableMobileConnectionUsingRelay(""));
                if (!current.devices?.length && !current.pendingPairing) await fresh();
            } catch {
                if (live) setRelayUnreachable(true);
            }
        })();
        const timer = window.setInterval(() => {
            void GetMobileConnectionStatus()
                .then((s) => {
                    if (!live || s.devices?.length || s.pendingPairing) return;
                    return fresh();
                })
                .catch(() => {});
        }, PAIRING_CODE_REFRESH_MS);
        const off = EventsOn("mobile:updated", () => {
            void GetMobileConnectionStatus()
                .then(take)
                .catch(() => {});
        });
        return () => {
            live = false;
            window.clearInterval(timer);
            off();
        };
    }, []);

    const card = cardFor(status);

    // A code that expires before the refresh is replaced at once.
    const expiresAt = status?.expiresAt ?? "";
    useEffect(() => {
        if (card.kind !== "code" || !expiresAt) return;
        const left = Date.parse(expiresAt) - Date.now();
        if (!Number.isFinite(left) || left > PAIRING_CODE_REFRESH_MS) return;
        const timer = window.setTimeout(
            () => void GenerateMobileConnectionCode().then(setStatus).catch(() => {}),
            Math.max(0, left),
        );
        return () => window.clearTimeout(timer);
    }, [card.kind, expiresAt]);

    const confirm = useCallback(async (pairingId: string, approve: boolean) => {
        setPairFailed(false);
        try {
            const next = await ConfirmMobilePairing(pairingId, approve);
            setStatus(next);
            if (!approve) setStatus(await GenerateMobileConnectionCode());
        } catch {
            setPairFailed(true);
            void GenerateMobileConnectionCode().then(setStatus).catch(() => {});
        }
    }, []);

    const retry = useCallback(() => {
        setRelayUnreachable(false);
        void GenerateMobileConnectionCode()
            .then(setStatus)
            .catch(() => setRelayUnreachable(true));
    }, []);

    return { card, relayUnreachable, pairFailed, confirm, retry };
}
