(() => {
  const panels = {
    overview: document.getElementById("panel-overview"),
    vram: document.getElementById("panel-vram"),
    rules: document.getElementById("panel-rules"),
    hoops: document.getElementById("panel-hoops"),
    graphs: document.getElementById("panel-graphs"),
    mcp: document.getElementById("panel-mcp"),
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
    document.body.classList.toggle("graphs-tab-active", name === "graphs");
    if (name === "vram") loadVRAM();
    if (name === "rules") renderRulesEditor(currentCfg);
    if (name === "hoops") {
      refreshHoopsPanel();
      startLiveBoardPoll();
    } else     if (name === "graphs") {
      refreshStageLiveThenRender();
      startLiveBoardPoll();
      const focusGraphs = () => {
        renderStageGraph();
        renderSwarmGraph();
        resizeCyInstances({ fit: true });
        const focus = opts && opts.focus;
        if (focus === "swarm") {
          document.getElementById("graphs-swarm-section")?.scrollIntoView({ behavior: "smooth", block: "start" });
        } else if (focus === "stage") {
          document.getElementById("graphs-stage-section")?.scrollIntoView({ behavior: "smooth", block: "nearest" });
        }
      };
      requestAnimationFrame(() =>
        requestAnimationFrame(() => {
          focusGraphs();
          setTimeout(() => resizeCyInstances({ fit: true }), 60);
        })
      );
    } else if (prev === "hoops" || prev === "graphs") {
      if (name !== "hoops" && name !== "graphs") stopLiveBoardPoll();
    }
    if (name === "overview") loadSessions();
    if (name === "mcp") refreshMCPPanel();
  }

  document.querySelectorAll(".tab").forEach((btn) => {
    btn.addEventListener("click", () => activateTab(btn.dataset.tab));
  });

  document.querySelectorAll("[data-goto-tab]").forEach((el) => {
    el.addEventListener("click", () => activateTab(el.getAttribute("data-goto-tab")));
  });

  const logEl = document.getElementById("request-log");

  function tipAttrs(el) {
    // native title tooltips already on labels; keep helper for dynamic nodes
    return el;
  }

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
    return action === "local" || action === "cloud" || action === "origin_passthrough" || action === "canned" || action === "error";
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
    document.getElementById("cfg-dash-auth").checked = !!cfg.dashboard?.auth;

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
        auth: document.getElementById("cfg-dash-auth").checked,
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
          <label title="Human-readable rule name">Name<input data-f="name" value="${esc(r.name || "")}" /></label>
          <label title="Higher priority is evaluated first">Priority<input data-f="priority" type="number" value="${r.priority ?? 0}" /></label>
          <label class="check" title="Disabled rules stay in config but are skipped by the router"><input data-f="enabled" type="checkbox" ${ruleEnabled(r) ? "checked" : ""}/> Enabled</label>
          <button type="button" class="linkish rule-del" title="Remove this rule">Remove</button>
        </div>
        <div class="rule-card-grid">
          <label title="explicit: /local /cloud commands | context_size: token threshold | script: Starlark file | always: default fallback | regex: pattern match">Trigger type
            <select data-f="trigger.type">
              ${opt("explicit", trig.type)}
              ${opt("context_size", trig.type)}
              ${opt("script", trig.type)}
              ${opt("always", trig.type)}
              ${opt("regex", trig.type)}
            </select>
          </label>
          <label title="Comma-separated commands for explicit triggers (e.g. /local,/fast)">Commands<input data-f="trigger.commands" value="${esc((trig.commands || []).join(", "))}" /></label>
          <label title="Regex pattern (trigger type regex)">Pattern<input data-f="trigger.pattern" value="${esc(trig.pattern || "")}" /></label>
          <label title="Starlark script path relative to process cwd">Script file<input data-f="trigger.file" value="${esc(trig.file || "")}" /></label>
          <label title="Comparison operator for context_size (>, >=, <, <=, ==)">Operator<input data-f="trigger.operator" value="${esc(trig.operator || "")}" /></label>
          <label title="Token count threshold for context_size rules">Value<input data-f="trigger.value" type="number" value="${trig.value ?? 0}" /></label>
          <label title="local = Ollama/vLLM | cloud = BYOK (gateway) or origin passthrough (MITM)">Action target
            <select data-f="action.target">
              ${opt("local", act.target)}
              ${opt("cloud", act.target)}
            </select>
          </label>
          <label title="Optional backend name for cloud actions">Backend<input data-f="action.backend" value="${esc(act.backend || "")}" /></label>
          <label title="Model to use when this rule matches">Model<input data-f="action.model" value="${esc(act.model || "")}" /></label>
          <label title="Optional LoRA adapter name">Adapter<input data-f="action.adapter" value="${esc(act.adapter || "")}" /></label>
        </div>`;
      root.appendChild(card);
      tipAttrs(card);
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

  function esc(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/"/g, "&quot;")
      .replace(/</g, "&lt;");
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
  const STAGE_KINDS = ["router", "planner", "actor", "critic", "memory", "human_gate"];
  let liveBoardTimer = null;
  let liveWsConnected = false;
  let hotswapGenCache = {};
  let stageDnd = { kind: null, from: null, index: -1, uid: null };
  let lastHoopsSnap = "";
  let lastHotswapSnap = "";
  let stageNodes = [];
  /** @type {{id:string,source:string,target:string,kind:string}[]} */
  let stageEdges = [];
  let stageSelectedUid = null;
  let stageSelectedEdgeId = null;
  let stageLiveStatus = {};
  let stageLivePath = []; // node ids/kinds on DecisionRoute path
  let stageLiveEdges = {}; // edge id -> ok|running|next|fail
  let stageLiveCurrent = "";
  let stageUidSeq = 0;
  let swarmThreads = [];
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
    }
    updateAgentLogChrome();
    refreshAgentLogPanels();
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

  function agentLogRowHTML(e) {
    const t = e.at ? new Date(e.at).toLocaleTimeString() : "--";
    const lvl = (e.level || "info").toUpperCase();
    const stage = agentLogStageLabel(e);
    const cls = e.level === "error" ? "error" : e.level === "warn" ? "warn" : "";
    return (
      '<div class="log-row ' +
      cls +
      '">' +
      '<span class="log-time">' +
      esc(t) +
      "</span>" +
      '<span class="log-level">' +
      esc(lvl) +
      "</span>" +
      '<span class="log-stage" title="' +
      esc(stage) +
      '">' +
      esc(stage) +
      "</span>" +
      '<span class="log-msg">' +
      esc(e.message || "") +
      "</span>" +
      "</div>"
    );
  }

  function agentLogRowElement(e) {
    const div = document.createElement("div");
    const cls = e.level === "error" ? "error" : e.level === "warn" ? "warn" : "";
    div.className = "log-row" + (cls ? " " + cls : "");
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
    div.appendChild(t);
    div.appendChild(lvl);
    div.appendChild(stage);
    div.appendChild(msg);
    return div;
  }

  function renderAgentLogPanels(entries) {
    agentLogViewLines = Array.isArray(entries) ? entries.slice() : [];
    const html =
      !agentLogViewLines.length
        ? '<p class="log-empty">No log lines for this instance yet. Start a hoop or run a swarm while following it.</p>'
        : agentLogViewLines.map(agentLogRowHTML).join("");
    const panels = [
      document.getElementById("agent-log-panel"),
      document.getElementById("hoops-agent-log-panel"),
    ];
    panels.forEach((a) => {
      if (!a) return;
      a.innerHTML = html;
      if (agentLogAutoScroll) a.scrollTop = a.scrollHeight;
    });
  }

  async function refreshAgentLogPanels() {
    if (!agentLogFocus) {
      renderAgentLogPanels([]);
      return;
    }
    try {
      const q =
        "/api/agent-logs?scope=" +
        encodeURIComponent(agentLogFocus.scope) +
        "&id=" +
        encodeURIComponent(agentLogFocus.id) +
        "&limit=200";
      const res = await fetch(q);
      const data = await res.json();
      renderAgentLogPanels(Array.isArray(data.entries) ? data.entries : []);
    } catch (_) {
      renderAgentLogPanels([]);
    }
  }

  function appendLiveAgentLog(e) {
    if (!agentLogFocus || !e) return;
    if (e.scope !== agentLogFocus.scope || e.instance_id !== agentLogFocus.id) return;
    if (agentLogPaused) return;
    agentLogViewLines.push(e);
    if (agentLogViewLines.length > 400) agentLogViewLines = agentLogViewLines.slice(-400);
    const panels = [
      document.getElementById("agent-log-panel"),
      document.getElementById("hoops-agent-log-panel"),
    ];
    panels.forEach((panel) => {
      if (!panel) return;
      const empty = panel.querySelector(".log-empty");
      if (empty) empty.remove();
      panel.appendChild(agentLogRowElement(e));
      if (agentLogAutoScroll) panel.scrollTop = panel.scrollHeight;
    });
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
    renderAgentLogPanels([]);
  }

  function initAgentLogUI() {
    const refresh = () => refreshAgentLogPanels();
    const clear = () => clearAgentLogView();
    const togglePause = () => setAgentLogPaused(!agentLogPaused);
    document.getElementById("agent-log-refresh")?.addEventListener("click", refresh);
    document.getElementById("hoops-agent-log-refresh")?.addEventListener("click", refresh);
    document.getElementById("agent-log-clear-view")?.addEventListener("click", clear);
    document.getElementById("hoops-agent-log-clear")?.addEventListener("click", clear);
    document.getElementById("agent-log-pause")?.addEventListener("click", togglePause);
    document.getElementById("hoops-agent-log-pause")?.addEventListener("click", togglePause);
    ["agent-log-panel", "hoops-agent-log-panel"].forEach((id) => {
      const panel = document.getElementById(id);
      if (!panel) return;
      panel.addEventListener("scroll", () => {
        const nearBottom = panel.scrollHeight - panel.scrollTop - panel.clientHeight < 40;
        agentLogAutoScroll = nearBottom;
      });
    });
    updateAgentLogChrome();
    renderAgentLogPanels([]);
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
        "border-color": "#2563eb",
        "font-size": 10,
      },
    },
    {
      selector: "node:selected",
      style: {
        "border-width": 2.5,
        "border-color": "#2563eb",
        "background-color": "#eff6ff",
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
      selector: "node.status-running",
      style: { "border-color": "#2563eb", "border-width": 2.5 },
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
      selector: "node.route-current",
      style: { "border-color": "#2563eb", "border-width": 3, "background-color": "#dbeafe" },
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
        "background-color": "#2563eb",
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
        "line-color": "#2563eb",
        "target-arrow-color": "#2563eb",
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
      style: { "line-color": "#16a34a", "target-arrow-color": "#16a34a", width: 2.2 },
    },
    {
      selector: "edge.status-fail",
      style: { "line-color": "#b91c1c", "target-arrow-color": "#b91c1c" },
    },
    {
      selector: "edge.status-running, edge.route-next",
      style: { "line-color": "#2563eb", "target-arrow-color": "#2563eb", width: 2.2 },
    },
    {
      selector: ".eh-preview, .eh-ghost-edge",
      style: {
        "line-color": "#2563eb",
        "target-arrow-color": "#2563eb",
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
      await Promise.all([loadHoops(), loadHotSwap()]);
    }
  }

  async function refreshLiveBoard() {
    setLiveWs(liveWsConnected ? "Connected" : "Disconnected", liveWsConnected);
    const [loopsRes, modsRes, metricsRes, ctxRes] = await Promise.allSettled([
      fetch("/api/loops").then((r) => (r.ok ? r.json() : [])),
      fetch("/api/hotswap/modules").then((r) => (r.ok ? r.json() : {})),
      fetch("/api/metrics").then((r) => (r.ok ? r.json() : null)),
      fetch("/api/context/recent?limit=1").then((r) => (r.ok ? r.json() : null)),
    ]);

    const loops = loopsRes.status === "fulfilled" && Array.isArray(loopsRes.value) ? loopsRes.value : [];
    refreshStageLiveFromLoops(loops);
    const set = (id, v) => {
      const el = document.getElementById(id);
      if (el) el.textContent = v;
    };
    set("live-hoops", String(loops.length));
    const running = loops.filter((st) => String(st.status || "").toLowerCase() === "running").length;
    set("live-running", String(running));

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
        const tgt = stageNodes.find((n) => n.uid === e.target);
        const st = stageLiveStatus[tgt?.id] || stageLiveStatus[tgt?.kind] || "idle";
        const routeSt = stageLiveEdges[e.id] || "";
        const kindClass = e.kind && e.kind !== "flow" ? e.kind : "";
        const routeClass = routeSt === "taken" ? "route-taken" : routeSt === "next" ? "route-next" : "";
        const classes = [kindClass, "status-" + st, routeClass].filter(Boolean).join(" ");
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

  function upsertStageEdge(source, target, kind) {
    pushStageHistory();
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

  function removeStageEdgeById(eid) {
    pushStageHistory();
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
    const cycle = ["flow", "feedback", "on_fail", "escalate", "conditional", "budget_exceeded", "parallel", "merge"];
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
      stageCy.on("dragfree", "node", (ev) => {
        if (ev.target.hasClass("eh-handle")) return;
        const node = stageNodes.find((n) => n.uid === ev.target.id());
        if (!node) return;
        const p = ev.target.position();
        node.x = p.x;
        node.y = p.y;
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
      ? `<span class="live-value ok">configured</span> (<code>${escapeHtml(gh.token_env || "GITHUB_*")}</code>)`
      : `<span class="live-value bad">missing</span> — set <code>GITHUB_PERSONAL_ACCESS_TOKEN</code> / <code>GITHUB_TOKEN</code> / <code>GH_TOKEN</code>`;
    const http = gh.http_connected ? `<span class="live-value ok">connected</span>` : `<span class="live-value bad">disconnected</span>`;
    const stdio = gh.stdio_connected ? `<span class="live-value ok">connected</span>` : `<span class="live-value">idle</span>`;
    el.innerHTML = `
      <div class="mcp-github-grid">
        <div><span class="live-label">Token</span>${tok}</div>
        <div><span class="live-label">HTTP (github)</span>${http}</div>
        <div><span class="live-label">Stdio (github-stdio)</span>${stdio}</div>
        <div><span class="live-label">Endpoint</span><code>${escapeHtml(gh.remote_url || "")}</code></div>
      </div>`;
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
      const tok = s.token_configured ? "yes" : (s.token_env ? "no" : "—");
      const active = s.id === mcpSelectedServerId ? " mcp-row-active" : "";
      return `<tr class="mcp-server-row${active}" data-mcp-id="${escapeAttr(s.id)}">
        <td><code>${escapeHtml(s.id)}</code><div class="graph-hint">${escapeHtml(s.name || "")}</div></td>
        <td>${escapeHtml(s.transport || "—")}</td>
        <td>${health}</td>
        <td>${s.tool_count != null ? s.tool_count : "—"}</td>
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
    list.innerHTML = "<p class=\"hint\">Loading…</p>";
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
        hint.textContent = src === "live"
          ? "Live tools from connected server."
          : src === "catalog"
            ? "Documented catalog (server not connected — connect for live list)."
            : "Tools";
      }
      if (!tools.length) {
        list.innerHTML = "<p class=\"hint\">No tools.</p>";
        return;
      }
      list.innerHTML = tools.map((t) => `
        <div class="mcp-tool-row">
          <code>${escapeHtml(t.name)}</code>
          <span class="hint">${escapeHtml(t.description || "")}</span>
        </div>`).join("");
    } catch (err) {
      list.innerHTML = `<p class="cfg-error">${escapeHtml(String(err.message || err))}</p>`;
    }
  }

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
      return `<label class="mcp-check"><input type="checkbox" data-mcp-server="${escapeAttr(s.id)}" ${checked} /> <code>${escapeHtml(s.id)}</code> <span class="hint">${escapeHtml(s.name || "")}</span></label>`;
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
    toolsEl.innerHTML = "<p class=\"hint\">Loading tools…</p>";
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
        blocks.push(`<div class="mcp-tool-group"><strong>${escapeHtml(sid)}</strong><p class="hint">No tools listed — leave unchecked to bind all.</p></div>`);
        continue;
      }
      const checks = tools.map((t) => {
        const key = `${sid}:${t.name}`;
        const checked = stageEditMcpToolNames.has(key) ? "checked" : "";
        return `<label class="mcp-check"><input type="checkbox" data-mcp-tool-server="${escapeAttr(sid)}" data-mcp-tool-name="${escapeAttr(t.name)}" ${checked} /> <code>${escapeHtml(t.name)}</code></label>`;
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
      selectStageNode(node.uid);
      renderStageGraph();
      showHoopsOk("Updated stage: " + node.id);
    } else {
      // rest of applyStageEditForm continues below — keep existing branch
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
      upsertStageEdge(prev.uid, node.uid, "flow");
    }
    if (next) upsertStageEdge(node.uid, next.uid, "flow");
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
    if (prev && next) upsertStageEdge(prev.uid, next.uid, "flow");
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
      if (prev) upsertStageEdge(prev.uid, node.uid, "flow");
      if (next) upsertStageEdge(node.uid, next.uid, "flow");
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
    showHoopsOk("Loaded " + (spec.name || spec.id) + " -- edit graph then Update");
    if (opts && opts.openGraph) {
      openGraphEditor("stage");
    } else {
      document.getElementById("hoop-form")?.scrollIntoView({ behavior: "smooth", block: "start" });
    }
  }

  function applyStageLiveFromHoop(st) {
    stageLiveStatus = {};
    stageLivePath = [];
    stageLiveEdges = {};
    stageLiveCurrent = "";
    const status = String(st.status || "").toLowerCase();
    const last = (st.outcomes || []).length ? st.outcomes[st.outcomes.length - 1] : null;
    const prog = st.progress || {};
    stageLiveCurrent = prog.current || prog.stage_id || prog.stage_kind || "";
    if (Array.isArray(prog.path_taken)) stageLivePath = prog.path_taken.slice();
    (prog.edges_taken || []).forEach((eid) => { stageLiveEdges[eid] = "taken"; });
    (prog.next_edges || []).forEach((eid) => { stageLiveEdges[eid] = "next"; });
    // Also match edges by source->target when ids differ from canvas.
    (prog.branch_choices || []).forEach((b) => {
      if (b.selected && b.edge_id) stageLiveEdges[b.edge_id] = "taken";
      else if (b.edge_id && !stageLiveEdges[b.edge_id]) stageLiveEdges[b.edge_id] = "next";
    });
    if (status === "running" || status === "waiting_human") {
      (st.spec?.stages || []).forEach((s) => {
        if (s.enabled === false || s.disabled) return;
        stageLiveStatus[s.kind] = "pending";
        if (s.id) stageLiveStatus[s.id] = "pending";
      });
      (stageLivePath || []).forEach((id) => {
        stageLiveStatus[id] = "ok";
      });
      const cur = prog.stage_kind || prog.stage_id || stageLiveCurrent;
      if (cur) {
        const paint = status === "waiting_human" ? "waiting" : "running";
        stageLiveStatus[cur] = paint;
        if (prog.stage_id) stageLiveStatus[prog.stage_id] = paint;
      }
    } else if (last?.stages?.length) {
      last.stages.forEach((s) => {
        stageLiveStatus[s.kind] = s.success ? "ok" : "fail";
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
  }

  function refreshStageLiveFromLoops(list) {
    const editId = document.getElementById("hoop-edit-id")?.value;
    if (!editId || !Array.isArray(list)) return;
    const st = list.find((x) => x.spec?.id === editId);
    if (!st) return;
    const before = JSON.stringify(stageLiveStatus);
    applyStageLiveFromHoop(st);
    if (JSON.stringify(stageLiveStatus) !== before) renderStageGraph();
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
  }

  function buildSwarmCyElements() {
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
      swarmCy.on("dragfree", "node", (ev) => {
        if (ev.target.hasClass("eh-handle") || ev.target.id() === "orch") return;
        const th = swarmThreads.find((t) => t.uid === ev.target.id());
        if (!th) return;
        const p = ev.target.position();
        th.x = p.x;
        th.y = p.y;
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
          (th.summary ? " | " + th.summary.slice(0, 80) : "");
      });
      swarmCy.on("mouseout", "node", () => {
        host.title = "";
      });
      swarmEh = makeEdgehandles(swarmCy, (source, target) => {
        let threadId = null;
        if (source === "orch" && target !== "orch") threadId = target;
        else if (target === "orch" && source !== "orch") threadId = source;
        else if (source !== "orch" && target !== "orch") {
          if (!swarmLinks.includes(source)) swarmLinks.push(source);
          if (!swarmLinks.includes(target)) swarmLinks.push(target);
          syncSwarmRolesFromGraph();
          renderSwarmGraph();
          showHoopsOk("Linked threads via orchestrator");
          return;
        }
        if (!threadId || !swarmThreads.some((t) => t.uid === threadId)) return;
        if (!swarmLinks.includes(threadId)) swarmLinks.push(threadId);
        syncSwarmRolesFromGraph();
        renderSwarmGraph();
        showHoopsOk("Linked thread to orchestrator");
      });
      if (swarmLinkMode) swarmEh?.enableDrawMode();
      swarmCy.on("tap", "edge", (ev) => {
        const id = ev.target.id();
        if (id.startsWith("se-")) {
          const uid = id.slice(3);
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
    if (host) host.classList.toggle("has-nodes", swarmThreads.length > 0);
    if (empty) empty.hidden = swarmThreads.length > 0;
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
      if (swarmSelectedUid) {
        const sel = swarmCy.getElementById(swarmSelectedUid);
        if (sel.nonempty()) sel.select();
      }
    });
    suppressEdgeSync = false;
    swarmCy.resize();
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
    lastHoopsSnap = "";
    lastHotswapSnap = "";
    await Promise.all([loadHoops(), loadHotSwap(), loadSwarmTemplates(), refreshLiveBoard()]);
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
      lastHoopsSnap = snap;
      if (!Array.isArray(list) || list.length === 0) {
        el.innerHTML = `<p class="hint">No hoops yet. Build a stage graph and create one.</p>`;
        return;
      }
      el.innerHTML = list.map((st) => {
        const outcomes = (st.outcomes || []).slice(-8).reverse();
        const last = (st.outcomes || []).length
          ? (st.outcomes)[(st.outcomes).length - 1]
          : null;
        const rows = outcomes.map((o) => {
          const pills = (o.stages || []).map((s) =>
            `<span class="stage-pill ${s.success ? "ok" : "fail"}">${esc(s.kind)}</span>`
          ).join("");
          return `<div class="hoop-outcome ${o.success ? "ok" : "fail"}">` +
            `<span>#${o.iteration}</span><span>${esc(o.route || "")}</span>` +
            `<span>${o.latency_ms || 0}ms</span>` +
            `<span>${pills} ${esc((o.summary || o.err || "").slice(0, 100))}</span></div>`;
        }).join("");
        const name = esc(st.spec?.name || st.spec?.id || "");
        const id = esc(st.spec?.id || "");
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
        const hitlBox = status === "waiting_human"
          ? `<div class="hoop-hitl">
              <p class="hint">Waiting for human: ${esc((gate.reason || "approval required").slice(0, 120))}</p>
              <label class="span2">Comment <input type="text" class="hoop-hitl-comment" data-id="${id}" placeholder="optional note" /></label>
              <span class="hoop-actions">
                <button type="button" class="linkish hoop-approve" data-id="${id}">Approve + resume</button>
                <button type="button" class="linkish hoop-reject" data-id="${id}">Reject</button>
              </span>
            </div>`
          : "";
        let lastOut = "No cycles yet";
        if (last) {
          const bit = (last.summary || last.err || (last.success ? "ok" : "fail") || "").slice(0, 80);
          lastOut = `#${last.iteration} ${last.success ? "ok" : "fail"} * ${bit}`;
        }
        return `<div class="hoop-card ${isRunning ? "is-running" : ""} ${status === "waiting_human" ? "is-waiting" : ""}" data-id="${id}" data-status="${esc(status)}">
          <div class="hoop-card-head">
            <strong>${name}</strong>
            <span class="status-badge ${badge}">${esc(status)}</span>
            <span class="muted">bias ${bias}</span>
            <span class="muted">score ${score}</span>
            <span class="hoop-actions">
              <button type="button" class="linkish hoop-edit-graph hoop-load-graph" data-id="${id}">Edit graph</button>
              <button type="button" class="linkish hoop-start" data-id="${id}">Start</button>
              <button type="button" class="linkish hoop-stop" data-id="${id}">Stop</button>
              <button type="button" class="linkish hoop-del" data-id="${id}">Delete</button>
            </span>
          </div>
          ${progLine}
          ${hitlBox}
          <p class="hoop-last-outcome" title="Last outcome">${esc(lastOut)}</p>
          <p class="hint" style="margin:8px 0">Goal: ${esc((st.spec?.goal || st.spec?.prompt || "").slice(0, 160))}</p>
          ${evalGoal ? `<p class="hint" style="margin:0 0 8px">Eval: ${evalGoal}</p>` : ""}
          <div class="hoop-stage-pills">${stageTags}</div>
          <div class="hoop-outcomes">${rows || `<span class="muted">No cycles yet -- start to run the pipeline</span>`}</div>
        </div>`;
      }).join("");
      el.querySelectorAll(".hoop-start").forEach((b) => b.addEventListener("click", () => hoopAction(b.dataset.id, "start")));
      el.querySelectorAll(".hoop-stop").forEach((b) => b.addEventListener("click", () => hoopAction(b.dataset.id, "stop")));
      el.querySelectorAll(".hoop-del").forEach((b) => b.addEventListener("click", () => deleteHoop(b.dataset.id)));
      el.querySelectorAll(".hoop-approve").forEach((b) => b.addEventListener("click", () => hoopGate(b.dataset.id, true)));
      el.querySelectorAll(".hoop-reject").forEach((b) => b.addEventListener("click", () => hoopGate(b.dataset.id, false)));
      el.querySelectorAll(".hoop-card").forEach((card) => {
        card.addEventListener("click", (ev) => {
          if (ev.target.closest("button")) return;
          const hid = card.dataset.id;
          if (hid) setAgentLogFocus("hoop", hid);
        });
      });
      updateAgentLogChrome();
      el.querySelectorAll(".hoop-edit-graph").forEach((b) => b.addEventListener("click", async () => {
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
  async function hoopAction(id, action) {
    const res = await fetch("/api/loops/" + encodeURIComponent(id) + "/" + action, { method: "POST" });
    if (!res.ok) {
      showHoopsError(await res.text());
      return;
    }
    if (action === "start" || action === "resume") setAgentLogFocus("hoop", id);
    showHoopsOk(action + " " + id);
    lastHoopsSnap = "";
    await loadHoops();
    if (isLiveLoopTabActive()) refreshLiveBoard();
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
          // MCP server bindings without specific tools → bind server for catalog fill.
          const wild = s.tools.filter((t) => t && t.kind === "mcp" && (t.name === "*" || !t.name));
          wild.forEach((t) => {
            if (t.server) row.tools.push({ name: "list_tools", kind: "mcp", server: t.server });
          });
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
      const snap = JSON.stringify(mods.map((m) => [m.name, m.enabled, m.generation]));
      if (snap === lastHotswapSnap && el.querySelector(".hotswap-row, .hint, .cfg-error")) return;
      lastHotswapSnap = snap;
      el.innerHTML = mods.map((m) => {
        const en = m.enabled !== false;
        const gen = Number(m.generation) || 0;
        const prev = hotswapGenCache[m.name];
        const bumped = prev != null && gen > prev;
        hotswapGenCache[m.name] = gen;
        return '<div class="hotswap-row ' + (m.stage ? "stage" : "") + (bumped ? " gen-bump" : "") + '">' +
          '<span class="hotswap-name">' + esc(m.name) + '</span>' +
          '<span class="tag">' + esc(m.kind || "") + '</span>' +
          '<span class="tag">' + esc(m.reload || (m.hot ? "hot" : "restart")) + '</span>' +
          '<span class="hotswap-gen ' + (bumped ? "live" : "") + '" title="' + esc(m.description || "") + '">gen ' + gen + '</span>' +
          '<label class="check"><input type="checkbox" data-mod="' + esc(m.name) + '" ' + (en ? "checked" : "") + " " + (m.hot ? "" : "disabled") + ' /> enabled</label>' +
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
        `<div class="hoop-card" data-tpl="${esc(t.id)}">` +
        `<div class="hoop-card-head"><strong>${esc(t.name || t.id)}</strong> ` +
        `<span class="tag">${t.enabled ? "on" : "off"}</span>` +
        `<span class="hoop-actions">` +
        `<button type="button" class="linkish tpl-load-graph" data-id="${esc(t.id)}">Load threads</button>` +
        `</span></div>` +
        `<p class="hint">${esc((t.prompt || "").slice(0, 120))}</p>` +
        `<p class="muted">roles: ${esc((t.roles || []).join(", ") || "--")}</p></div>`
      ).join("");
      el.querySelectorAll(".tpl-load-graph").forEach((b) => {
        b.addEventListener("click", () => {
          const tpl = list.find((x) => x.id === b.dataset.id);
          if (!tpl) return;
          const roles = (tpl.roles || []).length ? tpl.roles : ["plan", "exec"];
          writeRolesInput(roles);
          setSwarmThreadsFromRoles(roles);
          if (tpl.prompt) document.getElementById("swarm-prompt").value = tpl.prompt;
          const workers = document.getElementById("swarm-workers");
          if (workers) workers.value = String(tpl.max_workers || Math.min(4, roles.length) || 2);
          showHoopsOk(`Loaded template ${tpl.name || tpl.id} into thread graph`);
        });
      });
    } catch (e) {
      el.innerHTML = `<p class="cfg-error">${esc(String(e))}</p>`;
    }
  }

  const swarmForm = document.getElementById("swarm-form");
  if (swarmForm) {
    swarmForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const out = document.getElementById("swarm-result");
      const roles = rolesFromInput();
      setSwarmThreadsFromRoles(roles);
      swarmThreads.forEach((t) => {
        t.status = "running";
        t.summary = "";
        t.err = "";
      });
      renderSwarmGraph();
      const body = {
        prompt: document.getElementById("swarm-prompt").value.trim(),
        roles,
        max_workers: Number(document.getElementById("swarm-workers").value) || 2,
        prefer_local: true,
      };
      const res = await fetch("/api/swarm/run", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      const text = await res.text();
      if (out) {
        out.hidden = false;
        out.textContent = text;
      }
      if (!res.ok) {
        swarmThreads.forEach((t) => { t.status = "fail"; t.err = text.slice(0, 120); });
        renderSwarmGraph();
        showHoopsError(text);
        return;
      }
      try {
        const data = JSON.parse(text);
        if (data.turn_id) {
          lastSwarmRunId = data.turn_id;
          setAgentLogFocus("swarm", data.turn_id);
        }
        const results = data.results || [];
        const prog = data.progress || {};
        const pathSet = new Set(prog.path_taken || []);
        swarmThreads.forEach((t, i) => {
          const r = results.find((x) => String(x.role || "").toLowerCase() === t.role) || results[i];
          if (!r) {
            t.status = "ok";
            return;
          }
          t.status = r.err ? "fail" : "ok";
          t.summary = r.summary || r.episode?.summary || "";
          t.err = r.err || "";
          // Live DecisionRoute: mark workers on path.
          const wid = "worker-" + String(t.role || "").toLowerCase();
          if (pathSet.has(wid) && t.status === "ok") t.status = "ok";
        });
        if (prog.merge_failed) {
          swarmThreads.forEach((t) => {
            if (t.status !== "fail" && t.err) t.status = "fail";
          });
          if (out) {
            out.textContent = (prog.merge_narrative || data.summary || text) +
              (prog.path_taken ? "\n\npath: " + (prog.path_taken || []).join(" -> ") : "");
          }
        } else if (prog.path_taken && out) {
          out.textContent = (data.summary || text) + "\n\nroute: " + prog.path_taken.join(" -> ");
        }
      } catch (_) {
        swarmThreads.forEach((t) => { t.status = "ok"; });
      }
      renderSwarmGraph();
      showHoopsOk("Swarm finished");
    });
  }
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

  loadConfig();
  loadVRAM();
  loadSessions();
  refreshMetricsSnapshot();
})();
