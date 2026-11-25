import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { GetContactsAccessStatus, OpenPermissionSettings, type ContactsAccessStatus } from "@/lib/wails-shim-app";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import { Button } from "@/components/ui/button";
import { SettingsRow } from "@/components/settings/settings-primitives";
import { SettingsFailureLine, useSettingsFailure } from "@/components/settings/settings-failure";

// ── Contacts, in What Isoform sees › Messages' details ──
//
// computer-conformance answers v1.5.0 S15: Contacts decides who "people you
// know" are, so it is Messages' own line: "Contacts · how Isoform knows who
// you know", its state and Open System Settings. Under it, what Messages does
// without Contacts, from the code: the Messages reader sends a conversation
// to the AI only when someone in it resolves to a person Isoform knows by
// their iMessage number or email (imessage_capture.go, svc.resolve), so
// without Contacts the people known from elsewhere are still read.
//
// Before macOS has been asked, System Settings has no Isoform row to turn
// on, and the People sheet records consent before the prompt; so the row
// then offers that sheet instead (RequestContactsAccess runs only there).

const CONTACTS_PERMISSION_KEY = "contacts";

export function ContactsPermissionRow({ onOpenPeople }: { onOpenPeople: () => void }) {
    const { t } = useTranslation("settings");
    const { failure: openFailure, fail: failOpen, clear } = useSettingsFailure();
    const [status, setStatus] = useState<ContactsAccessStatus | null>(null);
    const [readFailed, setReadFailed] = useState(false);
    const mounted = useRef(true);

    const refresh = useCallback(() => {
        GetContactsAccessStatus()
            .then((next) => {
                if (!mounted.current) return;
                setStatus(next);
                setReadFailed(false);
            })
            .catch(() => {
                if (!mounted.current) return;
                setStatus(null);
                setReadFailed(true);
            });
    }, []);

    useEffect(() => {
        mounted.current = true;
        refresh();
        let timer: ReturnType<typeof setTimeout>;
        // Coming back from System Settings: read the grant again.
        const onWindowFocus = () => {
            clearTimeout(timer);
            timer = setTimeout(refresh, 500);
        };
        window.addEventListener("focus", onWindowFocus);
        const offPerm = EventsOn("permissions:changed", () => refresh());
        return () => {
            mounted.current = false;
            clearTimeout(timer);
            window.removeEventListener("focus", onWindowFocus);
            offPerm();
        };
    }, [refresh]);

    const openSystem = () => {
        clear();
        OpenPermissionSettings(CONTACTS_PERMISSION_KEY).catch(() => failOpen(t("sees.openSystemFailed"), openSystem));
    };

    if (readFailed) {
        return <SettingsFailureLine failure={{ text: t("sees.contactsFailed"), retry: refresh }} />;
    }
    // Where this computer has no Contacts to give, there is nothing to say.
    if (!status || !status.supported || status.permission === "not_applicable") return null;

    const state =
        status.permission === "granted"
            ? t("sees.contactsAllowed")
            : status.permission === "not_determined"
              ? t("sees.contactsNotAsked")
              : t("sees.contactsNotAllowed");
    return (
        <div data-testid="sees-contacts">
            <SettingsRow
                title={t("sees.contacts")}
                description={state}
                failure={openFailure}
                control={
                    status.permission === "not_determined" ? (
                        <Button type="button" variant="outline" size="sm" onClick={onOpenPeople}>
                            {t("learningMemory.contacts.openPeople")}
                        </Button>
                    ) : (
                        <Button type="button" variant="outline" size="sm" onClick={openSystem}>
                            {t("sees.openSystem")}
                        </Button>
                    )
                }
            />
            <p className="settings-row-desc -mt-1 pb-2" data-testid="sees-messages-without-contacts">
                {t("sees.messagesWithoutContacts")}
            </p>
        </div>
    );
}
