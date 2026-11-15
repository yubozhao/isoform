import { useCallback, useEffect, useRef, useState } from "react";
import { DismissTodayBeforeYouTalk, SetEngagementKindEnabled, type TodayBeforeYouTalk } from "@/lib/wails-shim-app";
import { GetTodayBeforeYouTalk, usePagePrivacyWithdrawal, usePageQueryRefresh } from "@/lib/page-queries";
import { pageQueryCache } from "@/lib/query-cache";
import { activeInterval } from "@/lib/app-activity";

/** The meeting preview Today shows. A re-read keeps the card on screen until
 * its answer arrives, and a failed read keeps the last card (the next tick
 * reads again): the card no longer blinks out every 30 s or vanishes after one
 * failed read. Only a privacy withdrawal (below) clears it at once. */
export function useTodayBeforeYouTalk(enabled: boolean) {
    const [card, setCard] = useState<TodayBeforeYouTalk | null>(() => pageQueryCache.peek<TodayBeforeYouTalk>("GetTodayBeforeYouTalk") ?? null);
    const version = useRef(0);
    const refresh = useCallback(() => {
        const current = ++version.current;
        // A hidden Today keeps no private card (it reads again when shown).
        if (!enabled) {
            setCard(null);
            return;
        }
        void GetTodayBeforeYouTalk().then((next) => {
            if (current === version.current) setCard(next);
        }, () => {});
    }, [enabled]);
    // Hidden pages still discard private local state, without starting reads.
    usePageQueryRefresh(["GetTodayBeforeYouTalk"], refresh, true);
    useEffect(() => {
        refresh();
        if (!enabled) return;
        // The card follows meeting times: re-read every 30 s while Today is
        // looked at, and at once when it is shown again. Stopped while the
        // window is hidden or the Mac locked.
        const invalidate = () => pageQueryCache.invalidate(["GetTodayBeforeYouTalk"], false);
        const stop = activeInterval(invalidate, 30_000);
        return () => {
            version.current++;
            stop();
        };
    }, [enabled, refresh]);
    // Forget, app protection, Closed Eyes and identity withdrawal retire the
    // rendered card and its in-flight reads in the event's own turn; an
    // ordinary quota poll is not a withdrawal and keeps the card.
    usePagePrivacyWithdrawal(() => {
        version.current++;
        setCard(null);
    });
    const dismiss = useCallback(async (meetingID: string) => {
        await DismissTodayBeforeYouTalk(meetingID);
        pageQueryCache.invalidate(["GetTodayBeforeYouTalk"]);
        refresh();
    }, [refresh]);
    const mute = useCallback(async () => {
        await SetEngagementKindEnabled("meeting_prep", false);
        pageQueryCache.invalidate(["GetTodayBeforeYouTalk"]);
        refresh();
    }, [refresh]);
    return { card, dismiss, mute };
}
