import { useRef, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Switch } from '@/components/ui/switch';
import {
    QUICK_CHAT_GLOBAL_ID,
    ShortcutRow,
    useQuickChatHotkeyHydration,
    useShortcutEditor,
    type Keybindings,
} from '@/components/keybindings-settings';
import { SettingsFailureLine } from '@/components/settings/settings-failure';
import {
    comboToDisplayString,
    comboToSpokenString,
    HEAR_FIXED_KEY,
    ITEM_FIXED_KEYS,
    NEW_LINE_FIXED_KEY,
    PAGE_KEYS,
    type KeyCombo,
} from '@/hooks/useKeybindings';
import { isMac, isWindows } from '@/lib/platform';

// The item, brief and New line keys are Today's and Chat's handlers. The
// sheet lists them so they can be learned and binds nothing (computer-keys
// v1.0.0 r2); a binding may not take them (findFixedKeyClash).

/** In Isoform, in the design's order: the pages, then Find, the sidebar,
 *  Settings and this sheet. */
const IN_APP_IDS = [
    ...PAGE_KEYS.map((page) => `go-${page}`),
    'command-palette',
    'toggle-sidebar',
    'open-settings',
    'keyboard-shortcuts',
];

interface KeysSheetProps {
    open: boolean;
    onOpenChange: (open: boolean) => void;
    keybindings: Keybindings;
    vimMode: boolean;
    onVimModeChange: (enabled: boolean) => void;
}

/**
 * Keyboard shortcuts (computer-keys v1.0.0): every key in one sheet, grouped
 * by where it works, opened with Cmd+/ over whatever is in front. Done or Esc
 * closes it and focus goes back to where it was.
 */
export function KeysSheet({ open, onOpenChange, keybindings, vimMode, onVimModeChange }: KeysSheetProps) {
    const { t } = useTranslation('settings');
    const contentRef = useRef<HTMLDivElement>(null);
    // Where focus was when the sheet opened: Cmd+/ opens it from anywhere,
    // with no trigger for Radix to return to, so Esc and Done go back here.
    const returnFocusRef = useRef<HTMLElement | null>(null);
    const wasOpenRef = useRef(false);
    if (open && !wasOpenRef.current && typeof document !== 'undefined') {
        returnFocusRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    }
    wasOpenRef.current = open;

    return (
        <Dialog open={open} onOpenChange={onOpenChange}>
            <DialogContent
                ref={contentRef}
                aria-describedby={undefined}
                className="w-[560px] max-w-[calc(100vw-48px)]"
                onOpenAutoFocus={(event) => {
                    // Focus starts on the first Change (pass-access v1.1.0 r5).
                    const first = contentRef.current?.querySelector<HTMLElement>('[data-keys-change]');
                    if (!first) return;
                    event.preventDefault();
                    first.focus();
                }}
                onCloseAutoFocus={(event) => {
                    const target = returnFocusRef.current;
                    if (!target?.isConnected) return;
                    event.preventDefault();
                    target.focus();
                }}
            >
                <DialogHeader>
                    <DialogTitle>{t('keys.title')}</DialogTitle>
                </DialogHeader>
                <KeysSheetBody keybindings={keybindings} vimMode={vimMode} onVimModeChange={onVimModeChange} />
                <div className="flex shrink-0 justify-end border-t border-border px-6 py-4">
                    <Button size="sm" onClick={() => onOpenChange(false)}>
                        {t('keys.done')}
                    </Button>
                </div>
            </DialogContent>
        </Dialog>
    );
}

function KeysSheetBody({ keybindings, vimMode, onVimModeChange }: Omit<KeysSheetProps, 'open' | 'onOpenChange'>) {
    const { t } = useTranslation('settings');
    const editor = useShortcutEditor(keybindings);
    useQuickChatHotkeyHydration(keybindings);

    const row = (id: string, note?: string) => {
        const action = keybindings.actions.find((entry) => entry.id === id);
        if (!action) return null;
        // Quick chat is an OS hook on macOS and Windows only.
        if (id === QUICK_CHAT_GLOBAL_ID && !isMac && !isWindows) return null;
        return (
            <ShortcutRow
                key={id}
                action={action}
                currentCombo={keybindings.getCombo(id) ?? action.defaultCombo}
                isCustomized={keybindings.isCustomized(id)}
                recording={editor.recordingId === id}
                note={note}
                clashRow={editor.clash?.actionId === id ? editor.clash.row : undefined}
                onRecord={editor.startRecording}
                onCancelRecord={editor.cancelRecording}
                onSaveCombo={editor.saveCombo}
                onReset={editor.resetBinding}
            />
        );
    };

    return (
        <div className="flex min-h-0 flex-1 flex-col gap-5 overflow-y-auto px-6 py-4">
            <KeysGroup title={t('keys.anywhere')}>
                {row(QUICK_CHAT_GLOBAL_ID)}
                {row('pause-observing', t('keys.always'))}
            </KeysGroup>
            <KeysGroup title={t('keys.inApp')}>{IN_APP_IDS.map((id) => row(id))}</KeysGroup>
            <KeysGroup title={t('keys.onItem')}>
                {ITEM_FIXED_KEYS.map(({ labelKey, combos }) => (
                    <FixedRow key={labelKey} name={t(labelKey as never) as string} combos={combos} />
                ))}
            </KeysGroup>
            <KeysGroup title={t('keys.brief')}>
                <FixedRow name={t('keys.hear')} combos={HEAR_FIXED_KEY.combos} />
            </KeysGroup>
            <KeysGroup title={t('keys.chat')}>
                {row('send-prompt')}
                <FixedRow name={t('keys.newLine')} combos={NEW_LINE_FIXED_KEY.combos} />
            </KeysGroup>
            <SettingsFailureLine failure={editor.failure} />

            <div className="flex flex-col gap-3 border-t border-border pt-4">
                <div className="flex items-center justify-between gap-4">
                    <div className="flex min-w-0 flex-col">
                        <span className="text-[13px] text-ink">{t('keys.vim')}</span>
                        <span className="text-xs text-muted">{t('keys.vimSub')}</span>
                    </div>
                    <Switch checked={vimMode} onCheckedChange={onVimModeChange} aria-label={t('keys.vim')} />
                </div>
                <p className="text-xs text-muted">{t('keys.focusNote')}</p>
            </div>
        </div>
    );
}

function KeysGroup({ title, children }: { title: string; children: ReactNode }) {
    return (
        <section aria-label={title} data-keys-group={title} className="flex flex-col">
            <h3 className="pb-1 text-xs font-medium text-muted">{title}</h3>
            <div className="flex flex-col">{children}</div>
        </section>
    );
}

/** A key that is listed so it can be learned, with no Change. */
function FixedRow({ name, combos }: { name: string; combos: KeyCombo[] }) {
    const { t } = useTranslation('settings');
    const keys = combos.map(comboToDisplayString).join('');
    const spoken = combos.map((combo) => comboToSpokenString(combo, t as (key: string) => string)).join(' ');
    return (
        <div className="flex items-center justify-between gap-4 border-b border-border py-2.5 last:border-b-0">
            <span className="min-w-0 truncate text-[13px] text-ink">{name}</span>
            <kbd
                aria-label={t('keys.fixedRowLabel', { name, keys: spoken })}
                className="inline-flex shrink-0 items-center gap-1 rounded-md bg-ink/5 px-2.5 py-1 font-mono text-xs text-muted"
            >
                {keys}
            </kbd>
        </div>
    );
}
