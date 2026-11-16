import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { isMac, isWindows } from '@/lib/platform';

// ── Types ──

export interface KeyCombo {
    meta: boolean;
    ctrl: boolean;
    alt: boolean;
    shift: boolean;
    key: string; // lowercase key name, e.g. 'b', 'enter'
}

export interface KeybindingAction {
    id: string;
    label: string;
    description: string;
    group: 'global' | 'composer' | 'vim';
    defaultCombo: KeyCombo;
    readonly: boolean;
    /** A developer shortcut: App.tsx binds it only while Developer tools are
     *  on, and Settings does not list it. */
    devOnly?: boolean;
}

// ── Defaults ──

/** Platform-aware primary modifier: ⌘ on macOS, Ctrl on Windows/Linux. */
function primaryCombo(shift: boolean, key: string): KeyCombo {
    return isMac
        ? { meta: true, ctrl: false, alt: false, shift, key }
        : { meta: false, ctrl: true, alt: false, shift, key };
}

/**
 * Default combo for the system-wide Quick Chat Spotlight hotkey. The
 * actual binding is persisted by the Go side (see quick_chat_hotkey_config.go)
 * and applied to the OS-level hook directly. Keyboard events for the
 * registered combo never reach the renderer — when the user rebinds it
 * here, we round-trip the new value to Go via SetQuickChatHotkey.
 *
 * Defaults preserve the original Isoform behaviour: macOS = double-tap
 * Option (sentinel key 'opt-opt'), Windows = Alt+Space. Users who hit
 * an OS conflict rebind in settings; we don't change the default for
 * everyone.
 */
function quickChatGlobalCombo(): KeyCombo {
    return isMac
        ? { meta: false, ctrl: false, alt: false, shift: false, key: 'opt-opt' }
        : { meta: false, ctrl: false, alt: true, shift: false, key: ' ' };
}

/**
 * The system-wide Pause observing shortcut. It is fixed in the Go platform
 * hooks (closedEyesHotkeyMatches in quick_chat_hotkey_darwin.go,
 * matchesClosedEyesHotkey in quick_chat_hotkey_windows.go). It toggles Closed
 * Eyes until explicitly resumed; the menu item uses a timed pause. Settings
 * lists it read-only. Linux has no hook, so there is nothing to list.
 */
function pauseObservingCombo(): KeyCombo | null {
    if (isMac) return { meta: true, ctrl: true, alt: true, shift: false, key: 'e' };
    if (isWindows) return { meta: false, ctrl: true, alt: true, shift: true, key: 'e' };
    return null;
}

/** Read-only: shown on Settings > General > Advanced and on Observation. */
export const PAUSE_OBSERVING_COMBO = pauseObservingCombo();

/** The pages ⌘1 to ⌘5 open, in the sidebar's order. */
export const PAGE_KEYS = ['today', 'chat', 'agents', 'activity', 'you'] as const;
export type PageKey = (typeof PAGE_KEYS)[number];

const DEFAULT_KEYBINDINGS: KeybindingAction[] = [
    // Global shortcuts
    {
        id: 'command-palette',
        label: 'Command palette',
        description: 'Open search and commands',
        group: 'global',
        defaultCombo: primaryCombo(false, 'k'),
        readonly: false,
    },
    {
        id: 'toggle-sidebar',
        label: 'Toggle sidebar',
        description: 'Show or hide the left sidebar',
        group: 'global',
        defaultCombo: primaryCombo(false, 'b'),
        readonly: false,
    },
    {
        id: 'open-settings',
        label: 'Open settings',
        description: 'Open the settings page',
        group: 'global',
        defaultCombo: primaryCombo(false, ','),
        readonly: false,
    },
    {
        id: 'keyboard-shortcuts',
        label: '',
        description: '',
        group: 'global',
        defaultCombo: primaryCombo(false, '/'),
        readonly: false,
    },
    {
        id: 'new-chat',
        label: 'New chat',
        description: 'Start a new chat session',
        group: 'global',
        defaultCombo: primaryCombo(false, 't'),
        readonly: false,
    },
    // The pages, in the sidebar's order (pass-access v1.0.0 rule 1).
    ...PAGE_KEYS.map((page, i) => ({
        id: `go-${page}`,
        label: '',
        description: '',
        group: 'global' as const,
        defaultCombo: primaryCombo(false, String(i + 1)),
        readonly: false,
    })),
    // Interface zoom, 90% to 150% (pass-access v1.0.0 rule 6). Cmd+Shift+=
    // (the + key) zooms in too; App.tsx maps it to zoom-in.
    { id: 'zoom-in', label: '', description: '', group: 'global', defaultCombo: primaryCombo(false, '='), readonly: false },
    { id: 'zoom-out', label: '', description: '', group: 'global', defaultCombo: primaryCombo(false, '-'), readonly: false },
    { id: 'zoom-reset', label: '', description: '', group: 'global', defaultCombo: primaryCombo(false, '0'), readonly: false },
    {
        id: 'replay-onboarding',
        label: 'Run setup again',
        description: 'Restart the onboarding flow (debug)',
        group: 'global',
        defaultCombo: primaryCombo(true, 'o'),
        readonly: false,
        devOnly: true,
    },
    {
        // OS-level binding: matched by the Go-side platform hook, not the
        // browser keydown listener. Recording in settings still works the
        // same — the recorded combo is mirrored down to Go via
        // SetQuickChatHotkey so the OS hook fires it from any app.
        id: 'quick-chat-global',
        label: 'Quick Chat',
        description: 'Summon the Quick Chat Spotlight from any app',
        group: 'global',
        defaultCombo: quickChatGlobalCombo(),
        readonly: false,
    },
    ...(PAUSE_OBSERVING_COMBO
        ? [
              {
                  id: 'pause-observing',
                  label: 'Pause observing',
                  description: 'Pause or resume screen and audio observation from any app',
                  group: 'global' as const,
                  defaultCombo: PAUSE_OBSERVING_COMBO,
                  readonly: true,
              },
          ]
        : []),
    // Composer
    {
        id: 'send-prompt',
        label: 'Send prompt',
        description: 'Submit the current message',
        group: 'composer',
        defaultCombo: { meta: false, ctrl: false, alt: false, shift: false, key: 'enter' },
        readonly: false,
    },
];

/** Vim navigation bindings — display-only, handled by useVimScroll. */
export const VIM_BINDINGS: KeybindingAction[] = [
    {
        id: 'vim-scroll-down',
        label: 'Scroll down',
        description: 'Scroll down by one step',
        group: 'vim',
        defaultCombo: { meta: false, ctrl: false, alt: false, shift: false, key: 'j' },
        readonly: true,
    },
    {
        id: 'vim-scroll-up',
        label: 'Scroll up',
        description: 'Scroll up by one step',
        group: 'vim',
        defaultCombo: { meta: false, ctrl: false, alt: false, shift: false, key: 'k' },
        readonly: true,
    },
    {
        id: 'vim-half-page-down',
        label: 'Half page down',
        description: 'Scroll down half a page',
        group: 'vim',
        defaultCombo: { meta: false, ctrl: true, alt: false, shift: false, key: 'd' },
        readonly: true,
    },
    {
        id: 'vim-half-page-up',
        label: 'Half page up',
        description: 'Scroll up half a page',
        group: 'vim',
        defaultCombo: { meta: false, ctrl: true, alt: false, shift: false, key: 'u' },
        readonly: true,
    },
    {
        id: 'vim-full-page-down',
        label: 'Full page down',
        description: 'Scroll down a full page',
        group: 'vim',
        defaultCombo: { meta: false, ctrl: true, alt: false, shift: false, key: 'f' },
        readonly: true,
    },
    {
        id: 'vim-full-page-up',
        label: 'Full page up',
        description: 'Scroll up a full page',
        group: 'vim',
        defaultCombo: { meta: false, ctrl: true, alt: false, shift: false, key: 'b' },
        readonly: true,
    },
    {
        id: 'vim-scroll-bottom',
        label: 'Scroll to bottom',
        description: 'Jump to the bottom of the chat',
        group: 'vim',
        defaultCombo: { meta: false, ctrl: false, alt: false, shift: true, key: 'g' },
        readonly: true,
    },
    {
        id: 'vim-scroll-top',
        label: 'Scroll to top',
        description: 'Jump to the top (press g twice)',
        group: 'vim',
        defaultCombo: { meta: false, ctrl: false, alt: false, shift: false, key: 'gg' },
        readonly: true,
    },
    {
        id: 'vim-insert-mode',
        label: 'Insert mode',
        description: 'Move the cursor to the chat box',
        group: 'vim',
        defaultCombo: { meta: false, ctrl: false, alt: false, shift: false, key: 'i' },
        readonly: true,
    },
];

// ── Helpers ──

const KEYBINDINGS_STORAGE_KEY = 'yansu.keybindings';

function loadOverrides(): Record<string, KeyCombo> {
    try {
        const raw = localStorage.getItem(KEYBINDINGS_STORAGE_KEY);
        if (!raw) return {};
        return JSON.parse(raw) as Record<string, KeyCombo>;
    } catch {
        return {};
    }
}

function saveOverrides(overrides: Record<string, KeyCombo>) {
    try {
        localStorage.setItem(KEYBINDINGS_STORAGE_KEY, JSON.stringify(overrides));
    } catch {
        // ignore
    }
}

export function comboEquals(a: KeyCombo, b: KeyCombo): boolean {
    return (
        a.meta === b.meta &&
        a.ctrl === b.ctrl &&
        a.alt === b.alt &&
        a.shift === b.shift &&
        a.key === b.key
    );
}

export function eventMatchesCombo(e: KeyboardEvent, combo: KeyCombo): boolean {
    return (
        e.metaKey === combo.meta &&
        e.ctrlKey === combo.ctrl &&
        e.altKey === combo.alt &&
        e.shiftKey === combo.shift &&
        e.key.toLowerCase() === combo.key
    );
}

const KEY_DISPLAY: Record<string, string> = {
    enter: '↵',
    escape: 'Esc',
    arrowup: '↑',
    arrowdown: '↓',
    arrowleft: '←',
    arrowright: '→',
    backspace: '⌫',
    delete: '⌦',
    tab: '⇥',
    ' ': 'Space',
    gg: 'gg',
    'opt-opt': '⌥⌥',
};

export function comboToDisplayString(combo: KeyCombo): string {
    const parts: string[] = [];
    if (isMac) {
        if (combo.ctrl) parts.push('⌃');
        if (combo.alt) parts.push('⌥');
        if (combo.shift) parts.push('⇧');
        if (combo.meta) parts.push('⌘');
        parts.push(KEY_DISPLAY[combo.key] ?? combo.key.toUpperCase());
        return parts.join('');
    }
    // Windows / Linux: readable labels with "+" separator
    if (combo.ctrl) parts.push('Ctrl');
    if (combo.alt) parts.push('Alt');
    if (combo.shift) parts.push('Shift');
    if (combo.meta) parts.push('Win');
    parts.push(KEY_DISPLAY[combo.key] ?? combo.key.toUpperCase());
    return parts.join('+');
}

export function eventToCombo(e: KeyboardEvent): KeyCombo | null {
    if (['Meta', 'Control', 'Alt', 'Shift'].includes(e.key)) return null;
    return {
        meta: e.metaKey,
        ctrl: e.ctrlKey,
        alt: e.altKey,
        shift: e.shiftKey,
        key: e.key.toLowerCase(),
    };
}

// ── Hook ──

export function useKeybindings() {
    const { t } = useTranslation('settings');
    const actions = useMemo(() => DEFAULT_KEYBINDINGS.map((action) => {
        const translationId = action.id === 'pause-observing' && isMac ? 'pause-observing-one-hour' : action.id;
        return {
            ...action,
            label: t(`keybindings.commands.${translationId}.label` as never) as string,
            description: t(`keybindings.commands.${translationId}.description` as never) as string,
        };
    }), [t]);
    const [overrides, setOverrides] = useState<Record<string, KeyCombo>>(loadOverrides);

    useEffect(() => {
        saveOverrides(overrides);
    }, [overrides]);

    const getCombo = useCallback(
        (actionId: string): KeyCombo | undefined => {
            if (overrides[actionId]) return overrides[actionId];
            return DEFAULT_KEYBINDINGS.find((a) => a.id === actionId)?.defaultCombo;
        },
        [overrides],
    );

    const updateBinding = useCallback((actionId: string, combo: KeyCombo) => {
        setOverrides((prev) => ({ ...prev, [actionId]: combo }));
    }, []);

    const resetBinding = useCallback((actionId: string) => {
        setOverrides((prev) => {
            const next = { ...prev };
            delete next[actionId];
            return next;
        });
    }, []);

    const resetAll = useCallback(() => {
        setOverrides({});
    }, []);

    const findMatchingAction = useCallback(
        (e: KeyboardEvent): string | null => {
            for (const action of DEFAULT_KEYBINDINGS) {
                if (action.readonly) continue;
                const combo = overrides[action.id] ?? action.defaultCombo;
                if (eventMatchesCombo(e, combo)) return action.id;
            }
            return null;
        },
        [overrides],
    );

    // Read-only entries still count here: the OS-level Pause observing
    // shortcut fires from any app, so nothing else may take its keys.
    const findConflict = useCallback(
        (candidateCombo: KeyCombo, excludeActionId: string): string | null => {
            for (const action of DEFAULT_KEYBINDINGS) {
                if (action.id === excludeActionId) continue;
                const combo = overrides[action.id] ?? action.defaultCombo;
                if (comboEquals(combo, candidateCombo)) return action.id;
            }
            return null;
        },
        [overrides],
    );

    const isCustomized = useCallback(
        (actionId: string): boolean => actionId in overrides,
        [overrides],
    );

    // Memoize the returned API so consumer useCallback/useEffect dependencies
    // remain stable across unrelated re-renders. Without this, the shortcut
    // recording listener in ShortcutRow tears down its 50 ms timer faster than
    // it can attach, and new keybindings never get captured.
    return useMemo(
        () => ({
            actions,
            vimActions: VIM_BINDINGS,
            getCombo,
            updateBinding,
            resetBinding,
            resetAll,
            findMatchingAction,
            findConflict,
            isCustomized,
        }),
        [
            actions,
            getCombo,
            updateBinding,
            resetBinding,
            resetAll,
            findMatchingAction,
            findConflict,
            isCustomized,
        ],
    );
}
