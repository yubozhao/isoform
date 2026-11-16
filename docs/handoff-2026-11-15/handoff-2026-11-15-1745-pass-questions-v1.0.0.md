# Pass questions v1.0.0: four builder questions on the phone and computer passes, decided

Version 1.0.0 · written November 15, 2026, 5:45 PM Pacific, by Claude Design.

**The hub, November 15:** the passes are mirrored in the repo (docs/handoff-2026-11-15, PR #1105) and eight sessions are building them; the builders raised four questions. Each answer below changes the file it names, from now on; that file isn't edited.

A change goes in a new file with a new version; this one isn't edited after it's written.

## 1. Closed Eyes shortcut: one shortcut, ⌘⌃⌥E, everywhere

1. **Closed Eyes has one shortcut, ⌘⌃⌥E,** global and in the app, and that's the key shown on the sidebar button, in the menu bar popover and in Settings › Closed Eyes. ⌘⇧E goes everywhere it was written (computer-map rule 2, computer-settings v1.0.1 rule 8, the board).
2. **Why:** one action has one key to learn, and the key on the button must work wherever you are; ⌘⇧E belongs to other apps (VS Code's Explorer, among others), and Isoform never takes a common shortcut from them.

## 2. The mark's poses at the sidebar's foot: already drawn

3. **The poses exist** in `watch-1.2.0/mark-poses.json` (watch v1.2.0), in the mark kit's own format (mark-motion-1.0.0/mark-kit.js): **listening** (groucho, the eye lifted, open), **notListening** (logo, shut), **closedEyes** (cap, shut), **rest**. Port them into MarkMotion from that file; nothing new to draw.
4. **At the sidebar's foot the watch line shows one still pose,** 16 points, no ripple and no idle moments: listening while the watch listens, notListening when it doesn't, closedEyes during Closed Eyes. The phone line keeps the phone icon, not the mark. Words stay beside the pose ("Watch · listening"), so it never relies on the pose alone.

## 3. "Listen on your watch" on the phone: keep the switch, off only

5. **Keep the switch, and narrow phone-watch rule 4:** the phone can turn listening off for good, never start it.
6. **Off takes effect at once,** not at the next sync: the computer drops any watch audio heard after the moment it was turned off (the same guard as a pause window, watch v1.1.0), and the watch stops when it next syncs. The row's line says "Off. Your watch stops when it next syncs; nothing heard from now is kept."
7. **Turning it back on from the phone** shows "Tap the mark on your watch to start listening." and the switch stays off until the watch reports it's listening, because watchOS can't be relied on to start listening from the phone (the reason for rule 4).

## 4. The brief card in Chat: it leaves Chat; no reserved space anywhere

8. **The brief card doesn't belong in Chat any more:** the brief lives on Today (redesign-work rule 8), and Chat never speaks first (phone-chat rule 3, computer-chat rule 6). Remove the card from Chat on both devices rather than reserving its row.
9. **On Today the brief draws whole from the first read,** with Hear it or Read it in place (B1: the brief package whole and first). No reserved gap, as decided on November 5 and 6 (chat R38 review; PR #646 review): a card never shows empty space under its words.

## Every word

| Key | English | Where |
| --- | --- | --- |
| watch.listenOff | Off. Your watch stops when it next syncs; nothing heard from now is kept. | Phone › Watch › Listen on your watch, just turned off |
| watch.listenOnFromPhone | Tap the mark on your watch to start listening. | The same row, when turned on from the phone |

## Proof

On named builds on Bo's computer, phone and watch:

1. **One shortcut:** ⌘⌃⌥E from Xcode closes Isoform's eyes; ⌘⇧E in VS Code still opens its Explorer; the sidebar button shows ⌘⌃⌥E.
2. **Poses:** with the watch listening, then Closed Eyes on, a screenshot of the sidebar's foot shows the lifted eye, then the cap.
3. **Off from the phone:** turned off at a noted time with the watch out of reach; when it syncs, Activity shows nothing heard after that time.
4. **No brief card in Chat:** after the evening brief, Chat has no card; Today shows the brief whole on first paint.

## On the map

- shell SH5 now says ⌘⌃⌥E.
- phone P176 and P234: Listen on your watch turns off from the phone at once (the computer drops what's heard after), and never starts from the phone.
