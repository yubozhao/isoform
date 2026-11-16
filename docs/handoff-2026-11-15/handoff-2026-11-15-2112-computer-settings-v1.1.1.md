# Computer settings v1.1.1: in the menu bar, an agents' request opens Today too

Version 1.1.1 · written November 15, 2026, 9:12 PM Pacific, by Claude Design.

**This file adds one sentence to computer-settings v1.0.1 rule 8; everything in v1.0.0, v1.0.1 and v1.1.0 stands.** v1.0.1 let the popover answer the next item with Yes and No, except work for an agent, whose Yes opens Today so the hand-off sheet is seen first. A request between agents (computer-today v1.1.0 rule 7) moves something from one agent to another, so it needs its "What would move" seen the same way.

A change goes in a new file with a new version; this one isn't edited after it's written.

## The rule

8a. **When the popover's next item is an agents' request,** it shows "Next · {i} of {n} · {from} asks {to}" and the question, with **Open Today** only (no Yes or No in the popover). Open Today focuses the request there, with What would move in view. No in the popover would be safe, but one button keeps the popover's rule simple: anything that sends something somewhere is answered where you can see what goes.

## Every word

| Key | English | Where |
| --- | --- | --- |
| menu.nextAsks | Next · {i} of {n} · {from} asks {to} | Popover |

## Proof

On Bo's computer, a named build, with a request waiting: the popover shows it with Open Today only; Open Today focuses it on Today with What would move visible.

## On the map (shell)

- ST4 stands; its note gains: an agents' request opens Today from the popover.
