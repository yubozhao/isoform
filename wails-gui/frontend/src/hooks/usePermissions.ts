import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import type { PlatformPermissions, PermissionItem } from '@/types';
import {
    GetPlatformPermissions,
    OpenPermissionSettings,
    RequestPermission,
} from '../../wailsjs/go/main/App';

export function usePermissions() {
    const { t } = useTranslation(['onboarding', 'settings']);
    const [permissions, setPermissions] = useState<PlatformPermissions | null>(null);
    const [loading, setLoading] = useState(false);
    // The last read failed: shown as "Couldn't read", never as off.
    const [readFailed, setReadFailed] = useState(false);
    // The key being requested right now. macOS shows one dialog per request,
    // so a second click waits until the first prompt is answered: it is
    // ignored here and the Go side serializes requests as well.
    const [requestingKey, setRequestingKey] = useState<string | null>(null);
    const requestingRef = useRef(false);

    const refresh = useCallback(async () => {
        setLoading(true);
        try {
            const next = await GetPlatformPermissions();
            setPermissions(next);
            setReadFailed(false);
            return next;
        } catch (error) {
            setReadFailed(true);
            const message = error instanceof Error ? error.message : String(error);
            toast.error(`${t('settings:permissions.checkFailedToast')}: ${message}`);
            return null;
        } finally {
            setLoading(false);
        }
    }, [t]);

    // Initial fetch
    useEffect(() => {
        void refresh();
    }, [refresh]);

    // Re-fetch on window focus / tab visibility
    useEffect(() => {
        const onWindowFocus = () => {
            void refresh();
        };
        const onVisibilityChange = () => {
            if (document.visibilityState === 'visible') {
                void refresh();
            }
        };
        window.addEventListener('focus', onWindowFocus);
        document.addEventListener('visibilitychange', onVisibilityChange);
        return () => {
            window.removeEventListener('focus', onWindowFocus);
            document.removeEventListener('visibilitychange', onVisibilityChange);
        };
    }, [refresh]);

    // Stay in sync when permissions or background listening change from
    // elsewhere — e.g. the microphone toggle on Windows mirrors background
    // listening, so a flip on either surface needs both to refresh.
    useEffect(() => {
        const offPerm = EventsOn('permissions:changed', () => void refresh());
        const offAudio = EventsOn('background-listening:changed', () => void refresh());
        return () => {
            offPerm();
            offAudio();
        };
    }, [refresh]);

    const requestPermission = useCallback(async (item: PermissionItem) => {
        // Contacts can be requested only after the Who you know consent sheet.
        if (item.key === 'contacts' || item.key === 'automation_messages') return;
        if (requestingRef.current) return;
        requestingRef.current = true;
        setRequestingKey(item.key);
        // Every key the Go side reports has a label in both locales
        // (permission-copy.test.ts checks this).
        const localizedLabel = t(
            `settings:permissions.items.${item.key}.label` as never,
        ) as string;
        try {
            const granted = await RequestPermission(item.key);
            const nextPermissions = await GetPlatformPermissions();
            setPermissions(nextPermissions);

            const updatedItem = nextPermissions.items.find((i: PermissionItem) => i.key === item.key);
            const isGranted = granted && (updatedItem?.granted ?? false);

            if (isGranted) {
                toast.success(t('settings:permissions.grantedToast', { label: localizedLabel }));
            } else {
                try {
                    await OpenPermissionSettings(item.key);
                } catch {
                    // Settings may not be openable on all platforms
                }
                toast.warning(
                    t('settings:permissions.stillNotGrantedToast', { label: localizedLabel }),
                );
            }
        } catch (error) {
            const message = error instanceof Error ? error.message : String(error);
            toast.error(
                t('settings:permissions.requestFailedToast', { label: localizedLabel, error: message }),
            );
        } finally {
            requestingRef.current = false;
            setRequestingKey(null);
        }
    }, [t]);

    const hasItems = (permissions?.items ?? []).length > 0;
    const requiredItems = (permissions?.items ?? []).filter((i) => i.required);
    const allGranted = permissions !== null && (requiredItems.length === 0 || requiredItems.every((i) => i.granted));

    return {
        permissions,
        loading,
        readFailed,
        refresh,
        requestPermission,
        requestingKey,
        hasItems,
        requiredItems,
        allGranted,
    };
}
