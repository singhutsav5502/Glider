(() => {
  const panels = {
    overview: document.getElementById("panel-overview"),
    vram: document.getElementById("panel-vram"),
    rules: document.getElementById("panel-rules"),
    settings: document.getElementById("panel-settings"),
  };

  document.querySelectorAll(".tab").forEach((btn) => {
    btn.addEventListener("click", () => {
      document.querySelectorAll(".tab").forEach((b) => b.classList.remove("active"));
      btn.classList.add("active");
      Object.values(panels).forEach((p) => p.classList.remove("active"));
      panels[btn.dataset.tab].classList.add("active");
    });
  });

  const logEl = document.getElementById("request-log");
  let requests = 0;
  let local = 0;
  let cloud = 0;

  function addLog(data) {
    requests += 1;
    if (data.route === "local") local += 1;
    if (data.route === "cloud") cloud += 1;
    document.getElementById("m-requests").textContent = requests.toLocaleString();
    const total = local + cloud || 1;
    document.getElementById("m-split").textContent =
      `${Math.round((local / total) * 100)}% / ${Math.round((cloud / total) * 100)}%`;
    document.getElementById("m-latency").textContent =
      data.latency_ms != null ? `${Number(data.latency_ms).toFixed(1)}ms` : "—";

    const row = document.createElement("div");
    row.className = "log-row";
    const time = new Date().toLocaleTimeString();
    row.innerHTML = `<span>${time}</span><span>${data.route || ""}</span><span>${data.model || ""}</span><span>—</span><span>${data.latency_ms ?? ""}</span><span>${data.tokens ?? ""}</span>`;
    logEl.prepend(row);
  }

  function renderModels(models) {
    const tbody = document.querySelector("#models-table tbody");
    tbody.innerHTML = "";
    (models || []).forEach((m) => {
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td>${m.Name || m.name}</td>
        <td>${m.Backend || m.backend || ""}</td>
        <td>${m.VRAMEstimateMB || m.vram_estimate_mb || 0} MB</td>
        <td>${m.State || m.state || ""}</td>
        <td>
          <button data-action="load" data-name="${m.Name || m.name}">load</button>
          <button data-action="unload" data-name="${m.Name || m.name}">unload</button>
        </td>`;
      tbody.appendChild(tr);
    });
    tbody.querySelectorAll("button").forEach((b) => {
      b.addEventListener("click", async () => {
        await fetch(`/api/models/${encodeURIComponent(b.dataset.name)}/${b.dataset.action}`, { method: "POST" });
        loadModels();
      });
    });
  }

  async function loadConfig() {
    const res = await fetch("/api/config");
    const cfg = await res.json();
    document.getElementById("cfg-tokens").value = cfg.thresholds?.max_local_context_tokens ?? "";
    document.getElementById("cfg-idle").value = cfg.thresholds?.idle_unload_timeout ?? "";
    const list = document.getElementById("rules-list");
    list.innerHTML = "";
    (cfg.routing?.rules || []).forEach((r) => {
      const li = document.createElement("li");
      li.textContent = `${r.name} (p${r.priority}) → ${r.action?.target} ${r.action?.model || ""}`;
      list.appendChild(li);
    });
  }

  async function loadModels() {
    const res = await fetch("/api/models");
    renderModels(await res.json());
  }

  document.getElementById("settings-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const tokens = Number(document.getElementById("cfg-tokens").value);
    await fetch("/api/config", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ thresholds: { max_local_context_tokens: tokens } }),
    });
    loadConfig();
  });

  const proto = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.type === "request") addLog(msg.data || {});
      if (msg.type === "vram_update") {
        const g = document.getElementById("gpu-gauges");
        const usedPct = msg.data.total ? Math.round((msg.data.used / msg.data.total) * 100) : 0;
        g.innerHTML = `<div class="gauge"><div class="gauge-label">GPU 0 — ${usedPct}% used</div><div class="gauge-bar"><div class="gauge-used" style="width:${usedPct}%"></div></div></div>`;
      }
    } catch (_) {}
  };

  loadConfig();
  loadModels();
})();
