(() => {
  const panels = {
    overview: document.getElementById("panel-overview"),
    vram: document.getElementById("panel-vram"),
    rules: document.getElementById("panel-rules"),
    mcp: document.getElementById("panel-mcp"),
    vendors: document.getElementById("panel-vendors"),
    workspace: document.getElementById("panel-workspace"),
    playground: document.getElementById("panel-playground"),
    settings: document.getElementById("panel-settings"),
  };

  let currentCfg = null;
  let viewingSessionId = null;
  let liveMode = true;
  let requests = 0; // handled — see countRequests
  let seen = 0;     // handled + declined (skip / blind tunnel)
  let local = 0;
  let cloud = 0;
  let canned = 0;
  let lastDist = null; // { local_pct, cloud_pct, canned_pct } from API when available
  let tokenTotal = 0;
  let latencySum = 0;
  let gpuAssignmentDraft = {};
  // Carried across a config save without being shown — see loadConfig.
  let preservedMitmCA = { ca_cert: "", ca_key: "" };

  function activateTab(tabName, opts) {
    const prev = document.querySelector(".tab.active")?.dataset?.tab;
    const name = tabName && panels[tabName] ? tabName : "overview";
    document.querySelectorAll(".tab").forEach((b) => {
      b.classList.toggle("active", b.dataset.tab === name);
    });
    Object.entries(panels).forEach(([key, p]) => {
      if (p) p.classList.toggle("active", key === name);
    });
    if (name === "vram") loadVRAM();
    if (name === "rules") {
      renderRulesEditor(currentCfg);
      loadHotSwap();
      loadRulesLint();
    }
    if (name === "overview") loadSessions();
    if (name === "mcp") refreshMCPPanel();
    if (name === "vendors") refreshVendorsPanel();
    if (name === "workspace") refreshWorkspacePanel();
    if (name === "playground") refreshPlaygroundPanel();
  }

  document.querySelectorAll(".tab").forEach((btn) => {
    btn.addEventListener("click", () => activateTab(btn.dataset.tab));
  });

  document.querySelectorAll("[data-goto-tab]").forEach((el) => {
    el.addEventListener("click", () => activateTab(el.getAttribute("data-goto-tab")));
  });

  const logEl = document.getElementById("request-log");

  /** @type {Record<string, { title: string, body?: string, values?: { v: string, d: string }[] }>} */
  const TIP_CATALOG = {
    "glider-prompt-input": {
      title: "Value",
      body: "The text that you write for the question in the dialog.",
    },
    "session-select": {
      title: "Session",
      body: "A session is one run of the Glider program. Glider adds the live WebSocket events to the current session. Select an earlier run to read its recorded log.",
    },
    "cfg-proxy-port": { title: "Gateway proxy port", body: "The port of the OpenAI /v1 endpoints. You must restart Glider after a change." },
    "cfg-dash-port": { title: "Dashboard port", body: "The port of this interface and of the REST and WebSocket endpoints. You must restart Glider after a change." },
    "cfg-log-level": {
      title: "Log level",
      body: "The slog level of the Glider program. Glider applies this immediately when you save.",
      values: [
        { v: "debug", d: "Records all the messages" },
        { v: "info", d: "The default level" },
        { v: "warn", d: "Records the warnings and the errors" },
        { v: "error", d: "Records only the errors" },
      ],
    },
    "cfg-tokens": { title: "Max local context tokens", body: "Above this context size, the router prefers the cloud or the origin. Glider applies a change immediately." },
    "cfg-idle": { title: "Idle unload timeout", body: "Glider unloads a local model after this time with no use. For example, 5m." },
    "cfg-req-timeout": { title: "Request timeout", body: "The time limit for one backend completion. For example, 120s." },
    "cfg-mitm-enabled": { title: "MITM enabled", body: "Turns on the MITM proxy. You must restart Glider after a change." },
    "cfg-mitm-port": { title: "MITM listen port", body: "The CONNECT port. You must restart Glider after a change." },
    "cfg-mitm-passthrough": { title: "Passthrough default", body: "When this is true, a route that is not local goes to the Cursor origin. Glider does not use your own cloud key." },
    "cfg-mitm-hosts": { title: "MITM hosts", body: "The host names to intercept, one on each line. You can use a simple wildcard, for example *.api5.cursor.sh." },
    "cfg-vram-strategy": {
      title: "VRAM strategy",
      values: [
        { v: "static", d: "Keeps the models in memory" },
        { v: "dynamic", d: "Removes a model quickly" },
        { v: "hybrid", d: "Uses a balance of the two" },
      ],
    },
    "cfg-vram-headroom": { title: "Headroom (MB)", body: "The quantity of free VRAM that Glider does not use." },
    "cfg-vram-max": { title: "Max loaded models", body: "The usual maximum number of local models in memory at the same time." },
    "cfg-vram-gpus": { title: "GPU assignments", body: "A JSON map of a model name to a GPU number. Use the VRAM & Models page instead." },
    "cfg-dash-enabled": { title: "Dashboard enabled", body: "Serves this interface. You must restart Glider after a change." },
    "cfg-xform-enabled": { title: "Transforms enabled", body: "The primary control for the prompt transforms." },
    "cfg-xform-trim": { title: "Trim context", body: "Decreases a large context to the maximum local token count." },
    "cfg-xform-prepend": { title: "Augment prepend", body: "Glider puts this text before the prompt when the transforms are on." },
    "cfg-xform-append": { title: "Augment append", body: "Glider puts this text after the prompt when the transforms are on." },
    "cfg-aliases": { title: "Aliases JSON", body: "A JSON object that maps a client model name to a local model name." },
    "cfg-models": { title: "Models JSON", body: "A JSON array of the model objects. Each object has a name, a backend and a vram_estimate_mb value." },
    "cfg-backends": { title: "Backends JSON", body: "A JSON array of the ollama and vllm entries. Each entry has a url and a health_check_interval. Glider applies a change immediately, but a completion that is in progress keeps the old client." },
    "cfg-budget": { title: "Budget cap (USD)", body: "An optional limit in USD for the cloud cost." },
    "cfg-rpm": { title: "Requests / min", body: "The maximum number of requests each minute for all the cloud providers." },
    "cfg-tpm": { title: "Tokens / min", body: "The maximum number of tokens each minute for all the cloud providers." },
    "cfg-providers": { title: "Providers JSON", body: "The array of the providers. Use the api_key_env names. Do not write a secret here." },
    "cfg-yaml": { title: "glider.yaml", body: "The editor for the full YAML config. Use the form instead, unless you need a key that the form does not have." },
    "rule-name": { title: "Rule name", body: "Human-readable rule name shown in the rules list." },
    "rule-priority": { title: "Priority", body: "Higher priority is evaluated first." },
    "rule-enabled": { title: "Enabled", body: "Disabled rules stay in config but are skipped by the router." },
    "rule-trigger-type": {
      title: "Trigger type",
      values: [
        { v: "explicit", d: "Match /local /cloud (or custom) commands in the prompt" },
        { v: "context_size", d: "Token threshold comparison" },
        { v: "script", d: "Starlark script file" },
        { v: "always", d: "Default fallback when nothing else matches" },
        { v: "regex", d: "Regex pattern match" },
      ],
    },
    "rule-commands": { title: "Commands", body: "Comma-separated commands for explicit triggers (e.g. /local,/fast)." },
    "rule-pattern": { title: "Pattern", body: "Regex pattern when trigger type is regex." },
    "rule-script": { title: "Script file", body: "Starlark script path relative to process cwd." },
    "rule-operator": { title: "Operator", body: "Comparison for context_size: >, >=, <, <=, ==" },
    "rule-value": { title: "Value", body: "Token count threshold for context_size rules." },
    "rule-target": {
      title: "Action target",
      values: [
        { v: "local", d: "Ollama/vLLM" },
        { v: "cloud", d: "BYOK (gateway) or origin passthrough (MITM)" },
      ],
    },
    "rule-backend": { title: "Backend", body: "Optional backend name for cloud actions." },
    "rule-model": { title: "Model", body: "Model to use when this rule matches." },
    "rule-adapter": { title: "Adapter", body: "Optional LoRA adapter name." },
  };

  const tipEl = document.getElementById("glider-tip");
  let tipHideTimer = null;

  function tipEsc(s) {
    return String(s ?? "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function resolveTip(el) {
    if (!el) return null;
    const key = el.getAttribute("data-tip-key");
    if (key && TIP_CATALOG[key]) return TIP_CATALOG[key];
    const raw = el.getAttribute("data-tip");
    if (raw) return { title: "", body: raw };
    return null;
  }

  function renderTipHTML(tip) {
    if (!tip) return "";
    let html = "";
    if (tip.title) html += `<div class="glider-tip-title">${tipEsc(tip.title)}</div>`;
    if (tip.body) html += `<p class="glider-tip-body">${tipEsc(tip.body)}</p>`;
    if (tip.values && tip.values.length) {
      html += `<ul class="glider-tip-values">${tip.values
        .map((x) => `<li><code>${tipEsc(x.v)}</code><span>${tipEsc(x.d)}</span></li>`)
        .join("")}</ul>`;
    }
    return html;
  }

  function placeTip(anchor) {
    if (!tipEl || !anchor) return;
    const pad = 10;
    const rect = anchor.getBoundingClientRect();
    tipEl.hidden = false;
    tipEl.classList.add("is-open");
    const tw = tipEl.offsetWidth;
    const th = tipEl.offsetHeight;
    let left = rect.left;
    let top = rect.bottom + 8;
    if (left + tw > window.innerWidth - pad) left = window.innerWidth - tw - pad;
    if (left < pad) left = pad;
    if (top + th > window.innerHeight - pad) top = rect.top - th - 8;
    if (top < pad) top = pad;
    tipEl.style.left = `${Math.round(left)}px`;
    tipEl.style.top = `${Math.round(top)}px`;
  }

  function showTipFor(el) {
    const tip = resolveTip(el);
    if (!tip || !tipEl) return;
    if (tipHideTimer) {
      clearTimeout(tipHideTimer);
      tipHideTimer = null;
    }
    tipEl.innerHTML = renderTipHTML(tip);
    placeTip(el);
  }

  function hideTipSoon() {
    if (tipHideTimer) clearTimeout(tipHideTimer);
    tipHideTimer = setTimeout(() => {
      if (!tipEl) return;
      tipEl.classList.remove("is-open");
      tipEl.hidden = true;
      tipEl.innerHTML = "";
    }, 80);
  }

  function bindCustomTips(root) {
    const scope = root || document;
    scope.querySelectorAll("[data-tip-key], [data-tip]").forEach((el) => {
      if (el.dataset.tipBound === "1") return;
      el.dataset.tipBound = "1";
      // Prevent native browser tooltip chrome.
      if (el.hasAttribute("title")) el.removeAttribute("title");
      el.querySelectorAll("[title]").forEach((c) => c.removeAttribute("title"));
      el.addEventListener("mouseenter", () => showTipFor(el));
      el.addEventListener("mouseleave", hideTipSoon);
      el.addEventListener("focusin", () => showTipFor(el));
      el.addEventListener("focusout", hideTipSoon);
    });
  }

  bindCustomTips(document);
  // Re-bind after dynamic panel renders.
  const tipObserver = new MutationObserver(() => bindCustomTips(document));
  tipObserver.observe(document.getElementById("app") || document.body, { childList: true, subtree: true });

  /* Split what Glider DID from what it merely SAW.
     local/cloud/canned/error are the four the counter used to sum, and on
     their own they read 0 while the product is plainly working: a delegate
     run, or an intercepted request passed to its origin, is none of those.
     Found 2026-08-13 — a transparent session that carried 22 requests showed
     REQUESTS: 0, as did two successful delegations.

     "Handled" is every action where Glider acted on the request. "Seen"
     adds the ones it deliberately did not touch — a blind tunnel or a skip
     is traffic it declined, so counting those as work would overstate it
     just as badly in the other direction. */
  /* This list mirrors metrics.IsRequestLogAction in internal/metrics/
     collector.go, and must keep mirroring it. Those six are the only
     actions that become a row in the log below this counter; every other
     action is a counter alone, on purpose — Record downgrades it to
     IncAction so the log never carries a row with no model, no tokens and
     no routing decision.

     "decrypt" was in this list briefly and was wrong: it made the headline
     number count something that can never appear in the table under it. */
  const HANDLED_ACTIONS = [
    "local", "cloud", "origin_passthrough", "canned", "error", "delegate",
  ];
  // Deliberately not a list. Anything that is not a request-log action is
  // observed-only, so a new action added in Go shows up here without
  // needing a matching edit in this file.
  const BASE_COUNT_ACTIONS = ["local", "cloud", "canned", "error"];

  function countRequests(dist) {
    if (!dist) return { handled: 0, seen: 0 };
    const actions = dist.actions || {};
    // local/cloud/canned/error arrive as top-level fields, not in `actions`.
    let handled = (dist.local_count || 0) + (dist.cloud_count || 0) +
      (dist.canned_count || 0) + (dist.error_count || 0);
    let seen = handled;
    Object.keys(actions).forEach((name) => {
      if (BASE_COUNT_ACTIONS.includes(name)) return; // already in the base sum
      const n = actions[name] || 0;
      if (HANDLED_ACTIONS.includes(name)) handled += n;
      seen += n;
    });
    return { handled, seen };
  }

  function resetLiveMetrics() {
    requests = 0;
    seen = 0;
    local = 0;
    cloud = 0;
    canned = 0;
    lastDist = null;
    tokenTotal = 0;
    latencySum = 0;
    updateMetricsUI();
  }

  function updateMetricsUI() {
    document.getElementById("m-requests").textContent = requests.toLocaleString();
    // Only say "seen" when it differs — an identical second number reads as
    // noise on a screen that is mostly numbers.
    const seenEl = document.getElementById("m-requests-seen");
    if (seenEl) {
      seenEl.textContent = seen > requests ? `${seen.toLocaleString()} seen` : "";
    }
    let localPct;
    let cloudPct;
    let cannedPct;
    if (lastDist && lastDist.local_pct != null) {
      localPct = Number(lastDist.local_pct);
      cloudPct = Number(lastDist.cloud_pct);
      cannedPct = Number(lastDist.canned_pct);
    } else {
      const total = local + cloud + canned || 1;
      localPct = Math.round((local / total) * 1000) / 10;
      cloudPct = Math.round((cloud / total) * 1000) / 10;
      cannedPct = Math.round((canned / total) * 1000) / 10;
    }
    document.getElementById("m-split").textContent =
      `${fmtPct(localPct)}% / ${fmtPct(cloudPct)}% / ${fmtPct(cannedPct)}%`;
    updateDistBar(localPct, cloudPct, cannedPct);
    document.getElementById("m-tokens").textContent = tokenTotal.toLocaleString();
    document.getElementById("m-latency").textContent =
      requests ? `${(latencySum / requests).toFixed(1)}ms` : "--";
  }

  function fmtPct(n) {
    if (!Number.isFinite(n)) return "0";
    return Number.isInteger(n) ? String(n) : String(n);
  }

  function updateDistBar(localPct, cloudPct, cannedPct) {
    const bar = document.getElementById("dist-bar");
    if (!bar) return;
    const sum = (localPct || 0) + (cloudPct || 0) + (cannedPct || 0);
    if (sum <= 0) {
      bar.hidden = true;
      return;
    }
    bar.hidden = false;
    const set = (id, pct) => {
      const el = document.getElementById(id);
      if (el) el.style.flex = `${Math.max(0, pct)} 0 0`;
    };
    set("dist-local", localPct);
    set("dist-cloud", cloudPct);
    set("dist-canned", cannedPct);
  }

  function applyClassRates(classRates, roleRates) {
    const el = document.getElementById("class-rates");
    if (!el) return;
    const chips = [];
    const classes = classRates || {};
    const roles = roleRates || {};
    const classKeys = Object.keys(classes).sort();
    const roleKeys = Object.keys(roles).sort();
    if (!classKeys.length && !roleKeys.length) {
      el.innerHTML = "";
      el.hidden = true;
      return;
    }
    el.hidden = false;
    for (const k of classKeys) {
      chips.push(`<span class="chip" title="routing reason">${esc(k)} ${classes[k]}</span>`);
    }
    for (const k of roleKeys) {
      chips.push(`<span class="chip role" title="task role">${esc(k)} ${roles[k]}</span>`);
    }
    el.innerHTML = `<span class="chip-label">CLASS</span>${chips.join("")}`;
  }

  function episodeChipLabel(ep) {
    const role = String(ep.role || "").trim();
    const model = String(ep.model || "").trim();
    const rule = String(ep.rule || "").trim();
    const head = role || model || rule || String(ep.id || "ep").slice(0, 10);
    const summary = String(ep.summary || "").replace(/\s+/g, " ").trim();
    const short = summary.length > 48 ? summary.slice(0, 48) + "…" : summary;
    return short ? `${head}: ${short}` : head;
  }

  function episodeChipTitle(ep) {
    const bits = [];
    if (ep.id) bits.push("id " + ep.id);
    if (ep.turn_id) bits.push("turn " + ep.turn_id);
    if (ep.role) bits.push("role " + ep.role);
    if (ep.model) bits.push("model " + ep.model);
    if (ep.rule) bits.push("rule " + ep.rule);
    if (ep.tokens) bits.push(ep.tokens + " tok");
    if (ep.created_at) {
      try {
        bits.push(new Date(ep.created_at).toLocaleString());
      } catch (_) {}
    }
    if (ep.summary) bits.push(String(ep.summary).slice(0, 240));
    return bits.join(" · ");
  }

  function renderEpisodeChips(episodes) {
    const el = document.getElementById("episode-chips");
    if (!el) return;
    const list = Array.isArray(episodes) ? episodes.slice() : [];
    list.sort((a, b) => {
      const ta = a && a.created_at ? Date.parse(a.created_at) || 0 : 0;
      const tb = b && b.created_at ? Date.parse(b.created_at) || 0 : 0;
      return tb - ta;
    });
    const recent = list.slice(0, 12);
    if (!recent.length) {
      el.innerHTML = "";
      el.hidden = true;
      return;
    }
    el.hidden = false;
    const chips = recent.map((ep) => {
      const cls = ["chip"];
      if (ep.role) cls.push("role");
      if (Array.isArray(ep.artifacts) && ep.artifacts.length) cls.push("has-artifacts");
      return `<span class="${cls.join(" ")}" title="${esc(episodeChipTitle(ep))}">${esc(episodeChipLabel(ep))}</span>`;
    });
    el.innerHTML = `<span class="chip-label">EPISODES</span>${chips.join("")}`;
  }

  async function loadEpisodes(sessionId) {
    try {
      const q = new URLSearchParams({ limit: "16" });
      if (sessionId) q.set("session", sessionId);
      const res = await fetch("/api/context/episodes?" + q.toString());
      if (!res.ok) {
        renderEpisodeChips([]);
        return;
      }
      const data = await res.json();
      let eps = Array.isArray(data.episodes) ? data.episodes : [];
      // If session filter returned empty but other sessions exist, show merged recent.
      if (!eps.length && sessionId) {
        const all = await fetch("/api/context/episodes?limit=16");
        if (all.ok) {
          const merged = await all.json();
          eps = Array.isArray(merged.episodes) ? merged.episodes : [];
        }
      }
      renderEpisodeChips(eps);
    } catch (_) {
      renderEpisodeChips([]);
    }
  }

  function esc(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    }[c]));
  }

  async function refreshMetricsSnapshot() {
    if (!liveMode) return;
    try {
      const res = await fetch("/api/metrics");
      if (!res.ok) return;
      const snap = await res.json();
      if (snap.distribution) {
        const counts = countRequests(snap.distribution);
        seen = counts.seen;
        applyDistribution(snap.distribution, { requests: counts.handled });
      }
      applyClassRates(snap.class_rates, snap.role_rates);
      if (snap.token_stats && snap.token_stats.total != null) {
        tokenTotal = snap.token_stats.total;
        updateMetricsUI();
      }
      if (viewingSessionId) loadEpisodes(viewingSessionId);
      else loadEpisodes();
    } catch (_) {}
  }

  function applyDistribution(dist, opts) {
    if (!dist) return;
    local = Number(dist.local_count) || 0;
    cloud = Number(dist.cloud_count) || 0;
    canned = Number(dist.canned_count) || 0;
    lastDist = {
      local_pct: dist.local_pct,
      cloud_pct: dist.cloud_pct,
      canned_pct: dist.canned_pct,
    };
    if (opts && opts.requests != null) {
      requests = Number(opts.requests) || 0;
    } else {
      requests = local + cloud + canned + (Number(dist.error_count) || 0);
    }
    updateMetricsUI();
  }

  function isRequestLogRow(data) {
    const action = data.action || data.route || "";
    // "delegate" added 2026-07-30 alongside the backend's own
    // IsRequestLogAction (internal/metrics/collector.go) -- without it, a
    // delegate call's RequestRecord reaches the browser correctly but gets
    // silently dropped right here, invisible in the Overview table despite
    // every earlier layer having been fixed.
    return action === "local" || action === "cloud" || action === "origin_passthrough" || action === "canned" || action === "error" || action === "delegate";
  }

  function addLog(data, opts) {
    // Tunnel opens / non-LLM skips are counters only -- omit from Overview table
    // (also filters historical sessions that recorded decrypt/skip before the fix).
    if (!isRequestLogRow(data)) {
      return;
    }
    const prepend = !opts || opts.prepend !== false;
    const fromLive = !opts || opts.live !== false;
    if (fromLive && liveMode) {
      lastDist = null;
      const action = data.action || data.route || "";
      // Same split as countRequests, applied one record at a time. Note this
      // path only ever sees request-log actions — a counter-only action
      // never reaches the bus — but the check keeps the two definitions
      // from drifting apart.
      seen += 1;
      if (HANDLED_ACTIONS.includes(action)) requests += 1;
      if (action === "canned") canned += 1;
      else if (action === "local" || data.route === "local") local += 1;
      if (action === "cloud" || action === "origin_passthrough" || data.route === "cloud") cloud += 1;
      tokenTotal += Number(data.tokens) || 0;
      if (data.latency_ms != null) latencySum += Number(data.latency_ms);
      updateMetricsUI();
    }

    const row = document.createElement("div");
    row.className = "log-row";
    const ts = data.ts ? new Date(data.ts) : new Date();
    const time = ts.toLocaleTimeString();
    const action = data.action || data.route || "";
    const parts = [];
    if (data.host) parts.push(data.host);
    let modelLabel = data.model || "";
    if (data.original_model && data.original_model !== data.model) {
      modelLabel = modelLabel
        ? `${modelLabel} (${data.original_model})`
        : data.original_model;
    }
    if (modelLabel) parts.push(modelLabel);
    const hostModel = parts.join(" | ") || "--";
    const rule = data.rule || "--";
    const hasLatency = data.latency_ms != null && data.latency_ms !== "";
    const hasTokens = data.tokens != null && data.tokens !== "";
    const latency = hasLatency ? Number(data.latency_ms).toFixed(1) : "--";
    const tokens = hasTokens ? data.tokens : "--";
    row.innerHTML = `<span>${time}</span><span>${data.mode || ""}</span><span>${action}</span><span>${hostModel}</span><span>${rule}</span><span>${latency}</span><span>${tokens}</span>`;
    if (prepend) logEl.prepend(row);
    else logEl.appendChild(row);
  }

  function showError(msg) {
    const el = document.getElementById("cfg-error");
    const ok = document.getElementById("cfg-ok");
    ok.hidden = true;
    if (!msg) {
      el.hidden = true;
      el.textContent = "";
      return;
    }
    el.hidden = false;
    el.textContent = msg;
  }

  function showWarn(msg) {
    const el = document.getElementById("cfg-warn");
    if (!msg) {
      el.hidden = true;
      el.textContent = "";
      return;
    }
    el.hidden = false;
    el.textContent = msg;
  }

  function showOk() {
    const el = document.getElementById("cfg-ok");
    document.getElementById("cfg-error").hidden = true;
    el.hidden = false;
    setTimeout(() => { el.hidden = true; }, 2500);
  }

  function yamlDump(value) {
    if (value == null) return "";
    if (typeof value === "string") return value;
    return JSON.stringify(value, null, 2);
  }

  function parseYamlish(text, fallback) {
    const t = (text || "").trim();
    if (!t) return fallback;
    try {
      return JSON.parse(t);
    } catch (_) {
      throw new Error("Use JSON for list/map fields in the form (or Edit YAML). Invalid JSON: " + t.slice(0, 80));
    }
  }

  function linesToList(text) {
    return (text || "")
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean);
  }

  async function runValidate(cfg) {
    const res = await fetch("/api/validate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg || currentCfg || {}),
    });
    const data = await res.json();
    const parts = [];
    if (data.errors?.length) parts.push("Errors: " + data.errors.join("; "));
    if (data.warnings?.length) parts.push("Warnings: " + data.warnings.join("; "));
    showWarn(parts.join("\n") || "");
    return data;
  }

  function fillForm(cfg) {
    currentCfg = cfg;
    document.getElementById("cfg-proxy-port").value = cfg.server?.proxy_port ?? "";
    document.getElementById("cfg-dash-port").value = cfg.server?.dashboard_port ?? "";
    document.getElementById("cfg-log-level").value = cfg.server?.log_level || "info";

    document.getElementById("cfg-tokens").value = cfg.thresholds?.max_local_context_tokens ?? "";
    document.getElementById("cfg-idle").value = cfg.thresholds?.idle_unload_timeout ?? "";
    document.getElementById("cfg-req-timeout").value = cfg.thresholds?.request_timeout ?? "";

    document.getElementById("cfg-mitm-enabled").checked = !!cfg.mitm?.enabled;
    document.getElementById("cfg-mitm-port").value = cfg.mitm?.port ?? "";
    document.getElementById("cfg-mitm-passthrough").checked = !!cfg.mitm?.passthrough_default;
    // The CA paths are not editable here on purpose — Glider generates its
    // own CA and a user has no reason to repoint it. They are still carried
    // across a save: PUT /api/config replaces the whole document, so dropping
    // them would blank the paths, and LoadOrCreateAuthority("","") then mints
    // a fresh in-memory CA on every restart — silently invalidating a CA the
    // user had already installed in their trust store.
    preservedMitmCA = { ca_cert: cfg.mitm?.ca_cert ?? "", ca_key: cfg.mitm?.ca_key ?? "" };
    document.getElementById("cfg-mitm-hosts").value = (cfg.mitm?.hosts || []).join("\n");

    document.getElementById("cfg-vram-strategy").value = cfg.vram?.strategy || "dynamic";
    document.getElementById("cfg-vram-headroom").value = cfg.vram?.headroom_mb ?? "";
    document.getElementById("cfg-vram-max").value = cfg.vram?.max_loaded_models ?? "";
    document.getElementById("cfg-vram-gpus").value = yamlDump(cfg.vram?.gpu_assignments || {});

    document.getElementById("cfg-dash-enabled").checked = !!cfg.dashboard?.enabled;

    document.getElementById("cfg-xform-enabled").checked = !!cfg.transform?.enabled;
    document.getElementById("cfg-xform-trim").checked = !!cfg.transform?.trim_context;
    document.getElementById("cfg-xform-prepend").value = cfg.transform?.augment_prepend ?? "";
    document.getElementById("cfg-xform-append").value = cfg.transform?.augment_append ?? "";

    document.getElementById("cfg-aliases").value = yamlDump(cfg.model_aliases || {});
    document.getElementById("cfg-routing").value = yamlDump(cfg.routing?.rules || []);
    document.getElementById("cfg-models").value = yamlDump(cfg.models || []);
    document.getElementById("cfg-backends").value = yamlDump(cfg.backends || []);
    document.getElementById("cfg-providers").value = yamlDump(cfg.cloud?.providers || []);
    document.getElementById("cfg-budget").value = cfg.cloud?.budget_cap_usd ?? "";
    document.getElementById("cfg-rpm").value = cfg.cloud?.rate_limit?.requests_per_minute ?? "";
    document.getElementById("cfg-tpm").value = cfg.cloud?.rate_limit?.tokens_per_minute ?? "";

    renderRulesEditor(cfg);
    runValidate(cfg);
  }

  function collectForm() {
    return {
      server: {
        proxy_port: Number(document.getElementById("cfg-proxy-port").value),
        dashboard_port: Number(document.getElementById("cfg-dash-port").value),
        log_level: document.getElementById("cfg-log-level").value,
      },
      thresholds: {
        max_local_context_tokens: Number(document.getElementById("cfg-tokens").value),
        idle_unload_timeout: document.getElementById("cfg-idle").value,
        request_timeout: document.getElementById("cfg-req-timeout").value,
      },
      mitm: {
        enabled: document.getElementById("cfg-mitm-enabled").checked,
        port: Number(document.getElementById("cfg-mitm-port").value),
        passthrough_default: document.getElementById("cfg-mitm-passthrough").checked,
        ca_cert: preservedMitmCA.ca_cert,
        ca_key: preservedMitmCA.ca_key,
        hosts: linesToList(document.getElementById("cfg-mitm-hosts").value),
      },
      vram: {
        strategy: document.getElementById("cfg-vram-strategy").value,
        headroom_mb: Number(document.getElementById("cfg-vram-headroom").value),
        max_loaded_models: Number(document.getElementById("cfg-vram-max").value),
        gpu_assignments: parseYamlish(document.getElementById("cfg-vram-gpus").value, {}),
      },
      dashboard: {
        enabled: document.getElementById("cfg-dash-enabled").checked,
      },
      transform: {
        enabled: document.getElementById("cfg-xform-enabled").checked,
        trim_context: document.getElementById("cfg-xform-trim").checked,
        augment_prepend: document.getElementById("cfg-xform-prepend").value,
        augment_append: document.getElementById("cfg-xform-append").value,
      },
      model_aliases: parseYamlish(document.getElementById("cfg-aliases").value, {}),
      routing: { rules: parseYamlish(document.getElementById("cfg-routing").value, []) },
      models: parseYamlish(document.getElementById("cfg-models").value, []),
      backends: parseYamlish(document.getElementById("cfg-backends").value, []),
      cloud: {
        providers: parseYamlish(document.getElementById("cfg-providers").value, []),
        budget_cap_usd: Number(document.getElementById("cfg-budget").value),
        rate_limit: {
          requests_per_minute: Number(document.getElementById("cfg-rpm").value),
          tokens_per_minute: Number(document.getElementById("cfg-tpm").value),
        },
      },
    };
  }

  async function saveJSON(cfg) {
    showError("");
    const res = await fetch("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg),
    });
    const text = await res.text();
    if (!res.ok) {
      showError(text.trim() || `Save failed (${res.status})`);
      return null;
    }
    const warn = res.headers.get("X-Glider-Warnings");
    if (warn) showWarn("Warnings: " + warn);
    const br = res.headers.get("X-Glider-Backend-Reload");
    if (br === "error") {
      showWarn("Backend reload failed (previous clients kept): " + (res.headers.get("X-Glider-Backend-Reload-Error") || "unknown"));
    } else if (br === "ok") {
      const bw = res.headers.get("X-Glider-Backend-Reload-Warnings");
      if (bw) showWarn("Backends reloaded with warnings: " + bw);
    }
    try {
      return JSON.parse(text);
    } catch (_) {
      showOk();
      return cfg;
    }
  }

  async function saveYAML(yamlText) {
    showError("");
    const res = await fetch("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/yaml" },
      body: yamlText,
    });
    const text = await res.text();
    if (!res.ok) {
      showError(text.trim() || `Save failed (${res.status})`);
      return null;
    }
    const br = res.headers.get("X-Glider-Backend-Reload");
    if (br === "error") {
      showWarn("Backend reload failed (previous clients kept): " + (res.headers.get("X-Glider-Backend-Reload-Error") || "unknown"));
    } else if (br === "ok") {
      const bw = res.headers.get("X-Glider-Backend-Reload-Warnings");
      if (bw) showWarn("Backends reloaded with warnings: " + bw);
    }
    try {
      return JSON.parse(text);
    } catch (_) {
      return null;
    }
  }

  async function loadConfig() {
    const res = await fetch("/api/config");
    if (!res.ok) {
      showError("Failed to load config");
      return;
    }
    const cfg = await res.json();
    fillForm(cfg);
  }

  async function loadYamlEditor() {
    const res = await fetch("/api/config?format=yaml");
    if (!res.ok) {
      showError("Failed to load YAML");
      return;
    }
    document.getElementById("cfg-yaml").value = await res.text();
  }

  function renderGPUGauges(gpus) {
    const g = document.getElementById("gpu-gauges");
    if (!gpus || !gpus.length) {
      g.innerHTML = `<div class="gauge"><div class="gauge-label">No GPU data (nvidia-smi unavailable)</div></div>`;
      return;
    }
    g.innerHTML = gpus.map((gpu) => {
      if (gpu.error) {
        return `<div class="gauge"><div class="gauge-label">GPU ${gpu.index} -- ${gpu.error}</div></div>`;
      }
      const usedPct = gpu.total_bytes ? Math.round((gpu.used_bytes / gpu.total_bytes) * 100) : 0;
      return `<div class="gauge">
        <div class="gauge-label">GPU ${gpu.index} -- ${usedPct}% used | ${gpu.used_mb}/${gpu.total_mb} MB</div>
        <div class="gauge-bar"><div class="gauge-used" style="width:${usedPct}%"></div></div>
      </div>`;
    }).join("");
  }

  function renderVRAMModels(snap) {
    const tbody = document.querySelector("#models-table tbody");
    tbody.innerHTML = "";
    gpuAssignmentDraft = { ...(snap.gpu_assignments || {}) };
    const gpuCount = (snap.gpus || []).filter((g) => !g.error).length;
    const options = [];
    options.push(`<option value="">--</option>`);
    const n = Math.max(gpuCount, 1);
    for (let i = 0; i < n; i++) {
      options.push(`<option value="${i}">${i}</option>`);
    }

    (snap.models || []).forEach((m) => {
      const name = m.name || m.Name;
      const assigned = gpuAssignmentDraft[name];
      const vramLabel = m.size_vram
        ? `${Math.round(m.size_vram / (1024 * 1024))} MB live`
        : `${m.vram_estimate_mb || 0} MB est.`;
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td>${name}${m.in_config ? "" : ' <span class="tag">discovered</span>'}</td>
        <td>${m.backend || ""}</td>
        <td>${m.source || ""}</td>
        <td>${vramLabel}</td>
        <td>${m.state || (m.available ? "available" : "config")}</td>
        <td><select class="gpu-select" data-name="${name}">${options.join("")}</select></td>
        <td>
          <button data-action="load" data-name="${name}">load</button>
          <button data-action="unload" data-name="${name}">unload</button>
        </td>`;
      tbody.appendChild(tr);
      const sel = tr.querySelector("select");
      if (assigned != null && assigned !== "") sel.value = String(assigned);
      sel.addEventListener("change", () => {
        const v = sel.value;
        if (v === "") delete gpuAssignmentDraft[name];
        else gpuAssignmentDraft[name] = Number(v);
      });
    });

    tbody.querySelectorAll("button").forEach((b) => {
      b.addEventListener("click", async () => {
        await fetch(`/api/models/${encodeURIComponent(b.dataset.name)}/${b.dataset.action}`, { method: "POST" });
        loadVRAM();
      });
    });
  }

  async function loadVRAM() {
    const errEl = document.getElementById("vram-errors");
    const warnEl = document.getElementById("vram-warnings");
    errEl.hidden = true;
    if (warnEl) warnEl.hidden = true;
    const res = await fetch("/api/vram");
    if (!res.ok) {
      errEl.hidden = false;
      errEl.textContent = "Failed to load VRAM snapshot";
      return;
    }
    const snap = await res.json();
    renderGPUGauges(snap.gpus);
    renderVRAMModels(snap);
    if (snap.backend_errors?.length) {
      errEl.hidden = false;
      errEl.textContent = snap.backend_errors.join("; ");
    }
    if (warnEl && snap.backend_warnings?.length) {
      warnEl.hidden = false;
      warnEl.textContent = "Optional backend unreachable: " + snap.backend_warnings.join("; ");
    }
  }

  // --- Rules editor ---
  function ruleEnabled(r) {
    return r.enabled !== false;
  }

  function renderRulesEditor(cfg) {
    const root = document.getElementById("rules-editor");
    if (!root) return;
    const rules = (cfg?.routing?.rules || []).slice().sort((a, b) => (b.priority || 0) - (a.priority || 0));
    root.innerHTML = "";
    if (!rules.length) {
      root.innerHTML = `<p class="hint">No rules yet. Add one to get started.</p>`;
      return;
    }
    rules.forEach((r, idx) => {
      const card = document.createElement("article");
      card.className = "rule-card";
      card.dataset.idx = String(idx);
      const trig = r.trigger || {};
      const act = r.action || {};
      card.innerHTML = `
        <div class="rule-card-head">
          <label data-tip-key="rule-name">Name<input data-f="name" value="${esc(r.name || "")}" /></label>
          <label data-tip-key="rule-priority">Priority<input data-f="priority" type="number" value="${r.priority ?? 0}" /></label>
          <label class="check" data-tip-key="rule-enabled"><input data-f="enabled" type="checkbox" ${ruleEnabled(r) ? "checked" : ""}/> Enabled</label>
          <button type="button" class="linkish rule-del" data-tip="Removes this rule from the list. Push Save rules to write the change.">Remove</button>
        </div>
        <div class="rule-card-grid">
          <label data-tip-key="rule-trigger-type">Trigger type
            <select data-f="trigger.type">
              ${opt("explicit", trig.type)}
              ${opt("context_size", trig.type)}
              ${opt("script", trig.type)}
              ${opt("always", trig.type)}
              ${opt("regex", trig.type)}
            </select>
          </label>
          <label data-tip-key="rule-commands">Commands<input data-f="trigger.commands" value="${esc((trig.commands || []).join(", "))}" /></label>
          <label data-tip-key="rule-pattern">Pattern<input data-f="trigger.pattern" value="${esc(trig.pattern || "")}" /></label>
          <label data-tip-key="rule-script">Script file<input data-f="trigger.file" value="${esc(trig.file || "")}" /></label>
          <label data-tip-key="rule-operator">Operator<input data-f="trigger.operator" value="${esc(trig.operator || "")}" /></label>
          <label data-tip-key="rule-value">Value<input data-f="trigger.value" type="number" value="${trig.value ?? 0}" /></label>
          <label data-tip-key="rule-target">Action target
            <select data-f="action.target">
              ${opt("local", act.target)}
              ${opt("cloud", act.target)}
            </select>
          </label>
          <label data-tip-key="rule-backend">Backend<input data-f="action.backend" value="${esc(act.backend || "")}" /></label>
          <label data-tip-key="rule-model">Model<input data-f="action.model" value="${esc(act.model || "")}" /></label>
          <label data-tip-key="rule-adapter">Adapter<input data-f="action.adapter" value="${esc(act.adapter || "")}" /></label>
        </div>`;
      root.appendChild(card);
      bindCustomTips(card);
      card.querySelector(".rule-del").addEventListener("click", () => {
        const all = collectRulesFromEditor();
        all.splice(idx, 1);
        if (!currentCfg) currentCfg = {};
        currentCfg.routing = { rules: all };
        renderRulesEditor(currentCfg);
      });
    });
  }

  function opt(v, cur) {
    return `<option value="${v}" ${cur === v ? "selected" : ""}>${v}</option>`;
  }

  function setPath(obj, path, value) {
    const parts = path.split(".");
    let cur = obj;
    for (let i = 0; i < parts.length - 1; i++) {
      if (!cur[parts[i]]) cur[parts[i]] = {};
      cur = cur[parts[i]];
    }
    cur[parts[parts.length - 1]] = value;
  }

  function collectRulesFromEditor() {
    const cards = [...document.querySelectorAll("#rules-editor .rule-card")];
    return cards.map((card) => {
      const rule = { trigger: {}, action: {} };
      card.querySelectorAll("[data-f]").forEach((el) => {
        const path = el.dataset.f;
        let val;
        if (el.type === "checkbox") val = el.checked;
        else if (el.type === "number") val = Number(el.value);
        else val = el.value;
        if (path === "trigger.commands") {
          val = String(el.value).split(",").map((s) => s.trim()).filter(Boolean);
        }
        if (path === "enabled") {
          rule.enabled = !!val;
          return;
        }
        setPath(rule, path, val);
      });
      return rule;
    });
  }

  document.getElementById("rule-add").addEventListener("click", () => {
    if (!currentCfg) currentCfg = { routing: { rules: [] } };
    if (!currentCfg.routing) currentCfg.routing = { rules: [] };
    currentCfg.routing.rules = collectRulesFromEditor();
    currentCfg.routing.rules.push({
      name: "New rule",
      priority: 1,
      enabled: true,
      trigger: { type: "explicit", commands: ["/local"] },
      action: { target: "local", model: "" },
    });
    renderRulesEditor(currentCfg);
  });

  document.getElementById("rules-save").addEventListener("click", async () => {
    const err = document.getElementById("rules-error");
    const ok = document.getElementById("rules-ok");
    err.hidden = true;
    ok.hidden = true;
    try {
      const rules = collectRulesFromEditor();
      const base = currentCfg || (await (await fetch("/api/config")).json());
      base.routing = { rules };
      const saved = await saveJSON(base);
      if (saved) {
        fillForm(saved);
        ok.hidden = false;
        setTimeout(() => { ok.hidden = true; }, 2500);
        loadRulesLint();
      } else {
        err.hidden = false;
        err.textContent = document.getElementById("cfg-error").textContent || "Save failed";
      }
    } catch (e) {
      err.hidden = false;
      err.textContent = e.message || String(e);
    }
  });

  document.getElementById("rules-reload").addEventListener("click", () => loadConfig());

  document.getElementById("vram-refresh").addEventListener("click", () => loadVRAM());
  document.getElementById("vram-save-gpus").addEventListener("click", async () => {
    const res = await fetch("/api/gpu-assignments", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ assignments: gpuAssignmentDraft }),
    });
    if (!res.ok) {
      document.getElementById("vram-errors").hidden = false;
      document.getElementById("vram-errors").textContent = await res.text();
      return;
    }
    await loadConfig();
    await loadVRAM();
  });

  // --- Sessions ---
  async function loadSessions() {
    const sel = document.getElementById("session-select");
    const res = await fetch("/api/sessions");
    if (!res.ok) return;
    const sessions = await res.json();
    const prev = sel.value;
    sel.innerHTML = "";
    (sessions || []).forEach((s) => {
      const optEl = document.createElement("option");
      optEl.value = s.id;
      const label = s.current ? `Current | ${s.id.slice(0, 12)}...` : `${new Date(s.started_at).toLocaleString()} | ${s.request_count} req`;
      optEl.textContent = label;
      if (s.current) optEl.selected = true;
      sel.appendChild(optEl);
    });
    if (prev && [...sel.options].some((o) => o.value === prev)) sel.value = prev;
    const current = sessions.find((s) => s.current) || sessions[0];
    if (current) {
      viewingSessionId = current.id;
      liveMode = !!current.current;
      await loadSessionView(current.id, !!current.current);
      await loadEpisodes(current.id);
    }
  }

  async function loadSessionView(id, isLive) {
    viewingSessionId = id;
    liveMode = isLive;
    const meta = document.getElementById("session-meta");
    const aggRes = await fetch(`/api/sessions/${encodeURIComponent(id)}`);
    if (aggRes.ok) {
      const agg = await aggRes.json();
      const s = agg.session || {};
      meta.textContent = `${s.request_count || 0} requests | ${s.token_total || 0} tokens | avg ${Number(agg.avg_latency_ms || 0).toFixed(1)}ms`;
      if (!isLive) {
        tokenTotal = s.token_total || 0;
        latencySum = s.latency_sum_ms || 0;
        if (agg.distribution) {
          // Not s.request_count: that is the session's record count, which
          // omits the actions countRequests exists to include.
          const c = countRequests(agg.distribution);
          seen = c.seen;
          applyDistribution(agg.distribution, { requests: c.handled });
        } else {
          requests = s.request_count || 0;
          seen = requests;
          local = s.local_count || 0;
          cloud = s.cloud_count || 0;
          canned = s.canned_count || 0;
          updateMetricsUI();
        }
      }
    }
    if (!isLive) {
      logEl.innerHTML = "";
      const reqRes = await fetch(`/api/sessions/${encodeURIComponent(id)}/requests?limit=200`);
      if (reqRes.ok) {
        const reqs = await reqRes.json();
        // API returns newest first; append in reverse so newest ends on top via prepend... already newest first, prepend each would reverse -- append in order
        reqs.forEach((r) => addLog(r, { live: false, prepend: false }));
      }
    } else if (logEl.children.length === 0) {
      // hydrate current session history once
      const reqRes = await fetch(`/api/sessions/${encodeURIComponent(id)}/requests?limit=100`);
      if (reqRes.ok) {
        const reqs = await reqRes.json();
        reqs.reverse().forEach((r) => addLog(r, { live: false, prepend: true }));
        // rebuild live counters from aggregates / distribution (includes origin_passthrough as cloud)
        const aggRes2 = await fetch(`/api/sessions/${encodeURIComponent(id)}`);
        if (aggRes2.ok) {
          const agg = await aggRes2.json();
          const s = agg.session || {};
          tokenTotal = s.token_total || 0;
          latencySum = s.latency_sum_ms || 0;
          // Deliberately does NOT set requests/seen here. This branch is the
          // live session, and the session aggregate is narrower than the
          // truth: MITM decrypt/skip/blind_tunnel bump the global counters
          // but never become session RequestRecords, so hydrating from it
          // overwrites a correct global count with a smaller one.
          // refreshMetricsSnapshot owns those two while live.
          if (agg.distribution) {
            applyDistribution(agg.distribution, { requests });
          } else {
            local = s.local_count || 0;
            cloud = s.cloud_count || 0;
            canned = s.canned_count || 0;
            updateMetricsUI();
          }
        }
      }
    }
  }

  document.getElementById("session-select").addEventListener("change", async (e) => {
    const id = e.target.value;
    const opt = e.target.selectedOptions[0];
    const isLive = opt && opt.textContent.startsWith("Current");
    logEl.innerHTML = "";
    if (isLive) resetLiveMetrics();
    await loadSessionView(id, isLive);
    await loadEpisodes(id);
  });

  document.getElementById("session-refresh").addEventListener("click", () => loadSessions());

  document.getElementById("cfg-toggle-yaml").addEventListener("click", () => {
    const wrap = document.getElementById("yaml-editor-wrap");
    const btn = document.getElementById("cfg-toggle-yaml");
    const open = wrap.hidden;
    wrap.hidden = !open;
    btn.setAttribute("aria-expanded", open ? "true" : "false");
    btn.textContent = open ? "Hide YAML editor" : "Edit YAML (optional)";
    if (open) loadYamlEditor();
  });

  document.getElementById("settings-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      const cfg = collectForm();
      // Prefer rules editor state if present
      const edited = collectRulesFromEditor();
      if (edited.length) cfg.routing = { rules: edited };
      const saved = await saveJSON(cfg);
      if (saved) {
        fillForm(saved);
        showOk();
        document.getElementById("status").textContent = "Status: OK";
      }
    } catch (err) {
      showError(err.message || String(err));
    }
  });

  document.getElementById("cfg-reload").addEventListener("click", () => {
    showError("");
    loadConfig();
  });

  document.getElementById("cfg-yaml-save").addEventListener("click", async () => {
    const saved = await saveYAML(document.getElementById("cfg-yaml").value);
    if (saved) {
      fillForm(saved);
      showOk();
      await loadYamlEditor();
    }
  });

  document.getElementById("cfg-yaml-reload").addEventListener("click", () => {
    showError("");
    loadYamlEditor();
  });

  /** Loop API StageKind values only (see internal/loop/stages.go). */
	const STAGE_KINDS = ["workspace", "router", "planner", "actor", "critic", "memory", "context", "human_gate"];
  let liveWsConnected = false;
  let hotswapGenCache = {};
  let lastHotswapSnap = "";

  function openDialog(el) {
    if (!el) return;
    if (typeof el.showModal === "function") el.showModal();
    else el.setAttribute("open", "");
  }

  function closeDialog(el) {
    if (!el) return;
    if (typeof el.close === "function") el.close();
    else el.removeAttribute("open");
  }

  /** In-app prompt replacement for window.prompt. */
  function gliderPrompt(title, hint, initial) {
    return new Promise((resolve) => {
      const dlg = document.getElementById("glider-prompt-dialog");
      const form = document.getElementById("glider-prompt-form");
      const titleEl = document.getElementById("glider-prompt-title");
      const hintEl = document.getElementById("glider-prompt-hint");
      const input = document.getElementById("glider-prompt-input");
      if (!dlg || !form || !input) {
        resolve(null);
        return;
      }
      if (titleEl) titleEl.textContent = title || "Input";
      if (hintEl) hintEl.textContent = hint || "";
      input.value = initial == null ? "" : String(initial);
      const onCancel = () => {
        cleanup();
        resolve(null);
      };
      const onSubmit = (ev) => {
        ev.preventDefault();
        const v = input.value;
        cleanup();
        resolve(v);
      };
      function cleanup() {
        form.removeEventListener("submit", onSubmit);
        document.getElementById("glider-prompt-cancel")?.removeEventListener("click", onCancel);
        closeDialog(dlg);
      }
      form.addEventListener("submit", onSubmit);
      document.getElementById("glider-prompt-cancel")?.addEventListener("click", onCancel);
      openDialog(dlg);
      setTimeout(() => input.focus(), 30);
    });
  }

  /** In-app confirm replacement. */
  function gliderConfirm(title, message) {
    return new Promise((resolve) => {
      const dlg = document.getElementById("glider-confirm-dialog");
      const form = document.getElementById("glider-confirm-form");
      if (!dlg || !form) {
        resolve(false);
        return;
      }
      const titleEl = document.getElementById("glider-confirm-title");
      const msgEl = document.getElementById("glider-confirm-message");
      if (titleEl) titleEl.textContent = title || "Confirm";
      if (msgEl) msgEl.textContent = message || "";
      const onCancel = () => {
        cleanup();
        resolve(false);
      };
      const onSubmit = (ev) => {
        ev.preventDefault();
        cleanup();
        resolve(true);
      };
      function cleanup() {
        form.removeEventListener("submit", onSubmit);
        document.getElementById("glider-confirm-cancel")?.removeEventListener("click", onCancel);
        closeDialog(dlg);
      }
      form.addEventListener("submit", onSubmit);
      document.getElementById("glider-confirm-cancel")?.addEventListener("click", onCancel);
      openDialog(dlg);
    });
  }

  // Shows the state of the WebSocket connection in the topbar. This used
  // #live-ws before. That element was inside the removed panel-hoops section,
  // so the state had no display at all. #status is the equivalent live
  // element, and its name agrees with this function.
  function setLiveWs(text, ok) {
    const el = document.getElementById("status");
    if (!el) return;
    el.textContent = "Status: " + text;
    el.classList.toggle("ok", !!ok);
    el.classList.toggle("bad", ok === false);
  }

  let mcpServersCache = [];
  let mcpSelectedServerId = "";

  function showMCPError(msg) {
    const e = document.getElementById("mcp-error");
    const o = document.getElementById("mcp-ok");
    if (o) o.hidden = true;
    if (!e) return;
    if (!msg) { e.hidden = true; e.textContent = ""; return; }
    e.hidden = false;
    e.textContent = msg;
  }

  function showMCPOk(msg) {
    const e = document.getElementById("mcp-error");
    const o = document.getElementById("mcp-ok");
    if (e) e.hidden = true;
    if (!o) return;
    o.hidden = false;
    o.textContent = msg || "OK";
  }

  function showVendorsError(msg) {
    const e = document.getElementById("vendors-error");
    const o = document.getElementById("vendors-ok");
    if (o) o.hidden = true;
    if (!e) return;
    if (!msg) { e.hidden = true; return; }
    e.hidden = false;
    e.textContent = msg;
  }

  function showVendorsOk(msg) {
    const e = document.getElementById("vendors-error");
    const o = document.getElementById("vendors-ok");
    if (e) e.hidden = true;
    if (!o) return;
    o.hidden = false;
    o.textContent = msg || "OK";
  }

  function renderVendorCards(vendorsList) {
    const container = document.getElementById("vendors-cards");
    if (!container) return;
    if (!vendorsList.length) {
      container.innerHTML = `<p class="hint">No CLIs found yet. Click "Rescan for CLIs" above.</p>`;
      return;
    }
    container.innerHTML = vendorsList.map((v) => {
      const toggleAction = v.enabled ? "disable" : "enable";
      return `<div class="vendor-card" data-vendor-name="${escapeAttr(v.name)}">
        <div class="vendor-card-head">
          <div>
            <div class="vendor-card-name">${escapeHtml(v.name)}</div>
            <div class="vendor-card-path">${escapeHtml(v.path || v.binary)}${v.version ? " · " + escapeHtml(v.version) : ""}</div>
          </div>
          <div class="vendor-card-actions">
            <label class="vendor-enable-toggle">
              <input type="checkbox" data-vendor-action="${toggleAction}" ${v.enabled ? "checked" : ""}>
              ${v.enabled ? "Enabled" : "Disabled"}
            </label>
            <button type="button" class="vendor-launch-btn" data-vendor-action="launch">▶ Launch interactively</button>
          </div>
        </div>

        <div class="vendor-card-section">
          <div class="vendor-card-section-label">How much can it do on its own?</div>
          <div class="preset-buttons" data-vendor-presets="${escapeAttr(v.name)}">
            <p class="hint">Loading…</p>
          </div>
        </div>

        <details class="vendor-advanced">
          <summary>Advanced: raw launch command (YAML/JSON)</summary>
          <div class="vendor-advanced-body">
            <p class="hint" style="margin:0 0 6px">
              Exact command-line args used for headless delegation. <code>{{prompt}}</code>, <code>{{session_id}}</code>
              (for a "resume" template), and <code>{{cwd}}</code> are substituted at run time. Most people never need
              to touch this — use the buttons above instead.
            </p>
            <textarea class="vendor-templates-editor" data-vendor-templates-editor="${escapeAttr(v.name)}"
              rows="8" style="width:100%;font-family:monospace;font-size:12px"
            >${escapeHtml(JSON.stringify(v.templates || [], null, 2))}</textarea>
            <p class="cfg-error vendor-templates-error" data-vendor-templates-error="${escapeAttr(v.name)}" hidden></p>
            <div style="margin-top:6px">
              <button type="button" class="linkish" data-vendor-action="save-templates">Save</button>
            </div>
          </div>
        </details>
      </div>`;
    }).join("");

    vendorsList.forEach((v) => loadVendorPresets(v.name));
  }

  async function loadVendorPresets(name) {
    const holder = document.querySelector(`[data-vendor-presets="${CSS.escape(name)}"]`);
    if (!holder) return;
    try {
      const res = await fetch(`/api/vendors/${encodeURIComponent(name)}/permissions`);
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const presets = Array.isArray(data.presets) ? data.presets : [];
      if (!presets.length) {
        holder.innerHTML = `<p class="hint">No presets for this CLI.</p>`;
        return;
      }
      holder.innerHTML = presets.map((p) => `
        <button type="button" class="preset-btn${p.id === data.current ? " active" : ""}${p.risky ? " risky" : ""}"
          data-vendor-preset="${escapeAttr(p.id)}" ${presets.length === 1 ? "disabled" : ""}>
          <div class="preset-btn-label">${escapeHtml(p.label)}</div>
          <div class="preset-btn-desc">${escapeHtml(p.description)}</div>
        </button>`).join("");
    } catch (err) {
      holder.innerHTML = `<p class="cfg-error">${escapeHtml(String(err.message || err))}</p>`;
    }
  }

  async function refreshVendorsPanel() {
    showVendorsError("");
    try {
      const res = await fetch("/api/vendors");
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      renderVendorCards(Array.isArray(data.vendors) ? data.vendors : []);
      const workspaceInput = document.getElementById("vendors-default-workspace");
      if (workspaceInput) workspaceInput.value = data.defaultWorkspace || "";
      const detailSelect = document.getElementById("vendors-response-detail");
      if (detailSelect) detailSelect.value = data.responseDetail || "clean";
    } catch (err) {
      showVendorsError(String(err.message || err));
    }
  }

  async function saveDefaultWorkspace() {
    showVendorsError("");
    const input = document.getElementById("vendors-default-workspace");
    if (!input) return;
    try {
      const res = await fetch("/api/vendors/workspace", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: input.value }),
      });
      if (!res.ok) throw new Error(await res.text());
      showVendorsOk("Default folder saved.");
      refreshVendorsPanel();
    } catch (err) {
      showVendorsError(String(err.message || err));
    }
  }

  document.getElementById("vendors-default-workspace-save")?.addEventListener("click", () => saveDefaultWorkspace());

  async function saveResponseDetail(mode) {
    showVendorsError("");
    try {
      const res = await fetch("/api/vendors/response-detail", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode }),
      });
      if (!res.ok) throw new Error(await res.text());
      showVendorsOk(mode === "raw" ? "Showing raw output from delegated tasks." : "Showing clean answers from delegated tasks.");
    } catch (err) {
      showVendorsError(String(err.message || err));
    }
  }

  document.getElementById("vendors-response-detail")?.addEventListener("change", (e) => saveResponseDetail(e.target.value));

  async function vendorsDiscover() {
    showVendorsError("");
    try {
      const res = await fetch("/api/vendors/discover", { method: "POST" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      renderVendorCards(Array.isArray(data.vendors) ? data.vendors : []);
      showVendorsOk(`Found ${(data.vendors || []).length} CLI(s).`);
    } catch (err) {
      showVendorsError(String(err.message || err));
    }
  }

  async function vendorAction(name, action) {
    showVendorsError("");
    try {
      const res = await fetch(`/api/vendors/${encodeURIComponent(name)}/${action}`, { method: "POST" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      renderVendorCards(Array.isArray(data.vendors) ? data.vendors : []);
    } catch (err) {
      showVendorsError(String(err.message || err));
    }
  }

  async function launchVendorInteractive(name) {
    showVendorsError("");
    try {
      const res = await fetch(`/api/vendors/${encodeURIComponent(name)}/launch-interactive`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({}),
      });
      if (!res.ok) throw new Error(await res.text());
      showVendorsOk(`Launched ${name} in a new window.`);
    } catch (err) {
      showVendorsError(String(err.message || err));
    }
  }

  async function applyVendorPreset(name, preset) {
    showVendorsError("");
    try {
      const res = await fetch(`/api/vendors/${encodeURIComponent(name)}/permissions`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ preset }),
      });
      if (!res.ok) throw new Error(await res.text());
      showVendorsOk(`${name}: switched to "${preset.replace(/_/g, " ")}".`);
      loadVendorPresets(name);
      refreshVendorsPanel(); // claude's preset changes its templates — reload so Advanced stays in sync
    } catch (err) {
      showVendorsError(String(err.message || err));
    }
  }

  async function saveVendorTemplates(name) {
    const editor = document.querySelector(`[data-vendor-templates-editor="${CSS.escape(name)}"]`);
    const errEl = document.querySelector(`[data-vendor-templates-error="${CSS.escape(name)}"]`);
    if (errEl) errEl.hidden = true;
    if (!editor) return;

    let templates;
    try {
      templates = JSON.parse(editor.value);
    } catch (e) {
      if (errEl) { errEl.hidden = false; errEl.textContent = "Invalid JSON: " + e.message; }
      return;
    }

    try {
      const res = await fetch(`/api/vendors/${encodeURIComponent(name)}/templates`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ templates }),
      });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      renderVendorCards(Array.isArray(data.vendors) ? data.vendors : []);
      showVendorsOk(`Saved launch command for ${name}.`);
    } catch (err) {
      if (errEl) { errEl.hidden = false; errEl.textContent = String(err.message || err); }
    }
  }

  document.getElementById("vendors-discover")?.addEventListener("click", () => vendorsDiscover());
  document.getElementById("vendors-cards")?.addEventListener("click", (ev) => {
    const presetBtn = ev.target.closest("[data-vendor-preset]");
    if (presetBtn) {
      const card = ev.target.closest("[data-vendor-name]");
      if (!card) return;
      applyVendorPreset(card.getAttribute("data-vendor-name"), presetBtn.getAttribute("data-vendor-preset"));
      return;
    }
    const btn = ev.target.closest("[data-vendor-action]");
    if (!btn) return;
    const action = btn.getAttribute("data-vendor-action");
    const card = ev.target.closest("[data-vendor-name]");
    if (!card) return;
    const name = card.getAttribute("data-vendor-name");

    if (action === "launch") {
      launchVendorInteractive(name);
      return;
    }
    if (action === "save-templates") {
      saveVendorTemplates(name);
      return;
    }
    // "enable" / "disable" also fire on the checkbox's own change event below;
    // this click handler only needs to cover it when something other than
    // the checkbox itself (e.g. its label text) was clicked.
    if (action === "enable" || action === "disable") {
      if (ev.target.tagName === "INPUT") return; // let the change listener own this one
      vendorAction(name, action);
    }
  });
  document.getElementById("vendors-cards")?.addEventListener("change", (ev) => {
    const checkbox = ev.target.closest("[data-vendor-action]");
    if (!checkbox || checkbox.tagName !== "INPUT") return;
    const card = ev.target.closest("[data-vendor-name]");
    if (!card) return;
    vendorAction(card.getAttribute("data-vendor-name"), checkbox.getAttribute("data-vendor-action"));
  });

  async function refreshMCPPanel() {
    showMCPError("");
    try {
      const res = await fetch("/api/mcp/servers");
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      mcpServersCache = Array.isArray(data.servers) ? data.servers : [];
      renderMCPGitHubCard(data.github || {});
      renderMCPServersTable(mcpServersCache);
      if (mcpSelectedServerId) {
        await loadMCPToolsCatalog(mcpSelectedServerId);
      } else if (mcpServersCache.length) {
        mcpSelectedServerId = mcpServersCache[0].id;
        await loadMCPToolsCatalog(mcpSelectedServerId);
      } else {
        const list = document.getElementById("mcp-tools-list");
        if (list) list.innerHTML = "<p class=\"hint\">No servers configured.</p>";
      }
    } catch (err) {
      showMCPError(String(err.message || err));
    }
  }

  function renderMCPGitHubCard(gh) {
    const el = document.getElementById("mcp-github-card");
    if (!el) return;
    const tok = gh.token_configured
      ? `<span class="live-value ok">configured</span> (<code>${escapeHtml(gh.token_source || gh.token_env || "GITHUB_*")}</code>)`
      : `<span class="live-value bad">missing</span> — Sign in or paste a PAT below`;
    const http = gh.http_connected
      ? `<span class="live-value ok">connected</span>`
      : `<span class="live-value bad" title="${escapeAttr(gh.http_health_error || "")}">disconnected</span>`;
    const stdio = gh.stdio_connected
      ? `<span class="live-value ok">connected</span>`
      : `<span class="live-value" title="${escapeAttr(gh.stdio_health_error || "")}">idle</span>`;
    const oauth = gh.device_flow_ready
      ? `<span class="live-value ok">ready</span>`
      : `<span class="live-value">set GLIDER_GITHUB_OAUTH_CLIENT_ID</span>`;
    el.innerHTML = `
      <div class="mcp-github-grid">
        <div data-tip="Shows if a GitHub token is available. Glider looks in the environment variables and in ~/.glider/credentials/github_token."><span class="live-label">Token</span>${tok}</div>
        <div data-tip="The GitHub MCP server on HTTP (api.githubcopilot.com/mcp/)"><span class="live-label">HTTP (github)</span>${http}</div>
        <div data-tip="An optional local GitHub MCP process that uses stdio"><span class="live-label">Stdio (github-stdio)</span>${stdio}</div>
        <div data-tip="Shows if the OAuth procedure is available. It needs GLIDER_GITHUB_OAUTH_CLIENT_ID."><span class="live-label">Device flow</span>${oauth}</div>
        <div data-tip="The URL of the remote MCP server"><span class="live-label">Endpoint</span><code>${escapeHtml(gh.remote_url || "")}</code></div>
      </div>
      ${gh.hint ? `<p class="hint" style="margin:10px 0 0">${escapeHtml(gh.hint)}</p>` : ""}
      <div class="mcp-github-actions">
        <button type="button" class="primary" data-mcp-gh="signin" data-tip="Starts the GitHub OAuth procedure in the browser. If there is no client secret, Glider uses the device procedure. Glider then keeps the token and connects to MCP.">Sign in with GitHub</button>
        <button type="button" class="linkish" data-mcp-gh="pat" data-tip="Lets you write a personal access token. Glider saves it to ~/.glider/credentials/github_token.">Paste PAT</button>
        <button type="button" class="linkish" data-mcp-gh="forget" data-tip="Deletes the saved credential file and disconnects the GitHub MCP sessions.">Forget token</button>
      </div>
      <div id="mcp-github-device-panel" class="mcp-device-panel" hidden></div>`;
  }

  function renderMCPServersTable(servers) {
    const body = document.getElementById("mcp-servers-body");
    if (!body) return;
    if (!servers.length) {
      body.innerHTML = `<tr><td colspan="6" class="hint">No MCP servers configured. Restart Glider with dashboard enabled.</td></tr>`;
      return;
    }
    body.innerHTML = servers.map((s) => {
      const health = s.connected && s.health_ok
        ? `<span class="live-value ok">connected</span>`
        : `<span class="live-value bad" title="${escapeAttr(s.health_error || "")}">disconnected</span>`;
      const tok = s.token_configured ? "yes" : (s.token_env ? "no" : "â€”");
      const active = s.id === mcpSelectedServerId ? " mcp-row-active" : "";
      return `<tr class="mcp-server-row${active}" data-mcp-id="${escapeAttr(s.id)}">
        <td><code>${escapeHtml(s.id)}</code><div class="graph-hint">${escapeHtml(s.name || "")}</div></td>
        <td>${escapeHtml(s.transport || "â€”")}</td>
        <td>${health}</td>
        <td>${s.tool_count != null ? s.tool_count : "â€”"}</td>
        <td>${tok}</td>
        <td class="mcp-actions">
          <button type="button" class="linkish" data-mcp-act="tools" data-id="${escapeAttr(s.id)}">Tools</button>
          ${s.connected
            ? `<button type="button" class="linkish" data-mcp-act="disconnect" data-id="${escapeAttr(s.id)}">Disconnect</button>
               <button type="button" class="linkish" data-mcp-act="reconnect" data-id="${escapeAttr(s.id)}">Reconnect</button>`
            : `<button type="button" class="linkish" data-mcp-act="connect" data-id="${escapeAttr(s.id)}">Connect</button>`}
          <button type="button" class="linkish" data-mcp-act="refresh" data-id="${escapeAttr(s.id)}">Refresh</button>
        </td>
      </tr>`;
    }).join("");
  }

  async function loadMCPToolsCatalog(serverId) {
    mcpSelectedServerId = serverId;
    const label = document.getElementById("mcp-tools-server-label");
    if (label) label.textContent = serverId ? `(${serverId})` : "";
    const list = document.getElementById("mcp-tools-list");
    if (!list) return;
    list.innerHTML = "<p class=\"hint\">Loadingâ€¦</p>";
    document.querySelectorAll(".mcp-server-row").forEach((tr) => {
      tr.classList.toggle("mcp-row-active", tr.getAttribute("data-mcp-id") === serverId);
    });
    try {
      const res = await fetch(`/api/mcp/servers/${encodeURIComponent(serverId)}/tools`);
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const tools = Array.isArray(data.tools) ? data.tools : [];
      const src = data.source || "";
      const hint = document.getElementById("mcp-tools-hint");
      if (hint) {
        if (data.message) {
          hint.textContent = data.message;
        } else if (src === "live") {
          hint.textContent = "Live tools from connected MCP session.";
        } else if (src === "catalog") {
          hint.textContent = "Documented catalog only â€” Glider is running, but this MCP session is not connected. Sign in / Connect for a live list.";
        } else {
          hint.textContent = "Tools";
        }
        hint.classList.toggle("cfg-warn", src === "catalog");
      }
      if (!tools.length) {
        list.innerHTML = "<p class=\"hint\">No tools.</p>";
        return;
      }
      const badge = src === "live"
        ? `<span class="live-value ok">live</span>`
        : `<span class="live-value bad">catalog</span>`;
      list.innerHTML = `<div class="hint" style="margin-bottom:8px">Source: ${badge}</div>` + tools.map((t) => `
        <div class="mcp-tool-row">
          <code>${escapeHtml(t.name)}</code>
          <span class="hint">${escapeHtml(t.description || "")}</span>
        </div>`).join("");
    } catch (err) {
      list.innerHTML = `<p class="cfg-error">${escapeHtml(String(err.message || err))}</p>`;
    }
  }

  let mcpDevicePollTimer = null;

  async function mcpGitHubPastePAT() {
    const token = await gliderPrompt(
      "Paste GitHub PAT",
      "Stored in ~/.glider/credentials/github_token (0600). Never put tokens in YAML.",
      ""
    );
    if (token == null) return;
    if (!String(token).trim()) {
      showMCPError("Empty token");
      return;
    }
    showMCPError("");
    try {
      const res = await fetch("/api/mcp/github/token", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: String(token).trim() }),
      });
      if (!res.ok) throw new Error(await res.text());
      showMCPOk("GitHub token saved and HTTP MCP connect attempted");
      await refreshMCPPanel();
      await loadMCPToolsCatalog("github");
    } catch (err) {
      showMCPError(String(err.message || err));
      await refreshMCPPanel();
    }
  }

  async function mcpGitHubForgetToken() {
    const ok = await gliderConfirm("Forget GitHub token", "Remove saved GitHub token and disconnect MCP sessions?");
    if (!ok) return;
    try {
      const res = await fetch("/api/mcp/github/token", { method: "DELETE" });
      if (!res.ok) throw new Error(await res.text());
      showMCPOk("GitHub token cleared");
      await refreshMCPPanel();
    } catch (err) {
      showMCPError(String(err.message || err));
    }
  }

  async function mcpGitHubDeviceStart() {
    // Prefer browser OAuth (classic OAuth App + client secret). Fall back to device flow.
    showMCPError("");
    const panel = document.getElementById("mcp-github-device-panel");
    try {
      const oauthRes = await fetch("/api/mcp/github/oauth/start", { method: "POST" });
      if (oauthRes.ok) {
        const data = await oauthRes.json();
        if (data.authorize_url) {
          if (panel) {
            panel.hidden = false;
            panel.innerHTML = `<span class="hint">Opening GitHub authorizationâ€¦</span>`;
          }
          window.location.href = data.authorize_url;
          return;
        }
      } else {
        const oauthErr = await oauthRes.text();
        // If secret missing, try device flow next; otherwise show oauth error.
        if (!/CLIENT_SECRET|client secret/i.test(oauthErr)) {
          // still try device as fallback
        }
      }
    } catch (_) {}

    try {
      const res = await fetch("/api/mcp/github/device/start", { method: "POST" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      if (panel) {
        panel.hidden = false;
        const link = data.verification_uri_complete || data.verification_uri;
        panel.innerHTML = `
          <strong>Authorize GitHub (device flow)</strong><br/>
          Code: <code style="font-size:1.2rem;letter-spacing:0.08em">${escapeHtml(data.user_code || "")}</code><br/>
          <a href="${escapeAttr(link)}" target="_blank" rel="noopener">Open ${escapeHtml(data.verification_uri || "GitHub")}</a>
          <span class="hint"> â€” waiting for authorizationâ€¦</span>`;
      }
      if (mcpDevicePollTimer) clearInterval(mcpDevicePollTimer);
      const intervalMs = Math.max(5, Number(data.interval) || 5) * 1000;
      mcpDevicePollTimer = setInterval(() => mcpGitHubDevicePoll(data.device_code, intervalMs), intervalMs);
      if (data.verification_uri_complete || data.verification_uri) {
        window.open(data.verification_uri_complete || data.verification_uri, "_blank", "noopener");
      }
    } catch (err) {
      showMCPError(String(err.message || err));
      if (panel) {
        panel.hidden = false;
        panel.innerHTML = `<span class="cfg-error">${escapeHtml(String(err.message || err))}</span>
          <span class="hint"> For a classic OAuth App: set <code>GLIDER_GITHUB_OAUTH_CLIENT_SECRET</code> in <code>.env.local</code>,
          callback <code>http://127.0.0.1:8081/oauth/callback</code>, rebuild Glider, then Sign in again.
          Or enable Device Flow on the app, or use Paste PAT.</span>`;
      }
    }
  }

  async function mcpGitHubDevicePoll(deviceCode) {
    try {
      const res = await fetch("/api/mcp/github/device/poll", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ device_code: deviceCode }),
      });
      const text = await res.text();
      let data = {};
      try { data = JSON.parse(text); } catch (_) { throw new Error(text || res.statusText); }
      if (!res.ok) throw new Error(data.error || text);
      const panel = document.getElementById("mcp-github-device-panel");
      if (data.status === "authorized") {
        if (mcpDevicePollTimer) clearInterval(mcpDevicePollTimer);
        mcpDevicePollTimer = null;
        if (panel) {
          panel.hidden = false;
          panel.innerHTML = `<span class="live-value ok">Authorized</span> â€” GitHub MCP connect ${data.http_connected || data.connected ? "ok" : "attempted"}.`;
        }
        showMCPOk("GitHub device login complete");
        await refreshMCPPanel();
        await loadMCPToolsCatalog("github");
        return;
      }
      if (data.status === "expired" || data.status === "error") {
        if (mcpDevicePollTimer) clearInterval(mcpDevicePollTimer);
        mcpDevicePollTimer = null;
        showMCPError(data.error_description || data.error || data.status);
      }
      // pending / slow_down: keep polling
    } catch (err) {
      if (mcpDevicePollTimer) clearInterval(mcpDevicePollTimer);
      mcpDevicePollTimer = null;
      showMCPError(String(err.message || err));
    }
  }

  document.getElementById("mcp-github-card")?.addEventListener("click", (ev) => {
    const btn = ev.target.closest("[data-mcp-gh]");
    if (!btn) return;
    const act = btn.getAttribute("data-mcp-gh");
    if (act === "signin") mcpGitHubDeviceStart();
    else if (act === "pat") mcpGitHubPastePAT();
    else if (act === "forget") mcpGitHubForgetToken();
  });

  async function mcpServerAction(id, act) {
    showMCPError("");
    try {
      const res = await fetch(`/api/mcp/servers/${encodeURIComponent(id)}/${act}`, { method: "POST" });
      if (!res.ok) throw new Error(await res.text());
      showMCPOk(`${act} ${id}`);
      await refreshMCPPanel();
      if (act === "tools" || act === "connect" || act === "reconnect" || act === "refresh") {
        await loadMCPToolsCatalog(id);
      }
    } catch (err) {
      showMCPError(String(err.message || err));
    }
  }

  document.getElementById("mcp-refresh")?.addEventListener("click", () => refreshMCPPanel());
  document.getElementById("mcp-servers-body")?.addEventListener("click", (ev) => {
    const btn = ev.target.closest("[data-mcp-act]");
    if (btn) {
      const id = btn.getAttribute("data-id");
      const act = btn.getAttribute("data-mcp-act");
      if (act === "tools") {
        loadMCPToolsCatalog(id);
        return;
      }
      mcpServerAction(id, act);
      return;
    }
    const row = ev.target.closest(".mcp-server-row");
    if (row) loadMCPToolsCatalog(row.getAttribute("data-mcp-id"));
  });

  function escapeHtml(s) {
    return String(s ?? "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function escapeAttr(s) {
    return escapeHtml(s).replace(/'/g, "&#39;");
  }

  // The hot-swap module list is on the Rules Engine page, so its messages go
  // to that page's own error and ok elements. These two helpers used
  // #hoops-error and #hoops-ok before. Those elements went away with the
  // removed panels, and the toggle then gave no message at all.
  function showRulesError(msg) {
    const e = document.getElementById("rules-error");
    const o = document.getElementById("rules-ok");
    if (o) o.hidden = true;
    if (!e) return;
    if (!msg) { e.hidden = true; e.textContent = ""; return; }
    e.hidden = false;
    e.textContent = msg;
  }

  function showRulesOk(msg) {
    const e = document.getElementById("rules-error");
    const o = document.getElementById("rules-ok");
    if (e) e.hidden = true;
    if (!o) return;
    o.hidden = false;
    o.textContent = msg || "OK";
    setTimeout(() => { o.hidden = true; }, 2500);
  }

  async function loadHotSwap() {
    const el = document.getElementById("hotswap-list");
    if (!el) return;
    try {
      const res = await fetch("/api/hotswap/modules");
      const data = await res.json();
      let mods = data.modules?.length ? data.modules : (data.catalog || []);
      mods = [...mods].sort((a, b) => {
        const as = a.stage ? 0 : 1;
        const bs = b.stage ? 0 : 1;
        if (as !== bs) return as - bs;
        return String(a.name).localeCompare(String(b.name));
      });
      const snap = JSON.stringify(mods.map((m) => [m.name, m.enabled, m.generation, m.last_ok, m.last_error]));
      if (snap === lastHotswapSnap && el.querySelector(".hotswap-row, .hint, .cfg-error")) return;
      lastHotswapSnap = snap;
      el.innerHTML = mods.map((m) => {
        const en = m.enabled !== false;
        const gen = Number(m.generation) || 0;
        const prev = hotswapGenCache[m.name];
        const bumped = prev != null && gen > prev;
        hotswapGenCache[m.name] = gen;
        let statusTag = "";
        if (m.last_ok === true) statusTag = '<span class="tag ok" title="' + esc((m.last_warnings || []).join(" | ") || "last reload ok") + '">ok</span>';
        else if (m.last_ok === false) statusTag = '<span class="tag fail" title="' + esc(m.last_error || "last reload failed") + '">fail</span>';
        return '<div class="hotswap-row ' + (m.stage ? "stage" : "") + (bumped ? " gen-bump" : "") + '">' +
          '<span class="hotswap-name">' + esc(m.name) + '</span>' +
          '<span class="tag">' + esc(m.kind || "") + '</span>' +
          '<span class="tag">' + esc(m.reload || (m.hot ? "hot" : "restart")) + '</span>' +
          statusTag +
          '<span class="hotswap-gen ' + (bumped ? "live" : "") + '" title="' + esc(m.description || "") + '">gen ' + gen + '</span>' +
          '<label class="check" title="' + esc(m.description || (m.hot ? "Hot-toggle this module without restart" : "Requires restart; toggle is informational")) + '"><input type="checkbox" data-mod="' + esc(m.name) + '" ' + (en ? "checked" : "") + " " + (m.hot ? "" : "disabled") + ' title="' + esc(m.description || "Enable or disable this module") + '" /> enabled</label>' +
          '</div>';
      }).join("") || '<p class="hint">No modules registered.</p>';
      el.querySelectorAll("input[data-mod]").forEach((inp) => {
        inp.addEventListener("change", async () => {
          const res = await fetch("/api/hotswap/modules/" + encodeURIComponent(inp.dataset.mod), {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ enabled: inp.checked }),
          });
          if (!res.ok) showRulesError(await res.text());
          else {
            showRulesOk(inp.dataset.mod + " " + (inp.checked ? "enabled" : "disabled"));
            lastHotswapSnap = "";
            await loadHotSwap();
          }
        });
      });
    } catch (e) {
      el.innerHTML = '<p class="cfg-error">' + esc(String(e)) + '</p>';
    }
  }

  // Quick log-level change from config still goes through full save; also listen for select blur optional -- covered by Save.

  // ── Workspace browser ─────────────────────────────────────────────────────
  let wsSelectedFile = "";

  function showWsError(msg) {
    const el = document.getElementById("ws-error");
    if (!el) return;
    if (!msg) {
      el.hidden = true;
      el.textContent = "";
      return;
    }
    el.hidden = false;
    el.textContent = msg;
  }

  function joinWsPath(base, name) {
    const b = String(base || "").replace(/\/+$/, "");
    const n = String(name || "").replace(/^\/+/, "");
    if (!b || b === ".") return n;
    if (!n) return b;
    return b + "/" + n;
  }

  function parentWsPath(p) {
    const parts = String(p || "").replace(/\/+$/, "").split("/").filter(Boolean);
    if (parts.length <= 1) return "runs";
    parts.pop();
    return parts.join("/") || "runs";
  }

  async function refreshWorkspacePanel() {
    showWsError("");
    const runEl = document.getElementById("ws-run");
    if (runEl && runEl.value.trim()) {
      await loadWorkspaceRun();
      return;
    }
    await loadWorkspaceList();
  }

  async function loadWorkspaceList() {
    const pathEl = document.getElementById("ws-path");
    const recEl = document.getElementById("ws-recursive");
    const listEl = document.getElementById("ws-file-list");
    const metaEl = document.getElementById("ws-files-meta");
    const rootEl = document.getElementById("ws-root-label");
    if (!listEl) return;
    const path = (pathEl && pathEl.value.trim()) || "runs";
    const recursive = !!(recEl && recEl.checked);
    const q = new URLSearchParams({ path, recursive: recursive ? "1" : "0", limit: "400" });
    try {
      const res = await fetch("/api/workspace?" + q.toString());
      const text = await res.text();
      if (!res.ok) throw new Error(text || res.statusText);
      const data = JSON.parse(text);
      if (rootEl) rootEl.textContent = data.workspace || "";
      if (metaEl) metaEl.textContent = data.path || path;
      const files = Array.isArray(data.files) ? data.files : [];
      renderWorkspaceFiles(files, data.path || path, !recursive);
    } catch (err) {
      showWsError(err.message || String(err));
      listEl.innerHTML = `<li class="hint">Failed to list</li>`;
    }
  }

  async function loadWorkspaceRun() {
    const runEl = document.getElementById("ws-run");
    const listEl = document.getElementById("ws-file-list");
    const metaEl = document.getElementById("ws-files-meta");
    const rootEl = document.getElementById("ws-root-label");
    const pathEl = document.getElementById("ws-path");
    if (!listEl || !runEl) return;
    const run = runEl.value.trim();
    if (!run) {
      showWsError("Enter a run id");
      return;
    }
    showWsError("");
    try {
      const res = await fetch("/api/workspace?run=" + encodeURIComponent(run) + "&limit=400");
      const text = await res.text();
      if (!res.ok) throw new Error(text || res.statusText);
      const data = JSON.parse(text);
      if (rootEl) rootEl.textContent = data.workspace || "";
      if (metaEl) metaEl.textContent = "run " + (data.run || run);
      if (pathEl && data.work_dir) pathEl.value = data.work_dir.replace(/\/work$/, "") || "runs/" + (data.run || run);
      const work = (Array.isArray(data.work) ? data.work : []).map((f) => ({ path: f, label: f, kind: "work" }));
      const out = (Array.isArray(data.out) ? data.out : []).map((f) => ({ path: f, label: f, kind: "out" }));
      const rows = work.concat(out);
      if (!rows.length) {
        listEl.innerHTML = `<li class="hint">No files under work/ or out/ for this run</li>`;
        return;
      }
      listEl.innerHTML = rows.map((r) =>
        `<li><button type="button" class="ws-file-btn" data-ws-file="${esc(r.path)}">` +
        `<span class="ws-file-kind">${esc(r.kind)}</span> ${esc(r.label)}</button></li>`
      ).join("");
      listEl.querySelectorAll("[data-ws-file]").forEach((btn) => {
        btn.addEventListener("click", () => openWorkspaceFile(btn.getAttribute("data-ws-file")));
      });
    } catch (err) {
      showWsError(err.message || String(err));
      listEl.innerHTML = `<li class="hint">Failed to load run</li>`;
    }
  }

  function renderWorkspaceFiles(files, basePath, dirsNavigable) {
    const listEl = document.getElementById("ws-file-list");
    if (!listEl) return;
    const items = [];
    if (basePath && basePath !== "runs" && basePath !== ".") {
      items.push(`<li><button type="button" class="ws-file-btn ws-up" data-ws-dir="${esc(parentWsPath(basePath))}">../</button></li>`);
    }
    if (!files.length) {
      items.push(`<li class="hint">Empty</li>`);
    }
    files.forEach((name) => {
      const isDir = String(name).endsWith("/");
      const clean = String(name).replace(/\/$/, "");
      const full = joinWsPath(basePath, clean);
      if (isDir && dirsNavigable) {
        items.push(
          `<li><button type="button" class="ws-file-btn ws-dir" data-ws-dir="${esc(full)}">${esc(name)}</button></li>`
        );
      } else if (isDir) {
        items.push(`<li class="hint">${esc(name)}</li>`);
      } else {
        const filePath = dirsNavigable ? full : String(name);
        items.push(
          `<li><button type="button" class="ws-file-btn" data-ws-file="${esc(filePath)}">${esc(name)}</button></li>`
        );
      }
    });
    listEl.innerHTML = items.join("");
    listEl.querySelectorAll("[data-ws-dir]").forEach((btn) => {
      btn.addEventListener("click", () => {
        const pathEl = document.getElementById("ws-path");
        if (pathEl) pathEl.value = btn.getAttribute("data-ws-dir") || "runs";
        const runEl = document.getElementById("ws-run");
        if (runEl) runEl.value = "";
        loadWorkspaceList();
      });
    });
    listEl.querySelectorAll("[data-ws-file]").forEach((btn) => {
      btn.addEventListener("click", () => openWorkspaceFile(btn.getAttribute("data-ws-file")));
    });
  }

  async function openWorkspaceFile(path) {
    if (!path) return;
    wsSelectedFile = path;
    showWsError("");
    const preview = document.getElementById("ws-preview");
    const pathLabel = document.getElementById("ws-preview-path");
    const diffA = document.getElementById("ws-diff-a");
    if (pathLabel) pathLabel.textContent = path;
    if (diffA && !diffA.value.trim()) diffA.value = path;
    if (preview) preview.textContent = "Loading…";
    try {
      const res = await fetch("/api/workspace?file=" + encodeURIComponent(path));
      const text = await res.text();
      if (!res.ok) throw new Error(text || res.statusText);
      const data = JSON.parse(text);
      let body = data.content || "";
      if (data.binary) body = "(binary file — preview skipped)\n" + body.slice(0, 200);
      if (data.truncated) body += "\n\n… truncated (" + (data.size || "?") + " bytes)";
      if (preview) preview.textContent = body || "(empty)";
    } catch (err) {
      showWsError(err.message || String(err));
      if (preview) preview.textContent = "Failed to load file";
    }
  }

  async function runWorkspaceFileDiff() {
    const a = document.getElementById("ws-diff-a")?.value.trim();
    const b = document.getElementById("ws-diff-b")?.value.trim();
    const out = document.getElementById("ws-diff");
    if (!a || !b) {
      showWsError("Diff needs both A and B paths");
      return;
    }
    showWsError("");
    if (out) out.textContent = "Diffing…";
    try {
      const q = new URLSearchParams({ diff: "1", a, b });
      const res = await fetch("/api/workspace?" + q.toString());
      const text = await res.text();
      if (!res.ok) throw new Error(text || res.statusText);
      const data = JSON.parse(text);
      if (out) out.textContent = data.diff || "(empty diff)";
    } catch (err) {
      showWsError(err.message || String(err));
      if (out) out.textContent = "Diff failed";
    }
  }

  async function runWorkspaceGitDiff() {
    const path = wsSelectedFile || document.getElementById("ws-diff-a")?.value.trim() ||
      document.getElementById("ws-path")?.value.trim();
    const out = document.getElementById("ws-diff");
    if (!path) {
      showWsError("Select a file/folder under a git clone first");
      return;
    }
    showWsError("");
    if (out) out.textContent = "git diff…";
    try {
      const q = new URLSearchParams({ diff: "1", path });
      const res = await fetch("/api/workspace?" + q.toString());
      const text = await res.text();
      if (!res.ok) throw new Error(text || res.statusText);
      const data = JSON.parse(text);
      const head = data.repo ? "repo: " + data.repo + "\n\n" : "";
      if (out) out.textContent = head + (data.diff || "(empty)");
    } catch (err) {
      showWsError(err.message || String(err));
      if (out) out.textContent = "Git diff failed";
    }
  }

  document.getElementById("ws-refresh")?.addEventListener("click", () => refreshWorkspacePanel());
  document.getElementById("ws-load")?.addEventListener("click", () => {
    const runEl = document.getElementById("ws-run");
    if (runEl) runEl.value = "";
    loadWorkspaceList();
  });
  document.getElementById("ws-load-run")?.addEventListener("click", () => loadWorkspaceRun());
  document.getElementById("ws-diff-files")?.addEventListener("click", () => runWorkspaceFileDiff());
  document.getElementById("ws-diff-git")?.addEventListener("click", () => runWorkspaceGitDiff());
  document.getElementById("ws-path")?.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") {
      ev.preventDefault();
      document.getElementById("ws-run").value = "";
      loadWorkspaceList();
    }
  });
  document.getElementById("ws-run")?.addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") {
      ev.preventDefault();
      loadWorkspaceRun();
    }
  });

  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(proto + "://" + location.host + "/ws");
  setLiveWs("Connecting...", null);
  ws.onopen = () => {
    liveWsConnected = true;
    setLiveWs("Connected", true);
  };
  ws.onclose = () => {
    liveWsConnected = false;
    setLiveWs("Disconnected", false);
  };
  ws.onerror = () => {
    liveWsConnected = false;
    setLiveWs("Disconnected", false);
  };
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.type === "request") {
        if (liveMode) {
          addLog(msg.data || {}, { live: true, prepend: true });
          refreshMetricsSnapshot();
        }
      }
      if (msg.type === "vram_update") {
        const g = document.getElementById("gpu-gauges");
        if (g && panels.vram.classList.contains("active") && msg.data) {
          const usedPct = msg.data.total ? Math.round((msg.data.used / msg.data.total) * 100) : 0;
          const first = g.querySelector(".gauge-used");
          if (first) first.style.width = usedPct + "%";
        }
      }
    } catch (_) {}
  };

  // ---- Rules Engine: config health (lint) + explain dry-run --------
  // Predictability/debuggability tools for the routing rule chain
  // itself — distinct from the Playground tab, which teaches chat-typed
  // *commands*, not the implicit rule evaluation every request goes
  // through. Both call real backend logic (router.LintConfig,
  // router.Engine.RouteExplain) against the operator's own live config;
  // neither ever issues a real completion.

  async function loadRulesLint() {
    const container = document.getElementById("rules-lint-list");
    if (!container) return;
    try {
      const res = await fetch("/api/router/lint");
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      const findings = Array.isArray(data.findings) ? data.findings : [];
      if (!findings.length) {
        container.innerHTML = `<p class="hint">No ambiguity found in your saved rules.</p>`;
        return;
      }
      container.innerHTML = findings.map((f) => `
        <div class="lint-finding">
          <p style="margin:0">${escapeHtml(f.message)}</p>
          ${f.example ? `<button type="button" class="linkish" data-lint-example="${escapeAttr(f.example)}">Show me in Explain →</button>` : ""}
        </div>`).join("");
    } catch (err) {
      container.innerHTML = `<p class="cfg-error">${escapeHtml(String(err.message || err))}</p>`;
    }
  }

  function explainStatusClass(e) {
    if (e.err) return "explain-err";
    if (!e.matched) return "explain-nomatch";
    return e.shadowed ? "explain-shadowed" : "explain-matched";
  }

  function explainStatusLabel(e) {
    if (e.err) return "error";
    if (!e.matched) return "no match";
    return e.shadowed ? "matched — shadowed" : "matched — winner";
  }

  function renderExplainTrace(resp) {
    const container = document.getElementById("rules-explain-result");
    if (!container) return;

    const skippedNote = (resp.skippedRules && resp.skippedRules.length)
      ? `<p class="cfg-warn" style="display:block">Not evaluated: ${resp.skippedRules.map(escapeHtml).join("; ")}</p>`
      : "";

    const winner = resp.decision
      ? `<p class="cfg-ok" style="display:block">Winner: <strong>${escapeHtml(resp.decision.ruleName || "")}</strong> → ${escapeHtml(resp.decision.target || "")}${resp.decision.model ? " · " + escapeHtml(resp.decision.model) : ""}${resp.decision.reason ? ` (${escapeHtml(resp.decision.reason)})` : ""}</p>`
      : `<p class="hint">No rule matched — a real request here would fail with "no routing rule matched."</p>`;

    const rows = (resp.entries || []).map((e) => {
      let detail = "";
      if (e.err) {
        detail = `<span class="cfg-error" style="display:inline">${escapeHtml(e.err)}</span>`;
      } else if (e.decision) {
        const bits = [e.decision.target, e.decision.backendName, e.decision.model];
        if (e.decision.reason) bits.push(`reason: ${e.decision.reason}`);
        detail = escapeHtml(bits.filter(Boolean).join(" · "));
      }
      return `<div class="explain-row ${explainStatusClass(e)}">
        <div class="explain-row-head">
          <span class="explain-rule-name">${escapeHtml(e.ruleName)}</span>
          <span class="explain-kind">${escapeHtml(e.kind)}</span>
          <span class="explain-priority">priority ${e.priority}</span>
          <span class="explain-status">${escapeHtml(explainStatusLabel(e))}</span>
        </div>
        ${detail ? `<div class="explain-row-detail">${detail}</div>` : ""}
      </div>`;
    }).join("");

    container.innerHTML = skippedNote + winner + `<div class="explain-rows">${rows}</div>`;
  }

  async function runRulesExplain() {
    const input = document.getElementById("rules-explain-input");
    const container = document.getElementById("rules-explain-result");
    if (!input || !container) return;
    const text = input.value;
    if (!text.trim()) {
      container.innerHTML = `<p class="hint">Type a message above and click Explain.</p>`;
      return;
    }
    const toolsInput = document.getElementById("rules-explain-tools");
    const tokensInput = document.getElementById("rules-explain-tokens");
    const tools = (toolsInput?.value || "").split(",").map((s) => s.trim()).filter(Boolean);
    const estimatedTokens = parseInt(tokensInput?.value || "0", 10) || 0;
    container.innerHTML = `<p class="hint">Running…</p>`;
    try {
      const res = await fetch("/api/router/explain", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text, tools, estimatedTokens }),
      });
      if (!res.ok) throw new Error(await res.text());
      renderExplainTrace(await res.json());
    } catch (err) {
      container.innerHTML = `<p class="cfg-error">${escapeHtml(String(err.message || err))}</p>`;
    }
  }

  document.getElementById("rules-explain-run")?.addEventListener("click", runRulesExplain);
  document.getElementById("rules-lint-list")?.addEventListener("click", (ev) => {
    const btn = ev.target.closest("[data-lint-example]");
    if (!btn) return;
    const input = document.getElementById("rules-explain-input");
    if (!input) return;
    input.value = btn.getAttribute("data-lint-example");
    runRulesExplain();
    input.scrollIntoView({ behavior: "smooth", block: "center" });
  });

  // ---- Playground -------------------------------------------------
  // Teaches Glider's chat-typed command syntax by classifying whatever
  // the user types through the REAL Go parsers (POST /api/playground/parse)
  // instead of a JS reimplementation, so what's shown here can never
  // drift out of sync with what Glider actually does. Purely read-only
  // server-side: no CLI run, no permission grant, no workspace write —
  // safe to try anything. Lesson progress is local-only (localStorage),
  // not graded or synced anywhere.

  let playgroundInited = false;
  let playgroundInfo = { vendors: [], routingCommands: [], scriptRules: [] };
  let playgroundDebounceTimer = null;
  const PLAYGROUND_PROGRESS_KEY = "glider-playground-progress";

  function playgroundVendorExample() {
    // The server sorts headless-default vendors first, so [0] is a name whose
    // example returns an answer instead of opening a window. The placeholder
    // is deliberately not a real CLI name: with no vendor discovered yet, a
    // real name would look like a working command.
    return playgroundInfo.vendors[0] || "<cli>";
  }

  const PLAYGROUND_LESSONS = [
    {
      id: "delegate-run",
      title: "1. Send a task to another CLI",
      goal: "Send a task to a different CLI. The flag must be the last item in your message. If you put \"/name\" at the start, your own CLI reads it as a local command, and Glider does not receive it.",
      hint: () => `<your prompt> /${playgroundVendorExample()}`,
      example: () => `summarize recent commits /${playgroundVendorExample()}`,
      check: (r) => r.delegate.matched && r.delegate.kind === "run",
    },
    {
      id: "delegate-template",
      title: "2. Select a template",
      goal: "A CLI can have more than one start method. For example, it can have an interactive method. Write \":template-name\" immediately after the vendor name.",
      hint: () => `<your prompt> /${playgroundVendorExample()}:interactive`,
      example: () => `fix the auth bug /${playgroundVendorExample()}:interactive`,
      check: (r) => r.delegate.matched && r.delegate.template && r.delegate.template !== "default",
    },
    {
      id: "workspace",
      title: "3. Set your workspace",
      goal: "A delegate CLI must know which folder to use. The \"/workspace\" flag applies to each vendor. It is a property of your session.",
      hint: () => `<path> /workspace`,
      example: () => `. /workspace`,
      check: (r) => r.workspace.matched,
    },
    {
      id: "permission",
      title: "4. Answer a permission question",
      goal: "When a delegate CLI needs your permission, Glider gives you a short token in the reply. Send that token again with \":allow\" or with \":deny\".",
      hint: () => `<token> /${playgroundVendorExample()}:allow`,
      example: () => `abc123 /${playgroundVendorExample()}:allow`,
      check: (r) => r.delegate.matched && (r.delegate.kind === "allow" || r.delegate.kind === "deny"),
    },
    {
      id: "routing",
      title: "5. Select the local model or the cloud",
      goal: "This is a different mechanism from the delegate flags above. Glider finds this command at any position in the message, not only at the end. You set the words on the Rules Engine page, and they are not fixed.",
      hint: () => (playgroundInfo.routingCommands.length
        ? `${playgroundInfo.routingCommands[0]} <your message>`
        : "(There is no routing command yet. Add one on the Rules Engine page, or continue to the next lesson.)"),
      example: () => (playgroundInfo.routingCommands.length ? `${playgroundInfo.routingCommands[0]} keep this on-device` : ""),
      check: (r) => r.routing.matched,
    },
  ];

  const PLAYGROUND_REFERENCE = [
    {
      family: "Delegate",
      syntax: "<prompt> /vendor[:template]",
      body: "Sends the prompt to a different CLI and returns the answer to this chat. With an \"interactive\" template, Glider opens that CLI in its own window instead. The flag must be the last item in the message. If you put \"/vendor\" at the start, your own CLI reads it as a local command.",
    },
    {
      family: "Workspace",
      syntax: "<path> /workspace",
      body: "Tells Glider which folder a delegate CLI must use in this session. The flag applies to each vendor, and Glider uses it for each delegate call from this session.",
    },
    {
      family: "Permission allow or deny",
      syntax: "<token> /vendor:allow   or   <token> /vendor:deny",
      body: "Answers a permission question from a delegate CLI. The token identifies the run and the vendor. Glider gives you the token in the message, and you must not write your own token.",
    },
    {
      family: "Routing command",
      syntax: "/local, /fast, /cloud, /heavy (you can change these words)",
      body: "Selects the model for this turn. Glider finds this command at any position in the message, not only at the end. This is a different mechanism from the three above, because it controls the route and not the delegation. Set the words on the Rules Engine page.",
    },
    {
      family: "Script rules",
      syntax: "the text that a .star rule looks for",
      body: "A rule with a script trigger can look for any text that your Starlark code examines. For example, \"/swarm\". The text is not fixed, and this page cannot test it. Open the Rules Engine page to see your rules.",
    },
  ];

  function playgroundProgress() {
    try {
      return JSON.parse(localStorage.getItem(PLAYGROUND_PROGRESS_KEY) || "[]");
    } catch (_) {
      return [];
    }
  }

  function markPlaygroundLessonDone(id) {
    const done = new Set(playgroundProgress());
    if (done.has(id)) return;
    done.add(id);
    localStorage.setItem(PLAYGROUND_PROGRESS_KEY, JSON.stringify([...done]));
    renderPlaygroundLessons();
  }

  function checkPlaygroundLessons(result) {
    PLAYGROUND_LESSONS.forEach((lesson) => {
      if (lesson.check(result)) markPlaygroundLessonDone(lesson.id);
    });
  }

  function renderPlaygroundLessons() {
    const container = document.getElementById("pg-lessons");
    if (!container) return;
    const done = new Set(playgroundProgress());
    const progressLabel = document.getElementById("pg-progress");
    if (progressLabel) progressLabel.textContent = `${done.size}/${PLAYGROUND_LESSONS.length} complete`;
    container.innerHTML = PLAYGROUND_LESSONS.map((lesson) => {
      const complete = done.has(lesson.id);
      const hint = lesson.hint();
      const example = lesson.example();
      return `<div class="pg-lesson${complete ? " complete" : ""}">
        <div class="pg-lesson-head">
          <span class="pg-lesson-check" aria-hidden="true">${complete ? "✓" : ""}</span>
          <span class="pg-lesson-title">${escapeHtml(lesson.title)}</span>
        </div>
        <p class="hint" style="margin:4px 0 8px">${escapeHtml(lesson.goal)}</p>
        <div class="pg-lesson-actions">
          <code class="pg-lesson-hint">${escapeHtml(hint)}</code>
          ${example ? `<button type="button" class="linkish" data-pg-fill="${escapeAttr(example)}">Try this</button>` : ""}
        </div>
      </div>`;
    }).join("");
  }

  function renderPlaygroundReference() {
    const container = document.getElementById("pg-reference");
    if (!container) return;
    container.innerHTML = PLAYGROUND_REFERENCE.map((r) => `
      <div class="pg-ref-card">
        <div class="pg-ref-family">${escapeHtml(r.family)}</div>
        <code class="pg-ref-syntax">${escapeHtml(r.syntax)}</code>
        <p class="hint" style="margin:6px 0 0">${escapeHtml(r.body)}</p>
      </div>`).join("");
  }

  function renderPlaygroundExamples() {
    const container = document.getElementById("pg-examples");
    if (!container) return;
    const vendor = playgroundVendorExample();
    const examples = [
      `summarize recent commits /${vendor}`,
      `. /workspace`,
      `abc123 /${vendor}:allow`,
    ];
    if (playgroundInfo.routingCommands.length) {
      examples.push(`${playgroundInfo.routingCommands[0]} keep this on-device`);
    }
    container.innerHTML = examples.map((ex) =>
      `<button type="button" class="linkish pg-example-chip" data-pg-fill="${escapeAttr(ex)}">${escapeHtml(ex)}</button>`
    ).join("");
  }

  function playgroundBadge(label, cls) {
    return `<span class="pg-badge pg-badge-${cls}">${escapeHtml(label)}</span>`;
  }

  function renderPlaygroundResult(result, text) {
    const container = document.getElementById("pg-result");
    if (!container) return;
    if (!text.trim()) {
      container.innerHTML = `<p class="hint">Type a message above to see what Glider would do with it.</p>`;
      return;
    }

    const rows = [];
    if (result.delegate.matched) {
      const d = result.delegate;
      const descriptions = {
        run: `Delegates to <strong>${escapeHtml(d.vendor)}</strong> — runs headlessly with prompt "${escapeHtml(d.prompt)}", answer relayed back here.`,
        interactive: `Delegates to <strong>${escapeHtml(d.vendor)}</strong> (interactive template "${escapeHtml(d.template)}") — opens a real window with "${escapeHtml(d.prompt)}" seeded in; nothing relayed back.`,
        allow: `Grants the pending permission for token <code>${escapeHtml(d.prompt)}</code>, then resumes <strong>${escapeHtml(d.vendor)}</strong>.`,
        deny: `Denies the pending permission for token <code>${escapeHtml(d.prompt)}</code> — the run is abandoned.`,
        unknown_template: `<strong>${escapeHtml(d.vendor)}</strong> has no ":${escapeHtml(d.template)}" template configured — this comes back as an error message, not a real run.`,
      };
      rows.push(`<div class="pg-result-row">${playgroundBadge("Delegate", "delegate")} ${descriptions[d.kind] || ""}</div>`);
    }
    if (result.workspace.matched) {
      rows.push(`<div class="pg-result-row">${playgroundBadge("Workspace", "workspace")} Sets this session's working directory to <code>${escapeHtml(result.workspace.path)}</code>.</div>`);
    }
    if (result.routing.matched) {
      const r = result.routing;
      const ruleNote = r.ruleName ? ` (rule "${escapeHtml(r.ruleName)}"${r.target ? `, forces target "${escapeHtml(r.target)}"` : ""})` : "";
      rows.push(`<div class="pg-result-row">${playgroundBadge("Routing override", "routing")} Matched <code>${escapeHtml(r.command)}</code>${ruleNote}. Rest of the message: "${escapeHtml(r.remainder)}".</div>`);
    }
    if (!rows.length) {
      let note = "No command recognized — this would be sent through as an ordinary chat message.";
      if (result.scriptRules && result.scriptRules.length) {
        note += ` (Your Rules Engine also has ${result.scriptRules.length} script-triggered rule(s) — e.g. "${escapeHtml(result.scriptRules[0])}" — that could still match on phrases this playground can't check.)`;
      }
      rows.push(`<div class="pg-result-row pg-result-empty">${note}</div>`);
    }
    container.innerHTML = rows.join("");
  }

  async function playgroundCheck() {
    const input = document.getElementById("pg-input");
    if (!input) return;
    const text = input.value;
    try {
      const res = await fetch("/api/playground/parse", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text }),
      });
      if (!res.ok) throw new Error(await res.text());
      const result = await res.json();
      playgroundInfo = {
        vendors: Array.isArray(result.vendors) ? result.vendors : [],
        routingCommands: Array.isArray(result.routing && result.routing.configuredCommands) ? result.routing.configuredCommands : [],
        scriptRules: Array.isArray(result.scriptRules) ? result.scriptRules : [],
      };
      renderPlaygroundResult(result, text);
      if (text.trim()) checkPlaygroundLessons(result);
    } catch (err) {
      const container = document.getElementById("pg-result");
      if (container) container.innerHTML = `<p class="cfg-error">${escapeHtml(String(err.message || err))}</p>`;
    }
  }

  function debouncePlaygroundCheck() {
    clearTimeout(playgroundDebounceTimer);
    playgroundDebounceTimer = setTimeout(playgroundCheck, 250);
  }

  async function refreshPlaygroundPanel() {
    renderPlaygroundReference();
    renderPlaygroundLessons();
    if (playgroundInited) return;
    playgroundInited = true;

    const input = document.getElementById("pg-input");
    input?.addEventListener("input", debouncePlaygroundCheck);

    document.getElementById("panel-playground")?.addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-pg-fill]");
      if (!btn || !input) return;
      input.value = btn.getAttribute("data-pg-fill");
      playgroundCheck();
      input.focus();
    });

    // Prime vendor/routing-command context with one harmless empty-text
    // classification before the user has typed anything, then re-render
    // lessons/examples so their hints use real vendor names instead of
    // the "agy" fallback.
    await playgroundCheck();
    renderPlaygroundLessons();
    renderPlaygroundExamples();
  }

  loadConfig();
  loadVRAM();
  loadSessions();
  refreshMetricsSnapshot();
})();
