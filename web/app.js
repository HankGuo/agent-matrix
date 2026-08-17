/* Agent Matrix WebUI */
"use strict";

const $ = (sel) => document.querySelector(sel);

const loginView = $("#loginView");
const setupView = $("#setupView");
const dashView = $("#dashView");
const topActions = $("#topActions");
let refreshTimer = null;
let useTokenLogin = false;
let firstLoad = true;

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...opts,
  });
  if (res.status === 401 && path !== "/api/login" && path !== "/api/setup") {
    boot();
    throw new Error("unauthorized");
  }
  return res;
}

function hideAll() {
  loginView.hidden = true;
  setupView.hidden = true;
  dashView.hidden = true;
  topActions.hidden = true;
  if (refreshTimer) clearInterval(refreshTimer);
}

function showLogin(envLogin) {
  hideAll();
  loginView.hidden = false;
  $("#loginToggle").hidden = !envLogin;
  $("#loginError").hidden = true;
}

function showSetup() {
  hideAll();
  setupView.hidden = false;
  $("#setupError").hidden = true;
}

function showDash() {
  hideAll();
  dashView.hidden = false;
  topActions.hidden = false;
  loadAgents();
  refreshTimer = setInterval(loadAgents, 15000);
}

/* ---- 初始化（首次访问强制设置账号） ---- */

$("#setupForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const errEl = $("#setupError");
  errEl.hidden = true;
  const username = $("#setupUser").value.trim();
  const password = $("#setupPass").value;
  if (password !== $("#setupPass2").value) {
    errEl.textContent = "两次输入的密码不一致";
    errEl.hidden = false;
    return;
  }
  try {
    const res = await api("/api/setup", {
      method: "POST",
      body: JSON.stringify({
        username,
        password,
        base_url: $("#setupBaseURL").value.trim(),
      }),
    });
    if (!res.ok) {
      errEl.textContent = (await res.json()).error || "初始化失败";
      errEl.hidden = false;
      return;
    }
    showDash();
  } catch {
    errEl.textContent = "网络错误";
    errEl.hidden = false;
  }
});

/* ---- 登录 / 退出 ---- */

$("#loginToggleLink").addEventListener("click", (e) => {
  e.preventDefault();
  useTokenLogin = !useTokenLogin;
  $("#accountFields").hidden = useTokenLogin;
  $("#tokenField").hidden = !useTokenLogin;
  $("#loginToggleLink").textContent = useTokenLogin ? "改用账号密码登录" : "改用应急令牌登录";
});

$("#loginForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const errEl = $("#loginError");
  errEl.hidden = true;
  const body = useTokenLogin
    ? { token: $("#loginToken").value }
    : { username: $("#loginUser").value.trim(), password: $("#loginPass").value };
  try {
    const res = await api("/api/login", { method: "POST", body: JSON.stringify(body) });
    if (!res.ok) {
      errEl.textContent = (await res.json()).error || "登录失败";
      errEl.hidden = false;
      return;
    }
    $("#loginToken").value = "";
    $("#loginPass").value = "";
    showDash();
  } catch {
    errEl.textContent = "网络错误";
    errEl.hidden = false;
  }
});

$("#btnLogout").addEventListener("click", async () => {
  await api("/api/logout", { method: "POST", body: "{}" });
  boot();
});

/* ---- Agent 列表 ---- */

function relTime(ts) {
  const diff = Math.max(0, Math.floor(Date.now() / 1000) - ts);
  if (diff < 10) return "刚刚";
  if (diff < 60) return diff + " 秒前";
  if (diff < 3600) return Math.floor(diff / 60) + " 分钟前";
  if (diff < 86400) return Math.floor(diff / 3600) + " 小时前";
  return Math.floor(diff / 86400) + " 天前";
}

function fmtTime(ts) {
  const d = new Date(ts * 1000);
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function skeleton() {
  const tbody = $("#agentRows");
  const cards = $("#agentCards");
  tbody.replaceChildren();
  cards.replaceChildren();
  for (let i = 0; i < 3; i++) {
    const tr = document.createElement("tr");
    tr.className = "sk-row";
    for (let c = 0; c < 8; c++) {
      const td = document.createElement("td");
      const bar = document.createElement("div");
      bar.className = "sk";
      bar.style.width = (55 + ((i * 7 + c * 13) % 40)) + "%";
      td.append(bar);
      tr.append(td);
    }
    tbody.append(tr);
    const card = document.createElement("div");
    card.className = "acard";
    const bar = document.createElement("div");
    bar.className = "sk";
    bar.style.width = "60%";
    const bar2 = document.createElement("div");
    bar2.className = "sk";
    bar2.style.cssText = "width:90%;margin-top:10px";
    card.append(bar, bar2);
    cards.append(card);
  }
}

async function loadAgents() {
  if (firstLoad) {
    skeleton();
  }
  let data;
  try {
    const res = await api("/api/agents");
    if (!res.ok) return;
    data = await res.json();
  } catch {
    return;
  } finally {
    firstLoad = false;
  }
  const agents = data.agents || [];
  const online = agents.filter((a) => a.online).length;
  $("#statTotal").textContent = agents.length;
  $("#statOnline").textContent = online;
  $("#statOffline").textContent = agents.length - online;
  const hasAgents = agents.length > 0;
  document.querySelector(".table-scroll").style.display = hasAgents ? "" : "none";
  $("#agentCards").style.display = hasAgents ? "" : "none";
  $("#emptyTip").hidden = hasAgents;
  const now = new Date();
  const p = (n) => String(n).padStart(2, "0");
  const tip = `更新于 ${p(now.getHours())}:${p(now.getMinutes())}:${p(now.getSeconds())} · 每 15 秒自动刷新`;
  $("#syncTip").textContent = tip;
  $("#footSync").textContent = tip;

  const tbody = $("#agentRows");
  const cards = $("#agentCards");
  tbody.replaceChildren();
  cards.replaceChildren();
  for (const a of agents) {
    const tr = document.createElement("tr");

    const status = document.createElement("td");
    const dot = document.createElement("span");
    dot.className = "dot " + (a.online ? "on" : "off");
    const st = document.createElement("span");
    st.className = "status-text " + (a.online ? "on" : "off");
    st.textContent = a.online ? " 在线" : " 离线";
    status.append(dot, st);

    const name = document.createElement("td");
    const nameDiv = document.createElement("div");
    nameDiv.className = "agent-name";
    nameDiv.textContent = a.name;
    const idDiv = document.createElement("div");
    idDiv.className = "sub mono";
    idDiv.textContent = a.id;
    name.append(nameDiv, idDiv);

    const host = td(a.hostname || "-");
    const os = td(a.os ? `${a.os}/${a.arch || "?"}` : "-");
    const ip = td(a.ip || "-");
    ip.className = "mono";
    const seen = td(relTime(a.last_seen));
    seen.title = fmtTime(a.last_seen);
    const created = td(fmtTime(a.created_at));
    created.className = "sub col-created";

    const ops = document.createElement("td");
    const del = document.createElement("button");
    del.className = "btn danger";
    del.textContent = "删除";
    del.addEventListener("click", () => removeAgent(a));
    ops.append(del);

    tr.append(status, name, host, os, ip, seen, created, ops);
    tbody.append(tr);
    cards.append(agentCard(a));
  }
}

/* 移动端卡片视图 */
function agentCard(a) {
  const card = document.createElement("div");
  card.className = "acard";

  const head = document.createElement("div");
  head.className = "acard-head";
  const dot = document.createElement("span");
  dot.className = "dot " + (a.online ? "on" : "off");
  const st = document.createElement("span");
  st.className = "status-text " + (a.online ? "on" : "off");
  st.textContent = a.online ? "在线" : "离线";
  const nm = document.createElement("span");
  nm.className = "acard-name";
  nm.textContent = a.name;
  const del = document.createElement("button");
  del.className = "btn danger";
  del.textContent = "删除";
  del.addEventListener("click", () => removeAgent(a));
  head.append(dot, st, nm, del);

  const idLine = document.createElement("div");
  idLine.className = "acard-id mono";
  idLine.textContent = a.id;

  const meta = document.createElement("dl");
  meta.className = "acard-meta";
  const fields = [
    ["主机", a.hostname || "-"],
    ["系统", a.os ? `${a.os}/${a.arch || "?"}` : "-"],
    ["IP", a.ip || "-"],
    ["最后心跳", relTime(a.last_seen)],
    ["注册时间", fmtTime(a.created_at)],
  ];
  for (const [k, v] of fields) {
    const wrap = document.createElement("div");
    const dt = document.createElement("dt");
    dt.textContent = k;
    const dd = document.createElement("dd");
    dd.textContent = v;
    wrap.append(dt, dd);
    meta.append(wrap);
  }

  card.append(head, idLine, meta);
  return card;
}

function td(text) {
  const el = document.createElement("td");
  el.textContent = text;
  return el;
}

async function removeAgent(a) {
  if (!confirm(`确定删除 Agent「${a.name}」(${a.id})？删除后它将无法继续上报心跳。`)) return;
  const res = await api("/api/agents/" + encodeURIComponent(a.id), { method: "DELETE" });
  if (res.ok) loadAgents();
}

/* ---- 接入新 Agent（右侧滑出面板） ---- */

const panel = $("#enrollPanel");
const panelMask = $("#panelMask");
const settingsPanel = $("#settingsPanel");

function openPanel() {
  $("#enrollForm").hidden = false;
  $("#enrollResult").hidden = true;
  $("#enrollLabel").value = "";
  panel.classList.add("show");
  panelMask.classList.add("show");
  $("#enrollLabel").focus();
}

function closePanels() {
  panel.classList.remove("show");
  settingsPanel.classList.remove("show");
  panelMask.classList.remove("show");
}

$("#btnNew").addEventListener("click", openPanel);
$("#btnNewEmpty").addEventListener("click", openPanel);
$("#btnClose").addEventListener("click", closePanels);
panelMask.addEventListener("click", closePanels);
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") closePanels();
});

/* ---- 设置面板 ---- */

$("#btnSettings").addEventListener("click", async () => {
  try {
    const res = await api("/api/settings");
    if (res.ok) {
      const data = await res.json();
      $("#setBaseURL").value = data.base_url || "";
      $("#settingsVer").textContent = "Agent Matrix v" + (data.version || "");
    }
  } catch { /* 保持旧值 */ }
  $("#settingsSaved").hidden = true;
  settingsPanel.classList.add("show");
  panelMask.classList.add("show");
});

$("#btnSettingsClose").addEventListener("click", closePanels);

$("#btnSaveSettings").addEventListener("click", async () => {
  const res = await api("/api/settings", {
    method: "POST",
    body: JSON.stringify({ base_url: $("#setBaseURL").value }),
  });
  const data = await res.json();
  if (!res.ok) {
    alert(data.error || "保存失败");
    return;
  }
  $("#setBaseURL").value = data.base_url;
  const ok = $("#settingsSaved");
  ok.hidden = false;
  setTimeout(() => (ok.hidden = true), 2000);
});

$("#btnGen").addEventListener("click", async () => {
  const res = await api("/api/enrollments", {
    method: "POST",
    body: JSON.stringify({ label: $("#enrollLabel").value }),
  });
  if (!res.ok) {
    alert("生成失败：" + (await res.json()).error);
    return;
  }
  const data = await res.json();
  $("#enrollForm").hidden = true;
  $("#enrollResult").hidden = false;
  $("#promptText").textContent = data.prompt;
});

$("#btnCopy").addEventListener("click", async () => {
  const btn = $("#btnCopy");
  try {
    await navigator.clipboard.writeText($("#promptText").textContent);
    btn.textContent = "已复制 ✓";
  } catch {
    const range = document.createRange();
    range.selectNodeContents($("#promptText"));
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
    btn.textContent = "已选中，请按 ⌘C";
  }
  setTimeout(() => (btn.textContent = "复制指令"), 2000);
});

/* ---- 启动：探测会话与初始化状态 ---- */
async function boot() {
  try {
    const res = await fetch("/api/agents");
    if (res.ok) {
      showDash();
      return;
    }
  } catch { /* 继续走状态探测 */ }
  try {
    const st = await (await fetch("/api/auth/status")).json();
    if (st.version) $("#ver").textContent = "Agent Matrix v" + st.version;
    if (st.base_url) $("#setupBaseURL").value = st.base_url;
    if (st.needs_setup) showSetup();
    else showLogin(st.env_login);
  } catch {
    showLogin(false);
  }
}
boot();
