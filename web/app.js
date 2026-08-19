/* Agent Matrix WebUI — 任务优先的双看板控制台 */
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
  applyHash();
  loadAgents();
  loadTasks();
  refreshTimer = setInterval(() => {
    loadAgents();
    loadTasks();
    if (currentTaskId && taskDetailPanel.classList.contains("show")) {
      refreshTaskDetail();
    }
  }, 15000);
}

/* ---- 视图路由：#/tasks（默认）与 #/agents，支持 #/tasks/<id> 直达详情 ---- */
let currentView = "tasks";

function setView(v) {
  currentView = v === "agents" ? "agents" : "tasks";
  document.querySelectorAll("#dashTabs .tab").forEach((b) =>
    b.classList.toggle("active", b.dataset.view === currentView));
  $("#agentsView").hidden = currentView !== "agents";
  $("#tasksView").hidden = currentView !== "tasks";
}

function applyHash() {
  const h = location.hash;
  const m = h.match(/^#\/tasks\/(tsk_[0-9a-f]+)$/);
  if (h.startsWith("#/agents")) {
    setView("agents");
  } else {
    setView("tasks");
  }
  if (m) {
    openTaskDetail(m[1], true);
  } else if (currentTaskId) {
    closePanels(); // 从详情退回列表（浏览器后退等）时关闭面板
  }
}

document.querySelectorAll("#dashTabs .tab").forEach((btn) => {
  btn.addEventListener("click", () => {
    location.hash = "#/" + btn.dataset.view;
  });
});
window.addEventListener("hashchange", () => {
  if (!dashView.hidden) applyHash();
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

/* ---- 时间 ---- */

function relTime(ts) {
  if (!ts) return "-";
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

/* ---- 状态元数据 ---- */

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
// 看板分列：进行中 / 已完成 / 失败与取消
const boardCols = { active: ["pending", "running"], done: ["done"], failed: ["failed", "partial", "canceled"] };

function pill(meta) {
  const s = document.createElement("span");
  s.className = "pill " + (meta ? meta[1] : "gray");
  s.textContent = meta ? meta[0] : "?";
  return s;
}

/* 指派状态对应的语义灯 */
function statusDot(status) {
  const d = document.createElement("span");
  d.className = "dot";
  if (status === "done") d.classList.add("on");
  else if (status === "delivered") d.classList.add("pulse");
  else if (status === "failed") d.style.background = "var(--danger)";
  else d.classList.add("off");
  return d;
}

/* ---- 数据缓存 ---- */
let agentsCache = [];
let tasksCache = [];

/* ---- Agent 看板 ---- */

function skeleton() {
  const grid = $("#agentGrid");
  grid.replaceChildren();
  for (let i = 0; i < 3; i++) {
    const card = document.createElement("div");
    card.className = "acard";
    const bar = document.createElement("div");
    bar.className = "sk";
    bar.style.width = "60%";
    const bar2 = document.createElement("div");
    bar2.className = "sk";
    bar2.style.cssText = "width:90%;margin-top:10px";
    card.append(bar, bar2);
    grid.append(card);
  }
}

async function loadAgents() {
  if (firstLoad) skeleton();
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
  agentsCache = data.agents || [];
  renderAgents();
}

function renderAgents() {
  const agents = agentsCache;
  const online = agents.filter((a) => a.online).length;
  $("#agentStat").textContent = agents.length
    ? `${agents.length} 台已纳管 · ${online} 在线 · ${agents.length - online} 离线`
    : "";
  const now = new Date();
  const p = (n) => String(n).padStart(2, "0");
  const tip = `更新于 ${p(now.getHours())}:${p(now.getMinutes())}:${p(now.getSeconds())} · 每 15 秒自动刷新`;
  $("#footSync").textContent = tip;

  const hasAgents = agents.length > 0;
  $("#agentGrid").style.display = hasAgents ? "" : "none";
  $("#emptyTip").hidden = hasAgents;

  const grid = $("#agentGrid");
  grid.replaceChildren();
  for (const a of agents) grid.append(agentCard(a));
}

/* Agent 的最近一条任务（跨任务取最新一轮指派） */
function latestAssignmentOf(agentID) {
  let best = null;
  for (const t of tasksCache) {
    for (const a of t.assignments || []) {
      if (a.agent_id !== agentID) continue;
      const key = t.created_at * 1000 + a.seq;
      if (!best || key > best.key) best = { key, task: t, asg: a };
    }
  }
  return best;
}

function agentCard(a) {
  const card = document.createElement("div");
  card.className = "acard" + (a.online ? "" : " off");

  // 能力画像（注册/升级时 Agent 自报的 meta JSON），容错解析
  let metaInfo = {};
  try { metaInfo = JSON.parse(a.meta || "{}"); } catch { metaInfo = {}; }

  const head = document.createElement("div");
  head.className = "acard-head";
  const nm = document.createElement("span");
  nm.className = "acard-name";
  nm.textContent = a.name;
  nm.title = a.name;
  const chip = document.createElement("span");
  chip.className = "status-chip";
  chip.append(a.online ? Object.assign(document.createElement("span"), { className: "dot on" })
                       : Object.assign(document.createElement("span"), { className: "dot off" }));
  const st = document.createElement("span");
  st.textContent = a.online ? "在线" : "离线";
  chip.append(st);
  head.append(nm, chip);

  const idLine = document.createElement("div");
  idLine.className = "acard-id mono";
  idLine.textContent = a.id;

  // 人设：一句话能力描述，缺失则不占行
  let persona = null;
  if (metaInfo.persona) {
    persona = document.createElement("div");
    persona.className = "acard-persona";
    persona.textContent = metaInfo.persona;
    persona.title = metaInfo.persona;
  }

  const meta = document.createElement("dl");
  meta.className = "acard-meta";
  const execText = metaInfo.executor
    ? metaInfo.executor + (metaInfo.executor_version ? " " + metaInfo.executor_version : "")
    : "-";
  const fields = [
    ["执行器", execText],
    ["模型", metaInfo.model || "-"],
    ["主机", a.hostname || "-"],
    ["系统", a.os ? `${a.os}/${a.arch || "?"}` : "-"],
    ["IP", a.ip || "-"],
    ["最后心跳", relTime(a.last_seen)],
  ];
  for (const [k, v] of fields) {
    const wrap = document.createElement("div");
    const dtx = document.createElement("dt");
    dtx.textContent = k;
    const dd = document.createElement("dd");
    dd.textContent = v;
    dd.title = v;
    wrap.append(dtx, dd);
    meta.append(wrap);
  }

  // 技能 chips
  const skills = Array.isArray(metaInfo.skills) ? metaInfo.skills.slice(0, 20) : [];
  let skillsEl = null;
  if (skills.length) {
    skillsEl = document.createElement("div");
    skillsEl.className = "acard-skills";
    for (const s of skills) {
      const c = document.createElement("span");
      c.className = "chip";
      c.title = String(s);
      const n = document.createElement("span");
      n.textContent = String(s);
      c.append(n);
      skillsEl.append(c);
    }
  }

  // 最近任务行：Agent 视角的「它在干什么」
  const taskLine = document.createElement("div");
  taskLine.className = "acard-task";
  const last = latestAssignmentOf(a.id);
  if (last) {
    taskLine.append(statusDot(last.asg.status));
    const t = document.createElement("span");
    t.className = "t";
    t.textContent = last.task.title;
    t.title = last.task.title;
    const tm = document.createElement("span");
    tm.className = "tcard-time";
    tm.textContent = relTime(last.asg.result_at || last.asg.delivered_at || last.task.created_at);
    taskLine.append(t, tm);
    taskLine.style.cursor = "pointer";
    taskLine.addEventListener("click", () => {
      location.hash = "#/tasks/" + last.task.id;
    });
  } else {
    const none = document.createElement("span");
    none.className = "none";
    none.textContent = "暂无任务";
    taskLine.append(none);
  }

  const foot = document.createElement("div");
  foot.className = "acard-foot";
  const del = document.createElement("button");
  del.className = "btn text acard-del";
  del.textContent = "下线";
  del.addEventListener("click", () => removeAgent(a));
  foot.append(del);

  card.append(head, idLine);
  if (persona) card.append(persona);
  card.append(meta);
  if (skillsEl) card.append(skillsEl);
  card.append(taskLine, foot);
  return card;
}

async function removeAgent(a) {
  if (!confirm(`确定下线 Agent「${a.name}」(${a.id})？\n\n它会立即从列表消失，并在一分钟左右收到信号、自动卸载本机的定时任务与配置。`)) return;
  const res = await api("/api/agents/" + encodeURIComponent(a.id), { method: "DELETE" });
  if (res.ok) loadAgents();
}

/* ---- 面板开关 ---- */

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
  if (currentTaskId) {
    currentTaskId = null;
    if (location.hash.startsWith("#/tasks/")) location.hash = "#/tasks";
  }
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

/* ---- 任务看板 ---- */

let currentSeg = "active";

document.querySelectorAll("#taskSeg .seg-btn").forEach((btn) => {
  btn.addEventListener("click", () => {
    currentSeg = btn.dataset.col;
    document.querySelectorAll("#taskSeg .seg-btn").forEach((b) =>
      b.classList.toggle("active", b === btn));
    renderMobileCol();
  });
});

async function loadTasks() {
  let data;
  try {
    const res = await api("/api/tasks");
    if (!res.ok) return;
    data = await res.json();
  } catch {
    return;
  }
  tasksCache = data.tasks || [];
  renderBoard();
  if (agentsCache.length) renderAgents(); // Agent 卡片的“最近任务”依赖任务数据
}

/* 任务卡的最近活动时间 */
function taskActivity(t) {
  let ts = t.created_at;
  for (const a of t.assignments || []) {
    if (a.delivered_at && a.delivered_at > ts) ts = a.delivered_at;
    if (a.result_at && a.result_at > ts) ts = a.result_at;
  }
  return ts;
}

/* 每个 Agent 最新一轮的指派（卡片上只展示最新状态） */
function latestPerAgent(t) {
  const byAgent = {};
  for (const a of t.assignments || []) {
    if (!byAgent[a.agent_id] || a.seq > byAgent[a.agent_id].seq) byAgent[a.agent_id] = a;
  }
  return Object.values(byAgent).sort((x, y) => x.agent_name.localeCompare(y.agent_name));
}

function maxSeq(t) {
  let s = 0;
  for (const a of t.assignments || []) if (a.seq > s) s = a.seq;
  return s;
}

function taskCardEl(t) {
  const card = document.createElement("div");
  card.className = "tcard";
  card.addEventListener("click", () => {
    location.hash = "#/tasks/" + t.id;
  });

  const top = document.createElement("div");
  top.className = "tcard-top";
  const title = document.createElement("div");
  title.className = "tcard-title";
  title.textContent = t.title;
  top.append(title, pill(taskStatusMeta[t.status]));

  const snippet = document.createElement("div");
  snippet.className = "tcard-snippet";
  snippet.textContent = t.content;

  const foot = document.createElement("div");
  foot.className = "tcard-foot";
  const chips = document.createElement("div");
  chips.className = "chips";
  for (const a of latestPerAgent(t)) {
    const c = document.createElement("span");
    c.className = "chip";
    c.title = a.agent_name + " · " + (asgStatusMeta[a.status] || [a.status])[0] + (a.stale ? " · 疑似卡住" : "");
    c.append(statusDot(a.status));
    const n = document.createElement("span");
    n.textContent = a.agent_name;
    c.append(n);
    chips.append(c);
  }
  const right = document.createElement("span");
  right.className = "tcard-time";
  const rounds = maxSeq(t);
  right.textContent = (rounds > 1 ? rounds + " 轮 · " : "") + relTime(taskActivity(t));
  foot.append(chips, right);

  card.append(top, snippet, foot);
  return card;
}

function renderBoard() {
  const tasks = tasksCache;
  const n = { active: 0, done: 0, failed: 0 };
  for (const t of tasks) {
    for (const col in boardCols) if (boardCols[col].includes(t.status)) n[col]++;
  }
  $("#taskStat").textContent = tasks.length
    ? `${n.active} 进行中 · ${n.done} 已完成 · ${n.failed} 失败/取消，共 ${tasks.length} 个任务`
    : "";
  $("#cntActive").textContent = n.active;
  $("#cntDone").textContent = n.done;
  $("#cntFailed").textContent = n.failed;
  $("#segActive").textContent = n.active;
  $("#segDone").textContent = n.done;
  $("#segFailed").textContent = n.failed;

  const has = tasks.length > 0;
  $("#taskBoard").style.display = has ? "" : "none";
  $("#taskSeg").style.display = has ? "" : "none";
  $("#taskMobile").style.display = has ? "" : "none";
  $("#taskEmpty").hidden = has;

  const cols = { active: $("#colActive"), done: $("#colDone"), failed: $("#colFailed") };
  for (const c in cols) cols[c].replaceChildren();
  for (const t of tasks) {
    for (const col in boardCols) {
      if (boardCols[col].includes(t.status)) {
        cols[col].append(taskCardEl(t));
        break;
      }
    }
  }
  renderMobileCol();
}

function renderMobileCol() {
  const box = $("#taskMobile");
  box.replaceChildren();
  for (const t of tasksCache) {
    if (boardCols[currentSeg].includes(t.status)) box.append(taskCardEl(t));
  }
  if (!box.children.length && tasksCache.length) {
    const p = document.createElement("p");
    p.className = "muted small";
    p.style.padding = "18px 6px";
    p.textContent = "该状态下暂无任务";
    box.append(p);
  }
}

/* ---- 新建任务 ---- */
let pendingAtts = [];

function fmtSize(n) {
  return n >= 1048576 ? (n / 1048576).toFixed(1) + " MB" : Math.ceil(n / 1024) + " KB";
}

function attRow(file) {
  const row = document.createElement("div");
  row.className = "att-row";
  const nm = document.createElement("span");
  nm.className = "att-name";
  nm.textContent = file.name + "（" + fmtSize(file.size) + "）";
  nm.title = file.name;
  const desc = document.createElement("input");
  desc.className = "att-desc";
  desc.placeholder = "说明（可选）：这是什么、要重点关注什么";
  desc.maxLength = 300;
  const rm = document.createElement("button");
  rm.className = "btn text att-rm";
  rm.type = "button";
  rm.textContent = "移除";
  rm.addEventListener("click", () => {
    pendingAtts = pendingAtts.filter((p) => p.file !== file);
    row.remove();
  });
  row.append(nm, desc, rm);
  return { row, desc };
}

$("#btnAddAtt").addEventListener("click", () => $("#attFile").click());
$("#attFile").addEventListener("change", (e) => {
  for (const f of e.target.files) {
    if (pendingAtts.length >= 10) break;
    if (f.size > 100 * 1048576) {
      alert("「" + f.name + "」超过 100MB，已跳过");
      continue;
    }
    const { row, desc } = attRow(f);
    pendingAtts.push({ file: f, descEl: desc });
    $("#attList").append(row);
  }
  e.target.value = "";
});

function agentPickEl(a, checked) {
  const lab = document.createElement("label");
  lab.className = "pick";
  const cb = document.createElement("input");
  cb.type = "checkbox";
  cb.value = a.id;
  cb.checked = checked;
  const dot = document.createElement("span");
  dot.className = "dot " + (a.online ? "on" : "off");
  const nm = document.createElement("span");
  nm.textContent = a.name + (a.online ? "" : "（离线）");
  lab.append(cb, dot, nm);
  return lab;
}

async function openTaskNew() {
  $("#taskTitle").value = "";
  $("#taskContent").value = "";
  $("#taskNewError").hidden = true;
  pendingAtts = [];
  $("#attList").replaceChildren();
  const picker = $("#agentPicker");
  picker.replaceChildren();
  if (!agentsCache.length) {
    const p = document.createElement("p");
    p.className = "muted small";
    p.textContent = "还没有已接入的 Agent，请先在「Agent」页接入。";
    picker.append(p);
  }
  for (const a of agentsCache) picker.append(agentPickEl(a, false));
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
  const btn = $("#btnCreateTask");
  let res;
  if (pendingAtts.length) {
    // 有附件走 multipart：desc_i 与 file_i 按序配对
    const fd = new FormData();
    fd.append("title", $("#taskTitle").value);
    fd.append("content", $("#taskContent").value);
    fd.append("agent_ids", ids.join(","));
    for (const p of pendingAtts) {
      fd.append("desc", p.descEl.value);
      fd.append("file", p.file, p.file.name);
    }
    btn.disabled = true;
    btn.textContent = "上传中…";
    try {
      res = await fetch("/api/tasks", { method: "POST", body: fd, credentials: "same-origin" });
    } catch {
      res = null;
    } finally {
      btn.disabled = false;
      btn.textContent = "创建任务";
    }
  } else {
    res = await api("/api/tasks", {
      method: "POST",
      body: JSON.stringify({
        title: $("#taskTitle").value,
        content: $("#taskContent").value,
        agent_ids: ids,
      }),
    }).catch(() => null);
  }
  if (!res) {
    errEl.textContent = "网络错误，请重试";
    errEl.hidden = false;
    return;
  }
  const data = await res.json();
  if (!res.ok) {
    errEl.textContent = data.error || "创建失败";
    errEl.hidden = false;
    return;
  }
  closePanels();
  // 直接进入新任务的详情线程
  location.hash = "#/tasks/" + data.task.id;
  loadTasks();
});

/* ---- 任务详情：按轮次的对话线程 ---- */
let currentTaskId = null;

async function openTaskDetail(id, fromHash) {
  currentTaskId = id;
  if (!fromHash && location.hash !== "#/tasks/" + id) {
    location.hash = "#/tasks/" + id;
  }
  if (!agentsCache.length) await loadAgents(); // 继续任务的选择器需要 Agent 名单
  try {
    const res = await api("/api/tasks/" + encodeURIComponent(id));
    if (!res.ok) return;
    renderTaskDetail(await res.json(), true);
    taskDetailPanel.classList.add("show");
    panelMask.classList.add("show");
  } catch { /* 网络错误时保持现状 */ }
}

/* 面板打开时的静默刷新：保留滚动位置（本来在底部则贴底） */
async function refreshTaskDetail() {
  if (!currentTaskId) return;
  try {
    const res = await api("/api/tasks/" + encodeURIComponent(currentTaskId));
    if (!res.ok) return;
    renderTaskDetail(await res.json(), false);
  } catch { /* 忽略 */ }
}

/* 附件条目：白名单类型内联预览，其余给下载链接 */
function attEl(a, idx) {
  const wrap = document.createElement("div");
  wrap.className = "att-item";
  const head = document.createElement("div");
  head.className = "att-item-head";
  const nm = document.createElement("span");
  nm.className = "att-name";
  nm.textContent = (idx != null ? "[附件" + idx + "] " : "") + a.name;
  nm.title = a.name;
  const size = document.createElement("span");
  size.className = "sub small";
  size.textContent = fmtSize(a.size);
  const link = document.createElement("a");
  link.className = "btn text att-dl";
  link.href = "/api/attachments/" + encodeURIComponent(a.id) + "?download=1";
  link.textContent = "下载";
  head.append(nm, size, link);
  wrap.append(head);
  if (a.description) {
    const d = document.createElement("p");
    d.className = "sub small att-desc-view";
    d.textContent = a.description;
    wrap.append(d);
  }
  const url = "/api/attachments/" + encodeURIComponent(a.id);
  if (/^image\//.test(a.mime) && a.mime !== "image/svg+xml") {
    const img = document.createElement("img");
    img.className = "att-preview";
    img.src = url;
    img.alt = a.name;
    img.loading = "lazy";
    wrap.append(img);
  } else if (/^audio\//.test(a.mime)) {
    const au = document.createElement("audio");
    au.controls = true;
    au.src = url;
    au.className = "att-media";
    wrap.append(au);
  } else if (/^video\//.test(a.mime)) {
    const v = document.createElement("video");
    v.controls = true;
    v.src = url;
    v.className = "att-media";
    wrap.append(v);
  } else if (a.mime === "application/pdf") {
    const a2 = document.createElement("a");
    a2.className = "btn text att-dl";
    a2.href = url;
    a2.target = "_blank";
    a2.rel = "noopener";
    a2.textContent = "预览 PDF";
    wrap.append(a2);
  }
  return wrap;
}

function renderTaskDetail(d, scrollToEnd) {
  $("#tdTitle").textContent = d.task.title;
  $("#tdStatus").replaceChildren(pill(taskStatusMeta[d.status]));
  $("#tdId").textContent = d.task.id;
  $("#tdTime").textContent =
    "创建于 " + fmtTime(d.task.created_at) +
    (d.task.canceled_at ? "，取消于 " + fmtTime(d.task.canceled_at) : "");

  const body = $("#tdBody");
  const stickToEnd = scrollToEnd || body.scrollHeight - body.scrollTop - body.clientHeight < 60;

  // 按 seq 聚合成轮次
  const rounds = new Map();
  for (const a of d.assignments || []) {
    if (!rounds.has(a.seq)) rounds.set(a.seq, []);
    rounds.get(a.seq).push(a);
  }
  const seqs = [...rounds.keys()].sort((x, y) => x - y);
  const outputs = d.outputs || {};
  const inputs = d.inputs || [];

  const thread = $("#tdThread");
  thread.replaceChildren();
  for (const seq of seqs) {
    const group = rounds.get(seq);
    const roundEl = document.createElement("div");
    roundEl.className = "round";

    const label = document.createElement("div");
    label.className = "round-label";
    label.textContent = seqs.length > 1 ? "第 " + seq + " 轮 / 共 " + seqs.length + " 轮" : "任务指令";
    roundEl.append(label);

    const instr = document.createElement("div");
    instr.className = "instr";
    const who = document.createElement("span");
    who.className = "who";
    who.textContent = "你 · " + fmtTime(group[0].created_at || d.task.created_at);
    instr.append(who, document.createTextNode(group[0].content));
    roundEl.append(instr);

    // 附件只在首轮指令下展示（输入件挂在任务上）
    if (seq === 1 && inputs.length) {
      inputs.forEach((a, i) => roundEl.append(attEl(a, i + 1)));
    }

    for (const a of group) roundEl.append(assignBlock(a, outputs[a.id] || []));
    thread.append(roundEl);
  }

  const terminal = ["done", "failed", "partial", "canceled"].includes(d.status);
  $("#btnCancelTask").hidden = terminal;
  // 已取消的任务不能继续追加
  $("#tdComposer").hidden = d.task.canceled_at != null;

  // 继续任务的 Agent 选择器：默认勾选任务当前名单，可拉上其他在线 Agent
  renderFollowupPicker(d);

  if (stickToEnd) body.scrollTop = body.scrollHeight;
}

function assignBlock(a, outs) {
  const div = document.createElement("div");
  div.className = "asgn st-" + a.status;

  const head = document.createElement("div");
  head.className = "asgn-head";
  head.append(statusDot(a.status));
  const nm = document.createElement("span");
  nm.className = "asgn-name";
  nm.textContent = a.agent_name + (a.online ? "" : "（离线）");
  head.append(nm, pill(asgStatusMeta[a.status]));
  if (a.stale) head.append(pill(["疑似卡住", "amber"]));

  const times = document.createElement("p");
  times.className = "sub small";
  const parts = [];
  if (a.delivered_at) parts.push("投递于 " + fmtTime(a.delivered_at));
  if (a.result_at) parts.push("回写于 " + fmtTime(a.result_at));
  if (!parts.length) parts.push("等待 Agent 拉取");
  times.textContent = parts.join("，");

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
        refreshTaskDetail();
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
  if (outs && outs.length) {
    const oh = document.createElement("p");
    oh.className = "sub small att-out-head";
    oh.textContent = "产出文件（" + outs.length + "）";
    div.append(oh);
    for (const o of outs) div.append(attEl(o, null));
  }
  return div;
}

/* 继续任务：选择器与提交 */
function renderFollowupPicker(d) {
  const picker = $("#fuPicker");
  const inTask = new Set();
  for (const a of d.assignments || []) inTask.add(a.agent_id);
  // 已勾选的尽量保留（静默刷新时不打断用户编辑）
  const prevChecked = new Set([...picker.querySelectorAll("input:checked")].map((i) => i.value));
  const keep = picker.dataset.taskId === d.task.id && prevChecked.size > 0;
  picker.dataset.taskId = d.task.id;
  picker.replaceChildren();
  for (const a of agentsCache) {
    const checked = keep ? prevChecked.has(a.id) : inTask.has(a.id);
    picker.append(agentPickEl(a, checked));
  }
  if (!agentsCache.length) {
    const p = document.createElement("span");
    p.className = "muted small";
    p.textContent = "无可用 Agent";
    picker.append(p);
  }
}

$("#btnFollowup").addEventListener("click", async () => {
  const errEl = $("#fuError");
  errEl.hidden = true;
  const content = $("#fuContent").value.trim();
  if (!content) {
    errEl.textContent = "先写点内容";
    errEl.hidden = false;
    return;
  }
  const ids = [...document.querySelectorAll("#fuPicker input:checked")].map((i) => i.value);
  if (!ids.length) {
    errEl.textContent = "至少勾选一个 Agent";
    errEl.hidden = false;
    return;
  }
  const btn = $("#btnFollowup");
  btn.disabled = true;
  btn.textContent = "发送中…";
  let res;
  try {
    res = await api("/api/tasks/" + encodeURIComponent(currentTaskId) + "/followup", {
      method: "POST",
      body: JSON.stringify({ content, agent_ids: ids }),
    });
  } catch {
    res = null;
  } finally {
    btn.disabled = false;
    btn.textContent = "发送";
  }
  if (!res) {
    errEl.textContent = "网络错误，请重试";
    errEl.hidden = false;
    return;
  }
  if (!res.ok) {
    errEl.textContent = (await res.json()).error || "发送失败";
    errEl.hidden = false;
    return;
  }
  $("#fuContent").value = "";
  refreshTaskDetail();
  loadTasks();
});

$("#btnTdClose").addEventListener("click", closePanels);

$("#btnDeleteTask").addEventListener("click", async () => {
  if (!currentTaskId) return;
  if (!confirm("确定删除该任务？指派、结果与全部附件文件都会一并删除，不可恢复。")) return;
  const res = await api("/api/tasks/" + encodeURIComponent(currentTaskId) + "/delete", {
    method: "POST",
    body: "{}",
  });
  if (res.ok) {
    closePanels();
    loadTasks();
  }
});

$("#btnCancelTask").addEventListener("click", async () => {
  if (!currentTaskId) return;
  if (!confirm("确定取消该任务？所有未结束的指派都会终止，且不能再追加新轮次。")) return;
  const res = await api("/api/tasks/" + encodeURIComponent(currentTaskId) + "/cancel", {
    method: "POST",
    body: "{}",
  });
  if (res.ok) {
    refreshTaskDetail();
    loadTasks();
  }
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
