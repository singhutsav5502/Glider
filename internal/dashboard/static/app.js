(() => {
  const panels = {
    overview: document.getElementById("panel-overview"),
    vram: document.getElementById("panel-vram"),
    rules: document.getElementById("panel-rules"),
    hoops: document.getElementById("panel-hoops"),
    graphs: document.getElementById("panel-graphs"),
    mcp: document.getElementById("panel-mcp"),
    vendors: document.getElementById("panel-vendors"),
    workspace: document.getElementById("panel-workspace"),
    playground: document.getElementById("panel-playground"),
    settings: document.getElementById("panel-settings"),
  };

  let currentCfg = null;
  let viewingSessionId = null;
  let liveMode = true;
  let requests = 0;
  let local = 0;
  let cloud = 0;
  let canned = 0;
  let lastDist = null; // { local_pct, cloud_pct, canned_pct } from API when available
  let tokenTotal = 0;
  let latencySum = 0;
  let gpuAssignmentDraft = {};

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
    "hoop-name": { title: "Hoop name", body: "Display name for this hoop. Used in the manager list and logs." },
    "hoop-interval": {
      title: "Heartbeat",
      body: "Optional delay between hoop cycles. Empty = run without waiting.",
      values: [
        { v: "5m", d: "Five minutes between cycles" },
        { v: "30s", d: "Thirty seconds" },
        { v: "(empty)", d: "No heartbeat wait" },
      ],
    },
    "hoop-prompt": {
      title: "Goal / purpose",
      body: "Primary objective sent to planner/actor each cycle. For clone/audit samples, include repo=<url> or an https:// URL.",
    },
    "hoop-eval-goal": {
      title: "Eval goal (critic)",
      body: "Critic success criteria. Cycles stop when this is met and score thresholds pass.",
    },
    "hoop-route": {
      title: "Inference route",
      body: "Forces how each stage reaches the model via the Glider gateway.",
      values: [
        { v: "local", d: "Prefix /local → Ollama or vLLM only" },
        { v: "cloud", d: "Prefix /cloud → BYOK OpenAI (or other cloud providers in config)" },
        { v: "auto", d: "No prefix → gateway rules decide (default rule is often cloud BYOK)" },
      ],
    },
    "hoop-learning": {
      title: "Self-learning",
      body: "Only affects route=auto. Successful local outcomes bias toward local; cloud successes bias toward cloud.",
    },
    "hoop-max-iter": {
      title: "Max iterations",
      body: "Upper bound on hoop cycles before forced stop. 0 is treated as the UI default (3).",
    },
    "swarm-prompt": { title: "Swarm prompt", body: "Task text given to each worker role (and to decompose/planner when enabled)." },
    "swarm-roles": {
      title: "Swarm roles",
      body: "Comma-separated worker roles linked on the swarm graph. Canonical roles are preferred; free-spawn can invent more when enabled.",
      values: [
        { v: "plan", d: "Planner / decomposition narrative" },
        { v: "exec", d: "Implementer / doer" },
        { v: "research", d: "Lookup / investigate" },
        { v: "worker", d: "Generic short-lived action" },
      ],
    },
    "swarm-workers": {
      title: "Max workers",
      body: "Parallel FanOut concurrency cap.",
      values: [
        { v: "1–4", d: "Hard cap is 4 workers per wave" },
      ],
    },
    "swarm-waves": {
      title: "Waves",
      body: "Sequential FanOut waves. Later waves can read prior weave output.",
      values: [
        { v: "1", d: "Single FanOut" },
        { v: "2–4", d: "Multi-wave weave (durable thread)" },
      ],
    },
    "swarm-route": {
      title: "Swarm inference route",
      body: "Same gateway prefixes as hoop route. Applies to every worker and llm_critic weave.",
      values: [
        { v: "local", d: "Ollama/vLLM via /local" },
        { v: "cloud", d: "BYOK OpenAI (etc.) via /cloud" },
        { v: "auto", d: "Gateway rules (often default cloud)" },
      ],
    },
    "swarm-weave-policy": {
      title: "Weave policy",
      body: "How worker outputs are merged across a wave / multi-wave run.",
      values: [
        { v: "critic", d: "Rank / critique merge (default)" },
        { v: "concatenate", d: "Simple concatenation of summaries" },
        { v: "role_weighted", d: "Weight plan/research slightly over raw exec dumps" },
        { v: "conflict_callouts", d: "Surface disagreements between roles" },
        { v: "llm_critic", d: "Extra LLM critic pass (uses swarm CriticFn + route)" },
      ],
    },
    "swarm-decompose": {
      title: "Decompose planner",
      body: "Ask a planner wave to propose SubTasks / roles before later FanOut waves.",
    },
    "swarm-free-spawn": {
      title: "Free spawn roles",
      body: "Allow planner free-form role labels (bounded ≤4) beyond the fixed role list.",
    },
    "tpl-id": { title: "Template ID", body: "Stable slug used in APIs and on disk." },
    "tpl-name": { title: "Template name", body: "Human-readable template label." },
    "tpl-prompt": { title: "Template prompt", body: "Default prompt when running this template." },
    "tpl-roles": {
      title: "Template roles",
      body: "Default comma-separated roles.",
      values: [
        { v: "plan", d: "Planner" },
        { v: "exec", d: "Implementer" },
        { v: "research", d: "Research" },
        { v: "worker", d: "Generic worker" },
      ],
    },
    "tpl-local": {
      title: "Prefer local",
      body: "When checked, template runs resolve to route=local unless the run overrides route.",
    },
    "stage-edit-kind": {
      title: "Stage kind",
      body: "Loop Engineering stage type.",
      values: [
        { v: "workspace", d: "Bind work/out folders (fresh run or existing path)" },
        { v: "planner", d: "Produce a plan for the cycle" },
        { v: "actor", d: "Implement / execute against the plan" },
        { v: "critic", d: "Score / gate success (maker ≠ checker)" },
        { v: "memory", d: "Load / write shared memory" },
        { v: "context", d: "Seed shared contextgraph for later stages" },
        { v: "router", d: "Update local/cloud bias for following stages" },
        { v: "human_gate", d: "Pause for human approve / reject" },
      ],
    },
    "stage-edit-workspace-mode": {
      title: "Workspace mode",
      body: "run = fresh runs/<hoop>/{work,out}. existing = reuse a folder under ~/.glider/workspace.",
    },
    "stage-edit-workspace-path": {
      title: "Workspace path",
      body: "Required when mode=existing. Relative to the tools sandbox (e.g. projects/demo).",
    },
    "stage-edit-out-path": {
      title: "Out path",
      body: "Optional deliverables folder when mode=existing (default: <workspace_path>/out).",
    },
    "stage-chip-workspace": {
      title: "workspace",
      body: "Bind action (work) + output (out) for this run — fresh or existing.",
    },
    "stage-chip-context": {
      title: "context",
      body: "Seed shared contextgraph keys for later actors / swarm.",
    },
    "stage-edit-id": { title: "Stage id", body: "Stable id used in graph_edges and runtime state (slug-safe)." },
    "stage-edit-name": { title: "Stage name", body: "Display name for this stage." },
    "stage-edit-enabled": { title: "Enabled", body: "When unchecked, this stage is skipped in the cycle." },
    "stage-edit-prompt": { title: "Stage prompt", body: "Instructions for the model at this stage." },
    "stage-edit-route": {
      title: "Stage route",
      body: "Per-stage override of hoop route.",
      values: [
        { v: "(inherit)", d: "Use hoop route" },
        { v: "local", d: "Force Ollama/vLLM" },
        { v: "cloud", d: "Force BYOK cloud" },
        { v: "auto", d: "Gateway rules" },
      ],
    },
    "stage-edit-eval-min": {
      title: "Eval min (critic)",
      body: "Minimum critic score (0–1) required to treat evaluation as success.",
    },
    "stage-edit-mcp": {
      title: "MCP servers",
      body: "Which MCP servers this stage may call. Leave tools empty to allow all tools from selected servers.",
    },
    "stage-edit-tools": {
      title: "Tools JSON",
      body: "Advanced StageSpec.tools JSON (builtins + MCP refs). Prefer the MCP pickers above when possible.",
    },
    "edge-kind-select": {
      title: "Edge kind",
      body: "Graph transition type (state machine), not model routing.",
      values: [
        { v: "flow", d: "Normal forward edge" },
        { v: "feedback", d: "Loop-back / retry edge" },
        { v: "on_fail", d: "Taken when stage fails" },
        { v: "escalate", d: "Escalate path" },
        { v: "conditional", d: "Guard-driven branch" },
        { v: "budget_exceeded", d: "Budget breach path" },
        { v: "parallel", d: "Fan-out marker" },
        { v: "merge", d: "Join after parallel" },
        { v: "feeds", d: "Data seed: producer summary → consumer prompt (not control-flow)" },
      ],
    },
    "glider-prompt-input": { title: "Prompt value", body: "Value entered for the modal prompt dialog." },
    "session-select": {
      title: "Session",
      body: "A session is one Glider process run. Live WebSocket events append to the current session; pick a past run to browse stored logs.",
    },
    "cfg-proxy-port": { title: "Gateway proxy port", body: "OpenAI-compatible /v1 listener. Restart required after change." },
    "cfg-dash-port": { title: "Dashboard port", body: "This UI + REST/WebSocket. Restart required after change." },
    "cfg-log-level": {
      title: "Log level",
      body: "slog level for the Glider process. Applied immediately on save.",
      values: [
        { v: "debug", d: "Verbose" },
        { v: "info", d: "Default" },
        { v: "warn", d: "Warnings+" },
        { v: "error", d: "Errors only" },
      ],
    },
    "cfg-tokens": { title: "Max local context tokens", body: "Context size above which routing prefers cloud/origin. Hot-reloaded." },
    "cfg-idle": { title: "Idle unload timeout", body: "Unload idle local models after this duration (e.g. 5m)." },
    "cfg-req-timeout": { title: "Request timeout", body: "Per-request timeout for backend completions (e.g. 120s)." },
    "cfg-mitm-enabled": { title: "MITM enabled", body: "Enable the MITM proxy listener. Restart required." },
    "cfg-mitm-port": { title: "MITM listen port", body: "CONNECT listen port. Restart required." },
    "cfg-mitm-passthrough": { title: "Passthrough default", body: "When true, non-local routes pass through to Cursor origin instead of BYOK." },
    "cfg-mitm-cacert": { title: "CA cert path", body: "Path to MITM CA certificate (~ expanded)." },
    "cfg-mitm-cakey": { title: "CA key path", body: "Path to MITM CA private key." },
    "cfg-mitm-hosts": { title: "MITM hosts", body: "Hostnames to intercept (one per line). Supports simple wildcards like *.api5.cursor.sh." },
    "cfg-vram-strategy": {
      title: "VRAM strategy",
      values: [
        { v: "static", d: "Keep warm models" },
        { v: "dynamic", d: "Evict aggressively" },
        { v: "hybrid", d: "Balance both" },
      ],
    },
    "cfg-vram-headroom": { title: "Headroom (MB)", body: "Reserved free VRAM the allocator will not fill." },
    "cfg-vram-max": { title: "Max loaded models", body: "Soft cap on concurrently loaded local models." },
    "cfg-vram-gpus": { title: "GPU assignments", body: "JSON map of model name → GPU index. Prefer the VRAM & Models tab." },
    "cfg-dash-enabled": { title: "Dashboard enabled", body: "Serve this UI. Restart required to toggle." },
    "cfg-xform-enabled": { title: "Transforms enabled", body: "Master switch for prompt transforms." },
    "cfg-xform-trim": { title: "Trim context", body: "Trim oversized context toward max local tokens." },
    "cfg-xform-prepend": { title: "Augment prepend", body: "Text prepended when transforms are enabled." },
    "cfg-xform-append": { title: "Augment append", body: "Text appended when transforms are enabled." },
    "cfg-aliases": { title: "Aliases JSON", body: "JSON object: client model → local model name." },
    "cfg-rules": { title: "Rules JSON", body: "JSON array of rules. Prefer the Rules Engine tab." },
    "cfg-models": { title: "Models JSON", body: "JSON array of model objects (name, backend, vram_estimate_mb, …)." },
    "cfg-backends": { title: "Backends JSON", body: "JSON array: ollama / vllm entries with url and health_check_interval. Hot-reloaded into the live registry (in-flight Complete keeps the old client)." },
    "cfg-budget": { title: "Budget cap (USD)", body: "Optional USD budget cap for cloud spend tracking." },
    "cfg-rpm": { title: "Requests / min", body: "Rate limit across cloud providers." },
    "cfg-tpm": { title: "Tokens / min", body: "Token rate limit across cloud providers." },
    "cfg-providers": { title: "Providers JSON", body: "Providers array. Use api_key_env names — never paste secrets." },
    "cfg-yaml": { title: "glider.yaml", body: "Raw YAML editor for full config. Prefer the structured form unless you need advanced keys." },
    "stage-chip-router": { title: "Add router", body: "Chooses / updates local vs cloud bias for following stages." },
    "stage-chip-planner": { title: "Add planner", body: "Produces the cycle plan." },
    "stage-chip-actor": { title: "Add actor", body: "Implements against the plan." },
    "stage-chip-critic": { title: "Add critic", body: "Scores / gates success." },
    "stage-chip-memory": { title: "Add memory", body: "Loads or writes shared memory." },
    "stage-chip-human_gate": { title: "Add human gate", body: "Pauses for human approval." },
    "stage-chip-code": { title: "Add code stage", body: "Open the stage editor to set id, name, prompt, tools." },
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

  function tipAttrs(el) {
    // legacy no-op; prefer data-tip-key / data-tip + bindCustomTips
    return el;
  }

  bindCustomTips(document);
  // Re-bind after dynamic panel renders.
  const tipObserver = new MutationObserver(() => bindCustomTips(document));
  tipObserver.observe(document.getElementById("app") || document.body, { childList: true, subtree: true });


  function resetLiveMetrics() {
    requests = 0;
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

  function formatSpend(spend) {
    const s = spend && typeof spend === "object" ? spend : {};
    const tokens = Number(s.tokens) || 0;
    const cost = Number(s.cost_usd) || 0;
    const soft = !!s.soft_hit;
    const hard = !!s.hard_hit;
    if (!tokens && !cost && !soft && !hard) return null;
    const parts = [];
    parts.push(tokens.toLocaleString() + " tok");
    if (cost > 0) parts.push("$" + cost.toFixed(cost >= 1 ? 2 : 4));
    if (hard) parts.push("hard");
    else if (soft) parts.push("soft");
    return {
      text: parts.join(" · "),
      soft,
      hard,
      tokens,
      cost,
    };
  }

  function spendChipHTML(spend) {
    const f = formatSpend(spend);
    if (!f) return "";
    const cls = f.hard ? "spend-chip hard" : f.soft ? "spend-chip soft" : "spend-chip";
    const tip = [
      f.tokens + " tokens",
      f.cost > 0 ? ("$" + f.cost.toFixed(4)) : null,
      f.hard ? "hard budget hit" : f.soft ? "soft budget hit" : null,
    ]
      .filter(Boolean)
      .join(" · ");
    return `<span class="${cls}" title="${esc(tip)}">${esc(f.text)}</span>`;
  }

  function aggregateHoopSpend(loops) {
    const out = { tokens: 0, cost_usd: 0, soft_hit: false, hard_hit: false };
    (loops || []).forEach((st) => {
      const s = st && st.spend;
      if (!s) return;
      out.tokens += Number(s.tokens) || 0;
      out.cost_usd += Number(s.cost_usd) || 0;
      if (s.soft_hit) out.soft_hit = true;
      if (s.hard_hit) out.hard_hit = true;
    });
    return out;
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
        applyDistribution(snap.distribution, {
          requests: (snap.distribution.local_count || 0) +
            (snap.distribution.cloud_count || 0) +
            (snap.distribution.canned_count || 0) +
            (snap.distribution.error_count || 0),
        });
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
      requests += 1;
      const action = data.action || data.route || "";
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
    document.getElementById("cfg-mitm-cacert").value = cfg.mitm?.ca_cert ?? "";
    document.getElementById("cfg-mitm-cakey").value = cfg.mitm?.ca_key ?? "";
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
        ca_cert: document.getElementById("cfg-mitm-cacert").value,
        ca_key: document.getElementById("cfg-mitm-cakey").value,
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
          <button type="button" class="linkish rule-del" data-tip="Remove this rule from the draft list">Remove</button>
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
          applyDistribution(agg.distribution, { requests: s.request_count });
        } else {
          requests = s.request_count || 0;
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
          if (agg.distribution) {
            applyDistribution(agg.distribution, { requests: s.request_count });
          } else {
            requests = s.request_count || 0;
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


  /* ===== Hoops & Swarm + Graph editor (shared stage/swarm state) ===== */
  const DEFAULT_STAGES = ["router", "planner", "actor", "critic", "memory"];
  const SAMPLE_STAGES = ["router", "planner", "actor", "critic", "memory"];
  /** Loop API StageKind values only (see internal/loop/stages.go). */
	const STAGE_KINDS = ["workspace", "router", "planner", "actor", "critic", "memory", "context", "human_gate"];
  let liveBoardTimer = null;
  let liveWsConnected = false;
  let hotswapGenCache = {};
  let stageDnd = { kind: null, from: null, index: -1, uid: null };
  let lastHoopsSnap = "";
  let lastHoopsList = [];
  let lastHotswapSnap = "";
  let stageNodes = [];
  /** @type {{id:string,source:string,target:string,kind:string}[]} */
  let stageEdges = [];
  let stageSelectedUid = null;
  let stageSelectedEdgeId = null;
  let stageLiveStatus = {};
  let stageLivePath = []; // node ids/kinds on DecisionRoute path
  let stageLiveEdges = {}; // edge id -> taken|running|next|fail
  let stageLiveCurrent = "";
  /** @type {object|null} last hoop state for run rail / node inspect */
  let stageLiveHoopState = null;
  let cyLiveMotionRaf = null;
  let cyDashOffset = 0;
  let stageUidSeq = 0;
  let swarmThreads = [];
  let swarmWaveTimeline = null; // durable thread wave timeline overlay for Cytoscape
  /** thread uids linked from orchestrator */
  let swarmLinks = [];
  let swarmSelectedUid = null;
  let swarmUidSeq = 0;
  let suppressRolesSync = false;
  let stageCy = null;
  let swarmCy = null;
  let stageCyBound = false;
  let swarmCyBound = false;
  let suppressCySelect = false;
  let stageEh = null;
  let swarmEh = null;
  let suppressEdgeSync = false;
  let stageLinkMode = false;
  let swarmLinkMode = false;
  let stageEditUid = null;
  const HISTORY_CAP = 50;
  let stageUndoStack = [];
  let stageRedoStack = [];
  let swarmUndoStack = [];
  let swarmRedoStack = [];
  let historySuspended = false;
  /** @type {{scope:string,id:string}|null} */
  let agentLogFocus = null;
  let lastSwarmRunId = "";
  /** @type {object[]} */
  let agentLogViewLines = [];
  let agentLogPaused = false;
  let agentLogAutoScroll = true;
  /** User-pinned stage-run-rail card key (kind|id); empty = no pin. */
  let stageRunRailPin = "";
  /** Open stage-run-rail card keys (user + auto-follow). */
  let stageRunRailOpen = new Set();
  /** When true, newly entered current stage auto-opens once. */
  let stageRunRailFollowLive = true;
  /** Last known current stage key for follow-live transitions. */
  let stageRunRailCurKey = "";

  function cloneJSON(x) {
    return JSON.parse(JSON.stringify(x));
  }

  function snapshotStage() {
    return {
      nodes: cloneJSON(stageNodes),
      edges: cloneJSON(stageEdges),
      selectedUid: stageSelectedUid,
      selectedEdgeId: stageSelectedEdgeId,
    };
  }

  function restoreStage(snap) {
    if (!snap) return;
    historySuspended = true;
    stageNodes = cloneJSON(snap.nodes) || [];
    stageEdges = cloneJSON(snap.edges) || [];
    stageSelectedUid = snap.selectedUid || null;
    stageSelectedEdgeId = snap.selectedEdgeId || null;
    renderStageGraph();
    historySuspended = false;
    updateHistoryButtons();
  }

  function pushStageHistory() {
    if (historySuspended) return;
    stageUndoStack.push(snapshotStage());
    if (stageUndoStack.length > HISTORY_CAP) stageUndoStack.shift();
    stageRedoStack = [];
    updateHistoryButtons();
  }

  function undoStage() {
    if (!stageUndoStack.length) return;
    stageRedoStack.push(snapshotStage());
    restoreStage(stageUndoStack.pop());
  }

  function redoStage() {
    if (!stageRedoStack.length) return;
    stageUndoStack.push(snapshotStage());
    restoreStage(stageRedoStack.pop());
  }

  function snapshotSwarm() {
    return {
      threads: cloneJSON(swarmThreads),
      links: cloneJSON(swarmLinks),
      selectedUid: swarmSelectedUid,
    };
  }

  function restoreSwarm(snap) {
    if (!snap) return;
    historySuspended = true;
    swarmThreads = cloneJSON(snap.threads) || [];
    swarmLinks = cloneJSON(snap.links) || [];
    swarmSelectedUid = snap.selectedUid || null;
    syncSwarmRolesFromGraph();
    renderSwarmGraph();
    updateSwarmToolbar();
    historySuspended = false;
    updateHistoryButtons();
  }

  function pushSwarmHistory() {
    if (historySuspended) return;
    swarmUndoStack.push(snapshotSwarm());
    if (swarmUndoStack.length > HISTORY_CAP) swarmUndoStack.shift();
    swarmRedoStack = [];
    updateHistoryButtons();
  }

  function undoSwarm() {
    if (!swarmUndoStack.length) return;
    swarmRedoStack.push(snapshotSwarm());
    restoreSwarm(swarmUndoStack.pop());
  }

  function redoSwarm() {
    if (!swarmRedoStack.length) return;
    swarmUndoStack.push(snapshotSwarm());
    restoreSwarm(swarmRedoStack.pop());
  }

  function updateHistoryButtons() {
    const su = document.getElementById("stage-undo");
    const sr = document.getElementById("stage-redo");
    const wu = document.getElementById("swarm-undo");
    const wr = document.getElementById("swarm-redo");
    if (su) su.disabled = !stageUndoStack.length;
    if (sr) sr.disabled = !stageRedoStack.length;
    if (wu) wu.disabled = !swarmUndoStack.length;
    if (wr) wr.disabled = !swarmRedoStack.length;
  }

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

  function setAgentLogFocus(scope, id) {
    if (!scope || !id) {
      agentLogFocus = null;
    } else {
      agentLogFocus = { scope, id };
      const fold = document.getElementById("hoops-agent-log-fold");
      if (fold) fold.open = true;
    }
    updateAgentLogChrome();
    refreshAgentLogPanels({ force: true });
  }

  function updateAgentLogChrome() {
    const bound = !!agentLogFocus;
    const label = bound
      ? "Streaming " + agentLogFocus.scope + ":" + agentLogFocus.id + " only"
      : "Select a hoop card or run a swarm to stream that instance only";
    const badgeText = bound
      ? "Following " + agentLogFocus.scope + " " + agentLogFocus.id
      : "Not following";
    const hint = document.getElementById("agent-log-scope-hint");
    const hint2 = document.getElementById("hoops-agent-log-hint");
    if (hint) hint.textContent = label;
    if (hint2) hint2.textContent = label;
    ["agent-log-badge", "hoops-agent-log-badge"].forEach((bid) => {
      const el = document.getElementById(bid);
      if (!el) return;
      el.textContent = badgeText;
      el.classList.toggle("is-bound", bound);
      el.title = bound ? badgeText : "";
    });
    document.querySelectorAll(".hoop-card[data-id]").forEach((card) => {
      const on =
        agentLogFocus &&
        agentLogFocus.scope === "hoop" &&
        card.dataset.id === agentLogFocus.id;
      card.classList.toggle("is-log-focus", !!on);
    });
    syncAgentLogPauseButtons();
  }

  function agentLogStageLabel(e) {
    if (!e) return "--";
    const attrs = e.attrs || {};
    if (attrs.stage) return attrs.stage;
    if (attrs.role) return attrs.role;
    if (attrs.stage_id) return attrs.stage_id;
    if (e.kind) return e.kind;
    return "--";
  }

  function agentLogEntryKey(e) {
    if (!e) return "";
    if (e.seq != null && e.seq !== "") return "seq:" + e.seq;
    if (e.id != null && e.id !== "") return "id:" + e.id;
    return (
      "f:" +
      (e.at || "") +
      "|" +
      (e.kind || "") +
      "|" +
      (e.level || "") +
      "|" +
      (e.message || "")
    );
  }

  function agentLogRowHTML(e) {
    const t = e.at ? new Date(e.at).toLocaleTimeString() : "--";
    const lvl = (e.level || "info").toUpperCase();
    const stage = agentLogStageLabel(e);
    const cls = e.level === "error" ? "error" : e.level === "warn" ? "warn" : "";
    const attrBody = (e.attrs && (e.attrs.text || e.attrs.err)) || "";
    const shortMsg = e.message || "";
    // Prefer attrs body for "Full output" — never hide expander just because the
    // message line embeds a truncated prefix of the same text.
    const showDetail =
      attrBody.length > 0 &&
      (attrBody.length > Math.min(shortMsg.length || 0, 240) + 40 ||
        (shortMsg && attrBody && !shortMsg.includes(attrBody)));
    const full = attrBody || shortMsg;
    const key = agentLogEntryKey(e);
    return (
      '<div class="log-row ' +
      cls +
      '" data-log-key="' +
      esc(key) +
      '">' +
      '<span class="log-time">' +
      esc(t) +
      "</span>" +
      '<span class="log-level">' +
      esc(lvl) +
      "</span>" +
      '<span class="log-stage" data-tip="' +
      esc(stage) +
      '">' +
      esc(stage) +
      "</span>" +
      '<span class="log-msg">' +
      esc(shortMsg) +
      (showDetail
        ? '<details class="log-expand"><summary>Full output</summary><pre class="log-err-detail">' +
          esc(full) +
          "</pre></details>"
        : e.attrs && e.attrs.err && !(e.message || "").includes(e.attrs.err)
          ? '<div class="log-err-detail">' + esc(e.attrs.err) + "</div>"
          : "") +
      "</span>" +
      "</div>"
    );
  }

  function agentLogRowElement(e) {
    const div = document.createElement("div");
    const cls = e.level === "error" ? "error" : e.level === "warn" ? "warn" : "";
    div.className = "log-row" + (cls ? " " + cls : "");
    div.dataset.logKey = agentLogEntryKey(e);
    const t = document.createElement("span");
    t.className = "log-time";
    t.textContent = e.at ? new Date(e.at).toLocaleTimeString() : "--";
    const lvl = document.createElement("span");
    lvl.className = "log-level";
    lvl.textContent = (e.level || "info").toUpperCase();
    const stage = document.createElement("span");
    stage.className = "log-stage";
    stage.textContent = agentLogStageLabel(e);
    stage.title = stage.textContent;
    const msg = document.createElement("span");
    msg.className = "log-msg";
    msg.textContent = e.message || "";
    const full = (e.attrs && (e.attrs.text || e.attrs.err)) || "";
    const shortMsg = e.message || "";
    const showDetail =
      full.length > 0 &&
      (full.length > Math.min(shortMsg.length || 0, 240) + 40 ||
        (shortMsg && full && !shortMsg.includes(full)));
    if (showDetail) {
      const det = document.createElement("details");
      det.className = "log-expand";
      const sum = document.createElement("summary");
      sum.textContent = "Full output";
      const pre = document.createElement("pre");
      pre.className = "log-err-detail";
      pre.textContent = full;
      det.appendChild(sum);
      det.appendChild(pre);
      msg.appendChild(det);
    } else if (e.attrs && e.attrs.err && !(e.message || "").includes(e.attrs.err)) {
      const detail = document.createElement("div");
      detail.className = "log-err-detail";
      detail.textContent = e.attrs.err;
      msg.appendChild(detail);
    }
    div.appendChild(t);
    div.appendChild(lvl);
    div.appendChild(stage);
    div.appendChild(msg);
    return div;
  }

  function agentLogPanels() {
    return [
      document.getElementById("agent-log-panel"),
      document.getElementById("hoops-agent-log-panel"),
    ];
  }

  function updateAgentLogRowElement(row, e) {
    if (!row || !e) return;
    const openExpand = row.querySelector(".log-expand")?.open;
    const next = agentLogRowElement(e);
    if (openExpand) {
      const det = next.querySelector(".log-expand");
      if (det) det.open = true;
    }
    row.replaceWith(next);
  }

  function agentLogMaxSeq(lines) {
    let max = 0;
    (lines || []).forEach((e) => {
      const s = Number(e && e.seq);
      if (Number.isFinite(s) && s > max) max = s;
    });
    return max;
  }

  function mergeAgentLogIntoPanels(entries, opts) {
    const incremental = !!(opts && opts.incremental);
    const next = Array.isArray(entries) ? entries.slice() : [];
    const byKey = new Map();
    agentLogViewLines.forEach((e) => {
      const k = agentLogEntryKey(e);
      if (k) byKey.set(k, e);
    });
    next.forEach((e) => {
      const k = agentLogEntryKey(e);
      if (k) byKey.set(k, e);
    });
    if (incremental) {
      // after_seq poll / WS-compatible upsert: empty payload means nothing new.
      if (!next.length) return;
      agentLogViewLines = Array.from(byKey.values()).sort((a, b) => {
        const sa = Number(a && a.seq) || 0;
        const sb = Number(b && b.seq) || 0;
        return sa - sb;
      });
    } else {
      // Full snapshot: prefer server order; empty clears the view.
      const ordered = [];
      const seen = new Set();
      next.forEach((e) => {
        const k = agentLogEntryKey(e);
        if (!k || seen.has(k)) return;
        seen.add(k);
        ordered.push(byKey.get(k) || e);
      });
      if (!next.length) {
        agentLogViewLines = [];
      } else {
        agentLogViewLines = ordered;
      }
    }
    if (agentLogViewLines.length > 400) agentLogViewLines = agentLogViewLines.slice(-400);
    const panels = agentLogPanels();
    const wantKeys = new Set(agentLogViewLines.map(agentLogEntryKey));
    panels.forEach((panel) => {
      if (!panel) return;
      const empty = panel.querySelector(".log-empty");
      if (!agentLogViewLines.length) {
        panel.innerHTML =
          '<p class="log-empty">No log lines for this instance yet. Start a hoop or run a swarm while following it.</p>';
        return;
      }
      if (empty) empty.remove();
      const existing = new Map();
      panel.querySelectorAll(".log-row[data-log-key]").forEach((row) => {
        existing.set(row.dataset.logKey, row);
      });
      // Remove rows no longer present.
      existing.forEach((row, k) => {
        if (!wantKeys.has(k)) row.remove();
      });
      agentLogViewLines.forEach((e) => {
        const k = agentLogEntryKey(e);
        let row = existing.get(k);
        if (row) {
          // Refresh content only when message/attrs changed (cheap string check).
          const msgEl = row.querySelector(".log-msg");
          const msgText = msgEl?.childNodes?.[0]?.textContent || "";
          const fullPre = row.querySelector(".log-err-detail")?.textContent || "";
          const nextFull = (e.attrs && (e.attrs.text || e.attrs.err)) || "";
          if (msgText !== (e.message || "") || (nextFull && fullPre && fullPre !== nextFull)) {
            updateAgentLogRowElement(row, e);
            row = panel.querySelector('.log-row[data-log-key="' + CSS.escape(k) + '"]');
            if (row) existing.set(k, row);
          }
        } else {
          row = agentLogRowElement(e);
          panel.appendChild(row);
          existing.set(k, row);
        }
      });
      // Reorder to match agentLogViewLines.
      agentLogViewLines.forEach((e) => {
        const row = existing.get(agentLogEntryKey(e));
        if (row) panel.appendChild(row);
      });
      if (agentLogAutoScroll) panel.scrollTop = panel.scrollHeight;
    });
  }

  function renderAgentLogPanels(entries, opts) {
    const force = opts && opts.force;
    const incremental = opts && opts.incremental;
    const next = Array.isArray(entries) ? entries.slice() : [];
    const panels = agentLogPanels();
    if (!force) {
      mergeAgentLogIntoPanels(next, { incremental: !!incremental });
      return;
    }
    // Full replace (focus change / clear / manual refresh): preserve open expands by key.
    const openKeys = new Set();
    const scrollTops = panels.map((p) => (p ? p.scrollTop : 0));
    panels.forEach((panel) => {
      if (!panel) return;
      panel.querySelectorAll(".log-expand[open]").forEach((d) => {
        const row = d.closest(".log-row");
        if (row?.dataset.logKey) openKeys.add(row.dataset.logKey);
      });
    });
    agentLogViewLines = next;
    const html =
      !agentLogViewLines.length
        ? '<p class="log-empty">No log lines for this instance yet. Start a hoop or run a swarm while following it.</p>'
        : agentLogViewLines.map(agentLogRowHTML).join("");
    panels.forEach((a, pi) => {
      if (!a) return;
      a.innerHTML = html;
      if (openKeys.size) {
        a.querySelectorAll(".log-row[data-log-key]").forEach((row) => {
          if (!openKeys.has(row.dataset.logKey)) return;
          const d = row.querySelector(".log-expand");
          if (d) d.open = true;
        });
      }
      if (agentLogAutoScroll) a.scrollTop = a.scrollHeight;
      else a.scrollTop = scrollTops[pi] || 0;
    });
  }

  async function refreshAgentLogPanels(opts) {
    if (!agentLogFocus) {
      renderAgentLogPanels([], { force: true });
      return;
    }
    try {
      const force = !!(opts && opts.force);
      let q =
        "/api/agent-logs?scope=" +
        encodeURIComponent(agentLogFocus.scope) +
        "&id=" +
        encodeURIComponent(agentLogFocus.id) +
        "&limit=200";
      const afterSeq = !force ? agentLogMaxSeq(agentLogViewLines) : 0;
      if (afterSeq > 0) {
        q += "&after_seq=" + encodeURIComponent(String(afterSeq));
      }
      const res = await fetch(q);
      const data = await res.json();
      const entries = Array.isArray(data.entries) ? data.entries : [];
      if (force) {
        renderAgentLogPanels(entries, { force: true });
      } else if (afterSeq > 0) {
        renderAgentLogPanels(entries, { incremental: true });
      } else {
        renderAgentLogPanels(entries, opts);
      }
    } catch (_) {
      if (opts && opts.force) renderAgentLogPanels([], { force: true });
    }
  }

  function formatAgentLogPlain(entries) {
    return (entries || [])
      .map((e) => {
        const t = e.at ? new Date(e.at).toISOString() : "";
        const stage = agentLogStageLabel(e);
        const full = (e.attrs && (e.attrs.text || e.attrs.err)) || "";
        let line = `[${t}] ${(e.level || "info").toUpperCase()} ${stage} ${e.message || ""}`;
        if (full && full !== e.message) line += "\n" + full;
        return line.trim();
      })
      .join("\n\n");
  }

  function formatHoopOutcomePlain(st, o) {
    const id = st?.spec?.id || st?.spec?.name || "hoop";
    const lines = [
      `Hoop: ${id}`,
      `Cycle: #${o.iteration}  success=${!!o.success}  route=${o.route || ""}  latency_ms=${o.latency_ms || 0}`,
      "",
      "=== Summary ===",
      o.err || o.summary || "(empty)",
    ];
    (o.stages || []).forEach((s) => {
      lines.push("");
      lines.push(`=== ${s.kind || "stage"}${s.module_id ? " (" + s.module_id + ")" : ""} · ${s.success ? "ok" : "fail"} ===`);
      lines.push(s.err || s.summary || "(empty)");
    });
    return lines.join("\n");
  }

  async function copyText(text, okMsg) {
    const t = String(text || "");
    try {
      await navigator.clipboard.writeText(t);
      showHoopsOk(okMsg || "Copied");
    } catch (_) {
      try {
        const ta = document.createElement("textarea");
        ta.value = t;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        ta.remove();
        showHoopsOk(okMsg || "Copied");
      } catch (e) {
        showHoopsError("Copy failed: " + e);
      }
    }
  }

  function copyFocusedAgentLogs() {
    if (!agentLogViewLines.length) {
      showHoopsError("No log lines to copy");
      return;
    }
    const header = agentLogFocus
      ? `Agent log · ${agentLogFocus.scope}:${agentLogFocus.id}\n\n`
      : "Agent log\n\n";
    copyText(header + formatAgentLogPlain(agentLogViewLines), "Copied agent log");
  }

  function appendLiveAgentLog(e) {
    if (!agentLogFocus || !e) return;
    if (e.scope !== agentLogFocus.scope || e.instance_id !== agentLogFocus.id) return;
    if (agentLogPaused) return;
    const key = agentLogEntryKey(e);
    const idx = agentLogViewLines.findIndex((x) => agentLogEntryKey(x) === key);
    if (idx >= 0) {
      agentLogViewLines[idx] = e;
      agentLogPanels().forEach((panel) => {
        if (!panel) return;
        const row = panel.querySelector('.log-row[data-log-key="' + CSS.escape(key) + '"]');
        if (row) updateAgentLogRowElement(row, e);
      });
    } else {
      agentLogViewLines.push(e);
      if (agentLogViewLines.length > 400) {
        const drop = agentLogViewLines.length - 400;
        const dropped = agentLogViewLines.splice(0, drop);
        const dropKeys = new Set(dropped.map(agentLogEntryKey));
        agentLogPanels().forEach((panel) => {
          if (!panel) return;
          dropKeys.forEach((k) => {
            panel.querySelector('.log-row[data-log-key="' + CSS.escape(k) + '"]')?.remove();
          });
        });
      }
      agentLogPanels().forEach((panel) => {
        if (!panel) return;
        const empty = panel.querySelector(".log-empty");
        if (empty) empty.remove();
        panel.appendChild(agentLogRowElement(e));
        if (agentLogAutoScroll) panel.scrollTop = panel.scrollHeight;
      });
    }
    // Only refresh the running card's live text — never rebuild the rail.
    if (stageLiveHoopState && agentLogFocus.scope === "hoop") {
      updateStageRunRailLivePre(stageLiveHoopState);
    }
  }

  function setAgentLogPaused(on) {
    agentLogPaused = !!on;
    syncAgentLogPauseButtons();
  }

  function syncAgentLogPauseButtons() {
    ["agent-log-pause", "hoops-agent-log-pause"].forEach((id) => {
      const btn = document.getElementById(id);
      if (!btn) return;
      btn.setAttribute("aria-pressed", agentLogPaused ? "true" : "false");
      btn.textContent = agentLogPaused ? "Resume" : "Pause";
    });
  }

  function clearAgentLogView() {
    agentLogViewLines = [];
    renderAgentLogPanels([], { force: true });
  }

  async function clearFocusedAgentLog() {
    clearAgentLogView();
    if (agentLogFocus) {
      try {
        await clearServerAgentLog(agentLogFocus.scope, agentLogFocus.id);
      } catch (_) {}
    }
  }

  function initAgentLogUI() {
    const refresh = () => refreshAgentLogPanels({ force: true });
    const clear = () => clearFocusedAgentLog();
    const togglePause = () => setAgentLogPaused(!agentLogPaused);
    const copy = () => copyFocusedAgentLogs();
    document.getElementById("agent-log-refresh")?.addEventListener("click", refresh);
    document.getElementById("hoops-agent-log-refresh")?.addEventListener("click", refresh);
    document.getElementById("agent-log-clear-view")?.addEventListener("click", clear);
    document.getElementById("hoops-agent-log-clear")?.addEventListener("click", clear);
    document.getElementById("agent-log-pause")?.addEventListener("click", togglePause);
    document.getElementById("hoops-agent-log-pause")?.addEventListener("click", togglePause);
    document.getElementById("agent-log-copy")?.addEventListener("click", copy);
    document.getElementById("hoops-agent-log-copy")?.addEventListener("click", copy);
    ["agent-log-panel", "hoops-agent-log-panel"].forEach((id) => {
      const panel = document.getElementById(id);
      if (!panel) return;
      panel.addEventListener("scroll", () => {
        const nearBottom = panel.scrollHeight - panel.scrollTop - panel.clientHeight < 40;
        agentLogAutoScroll = nearBottom;
      });
    });
    updateAgentLogChrome();
    renderAgentLogPanels([], { force: true });
  }

  const CY_BASE_STYLE = [
    {
      selector: "node",
      style: {
        shape: "round-rectangle",
        width: 96,
        height: 44,
        "background-color": "#ffffff",
        "border-width": 1.5,
        "border-color": "#e5e7eb",
        label: "data(label)",
        "font-family": "IBM Plex Mono, ui-monospace, monospace",
        "font-size": 11,
        color: "#000000",
        "text-valign": "center",
        "text-halign": "center",
        "text-wrap": "wrap",
        "text-max-width": 88,
        "overlay-padding": 4,
      },
    },
    {
      selector: "node.orch",
      style: {
        width: 108,
        height: 52,
        "background-color": "#f8fafc",
        "border-color": "#c45c26",
        "font-size": 10,
      },
    },
    {
      selector: "node:selected",
      style: {
        "border-width": 2.5,
        "border-color": "#c45c26",
        "background-color": "#fff4ed",
      },
    },
    {
      selector: "node.status-ok",
      style: { "border-color": "#16a34a" },
    },
    {
      selector: "node.status-fail",
      style: { "border-color": "#b91c1c" },
    },
    {
      selector: "node.status-running, node.route-current",
      style: {
        "border-color": "#0d9488",
        "border-width": 3.5,
        "background-color": "#f0fdfa",
        "border-style": "dashed",
        "underlay-color": "#0d9488",
        "underlay-opacity": 0.18,
        "underlay-padding": 6,
      },
    },
    {
      selector: "node.status-pending",
      style: {
        "border-color": "#94a3b8",
        "border-style": "dashed",
        "background-color": "#f8fafc",
        color: "#64748b",
      },
    },
    {
      selector: "node.status-waiting",
      style: { "border-color": "#b45309", "border-width": 2.5, "background-color": "#fffbeb" },
    },
    {
      selector: "node.route-path",
      style: { "background-color": "#ecfdf5" },
    },
    {
      selector: "node.eh-handle",
      style: {
        height: 12,
        width: 12,
        shape: "ellipse",
        "background-color": "#64748b",
        "border-width": 0,
        label: "",
      },
    },
    {
      selector: "node.eh-hover",
      style: {
        "background-color": "#c45c26",
      },
    },
    {
      selector: "edge",
      style: {
        width: 1.5,
        "curve-style": "bezier",
        "line-color": "#e5e7eb",
        "target-arrow-shape": "triangle",
        "target-arrow-color": "#e5e7eb",
        "arrow-scale": 0.9,
      },
    },
    {
      selector: "edge:selected",
      style: {
        width: 2.5,
        "line-color": "#c45c26",
        "target-arrow-color": "#c45c26",
      },
    },
    {
      selector: "edge.feedback, edge.on_fail, edge.escalate",
      style: {
        "curve-style": "unbundled-bezier",
        "control-point-distances": [60],
        "control-point-weights": [0.5],
        "line-style": "dashed",
        "line-color": "#b45309",
        "target-arrow-color": "#b45309",
      },
    },
    {
      selector: "edge.conditional, edge.budget_exceeded",
      style: {
        "line-style": "dotted",
        "line-color": "#7c3aed",
        "target-arrow-color": "#7c3aed",
      },
    },
    {
      selector: "edge.feedback:selected",
      style: {
        "line-color": "#92400e",
        "target-arrow-color": "#92400e",
        width: 2.5,
      },
    },
    {
      selector: "edge.status-ok, edge.route-taken",
      style: {
        "line-color": "#16a34a",
        "target-arrow-color": "#16a34a",
        width: 2.4,
        "line-style": "solid",
      },
    },
    {
      selector: "edge.status-fail",
      style: { "line-color": "#b91c1c", "target-arrow-color": "#b91c1c" },
    },
    {
      selector: "edge.status-running, edge.route-next, edge.live-flow",
      style: {
        "line-color": "#0d9488",
        "target-arrow-color": "#0d9488",
        width: 2.8,
        "line-style": "dashed",
        "line-dash-pattern": [10, 6],
        "arrow-scale": 1.15,
      },
    },
    {
      selector: ".eh-preview, .eh-ghost-edge",
      style: {
        "line-color": "#c45c26",
        "target-arrow-color": "#c45c26",
        "line-style": "dashed",
      },
    },
  ];

  function cyAvailable() {
    return typeof window.cytoscape === "function";
  }

  function ehAvailable() {
    return cyAvailable() && stageCy && typeof stageCy.edgehandles === "function";
  }

  function resizeCyInstances(opts) {
    const fit = !!(opts && opts.fit);
    if (stageCy) {
      stageCy.resize();
      if (fit && stageNodes.length) stageCy.fit(undefined, 48);
    }
    if (swarmCy) {
      swarmCy.resize();
      if (fit && swarmThreads.length) swarmCy.fit(undefined, 40);
    }
  }

  function isHoopsTabActive() {
    return panels.hoops && panels.hoops.classList.contains("active");
  }

  function isGraphsTabActive() {
    return panels.graphs && panels.graphs.classList.contains("active");
  }

  function isLiveLoopTabActive() {
    return isHoopsTabActive() || isGraphsTabActive();
  }

  function openGraphEditor(focus) {
    activateTab("graphs", { focus: focus || "stage" });
  }

  function initGraphEditorNav() {
    document.querySelectorAll(".open-graph-editor").forEach((btn) => {
      btn.addEventListener("click", () => openGraphEditor(btn.dataset.graphFocus || "stage"));
    });
    document.getElementById("graphs-back-hoops")?.addEventListener("click", () => activateTab("hoops"));
  }

  async function refreshStageLiveThenRender() {
    try {
      const res = await fetch("/api/loops");
      const list = await res.json();
      refreshStageLiveFromLoops(Array.isArray(list) ? list : []);
    } catch (_) {}
    renderStageGraph();
    renderSwarmGraph();
  }

  function nextStageUid(kind) {
    stageUidSeq += 1;
    return (kind || "stage") + "_" + stageUidSeq;
  }

  function nextSwarmUid(role) {
    swarmUidSeq += 1;
    return "thread_" + (role || "w") + "_" + swarmUidSeq;
  }

  function nextEdgeId(prefix, source, target) {
    return prefix + "_" + source + "_" + target + "_" + Date.now().toString(36);
  }

  function coerceStageKind(kind) {
    const k = String(kind || "actor").toLowerCase().replace(/[^a-z0-9_-]+/g, "");
    if (STAGE_KINDS.includes(k)) return k;
    return "actor";
  }

  function normalizeStageNode(raw, idx) {
    if (typeof raw === "string") {
      const kind = coerceStageKind(raw);
      return {
        uid: nextStageUid(kind),
        kind,
        id: kind,
        name: kind,
        enabled: true,
        prompt: "",
        route: "",
        eval_min: 0,
        x: null,
        y: null,
      };
    }
    const rawKind = String(raw.kind || "actor").toLowerCase();
    const kind = coerceStageKind(rawKind === "code" ? "actor" : rawKind);
    const id = String(raw.id || raw.name || kind);
    const name = String(raw.name || id || kind);
    return {
      uid: raw.uid || nextStageUid(kind + (idx != null ? idx : "")),
      kind,
      id,
      name,
      enabled: raw.enabled !== false && !raw.disabled,
      prompt: raw.prompt || "",
      route: raw.route || "",
      eval_min: typeof raw.eval_min === "number" ? raw.eval_min : 0,
      parallel: typeof raw.parallel === "number" ? raw.parallel : 0,
      roles: Array.isArray(raw.roles) ? raw.roles : [],
      tools: Array.isArray(raw.tools) ? raw.tools : [],
      mcp: Array.isArray(raw.mcp) ? raw.mcp : (raw.mcp_servers || []),
      workspace_mode: raw.workspace_mode === "existing" ? "existing" : (kind === "workspace" ? "run" : ""),
      workspace_path: raw.workspace_path || "",
      out_path: raw.out_path || "",
      x: typeof raw.x === "number" ? raw.x : null,
      y: typeof raw.y === "number" ? raw.y : null,
    };
  }

  function hasFlowPath(from, to) {
    const adj = {};
    stageEdges
      .filter((e) => e.kind !== "feedback")
      .forEach((e) => {
        (adj[e.source] = adj[e.source] || []).push(e.target);
      });
    const seen = new Set();
    const stack = [from];
    while (stack.length) {
      const u = stack.pop();
      if (u === to) return true;
      if (seen.has(u)) continue;
      seen.add(u);
      (adj[u] || []).forEach((v) => stack.push(v));
    }
    return false;
  }

  function inferEdgeKind(source, target) {
    const src = stageNodes.find((n) => n.uid === source);
    const tgt = stageNodes.find((n) => n.uid === target);
    if (src && tgt && src.kind === "critic" && tgt.kind === "planner") return "feedback";
    if (hasFlowPath(target, source)) return "feedback";
    return "flow";
  }

  function syncSwarmRolesFromGraph() {
    const roles = swarmThreads.filter((t) => swarmLinks.includes(t.uid)).map((t) => t.role);
    writeRolesInput(roles);
  }

  function defaultEdgesForNodes(nodes) {
    const edges = [];
    for (let i = 0; i < nodes.length - 1; i++) {
      edges.push({
        id: "flow_" + nodes[i].uid + "_" + nodes[i + 1].uid,
        source: nodes[i].uid,
        target: nodes[i + 1].uid,
        kind: "flow",
      });
    }
    const critic = nodes.find((s) => s.kind === "critic");
    const planner = nodes.find((s) => s.kind === "planner");
    if (critic && planner && critic.uid !== planner.uid) {
      edges.push({
        id: "feedback_" + critic.uid + "_" + planner.uid,
        source: critic.uid,
        target: planner.uid,
        kind: "feedback",
      });
    }
    return edges;
  }

  function reorderStagesFromFlowEdges() {
    if (!stageNodes.length) return;
    const byUid = Object.fromEntries(stageNodes.map((n) => [n.uid, n]));
    const uids = stageNodes.map((n) => n.uid);
    const flow = stageEdges.filter((e) => e.kind !== "feedback");
    const indeg = Object.fromEntries(uids.map((u) => [u, 0]));
    const adj = Object.fromEntries(uids.map((u) => [u, []]));
    flow.forEach((e) => {
      if (!byUid[e.source] || !byUid[e.target]) return;
      adj[e.source].push(e.target);
      indeg[e.target] = (indeg[e.target] || 0) + 1;
    });
    const q = uids.filter((u) => (indeg[u] || 0) === 0);
    const ordered = [];
    while (q.length) {
      const u = q.shift();
      ordered.push(u);
      (adj[u] || []).forEach((v) => {
        indeg[v] -= 1;
        if (indeg[v] === 0) q.push(v);
      });
    }
    uids.forEach((u) => {
      if (!ordered.includes(u)) ordered.push(u);
    });
    stageNodes = ordered.map((u) => byUid[u]).filter(Boolean);
  }

  function stagesFromKinds(kinds) {
    return (kinds || []).map((k, i) => normalizeStageNode(k, i));
  }

  function setLiveWs(text, ok) {
    const el = document.getElementById("live-ws");
    if (!el) return;
    el.textContent = text;
    el.classList.toggle("ok", !!ok);
    el.classList.toggle("bad", ok === false);
  }

  function startLiveBoardPoll() {
    stopLiveBoardPoll();
    tickLiveBoard();
    liveBoardTimer = setInterval(() => {
      if (isLiveLoopTabActive()) tickLiveBoard();
      else stopLiveBoardPoll();
    }, 2000);
  }

  function stopLiveBoardPoll() {
    if (liveBoardTimer) {
      clearInterval(liveBoardTimer);
      liveBoardTimer = null;
    }
  }

  async function tickLiveBoard() {
    await refreshLiveBoard();
    if (isHoopsTabActive()) {
      await loadHotSwap();
    }
  }

  async function refreshLiveBoard() {
    setLiveWs(liveWsConnected ? "Connected" : "Disconnected", liveWsConnected);
    const [modsRes, metricsRes, ctxRes] = await Promise.allSettled([
      fetch("/api/hotswap/modules").then((r) => (r.ok ? r.json() : {})),
      fetch("/api/metrics").then((r) => (r.ok ? r.json() : null)),
      fetch("/api/context/recent?limit=1").then((r) => (r.ok ? r.json() : null)),
    ]);

    const set = (id, v) => {
      const el = document.getElementById(id);
      if (el) el.textContent = v;
    };

    if (metricsRes.status === "fulfilled" && metricsRes.value) {
      const dist = metricsRes.value.distribution || {};
      if (dist.total > 0 || dist.local_pct != null) {
        const lp = dist.local_pct != null ? Number(dist.local_pct) : 0;
        const cp = dist.cloud_pct != null ? Number(dist.cloud_pct) : 0;
        set("live-route", fmtPct(lp) + "% / " + fmtPct(cp) + "%");
      } else if (metricsRes.value.context_routes) {
        const cr = metricsRes.value.context_routes;
        set("live-route", (cr.local || 0) + " / " + (cr.cloud || 0));
      } else {
        set("live-route", "--");
      }
    } else {
      set("live-route", "--");
    }

    if (modsRes.status === "fulfilled" && modsRes.value) {
      const data = modsRes.value;
      let mods = data.modules?.length ? data.modules : (data.catalog || []);
      const on = mods.filter((m) => m.enabled !== false).length;
      set("live-modules", on + "/" + (mods.length || 0));
    } else {
      set("live-modules", "--");
    }

    if (ctxRes.status === "fulfilled" && ctxRes.value?.stats?.turns != null) {
      set("live-ctx", String(ctxRes.value.stats.turns));
    } else if (ctxRes.status === "fulfilled" && Array.isArray(ctxRes.value?.turns)) {
      set("live-ctx", String(ctxRes.value.turns.length));
    } else {
      set("live-ctx", "--");
    }
  }

  function statusBadgeClass(status) {
    const s = String(status || "idle").toLowerCase();
    if (s === "running") return "running";
    if (s === "waiting_human") return "waiting";
    if (s === "failed") return "failed";
    if (s === "completed") return "completed";
    if (s === "stopped") return "stopped";
    return "idle";
  }

  function syncStagesJSON() {
    const hidden = document.getElementById("hoop-stages-json");
    if (!hidden) return stageNodes.slice();
    reorderStagesFromFlowEdges();
    const payload = stageNodes.map((n) => {
      const kind = coerceStageKind(n.kind);
      const row = {
        kind,
        id: n.id || kind,
        name: n.name || kind,
        enabled: n.enabled !== false,
      };
      if (n.prompt) row.prompt = n.prompt;
      if (n.route) row.route = n.route;
      if (kind === "critic" && n.eval_min > 0) row.eval_min = n.eval_min;
      if (n.parallel > 1) row.parallel = Number(n.parallel) || 0;
      if (Array.isArray(n.roles) && n.roles.length) row.roles = n.roles;
      if (Array.isArray(n.tools) && n.tools.length) row.tools = n.tools;
      if (Array.isArray(n.mcp) && n.mcp.length) row.mcp = n.mcp;
      return row;
    });
    hidden.value = JSON.stringify(payload);
    const empty = document.getElementById("stage-graph-empty");
    const host = document.getElementById("stage-graph-host");
    if (empty) empty.hidden = stageNodes.length > 0;
    if (host) host.classList.toggle("has-nodes", stageNodes.length > 0);
    const pipeEmpty = document.getElementById("stage-pipeline-empty");
    if (pipeEmpty) pipeEmpty.hidden = stageNodes.length > 0;
    const pipe = document.getElementById("stage-pipeline");
    if (pipe) pipe.classList.toggle("has-chips", stageNodes.length > 0);
    return payload;
  }

  function readStagesFromJSON() {
    const hidden = document.getElementById("hoop-stages-json");
    if (!hidden) return stageNodes.slice();
    try {
      const v = JSON.parse(hidden.value || "[]");
      if (!Array.isArray(v)) return [];
      return v.map((x, i) => normalizeStageNode(x, i));
    } catch (_) {
      return [];
    }
  }

  function updateStageToolbar() {
    const has = !!stageSelectedUid && stageNodes.some((n) => n.uid === stageSelectedUid);
    const hasEdge = !!stageSelectedEdgeId && stageEdges.some((e) => e.id === stageSelectedEdgeId);
    ["stage-node-edit", "stage-node-up", "stage-node-down"].forEach((id) => {
      const el = document.getElementById(id);
      if (el) el.disabled = !has;
    });
    const rem = document.getElementById("stage-node-remove");
    if (rem) rem.disabled = !has && !hasEdge;
    const edgeBtn = document.getElementById("stage-edge-toggle");
    if (edgeBtn) edgeBtn.disabled = !hasEdge;
  }

  function setStageLinkMode(on) {
    stageLinkMode = !!on;
    const btn = document.getElementById("stage-link-mode");
    const host = document.getElementById("stage-graph-host");
    if (btn) {
      btn.setAttribute("aria-pressed", stageLinkMode ? "true" : "false");
      btn.textContent = stageLinkMode ? "Link mode: on" : "Link mode";
    }
    if (host) host.classList.toggle("link-mode-on", stageLinkMode);
    if (stageEh) {
      if (stageLinkMode) stageEh.enableDrawMode();
      else stageEh.disableDrawMode();
    }
  }

  function setSwarmLinkMode(on) {
    swarmLinkMode = !!on;
    const btn = document.getElementById("swarm-link-mode");
    const host = document.getElementById("swarm-graph-host");
    if (btn) {
      btn.setAttribute("aria-pressed", swarmLinkMode ? "true" : "false");
      btn.textContent = swarmLinkMode ? "Link mode: on" : "Link mode";
    }
    if (host) host.classList.toggle("link-mode-on", swarmLinkMode);
    if (swarmEh) {
      if (swarmLinkMode) swarmEh.enableDrawMode();
      else swarmEh.disableDrawMode();
    }
  }

  function selectStageNode(uid) {
    stageSelectedUid = uid;
    if (uid) stageSelectedEdgeId = null;
    updateStageToolbar();
    if (stageCy) {
      suppressCySelect = true;
      stageCy.elements().unselect();
      if (uid) {
        const el = stageCy.getElementById(uid);
        if (el && el.nonempty()) el.select();
      }
      suppressCySelect = false;
    }
    const node = stageNodes.find((n) => n.uid === uid);
    if (node) {
      const body = document.getElementById("stage-run-rail-body");
      body?.querySelectorAll(".stage-run-card").forEach((card) => {
        const match =
          stageNodeMatchesKey(node, card.dataset.stageId) ||
          stageNodeMatchesKey(node, card.dataset.stageKind);
        card.classList.toggle("is-selected", !!match);
        if (match) {
          const key = stageRunCardKey(card.dataset.stageKind, card.dataset.stageId);
          stageRunRailPin = key;
          stageRunRailFollowLive = false;
          stageRunRailOpen.add(key);
          if (!card.open) card.open = true;
          card.scrollIntoView({ block: "nearest", behavior: "smooth" });
        }
      });
    }
  }

  function selectStageEdge(eid) {
    stageSelectedEdgeId = eid;
    if (eid) stageSelectedUid = null;
    updateStageToolbar();
    if (stageCy) {
      suppressCySelect = true;
      stageCy.elements().unselect();
      if (eid) {
        const el = stageCy.getElementById(eid);
        if (el && el.nonempty()) el.select();
      }
      suppressCySelect = false;
    }
  }

  function defaultStagePosition(i, n, hostW, hostH) {
    const w = Math.max(hostW || 640, 320);
    const h = Math.max(hostH || 420, 280);
    const padX = 80;
    const padY = Math.min(h * 0.35, 120);
    const usable = Math.max(w - padX * 2, 80);
    const t = n <= 1 ? 0.5 : i / (n - 1);
    return {
      x: padX + usable * t,
      y: padY + Math.sin(t * Math.PI) * 36,
    };
  }

  function buildStageCyElements() {
    const host = document.getElementById("stage-graph-host");
    const w = host?.clientWidth || 640;
    const h = host?.clientHeight || 420;
    const n = stageNodes.length;
    const pathSet = new Set(stageLivePath || []);
    const nodes = stageNodes.map((node, i) => {
      const live = stageLiveStatus[node.id] || stageLiveStatus[node.kind] || "idle";
      let x = typeof node.x === "number" ? node.x : null;
      let y = typeof node.y === "number" ? node.y : null;
      if (x == null || y == null) {
        const p = defaultStagePosition(i, n, w, h);
        x = p.x;
        y = p.y;
        node.x = x;
        node.y = y;
      }
      const classes = ["status-" + live];
      if (pathSet.has(node.id) || pathSet.has(node.uid) || pathSet.has(node.kind)) classes.push("route-path");
      if (stageLiveCurrent && (stageLiveCurrent === node.id || stageLiveCurrent === node.uid || stageLiveCurrent === node.kind)) {
        classes.push("route-current");
      }
      return {
        group: "nodes",
        data: {
          id: node.uid,
          label: (node.name || node.kind).slice(0, 14) + "\n" + node.kind,
          kind: node.kind,
          status: live,
        },
        position: { x, y },
        classes: classes.join(" "),
      };
    });
    const uidSet = new Set(stageNodes.map((n) => n.uid));
    const edges = stageEdges
      .filter((e) => uidSet.has(e.source) && uidSet.has(e.target))
      .map((e) => {
        const routeSt = stageLiveEdges[e.id] || "";
        const kindClass = e.kind && e.kind !== "flow" ? e.kind : "";
        const routeBits = [];
        if (routeSt === "taken") routeBits.push("route-taken", "status-ok");
        else if (routeSt === "running" || routeSt === "next") routeBits.push("status-running", "route-next", "live-flow");
        else if (routeSt === "fail") routeBits.push("status-fail");
        const classes = [kindClass, ...routeBits].filter(Boolean).join(" ");
        return {
          group: "edges",
          data: {
            id: e.id,
            source: e.source,
            target: e.target,
            kind: e.kind || "flow",
          },
          classes,
        };
      });
    return nodes.concat(edges);
  }

  function upsertStageEdge(source, target, kind, opts) {
    if (!(opts && opts.skipHistory)) pushStageHistory();
    const k = kind === "feedback" ? "feedback" : "flow";
    if (!source || !target || source === target) return;
    if (!stageNodes.some((n) => n.uid === source) || !stageNodes.some((n) => n.uid === target)) return;
    if (k === "flow") {
      stageEdges = stageEdges.filter((e) => !(e.kind !== "feedback" && e.source === source));
    } else {
      stageEdges = stageEdges.filter((e) => !(e.kind === "feedback" && e.source === source && e.target === target));
    }
    stageEdges.push({
      id: nextEdgeId(k, source, target),
      source,
      target,
      kind: k,
    });
    if (k === "flow") reorderStagesFromFlowEdges();
  }

  function removeStageEdgeById(eid, opts) {
    if (!(opts && opts.skipHistory)) pushStageHistory();
    stageEdges = stageEdges.filter((e) => e.id !== eid);
    if (stageSelectedEdgeId === eid) stageSelectedEdgeId = null;
  }

  function toggleSelectedStageEdgeKind() {
    const e = stageEdges.find((x) => x.id === stageSelectedEdgeId);
    if (!e) return;
    const dlg = document.getElementById("edge-kind-dialog");
    const sel = document.getElementById("edge-kind-select");
    if (sel) sel.value = e.kind || "flow";
    if (dlg && typeof dlg.showModal === "function") {
      dlg.showModal();
      return;
    }
    // Fallback: cycle kinds when dialog unavailable.
    pushStageHistory();
    const cycle = ["flow", "feedback", "on_fail", "escalate", "conditional", "budget_exceeded", "parallel", "merge", "feeds"];
    const i = Math.max(0, cycle.indexOf(e.kind || "flow"));
    e.kind = cycle[(i + 1) % cycle.length];
    if (e.kind === "flow") reorderStagesFromFlowEdges();
    renderStageGraph();
    showHoopsOk("Edge is now " + e.kind);
  }

  function applyEdgeKindDialog() {
    const e = stageEdges.find((x) => x.id === stageSelectedEdgeId);
    const sel = document.getElementById("edge-kind-select");
    if (!e || !sel) return;
    pushStageHistory();
    e.kind = sel.value || "flow";
    if (e.kind === "flow") reorderStagesFromFlowEdges();
    renderStageGraph();
    showHoopsOk("Edge is now " + e.kind);
    const dlg = document.getElementById("edge-kind-dialog");
    if (dlg) dlg.close();
  }

  function initEdgeKindDialog() {
    document.getElementById("edge-kind-save")?.addEventListener("click", (ev) => {
      ev.preventDefault();
      applyEdgeKindDialog();
    });
    document.getElementById("edge-kind-cancel")?.addEventListener("click", (ev) => {
      ev.preventDefault();
      document.getElementById("edge-kind-dialog")?.close();
    });
  }

  function makeEdgehandles(cy, onComplete) {
    if (!cy || typeof cy.edgehandles !== "function") return null;
    const eh = cy.edgehandles({
      canConnect: (source, target) => source && target && !source.same(target),
      edgeParams: () => ({ data: { kind: "flow" }, classes: "eh-ghost" }),
      hoverDelay: 80,
      snap: true,
      snapThreshold: 36,
      snapFrequency: 15,
      noEdgeEventsInDraw: true,
      disableBrowserGestures: true,
    });
    cy.on("ehcomplete", (_ev, sourceNode, targetNode, added) => {
      if (suppressEdgeSync) return;
      const source = sourceNode.id();
      const target = targetNode.id();
      if (added && added.length) added.remove();
      onComplete(source, target);
    });
    return eh;
  }

  function ensureStageCy() {
    if (stageCy || !cyAvailable()) return stageCy;
    const host = document.getElementById("stage-graph-host");
    if (!host) return null;
    stageCy = window.cytoscape({
      container: host,
      elements: [],
      style: CY_BASE_STYLE,
      layout: { name: "preset" },
      minZoom: 0.35,
      maxZoom: 2.5,
      wheelSensitivity: 0.25,
      boxSelectionEnabled: false,
      selectionType: "single",
    });
    if (!stageCyBound) {
      stageCyBound = true;
      stageCy.on("tap", "node", (ev) => {
        if (suppressCySelect) return;
        if (ev.target.hasClass("eh-handle")) return;
        selectStageNode(ev.target.id());
      });
      stageCy.on("tap", "edge", (ev) => {
        if (suppressCySelect) return;
        selectStageEdge(ev.target.id());
      });
      stageCy.on("tap", (ev) => {
        if (ev.target === stageCy) {
          selectStageNode(null);
          selectStageEdge(null);
        }
      });
      stageCy.on("dbltap", "node", (ev) => {
        if (ev.target.hasClass("eh-handle")) return;
        selectStageNode(ev.target.id());
        editSelectedStageNode();
      });
      stageCy.on("dbltap", "edge", (ev) => {
        selectStageEdge(ev.target.id());
        toggleSelectedStageEdgeKind();
      });
      stageCy.on("grab", "node", (ev) => {
        if (historySuspended || stageLinkMode || ev.target.hasClass("eh-handle")) return;
        const node = stageNodes.find((n) => n.uid === ev.target.id());
        if (!node) return;
        const p = ev.target.position();
        ev.target.scratch("_preDrag", { x: node.x, y: node.y, px: p.x, py: p.y });
        pushStageHistory();
        ev.target.scratch("_histPushed", true);
      });
      stageCy.on("dragfree", "node", (ev) => {
        if (ev.target.hasClass("eh-handle")) return;
        const node = stageNodes.find((n) => n.uid === ev.target.id());
        if (!node) return;
        const p = ev.target.position();
        const pre = ev.target.scratch("_preDrag");
        const moved =
          !pre ||
          Math.abs((pre.px || 0) - p.x) > 2 ||
          Math.abs((pre.py || 0) - p.y) > 2;
        if (!moved && ev.target.scratch("_histPushed") && stageUndoStack.length) {
          stageUndoStack.pop();
          updateHistoryButtons();
        }
        node.x = p.x;
        node.y = p.y;
        ev.target.scratch("_preDrag", null);
        ev.target.scratch("_histPushed", false);
        syncStagesJSON();
      });
      stageCy.on("mouseover", "node", (ev) => {
        if (ev.target.hasClass("eh-handle")) return;
        const node = stageNodes.find((n) => n.uid === ev.target.id());
        if (!node) return;
        const live = stageLiveStatus[node.kind] || "idle";
        host.title =
          (node.name || node.kind) +
          " | kind: " +
          node.kind +
          " | status: " +
          live +
          (node.prompt ? " | prompt set" : "");
      });
      stageCy.on("mouseout", "node", () => {
        host.title = "";
      });
      stageEh = makeEdgehandles(stageCy, (source, target) => {
        const kind = inferEdgeKind(source, target);
        upsertStageEdge(source, target, kind);
        renderStageGraph();
        showHoopsOk((kind === "feedback" ? "Feedback " : "Linked ") + source + " -> " + target);
      });
      if (stageLinkMode) stageEh?.enableDrawMode();
    }
    return stageCy;
  }

  function renderStageGraph() {
    syncStagesJSON();
    syncPipelineChipsFromNodes();
    updateStageToolbar();
    const empty = document.getElementById("stage-graph-empty");
    const host = document.getElementById("stage-graph-host");
    if (empty) empty.hidden = stageNodes.length > 0;
    if (host) host.classList.toggle("has-nodes", stageNodes.length > 0);
    if (!cyAvailable()) return;
    if (!isGraphsTabActive() && !stageCy) return;
    ensureStageCy();
    if (!stageCy) return;
    const els = buildStageCyElements();
    suppressEdgeSync = true;
    stageCy.batch(() => {
      stageCy.elements().remove();
      if (els.length) stageCy.add(els);
      stageCy.elements().unselect();
      if (stageSelectedUid) {
        const sel = stageCy.getElementById(stageSelectedUid);
        if (sel.nonempty()) sel.select();
      } else if (stageSelectedEdgeId) {
        const sel = stageCy.getElementById(stageSelectedEdgeId);
        if (sel.nonempty()) sel.select();
      }
    });
    suppressEdgeSync = false;
    stageCy.resize();
  }

  function syncPipelineChipsFromNodes() {
    const pipe = document.getElementById("stage-pipeline");
    if (!pipe) return;
    const existing = [...pipe.querySelectorAll(".stage-chip.in-pipeline")];
    const kinds = stageNodes.map((n) => n.kind);
    const same =
      existing.length === kinds.length &&
      existing.every((c, i) => c.dataset.kind === kinds[i] && c.dataset.uid === stageNodes[i].uid);
    if (same) {
      syncStagesJSON();
      return;
    }
    existing.forEach((c) => c.remove());
    stageNodes.forEach((n) => pipe.appendChild(makePipelineChip(n)));
    syncStagesJSON();
  }

  function makePipelineChip(nodeOrKind) {
    const node = typeof nodeOrKind === "string"
      ? normalizeStageNode(nodeOrKind)
      : nodeOrKind;
    const chip = document.createElement("div");
    chip.className = "stage-chip in-pipeline";
    chip.draggable = true;
    chip.dataset.kind = node.kind;
    chip.dataset.uid = node.uid;
    chip.innerHTML =
      `<span class="stage-chip-label">${esc(node.name || node.kind)}</span>` +
      `<button type="button" class="stage-chip-x" aria-label="Remove ${esc(node.kind)}" title="Remove">x</button>`;
    chip.addEventListener("dragstart", (ev) => {
      const chips = [...chip.parentElement.querySelectorAll(".stage-chip.in-pipeline")];
      stageDnd = { kind: node.kind, from: "pipeline", index: chips.indexOf(chip), uid: node.uid };
      chip.classList.add("dragging");
      ev.dataTransfer.effectAllowed = "move";
      ev.dataTransfer.setData("text/plain", node.kind);
    });
    chip.addEventListener("dragend", () => {
      chip.classList.remove("dragging");
      stageDnd = { kind: null, from: null, index: -1 };
      document.getElementById("stage-pipeline")?.classList.remove("drag-over");
      document.getElementById("stage-graph-host")?.classList.remove("drag-over");
    });
    chip.addEventListener("dblclick", () => {
      removeStageByUid(node.uid);
    });
    chip.querySelector(".stage-chip-x").addEventListener("click", (ev) => {
      ev.stopPropagation();
      removeStageByUid(node.uid);
    });
    return chip;
  }

  let mcpServersCache = [];
  let mcpSelectedServerId = "";
  let stageEditMcpSelected = [];
  let stageEditMcpToolNames = new Set(); // "server:tool"

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
        <div data-tip="Whether a GitHub token is available from env or ~/.glider/credentials/github_token"><span class="live-label">Token</span>${tok}</div>
        <div data-tip="Hosted GitHub MCP over HTTP (api.githubcopilot.com/mcp/)"><span class="live-label">HTTP (github)</span>${http}</div>
        <div data-tip="Optional local stdio GitHub MCP process"><span class="live-label">Stdio (github-stdio)</span>${stdio}</div>
        <div data-tip="OAuth device/browser flow readiness (needs GLIDER_GITHUB_OAUTH_CLIENT_ID)"><span class="live-label">Device flow</span>${oauth}</div>
        <div data-tip="Remote MCP endpoint URL"><span class="live-label">Endpoint</span><code>${escapeHtml(gh.remote_url || "")}</code></div>
      </div>
      ${gh.hint ? `<p class="hint" style="margin:10px 0 0">${escapeHtml(gh.hint)}</p>` : ""}
      <div class="mcp-github-actions">
        <button type="button" class="primary" data-mcp-gh="signin" data-tip="Open GitHub OAuth (browser or device flow) to store a token and connect MCP">Sign in with GitHub</button>
        <button type="button" class="linkish" data-mcp-gh="pat" data-tip="Paste a personal access token; saved to ~/.glider/credentials/github_token">Paste PAT</button>
        <button type="button" class="linkish" data-mcp-gh="forget" data-tip="Remove saved credential file and disconnect GitHub MCP sessions">Forget token</button>
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

  async function ensureMCPServersCache() {
    if (mcpServersCache.length) return mcpServersCache;
    try {
      const res = await fetch("/api/mcp/servers");
      if (!res.ok) return [];
      const data = await res.json();
      mcpServersCache = Array.isArray(data.servers) ? data.servers : [];
    } catch (_) {
      mcpServersCache = [];
    }
    return mcpServersCache;
  }

  async function renderStageMCPPickers(node) {
    const pick = document.getElementById("stage-edit-mcp-pick");
    const toolsEl = document.getElementById("stage-edit-mcp-tools");
    const hidden = document.getElementById("stage-edit-mcp");
    if (!pick) return;
    await ensureMCPServersCache();
    const fromNode = Array.isArray(node?.mcp) ? node.mcp.slice() : [];
    if (!fromNode.length && Array.isArray(node?.tools)) {
      node.tools.forEach((t) => {
        if (t && t.kind === "mcp" && t.server && !fromNode.includes(t.server)) fromNode.push(t.server);
      });
    }
    stageEditMcpSelected = fromNode;
    stageEditMcpToolNames = new Set();
    if (Array.isArray(node?.tools)) {
      node.tools.forEach((t) => {
        if (t && t.kind === "mcp" && t.server && t.name && t.name !== "*" && t.name !== "list_tools") {
          stageEditMcpToolNames.add(`${t.server}:${t.name}`);
        }
      });
    }
    if (hidden) hidden.value = stageEditMcpSelected.join(", ");
    const servers = mcpServersCache.length
      ? mcpServersCache
      : [{ id: "github", name: "GitHub MCP" }, { id: "github-stdio", name: "GitHub MCP (stdio)" }];
    pick.innerHTML = servers.map((s) => {
      const checked = stageEditMcpSelected.includes(s.id) ? "checked" : "";
      return `<label class="mcp-check" data-tip="Allow this stage to call tools from MCP server ${escapeAttr(s.id)}"><input type="checkbox" data-mcp-server="${escapeAttr(s.id)}" ${checked} /> <code>${escapeHtml(s.id)}</code> <span class="hint">${escapeHtml(s.name || "")}</span></label>`;
    }).join("");
    pick.querySelectorAll("input[data-mcp-server]").forEach((inp) => {
      inp.addEventListener("change", () => {
        stageEditMcpSelected = Array.from(pick.querySelectorAll("input[data-mcp-server]:checked")).map((i) => i.getAttribute("data-mcp-server"));
        if (hidden) hidden.value = stageEditMcpSelected.join(", ");
        renderStageMCPToolChecks();
      });
    });
    await renderStageMCPToolChecks();
  }

  async function renderStageMCPToolChecks() {
    const toolsEl = document.getElementById("stage-edit-mcp-tools");
    if (!toolsEl) return;
    if (!stageEditMcpSelected.length) {
      toolsEl.innerHTML = "<p class=\"hint\" style=\"margin:0\">Select one or more MCP servers above.</p>";
      return;
    }
    toolsEl.innerHTML = "<p class=\"hint\">Loading toolsâ€¦</p>";
    const blocks = [];
    for (const sid of stageEditMcpSelected) {
      let tools = [];
      try {
        const res = await fetch(`/api/mcp/servers/${encodeURIComponent(sid)}/tools`);
        if (res.ok) {
          const data = await res.json();
          tools = Array.isArray(data.tools) ? data.tools : [];
        }
      } catch (_) {}
      if (!tools.length) {
        blocks.push(`<div class="mcp-tool-group"><strong>${escapeHtml(sid)}</strong><p class="hint">No tools listed â€” leave unchecked to bind all.</p></div>`);
        continue;
      }
      const checks = tools.map((t) => {
        const key = `${sid}:${t.name}`;
        const checked = stageEditMcpToolNames.has(key) ? "checked" : "";
        const tip = escapeAttr(t.description || `Bind MCP tool ${t.name} from ${sid}`);
        return `<label class="mcp-check" data-tip="${tip}"><input type="checkbox" data-mcp-tool-server="${escapeAttr(sid)}" data-mcp-tool-name="${escapeAttr(t.name)}" ${checked} /> <code>${escapeHtml(t.name)}</code></label>`;
      }).join("");
      blocks.push(`<div class="mcp-tool-group"><strong>${escapeHtml(sid)}</strong><div class="mcp-tool-checks">${checks}</div></div>`);
    }
    toolsEl.innerHTML = blocks.join("");
    toolsEl.querySelectorAll("input[data-mcp-tool-name]").forEach((inp) => {
      inp.addEventListener("change", () => {
        const key = `${inp.getAttribute("data-mcp-tool-server")}:${inp.getAttribute("data-mcp-tool-name")}`;
        if (inp.checked) stageEditMcpToolNames.add(key);
        else stageEditMcpToolNames.delete(key);
      });
    });
  }

  function openStageEditDialog(uid) {
    const dlg = document.getElementById("stage-edit-dialog");
    if (!dlg) {
      if (uid) {
        stageSelectedUid = uid;
        editSelectedStageNodePrompts();
      }
      return;
    }
    stageEditUid = uid || null;
    const node = uid ? stageNodes.find((n) => n.uid === uid) : null;
    const title = document.getElementById("stage-edit-title");
    if (title) title.textContent = node ? "Edit stage" : "Code stage";
    const kindEl = document.getElementById("stage-edit-kind");
    const idEl = document.getElementById("stage-edit-id");
    const nameEl = document.getElementById("stage-edit-name");
    const enEl = document.getElementById("stage-edit-enabled");
    const promptEl = document.getElementById("stage-edit-prompt");
    const routeEl = document.getElementById("stage-edit-route");
    const evalEl = document.getElementById("stage-edit-eval-min");
    const toolsEl = document.getElementById("stage-edit-tools");
    if (kindEl) kindEl.value = coerceStageKind(node?.kind || "actor");
    if (idEl) idEl.value = node?.id || "";
    if (nameEl) nameEl.value = node?.name || "";
    if (enEl) enEl.checked = node ? node.enabled !== false : true;
    if (promptEl) promptEl.value = node?.prompt || "";
    if (routeEl) routeEl.value = node?.route || "";
    if (evalEl) evalEl.value = node?.eval_min > 0 ? String(node.eval_min) : "";
    const wsModeEl = document.getElementById("stage-edit-workspace-mode");
    const wsPathEl = document.getElementById("stage-edit-workspace-path");
    const outPathEl = document.getElementById("stage-edit-out-path");
    if (wsModeEl) wsModeEl.value = node?.workspace_mode === "existing" ? "existing" : "run";
    if (wsPathEl) wsPathEl.value = node?.workspace_path || "";
    if (outPathEl) outPathEl.value = node?.out_path || "";
    if (toolsEl) {
      // Show builtins + any non-picker MCP tools in advanced JSON.
      const builtins = (Array.isArray(node?.tools) ? node.tools : []).filter((t) => t && t.kind !== "mcp");
      toolsEl.value = builtins.length ? JSON.stringify(builtins, null, 0) : "";
    }
    renderStageMCPPickers(node);
    if (typeof dlg.showModal === "function") dlg.showModal();
    else dlg.setAttribute("open", "");
  }

  function applyStageEditForm() {
    const kind = coerceStageKind(document.getElementById("stage-edit-kind")?.value || "actor");
    let id = String(document.getElementById("stage-edit-id")?.value || "").trim();
    let name = String(document.getElementById("stage-edit-name")?.value || "").trim();
    if (!id) id = name || kind;
    if (!name) name = id;
    const enabled = !!document.getElementById("stage-edit-enabled")?.checked;
    const prompt = String(document.getElementById("stage-edit-prompt")?.value || "");
    const route = String(document.getElementById("stage-edit-route")?.value || "");
    const evalRaw = Number(document.getElementById("stage-edit-eval-min")?.value);
    const eval_min = Number.isFinite(evalRaw) && evalRaw > 0 ? evalRaw : 0;
    const workspace_mode = String(document.getElementById("stage-edit-workspace-mode")?.value || "run");
    const workspace_path = String(document.getElementById("stage-edit-workspace-path")?.value || "").trim();
    const out_path = String(document.getElementById("stage-edit-out-path")?.value || "").trim();
    let tools = [];
    const toolsRaw = String(document.getElementById("stage-edit-tools")?.value || "").trim();
    if (toolsRaw) {
      try {
        const parsed = JSON.parse(toolsRaw);
        if (Array.isArray(parsed)) tools = parsed.filter((t) => t && t.kind !== "mcp");
      } catch (_) {
        showHoopsOk("Tools JSON invalid -- fix before save");
        return;
      }
    }
    const pick = document.getElementById("stage-edit-mcp-pick");
    let mcp = stageEditMcpSelected.slice();
    if (pick) {
      mcp = Array.from(pick.querySelectorAll("input[data-mcp-server]:checked")).map((i) => i.getAttribute("data-mcp-server"));
    } else {
      mcp = String(document.getElementById("stage-edit-mcp")?.value || "")
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
    }
    // Collect explicit MCP tool picks.
    const toolPick = document.getElementById("stage-edit-mcp-tools");
    const picked = [];
    if (toolPick) {
      toolPick.querySelectorAll("input[data-mcp-tool-name]:checked").forEach((inp) => {
        picked.push({
          name: inp.getAttribute("data-mcp-tool-name"),
          kind: "mcp",
          server: inp.getAttribute("data-mcp-tool-server"),
        });
      });
    }
    if (picked.length) {
      tools = tools.concat(picked);
    } else if (mcp.length) {
      tools = tools.concat(mcp.map((server) => ({ name: "*", kind: "mcp", server })));
    }
    if (stageEditUid) {
      const node = stageNodes.find((n) => n.uid === stageEditUid);
      if (!node) return;
      pushStageHistory();
      node.kind = kind;
      node.id = id;
      node.name = name;
      node.enabled = enabled;
      node.prompt = prompt;
      node.route = route;
      node.eval_min = eval_min;
      node.tools = tools;
      node.mcp = mcp;
      node.workspace_mode = kind === "workspace" ? workspace_mode : "";
      node.workspace_path = kind === "workspace" ? workspace_path : "";
      node.out_path = kind === "workspace" ? out_path : "";
      selectStageNode(node.uid);
      renderStageGraph();
      showHoopsOk("Updated stage: " + node.id);
    } else {
      // rest of applyStageEditForm continues below â€” keep existing branch
      pushStageHistory();
      const node = normalizeStageNode({
        kind,
        id,
        name,
        enabled,
        prompt,
        route,
        eval_min,
        tools,
        mcp,
        workspace_mode: kind === "workspace" ? workspace_mode : "",
        workspace_path: kind === "workspace" ? workspace_path : "",
        out_path: kind === "workspace" ? out_path : "",
      });
      stageNodes.push(node);
      selectStageNode(node.uid);
      renderStageGraph();
      showHoopsOk("Added stage: " + node.id);
    }
    syncStagesJSON();
    const dlg = document.getElementById("stage-edit-dialog");
    if (dlg && typeof dlg.close === "function") dlg.close();
    else if (dlg) dlg.removeAttribute("open");
    stageEditUid = null;
  }

  function initStageEditDialog() {
    const form = document.getElementById("stage-edit-form");
    if (!form || form.dataset.bound) return;
    form.dataset.bound = "1";
    const closeDlg = () => {
      const dlg = document.getElementById("stage-edit-dialog");
      if (dlg && typeof dlg.close === "function") dlg.close();
      else if (dlg) dlg.removeAttribute("open");
    };
    document.getElementById("stage-edit-save")?.addEventListener("click", (ev) => {
      ev.preventDefault();
      applyStageEditForm();
      closeDlg();
    });
    document.getElementById("stage-edit-cancel")?.addEventListener("click", (ev) => {
      ev.preventDefault();
      stageEditUid = null;
      closeDlg();
    });
    form.addEventListener("submit", (ev) => {
      ev.preventDefault();
      applyStageEditForm();
      closeDlg();
    });
    document.getElementById("stage-node-add-code")?.addEventListener("click", () => openStageEditDialog(null));
  }

  function addStageNode(kind, insertAt, fields) {
    const raw = String(kind || "").toLowerCase();
    if (!raw || raw === "__code__" || raw === "code") {
      openStageEditDialog(null);
      return;
    }
    pushStageHistory();
    const k = coerceStageKind(raw);
    const node = normalizeStageNode({
      kind: k,
      id: (fields && fields.id) || k,
      name: (fields && fields.name) || k,
      enabled: fields && fields.enabled === false ? false : true,
      prompt: (fields && fields.prompt) || "",
      route: (fields && fields.route) || "",
      eval_min: (fields && fields.eval_min) || 0,
    });
    const at =
      insertAt == null || insertAt < 0 || insertAt >= stageNodes.length
        ? stageNodes.length
        : insertAt;
    if (at === 0) {
      stageNodes.unshift(node);
    } else {
      stageNodes.splice(at, 0, node);
    }
    const prev = stageNodes[at - 1];
    const next = stageNodes[at + 1];
    if (prev) {
      stageEdges = stageEdges.filter((e) => !(e.kind !== "feedback" && e.source === prev.uid && next && e.target === next.uid));
      upsertStageEdge(prev.uid, node.uid, "flow", { skipHistory: true });
    }
    if (next) upsertStageEdge(node.uid, next.uid, "flow", { skipHistory: true });
    selectStageNode(node.uid);
    renderStageGraph();
  }

  function removeStageByUid(uid) {
    const idx = stageNodes.findIndex((n) => n.uid === uid);
    if (idx < 0) return;
    pushStageHistory();
    const prev = stageNodes[idx - 1];
    const next = stageNodes[idx + 1];
    stageNodes.splice(idx, 1);
    stageEdges = stageEdges.filter((e) => e.source !== uid && e.target !== uid);
    if (prev && next) upsertStageEdge(prev.uid, next.uid, "flow", { skipHistory: true });
    if (stageSelectedUid === uid) stageSelectedUid = null;
    renderStageGraph();
  }

  function moveSelectedStage(delta) {
    const idx = stageNodes.findIndex((n) => n.uid === stageSelectedUid);
    if (idx < 0) return;
    const j = idx + delta;
    if (j < 0 || j >= stageNodes.length) return;
    pushStageHistory();
    const tmp = stageNodes[idx];
    stageNodes[idx] = stageNodes[j];
    stageNodes[j] = tmp;
    const keepFb = stageEdges.filter((e) => e.kind === "feedback");
    stageEdges = defaultEdgesForNodes(stageNodes).filter((e) => e.kind !== "feedback").concat(keepFb);
    renderStageGraph();
  }

  function editSelectedStageNode() {
    const node = stageNodes.find((n) => n.uid === stageSelectedUid);
    if (!node) return;
    openStageEditDialog(node.uid);
  }

  function editSelectedStageNodePrompts() {
    const node = stageNodes.find((n) => n.uid === stageSelectedUid);
    if (!node) return;
    openStageEditDialog(node.uid);
  }

  function setPipelineStages(kindsOrNodes, edges) {
    stageNodes = (kindsOrNodes || []).map((x, i) => normalizeStageNode(x, i));
    if (Array.isArray(edges) && edges.length) {
      const uidById = {};
      stageNodes.forEach((n) => {
        uidById[n.id] = n.uid;
        uidById[n.uid] = n.uid;
      });
      stageEdges = edges
        .map((e, i) => {
          const src = uidById[e.source] || e.source;
          const tgt = uidById[e.target] || e.target;
          if (!src || !tgt) return null;
          return {
            id: e.id || "e-" + i + "-" + src + "-" + tgt,
            source: src,
            target: tgt,
            kind: e.kind || "flow",
          };
        })
        .filter(Boolean);
      if (!stageEdges.length) stageEdges = defaultEdgesForNodes(stageNodes);
    } else {
      stageEdges = defaultEdgesForNodes(stageNodes);
    }
    stageSelectedUid = null;
    stageSelectedEdgeId = null;
    renderStageGraph();
  }

  function pipelineDropIndex(pipe, clientX, clientY) {
    const chips = [...pipe.querySelectorAll(".stage-chip.in-pipeline:not(.dragging)")];
    for (let i = 0; i < chips.length; i++) {
      const r = chips[i].getBoundingClientRect();
      const midX = r.left + r.width / 2;
      const midY = r.top + r.height / 2;
      if (clientY < midY || (Math.abs(clientY - midY) < r.height && clientX < midX)) {
        return i;
      }
    }
    return chips.length;
  }

  function applyStageDrop(kind, insertAt, moveUid) {
    if (moveUid) {
      const from = stageNodes.findIndex((n) => n.uid === moveUid);
      if (from < 0) return;
      pushStageHistory();
      const [node] = stageNodes.splice(from, 1);
      stageEdges = stageEdges.filter((e) => e.source !== moveUid && e.target !== moveUid);
      let at = insertAt == null ? stageNodes.length : insertAt;
      if (from < at) at -= 1;
      if (at < 0) at = 0;
      if (at > stageNodes.length) at = stageNodes.length;
      stageNodes.splice(at, 0, node);
      const prev = stageNodes[at - 1];
      const next = stageNodes[at + 1];
      if (prev) upsertStageEdge(prev.uid, node.uid, "flow", { skipHistory: true });
      if (next) upsertStageEdge(node.uid, next.uid, "flow", { skipHistory: true });
      selectStageNode(node.uid);
    } else {
      addStageNode(kind, insertAt);
      return;
    }
    renderStageGraph();
  }

  function initStageCyControls() {
    initStageEditDialog();
    initEdgeKindDialog();
    initAgentLogUI();
    document.getElementById("stage-undo")?.addEventListener("click", () => undoStage());
    document.getElementById("stage-redo")?.addEventListener("click", () => redoStage());
    document.getElementById("stage-link-mode")?.addEventListener("click", () => {
      setStageLinkMode(!stageLinkMode);
      showHoopsOk(stageLinkMode ? "Link mode on -- drag from a node onto another" : "Link mode off");
    });
    document.getElementById("stage-node-edit")?.addEventListener("click", () => editSelectedStageNode());
    document.getElementById("stage-node-remove")?.addEventListener("click", () => {
      if (stageSelectedUid) removeStageByUid(stageSelectedUid);
      else if (stageSelectedEdgeId) {
        removeStageEdgeById(stageSelectedEdgeId);
        renderStageGraph();
      }
    });
    document.getElementById("stage-edge-toggle")?.addEventListener("click", () => toggleSelectedStageEdgeKind());
    document.getElementById("stage-node-up")?.addEventListener("click", () => moveSelectedStage(-1));
    document.getElementById("stage-node-down")?.addEventListener("click", () => moveSelectedStage(1));
    document.getElementById("stage-zoom-reset")?.addEventListener("click", () => {
      if (stageCy) stageCy.fit(undefined, 48);
    });
    document.getElementById("stage-zoom-in")?.addEventListener("click", () => {
      if (!stageCy) return;
      stageCy.zoom({
        level: Math.min(2.5, stageCy.zoom() * 1.15),
        renderedPosition: { x: stageCy.width() / 2, y: stageCy.height() / 2 },
      });
    });
    document.getElementById("stage-zoom-out")?.addEventListener("click", () => {
      if (!stageCy) return;
      stageCy.zoom({
        level: Math.max(0.35, stageCy.zoom() / 1.15),
        renderedPosition: { x: stageCy.width() / 2, y: stageCy.height() / 2 },
      });
    });
    document.addEventListener("keydown", (ev) => {
      if (!isGraphsTabActive()) return;
      const t = ev.target;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable)) {
        return;
      }
      const mod = ev.ctrlKey || ev.metaKey;
      if (mod && (ev.key === "z" || ev.key === "Z")) {
        ev.preventDefault();
        if (ev.shiftKey) {
          redoStage();
          redoSwarm();
        } else {
          undoStage();
          undoSwarm();
        }
        return;
      }
      if (mod && (ev.key === "y" || ev.key === "Y")) {
        ev.preventDefault();
        redoStage();
        redoSwarm();
        return;
      }
      if (ev.key !== "Delete" && ev.key !== "Backspace") return;
      ev.preventDefault();
      if (stageSelectedEdgeId) {
        removeStageEdgeById(stageSelectedEdgeId);
        renderStageGraph();
      } else if (stageSelectedUid) {
        removeStageByUid(stageSelectedUid);
      } else if (swarmSelectedUid) {
        pushSwarmHistory();
        swarmThreads = swarmThreads.filter((x) => x.uid !== swarmSelectedUid);
        swarmLinks = swarmLinks.filter((id) => id !== swarmSelectedUid);
        swarmSelectedUid = null;
        syncSwarmRolesFromGraph();
        renderSwarmGraph();
        updateSwarmToolbar();
      }
    });
  }

  function initStageDnD() {
    const palettes = [
      document.getElementById("stage-palette"),
      document.getElementById("stage-palette-hoops"),
    ].filter(Boolean);
    const pipe = document.getElementById("stage-pipeline");
    const host = document.getElementById("stage-graph-host");
    if (!palettes.length || !pipe) return;

    function bindPalette(palette) {
      palette.querySelectorAll(".stage-chip").forEach((chip) => {
        chip.addEventListener("dragstart", (ev) => {
          const kind = chip.dataset.kind;
          stageDnd = { kind, from: "palette", index: -1 };
          chip.classList.add("dragging");
          ev.dataTransfer.effectAllowed = "copy";
          ev.dataTransfer.setData("text/plain", kind);
        });
        chip.addEventListener("dragend", () => {
          chip.classList.remove("dragging");
          stageDnd = { kind: null, from: null, index: -1 };
          pipe.classList.remove("drag-over");
          host?.classList.remove("drag-over");
        });
        chip.addEventListener("click", () => {
          if (chip.dataset.kind === "__code__" || chip.dataset.kind === "code") {
            openStageEditDialog(null);
            return;
          }
          addStageNode(chip.dataset.kind);
          showHoopsOk("Added " + chip.dataset.kind);
        });
      });
    }
    palettes.forEach(bindPalette);

    const bindDropTarget = (el, getInsertAt) => {
      el.addEventListener("dragover", (ev) => {
        ev.preventDefault();
        ev.dataTransfer.dropEffect = stageDnd.from === "pipeline" ? "move" : "copy";
        el.classList.add("drag-over");
      });
      el.addEventListener("dragleave", (ev) => {
        if (!el.contains(ev.relatedTarget)) el.classList.remove("drag-over");
      });
      el.addEventListener("drop", (ev) => {
        ev.preventDefault();
        el.classList.remove("drag-over");
        const kind = stageDnd.kind || ev.dataTransfer.getData("text/plain");
        if (!kind) return;
        const insertAt = getInsertAt(ev);
        if (stageDnd.from === "pipeline" && stageDnd.uid) {
          applyStageDrop(kind, insertAt, stageDnd.uid);
        } else if (stageDnd.from === "pipeline" && stageDnd.index >= 0) {
          const uid = stageNodes[stageDnd.index]?.uid;
          applyStageDrop(kind, insertAt, uid);
        } else {
          applyStageDrop(kind, insertAt, null);
        }
        stageDnd = { kind: null, from: null, index: -1 };
      });
    };

    bindDropTarget(pipe, (ev) => pipelineDropIndex(pipe, ev.clientX, ev.clientY));
    if (host) {
      bindDropTarget(host, () => stageNodes.length);
    }

    initStageCyControls();
    setPipelineStages(DEFAULT_STAGES);
    initSwarmGraph();
  }

  function setHoopEditMode(id, name) {
    const editId = document.getElementById("hoop-edit-id");
    const btn = document.getElementById("hoop-submit-btn");
    const cancel = document.getElementById("hoop-cancel-edit");
    const banner = document.getElementById("stage-edit-banner");
    if (editId) editId.value = id || "";
    if (btn) btn.textContent = id ? "Update hoop" : "Create hoop";
    if (cancel) cancel.hidden = !id;
    if (banner) {
      banner.hidden = !id;
      banner.textContent = id ? "Editing " + (name || id) : "";
    }
  }

  function clearHoopEditMode() {
    setHoopEditMode("", "");
  }

  function loadHoopIntoComposer(st, opts) {
    const spec = st.spec || st;
    document.getElementById("hoop-name").value = spec.name || spec.id || "";
    document.getElementById("hoop-interval").value = spec.interval || "";
    document.getElementById("hoop-prompt").value = spec.goal || spec.prompt || "";
    document.getElementById("hoop-eval-goal").value = spec.eval?.goal || "";
    document.getElementById("hoop-route").value = spec.route || "local";
    document.getElementById("hoop-learning").checked = !!spec.learning;
    document.getElementById("hoop-max-iter").value = String(spec.max_iterations || 3);
    const stages = (spec.stages || []).filter((s) => s.enabled !== false && !s.disabled);
    setPipelineStages(stages.length ? stages : DEFAULT_STAGES, spec.graph_edges || []);
    setHoopEditMode(spec.id, spec.name || spec.id);
    applyStageLiveFromHoop(st);
    const running =
      String(st.status || "").toLowerCase() === "running" ||
      String(st.status || "").toLowerCase() === "waiting_human";
    if (running) {
      setAgentLogFocus("hoop", spec.id);
      startHoopLiveFastPoll();
      startCyLiveMotion();
      refreshAgentLogPanels();
    }
    showHoopsOk(
      running
        ? "Live view: " + (spec.name || spec.id)
        : "Loaded " + (spec.name || spec.id) + " -- edit graph then Update"
    );
    if (opts && opts.openGraph) {
      openGraphEditor("stage");
      requestAnimationFrame(() => {
        renderStageGraph();
        startCyLiveMotion();
      });
    } else {
      document.getElementById("hoop-form")?.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }

  function stageNodeMatchesKey(node, key) {
    if (!node || key == null || key === "") return false;
    const k = String(key);
    return node.id === k || node.uid === k || node.kind === k || node.name === k;
  }

  function findStageEdgeBetween(aKey, bKey) {
    return stageEdges.find((e) => {
      const src = stageNodes.find((n) => n.uid === e.source);
      const tgt = stageNodes.find((n) => n.uid === e.target);
      return stageNodeMatchesKey(src, aKey) && stageNodeMatchesKey(tgt, bKey);
    });
  }

  function applyStageLiveFromHoop(st) {
    stageLiveHoopState = st || null;
    stageLiveStatus = {};
    stageLivePath = [];
    stageLiveEdges = {};
    stageLiveCurrent = "";
    if (!st) {
      paintStageRunRail(null);
      return;
    }
    const status = String(st.status || "").toLowerCase();
    const last = (st.outcomes || []).length ? st.outcomes[st.outcomes.length - 1] : null;
    const prog = st.progress || {};
    stageLiveCurrent = prog.current || prog.stage_id || prog.stage_kind || "";
    if (Array.isArray(prog.path_taken)) stageLivePath = prog.path_taken.slice();
    (prog.edges_taken || []).forEach((eid) => { stageLiveEdges[eid] = "taken"; });
    (prog.next_edges || []).forEach((eid) => {
      if (!stageLiveEdges[eid]) stageLiveEdges[eid] = "next";
    });
    (prog.branch_choices || []).forEach((b) => {
      if (!b || !b.edge_id) return;
      if (b.selected) stageLiveEdges[b.edge_id] = "taken";
      else if (!stageLiveEdges[b.edge_id]) stageLiveEdges[b.edge_id] = "next";
    });
    // Infer canvas edges from path sequence when SM edge ids don't match.
    for (let i = 0; i < stageLivePath.length - 1; i++) {
      const edge = findStageEdgeBetween(stageLivePath[i], stageLivePath[i + 1]);
      if (edge) stageLiveEdges[edge.id] = "taken";
    }
    const running = status === "running" || status === "waiting_human";
    if (running) {
      (st.spec?.stages || stageNodes || []).forEach((s) => {
        if (s.enabled === false || s.disabled) return;
        const kind = s.kind || s;
        const id = s.id;
        if (kind) stageLiveStatus[kind] = stageLiveStatus[kind] || "pending";
        if (id) stageLiveStatus[id] = stageLiveStatus[id] || "pending";
      });
      stageNodes.forEach((n) => {
        if (!stageLiveStatus[n.kind]) stageLiveStatus[n.kind] = "pending";
        if (n.id) stageLiveStatus[n.id] = stageLiveStatus[n.id] || "pending";
      });
      (st.live_stages || []).forEach((s) => {
        const paint = s.success ? "ok" : "fail";
        if (s.kind) stageLiveStatus[s.kind] = paint;
        if (s.module_id) stageLiveStatus[s.module_id] = paint;
      });
      (stageLivePath || []).forEach((id) => {
        if (stageLiveStatus[id] === "pending" || !stageLiveStatus[id]) stageLiveStatus[id] = "ok";
      });
      const cur = prog.stage_kind || prog.stage_id || stageLiveCurrent;
      if (cur) {
        const paint = status === "waiting_human" ? "waiting" : "running";
        stageLiveStatus[cur] = paint;
        if (prog.stage_id) stageLiveStatus[prog.stage_id] = paint;
        if (prog.stage_kind) stageLiveStatus[prog.stage_kind] = paint;
        // Animate edges into the active node.
        stageEdges.forEach((e) => {
          const tgt = stageNodes.find((n) => n.uid === e.target);
          if (!stageNodeMatchesKey(tgt, cur) && !stageNodeMatchesKey(tgt, prog.stage_id) && !stageNodeMatchesKey(tgt, prog.stage_kind)) {
            return;
          }
          if (stageLiveEdges[e.id] !== "taken") stageLiveEdges[e.id] = "running";
        });
        // Also animate edge from last path node → current.
        if (stageLivePath.length) {
          const prev = stageLivePath[stageLivePath.length - 1];
          if (prev !== cur) {
            const edge = findStageEdgeBetween(prev, cur);
            if (edge) stageLiveEdges[edge.id] = "running";
          }
        }
      }
      startCyLiveMotion();
    } else if (last?.stages?.length) {
      last.stages.forEach((s) => {
        stageLiveStatus[s.kind] = s.success ? "ok" : "fail";
        if (s.module_id) stageLiveStatus[s.module_id] = s.success ? "ok" : "fail";
      });
    } else if ((st.live_stages || []).length) {
      st.live_stages.forEach((s) => {
        stageLiveStatus[s.kind] = s.success ? "ok" : "fail";
        if (s.module_id) stageLiveStatus[s.module_id] = s.success ? "ok" : "fail";
      });
    } else if (status === "completed") {
      (st.spec?.stages || []).forEach((s) => {
        stageLiveStatus[s.kind] = "ok";
      });
    } else if (status === "failed") {
      (st.spec?.stages || []).forEach((s) => {
        stageLiveStatus[s.kind] = "fail";
      });
    }
    paintStageRunRail(st);
  }

  function stageRunCardKey(kind, id) {
    return String(kind || "") + "|" + String(id || "");
  }

  function stageRunLiveText(st, curKind, curId, status, prog) {
    const wait = status === "waiting_human";
    if (wait) {
      return (
        st.gate?.ask ||
        [
          st.gate?.reason || prog.note || "Waiting for human approval",
          st.cursor?.critic_text && "--- CRITIC ---\n" + st.cursor.critic_text,
          st.cursor?.actor_text && "--- ACTOR ---\n" + st.cursor.actor_text,
          st.cursor?.plan_text && "--- PLAN ---\n" + st.cursor.plan_text,
        ]
          .filter(Boolean)
          .join("\n\n")
      );
    }
    const liveLogs = (agentLogViewLines || [])
      .filter((e) => {
        const a = e.attrs || {};
        return (
          a.stage === curKind ||
          a.stage_id === curId ||
          a.module_id === curId ||
          (e.message || "").includes(curKind)
        );
      })
      .slice(-6);
    return (
      liveLogs
        .map((e) => {
          const full = (e.attrs && (e.attrs.text || e.attrs.err)) || e.message || "";
          return full;
        })
        .filter(Boolean)
        .join("\n\n") ||
      (prog.note ? "note: " + prog.note : "Running…")
    );
  }

  function updateStageRunRailLivePre(st) {
    if (!st) return;
    const body = document.getElementById("stage-run-rail-body");
    if (!body) return;
    const status = String(st.status || "").toLowerCase();
    const prog = st.progress || {};
    const curKind = prog.stage_kind || prog.stage_id || "";
    const curId = prog.stage_id || "";
    const curKey = stageRunCardKey(curKind, curId);
    const card = [...body.querySelectorAll(".stage-run-card")].find(
      (c) => stageRunCardKey(c.dataset.stageKind, c.dataset.stageId) === curKey
    );
    if (!card || !card.classList.contains("running")) return;
    const pre = card.querySelector(".stage-run-pre");
    if (!pre) return;
    const liveText = stageRunLiveText(st, curKind, curId, status, prog);
    if (pre.textContent !== (liveText || "(empty)")) {
      pre.textContent = liveText || "(empty)";
    }
  }

  function wireStageRunCard(card, key) {
    if (card.dataset.wired === "1") return;
    card.dataset.wired = "1";
    card.addEventListener("toggle", () => {
      if (card.open) {
        stageRunRailOpen.add(key);
        if (key !== stageRunRailCurKey) {
          stageRunRailPin = key;
          stageRunRailFollowLive = false;
        } else {
          stageRunRailPin = "";
          stageRunRailFollowLive = true;
        }
      } else {
        stageRunRailOpen.delete(key);
        if (stageRunRailPin === key) stageRunRailPin = "";
        if (key === stageRunRailCurKey) stageRunRailFollowLive = false;
      }
    });
    card.addEventListener("click", (ev) => {
      const kind = card.dataset.stageKind;
      const id = card.dataset.stageId;
      const node = stageNodes.find((n) => stageNodeMatchesKey(n, id) || stageNodeMatchesKey(n, kind));
      if (!node) return;
      // Summary click toggles open/close — don't call selectStageNode (it force-opens).
      if (ev.target.closest("summary")) {
        stageSelectedUid = node.uid;
        stageSelectedEdgeId = null;
        updateStageToolbar();
        if (stageCy) {
          suppressCySelect = true;
          stageCy.elements().unselect();
          const el = stageCy.getElementById(node.uid);
          if (el && el.nonempty()) el.select();
          suppressCySelect = false;
        }
        const body = document.getElementById("stage-run-rail-body");
        body?.querySelectorAll(".stage-run-card").forEach((c) => {
          const match =
            stageNodeMatchesKey(node, c.dataset.stageId) ||
            stageNodeMatchesKey(node, c.dataset.stageKind);
          c.classList.toggle("is-selected", !!match);
        });
        return;
      }
      selectStageNode(node.uid);
    });
  }

  function upsertStageRunCard(body, spec) {
    let card = [...body.querySelectorAll(".stage-run-card")].find(
      (c) => stageRunCardKey(c.dataset.stageKind, c.dataset.stageId) === spec.key
    );
    const selected = card?.classList.contains("is-selected");
    if (!card) {
      card = document.createElement("details");
      card.className = "stage-run-card " + spec.className;
      card.dataset.stageKind = spec.kind || "";
      card.dataset.stageId = spec.id || "";
      const summary = document.createElement("summary");
      summary.innerHTML = spec.summaryHTML;
      const pre = document.createElement("pre");
      pre.className = "stage-run-pre";
      pre.textContent = spec.preText || "(empty)";
      card.appendChild(summary);
      card.appendChild(pre);
      wireStageRunCard(card, spec.key);
      body.appendChild(card);
      if (spec.wantOpen) card.open = true;
    } else {
      card.className = "stage-run-card " + spec.className + (selected ? " is-selected" : "");
      card.dataset.stageKind = spec.kind || "";
      card.dataset.stageId = spec.id || "";
      const summary = card.querySelector("summary");
      if (summary && summary.innerHTML !== spec.summaryHTML) summary.innerHTML = spec.summaryHTML;
      const pre = card.querySelector(".stage-run-pre");
      if (pre && pre.textContent !== (spec.preText || "(empty)")) {
        pre.textContent = spec.preText || "(empty)";
      }
      // Never force-close / force-open on incremental update except explicit stage enter.
      if (spec.forceOpen) card.open = true;
      wireStageRunCard(card, spec.key);
    }
    return card;
  }

  function paintStageRunRail(st) {
    const rail = document.getElementById("stage-run-rail");
    const body = document.getElementById("stage-run-rail-body");
    const meta = document.getElementById("stage-run-rail-meta");
    if (!rail || !body) return;
    if (!st) {
      rail.hidden = true;
      body.innerHTML = "";
      if (meta) meta.textContent = "Idle";
      stageRunRailPin = "";
      stageRunRailOpen = new Set();
      stageRunRailFollowLive = true;
      stageRunRailCurKey = "";
      return;
    }
    // Sync open set from live DOM (user toggles) before deciding auto-follow.
    body.querySelectorAll(".stage-run-card").forEach((card) => {
      const key = stageRunCardKey(card.dataset.stageKind, card.dataset.stageId);
      if (card.open) stageRunRailOpen.add(key);
      else stageRunRailOpen.delete(key);
    });
    const status = String(st.status || "").toLowerCase();
    const prog = st.progress || {};
    const live = Array.isArray(st.live_stages) ? st.live_stages : [];
    const last = (st.outcomes || []).length ? st.outcomes[st.outcomes.length - 1] : null;
    const done = live.length
      ? live
      : status === "running" || status === "waiting_human"
        ? []
        : last?.stages || [];
    const running = status === "running" || status === "waiting_human";
    const curKind = prog.stage_kind || prog.stage_id || "";
    const curId = prog.stage_id || "";
    const curKey = stageRunCardKey(curKind, curId);
    const hasCurrent =
      running &&
      curKind &&
      !done.some((s) => s.kind === curKind || (curId && s.module_id === curId));
    if (!done.length && !hasCurrent && !running) {
      rail.hidden = true;
      body.innerHTML = "";
      if (meta) meta.textContent = "Idle";
      return;
    }
    rail.hidden = false;
    if (meta) {
      const spendFmt = formatSpend(st.spend);
      const base = running
        ? `Cycle #${prog.iteration || st.iteration || "?"} · ${prog.phase || status} · ${curKind || "…"}`
        : `Last cycle · ${status}`;
      meta.textContent = spendFmt ? `${base} · ${spendFmt.text}` : base;
      meta.title = spendFmt
        ? (spendFmt.hard ? "Hard budget hit" : spendFmt.soft ? "Soft budget hit" : "BudgetSpend")
        : "";
    }

    const stageChanged = curKey !== stageRunRailCurKey;
    if (stageChanged) {
      stageRunRailCurKey = curKey;
      // Auto-open the new current stage once when following live.
      if (stageRunRailFollowLive && hasCurrent && curKey) {
        stageRunRailOpen.add(curKey);
        stageRunRailPin = "";
      }
    }

    const desiredKeys = [];
    const placeholder = body.querySelector(":scope > .muted");
    if (placeholder) placeholder.remove();

    done.forEach((s) => {
      const text = s.err || s.summary || "";
      const ok = !!s.success;
      const key = stageRunCardKey(s.kind, s.module_id);
      desiredKeys.push(key);
      const wantOpen = stageRunRailOpen.has(key) || stageRunRailPin === key;
      upsertStageRunCard(body, {
        key,
        kind: s.kind || "",
        id: s.module_id || "",
        className: ok ? "ok" : "fail",
        summaryHTML:
          `<span class="stage-pill ${ok ? "ok" : "fail"}">${esc(s.kind || "?")}</span> ` +
          `${ok ? "done" : "fail"}` +
          (s.module_id && s.module_id !== s.kind ? ` · ${esc(s.module_id)}` : ""),
        preText: text || "(empty)",
        wantOpen,
        forceOpen: false,
      });
    });

    if (hasCurrent) {
      const wait = status === "waiting_human";
      const liveText = stageRunLiveText(st, curKind, curId, status, prog);
      desiredKeys.push(curKey);
      const wantOpen =
        stageRunRailOpen.has(curKey) ||
        stageRunRailPin === curKey ||
        (stageRunRailFollowLive && stageChanged);
      upsertStageRunCard(body, {
        key: curKey,
        kind: curKind,
        id: curId,
        className: "running",
        summaryHTML:
          `<span class="stage-pill ${wait ? "fail" : "ok"}">${esc(curKind)}</span> ` +
          `${wait ? "waiting" : "running now"}`,
        preText: liveText || "(empty)",
        wantOpen,
        forceOpen: !!(stageRunRailFollowLive && stageChanged),
      });
    }

    // Remove stale cards; keep open details on survivors.
    [...body.querySelectorAll(".stage-run-card")].forEach((card) => {
      const key = stageRunCardKey(card.dataset.stageKind, card.dataset.stageId);
      if (!desiredKeys.includes(key)) card.remove();
    });

    // Stable order: done stages then current.
    desiredKeys.forEach((key) => {
      const card = [...body.querySelectorAll(".stage-run-card")].find(
        (c) => stageRunCardKey(c.dataset.stageKind, c.dataset.stageId) === key
      );
      if (card) body.appendChild(card);
    });

    if (!desiredKeys.length) {
      body.innerHTML = `<p class="muted">Waiting for first stage…</p>`;
    }
  }

  function startCyLiveMotion() {
    if (cyLiveMotionRaf) return;
    const tick = () => {
      cyDashOffset = (cyDashOffset - 1.5) % 64;
      const pulse = 2.8 + Math.sin(Date.now() / 280) * 1.1;
      const under = 0.12 + (Math.sin(Date.now() / 280) + 1) * 0.1;
      let active = false;
      if (stageCy) {
        const runEdges = stageCy.$("edge.status-running, edge.route-next, edge.live-flow");
        const runNodes = stageCy.$("node.status-running, node.route-current");
        if (runEdges.nonempty() || runNodes.nonempty()) {
          active = true;
          stageCy.batch(() => {
            if (runEdges.nonempty()) {
              runEdges.style({
                "line-style": "dashed",
                "line-dash-pattern": [10, 6],
                "line-dash-offset": cyDashOffset,
              });
            }
            if (runNodes.nonempty()) {
              runNodes.style({
                "border-style": "dashed",
                "border-width": pulse,
                "underlay-opacity": under,
              });
            }
          });
        }
      }
      if (swarmCy) {
        const runEdges = swarmCy.$("edge.status-running, edge.route-next, node.status-running");
        const runNodes = swarmCy.$("node.status-running");
        if (runEdges.nonempty() || runNodes.nonempty()) {
          active = true;
          swarmCy.batch(() => {
            swarmCy.$("edge.status-running, edge.route-next").style({
              "line-style": "dashed",
              "line-dash-pattern": [10, 6],
              "line-dash-offset": cyDashOffset,
            });
            if (runNodes.nonempty()) {
              runNodes.style({
                "border-style": "dashed",
                "border-width": pulse,
              });
            }
          });
        }
      }
      if (!active && !hoopLiveFastTimer && !swarmLivePollTimer) {
        cyLiveMotionRaf = null;
        return;
      }
      cyLiveMotionRaf = requestAnimationFrame(tick);
    };
    cyLiveMotionRaf = requestAnimationFrame(tick);
  }

  function refreshStageLiveFromLoops(list) {
    const editId = document.getElementById("hoop-edit-id")?.value;
    if (!editId || !Array.isArray(list)) return;
    const st = list.find((x) => x.spec?.id === editId);
    if (!st) return;
    const before = JSON.stringify({
      status: stageLiveStatus,
      path: stageLivePath,
      edges: stageLiveEdges,
      current: stageLiveCurrent,
      live: st.live_stages,
      prog: st.progress,
      run: st.status,
    });
    applyStageLiveFromHoop(st);
    const after = JSON.stringify({
      status: stageLiveStatus,
      path: stageLivePath,
      edges: stageLiveEdges,
      current: stageLiveCurrent,
      live: st.live_stages,
      prog: st.progress,
      run: st.status,
    });
    if (after !== before) renderStageGraph();
  }

  /* ---- Swarm thread graph (Cytoscape) ---- */
  function rolesFromInput() {
    const el = document.getElementById("swarm-roles");
    if (!el) return ["plan", "exec"];
    return el.value.split(",").map((s) => s.trim()).filter(Boolean);
  }

  function writeRolesInput(roles) {
    const el = document.getElementById("swarm-roles");
    if (!el) return;
    suppressRolesSync = true;
    el.value = roles.join(",");
    suppressRolesSync = false;
  }

  function setSwarmThreadsFromRoles(roles, preserveStatus) {
    const prev = preserveStatus ? Object.fromEntries(swarmThreads.map((t) => [t.role, t])) : {};
    swarmThreads = (roles || []).slice(0, 4).map((role) => {
      const old = prev[role];
      return {
        uid: old?.uid || nextSwarmUid(role),
        role,
        status: old?.status || "idle",
        summary: old?.summary || "",
        err: old?.err || "",
      };
    });
    swarmLinks = swarmThreads.map((t) => t.uid);
    if (!swarmThreads.some((t) => t.uid === swarmSelectedUid)) swarmSelectedUid = null;
    renderSwarmGraph();
    updateSwarmToolbar();
  }

  function updateSwarmToolbar() {
    const has = !!swarmSelectedUid && swarmThreads.some((t) => t.uid === swarmSelectedUid);
    const rem = document.getElementById("swarm-thread-remove");
    const edit = document.getElementById("swarm-thread-edit");
    if (rem) rem.disabled = !has;
    if (edit) edit.disabled = !has;
  }

  function selectSwarmThread(uid) {
    swarmSelectedUid = uid;
    updateSwarmToolbar();
    if (swarmCy) {
      suppressCySelect = true;
      swarmCy.nodes().unselect();
      if (uid) {
        const el = swarmCy.getElementById(uid);
        if (el && el.nonempty()) el.select();
      }
      suppressCySelect = false;
    }
    const th = uid ? swarmThreads.find((t) => t.uid === uid) : null;
    showSwarmNodeInspect(th || null);
  }

  function showSwarmNodeInspect(th) {
    const box = document.getElementById("swarm-node-inspect");
    const title = document.getElementById("swarm-node-inspect-title");
    const status = document.getElementById("swarm-node-inspect-status");
    const body = document.getElementById("swarm-node-inspect-body");
    if (!box || !title || !status || !body) return;
    if (!th) {
      box.hidden = true;
      body.textContent = "";
      return;
    }
    box.hidden = false;
    title.textContent = th.role || th.uid || "Worker";
    status.textContent = th.status || "idle";
    status.className = "muted" + (th.status === "fail" ? " is-error" : th.status === "ok" ? " is-ok" : "");
    const bits = [];
    if (th.err) bits.push("ERROR\n" + th.err);
    if (th.summary) bits.push(th.summary);
    body.textContent = bits.join("\n\n") || "(no output yet — run swarm or wait for live progress)";
  }

  function paintSwarmRunInspect(data, rawText) {
    const wrap = document.getElementById("swarm-run-inspect");
    const meta = document.getElementById("swarm-run-inspect-meta");
    const body = document.getElementById("swarm-run-inspect-body");
    const raw = document.getElementById("swarm-result");
    if (!wrap || !body) return;
    wrap.hidden = false;
    if (raw) {
      try {
        raw.textContent = typeof rawText === "string" && rawText.trim().startsWith("{")
          ? JSON.stringify(JSON.parse(rawText), null, 2)
          : (rawText || "");
      } catch (_) {
        raw.textContent = rawText || "";
      }
    }
    if (!data || typeof data !== "object") {
      if (meta) meta.textContent = "parse error";
      body.innerHTML = `<pre class="run-inspect-pre">${esc(rawText || "")}</pre>`;
      return;
    }
    const results = data.results || [];
    const okN = results.filter((r) => !r.err).length;
    const failN = results.length - okN;
    if (meta) {
      meta.textContent =
        `${okN} ok / ${failN} fail · ${data.elapsed_ms || 0}ms` +
        (data.waves ? ` · ${data.waves} wave(s)` : "") +
        (data.weave_policy ? ` · ${data.weave_policy}` : "");
    }
    const summary = data.summary || data.episode?.summary || "";
    const workerBlocks = results
      .map((r, i) => {
        const label = r.role || r.worker_id || `worker-${i + 1}`;
        const text = r.err || r.summary || r.episode?.summary || "";
        const head = String(text).slice(0, 120);
        return (
          `<details class="hoop-stage-detail"${r.err || i === 0 ? " open" : ""}>` +
          `<summary><span class="stage-pill ${r.err ? "fail" : "ok"}">${esc(label)}</span> ` +
          `${r.err ? "fail" : "ok"}` +
          (r.tokens ? ` · ${r.tokens} tok` : "") +
          (r.model ? ` · ${esc(r.model)}` : "") +
          (head ? ` · ${esc(head)}${text.length > 120 ? "…" : ""}` : "") +
          `</summary>` +
          `<pre class="hoop-stage-pre">${esc(text) || "(empty)"}</pre></details>`
        );
      })
      .join("");
    body.innerHTML =
      (summary
        ? `<details class="hoop-stage-detail" open><summary>Merged summary</summary>` +
          `<pre class="hoop-stage-pre">${esc(summary)}</pre></details>`
        : "") +
      (workerBlocks
        ? `<div class="hoop-stage-details" style="margin-top:8px">${workerBlocks}</div>`
        : `<p class="muted">No per-worker results</p>`);
  }

  function buildSwarmCyElements() {
    if (swarmWaveTimeline && Array.isArray(swarmWaveTimeline.waves)) {
      return buildWaveTimelineElements(swarmWaveTimeline);
    }
    const host = document.getElementById("swarm-graph-host");
    const w = Math.max(host?.clientWidth || 560, 320);
    const h = Math.max(host?.clientHeight || 280, 240);
    const ox = 100;
    const oy = h / 2;
    const els = [
      {
        group: "nodes",
        data: { id: "orch", label: "orchestrator\nweave" },
        position: { x: ox, y: oy },
        classes: "orch",
        grabbable: true,
        selectable: false,
      },
    ];
    const n = swarmThreads.length || 1;
    const top = 48;
    const bot = h - 48;
    const linkSet = new Set(swarmLinks);
    swarmThreads.forEach((th, i) => {
      const t = n === 1 ? 0.5 : i / (n - 1);
      const y = top + (bot - top) * t;
      const x = w - 120;
      const st = th.status || "idle";
      els.push({
        group: "nodes",
        data: {
          id: th.uid,
          label: th.role.slice(0, 12) + "\n" + st,
          role: th.role,
          status: st,
        },
        position: { x: typeof th.x === "number" ? th.x : x, y: typeof th.y === "number" ? th.y : y },
        classes: "status-" + st,
      });
      if (linkSet.has(th.uid)) {
        els.push({
          group: "edges",
          data: { id: "se-" + th.uid, source: "orch", target: th.uid, kind: "flow" },
          classes: "status-" + st,
        });
      }
    });
    if (swarmThreads.length && swarmLinks.length) {
      const linked = swarmThreads.filter((t) => linkSet.has(t.uid));
      const mergeFail = linked.some((t) => t.status === "fail");
      linked.forEach((th) => {
        els.push({
          group: "edges",
          data: { id: "merge-" + th.uid, source: th.uid, target: "orch", kind: "merge" },
          classes: mergeFail ? "feedback status-fail" : "feedback",
        });
      });
      if (mergeFail) {
        els.push({
          group: "nodes",
          data: { id: "merge-fail", label: "merge\nFAILED" },
          position: { x: ox + 80, y: oy + 70 },
          classes: "status-fail",
          selectable: false,
        });
      }
    }
    return els;
  }

  function buildWaveTimelineElements(st) {
    const host = document.getElementById("swarm-graph-host");
    const w = Math.max(host?.clientWidth || 560, 320);
    const h = Math.max(host?.clientHeight || 280, 240);
    const waves = st.waves || [];
    const policy = String(st.weave_policy || "weave").slice(0, 16);
    const els = [
      {
        group: "nodes",
        data: {
          id: "thr",
          label: "thread\n" + String(st.id || "?").slice(0, 14),
        },
        position: { x: 80, y: h / 2 },
        classes: "orch",
        selectable: false,
      },
    ];
    const n = Math.max(waves.length, 1);
    waves.forEach((wv, i) => {
      const x = 180 + ((w - 260) * (n === 1 ? 0.5 : i / Math.max(n - 1, 1)));
      const wid = "wave-" + (wv.index != null ? wv.index : i);
      const fail = (wv.results || []).some((r) => r.err);
      const stLabel = fail ? "fail" : "ok";
      const sum = String((wv.merged && wv.merged.summary) || "").slice(0, 40);
      els.push({
        group: "nodes",
        data: {
          id: wid,
          label: "wave " + (wv.index != null ? wv.index : i) + "\n" + stLabel,
          status: stLabel,
          summary: sum,
        },
        position: { x, y: h / 2 - 20 },
        classes: "status-" + stLabel,
      });
      const prev = i === 0 ? "thr" : ("wave-" + (waves[i - 1].index != null ? waves[i - 1].index : i - 1));
      els.push({
        group: "edges",
        data: { id: "wf-" + wid, source: prev, target: wid, kind: "follows" },
        classes: "status-" + stLabel,
      });
      (wv.results || []).slice(0, 4).forEach((r, ri) => {
        const rid = wid + "-w" + ri;
        const rst = r.err ? "fail" : "ok";
        els.push({
          group: "nodes",
          data: {
            id: rid,
            label: String(r.role || r.worker_id || "w").slice(0, 10) + "\n" + rst,
            status: rst,
          },
          position: { x: x - 30 + ri * 28, y: h / 2 + 70 },
          classes: "status-" + rst,
        });
        els.push({
          group: "edges",
          data: { id: "we-" + rid, source: wid, target: rid, kind: "flow" },
          classes: "status-" + rst,
        });
      });
    });
    if (st.merged_summary || st.merged) {
      els.push({
        group: "nodes",
        data: {
          id: "woven",
          label: policy + "\nwoven",
        },
        position: { x: w - 90, y: h / 2 },
        classes: "status-ok",
        selectable: false,
      });
      const lastWave = waves.length
        ? ("wave-" + (waves[waves.length - 1].index != null ? waves[waves.length - 1].index : waves.length - 1))
        : "thr";
      els.push({
        group: "edges",
        data: { id: "weave-edge", source: lastWave, target: "woven", kind: "merged_into" },
        classes: "feedback",
      });
    }
    return els;
  }

  function paintWaveTimeline(st) {
    swarmWaveTimeline = st;
    const empty = document.getElementById("swarm-graph-empty");
    const host = document.getElementById("swarm-graph-host");
    if (host) host.classList.add("has-nodes");
    if (empty) empty.hidden = true;
    renderSwarmGraph();
    document.querySelector('.open-graph-editor[data-graph-focus="swarm"]')?.click();
    showHoopsOk("Wave timeline: " + (st.id || "thread"));
  }

  function clearWaveTimeline() {
    swarmWaveTimeline = null;
    renderSwarmGraph();
    showHoopsOk("Cleared wave timeline");
  }

  function ensureSwarmCy() {
    if (swarmCy || !cyAvailable()) return swarmCy;
    const host = document.getElementById("swarm-graph-host");
    if (!host) return null;
    swarmCy = window.cytoscape({
      container: host,
      elements: [],
      style: CY_BASE_STYLE,
      layout: { name: "preset" },
      minZoom: 0.35,
      maxZoom: 2.5,
      wheelSensitivity: 0.25,
      boxSelectionEnabled: false,
      selectionType: "single",
    });
    if (!swarmCyBound) {
      swarmCyBound = true;
      swarmCy.on("tap", "node", (ev) => {
        if (suppressCySelect) return;
        if (ev.target.hasClass("eh-handle")) return;
        const id = ev.target.id();
        if (id === "orch") return;
        selectSwarmThread(id);
      });
      swarmCy.on("tap", (ev) => {
        if (ev.target === swarmCy) selectSwarmThread(null);
      });
      swarmCy.on("dbltap", "node", (ev) => {
        if (ev.target.hasClass("eh-handle")) return;
        const id = ev.target.id();
        if (id === "orch") return;
        selectSwarmThread(id);
        editSelectedSwarmThread();
      });
      swarmCy.on("grab", "node", (ev) => {
        if (historySuspended || swarmLinkMode || ev.target.hasClass("eh-handle") || ev.target.id() === "orch") return;
        const th = swarmThreads.find((t) => t.uid === ev.target.id());
        if (!th) return;
        const p = ev.target.position();
        ev.target.scratch("_preDrag", { x: th.x, y: th.y, px: p.x, py: p.y });
        pushSwarmHistory();
        ev.target.scratch("_histPushed", true);
      });
      swarmCy.on("dragfree", "node", (ev) => {
        if (ev.target.hasClass("eh-handle") || ev.target.id() === "orch") return;
        const th = swarmThreads.find((t) => t.uid === ev.target.id());
        if (!th) return;
        const p = ev.target.position();
        const pre = ev.target.scratch("_preDrag");
        const moved =
          !pre ||
          Math.abs((pre.px || 0) - p.x) > 2 ||
          Math.abs((pre.py || 0) - p.y) > 2;
        if (!moved && ev.target.scratch("_histPushed") && swarmUndoStack.length) {
          swarmUndoStack.pop();
          updateHistoryButtons();
        }
        th.x = p.x;
        th.y = p.y;
        ev.target.scratch("_preDrag", null);
        ev.target.scratch("_histPushed", false);
      });
      swarmCy.on("mouseover", "node", (ev) => {
        if (ev.target.hasClass("eh-handle")) return;
        const id = ev.target.id();
        if (id === "orch") {
          host.title = "orchestrator (weave)";
          return;
        }
        const th = swarmThreads.find((t) => t.uid === id);
        if (!th) return;
        host.title =
          th.role +
          " | status: " +
          (th.status || "idle") +
          " — click for full output";
      });
      swarmCy.on("mouseout", "node", () => {
        host.title = "";
      });
      swarmEh = makeEdgehandles(swarmCy, (source, target) => {
        let threadId = null;
        if (source === "orch" && target !== "orch") threadId = target;
        else if (target === "orch" && source !== "orch") threadId = source;
        else if (source !== "orch" && target !== "orch") {
          pushSwarmHistory();
          if (!swarmLinks.includes(source)) swarmLinks.push(source);
          if (!swarmLinks.includes(target)) swarmLinks.push(target);
          syncSwarmRolesFromGraph();
          renderSwarmGraph();
          showHoopsOk("Linked threads via orchestrator");
          return;
        }
        if (!threadId || !swarmThreads.some((t) => t.uid === threadId)) return;
        if (swarmLinks.includes(threadId)) return;
        pushSwarmHistory();
        swarmLinks.push(threadId);
        syncSwarmRolesFromGraph();
        renderSwarmGraph();
        showHoopsOk("Linked thread to orchestrator");
      });
      if (swarmLinkMode) swarmEh?.enableDrawMode();
      swarmCy.on("tap", "edge", (ev) => {
        const id = ev.target.id();
        if (id.startsWith("se-")) {
          const uid = id.slice(3);
          if (!swarmLinks.includes(uid)) return;
          pushSwarmHistory();
          swarmLinks = swarmLinks.filter((x) => x !== uid);
          syncSwarmRolesFromGraph();
          renderSwarmGraph();
          showHoopsOk("Unlinked thread (Link mode + drag to relink)");
        }
      });
    }
    return swarmCy;
  }

  function renderSwarmGraph() {
    const host = document.getElementById("swarm-graph-host");
    const empty = document.getElementById("swarm-graph-empty");
    const has = swarmWaveTimeline ? true : swarmThreads.length > 0;
    if (host) host.classList.toggle("has-nodes", has);
    if (empty) empty.hidden = has;
    if (!cyAvailable()) return;
    if (!isGraphsTabActive() && !swarmCy) return;
    ensureSwarmCy();
    if (!swarmCy) return;
    const els = buildSwarmCyElements();
    suppressEdgeSync = true;
    swarmCy.batch(() => {
      swarmCy.elements().remove();
      swarmCy.add(els);
      swarmCy.nodes().unselect();
      if (swarmSelectedUid && !swarmWaveTimeline) {
        const sel = swarmCy.getElementById(swarmSelectedUid);
        if (sel.nonempty()) sel.select();
      }
    });
    suppressEdgeSync = false;
    swarmCy.resize();
    if (swarmWaveTimeline) {
      try { swarmCy.fit(undefined, 36); } catch (_) {}
    }
  }

  function editSelectedSwarmThread() {
    const th = swarmThreads.find((t) => t.uid === swarmSelectedUid);
    if (!th) return;
    gliderPrompt("Thread role", "plan | exec | research | security | tests | docs | worker", th.role).then((role) => {
      if (role == null) return;
      pushSwarmHistory();
      const next = String(role).trim().toLowerCase().replace(/[^a-z0-9_-]+/g, "") || th.role;
      th.role = next;
      syncSwarmRolesFromGraph();
      renderSwarmGraph();
    });
  }

  function initSwarmGraph() {
    setSwarmThreadsFromRoles(rolesFromInput());
    const rolesEl = document.getElementById("swarm-roles");
    rolesEl?.addEventListener("input", () => {
      if (suppressRolesSync) return;
      setSwarmThreadsFromRoles(rolesFromInput(), true);
    });
    document.getElementById("swarm-undo")?.addEventListener("click", () => undoSwarm());
    document.getElementById("swarm-redo")?.addEventListener("click", () => redoSwarm());
    document.getElementById("swarm-link-mode")?.addEventListener("click", () => {
      setSwarmLinkMode(!swarmLinkMode);
      showHoopsOk(swarmLinkMode ? "Swarm link mode on -- drag orch <-> worker" : "Swarm link mode off");
    });
    document.getElementById("swarm-thread-add")?.addEventListener("click", () => {
      if (swarmThreads.length >= 4) {
        showHoopsError("Max 4 swarm workers");
        return;
      }
      gliderPrompt("New thread role", "ASCII role id", "worker" + (swarmThreads.length + 1)).then((role) => {
        if (role == null || !String(role).trim()) return;
        pushSwarmHistory();
        const r = String(role).trim().toLowerCase().replace(/[^a-z0-9_-]+/g, "") || ("w" + (swarmThreads.length + 1));
        const uid = nextSwarmUid(r);
        swarmThreads.push({ uid, role: r, status: "idle", summary: "", err: "" });
        if (!swarmLinks.includes(uid)) swarmLinks.push(uid);
        syncSwarmRolesFromGraph();
        const workers = document.getElementById("swarm-workers");
        if (workers) workers.value = String(Math.min(4, Math.max(Number(workers.value) || 1, swarmThreads.length)));
        renderSwarmGraph();
        updateSwarmToolbar();
        showHoopsOk("Added thread " + r);
      });
    });
    document.getElementById("swarm-thread-remove")?.addEventListener("click", () => {
      if (!swarmSelectedUid) return;
      pushSwarmHistory();
      swarmThreads = swarmThreads.filter((t) => t.uid !== swarmSelectedUid);
      swarmLinks = swarmLinks.filter((id) => id !== swarmSelectedUid);
      swarmSelectedUid = null;
      syncSwarmRolesFromGraph();
      renderSwarmGraph();
      updateSwarmToolbar();
    });
    document.getElementById("swarm-thread-edit")?.addEventListener("click", () => editSelectedSwarmThread());
    document.getElementById("swarm-zoom-reset")?.addEventListener("click", () => {
      if (swarmCy) swarmCy.fit(undefined, 40);
    });
    document.getElementById("swarm-zoom-in")?.addEventListener("click", () => {
      if (!swarmCy) return;
      swarmCy.zoom({
        level: Math.min(2.5, swarmCy.zoom() * 1.15),
        renderedPosition: { x: swarmCy.width() / 2, y: swarmCy.height() / 2 },
      });
    });
    document.getElementById("swarm-zoom-out")?.addEventListener("click", () => {
      if (!swarmCy) return;
      swarmCy.zoom({
        level: Math.max(0.35, swarmCy.zoom() / 1.15),
        renderedPosition: { x: swarmCy.width() / 2, y: swarmCy.height() / 2 },
      });
    });
    window.addEventListener("resize", () => {
      if (isGraphsTabActive()) resizeCyInstances({ fit: false });
    });
  }

  async function refreshHoopsPanel() {
    lastHotswapSnap = "";
    await Promise.all([loadHotSwap(), loadSwarmTemplates(), refreshLiveBoard()]);
    renderStageGraph();
    renderSwarmGraph();
  }


  async function loadHoops() {
    const el = document.getElementById("hoops-list");
    if (!el) return;
    try {
      const res = await fetch("/api/loops");
      const list = await res.json();
      refreshStageLiveFromLoops(Array.isArray(list) ? list : []);
      const snap = JSON.stringify(list);
      if (snap === lastHoopsSnap && el.querySelector(".hoop-card, .hint, .cfg-error")) return;
      // Preserve nested open details across live list rebuilds.
      const openNested = new Set();
      el.querySelectorAll(".hoop-card").forEach((card) => {
        const hid = card.dataset.id || "";
        card.querySelectorAll(".hoop-stage-detail[open], .hoop-hitl-ask[open]").forEach((d, i) => {
          const label = d.querySelector("summary")?.textContent?.slice(0, 48) || String(i);
          openNested.add(hid + "|" + d.className + "|" + label);
        });
      });
      lastHoopsSnap = snap;
      lastHoopsList = Array.isArray(list) ? list : [];
      if (!Array.isArray(list) || list.length === 0) {
        el.innerHTML = `<p class="hint">No hoops yet. Build a stage graph and create one.</p>`;
        return;
      }
      el.innerHTML = list.map((st) => {
        const name = esc(st.spec?.name || st.spec?.id || "");
        const id = esc(st.spec?.id || "");
        const outcomes = (st.outcomes || []).slice(-8).reverse();
        const last = (st.outcomes || []).length
          ? (st.outcomes)[(st.outcomes).length - 1]
          : null;
        const rows = outcomes.map((o, oi) => {
          const pills = (o.stages || []).map((s) =>
            `<span class="stage-pill ${s.success ? "ok" : "fail"}">${esc(s.kind)}</span>`
          ).join("");
          const detail = o.success
            ? (o.summary || "")
            : (o.err || o.summary || "");
          const detailCls = o.success ? "hoop-outcome-detail" : "hoop-outcome-detail is-error";
          const stageBlocks = (o.stages || [])
            .filter((s) => (s.summary && s.summary.trim()) || (s.err && s.err.trim()))
            .map((s) => {
              const body = s.err || s.summary || "";
              return (
                `<details class="hoop-stage-detail">` +
                `<summary><span class="stage-pill ${s.success ? "ok" : "fail"}">${esc(s.kind)}</span> ` +
                `${s.success ? "ok" : "fail"} · ${esc(String(body).slice(0, 96))}${String(body).length > 96 ? "…" : ""}</summary>` +
                `<pre class="hoop-stage-pre">${esc(body)}</pre></details>`
              );
            })
            .join("");
          return `<div class="hoop-outcome ${o.success ? "ok" : "fail"}" data-outcome-idx="${oi}" data-hoop-id="${id}">` +
            `<span>#${o.iteration}</span><span>${esc(o.route || "")}</span>` +
            `<span>${o.latency_ms || 0}ms</span>` +
            `<button type="button" class="linkish hoop-copy-outcome" data-hoop-id="${id}" data-outcome-idx="${oi}" title="Copy this cycle's stage logs">Copy logs</button>` +
            `<div class="${detailCls}">` +
            `<div class="hoop-outcome-head">${pills}</div>` +
            `<pre class="hoop-outcome-pre">${esc(detail)}</pre>` +
            (stageBlocks ? `<div class="hoop-stage-details">${stageBlocks}</div>` : "") +
            `</div></div>`;
        }).join("");
        const status = String(st.status || "idle");
        const badge = statusBadgeClass(status);
        const isRunning = badge === "running";
        const bias = st.hoop?.local_bias != null ? Number(st.hoop.local_bias).toFixed(2) : "--";
        const stageTags = (st.spec?.stages || [])
          .filter((s) => s.enabled !== false && !s.disabled)
          .map((s) => `<span class="stage-pill">${esc(s.kind)}</span>`).join(" ");
        const evalGoal = esc(st.spec?.eval?.goal || "");
        const score = st.last_eval_score != null ? Number(st.last_eval_score).toFixed(2) : "--";
        const prog = st.progress || {};
        const showProg = (isRunning || status === "waiting_human") && (prog.phase || prog.stage_kind || prog.current);
        const routeBits = [];
        if (prog.topology) routeBits.push(prog.topology);
        if ((prog.path_taken || []).length) routeBits.push("path " + prog.path_taken.length);
        if ((prog.next_edges || []).length) routeBits.push("next " + prog.next_edges.length);
        const progLine = showProg
          ? `<p class="hint hoop-progress">Cycle #${prog.iteration || st.iteration || "?"} | ${esc(prog.phase || "")} | ${esc(prog.stage_kind || prog.stage_id || prog.current || "")}${prog.note ? " | " + esc(String(prog.note).slice(0, 60)) : ""}${routeBits.length ? " | " + esc(routeBits.join(" / ")) : ""}</p>`
          : "";
        const gate = st.gate || {};
        const cursor = st.cursor || {};
        const hitlAsk =
          gate.ask ||
          [
            gate.reason || "approval required",
            cursor.critic_text && "--- CRITIC ---\n" + cursor.critic_text,
            cursor.actor_text && "--- ACTOR OUTPUT TO REVIEW ---\n" + cursor.actor_text,
            cursor.plan_text && "--- PLAN ---\n" + cursor.plan_text,
          ]
            .filter(Boolean)
            .join("\n\n");
        const hitlBox = status === "waiting_human"
          ? `<div class="hoop-hitl">
              <p class="hint"><strong>Waiting for human</strong> — ${esc((gate.reason || "approval required").slice(0, 160))}</p>
              <details class="hoop-hitl-ask" open>
                <summary>What the agent asks you to review</summary>
                <pre class="hoop-hitl-ask-pre">${esc(hitlAsk)}</pre>
              </details>
              <label class="span2" title="Optional note stored with the human gate decision">Comment <input type="text" class="hoop-hitl-comment" data-id="${id}" placeholder="optional note" title="Optional note stored with the human gate decision" /></label>
              <span class="hoop-actions">
                <button type="button" class="linkish hoop-approve" data-id="${id}">Approve + resume</button>
                <button type="button" class="linkish hoop-reject" data-id="${id}">Reject</button>
                <button type="button" class="linkish hoop-copy-hitl" data-id="${id}" title="Copy review text">Copy ask</button>
              </span>
            </div>`
          : "";
        let lastOut = "No cycles yet";
        let lastTip = lastOut;
        if (last) {
          const bit = last.success
            ? (last.summary || "ok")
            : (last.err || last.summary || "fail");
          const short = String(bit).replace(/\s+/g, " ").trim();
          lastOut =
            `#${last.iteration} ${last.success ? "ok" : "fail"}` +
            (last.route ? ` · ${last.route}` : "") +
            ` · ${last.latency_ms || 0}ms · ` +
            (short.length > 140 ? short.slice(0, 140) + "…" : short);
          lastTip = `#${last.iteration} ${last.success ? "ok" : "fail"} — full text in Outcomes below`;
        }
        const forceOpen =
          status === "running" ||
          status === "waiting_human" ||
          hoopFoldOpen(id);
        return `<details class="hoop-card fold-card ${isRunning ? "is-running" : ""} ${status === "waiting_human" ? "is-waiting" : ""}" data-id="${id}" data-status="${esc(status)}" ${forceOpen ? "open" : ""}>
          <summary class="hoop-card-head">
            <strong>${name}</strong>
            <span class="status-badge ${badge}">${esc(status)}</span>
            ${spendChipHTML(st.spend)}
            <span class="muted">bias ${bias}</span>
            <span class="muted">score ${score}</span>
            <span class="hoop-actions" onclick="event.preventDefault()">
              <button type="button" class="linkish hoop-edit-graph hoop-load-graph" data-id="${id}">Edit graph</button>
              <button type="button" class="linkish hoop-start" data-id="${id}">Start</button>
              <button type="button" class="linkish hoop-stop" data-id="${id}">Stop</button>
              <button type="button" class="linkish hoop-clear-results" data-id="${id}" title="Clear outcomes + agent log for this hoop">Clear results</button>
              <button type="button" class="linkish hoop-del" data-id="${id}">Delete</button>
            </span>
          </summary>
          <div class="hoop-card-body">
          ${progLine}
          ${hitlBox}
          <p class="hoop-last-outcome ${last && !last.success ? "is-error" : ""}" data-tip="${esc(lastTip)}">${esc(lastOut)}</p>
          <p class="hint" style="margin:8px 0">Goal: ${esc((st.spec?.goal || st.spec?.prompt || "").slice(0, 160))}</p>
          ${evalGoal ? `<p class="hint" style="margin:0 0 8px">Eval: ${evalGoal}</p>` : ""}
          <div class="hoop-stage-pills">${stageTags}</div>
          <div class="hoop-outcomes">${rows || `<span class="muted">No cycles yet -- start to run the pipeline</span>`}</div>
          </div>
        </details>`;
      }).join("");
      if (openNested.size) {
        el.querySelectorAll(".hoop-card").forEach((card) => {
          const hid = card.dataset.id || "";
          card.querySelectorAll(".hoop-stage-detail, .hoop-hitl-ask").forEach((d, i) => {
            const label = d.querySelector("summary")?.textContent?.slice(0, 48) || String(i);
            if (openNested.has(hid + "|" + d.className + "|" + label)) d.open = true;
          });
        });
      }
      el.querySelectorAll(".hoop-start").forEach((b) => b.addEventListener("click", (ev) => { ev.preventDefault(); ev.stopPropagation(); hoopAction(b.dataset.id, "start"); }));
      el.querySelectorAll(".hoop-stop").forEach((b) => b.addEventListener("click", (ev) => { ev.preventDefault(); ev.stopPropagation(); hoopAction(b.dataset.id, "stop"); }));
      el.querySelectorAll(".hoop-del").forEach((b) => b.addEventListener("click", (ev) => { ev.preventDefault(); ev.stopPropagation(); deleteHoop(b.dataset.id); }));
      el.querySelectorAll(".hoop-clear-results").forEach((b) => b.addEventListener("click", (ev) => { ev.preventDefault(); ev.stopPropagation(); clearHoopResults(b.dataset.id); }));
      el.querySelectorAll(".hoop-approve").forEach((b) => b.addEventListener("click", (ev) => { ev.preventDefault(); ev.stopPropagation(); hoopGate(b.dataset.id, true); }));
      el.querySelectorAll(".hoop-reject").forEach((b) => b.addEventListener("click", (ev) => { ev.preventDefault(); ev.stopPropagation(); hoopGate(b.dataset.id, false); }));
      el.querySelectorAll(".hoop-copy-outcome").forEach((b) => b.addEventListener("click", (ev) => {
        ev.preventDefault();
        ev.stopPropagation();
        const hid = b.dataset.hoopId;
        const idx = Number(b.dataset.outcomeIdx);
        const st = lastHoopsList.find((x) => x.spec?.id === hid);
        if (!st) {
          showHoopsError("Hoop not found for copy");
          return;
        }
        const outcomes = (st.outcomes || []).slice(-8).reverse();
        const o = outcomes[idx];
        if (!o) {
          showHoopsError("Outcome not found");
          return;
        }
        copyText(formatHoopOutcomePlain(st, o), "Copied cycle #" + o.iteration + " logs");
      }));
      el.querySelectorAll(".hoop-copy-hitl").forEach((b) => b.addEventListener("click", (ev) => {
        ev.preventDefault();
        ev.stopPropagation();
        const st = lastHoopsList.find((x) => x.spec?.id === b.dataset.id);
        const ask =
          st?.gate?.ask ||
          [
            st?.gate?.reason,
            st?.cursor?.critic_text && "--- CRITIC ---\n" + st.cursor.critic_text,
            st?.cursor?.actor_text && "--- ACTOR ---\n" + st.cursor.actor_text,
            st?.cursor?.plan_text && "--- PLAN ---\n" + st.cursor.plan_text,
          ]
            .filter(Boolean)
            .join("\n\n") ||
          "";
        copyText(ask || "(empty)", "Copied HITL ask");
      }));
      el.querySelectorAll(".hoop-card").forEach((card) => {
        card.addEventListener("toggle", () => {
          const hid = card.dataset.id;
          if (hid) setHoopFoldOpen(hid, card.open);
        });
        card.addEventListener("click", (ev) => {
          if (ev.target.closest("button")) return;
          if (ev.target.closest("summary")) {
            const hid = card.dataset.id;
            if (hid) setAgentLogFocus("hoop", hid);
            return;
          }
          const hid = card.dataset.id;
          if (hid) setAgentLogFocus("hoop", hid);
        });
      });
      updateAgentLogChrome();
      el.querySelectorAll(".hoop-edit-graph").forEach((b) => b.addEventListener("click", async (ev) => {
        ev.preventDefault();
        ev.stopPropagation();
        const res = await fetch(`/api/loops/${encodeURIComponent(b.dataset.id)}`);
        if (!res.ok) {
          showHoopsError(await res.text());
          return;
        }
        setAgentLogFocus("hoop", b.dataset.id);
        loadHoopIntoComposer(await res.json(), { openGraph: true });
      }));
    } catch (e) {
      el.innerHTML = `<p class="cfg-error">${esc(String(e))}</p>`;
    }
  }

  function hoopFoldOpen(id) {
    try {
      return localStorage.getItem("glider.hoop.open." + id) === "1";
    } catch (_) {
      return false;
    }
  }

  function setHoopFoldOpen(id, open) {
    try {
      if (open) localStorage.setItem("glider.hoop.open." + id, "1");
      else localStorage.removeItem("glider.hoop.open." + id);
    } catch (_) {}
  }

  function setAllHoopFolds(open) {
    document.querySelectorAll("#hoops-list .hoop-card").forEach((card) => {
      card.open = open;
      if (card.dataset.id) setHoopFoldOpen(card.dataset.id, open);
    });
  }

  async function clearHoopResults(id) {
    if (!id) return;
    if (!confirm("Clear outcomes and agent log for " + id + "?")) return;
    const res = await fetch("/api/loops/" + encodeURIComponent(id) + "/clear-results", { method: "POST" });
    if (!res.ok) {
      showHoopsError(await res.text());
      return;
    }
    if (agentLogFocus && agentLogFocus.scope === "hoop" && agentLogFocus.id === id) {
      clearAgentLogView();
    }
    showHoopsOk("Cleared results for " + id);
    lastHoopsSnap = "";
    await loadHoops();
  }

  async function clearAllHoopResults() {
    if (!confirm("Clear outcomes + agent logs for all stopped hoops (running/waiting skipped)?")) return;
    const res = await fetch("/api/loops/clear-all-results", { method: "POST" });
    const text = await res.text();
    if (!res.ok) {
      showHoopsError(text);
      return;
    }
    clearAgentLogView();
    let msg = "Cleared logs & results";
    try {
      const data = JSON.parse(text);
      msg = `Cleared ${data.cleared || 0} hoop(s)` +
        (data.skipped && data.skipped.length ? `; skipped running: ${data.skipped.join(", ")}` : "");
    } catch (_) {}
    showHoopsOk(msg);
    lastHoopsSnap = "";
    await loadHoops();
  }

  async function clearServerAgentLog(scope, id) {
    if (!scope) {
      await fetch("/api/agent-logs?all=1", { method: "DELETE" });
      return;
    }
    const q = id
      ? "/api/agent-logs?scope=" + encodeURIComponent(scope) + "&id=" + encodeURIComponent(id)
      : "/api/agent-logs?scope=" + encodeURIComponent(scope);
    await fetch(q, { method: "DELETE" });
  }

  async function hoopAction(id, action) {
    const res = await fetch("/api/loops/" + encodeURIComponent(id) + "/" + action, { method: "POST" });
    if (!res.ok) {
      showHoopsError(await res.text());
      return;
    }
    if (action === "start" || action === "resume") {
      setAgentLogFocus("hoop", id);
      try {
        const det = await fetch("/api/loops/" + encodeURIComponent(id));
        if (det.ok) {
          loadHoopIntoComposer(await det.json(), { openGraph: true });
        } else {
          openGraphEditor("stage");
        }
      } catch (_) {
        openGraphEditor("stage");
      }
      startLiveBoardPoll();
      startHoopLiveFastPoll();
    }
    showHoopsOk(action + " " + id);
    lastHoopsSnap = "";
    await loadHoops();
    if (isLiveLoopTabActive()) refreshLiveBoard();
  }

  let hoopLiveFastTimer = null;
  function startHoopLiveFastPoll() {
    if (hoopLiveFastTimer) clearInterval(hoopLiveFastTimer);
    let awayTicks = 0;
    let doneTicks = 0;
    hoopLiveFastTimer = setInterval(async () => {
      if (!isLiveLoopTabActive()) {
        awayTicks += 1;
        if (awayTicks > 25) {
          clearInterval(hoopLiveFastTimer);
          hoopLiveFastTimer = null;
          return;
        }
      } else {
        awayTicks = 0;
      }
      try {
        const res = await fetch("/api/loops");
        const list = await res.json();
        refreshStageLiveFromLoops(Array.isArray(list) ? list : []);
        const editId = document.getElementById("hoop-edit-id")?.value;
        const st = Array.isArray(list) && editId ? list.find((x) => x.spec?.id === editId) : null;
        const running = st && (st.status === "running" || st.status === "waiting_human");
        if (running) {
          doneTicks = 0;
          startCyLiveMotion();
          if (st.status === "waiting_human") {
            updateGraphsLiveBadge("WAITING for human · Approve / Reject below or on Hoops tab");
            paintGraphsHitl(st);
          } else {
            updateGraphsLiveBadge(
              `Hoop live · ${st.progress?.stage_kind || st.progress?.current || st.progress?.stage_id || "…"}`
            );
            paintGraphsHitl(null);
          }
          // Rail already updated via refreshStageLiveFromLoops → applyStageLiveFromHoop.
        } else {
          paintGraphsHitl(null);
          doneTicks += 1;
          if (doneTicks > 8) {
            clearInterval(hoopLiveFastTimer);
            hoopLiveFastTimer = null;
            if (!swarmLivePollTimer) updateGraphsLiveBadge("");
          }
        }
      } catch (_) {}
    }, 400);
  }

  function updateGraphsLiveBadge(text) {
    let el = document.getElementById("graphs-live-badge");
    if (!el) {
      const head = document.querySelector(".graphs-panel-head > div");
      if (!head) return;
      el = document.createElement("p");
      el.id = "graphs-live-badge";
      el.className = "graphs-live-badge";
      head.appendChild(el);
    }
    if (!text) {
      el.hidden = true;
      el.textContent = "";
      return;
    }
    el.hidden = false;
    el.textContent = text;
    el.classList.toggle("is-waiting", /waiting/i.test(text));
  }

  function paintGraphsHitl(st) {
    let box = document.getElementById("graphs-hitl");
    if (!st || String(st.status || "").toLowerCase() !== "waiting_human") {
      if (box) box.hidden = true;
      return;
    }
    const id = st.spec?.id || "";
    if (!box) {
      const head = document.querySelector(".graphs-panel-head");
      if (!head) return;
      box = document.createElement("div");
      box.id = "graphs-hitl";
      box.className = "hoop-hitl graphs-hitl";
      head.insertAdjacentElement("afterend", box);
    }
    box.hidden = false;
    const reason = st.gate?.reason || st.progress?.note || "approval required";
    const ask =
      st.gate?.ask ||
      [
        reason,
        st.cursor?.critic_text && "--- CRITIC ---\n" + st.cursor.critic_text,
        st.cursor?.actor_text && "--- ACTOR OUTPUT TO REVIEW ---\n" + st.cursor.actor_text,
        st.cursor?.plan_text && "--- PLAN ---\n" + st.cursor.plan_text,
      ]
        .filter(Boolean)
        .join("\n\n");
    const sig = id + "|" + reason + "|" + ask;
    if (box.dataset.hitlSig === sig) return;
    // Preserve comment + open ask while refreshing.
    const prevComment = box.querySelector(".hoop-hitl-comment")?.value || "";
    const askWasOpen = box.querySelector(".hoop-hitl-ask")?.open !== false;
    box.dataset.hitlSig = sig;
    box.innerHTML =
      `<strong>Human gate</strong> — ${esc(reason)}` +
      `<details class="hoop-hitl-ask"${askWasOpen ? " open" : ""}><summary>What to review</summary>` +
      `<pre class="hoop-hitl-ask-pre">${esc(ask)}</pre></details>` +
      `<label class="span2">Comment <input type="text" class="hoop-hitl-comment" data-id="${esc(id)}" placeholder="optional note" /></label>` +
      `<div class="cfg-actions">` +
      `<button type="button" class="linkish graphs-hitl-approve" data-id="${esc(id)}">Approve + resume</button>` +
      `<button type="button" class="linkish graphs-hitl-reject" data-id="${esc(id)}">Reject</button>` +
      `<button type="button" class="linkish graphs-hitl-copy" data-id="${esc(id)}">Copy ask</button>` +
      `</div>`;
    const commentInput = box.querySelector(".hoop-hitl-comment");
    if (commentInput) commentInput.value = prevComment;
    box.querySelector(".graphs-hitl-approve")?.addEventListener("click", () => hoopGate(id, true));
    box.querySelector(".graphs-hitl-reject")?.addEventListener("click", () => hoopGate(id, false));
    box.querySelector(".graphs-hitl-copy")?.addEventListener("click", () => copyText(ask, "Copied HITL ask"));
  }

  async function hoopGate(id, approve) {
    const input = document.querySelector(`.hoop-hitl-comment[data-id="${CSS.escape(id)}"]`);
    const comment = input ? String(input.value || "") : "";
    const action = approve ? "approve" : "reject";
    const res = await fetch("/api/loops/" + encodeURIComponent(id) + "/" + action, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ comment, resume: !!approve, actor: "dashboard" }),
    });
    if (!res.ok) {
      showHoopsError(await res.text());
      return;
    }
    showHoopsOk(action + " " + id);
    lastHoopsSnap = "";
    await loadHoops();
    if (isLiveLoopTabActive()) refreshLiveBoard();
  }

  async function deleteHoop(id) {
    const ok = await gliderConfirm("Delete hoop", "Delete hoop " + id + "? This cannot be undone.");
    if (!ok) return;
    const res = await fetch("/api/loops/" + encodeURIComponent(id), { method: "DELETE" });
    if (!res.ok && res.status !== 204) {
      showHoopsError(await res.text());
      return;
    }
    showHoopsOk("deleted " + id);
    lastHoopsSnap = "";
    await loadHoops();
  }

  function showHoopsError(msg) {
    const e = document.getElementById("hoops-error");
    const o = document.getElementById("hoops-ok");
    if (o) o.hidden = true;
    if (!e) return;
    if (!msg) { e.hidden = true; e.textContent = ""; return; }
    e.hidden = false;
    e.textContent = msg;
  }

  function showHoopsOk(msg) {
    const e = document.getElementById("hoops-error");
    const o = document.getElementById("hoops-ok");
    if (e) e.hidden = true;
    if (!o) return;
    o.hidden = false;
    o.textContent = msg || "OK";
  }

  const hoopForm = document.getElementById("hoop-form");
  if (hoopForm) {
    hoopForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      showHoopsError("");
      const route = document.getElementById("hoop-route").value || "local";
      const name = document.getElementById("hoop-name").value.trim();
      const stagesPayload = syncStagesJSON();
      if (!stagesPayload.length) {
        showHoopsError("Add at least one stage to the graph (click or drag from palette).");
        return;
      }
      const stages = stagesPayload.map((s) => {
        const row = {
          kind: coerceStageKind(s.kind),
          id: s.id || s.kind,
          name: s.name || s.kind,
          enabled: s.enabled !== false,
        };
        if (s.prompt) row.prompt = s.prompt;
        if (s.route) row.route = s.route;
        if (row.kind === "critic" && s.eval_min > 0) row.eval_min = s.eval_min;
        if (s.parallel > 1) row.parallel = Number(s.parallel) || 0;
        if (Array.isArray(s.roles) && s.roles.length) row.roles = s.roles;
        if (Array.isArray(s.tools) && s.tools.length) {
          row.tools = s.tools.filter((t) => t && t.name && t.name !== "*");
          // MCP server bindings without specific tools → keep "*" for runtime ExpandRefs.
          const wild = s.tools.filter((t) => t && t.kind === "mcp" && (t.name === "*" || !t.name));
          wild.forEach((t) => {
            if (t.server) row.tools.push({ name: "*", kind: "mcp", server: t.server });
          });
        }
        if (row.kind === "workspace") {
          row.workspace_mode = s.workspace_mode === "existing" ? "existing" : "run";
          if (s.workspace_path) row.workspace_path = s.workspace_path;
          if (s.out_path) row.out_path = s.out_path;
        }
        return row;
      });
      const graph_edges = stageEdges.map((e) => ({
        id: e.id,
        source: (stageNodes.find((n) => n.uid === e.source) || {}).id || e.source,
        target: (stageNodes.find((n) => n.uid === e.target) || {}).id || e.target,
        kind: e.kind || "flow",
      }));
      const evalGoal = (document.getElementById("hoop-eval-goal") || {}).value?.trim() || "";
      const maxIter = Number(document.getElementById("hoop-max-iter")?.value) || 0;
      const goal = document.getElementById("hoop-prompt").value.trim();
      const editId = (document.getElementById("hoop-edit-id")?.value || "").trim();
      const id =
        editId ||
        name.toLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-|-$/g, "") ||
        undefined;
      const body = {
        id,
        name,
        goal,
        prompt: goal,
        interval: document.getElementById("hoop-interval").value.trim() || "",
        route,
        learning: document.getElementById("hoop-learning").checked,
        stages,
        graph_edges,
        eval: { goal: evalGoal || goal, on_success_n: evalGoal ? 1 : 0, min_score: 0.7 },
        max_iterations: maxIter || 3,
        autonomy: "L1",
      };
      const url = editId ? `/api/loops/${encodeURIComponent(editId)}` : "/api/loops";
      const method = editId ? "PUT" : "POST";
      const res = await fetch(url, {
        method,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        showHoopsError(await res.text());
        return;
      }
      showHoopsOk(editId ? `Hoop updated (${editId})` : "Hoop created");
      if (!editId) {
        hoopForm.reset();
        setPipelineStages(DEFAULT_STAGES);
        document.getElementById("hoop-interval").value = "";
        document.getElementById("hoop-max-iter").value = "3";
        document.getElementById("hoop-route").value = "local";
      }
      clearHoopEditMode();
      if (editId) {
        // keep shared stageNodes in sync after update
        setPipelineStages(stages);
      }
      lastHoopsSnap = "";
      await loadHoops();
      if (isLiveLoopTabActive()) refreshLiveBoard();
    });
  }

  document.getElementById("hoop-cancel-edit")?.addEventListener("click", () => {
    clearHoopEditMode();
    setPipelineStages(DEFAULT_STAGES);
    stageLiveStatus = {};
    renderStageGraph();
    showHoopsOk("Edit cancelled");
  });
  const hoopsRefresh = document.getElementById("hoops-refresh");
  if (hoopsRefresh) hoopsRefresh.addEventListener("click", () => refreshHoopsPanel());
  document.getElementById("hoops-expand-all")?.addEventListener("click", () => setAllHoopFolds(true));
  document.getElementById("hoops-collapse-all")?.addEventListener("click", () => setAllHoopFolds(false));
  document.getElementById("hoops-clear-all-results")?.addEventListener("click", () => clearAllHoopResults());
  document.getElementById("swarm-clear-logs")?.addEventListener("click", async () => {
    if (!confirm("Clear all swarm agent logs?")) return;
    try {
      await clearServerAgentLog("swarm");
      if (agentLogFocus && agentLogFocus.scope === "swarm") clearAgentLogView();
      showHoopsOk("Cleared swarm logs");
    } catch (e) {
      showHoopsError(String(e));
    }
  });

  const loadSampleBtn = document.getElementById("hoops-load-sample");
  if (loadSampleBtn) {
    loadSampleBtn.addEventListener("click", () => {
      setPipelineStages(SAMPLE_STAGES);
      const nameEl = document.getElementById("hoop-name");
      const promptEl = document.getElementById("hoop-prompt");
      const evalEl = document.getElementById("hoop-eval-goal");
      if (nameEl && !nameEl.value.trim()) nameEl.value = "hello-critic";
      if (promptEl && !promptEl.value.trim()) {
        promptEl.value =
          "Write exactly one friendly sentence that greets the user and includes the lowercase word glider. No markdown, no bullet list.";
      }
      if (evalEl && !evalEl.value.trim()) {
        evalEl.value = 'Greeting must be one sentence and contain "glider".';
      }
      document.getElementById("hoop-route").value = "local";
      document.getElementById("hoop-max-iter").value = "2";
      showHoopsOk("Loaded hello-critic sample stages");
    });
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
          if (!res.ok) showHoopsError(await res.text());
          else {
            showHoopsOk(inp.dataset.mod + " " + (inp.checked ? "enabled" : "disabled"));
            lastHotswapSnap = "";
            await loadHotSwap();
            if (isLiveLoopTabActive()) refreshLiveBoard();
          }
        });
      });
    } catch (e) {
      el.innerHTML = '<p class="cfg-error">' + esc(String(e)) + '</p>';
    }
  }

  async function loadSwarmTemplates() {
    const el = document.getElementById("swarm-templates");
    if (!el) return;
    try {
      const res = await fetch("/api/swarm/templates");
      const list = await res.json();
      if (!Array.isArray(list) || !list.length) {
        el.innerHTML = `<p class="hint">No swarm templates in ~/.glider/hoops.</p>`;
        return;
      }
      el.innerHTML = list.map((t) =>
        `<details class="hoop-card fold-card" data-tpl="${esc(t.id)}">` +
        `<summary class="hoop-card-head"><strong>${esc(t.name || t.id)}</strong> ` +
        `<span class="tag">${t.enabled ? "on" : "off"}</span>` +
        `<span class="hoop-actions" onclick="event.preventDefault()">` +
        `<button type="button" class="linkish tpl-load-graph" data-id="${esc(t.id)}">Load threads</button>` +
        `</span></summary>` +
        `<div class="hoop-card-body">` +
        `<p class="hint">${esc(t.prompt || "")}</p>` +
        `<p class="muted">roles: ${esc((t.roles || []).join(", ") || "--")}</p>` +
        `</div></details>`
      ).join("");
      el.querySelectorAll(".tpl-load-graph").forEach((b) => {
        b.addEventListener("click", (ev) => {
          ev.preventDefault();
          ev.stopPropagation();
          const tpl = list.find((x) => x.id === b.dataset.id);
          if (!tpl) return;
          const roles = (tpl.roles || []).length ? tpl.roles : ["plan", "exec"];
          writeRolesInput(roles);
          setSwarmThreadsFromRoles(roles);
          if (tpl.prompt) document.getElementById("swarm-prompt").value = tpl.prompt;
          const workers = document.getElementById("swarm-workers");
          if (workers) workers.value = String(tpl.max_workers || Math.min(4, roles.length) || 2);
          const waves = document.getElementById("swarm-waves");
          if (waves && tpl.waves) waves.value = String(tpl.waves);
          const weave = document.getElementById("swarm-weave-policy");
          if (weave && tpl.weave_policy) weave.value = tpl.weave_policy;
          const route = document.getElementById("swarm-route");
          if (route && tpl.prefer_local) route.value = "local";
          showHoopsOk(`Loaded template ${tpl.name || tpl.id} into thread graph`);
        });
      });
    } catch (e) {
      el.innerHTML = `<p class="cfg-error">${esc(String(e))}</p>`;
    }
  }

  let swarmLivePollTimer = null;
  function stopSwarmLivePoll() {
    if (swarmLivePollTimer) {
      clearInterval(swarmLivePollTimer);
      swarmLivePollTimer = null;
    }
  }
  function startSwarmLivePoll(turnId) {
    stopSwarmLivePoll();
    const tick = async () => {
      try {
        const res = await fetch("/api/swarm/runs/" + encodeURIComponent(turnId) + "/progress");
        if (res.status === 404 || !res.ok) return;
        applySwarmLiveProgress(await res.json());
      } catch (_) {}
    };
    tick();
    swarmLivePollTimer = setInterval(tick, 400);
  }
  function applySwarmLiveProgress(data) {
    if (!data) return;
    const workers = Array.isArray(data.workers) ? data.workers : [];
    workers.forEach((w) => {
      const role = String(w.role || "").toLowerCase();
      const th = swarmThreads.find((t) => String(t.role || "").toLowerCase() === role);
      if (!th) return;
      if (w.status) th.status = w.status;
      if (w.summary) th.summary = w.summary;
      if (w.err) th.err = w.err;
    });
    const phase = data.phase || data.status || "running";
    const waveBit = data.waves > 1 ? ` wave ${(data.wave || 0) + 1}/${data.waves}` : "";
    const runningN = workers.filter((w) => w.status === "running").length;
    const doneN = workers.filter((w) => w.status === "ok" || w.status === "fail").length;
    if (data.status === "completed" || data.status === "failed") {
      updateGraphsLiveBadge("");
    } else {
      updateGraphsLiveBadge(
        `Swarm live · ${phase}${waveBit} · ${runningN} running · ${doneN}/${workers.length || "?"} done`
      );
    }
    renderSwarmGraph();
  }

  const swarmForm = document.getElementById("swarm-form");
  if (swarmForm) {
    swarmForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const roles = rolesFromInput();
      setSwarmThreadsFromRoles(roles);
      swarmThreads.forEach((t) => {
        t.status = "pending";
        t.summary = "";
        t.err = "";
      });
      renderSwarmGraph();
      openGraphEditor("swarm");
      const turnId =
        "swarm-" +
        (typeof crypto !== "undefined" && crypto.randomUUID
          ? crypto.randomUUID()
          : String(Date.now()) + "-" + Math.random().toString(16).slice(2));
      lastSwarmRunId = turnId;
      setAgentLogFocus("swarm", turnId);
      startSwarmLivePoll(turnId);
      updateGraphsLiveBadge("Swarm live · fan-out…");
      const body = {
        turn_id: turnId,
        prompt: document.getElementById("swarm-prompt").value.trim(),
        roles,
        max_workers: Number(document.getElementById("swarm-workers").value) || 2,
        prefer_local: (document.getElementById("swarm-route")?.value || "local") === "local",
        route: document.getElementById("swarm-route")?.value || "local",
        waves: Number(document.getElementById("swarm-waves")?.value) || 1,
        weave_policy: document.getElementById("swarm-weave-policy")?.value || "critic",
        decompose: !!document.getElementById("swarm-decompose")?.checked,
        free_spawn: !!document.getElementById("swarm-free-spawn")?.checked,
      };
      let res;
      try {
        res = await fetch("/api/swarm/run", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
      } finally {
        stopSwarmLivePoll();
      }
      const text = await res.text();
      if (!res.ok) {
        paintSwarmRunInspect(null, text);
        swarmThreads.forEach((t) => {
          t.status = "fail";
          t.err = text.slice(0, 4000);
        });
        renderSwarmGraph();
        updateGraphsLiveBadge("");
        showHoopsError(text);
        return;
      }
      try {
        const data = JSON.parse(text);
        paintSwarmRunInspect(data, text);
        if (data.turn_id) {
          lastSwarmRunId = data.turn_id;
          setAgentLogFocus("swarm", data.thread_id || data.turn_id);
        }
        if (data.thread_id || data.waves) {
          const orch = swarmThreads.find((t) => t.uid === "orch");
          if (orch) {
            orch.status = "ok";
            orch.summary = (data.weave_policy || "weave") + " waves=" + (data.waves || 1);
          }
        }
        const results = data.results || [];
        const prog = data.progress || {};
        swarmThreads.forEach((t, i) => {
          const r = results.find((x) => String(x.role || "").toLowerCase() === t.role) || results[i];
          if (!r) {
            t.status = "ok";
            return;
          }
          t.status = r.err ? "fail" : "ok";
          t.summary = r.summary || r.episode?.summary || "";
          t.err = r.err || "";
        });
        if (prog.merge_failed && data.summary) {
          /* inspect already has full summary */
        }
      } catch (_) {
        paintSwarmRunInspect(null, text);
        swarmThreads.forEach((t) => {
          t.status = "ok";
        });
      }
      renderSwarmGraph();
      updateGraphsLiveBadge("");
      showHoopsOk("Swarm finished");
      refreshSwarmThreads();
    });
  }

  async function refreshSwarmThreads() {
    const el = document.getElementById("swarm-threads");
    if (!el) return;
    try {
      const res = await fetch("/api/swarm/threads");
      const list = await res.json();
      if (!Array.isArray(list) || !list.length) {
        el.innerHTML = `<p class="hint">No durable threads yet. Run with Waves &gt; 1.</p>`;
        return;
      }
      el.innerHTML = list.map((t) =>
        `<details class="hoop-card fold-card" data-thread="${esc(t.id)}">` +
        `<summary class="hoop-card-head"><strong>${esc(t.id)}</strong> ` +
        `<span class="tag">${esc(t.status || "?")}</span>` +
        `<span class="hoop-actions" onclick="event.preventDefault()">` +
        `<button type="button" class="linkish thr-resume" data-id="${esc(t.id)}">Resume</button>` +
        `<button type="button" class="linkish thr-view" data-id="${esc(t.id)}">View</button>` +
        `</span></summary>` +
        `<div class="hoop-card-body">` +
        `<p class="hint">waves=${t.wave_count || 0} policy=${esc(t.weave_policy || "-")}</p>` +
        `<pre class="hoop-stage-pre">${esc(t.merged_summary || t.goal || "(empty)")}</pre>` +
        `</div></details>`
      ).join("");
      el.querySelectorAll(".thr-resume").forEach((b) => {
        b.addEventListener("click", async (ev) => {
          ev.preventDefault();
          ev.stopPropagation();
          const id = b.dataset.id;
          try {
            const res = await fetch("/api/swarm/threads/" + encodeURIComponent(id) + "/resume", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ waves: 1 }),
            });
            const text = await res.text();
            try {
              const data = JSON.parse(text);
              paintSwarmRunInspect(data, text);
              if (data.thread_id || data.turn_id) {
                setAgentLogFocus("swarm", data.thread_id || data.turn_id);
              }
            } catch (_) {
              paintSwarmRunInspect(null, text);
            }
            if (!res.ok) { showHoopsError(text); return; }
            showHoopsOk("Resumed thread " + id);
            refreshSwarmThreads();
          } catch (e) {
            showHoopsError(String(e));
          }
        });
      });
      el.querySelectorAll(".thr-view").forEach((b) => {
        b.addEventListener("click", async (ev) => {
          ev.preventDefault();
          ev.stopPropagation();
          const res = await fetch("/api/swarm/threads/" + encodeURIComponent(b.dataset.id));
          const text = await res.text();
          setAgentLogFocus("swarm", b.dataset.id);
          try {
            const st = JSON.parse(text);
            paintSwarmRunInspect(
              {
                summary: st.merged_summary || st.merged?.summary || "",
                episode: st.merged,
                results: (st.waves || []).flatMap((wv) => wv.results || []),
                waves: (st.waves || []).length,
                weave_policy: st.weave_policy,
                turn_id: st.turn_id,
                thread_id: st.id,
              },
              text
            );
            if (st && Array.isArray(st.waves)) {
              paintWaveTimeline(st);
            }
          } catch (_) {
            paintSwarmRunInspect(null, text);
          }
        });
      });
    } catch (e) {
      el.innerHTML = `<p class="cfg-error">${esc(String(e))}</p>`;
    }
  }
  document.getElementById("swarm-threads-refresh")?.addEventListener("click", () => refreshSwarmThreads());
  document.getElementById("swarm-timeline-clear")?.addEventListener("click", () => clearWaveTimeline());
  refreshSwarmThreads();

  const tplForm = document.getElementById("tpl-form");
  if (tplForm) {
    tplForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const body = {
        id: document.getElementById("tpl-id").value.trim(),
        name: document.getElementById("tpl-name").value.trim(),
        prompt: document.getElementById("tpl-prompt").value.trim(),
        roles: document.getElementById("tpl-roles").value.split(",").map((s) => s.trim()).filter(Boolean),
        prefer_local: document.getElementById("tpl-local").checked,
        enabled: true,
        max_workers: 2,
      };
      const res = await fetch("/api/swarm/templates", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        showHoopsError(await res.text());
        return;
      }
      showHoopsOk("Template saved");
      tplForm.reset();
      document.getElementById("tpl-local").checked = true;
      document.getElementById("tpl-roles").value = "plan,exec";
      await loadSwarmTemplates();
    });
  }

  initGraphEditorNav();
  initStageDnD();

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
        if (isLiveLoopTabActive()) refreshLiveBoard();
      }
      if (msg.type === "agent_log") {
        const payload = msg.data && typeof msg.data === "object" ? msg.data : msg;
        appendLiveAgentLog(payload);
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
    return playgroundInfo.vendors[0] || "agy";
  }

  const PLAYGROUND_LESSONS = [
    {
      id: "delegate-run",
      title: "1. Delegate a task",
      goal: "Hand a task to another CLI. The flag has to be the very last thing in your message — a leading \"/name\" gets swallowed by the CLI's own slash-command handling before Glider ever sees it.",
      hint: () => `<your prompt> /${playgroundVendorExample()}`,
      example: () => `summarize recent commits /${playgroundVendorExample()}`,
      check: (r) => r.delegate.matched && r.delegate.kind === "run",
    },
    {
      id: "delegate-template",
      title: "2. Pick a named template",
      goal: "Some CLIs have more than one launch shape (e.g. a fully interactive one). Add \":template-name\" right after the vendor name.",
      hint: () => `<your prompt> /${playgroundVendorExample()}:interactive`,
      example: () => `fix the auth bug /${playgroundVendorExample()}:interactive`,
      check: (r) => r.delegate.matched && r.delegate.template && r.delegate.template !== "default",
    },
    {
      id: "workspace",
      title: "3. Set your workspace",
      goal: "A delegated CLI needs to know which real folder to run in. \"/workspace\" is not vendor-specific — one flag, a property of your own session.",
      hint: () => `<path> /workspace`,
      example: () => `. /workspace`,
      check: (r) => r.workspace.matched,
    },
    {
      id: "permission",
      title: "4. Answer a permission prompt",
      goal: "When a delegated CLI needs your OK mid-task, it hands back a short token in its reply. Send that token back with \":allow\" or \":deny\".",
      hint: () => `<token> /${playgroundVendorExample()}:allow`,
      example: () => `abc123 /${playgroundVendorExample()}:allow`,
      check: (r) => r.delegate.matched && (r.delegate.kind === "allow" || r.delegate.kind === "deny"),
    },
    {
      id: "routing",
      title: "5. Force local or cloud routing",
      goal: "A different subsystem from the delegate flags above — this matches anywhere in the message, not just at the end, and the exact words are configured on the Rules Engine tab, not fixed.",
      hint: () => (playgroundInfo.routingCommands.length
        ? `${playgroundInfo.routingCommands[0]} <your message>`
        : "(no routing override commands configured yet — add one on Rules Engine, or skip this one)"),
      example: () => (playgroundInfo.routingCommands.length ? `${playgroundInfo.routingCommands[0]} keep this on-device` : ""),
      check: (r) => r.routing.matched,
    },
  ];

  const PLAYGROUND_REFERENCE = [
    {
      family: "Delegate",
      syntax: "<prompt> /vendor[:template]",
      body: "Runs prompt headlessly against another CLI (or opens it interactively, for an \"interactive\"-mode template) and relays the answer back into this chat. Must be the trailing token — a leading \"/vendor\" is eaten by the front CLI's own slash-command handling first.",
    },
    {
      family: "Workspace",
      syntax: "<path> /workspace",
      body: "Records which real folder a delegated CLI should run in for this session. Not vendor-specific — one flag, used by every delegate call from this origin.",
    },
    {
      family: "Permission grant / deny",
      syntax: "<token> /vendor:allow   or   <token> /vendor:deny",
      body: "Answers a pending permission prompt a delegated CLI raised mid-task. The token identifies which run and which vendor — it's issued to you in the denial message itself, never typed from scratch.",
    },
    {
      family: "Routing override",
      syntax: "/local, /fast, /cloud, /heavy (configurable)",
      body: "Forces this turn's model target; matches anywhere in the message, not just at the end — a different subsystem from the three above (request routing, not CLI delegation). The exact words are config, not fixed: see the Rules Engine tab.",
    },
    {
      family: "Script-triggered rules",
      syntax: "whatever a .star rule looks for",
      body: "Rules Engine rules with a script trigger can match any phrase your own Starlark code checks for (e.g. \"/swarm\"). Not a fixed grammar, so this playground can't classify against it directly — check the Rules Engine tab for what's configured.",
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
