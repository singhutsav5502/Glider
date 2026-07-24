# Glider Design System (MASTER)

> Overrides ui-ux-pro-max default "AI purple" recommendation — product brief forbids purple-on-white / indigo glow aesthetics.

## Product
**Glider** — local AI routing harness for Cursor (gateway + MITM + loops/swarms). Audience: developers operating a control plane. Job: clear telemetry, dense operable panels, trustworthy status.

## Direction: Instrument panel
Aviation / lab-instrument vernacular: cool slate chassis, copper accent (routing "hot path"), crisp mono for telemetry. Light mode primary (ops desks), not OLED cinema-dark.

### Signature
Left copper "trim" on active surfaces + Space Grotesk wordmark against slate type. Dense dashboard spacing (8–32px).

### Color tokens
| Token | Hex | Role |
|-------|-----|------|
| `--bg` | `#F4F6F8` | Page ground |
| `--surface` | `#FFFFFF` | Panels / cards |
| `--fg` | `#0F172A` | Primary text |
| `--muted` | `#64748B` | Secondary |
| `--line` | `#E2E8F0` | Hairlines |
| `--border` | `#CBD5E1` | Controls |
| `--accent` | `#C45C26` | Copper CTA / active |
| `--accent-soft` | `#FFF4ED` | Accent wash |
| `--ok` | `#0F766E` | Teal healthy |
| `--warn` | `#B45309` | Amber |
| `--danger` | `#B91C1C` | Errors |
| `--ink` | `#1E293B` | Buttons / strong |

### Typography
- Display / brand: **Space Grotesk** 600–700
- Body / UI: **DM Sans** 400–600
- Telemetry: **IBM Plex Mono** 400–500
- Base 15–16px, line-height 1.5; labels 0.7–0.8rem tracked

### Layout
- App max-width 1120px (1600px on Graph tab)
- Topbar: brand | tabs | status
- Sections: white surface, 1px border, 12–16px radius, subtle elevation
- Primary buttons: ink fill; secondary: ghost copper text
- Focus ring: 2px copper outline, offset 2px

### Motion
- Tab/panel fade 180–220ms
- Hover 150ms color/border
- `prefers-reduced-motion: reduce` → disable transforms

### Anti-patterns (do not)
- Purple / indigo primary, pink CTA, glow blobs
- Cream + terracotta serif landing look
- Emoji as icons
- Placeholder-only labels
