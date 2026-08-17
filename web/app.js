/* Agent Matrix WebUI */
"use strict";

const $ = (sel) => document.querySelector(sel);

const loginView = $("#loginView");
const setupView = $("#setupView");
const dashView = $("#dashView");
const topActions = $("#topActions");
let refreshTimer = null;
let useTokenLogin = false;

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
      body: JSON.stringify({ username, password }),
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

async function loadAgents() {
  let data;
  try {
    const res = await api("/api/agents");
    if (!res.ok) return;
    data = await res.json();
  } catch {
    return;
  }
  const agents = data.agents || [];
  const online = agents.filter((a) => a.online).length;
  $("#statTotal").textContent = agents.length;
  $("#statOnline").textContent = online;
  $("#statOffline").textContent = agents.length - online;
  $("#emptyTip").hidden = agents.length > 0;

  const tbody = $("#agentRows");
  tbody.replaceChildren();
  for (const a of agents) {
    const tr = document.createElement("tr");

    const status = document.createElement("td");
    const dot = document.createElement("span");
    dot.className = "dot " + (a.online ? "on" : "off");
    const st = document.createElement("span");
    st.className = "status-text " + (a.online ? "on" : "off");
    st.textContent = a.online ? "在线" : "离线";
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
    created.className = "sub";

    const ops = document.createElement("td");
    const del = document.createElement("button");
    del.className = "btn small danger";
    del.textContent = "删除";
    del.addEventListener("click", () => removeAgent(a));
    ops.append(del);

    tr.append(status, name, host, os, ip, seen, created, ops);
    tbody.append(tr);
  }
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

/* ---- 接入新 Agent ---- */

const modal = $("#modal");

$("#btnNew").addEventListener("click", () => {
  modal.hidden = false;
  $("#enrollForm").hidden = false;
  $("#enrollResult").hidden = true;
  $("#enrollLabel").value = "";
  $("#enrollLabel").focus();
});

$("#btnClose").addEventListener("click", () => (modal.hidden = true));
modal.addEventListener("click", (e) => {
  if (e.target === modal) modal.hidden = true;
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
    // 剪贴板 API 不可用时退化为选中
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
    if (st.needs_setup) showSetup();
    else showLogin(st.env_login);
  } catch {
    showLogin(false);
  }
}
boot();
