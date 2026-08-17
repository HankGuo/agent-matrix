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
  loadTasks();
  refreshTimer = setInterval(() => {
    loadAgents();
    loadTasks();
  }, 15000);
}

/* ---- Agent / 任务 视图切换 ---- */
let currentView = "agents";
document.querySelectorAll("#dashTabs .tab").forEach((btn) => {
  btn.addEventListener("click", () => {
    currentView = btn.dataset.view;
    document.querySelectorAll("#dashTabs .tab").forEach((b) => b.classList.toggle("active", b === btn));
    $("#agentsView").hidden = currentView !== "agents";
    $("#tasksView").hidden = currentView !== "tasks";
  });
});

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
const taskNewPanel = $("#taskNewPanel");
const taskDetailPanel = $("#taskDetailPanel");

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
  taskNewPanel.classList.remove("show");
  taskDetailPanel.classList.remove("show");
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

/* ---- 任务 ---- */

const taskStatusMeta = {
  pending: ["待执行", "gray"],
  running: ["执行中", "amber"],
  done: ["已完成", "green"],
  partial: ["部分失败", "amber"],
  failed: ["失败", "red"],
  canceled: ["已取消", "gray"],
};
const asgStatusMeta = {
  pending: ["待拉取", "gray"],
  delivered: ["执行中", "amber"],
  done: ["完成", "green"],
  failed: ["失败", "red"],
  canceled: ["已取消", "gray"],
};

function pill(meta) {
  const s = document.createElement("span");
  s.className = "pill " + (meta ? meta[1] : "gray");
  s.textContent = meta ? meta[0] : "?";
  return s;
}

function assigneeText(t) {
  return (t.assignments || [])
    .map((a) => a.agent_name + (a.stale ? "（疑似卡住）" : ""))
    .join("、");
}

let tasksFirstLoad = true;

async function loadTasks() {
  let data;
  try {
    const res = await api("/api/tasks");
    if (!res.ok) return;
    data = await res.json();
  } catch {
    return;
  } finally {
    tasksFirstLoad = false;
  }
  const tasks = data.tasks || [];
  const running = tasks.filter((t) => t.status === "running" || t.status === "pending").length;
  $("#taskStat").textContent = tasks.length
    ? `共 ${tasks.length} 个任务 · ${running} 个进行中`
    : "";
  const has = tasks.length > 0;
  $("#taskTableWrap").style.display = has ? "" : "none";
  $("#taskCards").style.display = has ? "" : "none";
  $("#taskEmpty").hidden = has;

  const tbody = $("#taskRows");
  const cards = $("#taskCards");
  tbody.replaceChildren();
  cards.replaceChildren();
  for (const t of tasks) {
    const tr = document.createElement("tr");

    const status = document.createElement("td");
    status.append(pill(taskStatusMeta[t.status]));

    const title = document.createElement("td");
    const titleDiv = document.createElement("div");
    titleDiv.className = "agent-name";
    titleDiv.textContent = t.title;
    const idDiv = document.createElement("div");
    idDiv.className = "sub mono";
    idDiv.textContent = t.id;
    title.append(titleDiv, idDiv);

    const assignees = td(assigneeText(t) || "-");
    assignees.className = "task-assignees";
    const created = td(fmtTime(t.created_at));
    created.className = "sub col-created";

    const ops = document.createElement("td");
    const view = document.createElement("button");
    view.className = "btn text";
    view.textContent = "详情";
    view.addEventListener("click", () => openTaskDetail(t.id));
    ops.append(view);

    tr.append(status, title, assignees, created, ops);
    tbody.append(tr);
    cards.append(taskCard(t));
  }
}

/* 移动端任务卡片 */
function taskCard(t) {
  const card = document.createElement("div");
  card.className = "acard";

  const head = document.createElement("div");
  head.className = "acard-head";
  head.append(pill(taskStatusMeta[t.status]));
  const nm = document.createElement("span");
  nm.className = "acard-name";
  nm.textContent = t.title;
  const view = document.createElement("button");
  view.className = "btn text";
  view.textContent = "详情";
  view.addEventListener("click", () => openTaskDetail(t.id));
  head.append(nm, view);

  const meta = document.createElement("dl");
  meta.className = "acard-meta";
  const fields = [
    ["指派给", assigneeText(t) || "-"],
    ["创建时间", fmtTime(t.created_at)],
  ];
  for (const [k, v] of fields) {
    const wrap = document.createElement("div");
    const dtx = document.createElement("dt");
    dtx.textContent = k;
    const dd = document.createElement("dd");
    dd.textContent = v;
    wrap.append(dtx, dd);
    meta.append(wrap);
  }

  card.append(head, meta);
  return card;
}

/* 新建任务 */
async function openTaskNew() {
  $("#taskTitle").value = "";
  $("#taskContent").value = "";
  $("#taskNewError").hidden = true;
  const picker = $("#agentPicker");
  picker.replaceChildren();
  let agents = [];
  try {
    const res = await api("/api/agents");
    if (res.ok) agents = (await res.json()).agents || [];
  } catch { /* 保持空 */ }
  if (!agents.length) {
    const p = document.createElement("p");
    p.className = "muted small";
    p.textContent = "还没有已接入的 Agent，请先在「Agent」页接入。";
    picker.append(p);
  }
  for (const a of agents) {
    const lab = document.createElement("label");
    lab.className = "pick";
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.value = a.id;
    const dot = document.createElement("span");
    dot.className = "dot " + (a.online ? "on" : "off");
    const nm = document.createElement("span");
    nm.className = "pick-name";
    nm.textContent = a.name;
    lab.append(cb, dot, nm);
    if (!a.online) {
      const off = document.createElement("span");
      off.className = "sub small";
      off.textContent = "离线";
      lab.append(off);
    }
    picker.append(lab);
  }
  taskNewPanel.classList.add("show");
  panelMask.classList.add("show");
  $("#taskTitle").focus();
}

$("#btnNewTask").addEventListener("click", openTaskNew);
$("#btnNewTaskEmpty").addEventListener("click", openTaskNew);
$("#btnTaskNewClose").addEventListener("click", closePanels);

$("#btnCreateTask").addEventListener("click", async () => {
  const errEl = $("#taskNewError");
  errEl.hidden = true;
  const ids = [...document.querySelectorAll("#agentPicker input:checked")].map((i) => i.value);
  const res = await api("/api/tasks", {
    method: "POST",
    body: JSON.stringify({
      title: $("#taskTitle").value,
      content: $("#taskContent").value,
      agent_ids: ids,
    }),
  });
  const data = await res.json();
  if (!res.ok) {
    errEl.textContent = data.error || "创建失败";
    errEl.hidden = false;
    return;
  }
  closePanels();
  if (currentView !== "tasks") $("#dashTabs .tab[data-view=tasks]").click();
  loadTasks();
});

/* 任务详情 */
let currentTaskId = null;

async function openTaskDetail(id) {
  currentTaskId = id;
  try {
    const res = await api("/api/tasks/" + encodeURIComponent(id));
    if (!res.ok) return;
    renderTaskDetail(await res.json());
    taskDetailPanel.classList.add("show");
    panelMask.classList.add("show");
  } catch { /* 网络错误时保持现状 */ }
}

function renderTaskDetail(d) {
  $("#tdTitle").textContent = d.task.title;
  const st = $("#tdStatus");
  st.replaceChildren(pill(taskStatusMeta[d.status]));
  $("#tdTime").textContent =
    "创建于 " + fmtTime(d.task.created_at) +
    (d.task.canceled_at ? " · 取消于 " + fmtTime(d.task.canceled_at) : "");
  $("#tdContent").textContent = d.task.content;

  const box = $("#tdAssigns");
  box.replaceChildren();
  for (const a of d.assignments || []) {
    box.append(assignBlock(a));
  }
  $("#btnCancelTask").hidden = ["done", "failed", "partial", "canceled"].includes(d.status);
}

function assignBlock(a) {
  const div = document.createElement("div");
  div.className = "asgn";

  const head = document.createElement("div");
  head.className = "asgn-head";
  const dot = document.createElement("span");
  dot.className = "dot " + (a.online ? "on" : "off");
  const nm = document.createElement("span");
  nm.className = "asgn-name";
  nm.textContent = a.agent_name;
  head.append(dot, nm, pill(asgStatusMeta[a.status]));
  if (a.stale) head.append(pill(["疑似卡住", "amber"]));

  const times = document.createElement("p");
  times.className = "sub small";
  const parts = [];
  if (a.delivered_at) parts.push("投递于 " + fmtTime(a.delivered_at));
  if (a.result_at) parts.push("回写于 " + fmtTime(a.result_at));
  if (!parts.length) parts.push("等待 Agent 拉取");
  times.textContent = parts.join(" · ");

  div.append(head, times);

  if (a.status === "delivered") {
    const rq = document.createElement("button");
    rq.className = "btn text";
    rq.textContent = "重新投递";
    rq.addEventListener("click", async () => {
      const res = await api("/api/assignments/" + encodeURIComponent(a.id) + "/requeue", {
        method: "POST",
        body: "{}",
      });
      if (res.ok) {
        openTaskDetail(currentTaskId);
        loadTasks();
      }
    });
    div.append(rq);
  }
  if (a.result && a.result !== "…") {
    const pre = document.createElement("pre");
    pre.className = "result-view";
    pre.textContent = a.result;
    div.append(pre);
  }
  return div;
}

$("#btnTdClose").addEventListener("click", closePanels);

$("#btnCancelTask").addEventListener("click", async () => {
  if (!currentTaskId) return;
  if (!confirm("确定取消该任务？所有未结束的指派都会终止。")) return;
  const res = await api("/api/tasks/" + encodeURIComponent(currentTaskId) + "/cancel", {
    method: "POST",
    body: "{}",
  });
  if (res.ok) {
    openTaskDetail(currentTaskId);
    loadTasks();
  }
});

/* 老 Agent 补充任务能力指令 */
$("#btnTaskLoop").addEventListener("click", async () => {
  const res = await api("/api/taskloop-prompt");
  if (!res.ok) return;
  const data = await res.json();
  $("#taskLoopText").textContent = data.prompt;
  $("#taskLoopBox").hidden = false;
  $("#btnTaskLoop").hidden = true;
});

$("#btnCopyTaskLoop").addEventListener("click", async () => {
  const btn = $("#btnCopyTaskLoop");
  try {
    await navigator.clipboard.writeText($("#taskLoopText").textContent);
    btn.textContent = "已复制 ✓";
  } catch {
    const range = document.createRange();
    range.selectNodeContents($("#taskLoopText"));
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
