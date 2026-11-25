import { useEffect } from "react";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import { speakOnce } from "../lib/tts-player";

interface NotificationSpeakEvent {
    title?: string;
}

/**
 * Read notifications aloud (computer-conformance answers v1.5.0 S22): Go
 * sends "notification:speak" after each notification it shows on this
 * computer, only while the switch is on and outside quiet hours
 * (notification_speech.go), and this says its title with the on-device
 * voice. It used to speak each new hand-off's title, which isn't a
 * notification; the name stays for App.tsx's import.
 */
export function useHandoffNotification() {
    useEffect(() => {
        const cleanup = EventsOn("notification:speak", (event: NotificationSpeakEvent) => {
            const title = event?.title?.trim();
            if (title) void speakOnce(title).catch(() => {});
        });
        return cleanup;
    }, []);
}
