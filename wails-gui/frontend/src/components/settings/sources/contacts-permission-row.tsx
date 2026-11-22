import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { GetPlatformPermissions } from "../../../../wailsjs/go/main/App";
import { EventsOn } from "../../../../wailsjs/runtime/runtime";
import { Button } from "@/components/ui/button";
import { SettingsRow } from "@/components/settings/settings-primitives";

// ── Contacts prerequisite ──
//
// The People sheet explains what is read and records consent before macOS
// is asked. Settings only links there; it cannot request Contacts itself.
// It sits in What Isoform sees › Messages' details, where "people you know"
// is used (settings batch D3, from Learning and memory; design to place).
// It shows only while Contacts is missing.

const CONTACTS_PERMISSION_KEY = "contacts";

export function ContactsPermissionRow({ onOpenPeople }: { onOpenPeople: () => void }) {
    const { t } = useTranslation("settings");
    const [missing, setMissing] = useState(false);
    const mounted = useRef(true);

    const refresh = useCallback(() => {
        GetPlatformPermissions()
            .then((permissions) => {
                if (!mounted.current) return;
                const item = permissions.items.find((i) => i.key === CONTACTS_PERMISSION_KEY);
                setMissing(Boolean(item && item.actionable && !item.granted));
            })
            .catch(() => {
                // Without a reading there is nothing to prompt for.
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

    if (!missing) return null;

    return (
        <>
            <SettingsRow
                title={t("learningMemory.contacts.title")}
                description={t("learningMemory.contacts.description")}
                control={
                    <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={onOpenPeople}
                    >
                        {t("learningMemory.contacts.openPeople")}
                    </Button>
                }
            />
        </>
    );
}
