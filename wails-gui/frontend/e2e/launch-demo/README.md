# Launch demo harness

Renders launch videos and stills from the shipping frontend: the production
`App` (sidebar, Today, the chat panel, About you, Settings) mounted over
hard-coded sample content, inside a drawn macOS desktop with a scripted
cursor. Nothing here launches Isoform, opens a window, or reads
`~/.isoform-agent`; it is dev-only and never part of `vite build`.

## Files

- `content.ts`: the sample content (Bo, Lena's 2:00, Priya's reply, Halcyon's
  two failing tests, the written portrait). Change the story here.
- `fixture.ts`: in-memory Wails bindings and runtime events. Scripted reads
  are listed by name; sending to the chat appends the user's words and, after
  a short wait, Isoform's answer; Send on Priya's draft resolves the card and
  appends the app's own "Sent via Mail" receipt.
- `defaults.ts`: typed empty answers for every other binding (arrays empty,
  objects absent), read from `wailsjs/go/main/App.d.ts` and the shim's
  `appBinding` lines.
- `index.html`, `main.tsx`, `mount.tsx`: mount `App` the way `src/main.tsx`
  does. `?theme=onedark|paper`.
- `stage.html`, `stage.ts`: the 1920x1080 desktop: menu bar with the real
  menu bar icon, the app at 1440x840 scaled 1.2 in a window frame, the cursor.
- `takes.mjs`: the scripted takes (`hero`, `explainer`, `portrait`, `still`).
- `record.mjs`: starts its own Vite, records a take with a CDP screencast in
  real time, writes H.264 `.mov` at 60 fps with ffmpeg (or a `.png` still),
  1920x1080 points at 2x (3840x2160) by default.

## Run

Needs `wailsjs/` generated (or copied from a checkout that has it), ffmpeg,
and Playwright's chrome-headless-shell (`pnpm exec playwright install
chromium-headless-shell`). Chrome for Testing in new headless mode only
manages about 24 fps here.

```bash
cd wails-gui/frontend
export CHROME=~/Library/Caches/ms-playwright/chromium_headless_shell-1208/chrome-headless-shell-mac-arm64/chrome-headless-shell
node e2e/launch-demo/record.mjs hero onedark ~/Movies/launch-hero-take1.mov
DEMO_PACE=1.15 node e2e/launch-demo/record.mjs hero onedark ~/Movies/launch-hero-take2.mov
node e2e/launch-demo/record.mjs explainer paper ~/Movies/launch-explainer-light.mov
node e2e/launch-demo/record.mjs still onedark ~/Movies/today.png
```

At 2x, run with `DEMO_GPU=1 DEMO_JPEG=82`: software rendering holds only
about 36 fps at 3840x2160; Metal holds about 65-70 (95% of frame gaps at or
under 27 ms). `DEMO_SCALE=1` records 1080p at about 100 fps.

`DEMO_TIME` sets the clock the take starts at (default
`2026-11-03T07:02:00-08:00`), `DEMO_PORT` the Vite port (default 34191).
Do not edit files Vite serves while a take records: a hot reload lands in
the video.

## Keeping it true

Only real strings render: every label comes from the app's locale files and
every state is one the app reaches with this data. When a page changes what it
reads, the take shows the page's empty or failed state; add the read to
`fixture.ts` rather than changing the page.

## The website hero

`website-hero.cjs` shoots the isoform.ai hero (Chat answering "What's my
9:00?") from this harness; `?empty=1` starts the main chat empty. The
published capture was made from Mac 1.0.54 (`a3aa3cd45`) and names that build
in the website's claims table.
