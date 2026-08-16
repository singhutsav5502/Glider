# Glider Design System (MASTER)

> Superseded the earlier "instrument panel" direction (cool slate chassis,
> copper accent, Space Grotesk) on 2026-08-13. That direction disagreed with
> the landing page, and Glider was shipping two brands. The landing page won,
> because it is what a new reader sees first.

## Product
**Glider** — cross-CLI delegation with a permission relay, for AI coding CLIs.
Routing is the second half, not the headline. Audience: developers operating a
control plane. Job: clear telemetry, dense operable panels, trustworthy status.

## Source of truth
The `:root` block in the repo-root `index.html` owns every colour. Nothing else
defines a palette. When a colour changes, it changes there first and the other
two stylesheets follow:

| Surface | Stylesheet | Colours | Type |
|---|---|---|---|
| Landing page | `index.html` `:root` | source | Archivo / Newsreader / Plex Mono |
| Dashboard | `internal/dashboard/static/style.css` | aligned | Archivo / Plex Mono |
| Docs pages | `docs/site/assets/style.css` | aligned | **Fraunces / IBM Plex Sans** / Plex Mono |

The docs pages still carry their own display and body faces. That is the one
remaining divergence, and it is not yet a decision — either Fraunces suits
long-form technical prose and stays on purpose, or the docs move to
Archivo/Newsreader and the product has one type system. Decide it; do not let
it drift by default.

## Direction: warm ground, citron mark
Bone and paper ground, ink type, one high-chroma citron accent, teal for state.
Light mode primary (ops desks), not OLED cinema-dark. The signature is the
citron tick — a short 2px bar on a dark ground — which appears in the landing
hero, the demo film, and under the dashboard topbar.

## The mark: the Dart
A folded paper dart on a 100-unit grid, leaning at the angle of the flag you
type. Two paths, no strokes, no gradients.

```svg
<path d="M 88 10 L 12 78 L 44 58 Z" fill="var(--mark-body, #f5f1e8)"/>
<path d="M 88 10 L 44 58 L 58 90 Z" fill="var(--mark-wing, #c9f24d)"/>
```

The larger path is the body, the smaller is the lit wing.

### One source
`tools/genbrand` owns the geometry and writes every file. Run it, then run
`python tools/genbrand/wordmark.py` for the two files that need Archivo. No
person edits any of these by hand, and no page draws the dart from scratch.

| File | What it is |
|---|---|
| `docs/site/assets/brand/mark.svg` | the mark alone, square, no padding |
| `docs/site/assets/brand/logo.svg` | lockup: mark, gap, GLIDER as outlines |
| `docs/site/assets/brand/favicon.svg` | light pair, teal wing |
| `docs/site/assets/brand/icon-192.png`, `icon-512.png` | mark on ink, 18% padding |
| `docs/site/assets/brand/og.png` | 1200×630 social card |
| `docs/site/assets/brand/thumb.png` | 1280×720 thumbnail: lockup, tick, syntax |
| `internal/dashboard/static/favicon.svg` | the dashboard's copy of the favicon |
| `assets/icon.ico`, `internal/tray/icon.ico` | tray and window icon, 10% padding |

An HTML surface declares the two paths once per document, in a `<symbol
id="gl-dart">` at the top of `<body>`, and every occurrence is `<svg
class="mk"><use href="#gl-dart"/></svg>`. Custom properties inherit into the
shadow tree that `<use>` builds, which is why the colour pair rides on
`--mark-body` / `--mark-wing` and not on a class — a document stylesheet cannot
select inside that tree.

### Colour pairs — only these four
| Context | Body | Wing |
|---|---|---|
| On ink (site default) | `#F5F1E8` | `#C9F24D` |
| On citron field | `#12100E` | `#12100E` (solid, one colour) |
| On bone / light | `#12100E` | `#0A7C8C` |
| One colour (favicon fallback, print, stamps) | `currentColor` | `currentColor` |

On the citron field the mark goes solid ink. Citron on citron disappears, and a
third colour there breaks the field. A field sets the pair; the mark never sets
its own, so the two paths cannot be recoloured independently.

### Rules
- Clear space on all sides is the nose-to-tail height ÷ 4. Nothing enters it.
- Minimum size 14px. Below that, use the wordmark alone.
- Never rotate it, never outline it, never put it in a circle or a rounded
  square. The one exception is the app icon and the tray icon, where an ink
  field is what makes it legible on an unknown ground.
- It never animates on a page. The demo film is the exception, and there it
  only fades.
- Inline it wherever it must inherit colour. Use `<img src="…/mark.svg">` only
  where the colour is fixed, and remember that file's fallback is the ink pair.

### Where it appears
Landing page: the nav, every section eyebrow, the two plate header strips, the
terminal header strip, and the footer row. **Not** the hero — the `/agy` syntax
is the hero graphic, and a logo above it competes for the same job. Docs pages:
the nav brand, and nowhere else. Dashboard: the topbar brand, and nowhere else.

Do not add it anywhere else. If it shows up twice in one viewport, remove one.

### Colour tokens
| Token | Hex | Role |
|-------|-----|------|
| `--ink` | `#12100E` | Primary text, dark bands, log ground |
| `--bone` | `#F5F1E8` | Page ground |
| `--paper` | `#FBFAF7` | Panels / cards |
| `--citron` | `#C9F24D` | Accent **fill** only — never text |
| `--citron-ink` | `#2C3111` | Text on a citron fill |
| `--teal` | `#0A7C8C` | State: healthy, selected, focus |
| `--teal-lit` | `#5FC7D6` | Teal on a dark ground |
| `--muted` | `#A8A29A` | Secondary, on dark grounds |
| `--muted-lo` | `#55524A` | Secondary, on light grounds |
| `--rule` | `#E6E4DC` | Hairlines |
| `--rule-strong` | `#CFCCC2` | Controls |

Citron is a fill and never a text colour: `#C9F24D` on `#FBFAF7` measures
1.23:1. Anything that sets `color:` uses a dark token. In the dashboard this is
enforced by splitting the token — `--accent` is the dark `#3D4A12` for text,
`--accent-bright` is the citron for fills. Keep that split.

Every text/ground pair in the dashboard clears WCAG AA (lowest is teal at
4.71:1). Re-check with a contrast calculation before changing any of them.

### Typography
- Display / brand: **Archivo** 700–900
- Body / UI: **Archivo** 400–600
- Long-form prose, landing and docs only: **Newsreader** 400
- Telemetry and code: **IBM Plex Mono** 400–600

The dashboard sets its body text in Archivo, never Newsreader. A serif in a
table of request logs costs scanability and buys nothing. Newsreader is for
pages a person reads, not panels a person operates.

### Layout
- App max-width 1120px (1600px on Graph tab)
- Topbar: brand | tabs | status, with the citron tick under the brand
- Sections: paper surface, 1px `--rule` border, 10px radius, subtle elevation
- Primary buttons: ink fill; secondary: ghost dark-citron text
- Focus ring: 2px teal, offset 2px

### Motion
- Tab/panel fade 180–220ms
- Hover 150ms colour/border
- `prefers-reduced-motion: reduce` → disable transforms, and do not autoplay
  the demo film

### Anti-patterns (do not)
- Purple / indigo primary, pink CTA, glow blobs
- Citron as a text colour, or on any light ground
- A second accent alongside citron and teal
- Cool slate or copper anywhere — that is the superseded direction
- Emoji as icons
- Placeholder-only labels
