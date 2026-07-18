# Glider documentation site

Static HTML under [`site/`](site/) — host anywhere (GitHub Pages, nginx, Netlify) or serve locally:

```powershell
powershell -File scripts\serve-docs.ps1
# → http://127.0.0.1:8090
```

Or open `docs/site/index.html` directly in a browser.

## Pages

| Page | Topic |
|------|--------|
| [site/index.html](site/index.html) | Home |
| [site/architecture.html](site/architecture.html) | Surfaces + pipeline |
| [site/routing.html](site/routing.html) | Rule priority + sticky |
| [site/context.html](site/context.html) | Event log + locals |
| [site/path-a-b.html](site/path-a-b.html) | Gateway vs MITM |
| [site/loop-engineering.html](site/loop-engineering.html) | Hoops / stages |
| [site/pure-local.html](site/pure-local.html) | Ollama-only profile |
| [site/api.html](site/api.html) | HTTP API |
| [site/samples.html](site/samples.html) | Runnable sample hoops |

Planning depth remains in `/planning` (canonical engineering notes). This site is the **hostable product docs** layer.
