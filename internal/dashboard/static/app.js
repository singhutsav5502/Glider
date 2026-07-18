(() => {
  const panels = {
    overview: document.getElementById("panel-overview"),
    vram: document.getElementById("panel-vram"),
    rules: document.getElementById("panel-rules"),
    hoops: document.getElementById("panel-hoops"),
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

  document.querySelectorAll(".tab").forEach((btn) => {
    btn.addEventListener("click", () => {
      document.querySelectorAll(".tab").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      Object.values(panels).forEach((p) => p.classList.remove("active"));
      panels[btn.dataset.tab].classList.add("active");
      if (btn.dataset.tab === "vram") loadVRAM();
      if (btn.dataset.tab === "rules") renderRulesEditor(currentCfg);
      if (btn.dataset.tab === "hoops") refreshHoopsPanel();
      if (btn.dataset.tab === "overview") loadSessions();
    });
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
      requests ? `${(latencySum / requests).toFixed(1)}ms` : "â€”";
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
    // Tunnel opens / non-LLM skips are counters only â€” omit from Overview table
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
    const hostModel = parts.join(" Â· ") || "â€”";
    const rule = data.rule || "â€”";
    const hasLatency = data.latency_ms != null && data.latency_ms !== "";
    const hasTokens = data.tokens != null && data.tokens !== "";
    const latency = hasLatency ? Number(data.latency_ms).toFixed(1) : "â€”";
    const tokens = hasTokens ? data.tokens : "â€”";
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
        return `<div class="gauge"><div class="gauge-label">GPU ${gpu.index} â€” ${gpu.error}</div></div>`;
      }
      const usedPct = gpu.total_bytes ? Math.round((gpu.used_bytes / gpu.total_bytes) * 100) : 0;
      return `<div class="gauge">
        <div class="gauge-label">GPU ${gpu.index} â€” ${usedPct}% used Â· ${gpu.used_mb}/${gpu.total_mb} MB</div>
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
    options.push(`<option value="">â€”</option>`);
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
          <label title="explicit: /local /cloud commands Â· context_size: token threshold Â· script: Starlark file Â· always: default fallback Â· regex: pattern match">Trigger type
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
          <label title="local = Ollama/vLLM Â· cloud = BYOK (gateway) or origin passthrough (MITM)">Action target
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
      const label = s.current ? `Current Â· ${s.id.slice(0, 12)}â€¦` : `${new Date(s.started_at).toLocaleString()} Â· ${s.request_count} req`;
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
      meta.textContent = `${s.request_count || 0} requests Â· ${s.token_total || 0} tokens Â· avg ${Number(agg.avg_latency_ms || 0).toFixed(1)}ms`;
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
        // API returns newest first; append in reverse so newest ends on top via prepend... already newest first, prepend each would reverse â€” append in order
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

  async function refreshHoopsPanel() {
    await Promise.all([loadHoops(), loadHotSwap(), loadSwarmTemplates()]);
  }

  async function loadHoops() {
    const el = document.getElementById("hoops-list");
    if (!el) return;
    try {
      const res = await fetch("/api/loops");
      const list = await res.json();
      if (!Array.isArray(list) || list.length === 0) {
        el.innerHTML = `<p class="hint">No hoops yet. Compose stages + eval above.</p>`;
        return;
      }
      el.innerHTML = list.map((st) => {
        const outcomes = (st.outcomes || []).slice(-8).reverse();
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
        const status = esc(st.status || "");
        const bias = st.hoop?.local_bias != null ? Number(st.hoop.local_bias).toFixed(2) : "—";
        const stageTags = (st.spec?.stages || []).filter((s) => s.enabled !== false)
          .map((s) => `<span class="tag">${esc(s.kind)}</span>`).join(" ");
        const evalGoal = esc(st.spec?.eval?.goal || st.spec?.goal || "");
        const score = st.last_eval_score != null ? Number(st.last_eval_score).toFixed(2) : "—";
        return `<div class="hoop-card" data-id="${id}">
          <div class="hoop-card-head">
            <strong>${name}</strong>
            <span class="tag">${status}</span>
            <span class="muted">bias ${bias}</span>
            <span class="muted">score ${score}</span>
            <span class="hoop-actions">
              <button type="button" class="linkish hoop-start" data-id="${id}">Start</button>
              <button type="button" class="linkish hoop-stop" data-id="${id}">Stop</button>
              <button type="button" class="linkish hoop-del" data-id="${id}">Delete</button>
            </span>
          </div>
          <p class="hint" style="margin:8px 0">Goal: ${esc((st.spec?.goal || st.spec?.prompt || "").slice(0, 160))}</p>
          ${evalGoal ? `<p class="hint" style="margin:0 0 8px">Eval: ${evalGoal}</p>` : ""}
          <div>${stageTags}</div>
          <div class="hoop-outcomes">${rows || `<span class="muted">No cycles yet — start to run planner→actor→critic</span>`}</div>
        </div>`;
      }).join("");
      el.querySelectorAll(".hoop-start").forEach((b) => b.addEventListener("click", () => hoopAction(b.dataset.id, "start")));
      el.querySelectorAll(".hoop-stop").forEach((b) => b.addEventListener("click", () => hoopAction(b.dataset.id, "stop")));
      el.querySelectorAll(".hoop-del").forEach((b) => b.addEventListener("click", () => deleteHoop(b.dataset.id)));
    } catch (e) {
      el.innerHTML = `<p class="cfg-error">${esc(String(e))}</p>`;
    }
  }

  async function hoopAction(id, action) {
    const res = await fetch(`/api/loops/${encodeURIComponent(id)}/${action}`, { method: "POST" });
    if (!res.ok) {
      showHoopsError(await res.text());
      return;
    }
    showHoopsOk(`${action} ${id}`);
    await loadHoops();
  }

  async function deleteHoop(id) {
    const res = await fetch(`/api/loops/${encodeURIComponent(id)}`, { method: "DELETE" });
    if (!res.ok && res.status !== 204) {
      showHoopsError(await res.text());
      return;
    }
    showHoopsOk(`deleted ${id}`);
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

  function esc(s) {
    return String(s || "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  const hoopForm = document.getElementById("hoop-form");
  if (hoopForm) {
    hoopForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      showHoopsError("");
      const route = document.getElementById("hoop-route").value || "local";
      const name = document.getElementById("hoop-name").value.trim();
      const stages = [];
      document.querySelectorAll("#hoop-stages input[data-stage]").forEach((inp) => {
        stages.push({ kind: inp.dataset.stage, enabled: inp.checked, id: inp.dataset.stage, name: inp.dataset.stage });
      });
      const evalGoal = (document.getElementById("hoop-eval-goal") || {}).value?.trim() || "";
      const maxIter = Number(document.getElementById("hoop-max-iter")?.value) || 0;
      const goal = document.getElementById("hoop-prompt").value.trim();
      const body = {
        id: name.toLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-|-$/g, "") || undefined,
        name,
        goal,
        prompt: goal,
        interval: document.getElementById("hoop-interval").value.trim() || "",
        route,
        learning: document.getElementById("hoop-learning").checked,
        stages,
        eval: { goal: evalGoal || goal, on_success_n: evalGoal ? 1 : 0, min_score: 0.7 },
        max_iterations: maxIter || 3,
        autonomy: "L1",
      };
      const res = await fetch("/api/loops", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        showHoopsError(await res.text());
        return;
      }
      showHoopsOk("Hoop created");
      hoopForm.reset();
      document.querySelectorAll("#hoop-stages input[data-stage]").forEach((inp) => { inp.checked = true; });
      document.getElementById("hoop-interval").value = "5m";
      document.getElementById("hoop-max-iter").value = "3";
      document.getElementById("hoop-route").value = "local";
      await loadHoops();
    });
  }

  const hoopsRefresh = document.getElementById("hoops-refresh");
  if (hoopsRefresh) hoopsRefresh.addEventListener("click", () => refreshHoopsPanel());

  async function loadHotSwap() {
    const el = document.getElementById("hotswap-list");
    if (!el) return;
    try {
      const res = await fetch("/api/hotswap/modules");
      const data = await res.json();
      let mods = data.modules?.length ? data.modules : (data.catalog || []);
      // Prefer stage modules first for Loop Engineering framing.
      mods = [...mods].sort((a, b) => {
        const as = a.stage ? 0 : 1;
        const bs = b.stage ? 0 : 1;
        if (as !== bs) return as - bs;
        return String(a.name).localeCompare(String(b.name));
      });
      el.innerHTML = mods.map((m) => {
        const en = m.enabled !== false;
        return `<div class="hotswap-row ${m.stage ? "stage" : ""}">
          <span class="hotswap-name">${esc(m.name)}</span>
          <span class="tag">${esc(m.kind || "")}</span>
          <span class="tag">${esc(m.reload || (m.hot ? "hot" : "restart"))}</span>
          <span class="muted" title="${esc(m.description || "")}">gen ${m.generation || 0}</span>
          <label class="check"><input type="checkbox" data-mod="${esc(m.name)}" ${en ? "checked" : ""} ${m.hot ? "" : "disabled"} /> enabled</label>
        </div>`;
      }).join("") || `<p class="hint">No modules registered.</p>`;
      el.querySelectorAll("input[data-mod]").forEach((inp) => {
        inp.addEventListener("change", async () => {
          const res = await fetch(`/api/hotswap/modules/${encodeURIComponent(inp.dataset.mod)}`, {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ enabled: inp.checked }),
          });
          if (!res.ok) showHoopsError(await res.text());
          else showHoopsOk(`${inp.dataset.mod} ${inp.checked ? "enabled" : "disabled"}`);
        });
      });
    } catch (e) {
      el.innerHTML = `<p class="cfg-error">${esc(String(e))}</p>`;
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
        `<div class="hoop-card"><strong>${esc(t.name || t.id)}</strong> ` +
        `<span class="tag">${t.enabled ? "on" : "off"}</span>` +
        `<p class="hint">${esc((t.prompt || "").slice(0, 120))}</p></div>`
      ).join("");
    } catch (e) {
      el.innerHTML = `<p class="cfg-error">${esc(String(e))}</p>`;
    }
  }

  const swarmForm = document.getElementById("swarm-form");
  if (swarmForm) {
    swarmForm.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const out = document.getElementById("swarm-result");
      const roles = document.getElementById("swarm-roles").value.split(",").map((s) => s.trim()).filter(Boolean);
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
      if (!res.ok) showHoopsError(text);
      else showHoopsOk("Swarm finished");
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

  // Quick log-level change from config still goes through full save; also listen for select blur optional — covered by Save.

  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${proto}://${location.host}/ws`);
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
          // Only update first gauge live if present; full refresh on tab load
          const first = g.querySelector(".gauge-used");
          if (first) first.style.width = `${usedPct}%`;
        }
      }
    } catch (_) {}
  };

  loadConfig();
  loadVRAM();
  loadSessions();
  refreshMetricsSnapshot();
})();
