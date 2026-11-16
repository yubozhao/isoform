# Phone theme v1.0.0: the phone pass in dark mode, by Theme.swift; one bar that broke rule 12

Version 1.0.0 · written November 15, 2026, 8:58 PM Pacific, by Claude Design.

**Why this file:** the phone pass (phone-map v1.0.0 and after) was drawn light only, in colours close to iOS's system greys rather than the app's own. The app already has both themes in `ios/Yansu/Theme.swift` (Paper and One Dark, from docs/design-system/tokens.json; read on feat/iphone-companion c1f0e8bcf, read-only). This file names the Theme token for every colour on the Isoform iPhone board, adds dark frames, and fixes one drawing that broke AGENTS.md rule 12.

**Board:** Isoform iPhone (https://claude.ai/artifact/WKCdoNzqXTrsGG4aerRFrs), new artboard 12 (Dark): Today, the hand-off sheet, an agents' request, Watch, Chat, Settings. Artboard 5 (Watch) and 8 (Settings) updated.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Every page of the phone pass works in both appearances** from its first build.
2. **Builders use Theme's names,** never the board's hex.

## 1. The board's colours, by Theme name

| On the board (light) | Theme | Light | Dark |
| --- | --- | --- | --- |
| Screen behind the cards (#f2f2f7 on the board) | `background` | #f6f6f9 | #121217 |
| Cards, sheets, grouped rows (#fff) | `surface` | #ffffff | #1a1a21 |
| Grey box inside a card: the work, What would move (#f4f4f7) | `surfaceWarm` | #f0f1f6 | #1f1f27 |
| Agent badges, the person button, your Chat message wash (#ececf0, #e3e5f7) | `surfaceUser` | #ebecf2 | #26262f |
| Lines between rows (#e5e5ea) | `border` | #e2e3ec | #2a2b35 |
| Secondary button edges, sheet grabber (#d1d1d6) | `borderStrong` | #d4d6e2 | #353746 |
| Titles and text (#1c1c1e) | `ink` | #16161d | #ececf2 |
| Second lines, the work's text (#3a3a3c) | `secondary` | #5d5f6e | #a6a8b8 |
| Source lines, labels, times (#6e6e73) | `tertiary` | #6f7185 (see rule 3) | #868899 |
| Chevrons (#c7c7cc) | `faint` | #b4b6c4 | #4f5162 |
| Links, the mark, the selected tab | `accent` | #4f5ed8 | #7a8aff |
| Selected tab's wash, counts | `accentWash` | #5968df at 10% | #7a8aff at 14% |
| Primary buttons, your Chat messages | `solid` / `onSolid` | #5968df / #ffffff | #7a8aff / #101016 |
| The amber line, Soon, waiting (#b25e00) | `warning`, `warningWash` | #9b4f1d | #e8a066 |
| Unpair, errors (#d70015) | `error` | #a12538 | #f07380 |
| Done ticks (#248a3d) | `success`, `successWash` | #2a7a44 | #62c98d |
| Switches on | system green | — | — |

1. **Text on the accent is `onSolid`:** dark (#101016) in dark mode, as Theme already says. Never white on #7a8aff (3.0:1).
2. **The tab bar** is `surface` at 94% with the system blur, in both appearances; the home indicator follows iOS.
3. **The light `tertiary` token becomes #66687c,** the same change as computer-theme v1.0.0 rule 5 (4.4:1 on the background today, 5.1:1 after). It lives in docs/design-system/tokens.json, so one change fixes the computer and the phone.

## 2. Rule 12, fixed on the Watch tab

4. **The Watch tab drew Closed Eyes' gap as a striped bar at the row's left edge** (artboard 5). That's a left-edge bar, which AGENTS.md rule 12 forbids. It's now a 10-point striped dot in `warning`, like the computer's Activity (computer-activity rule 8). The row's words ("11:04–12:04 · Closed Eyes · nothing kept") carry it.

## 3. Words fixed on artboard 8

5. **Settings' row reads "Your agents"** (Codex, Claude) and Your data's row "Sent to your agents", as phone-agent v1.1.0 and agent-words v1.0.0 say; the board still showed v1.0.0's "Your agent".

## Proof

On Bo's iPhone, a named TestFlight build, light and dark (Settings › Display & Brightness):

1. **Both appearances:** screenshots of Today, a hand-off sheet, an agents' request, Watch, Chat and Settings in dark, against artboard 12.
2. **No white on the accent in dark:** Yes, Send and your Chat messages show dark text.
3. **No left-edge bar:** the Watch tab with a Closed Eyes hour shows the striped dot.

## On the map (phone)

- P257 · The phone pass in both appearances by Theme.swift; artboard 12 is dark; onSolid on the accent; light tertiary #66687c through tokens.json.
- P258 · The Watch tab's Closed Eyes gap is a striped dot, not a left-edge bar.
