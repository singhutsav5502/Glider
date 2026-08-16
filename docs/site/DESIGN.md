# Glider docs site — design brief

The single source of truth for how `docs/site/` should look. Read this before
editing any page or `assets/style.css`.

The look is adapted from utsv.work: **warm paper, ink text, one teal accent, a
serif display face against a clean sans body.** Not a dark developer-tool
theme — it should read like a well-set document, not a terminal.

## The mark

Every page carries the Dart once, as a `<symbol id="gl-dart">` at the top of
`<body>`, and the nav brand pulls it in with `<use>`. That is the only place it
appears on a docs page. Do not add it to a heading, a callout or a footer.

The pair here is fixed — an ink body and a teal wing — because every docs page
is a light ground and citron is invisible on one. `assets/style.css` sets it on
`--mark-body` and `--mark-wing`.

The files under `assets/brand/` are written by `tools/genbrand`. Never edit one
by hand, and never redraw the two paths. The full rules, including clear space
and the four colour pairs, are in `design-system/glider/MASTER.md`.

## Tokens

All defined in `assets/style.css` under `:root`. Use the variables, never
raw hex values.

| Token | Value | Use |
|---|---|---|
| `--ink` | `#12151a` | Headings, strong text |
| `--ink-soft` | `#3a4250` | Body copy |
| `--paper` | `#f4f1ea` | Page background |
| `--paper-deep` | `#e8e3d6` | Inline code background, subtle fills |
| `--line` | `#d4cec0` | Borders, rules, table lines |
| `--accent` | `#0a7c8c` | Links (hover), focus rings, quote bars |
| `--accent-strong` | `#086672` | Links (rest), primary button hover |
| `--surface` | `rgba(255,252,246,0.72)` | Cards, panels — translucent over the grain |

Code blocks deliberately invert: dark (`#171b22`) with `#e8e4da` text and a
`#2a313c` border. They're the one dark element on the page and should stay
that way — it's what makes them read as "machine output."

## Type

| Role | Family | Notes |
|---|---|---|
| Display (`h1`–`h4`) | `--font-display` — Fraunces, fallback Georgia/serif | weight 600, `line-height: 1.15`, `letter-spacing: -0.02em` |
| Body | `--font-body` — IBM Plex Sans, fallback system-ui | 400/600, `1.0625rem`, `line-height: 1.65` |
| Mono | `--font-mono` — IBM Plex Mono | code, `.meta` labels |

Sizes are fluid: `h1: clamp(2.4rem, 6vw, 3.75rem)`, `h2: clamp(1.6rem, 3vw, 2.1rem)`,
`h3: 1.35rem`.

`.meta` is the small-caps label style — mono, `0.78rem`, `letter-spacing: 0.04em`,
uppercase, `--ink-soft`. Use it for eyebrow labels above headings, not for body text.

## Texture

Two layers, both already in `style.css` — don't re-add them per page:

1. **Background wash** on `body`: two soft radial gradients (teal top-left,
   near-black top-right) plus a repeating 24px horizontal rule line at
   `rgba(18,21,26,0.025)` — it reads as faint ruled paper.
2. **Grain overlay** via `body::before`: an inline SVG `feTurbulence` at
   `opacity: 0.35`, `mix-blend-mode: multiply`, `pointer-events: none`.
   Page content must sit at `z-index: 1` above it.

On screens under 720px the grain drops to `opacity: 0.18` and the background
stops being `fixed` (mobile repaint cost).

## Layout

- `--measure: 42rem` — reading width for prose. Long text must not exceed it.
- `--wide: 64rem` — outer container for wide layouts (tables, card grids).
- `--space: clamp(1.15rem, 4.5vw, 1.5rem)` — the standard gutter.

Center with `width: min(100% - 2*var(--space), var(--wide))` and
`margin-inline: auto`.

## Components

- **`.btn`** — ink background, paper text, no radius. Hover: `--accent-strong`
  plus `translateY(-1px)`.
- **`.btn-ghost`** — transparent, `--line` border, ink text. Hover: `--paper-deep`.
- **`blockquote`** — 3px `--accent` left bar, no background, `1.08em`, ink text.
- **`hr`** — a single `--line` rule, `2rem` vertical margin.
- **Tables** — `--line` borders, `--paper-deep` header row, generous cell padding.
  Wide tables scroll inside their own `overflow-x: auto` container; the page body
  must never scroll sideways.

## Motion

`.rise` — 700ms `cubic-bezier(0.22, 1, 0.36, 1)`, fades up 12px. Stagger with
`.rise-delay-1/2/3` (80/160/240ms). Use sparingly: a hero, a card row. Never on
body text.

**Every animation must be wrapped in `@media (prefers-reduced-motion: reduce)`**
to disable it. This is non-negotiable.

## Rules

1. **No external requests.** Fonts load from Google Fonts via the existing
   `<link>` tags in each page's `<head>`; everything else — CSS, any SVG — stays
   inline or local. No CDN scripts, no JS frameworks, no build step. The site is
   static HTML you can open from disk.
2. **Use the tokens.** No raw hex outside `:root`.
3. **Keep the nav identical across pages** — same order, same markup, only the
   `class="active"` moves.
4. **Semantic HTML.** Real `<table>`, `<ul>`, `<h2>`; no `<div>` soup.
5. **Accessible contrast.** `--ink-soft` on `--paper` is the lightest text
   permitted for body copy.
6. **Every page ends with a footer** carrying prev/next links.

## Page inventory

`index` · `architecture` · `routing` · `delegation` · `mitm` · `ngl` ·
`context` · `pure-local` · `api` · `mcp`

Each has: nav, an optional `.badge` eyebrow, `h1`, intro paragraph, `h2`
sections with `id` anchors, and a footer.
