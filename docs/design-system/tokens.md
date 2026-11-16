# Isoform brand violet design tokens

Copied from [the violet handoff](../violet/handoff.md), decided by Bo on 2026-10-23. Values mirror [the source set](../violet/ds/tokens.json) and `wails-gui/frontend/src/index.css`; theme IDs remain `paper` (light) and `onedark` (dark).

Violet means Isoform acts or speaks, including automation, links, primary actions and selected tabs. Copper means waiting on you; ink means the user, headings and body text; red and green mean failure and success. Grounds are cool neutral gray. All texture variables remain `none` for compatibility; there is no Texture preference.

## Color

| Token | Light (`paper`) | Dark (`onedark`) |
|---|---|---|
| `color-background` | `#f6f6f9` | `#121217` |
| `color-background-accent` | `#f1f1f6` | `#15151b` |
| `color-panel` | `#ffffff` | `#1a1a21` |
| `color-panel-warm` | `#f0f1f6` | `#1f1f27` |
| `color-panel-code` | `#e9eaf1` | `#0e0e13` |
| `color-panel-result` | `#ffffff` | `#1f1f27` |
| `color-panel-user` | `#ebecf2` | `#26262f` |
| `color-ink` | `#16161d` | `#ececf2` |
| `color-muted` | `#5d5f6e` | `#a6a8b8` |
| `color-accent` | `#4f5ed8` | `#7a8aff` |
| `color-accent-light` | `rgba(89, 104, 223, 0.1)` | `rgba(122, 138, 255, 0.14)` |
| `color-accent-2` | `#4f5ed8` | `#7a8aff` |
| `color-accent-2-light` | `rgba(89, 104, 223, 0.1)` | `rgba(122, 138, 255, 0.14)` |
| `color-border` | `#e2e3ec` | `#2a2b35` |
| `color-border-light` | `#ececf2` | `#212129` |
| `color-border-strong` | `#d4d6e2` | `#353746` |
| `color-border-focus` | `#9ea7f0` | `#4a52a0` |
| `color-selection-bg` | `#d5d9fa` | `#333a7a` |
| `color-selection-code-fg` | `#16161d` | `var(--color-code-fg)` |
| `color-app-sage-bg` | `#eef1ea` | `#14201a` |
| `color-app-sage-border` | `#d8ded0` | `#1f2f26` |
| `color-app-sage-text` | `#647155` | `#8fb89c` |
| `color-app-teal-bg` | `#ebf1f0` | `#0f1e20` |
| `color-app-teal-border` | `#d1ddda` | `#1d2e30` |
| `color-app-teal-text` | `#5a726d` | `#8db0b0` |
| `color-app-amber-bg` | `#f3eee5` | `#1f1a14` |
| `color-app-amber-border` | `#e0d6c5` | `#2f281d` |
| `color-app-amber-text` | `#7c6952` | `#c4a87a` |
| `color-app-rose-bg` | `#f3ebea` | `#1f1617` |
| `color-app-rose-border` | `#e2d1cf` | `#2e2224` |
| `color-app-rose-text` | `#806466` | `#c49096` |
| `color-app-indigo-bg` | `#edf0f5` | `#131720` |
| `color-app-indigo-border` | `#d5dbe6` | `#222838` |
| `color-app-indigo-text` | `#606d8b` | `#98a9d1` |
| `color-app-plum-bg` | `#f1ecf3` | `#1a151e` |
| `color-app-plum-border` | `#ddd2e1` | `#2b2432` |
| `color-app-plum-text` | `#776586` | `#ab95c1` |
| `color-app-stone-bg` | `#efedea` | `#181716` |
| `color-app-stone-border` | `#dcd6d0` | `#272524` |
| `color-app-stone-text` | `#736a62` | `#9d948b` |
| `color-error` | `#a12538` | `#f07380` |
| `color-error-bg` | `rgba(161, 37, 56, 0.12)` | `rgba(240, 115, 128, 0.16)` |
| `color-error-border` | `rgba(161, 37, 56, 0.22)` | `rgba(240, 115, 128, 0.3)` |
| `color-error-strong` | `rgba(161, 37, 56, 0.6)` | `rgba(240, 115, 128, 0.6)` |
| `color-success` | `#2a7a44` | `#62c98d` |
| `color-success-bg` | `rgba(42, 122, 68, 0.12)` | `rgba(98, 201, 141, 0.16)` |
| `color-success-border` | `rgba(42, 122, 68, 0.22)` | `rgba(98, 201, 141, 0.3)` |
| `color-warning` | `#9b4f1d` | `#e8a066` |
| `color-warning-bg` | `rgba(155, 79, 29, 0.12)` | `rgba(232, 160, 102, 0.16)` |
| `color-warning-border` | `rgba(155, 79, 29, 0.22)` | `rgba(232, 160, 102, 0.3)` |
| `color-info` | `#4f5ed8` | `#7a8aff` |
| `color-info-bg` | `rgba(89, 104, 223, 0.12)` | `rgba(122, 138, 255, 0.16)` |
| `color-info-border` | `rgba(89, 104, 223, 0.22)` | `rgba(122, 138, 255, 0.3)` |
| `color-code-bg` | `#15151c` | `#0e0e13` |
| `color-code-fg` | `#e3e4ee` | `#e3e4ee` |
| `color-seaweed` | `#52b78c` | `#52b78c` |
| `color-seaweed-deep` | `#41a37a` | `#41a37a` |
| `color-seaweed-light` | `#7fcca8` | `#7fcca8` |
| `color-seaweed-foam` | `#b8e4ce` | `#b8e4ce` |
| `color-seaweed-olive` | `#64c19a` | `#64c19a` |
| `color-automation` | `#4f5ed8` | `#7a8aff` |
| `color-automation-deep` | `#3c49b8` | `#5c6be6` |
| `color-automation-light` | `#aab3f5` | `#b3bbff` |
| `color-extractor` | `#c08a2e` | `#d9a24a` |
| `color-claude` | `#d97757` | `#e08a6c` |
| `color-solid` | `#5968df` | `#7a8aff` |
| `color-solid-fg` | `#ffffff` | `#101016` |
| `color-subtle` | `#66687c` | `#868899` |
| `color-faint` | `#b4b6c4` | `#4f5162` |
| `color-label` | `#66687c` | `#868899` |
| `color-dropdown-label` | `#5d5f6e` | `#a6a8b8` |
| `color-welcome-sub` | `#5d5f6e` | `#a6a8b8` |
| `color-heading-strong` | `#16161d` | `#ececf2` |
| `color-icon-subtle` | `#424453` | `#babcca` |
| `color-border-subtle` | `#e8e9f0` | `#23232c` |
| `color-border-divider` | `#e2e3ec` | `#2a2b35` |
| `color-border-input` | `#e2e3ec` | `#2a2b35` |
| `color-border-emphasis` | `#d4d6e2` | `#353746` |
| `color-border-strong-alt` | `#d4d6e2` | `#353746` |
| `color-card-warm` | `#ffffff` | `#1a1a21` |
| `color-card-cool` | `#ffffff` | `#1a1a21` |
| `color-card-warm-alt` | `#f6f6f9` | `#1f1f27` |
| `color-card-cool-alt` | `#ffffff` | `#26262f` |
| `color-card-placeholder` | `#f0f1f6` | `#15151b` |
| `color-card-progress` | `#e2e3ec` | `#353746` |
| `color-sample-border` | `rgba(89, 104, 223, 0.35)` | `rgba(122, 138, 255, 0.4)` |
| `color-sample-text` | `#4f5ed8` | `#7a8aff` |
| `color-button-solid` | `#16161d` | `#ececf2` |
| `color-overlay-ink-25` | `rgba(0, 0, 0, 0.25)` | `rgba(255, 255, 255, 0.55)` |
| `color-card-edge` | `#ffffff` | `#24242e` |
| `color-surface-hover` | `#f0f1f6` | `#1f1f27` |
| `color-surface-active` | `#e9eaf1` | `#24242e` |
| `color-active-row-bg` | `#e9eaf1` | `#24242e` |
| `color-sample-badge-bg` | `#f0f1f6` | `#1f1f27` |
| `color-solid-btn-bg` | `#5968df` | `#7a8aff` |
| `color-solid-btn-fg` | `#ffffff` | `#101016` |
| `color-btn-connect-bg` | `#f0f1f6` | `#1f1f27` |
| `color-composer-bg` | `#ffffff` | `#1a1a21` |
| `color-composer-border` | `#d4d6e2` | `#353746` |
| `color-chip-bg` | `#f0f1f6` | `#1f1f27` |
| `color-chip-border` | `#d4d6e2` | `#353746` |
| `color-copper` | `#b4642d` | `#c9733a` |
| `color-automation-bg` | `rgba(89, 104, 223, 0.12)` | `rgba(122, 138, 255, 0.16)` |
| `color-automation-border` | `rgba(89, 104, 223, 0.22)` | `rgba(122, 138, 255, 0.3)` |

## Shadow

| Token | Light (`paper`) | Dark (`onedark`) |
|---|---|---|
| `shadow-card` | `0 1px 2px rgba(0, 0, 0, 0.04), 0 8px 24px -8px rgba(0, 0, 0, 0.16)` | `0 1px 2px rgba(0, 0, 0, 0.5), 0 8px 24px -8px rgba(0, 0, 0, 0.7)` |
| `shadow-soft` | `0 1px 2px rgba(0, 0, 0, 0.05)` | `0 1px 2px rgba(0, 0, 0, 0.5)` |
| `shadow-composer` | `0 1px 2px rgba(0, 0, 0, 0.04)` | `0 1px 2px rgba(0, 0, 0, 0.4)` |
| `shadow-composer-focus` | `0 2px 8px -2px rgba(0, 0, 0, 0.08)` | `0 2px 8px -2px rgba(0, 0, 0, 0.6)` |
| `shadow-button` | `none` | `none` |
| `shadow-button-hover` | `none` | `none` |
| `shadow-accent` | `none` | `none` |
| `shadow-solid-btn` | `inset 0 1px 0 0 rgba(255, 255, 255, 0.08)` | `none` |

## Spacing

| Token | Light (`paper`) | Dark (`onedark`) |
|---|---|---|
| `spacing` | `0.25rem` | `0.25rem` |

## Radius

| Token | Light (`paper`) | Dark (`onedark`) |
|---|---|---|
| `radius-sm` | `0.25rem` | `0.25rem` |
| `radius-md` | `0.375rem` | `0.375rem` |
| `radius-lg` | `0.5rem` | `0.5rem` |
| `radius-xl` | `0.75rem` | `0.75rem` |
| `radius-full` | `9999px` | `9999px` |

## Type

### Families

| Token | Font stack |
|---|---|
| `font-sans` | `-apple-system, BlinkMacSystemFont, "SF Pro Text", "Inter Variable", "Inter", "Segoe UI", system-ui, sans-serif` |
| `font-serif` | `"Palatino", "Book Antiqua", "Georgia", serif` |
| `font-mono` | `"SF Mono", "SFMono-Regular", "Monaco", "JetBrains Mono", "Consolas", monospace` |
| `font-display` | `"PP Mori", "PPMori", -apple-system, BlinkMacSystemFont, "SF Pro Text", "Inter Variable", "Inter", "Segoe UI", system-ui, sans-serif` |

### Styles

| Style | Weight | Size | Line height |
|---|---|---|---|
| `display` | 400 | 28px | 1.4 |
| `heading-1` | 400 | 22px | 1.4 |
| `heading-2` | 600 | 18px | 1.4 |
| `heading-3` | 400 | 16px | 1.4 |
| `body` | 400 | 14px | 1.4 |
| `body-sm` | 400 | 13px | 1.4 |
| `caption` | 400 | 12px | 1.4 |
| `label` | 500 | 11px | 1.4 |
| `micro` | 400 | 10px | 1.4 |
| `card-title` | 600 | 32px | 1.4 |

The handoff CSS overrides the older tooling display entry to `400 30px/1.25 var(--font-display)` and defined portrait as `400 19px/1.65 var(--font-display)`; the portrait v1.0.0 handoff (2026-10-28) sets it to `400 16px/1.66 var(--font-display)`. The app preserves those display styles. Compatibility chip text takes the JSON values; the supplied CSS still contains older app-chip label values. Active UI uses semantic colors.

## Texture compatibility

`texture-grain`, `texture-mottle-hi`, `texture-mottle-lo` and `texture-copper-hi` are always `none`. Old stored preferences are ignored.
