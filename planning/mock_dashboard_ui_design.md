# Glider Dashboard — UI/UX Design (Whitespace & Top Bar)

> **Archival design reference.** A **functional** embedded dashboard ships (Overview / VRAM / Rules / Config + LOCAL/CLOUD/CANNED %). Pixel-perfect parity with this mock is **not** done — see [README.md](./README.md).

> A hyper-direct, stark, and perfectly spaced interface. No sidebar, no fluff. It uses generous whitespace to frame the raw data, making a complex proxy system immediately readable at a single glance.

![Glider Top-Bar Minimalist Dashboard](C:/Users/Utsav/.gemini/antigravity/brain/dd367367-3e5e-404e-8f1b-f72391f4cc0e/glider_topbar_dashboard_mockup_1784320102761.png)

---

## 📐 Design System

| Element | Style & Rationale |
|---------|-------------------|
| **Spacing & Whitespace** | **Generous and Intentional.** Elements are not crammed together. Large margins frame the content in the center of the screen, allowing the eye to rest and quickly find specific data points. The UI feels "breathable". |
| **Typography** | **To-The-Point.** Crisp, geometric sans-serif for navigation and headers. Monospace fonts exclusively for live logs and precise metrics. Strict adherence to baseline grids for perfect horizontal and vertical alignment. |
| **Colors** | - **Primary:** Pure white background (`#FFFFFF`), stark black text (`#000000`), and soft grays (`#9CA3AF`) for secondary text.<br>- **Accent:** A single muted blue (`#2563EB`), used *only* as a 2px underline for the active top-nav tab.<br>- **Status:** Microscopic semantic dots (green/amber) used sparingly for system health. |
| **Structure** | Completely flat. Borders are either removed entirely or restricted to 1px hairline separators (`#F3F4F6`). Grouping is achieved through whitespace, not bounding boxes. |

---

## 🏛 Layout Architecture (Grounded in Phase 4)

### Top Bar (Global Navigation)
- **Brand/Logo:** Left-aligned. "Glider" in a heavy, clean typeface.
- **Tab Navigation:** Centered in the top bar.
  - `Overview` — Metrics & Live Routing Logs
  - `VRAM & Models` — GPU gauges & Model lifecycle management
  - `Rules Engine` — Starlark script editor & Route priorities
  - `Settings` — Dynamic configuration editor
- **Status:** Right-aligned global health indicator (e.g., `Status: OK`).

---

### Screen 1: Overview (Observability & Routing)
*Purpose: See what the proxy is doing right now and how much money it's saving.*
1. **High-Level Metrics:** Wide, generously spaced row. 
   - `REQUESTS: 1,204`
   - `LOCAL / CLOUD: 82% / 18%`
   - `SAVED: $14.20`
   - `LATENCY: 1.8ms` (Proxy overhead)
2. **Session picker:** One Glider process run = one session; live WS tails current; historical sessions under `~/.glider/history`.
3. **Real-Time Request Stream:** A spacious, terminal-inspired log taking up the rest of the vertical space.
   - Columns (shipped): `TIME | MODE | ACTION | HOST / MODEL | RULE | LATENCY | TOKENS`
   - Event fields include Mode, Action, Host, Path, Rule, OriginalModel (see `RequestEventData`).
   - Maps to the *Metrics Collector*, session history store, and *WebSocket push* features.

---

### Screen 2: VRAM & Models (Resource Management)
*Purpose: Understand and control local GPU memory allocations.*

![VRAM & Models Screen](C:/Users/Utsav/.gemini/antigravity/brain/dd367367-3e5e-404e-8f1b-f72391f4cc0e/glider_vram_models_mockup_1784320306478.png)

1. **Per-GPU VRAM Gauges:** 
   - Ultra-thin (2px-4px) horizontal lines per physical GPU.
   - Black for `Used`, light gray for `Free`, marked `Headroom`.
2. **Model Management Panel:** 
   - Tabular list from config + live discovery (Ollama `/api/tags`, vLLM `/v1/models`) with nvidia-smi gauges when available.
   - Columns (shipped): `MODEL`, `BACKEND`, `SOURCE`, `VRAM`, `STATE`, `GPU`
   - Actions: load / unload; assign GPU index → persists `vram.gpu_assignments`
   - Soft catalog validation warnings when models/assignments don’t match discovery
   - Maps to the *VRAM Allocator*, *Scale-to-Zero timeout*, and *Model Registry*.

---

### Screen 3: Rules Engine (Routing Logic)
*Purpose: Control exactly how requests are routed.*

![Rules Engine Screen](C:/Users/Utsav/.gemini/antigravity/brain/dd367367-3e5e-404e-8f1b-f72391f4cc0e/glider_rules_engine_mockup_1784320325810.png)

1. **Priority List:** Ordered list of routing rules (higher priority wins). Shipped UI supports create/edit/enable for explicit, script, context_size, always (and related types) persisted via `PUT /api/config`.
2. **Starlark Script Editor:** 
   - Mockup intent: in-browser `.star` editing with “Test Rule”.
   - Shipped: rules reference `file:` scripts; script body editing remains file-based; Rules Engine edits trigger/action/priority/enabled.
   - Maps to the *Starlark script executor* and *Rule priority engine*.

---

### Screen 4: Settings (Configuration)
*Purpose: Tweak limits, thresholds, and transformations; hot-reload what the process supports.*

![Settings Screen](C:/Users/Utsav/.gemini/antigravity/brain/dd367367-3e5e-404e-8f1b-f72391f4cc0e/glider_settings_mockup_1784320354550.png)

1. **Structured form (primary):** Section cards + tooltips for server, thresholds, VRAM, models, aliases, routing, MITM, cloud, backends, transform. `GET|PUT /api/config`.
2. **Hot-reload vs restart:** Routing / aliases / thresholds / log level hot-reload; ports / MITM / backends need restart.
3. **YAML View (optional/collapsed):** Advanced raw `glider.yaml` editor for power users.
   - Maps to the *Config Hot-Reload (fsnotify)* + Dashboard Swap path.

---

## 🛠 Why this design?
1. **To The Point:** Removing the sidebar instantly focuses the user purely on the VRAM state and the routing logs. 
2. **Cognitive Ease:** Generous whitespace prevents the dense data (tokens, latencies, model names) from feeling overwhelming.
3. **Professionalism:** The stark contrast and perfect alignment exude engineering precision. It feels like a serious tool, not a toy.

---

## 🎨 Design Tokens (CSS Variables)

To ensure this exact aesthetic translates to code during Phase 4, use these specific design tokens.

### Colors
```css
:root {
  /* Backgrounds */
  --bg-base: #FFFFFF;
  --bg-surface: #FAFAFA;
  
  /* Typography */
  --text-primary: #000000;   /* Pure Black */
  --text-secondary: #9CA3AF; /* Soft Gray */
  
  /* Structural */
  --border-light: #F3F4F6;   /* Hairline separators */
  
  /* Accents */
  --accent-blue: #2563EB;    /* Active tab underline */
  --status-ok: #10B981;      /* Microscopic green dot */
  --status-warn: #F59E0B;    /* Microscopic amber dot */
}
```

### Spacing (The "Breathable" Scale)
```css
:root {
  --space-1: 0.25rem;  /* 4px */
  --space-2: 0.5rem;   /* 8px */
  --space-3: 1.5rem;   /* 24px (Base padding) */
  --space-4: 3rem;     /* 48px (Sub-section gaps) */
  --space-5: 6rem;     /* 96px (Major section gaps) */
  --space-6: 12rem;    /* 192px (Page margins) */
}
```

### Typography
```css
:root {
  /* Fonts */
  --font-sans: 'Inter', -apple-system, sans-serif;
  --font-mono: 'JetBrains Mono', 'Geist Mono', monospace;
  
  /* Weights */
  --font-weight-regular: 400;
  --font-weight-medium: 500;
  --font-weight-bold: 700;     /* Stark contrast for active states */
  
  /* Scale */
  --text-xs: 0.75rem;    /* 12px (Micro dots/labels) */
  --text-sm: 0.875rem;   /* 14px (Table rows) */
  --text-base: 1rem;     /* 16px (Body) */
  --text-lg: 1.25rem;    /* 20px (KPI labels) */
  --text-xl: 2rem;       /* 32px (KPI values) */
}
```
