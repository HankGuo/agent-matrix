/* Agent Matrix WebUI — Vue 3 内嵌版：SSE 实时推送 + 120s 兜底轮询 */
"use strict";

/* ---------- 纯工具（模块级，根实例与组件共用） ---------- */

function fmtTime(ts) {
  const d = new Date(ts * 1000);
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function fmtClock(ts) {
  const d = new Date(ts * 1000);
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

/* 刊式短时间：MM-DD HH:MM（任务行右缘） */
function fmtMD(ts) {
  const d = new Date(ts * 1000);
  const p = (n) => String(n).padStart(2, "0");
  return `${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function relTime(ts, now) {
  if (!ts) return "-";
  const diff = Math.max(0, now - ts);
  if (diff < 10) return "刚刚";
  if (diff < 60) return diff + " 秒前";
  if (diff < 3600) return Math.floor(diff / 60) + " 分钟前";
  if (diff < 86400) return Math.floor(diff / 3600) + " 小时前";
  return Math.floor(diff / 86400) + " 天前";
}

function fmtSize(n) {
  return n >= 1048576 ? (n / 1048576).toFixed(1) + " MB" : Math.ceil(n / 1024) + " KB";
}

/* ---------- 状态元数据 ---------- */

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

function taskPill(s) { return taskStatusMeta[s] || ["?", "gray"]; }
function asgPill(s) { return asgStatusMeta[s] || [s || "?", "gray"]; }

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

/* SSE 更新脉冲的变更签名：状态 / 轮次 / 最近一次投递或回写时刻 */
function taskSig(t) {
  let mr = 0;
  for (const a of t.assignments || []) {
    if (a.result_at && a.result_at > mr) mr = a.result_at;
    if (a.delivered_at && a.delivered_at > mr) mr = a.delivered_at;
  }
  return t.status + "|" + maxSeq(t) + "|" + mr;
}

/* 复制到剪贴板：优先异步 clipboard API（安全上下文），
   微信/移动端 webview 回退 execCommand 路径。返回是否成功。 */
async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text);
      return true;
    } catch { /* 落入兜底 */ }
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.cssText = "position:fixed;top:0;left:-9999px;opacity:0;";
  document.body.appendChild(ta);
  ta.select();
  ta.setSelectionRange(0, ta.value.length); // iOS Safari 需要
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch { /* 保持 false */ }
  ta.remove();
  return ok;
}

/* ---------- API：401 一律回到 boot 重新探测会话 ---------- */
let appVm = null;
async function api(path, opts = {}) {
  // FormData 由浏览器自带 boundary，不能手动设 Content-Type
  const isForm = opts.body instanceof FormData;
  const res = await fetch(path, {
    ...(isForm ? {} : { headers: { "Content-Type": "application/json" } }),
    ...opts,
  });
  if (res.status === 401 && path !== "/api/login" && path !== "/api/setup") {
    if (appVm) appVm.boot();
    throw new Error("unauthorized");
  }
  return res;
}

/* 追问草稿按任务隔离：切换任务时暂存/恢复，避免草稿串到别的任务 */
const fuDrafts = new Map();

/* 轻量提示条：toastSeq 代数保证连发时旧回调不会提前隐藏新 toast */
let toastTimer = null;
let toastSeq = 0;

const { createApp } = Vue;

const app = createApp({
  data() {
    return {
      screen: "boot", // boot | setup | login | dash
      envLogin: false,
      useTokenLogin: false,
      version: "",
      setupForm: { user: "", pass: "", pass2: "", baseURL: "" },
      setupError: "",
      loginForm: { user: "", pass: "", token: "" },
      loginError: "",
      view: "tasks", // tasks | agents
      taskFilter: "all", // all | active | done | failed
      agents: [],
      tasks: [],
      agentsLoading: true,
      tasksLoaded: false,
      onlineTimeout: 90,
      serverOffset: 0, // 服务端时钟 - 本地时钟（秒），用于在线状态本地重算
      nowTick: Math.floor(Date.now() / 1000),
      sseState: "connecting", // live | retry
      lastSyncAt: 0,
      animEnter: false,
      pulseIds: [],
      panel: "", // "" | enroll | settings | taskNew | taskDetail
      enrollLabel: "",
      enrollPrompt: "",
      enrolling: false,
      copyPromptLabel: "复制指令",
      settings: { base_url: "", poll_interval: 60 },
      settingsSaved: false,
      newTask: { title: "", content: "" },
      newTaskChecked: [],
      pendingAtts: [], // { file, desc }
      taskNewError: "",
      creating: false,
      currentTaskId: "",
      detail: null, // { task, status, assignments, outputs, inputs }
      fuChecked: [],
      fuContent: "",
      fuError: "",
      fuSending: false,
      copiedAsgId: "",
      copiedAsgLabel: "",
      toastMsg: "",
      toastOn: false,
    };
  },
  computed: {
    nowTs() {
      return this.nowTick + this.serverOffset;
    },
    cols() {
      const c = { active: [], done: [], failed: [] };
      for (const t of this.tasks) {
        if (t.status === "pending" || t.status === "running") c.active.push(t);
        else if (t.status === "done") c.done.push(t);
        else c.failed.push(t); // failed / partial / canceled
      }
      return c;
    },
    counts() {
      const c = this.cols;
      return { active: c.active.length, done: c.done.length, failed: c.failed.length };
    },
    /* KPI 统计卡：在线 Agent 数（随 nowTick 本地重算） */
    onlineCount() {
      return this.agents.filter((a) => this.isOnline(a)).length;
    },
    /* 任务流：按 taskFilter 过滤后按 created_at 倒序，分「今天 / 昨天 / 更早」，
       组内保持倒序；空组不出小节眉 */
    groupedTasks() {
      const f = this.taskFilter;
      const inFilter = (t) => {
        if (f === "active") return t.status === "pending" || t.status === "running";
        if (f === "done") return t.status === "done";
        if (f === "failed") return t.status !== "pending" && t.status !== "running" && t.status !== "done";
        return true;
      };
      const list = this.tasks.filter(inFilter).sort((a, b) => b.created_at - a.created_at);
      const day0 = (ts) => { const d = new Date(ts * 1000); d.setHours(0, 0, 0, 0); return d.getTime(); };
      const today0 = day0(this.nowTs);
      const yest0 = today0 - 86400000;
      const groups = [
        { key: "today", label: "今天", items: [] },
        { key: "yesterday", label: "昨天", items: [] },
        { key: "earlier", label: "更早", items: [] },
      ];
      for (const t of list) {
        const c = t.created_at * 1000;
        groups[c >= today0 ? 0 : c >= yest0 ? 1 : 2].items.push(t);
      }
      return groups.filter((g) => g.items.length).map((g) => ({ ...g, note: g.items.length + " 项" }));
    },
    sortedAgents() {
      const on = (a) => (this.isOnline(a) ? 1 : 0);
      // 在线在前、离线沉底（sort 稳定，组内保持服务端返回顺序）
      return [...this.agents].sort((x, y) => on(y) - on(x));
    },
    /* Agent 的最近一条任务（跨任务取最新一轮指派） */
    latestByAgent() {
      const map = {};
      for (const t of this.tasks) {
        for (const a of t.assignments || []) {
          const key = t.created_at * 1000 + a.seq;
          if (!map[a.agent_id] || key > map[a.agent_id].key) map[a.agent_id] = { key, task: t, asg: a };
        }
      }
      return map;
    },
    /* 详情线程：按 seq 聚合成轮次 */
    rounds() {
      if (!this.detail) return [];
      const map = new Map();
      for (const a of this.detail.assignments || []) {
        if (!map.has(a.seq)) map.set(a.seq, []);
        map.get(a.seq).push(a);
      }
      return [...map.entries()].sort((x, y) => x[0] - y[0]).map(([seq, items]) => ({ seq, items }));
    },
    detailTerminal() {
      return !!this.detail && ["done", "failed", "partial", "canceled"].includes(this.detail.status);
    },
    createLabel() {
      if (!this.creating) return "创建任务";
      return this.pendingAtts.length ? "上传中…" : "创建中…";
    },
  },
  created() {
    this._sigCache = {}; // taskId → 变更签名（非响应式）
    this.pollTimer = null;
    this.es = null;
  },
  mounted() {
    window.addEventListener("hashchange", this.onHash);
    document.addEventListener("keydown", this.onKey);
    // 10s 心跳驱动相对时间与在线徽章的本地重算
    this.tickTimer = setInterval(() => {
      this.nowTick = Math.floor(Date.now() / 1000);
    }, 10000);
    this.boot();
  },
  methods: {
    fmtTime, fmtClock, fmtMD, fmtSize, taskPill, asgPill,
    relTime(ts) { return relTime(ts, this.nowTs); },
    isOnline(a) { return this.nowTs - a.last_seen <= this.onlineTimeout; },

    toast(msg) {
      this.toastMsg = msg;
      this.toastOn = true;
      clearTimeout(toastTimer);
      const seq = ++toastSeq;
      toastTimer = setTimeout(() => {
        if (seq === toastSeq) this.toastOn = false;
      }, 2200);
    },

    /* ---- 启动：探测会话与初始化状态 ---- */
    async boot() {
      if (this.pollTimer) { clearInterval(this.pollTimer); this.pollTimer = null; }
      if (this.es) { this.es.close(); this.es = null; }
      this.sseState = "connecting";
      this.panel = "";
      this.currentTaskId = "";
      this.detail = null;
      let st = null;
      try { st = await (await fetch("/api/auth/status")).json(); } catch { /* 继续走探测 */ }
      if (st) {
        if (st.version) this.version = st.version;
        if (st.base_url) this.setupForm.baseURL = st.base_url;
        this.envLogin = !!st.env_login;
      }
      try {
        const res = await fetch("/api/agents");
        if (res.ok) { this.enterDash(); return; }
      } catch { /* 落入登录/初始化 */ }
      this.setupError = "";
      this.loginError = "";
      this.screen = st && st.needs_setup ? "setup" : "login";
    },

    enterDash() {
      this.screen = "dash";
      this.$nextTick(() => this.applyHash());
      this.loadAgents();
      this.loadTasks();
      this.connectSSE();
      if (this.pollTimer) clearInterval(this.pollTimer);
      // SSE 之外的兜底：长连接静默断线时 120s 自愈一次
      this.pollTimer = setInterval(() => { this.loadAgents(); this.loadTasks(); }, 120000);
    },

    /* ---- SSE：服务端只推 topic，数据仍走 JSON 接口拉取 ---- */
    connectSSE() {
      if (!window.EventSource) return;
      if (this.es) this.es.close();
      const es = new EventSource("/api/events");
      this.es = es;
      es.onopen = () => { this.sseState = "live"; };
      es.onerror = () => { this.sseState = "retry"; }; // EventSource 自动重连
      es.onmessage = (e) => {
        let m;
        try { m = JSON.parse(e.data); } catch { return; }
        if (!m || !m.topic) return;
        if (m.topic === "tasks") {
          this.loadTasks();
          if (this.currentTaskId && this.panel === "taskDetail") this.refreshDetail();
        } else if (m.topic === "agents") {
          this.loadAgents();
        }
      };
    },

    /* ---- 认证 ---- */
    async submitSetup() {
      this.setupError = "";
      const f = this.setupForm;
      if (f.pass !== f.pass2) {
        this.setupError = "两次输入的密码不一致";
        return;
      }
      try {
        const res = await api("/api/setup", {
          method: "POST",
          body: JSON.stringify({ username: f.user.trim(), password: f.pass, base_url: f.baseURL.trim() }),
        });
        if (!res.ok) {
          this.setupError = (await res.json()).error || "初始化失败";
          return;
        }
        this.enterDash();
      } catch {
        this.setupError = "网络错误";
      }
    },

    toggleTokenLogin() {
      this.useTokenLogin = !this.useTokenLogin;
    },

    async submitLogin() {
      this.loginError = "";
      const body = this.useTokenLogin
        ? { token: this.loginForm.token }
        : { username: this.loginForm.user.trim(), password: this.loginForm.pass };
      try {
        const res = await api("/api/login", { method: "POST", body: JSON.stringify(body) });
        if (!res.ok) {
          this.loginError = (await res.json()).error || "登录失败";
          return;
        }
        this.loginForm.token = "";
        this.loginForm.pass = "";
        this.enterDash();
      } catch {
        this.loginError = "网络错误";
      }
    },

    async logout() {
      await api("/api/logout", { method: "POST", body: "{}" });
      this.boot();
    },

    /* ---- 视图路由：#/tasks（默认）、#/agents、#/settings，支持 #/tasks/<id> 直达详情 ---- */
    onHash() {
      if (this.screen === "dash") this.applyHash();
    },
    onKey(e) {
      if (e.key === "Escape") this.closePanels();
    },
    goView(v) { location.hash = "#/" + v; },
    goTask(id) { location.hash = "#/tasks/" + id; },
    applyHash() {
      const h = location.hash;
      const m = h.match(/^#\/tasks\/(tsk_[0-9a-f]+)$/);
      this.view = h.startsWith("#/agents") ? "agents" : h.startsWith("#/settings") ? "settings" : "tasks";
      if (this.view === "settings") this.loadSettings();
      if (m) {
        // 防止 openTaskDetail 内部补写 hash 造成的重入
        if (m[1] !== this.currentTaskId || this.panel !== "taskDetail") this.openTaskDetail(m[1]);
      } else if (this.currentTaskId) {
        this.closePanels(); // 从详情退回列表（浏览器后退等）时关闭面板
      }
    },
    closePanels() {
      this.panel = "";
      if (this.currentTaskId) {
        this.currentTaskId = "";
        if (location.hash.startsWith("#/tasks/")) location.hash = "#/tasks";
      }
    },

    /* ---- 数据加载 ---- */
    async loadAgents() {
      let data;
      try {
        const res = await api("/api/agents");
        if (!res.ok) return;
        data = await res.json();
      } catch {
        this.agentsLoading = false;
        return;
      }
      if (typeof data.online_timeout === "number") this.onlineTimeout = data.online_timeout;
      if (data.server_time) this.serverOffset = data.server_time - Math.floor(Date.now() / 1000);
      this.agents = data.agents || [];
      this.agentsLoading = false;
      this.lastSyncAt = Math.floor(Date.now() / 1000);
    },

    async loadTasks() {
      let data;
      try {
        const res = await api("/api/tasks");
        if (!res.ok) return;
        data = await res.json();
      } catch {
        return;
      }
      const fresh = data.tasks || [];
      const sigs = {};
      const changed = [];
      for (const t of fresh) {
        const sig = taskSig(t);
        sigs[t.id] = sig;
        if (this._sigCache[t.id] !== undefined && this._sigCache[t.id] !== sig) changed.push(t.id);
      }
      this._sigCache = sigs;
      this.tasks = fresh;
      if (this.tasksLoaded && changed.length) {
        this.markPulse(changed);
      } else if (!this.tasksLoaded && fresh.length) {
        // 首次入场 stagger
        this.animEnter = true;
        setTimeout(() => (this.animEnter = false), 1300);
      }
      this.tasksLoaded = true;
      this.lastSyncAt = Math.floor(Date.now() / 1000);
    },

    /* 签名交互：SSE 推送到达时，变更任务行做 1.2s 蓝色 ring 呼吸 */
    markPulse(ids) {
      this.pulseIds = [...new Set([...this.pulseIds, ...ids])];
      const drop = new Set(ids);
      setTimeout(() => {
        this.pulseIds = this.pulseIds.filter((x) => !drop.has(x));
      }, 1250);
    },

    /* ---- 接入面板 ---- */
    openEnroll() {
      this.enrollLabel = "";
      this.enrollPrompt = "";
      this.copyPromptLabel = "复制指令";
      this.panel = "enroll";
      this.$nextTick(() => { const el = this.$refs.enrollLabel; if (el) el.focus(); });
    },

    async genEnrollment() {
      this.enrolling = true;
      try {
        const res = await api("/api/enrollments", {
          method: "POST",
          body: JSON.stringify({ label: this.enrollLabel }),
        });
        if (!res.ok) {
          const d = await res.json().catch(() => ({}));
          this.toast("生成失败" + (d.error ? "：" + d.error : ""));
          return;
        }
        const data = await res.json();
        this.enrollPrompt = data.prompt;
      } catch {
        this.toast("网络错误，请重试");
      } finally {
        this.enrolling = false;
      }
    },

    async copyPrompt() {
      if (await copyText(this.enrollPrompt)) {
        this.copyPromptLabel = "已复制 ✓";
      } else {
        // 剪贴板不可用时选中文本，给用户手动复制
        const el = document.getElementById("promptText");
        if (el) {
          const range = document.createRange();
          range.selectNodeContents(el);
          const sel = window.getSelection();
          sel.removeAllRanges();
          sel.addRange(range);
        }
        this.copyPromptLabel = "已选中，请手动复制";
      }
      setTimeout(() => (this.copyPromptLabel = "复制指令"), 2000);
    },

    /* ---- 设置视图：进入 #/settings 时拉取一次 ---- */
    async loadSettings() {
      try {
        const res = await api("/api/settings");
        if (res.ok) {
          const data = await res.json();
          this.settings.base_url = data.base_url || "";
          this.settings.poll_interval = data.poll_interval || 60;
          if (data.version) this.version = data.version;
        }
      } catch { /* 保持旧值 */ }
      this.settingsSaved = false;
    },

    async saveSettings() {
      const interval = parseInt(this.settings.poll_interval, 10);
      try {
        const res = await api("/api/settings", {
          method: "POST",
          body: JSON.stringify({
            base_url: this.settings.base_url,
            poll_interval: Number.isInteger(interval) ? interval : null,
          }),
        });
        const data = await res.json();
        if (!res.ok) {
          this.toast(data.error || "保存失败");
          return;
        }
        this.settings.base_url = data.base_url;
        this.settings.poll_interval = data.poll_interval;
        this.settingsSaved = true;
        setTimeout(() => (this.settingsSaved = false), 2000);
      } catch {
        this.toast("网络错误，请重试");
      }
    },

    /* ---- 新建任务 ---- */
    openTaskNew() {
      this.newTask = { title: "", content: "" };
      this.newTaskChecked = [];
      this.pendingAtts = [];
      this.taskNewError = "";
      this.panel = "taskNew";
      this.$nextTick(() => { const el = this.$refs.taskTitle; if (el) el.focus(); });
    },

    onAttPicked(e) {
      for (const f of e.target.files) {
        if (this.pendingAtts.length >= 10) break;
        if (f.size > 100 * 1048576) {
          this.toast("「" + f.name + "」超过 100MB，已跳过");
          continue;
        }
        this.pendingAtts.push({ file: f, desc: "" });
      }
      e.target.value = "";
    },

    async createTask() {
      this.taskNewError = "";
      const ids = this.newTaskChecked;
      // 两条路径统一防重：连点不得创建重复任务
      this.creating = true;
      let res;
      try {
        if (this.pendingAtts.length) {
          // 有附件走 multipart：desc_i 与 file_i 按序配对（desc 必须在对应 file 之前）
          const fd = new FormData();
          fd.append("title", this.newTask.title);
          fd.append("content", this.newTask.content);
          fd.append("agent_ids", ids.join(","));
          for (const p of this.pendingAtts) {
            fd.append("desc", p.desc);
            fd.append("file", p.file, p.file.name);
          }
          res = await api("/api/tasks", { method: "POST", body: fd }).catch(() => null);
        } else {
          res = await api("/api/tasks", {
            method: "POST",
            body: JSON.stringify({
              title: this.newTask.title,
              content: this.newTask.content,
              agent_ids: ids,
            }),
          }).catch(() => null);
        }
      } finally {
        this.creating = false;
      }
      if (!res) {
        this.taskNewError = "网络错误，请重试";
        return;
      }
      const data = await res.json();
      if (!res.ok) {
        this.taskNewError = data.error || "创建失败";
        return;
      }
      this.closePanels();
      // 留在任务列表：新任务出现在列表顶部，由用户自己决定何时点进去
      this.loadTasks();
      this.toast(ids.length ? "任务已创建，派发给 " + ids.length + " 个 Agent" : "任务已创建");
    },

    /* ---- 任务详情 ---- */
    async openTaskDetail(id) {
      if (!this.agents.length) await this.loadAgents(); // 继续任务的选择器需要 Agent 名单
      let d;
      try {
        const res = await api("/api/tasks/" + encodeURIComponent(id));
        if (!res.ok) {
          this.toast("任务不存在或加载失败");
          return;
        }
        d = await res.json();
      } catch {
        this.toast("网络错误，请重试");
        return;
      }
      // 确认能加载后再切换状态：先存旧任务草稿，再恢复目标任务草稿
      if (this.currentTaskId && this.currentTaskId !== id && this.fuContent.trim()) {
        fuDrafts.set(this.currentTaskId, this.fuContent);
      }
      this.fuContent = fuDrafts.get(id) || "";
      this.fuError = "";
      this.currentTaskId = id;
      // 继续任务的选择器：默认勾选任务当前名单
      const inTask = new Set((d.assignments || []).map((a) => a.agent_id));
      this.fuChecked = this.agents.filter((a) => inTask.has(a.id)).map((a) => a.id);
      if (location.hash !== "#/tasks/" + id) location.hash = "#/tasks/" + id;
      this.detail = d;
      this.panel = "taskDetail";
      this.$nextTick(() => {
        const b = this.$refs.tdBody;
        if (b) b.scrollTop = b.scrollHeight;
      });
    },

    /* 面板打开时的静默刷新：本来在底部则贴底，否则保持阅读位置。
       （勾选状态由 v-model 持有，天然不被刷新打断） */
    async refreshDetail() {
      if (!this.currentTaskId) return;
      const body = this.$refs.tdBody;
      const stick = body && body.scrollHeight - body.scrollTop - body.clientHeight < 60;
      try {
        const res = await api("/api/tasks/" + encodeURIComponent(this.currentTaskId));
        if (!res.ok) return;
        this.detail = await res.json();
        if (stick) {
          this.$nextTick(() => {
            const b = this.$refs.tdBody;
            if (b) b.scrollTop = b.scrollHeight;
          });
        }
      } catch { /* 忽略 */ }
    },

    outputsOf(asgID) {
      return (this.detail && this.detail.outputs && this.detail.outputs[asgID]) || [];
    },

    asgTimes(a) {
      const parts = [];
      if (a.delivered_at) parts.push("投递于 " + fmtTime(a.delivered_at));
      if (a.result_at) parts.push("回写于 " + fmtTime(a.result_at));
      return parts.length ? parts.join("，") : "等待 Agent 拉取";
    },

    async requeue(a) {
      const res = await api("/api/assignments/" + encodeURIComponent(a.id) + "/requeue", {
        method: "POST",
        body: "{}",
      });
      if (res.ok) {
        this.refreshDetail();
        this.loadTasks();
      }
    },

    async copyResult(a) {
      const ok = await copyText(a.result);
      this.copiedAsgId = a.id;
      this.copiedAsgLabel = ok ? "已复制 ✓" : "复制失败，请长按选择";
      setTimeout(() => {
        if (this.copiedAsgId === a.id) this.copiedAsgId = "";
      }, 2000);
    },

    async followup() {
      this.fuError = "";
      const content = this.fuContent.trim();
      if (!content) {
        this.fuError = "先写点内容";
        return;
      }
      const ids = this.fuChecked;
      if (!ids.length) {
        this.fuError = "至少勾选一个 Agent";
        return;
      }
      this.fuSending = true;
      let res;
      try {
        res = await api("/api/tasks/" + encodeURIComponent(this.currentTaskId) + "/followup", {
          method: "POST",
          body: JSON.stringify({ content, agent_ids: ids }),
        });
      } catch {
        res = null;
      } finally {
        this.fuSending = false;
      }
      if (!res) {
        this.fuError = "网络错误，请重试";
        return;
      }
      if (!res.ok) {
        this.fuError = (await res.json()).error || "发送失败";
        return;
      }
      this.fuContent = "";
      fuDrafts.delete(this.currentTaskId); // 已发送的草稿不再保留
      this.refreshDetail();
      this.loadTasks();
    },

    async deleteTask() {
      if (!this.currentTaskId) return;
      if (!confirm("确定删除该任务？指派、结果与全部附件文件都会一并删除，不可恢复。")) return;
      const res = await api("/api/tasks/" + encodeURIComponent(this.currentTaskId) + "/delete", {
        method: "POST",
        body: "{}",
      });
      if (res.ok) {
        this.closePanels();
        this.loadTasks();
      }
    },

    async cancelTask() {
      if (!this.currentTaskId) return;
      if (!confirm("确定取消该任务？所有未结束的指派都会终止，且不能再追加新轮次。")) return;
      const res = await api("/api/tasks/" + encodeURIComponent(this.currentTaskId) + "/cancel", {
        method: "POST",
        body: "{}",
      });
      if (res.ok) {
        this.refreshDetail();
        this.loadTasks();
      }
    },

    async removeAgent(a) {
      if (!confirm(`确定下线 Agent「${a.name}」(${a.id})？\n\n它会立即从列表消失，并在一分钟左右收到信号、自动卸载本机的定时任务与配置。`)) return;
      let res = null;
      try {
        res = await api("/api/agents/" + encodeURIComponent(a.id), { method: "DELETE" });
      } catch { /* res 保持 null，按网络错误处理 */ }
      if (!res) {
        this.toast("网络错误，请重试");
        return;
      }
      if (!res.ok) {
        const d = await res.json().catch(() => ({}));
        this.toast(d.error || "下线失败，请重试");
        return;
      }
      this.toast(`「${a.name}」已下线，其一分钟左右将自动卸载定时任务与配置`);
      this.loadAgents();
    },
  },
});

/* ---- 任务行 ---- */
app.component("task-card", {
  props: ["t", "i", "now", "enter", "pulse"],
  emits: ["open"],
  methods: { taskPill, asgPill, fmtMD, latestPerAgent, maxSeq, taskActivity, relTime },
  template: `
  <article class="trow" :class="['st-' + t.status, { enter: enter, pulsing: pulse }]" :style="{ '--i': i }" @click="$emit('open', t.id)">
    <div class="trow-main">
      <h3 class="trow-title" :title="t.title">{{ t.title }}</h3>
      <div class="trow-meta">
        <span class="tchip" v-for="a in latestPerAgent(t)" :key="a.id"
              :title="a.agent_name + ' · ' + asgPill(a.status)[0] + (a.stale ? ' · 疑似卡住' : '')">
          <span class="tchip-name">@{{ a.agent_name }}</span><span class="tchip-stale" v-if="a.stale">卡住</span>
        </span>
        <span class="trow-seq mono" v-if="maxSeq(t) > 1">第 {{ maxSeq(t) }} 轮</span>
        <span class="trow-rel mono">{{ relTime(taskActivity(t), now) }}</span>
      </div>
    </div>
    <div class="trow-side">
      <span class="stag" :class="'stag-' + taskPill(t.status)[1]">{{ taskPill(t.status)[0] }}</span>
      <span class="trow-time mono">{{ fmtMD(taskActivity(t)) }}</span>
    </div>
  </article>`,
});

/* ---- Agent 行 ---- */
app.component("agent-card", {
  props: ["a", "now", "timeout", "last"],
  emits: ["remove", "open-task"],
  data() {
    return { menuOpen: false };
  },
  computed: {
    meta() {
      // 能力画像（注册/升级时 Agent 自报的 meta JSON），容错解析
      try { return JSON.parse(this.a.meta || "{}") } catch { return {}; }
    },
    online() { return this.now - this.a.last_seen <= this.timeout; },
    execTag() {
      const m = this.meta;
      return m.executor ? m.executor + (m.executor_version ? " " + m.executor_version : "") : "";
    },
    persona() { return this.meta.persona || ""; },
    skills() { return Array.isArray(this.meta.skills) ? this.meta.skills.slice(0, 20) : []; },
    lastTime() {
      if (!this.last) return "";
      return relTime(this.last.asg.result_at || this.last.asg.delivered_at || this.last.task.created_at, this.now);
    },
    heartbeat() { return relTime(this.a.last_seen, this.now); },
  },
  methods: { asgPill },
  template: `
  <div class="arow" :class="{ off: !online }">
    <span class="dot arow-dot" :class="online ? 'on' : 'off'" :title="online ? '在线' : '离线'"></span>
    <div class="arow-main">
      <div class="arow-top">
        <span class="arow-name" :title="a.name">{{ a.name }}</span>
        <span class="arow-exec mono" v-if="execTag" :title="execTag">{{ execTag }}</span>
      </div>
      <div class="arow-sub" v-if="skills.length || persona">
        <span class="tag mono" v-for="s in skills" :key="s" :title="s">{{ s }}</span>
        <span class="arow-persona" v-if="persona" :title="persona">{{ persona }}</span>
      </div>
    </div>
    <div class="arow-task" v-if="last" :title="'最近任务：' + last.task.title" @click="$emit('open-task', last.task.id)">
      <span class="stag" :class="'stag-' + asgPill(last.asg.status)[1]">{{ asgPill(last.asg.status)[0] }}</span>
      <span class="t">{{ last.task.title }}</span>
      <span class="arow-time mono">{{ lastTime }}</span>
    </div>
    <div class="arow-task none" v-else><span class="muted">暂无任务</span></div>
    <div class="arow-side">
      <span class="arow-hb mono" :title="'最后心跳 ' + heartbeat">{{ heartbeat }}</span>
      <div class="menu-wrap">
        <button class="icon-btn" type="button" aria-label="更多操作" :aria-expanded="menuOpen" @click.stop="menuOpen = !menuOpen">
          <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
            <circle cx="3" cy="8" r="1.5" fill="currentColor"/><circle cx="8" cy="8" r="1.5" fill="currentColor"/><circle cx="13" cy="8" r="1.5" fill="currentColor"/>
          </svg>
        </button>
        <div class="menu-mask" v-if="menuOpen" @click="menuOpen = false"></div>
        <div class="menu" v-if="menuOpen">
          <button class="menu-item danger" type="button" @click="menuOpen = false; $emit('remove', a)">下线 Agent</button>
        </div>
      </div>
    </div>
  </div>`,
});

/* ---- 附件条目：白名单类型内联预览，其余给下载链接 ---- */
app.component("att-item", {
  props: ["att", "idx"],
  computed: {
    url() { return "/api/attachments/" + encodeURIComponent(this.att.id); },
    dlUrl() { return this.url + "?download=1"; },
    kind() {
      const m = this.att.mime || "";
      if (/^image\//.test(m) && m !== "image/svg+xml") return "image";
      if (/^audio\//.test(m)) return "audio";
      if (/^video\//.test(m)) return "video";
      if (m === "application/pdf") return "pdf";
      return "";
    },
  },
  methods: { fmtSize },
  template: `
  <div class="att-item">
    <div class="att-item-head">
      <span class="att-name" :title="att.name">{{ idx != null ? '[附件' + idx + '] ' : '' }}{{ att.name }}</span>
      <span class="sub small">{{ fmtSize(att.size) }}</span>
      <a class="btn text att-dl" :href="dlUrl">下载</a>
    </div>
    <p class="sub small att-desc-view" v-if="att.description">{{ att.description }}</p>
    <img v-if="kind === 'image'" class="att-preview" :src="url" :alt="att.name" loading="lazy">
    <audio v-else-if="kind === 'audio'" class="att-media" controls :src="url"></audio>
    <video v-else-if="kind === 'video'" class="att-media" controls :src="url"></video>
    <a v-else-if="kind === 'pdf'" class="btn text att-dl att-dl-top" :href="url" target="_blank" rel="noopener">预览 PDF</a>
  </div>`,
});

appVm = app.mount("#app");
