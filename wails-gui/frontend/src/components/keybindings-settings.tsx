import { useState, useEffect, useCallback, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { RotateCcw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { SettingsCard, SettingsRow } from '@/components/settings/settings-primitives';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import {
    comboEquals,
    comboToDisplayString,
    comboToSpokenString,
    eventToCombo,
    findFixedKeyClash,
    type KeybindingAction,
    type KeyCombo,
    type useKeybindings,
} from '@/hooks/useKeybindings';
import { useSettingsFailure } from '@/components/settings/settings-failure';
import {
    GetQuickChatHotkey,
    SetQuickChatHotkey,
    SetQuickChatHotkeyRecording,
} from '../../wailsjs/go/main/App';
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { isMac, isWindows } from '@/lib/platform';

export const QUICK_CHAT_GLOBAL_ID = 'quick-chat-global';

// Keep the persisted KeyCombo shape, including sentinels for double tap and Off.
const QUICK_CHAT_PRESETS = [
    { id: 'optionTwice', combo: { meta: false, ctrl: false, alt: false, shift: false, key: 'opt-opt' } },
    { id: 'altSpace', combo: { meta: false, ctrl: false, alt: true, shift: false, key: ' ' } },
    { id: 'controlSpace', combo: { meta: false, ctrl: true, alt: false, shift: false, key: ' ' } },
    { id: 'commandShiftSpace', combo: { meta: true, ctrl: false, alt: false, shift: true, key: ' ' } },
    { id: 'off', combo: { meta: false, ctrl: false, alt: false, shift: false, key: 'off' } },
] as const;

export type Keybindings = ReturnType<typeof useKeybindings>;

// ── Shared recording and saving ──

/** A recorded combo that another row already uses: shown under the row
 *  that was recording, and nothing is saved (computer-keys v1.0.0 r4). */
export interface ShortcutClash {
    actionId: string;
    /** The name of the row that already has these keys. */
    row: string;
}

// One recorder per surface: which row is listening for keys, plus the save
// and reset rules shared by every shortcut (conflict check, and the Quick
// Chat round trip to the Go hook).
export function useShortcutEditor(keybindings: Keybindings) {
    const { t } = useTranslation('settings');
    const [recordingId, setRecordingId] = useState<string | null>(null);
    const [clash, setClash] = useState<ShortcutClash | null>(null);
    const { failure, fail, clear } = useSettingsFailure();

    const startRecording = useCallback((actionId: string) => {
        setClash(null);
        setRecordingId(actionId);
    }, []);

    const cancelRecording = useCallback(() => {
        setRecordingId(null);
    }, []);

    const saveCombo = useCallback(
        (actionId: string, combo: KeyCombo) => {
            const fixed = findFixedKeyClash(combo);
            if (fixed) {
                setClash({ actionId, row: t(fixed.labelKey as never) as string });
                setRecordingId(null);
                return;
            }
            const conflict = keybindings.findConflict(combo, actionId);
            if (conflict) {
                setClash({
                    actionId,
                    row: keybindings.actions.find((entry) => entry.id === conflict)?.label ?? conflict,
                });
                setRecordingId(null);
                return;
            }
            setClash(null);
            // Quick Chat is OS-level: the renderer never sees its keystrokes,
            // so we push the combo down to Go which retunes the platform hook
            // (Windows WH_KEYBOARD_LL / macOS CGEventTap). At least one
            // modifier is required by the Go validator; reject early with a
            // clearer message than the binding error.
            if (actionId === QUICK_CHAT_GLOBAL_ID) {
                if (!combo.meta && !combo.ctrl && !combo.alt && !combo.shift) {
                    fail(t('keybindings.toasts.modifierRequired'));
                    return;
                }
                const apply = () =>
                    SetQuickChatHotkey(combo).then(
                        () => {
                            keybindings.updateBinding(actionId, combo);
                            setRecordingId(null);
                            clear();
                        },
                        () => fail(t('settingChange.failed'), () => void apply()),
                    );
                void apply();
                return;
            }
            keybindings.updateBinding(actionId, combo);
            setRecordingId(null);
            clear();
        },
        [keybindings, t, fail, clear],
    );

    const quickChatDefault = keybindings.actions.find(
        (a) => a.id === QUICK_CHAT_GLOBAL_ID,
    )?.defaultCombo;

    const resetBinding = useCallback(
        (actionId: string) => {
            if (actionId === QUICK_CHAT_GLOBAL_ID && quickChatDefault) {
                // Push the default down to Go FIRST so the OS hook is
                // already retuned by the time the UI flips back.
                const reset = () =>
                    SetQuickChatHotkey(quickChatDefault).then(
                        () => {
                            keybindings.resetBinding(actionId);
                            clear();
                        },
                        () => fail(t('settingChange.failed'), () => void reset()),
                    );
                void reset();
                return;
            }
            keybindings.resetBinding(actionId);
            clear();
        },
        [keybindings, quickChatDefault, t, fail, clear],
    );

    return { recordingId, startRecording, cancelRecording, saveCombo, resetBinding, failure, clash };
}

/** Reads the Quick Chat hotkey the Go side persisted into `keybindings` once
 *  on mount, and reports whether that read is still running. */
export function useQuickChatHotkeyHydration(keybindings: Keybindings): boolean {
    const [loading, setLoading] = useState(true);

    // Hydrate the OS-level Quick Chat hotkey from Go on mount. The Go
    // side persists this binding to ~/.yansu-agent/quick_chat_hotkey.json
    // and is the source of truth for what the OS hook actually fires on,
    // so localStorage gets reconciled to it rather than the other way
    // around. Without this, a user who changed the combo on one machine
    // and synced their settings from another would see a stale value
    // here while the hotkey keeps firing on the persisted one.
    useEffect(() => {
        if (!isMac && !isWindows) return;
        let cancelled = false;
        void (async () => {
            try {
                const persisted = await GetQuickChatHotkey();
                if (cancelled) return;
                const current = keybindings.getCombo(QUICK_CHAT_GLOBAL_ID);
                if (persisted && persisted.key && (!current || !comboEquals(current, persisted))) {
                    keybindings.updateBinding(QUICK_CHAT_GLOBAL_ID, persisted);
                }
            } catch {
                // Non-fatal: dev server reload race, missing binding, etc.
            } finally {
                if (!cancelled) setLoading(false);
            }
        })();
        return () => {
            cancelled = true;
        };
        // We intentionally only run this on mount.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);
    return loading;
}

// ── Quick Chat shortcut (General, everyday) ──

interface QuickChatShortcutCardProps {
    keybindings: Keybindings;
}

/** The system-wide Quick Chat hotkey, on its own card because it is the one
 *  shortcut most people change. */
export function QuickChatShortcutCard({ keybindings }: QuickChatShortcutCardProps) {
    const { t } = useTranslation('settings');
    const action = keybindings.actions.find((a) => a.id === QUICK_CHAT_GLOBAL_ID);
    const loading = useQuickChatHotkeyHydration(keybindings);
    const [saving, setSaving] = useState(false);
    const savingRef = useRef(false);
    const { failure, fail, clear } = useSettingsFailure();

    if (!action || (!isMac && !isWindows)) return null;

    const current = keybindings.getCombo(action.id) ?? action.defaultCombo;
    const presets = QUICK_CHAT_PRESETS.filter(({ id }) => isMac ? id !== 'altSpace' : id !== 'optionTwice');
    const preset = presets.find(({ combo }) => comboEquals(combo, current));
    const savePreset = async (id: string) => {
        const next = presets.find((entry) => entry.id === id);
        if (!next || savingRef.current) return;
        const conflict = keybindings.findConflict(next.combo, action.id);
        if (conflict) {
            fail(t('keybindings.toasts.conflict', {
                action: keybindings.actions.find((entry) => entry.id === conflict)?.label,
            }));
            return;
        }
        savingRef.current = true;
        setSaving(true);
        try {
            await SetQuickChatHotkey(next.combo);
            keybindings.updateBinding(action.id, next.combo);
            clear();
        } catch {
            fail(t('settingChange.failed'), () => void savePreset(id));
        } finally {
            savingRef.current = false;
            setSaving(false);
        }
    };

    return (
        <SettingsCard>
            <SettingsRow
                title={t('keybindings.quickChat.title')}
                description={t('keybindings.quickChat.description')}
                failure={failure}
                control={
                    <Select value={preset?.id ?? ''} onValueChange={(id) => void savePreset(id)} disabled={loading || saving}>
                        <SelectTrigger className="w-[240px] text-xs" aria-label={t('keybindings.quickChat.title')}>
                            <SelectValue placeholder={t('keybindings.quickChat.savedShortcut')} />
                        </SelectTrigger>
                        <SelectContent>
                            {presets.map(({ id }) => (
                                <SelectItem key={id} value={id} className="text-xs">
                                    {isWindows && id === 'commandShiftSpace' ? t('keybindings.quickChat.presets.windowsShiftSpace') : t(`keybindings.quickChat.presets.${id}`)}
                                </SelectItem>
                            ))}
                        </SelectContent>
                    </Select>
                }
            />
        </SettingsCard>
    );
}

// ── ShortcutRow ──

interface ShortcutRowProps {
    action: KeybindingAction;
    currentCombo: KeyCombo;
    isCustomized: boolean;
    recording: boolean;
    /** Words after the keys, such as "always this one" on Closed Eyes. */
    note?: string;
    /** "Already used by {row}." under the row; nothing was saved. */
    clashRow?: string;
    onRecord: (actionId: string) => void;
    onCancelRecord: () => void;
    onSaveCombo: (actionId: string, combo: KeyCombo) => void;
    onReset: (actionId: string) => void;
}

/** One shortcut in the Keyboard shortcuts sheet: its name, its keys and,
 *  unless it is fixed, a Change button that records new keys. */
export function ShortcutRow({
    action,
    currentCombo,
    isCustomized,
    recording,
    note,
    clashRow,
    onRecord,
    onCancelRecord,
    onSaveCombo,
    onReset,
}: ShortcutRowProps) {
    const { t } = useTranslation('settings');
    const rowRef = useRef<HTMLDivElement>(null);
    const isQuickChat = action.id === QUICK_CHAT_GLOBAL_ID;

    // Recording mode: capture keydown.
    //
    // For most actions, a window-level keydown listener is enough. For
    // quick-chat-global we also engage the Go-side hook so the renderer
    // can record combos the OS would otherwise eat before they reach the
    // browser (Alt+Space → system menu, Win+L → lock screen, etc.). The
    // hook captures every non-modifier key during recording, snapshots
    // the live modifier state, swallows the keystroke, and emits the
    // combo via the `quickChat:hotkeyRecorded` Wails event. Escape is
    // forwarded the same way so cancel still works.
    useEffect(() => {
        if (!recording) return;

        const handleKeyDown = (e: KeyboardEvent) => {
            e.preventDefault();
            e.stopPropagation();

            if (e.key === 'Escape') {
                onCancelRecord();
                return;
            }

            const combo = eventToCombo(e);
            if (combo) {
                onSaveCombo(action.id, combo);
            }
        };

        const timeout = setTimeout(() => {
            window.addEventListener('keydown', handleKeyDown, true);
        }, 50);

        let offRecorded: (() => void) | undefined;
        if (isQuickChat) {
            void SetQuickChatHotkeyRecording(true);
            offRecorded = EventsOn('quickChat:hotkeyRecorded', (combo: KeyCombo) => {
                if (
                    combo.key === 'escape' &&
                    !combo.meta && !combo.ctrl && !combo.alt && !combo.shift
                ) {
                    onCancelRecord();
                    return;
                }
                onSaveCombo(action.id, combo);
            });
        }

        return () => {
            clearTimeout(timeout);
            window.removeEventListener('keydown', handleKeyDown, true);
            if (isQuickChat) {
                void SetQuickChatHotkeyRecording(false);
                offRecorded?.();
            }
        };
    }, [recording, isQuickChat, action.id, onCancelRecord, onSaveCombo]);

    // Click outside to cancel recording
    useEffect(() => {
        if (!recording) return;

        const handleClick = (e: MouseEvent) => {
            if (rowRef.current && !rowRef.current.contains(e.target as Node)) {
                onCancelRecord();
            }
        };

        // Delay to avoid the initial click that started recording
        const timeout = setTimeout(() => {
            document.addEventListener('mousedown', handleClick);
        }, 50);

        return () => {
            clearTimeout(timeout);
            document.removeEventListener('mousedown', handleClick);
        };
    }, [recording, onCancelRecord]);

    const label = t(`keybindings.commands.${action.id}.label` as never) as string;
    const keys = comboToDisplayString(currentCombo);
    const spoken = comboToSpokenString(currentCombo, t as (key: string) => string);

    return (
        <div ref={rowRef} className="flex flex-col border-b border-border py-2.5 last:border-b-0">
            <div className="flex items-center justify-between gap-4">
                <span className="min-w-0 truncate text-[13px] text-ink">{label}</span>
                <div className="flex shrink-0 items-center gap-2">
                    {note && <span className="text-xs text-subtle">{note}</span>}
                    <kbd
                        aria-hidden={!action.readonly}
                        aria-label={action.readonly ? t('keys.fixedRowLabel', { name: label, keys: spoken }) : undefined}
                        className={`inline-flex items-center gap-1 rounded-md px-2.5 py-1 font-mono text-xs transition-all ${
                            recording
                                ? 'animate-pulse border border-accent-2 bg-accent-2/5 text-accent-2 ring-1 ring-accent-2/40'
                                : action.readonly
                                  ? 'bg-ink/5 text-muted'
                                  : 'border border-border bg-background text-ink'
                        }`}
                    >
                        {recording ? t('keybindings.recordPlaceholder') : keys}
                    </kbd>
                    {isCustomized && !action.readonly && !recording && (
                        <button
                            type="button"
                            onClick={() => onReset(action.id)}
                            className="rounded-md p-1 text-muted transition-colors hover:bg-ink/[0.03] hover:text-ink"
                            title={t('keybindings.resetTooltip')}
                            aria-label={t('keybindings.resetTooltip')}
                        >
                            <RotateCcw className="h-3 w-3" />
                        </button>
                    )}
                    {!action.readonly && (
                        <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            data-keys-change=""
                            aria-label={t('keys.rowLabel', { name: label, keys: spoken })}
                            onClick={() => (recording ? onCancelRecord() : onRecord(action.id))}
                        >
                            {t('keys.change')}
                        </Button>
                    )}
                </div>
            </div>
            <span className="sr-only" aria-live="polite">
                {recording ? t('keys.recording', { keys: spoken }) : ''}
            </span>
            {clashRow && (
                <p role="alert" className="mt-1 text-[12px] text-copper">
                    {t('keys.clash', { row: clashRow })}
                </p>
            )}
        </div>
    );
}
