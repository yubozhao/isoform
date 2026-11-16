# Computer window v1.0.0: the window at every size

Version 1.0.0 · written November 15, 2026, 9:40 PM Pacific, by Claude Design.

**This file adds to computer-map v1.0.0 and computer-today v1.0.0 and v1.1.0; everything in them stands.** The boards draw the app at 1280 × 800. People make it smaller, beside an editor or a browser, and the build's sizes were set for the old Today panel. This says what every width and height shows.

**Board:** Isoform Computer, new artboard 25 (The window at every size): a at 1200 × 800, b at 940 × 720, c at 760 × 560.

A change goes in a new file with a new version; this one isn't edited after it's written.

## Ranked goals

1. **Every size keeps the same ranking:** the brief, then what's yours to answer, then the rest.
2. **Nothing moves into a menu,** and Closed Eyes is one click away at every size.

## What the build does now (c1f0e8bcf, read-only)

- `main.go`: the window opens at 1200 × 800 and has no smallest size.
- `hooks/useSidebarResize.ts`: the sidebar is 240 open and 56 collapsed, and collapses below a 1160 window, "before the Today panel loses its 920 pixels". A collapse by hand is kept. ⌘B switches it.
- `sidebar.tsx`: the collapsed rail shows each chat's first letter. The chat list is gone in the new sidebar (computer-map v1.0.0), so the rail needs its own contents.

## The rules

1. **The smallest window is 760 × 560,** set on the window itself (`MinWidth`, `MinHeight`), so nothing smaller is ever drawn.
2. **The sidebar is 214 wide** (the boards' width): five places and the foot fit, and there's no list to resize any more.
3. **Full sidebar at a window of 1100 and wider; the rail below.** ⌘B switches; a choice made by hand is kept, as now. This replaces 1160.
4. **The rail is 56 wide and keeps everything the sidebar has:** the mark at the top (it opens the sidebar, as now); the five places as icons, each with its name and shortcut on hover; counts as numbers on the icon's corner, never a bare dot; the phone and the watch as icons with a dot (green when connected or listening, grey when not); **Closed Eyes**, with the shut mark, never hidden; Settings. VoiceOver reads each icon as the sidebar row would ("Today, 4 to answer"; "Watch, listening").
5. **Today has two columns when its content is 880 wide or more** (Your work 480 to 640, flexible; the right column 320 to 372; 24 between; 28 each side). A 1200 window with the full sidebar gets them.
6. **Below 880, one column in rank order:** the brief; Your work; Up next; With your agents; Done today. The brief's "In this brief" becomes one line under its text ("3 meetings tomorrow · 4 to answer · from your watch: 1 conversation"), with Read all and Past briefs.
7. **Under 640 tall, the brief's text shows two lines;** Read all opens the rest. The item's question and its prep then show without scrolling at the smallest size, and Y, N and M answer it from the keyboard.
8. **The keys line is a strip of its own** at the foot, with a rule above it; content scrolls under it, never behind it. Below 880 it drops Quick chat's hint (⌥Space still works).
9. **Wide or full screen:** content stops at 1180 and centres; the brief's text stops at 720 wide so lines stay readable.
10. **Sheets** (hand-off, connect, the full brief, What went) are 560 wide, or the content's width less 48 when that's smaller.
11. **Chat:** the conversation stops at 720 wide and centres; the box stays at the foot. **Agents and Activity:** below 700 of content, each row's source line moves under its title and the time stays at the left.

## Every word

| Key | English | Where |
| --- | --- | --- |
| rail.today | Today, {n} to answer | Rail, hover and VoiceOver |
| rail.agents | Agents, {n} needs you | Rail, hover and VoiceOver, when a request waits |
| rail.open | Show the sidebar · ⌘B | The rail's mark, hover |

The phone, watch and Closed Eyes words are the sidebar's own ("Phone · connected", "Watch · listening", "Closed Eyes").

## Proof

On Bo's computer, a named build: three screenshots of Today at 1200 × 800, 940 × 720 and 760 × 560 against artboard 25, and a screen recording of dragging the window's corner smaller than 760 × 560, where it stops.

## On the map (shell)

- SH12 · The window at every size: smallest 760 × 560; the sidebar 214, full at 1100 and wider, the rail below with everything the sidebar has (counts as numbers, phone and watch with a dot, Closed Eyes never hidden); Today in two columns at 880 of content, one column in rank order below; the brief two lines under 640 tall; content stops at 1180.
