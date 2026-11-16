# Computer theme v1.0.0: the computer pass in both themes, by the app's own tokens; two contrast fixes

Version 1.0.0 · written November 15, 2026, 8:50 PM Pacific, by Claude Design.

**Why this file:** the computer pass (computer-map v1.0.0 and the files after it) was drawn dark only, in colours close to the app's but not the same. Builders shouldn't copy hex from a board. This file names the token for every colour on the Isoform Computer board, adds the light theme ("paper", the app's default), and fixes two places where the board or the app falls short of 4.5:1 for text.

**What I checked:** the app's tokens in `wails-gui/frontend/src/index.css` on main (fbc8050e0, read-only): `@theme` (light) and `:root[data-theme="onedark"]` (dark).

**Board:** Isoform Computer (https://claude.ai/artifact/4E18TrYUYKfQHqTQdtfoPn). Every dark artboard is now drawn in the onedark tokens exactly; new artboard 14 (Light) shows Today, Agents, Chat with Quick chat, and Settings with the menu bar in paper, made from the same pages by the token pairs below.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Every page of the pass works in both themes from its first build;** neither is a later pass.
2. **Builders use tokens, never the board's hex.**
3. **All text meets 4.5:1** (3:1 for icons and large text) in both themes.

## 1. The tokens, by what they're for

| On the board | Token | Light (paper) | Dark (onedark) |
| --- | --- | --- | --- |
| Page | `background` | #f6f6f9 | #121217 |
| Sidebar | `background-accent` | #f1f1f6 | #15151b |
| Cards, sheets, the menu bar popover | `panel` | #ffffff | #1a1a21 |
| Grey boxes inside a card (the work, What would move) | `surface-active` | #e9eaf1 | #24242e |
| Agent badges, chips | `chip-bg`, `chip-border` | #f0f1f6, #d4d6e2 | #1f1f27, #353746 |
| Lines between rows, card edges | `border` | #e2e3ec | #2a2b35 |
| Secondary button edges, switches off | `border-strong` | #d4d6e2 | #353746 |
| Titles and text | `ink` | #16161d | #ececf2 |
| Second lines, summaries | `muted` | #5d5f6e | #a6a8b8 |
| Source lines, labels, times | `subtle` / `label` | #6f7185 (see §2) | #868899 |
| Links, the focus ring, the open item's ring, the mark | `accent` | #4f5ed8 | #7a8aff |
| Counts' fill, the accent tint | `accent-light` | rgba(89,104,223,0.10) | rgba(122,138,255,0.14) |
| Primary buttons | `solid-btn-bg` / `solid-btn-fg` | #5968df / #ffffff | #7a8aff / #101016 |
| Something to act on: No word since, Couldn't start, Needs Full Disk Access | `warning`, `warning-bg` | #9b4f1d | #e8a066 |
| The green dot (Isoform can see it working), Done | `success`, `success-bg` | #2a7a44 | #62c98d |
| Scrim behind a sheet | `overlay-ink-25` | rgba(0,0,0,0.25) | the token's dark value (the board draws 0.5) |
| An agent's own words, quoted | `font-mono` on `code-bg` | the app's code colours | the app's code colours |

1. **Amber (`warning`) means there's something for you to do,** never "idle" or "old" (computer-agents v1.1.0 rule 10). **Green (`success`) means only "Isoform can see it working" or Done** (computer-agents v1.1.0 rule 1).
2. **State is carried by text and dots,** never a left-edge bar (AGENTS.md rule 12); Activity's source is a dot and its name (computer-activity rule 8).
3. **Activity's source dots** use the app's named hues: the watch `accent`, the screen `app-indigo-text`, your agents `app-plum-text`, Closed Eyes a striped `warning` dot. No new colours.

## 2. Two contrast fixes

4. **Dark primary buttons use dark text,** as the app's own token says (`solid-btn-fg` #101016 on #7a8aff, 6.1:1). The board had white on #7a8aff, 3.0:1; it's corrected on every artboard. The same holds for your own message bubble in Chat.
5. **Light labels on the page background fail by a hair:** `subtle`/`label` #6f7185 on `background` #f6f6f9 is 4.4:1, and labels ("YOUR WORK", "UP NEXT", times) are 12 to 13 px. **Change the light `subtle` and `label` tokens to #66687c** (5.1:1 on the background, 5.6:1 on panels). Dark is fine (5.3:1). One token change; nothing else moves.
6. **Light buttons are `solid-btn-bg` #5968df,** a step lighter than the board's #4f5ed8 (4.7:1 with white); use the token.

## 3. Contrast, checked

From the tokens, WCAG ratio, light · dark:

| Pair | Light | Dark |
| --- | --- | --- |
| ink on panel | 18.0 | 14.7 |
| muted on panel | 6.3 | 7.3 |
| subtle on panel | 4.8 (5.6 with §2) | 4.9 |
| subtle on background | 4.4 (5.1 with §2) | 5.3 |
| accent on panel (links) | 5.4 | 5.7 |
| warning on panel | 5.9 | 8.0 |
| success on panel | 5.3 | 8.5 |
| button text on the button fill | 4.7 (white on #5968df) | 6.1 (#101016 on #7a8aff; white was 3.0) |

## 4. The web pages of agents-join

7. **The join page and the report page** (agents-join §4 and §9) use the website's light tokens and follow the visitor's system theme with the website's dark tokens; they never use the app's sheet styles. They're drawn light on artboards 10 and 11.

## Proof

On Bo's computer, a named desktop build, in both themes (System Settings › Appearance):

1. **Both themes:** screenshots of Today, Agents, Chat with Quick chat, and Settings with the menu bar, light and dark, against artboard 14 and artboards 2, 4, 6 and 8.
2. **Contrast:** a check of every text colour on those pages against its background, by the tool the hub prefers, with no text under 4.5:1.
3. **Tokens only:** in the PRs for these pages, no colour hex outside `index.css`.

## On the map (shell)

- TH1 · The computer pass in both themes, by the app's tokens; artboard 14 is paper.
- TH2 · Dark primary buttons use `solid-btn-fg` (#101016); light `subtle` and `label` become #66687c.
