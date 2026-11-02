const state = {
  token: sessionStorage.getItem("harbor.token") || "",
  data: null,
};

const $ = (sel) => document.querySelector(sel);

function applyTheme(theme) {
  const next = theme === "light" ? "light" : "dark";
  document.documentElement.dataset.theme = next;
  localStorage.setItem("harbor.theme", next);
  const button = $("#theme");
  const light = next === "light";
  button.textContent = light ? "Dark theme" : "Light theme";
  button.setAttribute("aria-pressed", light ? "true" : "false");
}

function authHeaders() {
  const headers = { Accept: "application/json" };
  if (state.token) {
    headers.Authorization = "Bearer " + state.token;
  }
  return headers;
}

async function api(path, options) {
  const opts = options || {};
  opts.headers = Object.assign(authHeaders(), opts.headers || {});
  const res = await fetch(path, opts);
  if (res.status === 401) {
    openToken();
    throw new Error("unauthorized");
  }
  return res;
}

function openToken() {
  const dialog = $("#token-dialog");
  if (!dialog.open) {
    dialog.showModal();
  }
}

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) {
    node.className = className;
  }
  if (text != null) {
    node.textContent = text;
  }
  return node;
}

function dash(value) {
  return value == null || value === "" ? "—" : String(value);
}

function ago(iso) {
  if (!iso) {
    return "—";
  }
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) {
    return "—";
  }
  const s = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (s < 5) {
    return "just now";
  }
  if (s < 60) {
    return s + "s ago";
  }
  const m = Math.round(s / 60);
  if (m < 60) {
    return m + "m ago";
  }
  return Math.round(m / 60) + "h ago";
}

function render(data) {
  state.data = data;
  const banner = $("#banner");
  if (data.demo) {
    banner.hidden = false;
    banner.classList.remove("error");
    banner.textContent = "Demo data. These hosts are documentation addresses from RFC 5737. The scores are the health formula on a fixed window. They are not dialed, and they are not in rotation.";
  } else if (!banner.classList.contains("error")) {
    banner.hidden = true;
    banner.textContent = "";
  }

  document.querySelectorAll(".seg button").forEach((button) => {
    button.setAttribute("aria-pressed", button.dataset.strategy === data.strategy ? "true" : "false");
  });

  const stats = data.stats || {};
  const kpis = [
    ["Proxies", String(stats.total || 0)],
    ["Healthy", String(stats.healthy || 0)],
    ["Dead", String(stats.dead || 0)],
    ["Unknown", String(stats.unknown || 0)],
    ["Avg latency", stats.avgLatencyMs == null ? "—" : stats.avgLatencyMs + " ms"],
    ["Success", stats.successRate == null ? "—" : Math.round(stats.successRate * 100) + "%"],
  ];
  const kpiRoot = $("#kpis");
  kpiRoot.replaceChildren();
  kpis.forEach(([label, value]) => {
    const cell = el("div", "kpi");
    cell.append(el("span", "", label), el("strong", "", value));
    kpiRoot.append(cell);
  });

  const proxies = data.proxies || [];
  $("#pool-meta").textContent = proxies.length === 1 ? "1 proxy" : proxies.length + " proxies";
  $("#empty").hidden = proxies.length !== 0;
  const rows = $("#rows");
  rows.replaceChildren();
  proxies.forEach((px) => {
    const tr = document.createElement("tr");
    if (px.status === "dead") {
      tr.className = "is-dead";
    }

    const status = el("td");
    const pill = el("span", "pill " + (px.status || "unknown"), px.status || "unknown");
    status.append(pill);
    if (!px.inRotation) {
      const held = el("span", "sub", px.demo ? "held · demo" : "out of rotation");
      status.append(held);
    }

    const proxy = el("td");
    const line = el("div");
    line.append(el("span", "scheme", px.scheme), el("span", "host", px.host + ":" + px.port));
    proxy.append(line);
    const bits = [];
    if (px.username) {
      bits.push(px.username);
    }
    if (px.hasPassword) {
      bits.push("auth");
    }
    if (px.lastError) {
      bits.push(px.lastError);
    }
    if (bits.length) {
      proxy.append(el("span", "sub", bits.join(" · ")));
    }

    const latency = el("td", "", px.latencyMs == null ? "—" : px.latencyMs + " ms");
    const success = el("td", "", px.windowN ? Math.round(px.successRate * 100) + "%" : "—");
    if (px.windowN) {
      success.title = px.windowOk + " of " + px.windowN;
    }
    const scoreCell = el("td");
    if (!px.windowN) {
      scoreCell.textContent = "—";
    } else {
      scoreCell.append(document.createTextNode(px.score.toFixed(1)));
      const meter = el("span", "meter");
      const fill = el("span");
      fill.style.width = Math.max(0, Math.min(100, px.score)) + "%";
      meter.append(fill);
      scoreCell.append(meter);
    }
    const placeBits = [px.city, px.country].filter(Boolean);
    const place = el("td", "", placeBits.length ? placeBits.join(", ") : "—");
    const checked = el("td", "when", ago(px.lastChecked));
    checked.dataset.at = px.lastChecked || "";

    const actions = el("td", "actions");
    const check = el("button", "textish", "Check");
    check.type = "button";
    check.disabled = !!px.demo;
    if (px.demo) {
      check.title = "Demo proxies are not probed";
    }
    check.addEventListener("click", () => checkProxy(px.id));
    const remove = el("button", "textish", "Remove");
    remove.type = "button";
    remove.addEventListener("click", () => removeProxy(px.id));
    actions.append(check, document.createTextNode(" "), remove);

    tr.append(status, proxy, latency, success, scoreCell, place, checked, actions);
    rows.append(tr);
  });

  const gw = data.gateway || "127.0.0.1:8081";
  $("#curl").textContent = "curl -x http://" + gw + " \\\n  http://example.com/";
  const budget = data.settings ? data.settings.budgetMs : 1500;
  $("#legend").textContent = "Score is 70 points of the recent success rate and 30 points of latency inside " + budget + " ms. A proxy leaves rotation after " + (data.settings ? data.settings.evictAfter : 3) + " failures in a row.";
  $("#version").textContent = "Harbor " + (data.version || "") + " · " + (data.listen || "") + " · gateway " + gw;
  $("#refreshed").textContent = "Updated " + new Date().toLocaleTimeString();
}

function tickTimes() {
  document.querySelectorAll(".when").forEach((cell) => {
    cell.textContent = ago(cell.dataset.at);
  });
}

async function load() {
  let res;
  try {
    res = await api("/api/overview");
  } catch (err) {
    if (String(err.message) !== "unauthorized") {
      showBanner(err.message || "The API did not respond");
    }
    return;
  }
  if (!res.ok) {
    showBanner("The overview failed (" + res.status + ")");
    return;
  }
  render(await res.json());
}

function showBanner(text) {
  const banner = $("#banner");
  banner.hidden = false;
  banner.classList.add("error");
  banner.textContent = text;
}

async function setStrategy(name) {
  const res = await api("/api/strategy", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ strategy: name }),
  });
  if (!res.ok) {
    showBanner("Could not change strategy");
    return;
  }
  await load();
}

async function importList() {
  const text = $("#import-text").value;
  const msg = $("#import-msg");
  msg.className = "msg";
  if (!text.trim()) {
    msg.textContent = "Paste at least one proxy.";
    msg.classList.add("bad");
    return;
  }
  const res = await api("/api/proxies/import", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ text: text }),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    msg.textContent = body.error || "Import failed";
    msg.classList.add("bad");
    return;
  }
  const errors = body.errors ? body.errors.length : 0;
  msg.textContent = "Added " + body.imported + ", updated " + body.updated + (errors ? ", " + errors + " rejected" : "") + ".";
  msg.classList.add(errors ? "bad" : "ok");
  if (!errors) {
    $("#import-text").value = "";
  }
  await load();
}

async function removeProxy(id) {
  const res = await api("/api/proxies/" + encodeURIComponent(id), { method: "DELETE" });
  if (!res.ok && res.status !== 204) {
    showBanner("Could not remove that proxy");
    return;
  }
  await load();
}

async function checkProxy(id) {
  const res = await api("/api/proxies/" + encodeURIComponent(id) + "/check", { method: "POST" });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    showBanner(body.error || "Check failed");
    return;
  }
  await load();
}

document.querySelectorAll(".seg button").forEach((button) => {
  button.addEventListener("click", () => setStrategy(button.dataset.strategy));
});
$("#theme").addEventListener("click", () => {
  const current = document.documentElement.dataset.theme === "light" ? "light" : "dark";
  applyTheme(current === "light" ? "dark" : "light");
});
$("#import-btn").addEventListener("click", () => {
  importList().catch((err) => showBanner(err.message));
});
$("#token-form").addEventListener("submit", (event) => {
  event.preventDefault();
  state.token = $("#token-input").value;
  sessionStorage.setItem("harbor.token", state.token);
  $("#token-dialog").close();
  load();
});

const params = new URLSearchParams(location.search);
applyTheme(params.get("theme") || localStorage.getItem("harbor.theme") || "dark");
load();
setInterval(load, 2000);
setInterval(tickTimes, 1000);
