import { NativeClipboardSetText } from '../../wailsjs/go/main/App';
import { ClipboardSetText } from '../../wailsjs/runtime/runtime';

// The clipboard helper the day digest card used to share. The card left
// Activity (computer-conformance-answers A4: Activity is the record, the
// evening brief is the day's summary); About you's portrait still copies
// through this.

/** Copy via the cgo NSPasteboard bridge when available. Wails runtime
 *  ClipboardSetText shells out to `pbcopy`; when the app is launched from
 *  Finder the child process inherits no LANG/LC_CTYPE, so UTF-8 bytes get
 *  written to NSPasteboard as MacRoman — Chinese pastes as mojibake. The
 *  bridge is preferred; pbcopy is the non-darwin fallback. */
/** Resolves false when neither clipboard path took the text. */
export async function copyTextToClipboard(text: string): Promise<boolean> {
    try {
        await NativeClipboardSetText(text);
        return true;
    } catch { /* fall through to pbcopy path */ }
    try {
        await ClipboardSetText(text);
        return true;
    } catch {
        return false;
    }
}
