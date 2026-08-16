/* Harness Web UI 前端（零依赖原生 JS）
   状态模型（feat/webui 计划）：
   - timeline：元素数组（msg-<序号> / tool-<call_id> / system）
   - stream：单一流式块（Thinking/Text 分离累积；⚠ delta 事件无 MsgID，
     严禁按 msg_id 关联——对位 TUI StreamState）
   - reconnecting：重拉期间丢弃在途增量（防全量/增量交错）
   - overlay 互斥 + pendingOverlays FIFO（select/approval/ask/help 统一排队） */

"use strict";

const $ = (id) => document.getElementById(id);

const state = {
  sessions: [],
  active: null,
  timeline: [], // {kind, id, ...}
  stream: null, // {el, textEl, thinkingEl, text, thinking}
  running: false,
  queueLen: 0,
  overlay: null, // {type, ...}
  pendingOverlays: [],
  reconnecting: false,
  expanded: {}, // id → true（展开状态保留）
  msgSeq: 0, // 本地消息序号（流式消息用；state 重拉后重置）
};

/* ---------- DOM 引用 ---------- */
const timelineEl = $("timeline");
const timelineInner = document.createElement("div");
timelineInner.className = "timeline-inner";
timelineEl.appendChild(timelineInner);
const inputEl = $("input");
const emptyState = $("empty-state");
const offlineBanner = $("offline-banner");
const overlayRoot = $("overlay-root");
const overlayPanel = $("overlay-panel");
const toastEl = $("toast");
const completionEl = $("completion");

/* 命令目录（对位 TUI commandCatalog：completion 弹窗数据源）。 */
const commandCatalog = [
  { name: "switch", short: "切换会话" },
  { name: "model", short: "切换模型" },
  { name: "effort", short: "设置推理档位" },
  { name: "thinking", short: "开启/关闭思考" },
  { name: "permission", short: "设置审批模式" },
  { name: "plan", short: "切换规划模式 / 查看计划" },
  { name: "subagents", short: "查看子 agent" },
  { name: "usage", short: "查看用量" },
  { name: "compact", short: "压缩上下文" },
  { name: "rename", short: "重命名会话" },
  { name: "help", short: "命令与说明" },
  { name: "exit", short: "退出服务" },
];

let completionIndex = -1; // 当前高亮候选（-1 = 无）

/* ---------- 命令补全（对位 TUI completionView） ---------- */

// completionItems 按输入前缀过滤命令目录；非 / 开头或含空格返回空。
function completionItems(value) {
  if (!value.startsWith("/")) return [];
  const prefix = value.slice(1);
  if (prefix.includes(" ")) return [];
  return commandCatalog.filter((c) => c.name.startsWith(prefix));
}

// completionVisible 报告补全弹窗是否应显示（前缀部分匹配且非精确命中）。
function completionVisible() {
  const value = inputEl.value.trim();
  if (!value.startsWith("/") || value.includes(" ")) return false;
  const items = completionItems(value);
  if (items.length === 0) return false;
  const prefix = value.slice(1);
  return !items.some((c) => c.name === prefix); // 已精确输入完整命令 → 不弹
}

// renderCompletion 渲染候选列表（↑/↓ 高亮跟随 completionIndex）。
function renderCompletion() {
  if (!completionVisible()) {
    completionEl.hidden = true;
    completionIndex = -1;
    return;
  }
  const items = completionItems(inputEl.value.trim());
  if (completionIndex < 0 || completionIndex >= items.length) completionIndex = 0;
  completionEl.innerHTML = "";
  items.forEach((c, i) => {
    const row = document.createElement("div");
    row.className = "completion-item" + (i === completionIndex ? " selected" : "");
    row.innerHTML = '<span class="completion-cmd">/' + c.name + "</span>" +
      '<span class="completion-short">' + esc(c.short) + "</span>";
    row.addEventListener("click", () => acceptCompletion(i));
    row.addEventListener("mousemove", () => {
      if (completionIndex !== i) { completionIndex = i; renderCompletion(); }
    });
    completionEl.appendChild(row);
  });
  completionEl.hidden = false;
}

// moveCompletion 上下移动高亮（delta ±1，循环）。
function moveCompletion(delta) {
  const items = completionItems(inputEl.value.trim());
  if (items.length === 0) return;
  completionIndex = (completionIndex + delta + items.length) % items.length;
  renderCompletion();
}

// acceptCompletion 把选中命令补全进输入框（"/name "，光标末尾）。
function acceptCompletion(idx) {
  const items = completionItems(inputEl.value.trim());
  if (idx === undefined) idx = completionIndex;
  if (idx < 0 || idx >= items.length) return;
  inputEl.value = "/" + items[idx].name + " ";
  inputEl.selectionStart = inputEl.selectionEnd = inputEl.value.length;
  completionEl.hidden = true;
  completionIndex = -1;
  inputEl.focus();
  updateComposerHeight();
}

// updateComposerHeight 输入框随内容行数增长（对位 TUI updateComposerHeight）。
function updateComposerHeight() {
  inputEl.style.height = "auto";
  inputEl.style.height = Math.min(inputEl.scrollHeight, 140) + "px";
}

/* ---------- 工具函数 ---------- */
function esc(s) {
  const d = document.createElement("div");
  d.textContent = s == null ? "" : String(s);
  return d.innerHTML;
}

function toast(msg, isErr) {
  toastEl.textContent = msg;
  toastEl.className = isErr ? "err" : "";
  toastEl.hidden = false;
  clearTimeout(toast._t);
  toast._t = setTimeout(() => { toastEl.hidden = true; }, 2600);
}

function scrollToBottom() {
  if (timelineEl.scrollHeight - timelineEl.scrollTop - timelineEl.clientHeight < 200) {
    timelineEl.scrollTop = timelineEl.scrollHeight;
  }
}

function fmtTokens(n) {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return Math.floor(n / 1e3) + "k";
  return String(n);
}

function shortID(id) {
  return id && id.length > 12 ? id.slice(0, 12) : id;
}

/* ---------- API ---------- */
async function api(path, body) {
  const opts = { method: "POST", headers: { "Content-Type": "application/json" } };
  if (body !== undefined) opts.body = JSON.stringify(body);
  const res = await fetch(path, opts);
  let data = null;
  try { data = await res.json(); } catch (e) { /* 非 JSON */ }
  if (!res.ok) {
    const err = data && data.error ? data.error : ("HTTP " + res.status);
    throw Object.assign(new Error(err), { status: res.status, data });
  }
  return data;
}

/* ---------- 会话栏 ---------- */
function renderSessions() {
  const list = $("session-list");
  list.innerHTML = "";
  // 倒序显示（最新在前，对位 TUI switchItems）
  const sessions = state.sessions.slice().reverse();
  for (const s of sessions) {
    const item = document.createElement("div");
    item.className = "session-item" + (state.active && s.id === state.active.id ? " active" : "");
    item.innerHTML =
      '<span class="session-dot"></span>' +
      '<span class="session-name">' + esc(s.name || shortID(s.id)) + "</span>" +
      (s.model ? '<span class="session-model">' + esc(s.model) + "</span>" : "");
    item.title = s.id + (s.model ? " · " + s.model : "");
    item.addEventListener("click", () => switchSession(s.id));
    list.appendChild(item);
  }
}

async function switchSession(id) {
  try {
    await api("/api/switch", { session_id: id });
    // state_changed{switch} 事件会触发重拉；此处不重复拉
  } catch (e) {
    toast(e.message, true);
  }
}

async function newSession() {
  try {
    await api("/api/new", {});
  } catch (e) {
    toast(e.message, true);
  }
}

/* ---------- 状态栏 ---------- */
function renderStatusbar() {
  const a = state.active;
  $("status-session").textContent = a ? (a.name || shortID(a.id) || "New session") : "New session";
  $("status-plan").hidden = !(a && a.plan);
  $("status-subview").hidden = !(a && a.viewing_subagent);
  $("status-model").textContent = a && a.model ? a.model : "";
  $("status-model").hidden = !(a && a.model);
  $("status-effort").textContent = a && a.effort ? "effort:" + a.effort : "";
  $("status-effort").hidden = !(a && a.effort);
  $("status-permission").textContent = a && a.permission ? a.permission : "";
  $("status-permission").hidden = !(a && a.permission);
  $("status-todo").textContent = a && a.todos && a.todos.length ? "todo:" + a.todos.length : "";
  $("status-todo").hidden = !(a && a.todos && a.todos.length);

  const ct = a ? a.context_tokens : 0;
  const cw = a ? a.context_window : 0;
  const pct = cw > 0 ? Math.min(100, Math.round((ct / cw) * 100)) : 0;
  $("ctx-fill").style.width = pct + "%";
  $("ctx-text").textContent = ct > 0 ? fmtTokens(ct) + "/" + fmtTokens(cw) : "";

  // composer 状态：子 agent 查看禁用
  const viewing = !!(a && a.viewing_subagent);
  inputEl.disabled = viewing;
  $("btn-back").hidden = !viewing;
  $("btn-send").hidden = viewing;
  inputEl.placeholder = viewing
    ? "查看子 agent（只读）· 点击“← 返回”回到父会话"
    : "Ask Harness anything  （/help 查看命令）";
}

/* ---------- Timeline 渲染 ---------- */
function makeMessageEl(item) {
  const wrap = document.createElement("div");
  wrap.className = "msg " + (item.role === "user" ? "msg-user" : "msg-assistant");
  wrap.dataset.id = item.id;
  const avatar = item.role === "user" ? "你" : "AI";
  wrap.innerHTML =
    '<div class="msg-avatar">' + avatar + "</div>" +
    '<div class="msg-body">' +
    '<div class="msg-role">' + (item.role === "user" ? "You" : "Harness") + "</div>" +
    '<div class="msg-content"></div>' +
    "</div>";
  return wrap;
}

function appendMessage(item) {
  const el = makeMessageEl(item);
  timelineInner.appendChild(el);
  item.el = el;
  state.timeline.push(item);
  const contentEl = el.querySelector(".msg-content");
  if (item.html) contentEl.innerHTML = item.html;
  else contentEl.textContent = item.text || "";
  if (!item.done) el.classList.add("msg-streaming");
  scrollToBottom();
  return el;
}

function appendSystem(text, isErr) {
  const el = document.createElement("div");
  el.className = "sysline" + (isErr ? " err" : "");
  el.textContent = text;
  timelineInner.appendChild(el);
  state.timeline.push({ kind: "system", id: "sys-" + state.timeline.length, text, error: isErr });
  scrollToBottom();
  return el;
}

/* 流式块（单一 stream；delta 无 MsgID，只操作当前 stream） */
function ensureStream() {
  if (state.stream) return state.stream;
  const item = {
    kind: "message",
    role: "assistant",
    id: "msg-stream-" + state.msgSeq++,
    text: "",
    thinking: "",
    done: false,
  };
  const el = makeMessageEl(item);
  timelineInner.appendChild(el);
  el.classList.add("msg-streaming");
  const body = el.querySelector(".msg-body");
  const thinkingEl = document.createElement("div");
  thinkingEl.className = "thinking";
  thinkingEl.hidden = true;
  const textEl = document.createElement("div");
  textEl.className = "msg-content";
  body.appendChild(thinkingEl);
  body.appendChild(textEl);
  item.el = el;
  item.textEl = textEl;
  item.thinkingEl = thinkingEl;
  state.timeline.push(item);
  state.stream = item;
  scrollToBottom();
  return item;
}

function flushStream() {
  const s = state.stream;
  if (!s) return;
  state.stream = null;
  s.done = true;
  s.el.classList.remove("msg-streaming");
  if (s.thinking) {
    s.thinkingEl.hidden = false;
    s.thinkingEl.innerHTML =
      '<button class="thinking-toggle" data-expanded="0">💭 已思考 · 点击展开</button>' +
      '<div class="thinking-body" hidden></div>';
    s.thinkingEl.querySelector(".thinking-toggle").addEventListener("click", (e) => {
      const body = s.thinkingEl.querySelector(".thinking-body");
      const expanded = e.target.dataset.expanded === "1";
      e.target.dataset.expanded = expanded ? "0" : "1";
      e.target.textContent = expanded ? "💭 已思考 · 点击展开" : "💭 收起";
      body.hidden = expanded;
      body.textContent = s.thinking;
    });
  } else {
    s.thinkingEl.remove();
  }
  if (s.text) {
    // 块完成：用后端渲染的 HTML（html 由 SSE 附加字段提供）
    if (s.html) s.textEl.innerHTML = s.html;
    else s.textEl.textContent = s.text;
  } else if (!s.thinking) {
    s.el.remove(); // 空块（纯工具回合）
  }
  scrollToBottom();
}

/* 工具块 */
function makeToolEl(item) {
  const wrap = document.createElement("div");
  wrap.className = "tool-block running";
  wrap.dataset.id = item.id;
  wrap.innerHTML =
    '<div class="tool-head">' +
    '<span class="tool-indicator"></span>' +
    '<span class="tool-summary"></span>' +
    '<span class="tool-chevron">▶</span>' +
    "</div>" +
    '<div class="tool-body"></div>';
  wrap.querySelector(".tool-head").addEventListener("click", () => {
    wrap.classList.toggle("expanded");
    state.expanded[item.id] = wrap.classList.contains("expanded");
  });
  item.el = wrap;
  item.bodyEl = wrap.querySelector(".tool-body");
  return wrap;
}

function updateToolEl(item, view) {
  item.summary = view.summary || item.summary;
  item.el.querySelector(".tool-summary").textContent = item.summary || "";
  const body = item.bodyEl;
  body.innerHTML = "";
  if (view.content) {
    const pre = document.createElement("div");
    pre.textContent = view.content;
    body.appendChild(pre);
  }
  if (view.diff) {
    const d = document.createElement("div");
    d.innerHTML = view.diff;
    body.appendChild(d);
  }
  if (view.args) {
    const args = document.createElement("div");
    args.className = "tool-args";
    args.textContent = "参数：" + view.args;
    body.appendChild(args);
  }
  item.el.className = "tool-block " + (view.failed ? "failed" : "ok");
  if (state.expanded[item.id]) item.el.classList.add("expanded");
}

function onToolCall(ev) {
  const item = {
    kind: "tool",
    id: "tool-" + ev.ToolCall.id,
    name: ev.ToolCall.name,
    summary: ev.summary || ev.ToolCall.name,
    done: false,
  };
  const el = makeToolEl(item);
  timelineInner.appendChild(el);
  el.querySelector(".tool-summary").textContent = item.summary;
  state.timeline.push(item);
  scrollToBottom();
}

function onToolResult(ev, toolView) {
  const id = "tool-" + ev.ToolCall.id;
  const item = state.timeline.find((t) => t.kind === "tool" && t.id === id);
  if (!item) return;
  item.done = true;
  updateToolEl(item, toolView || { failed: !ev.ToolResult.success, content: ev.ToolResult.content });
}

/* ---------- agent 事件处理（增量） ---------- */
function handleAgentEvent(ev, extra) {
  switch (ev.Type) {
    case "turn_start":
      break; // 首个 delta 时建 stream
    case "thinking_delta":
      ensureStream().thinking += ev.Text;
      break;
    case "text_delta":
      ensureStream().text += ev.Text;
      ensureStream().textEl.textContent = ensureStream().text; // 流式期转义纯文本
      break;
    case "thinking_done":
      ensureStream().thinking = ev.Text;
      break;
    case "text_done":
      ensureStream().text = ev.Text;
      ensureStream().html = extra.html || "";
      break;
    case "tool_call":
      flushStream();
      onToolCall(ev);
      break;
    case "tool_result":
      onToolResult(ev, extra.tool);
      break;
    case "turn_done":
      flushStream();
      break;
    case "compact_start":
      appendSystem("正在压缩上下文…", false);
      break;
    case "compacted":
      appendSystem("上下文已压缩", false);
      break;
    case "notice":
      appendSystem(ev.Text, false);
      break;
    case "error":
      flushStream();
      appendSystem("Error: " + (extra.err_text || "unknown"), true);
      break;
    case "usage":
      // 用量已由后端写入 AgentState；下次 state 刷新展示
      break;
  }
}

/* ---------- 弹窗 ---------- */
function openOverlay(ovl) {
  if (state.overlay) {
    state.pendingOverlays.push(ovl);
    toast("已排队（等待当前弹窗关闭）");
    return;
  }
  state.overlay = ovl;
  renderOverlay(ovl);
  overlayRoot.hidden = false;
  const first = overlayPanel.querySelector("button, input, textarea");
  if (first) first.focus();
}

function closeOverlay() {
  state.overlay = null;
  overlayRoot.hidden = true;
  overlayPanel.innerHTML = "";
  if (state.pendingOverlays.length) {
    openOverlay(state.pendingOverlays.shift());
  }
  inputEl.focus();
}

function renderOverlay(ovl) {
  overlayPanel.innerHTML = "";
  const title = document.createElement("div");
  title.className = "overlay-title";
  title.textContent = ovl.title || "";
  const body = document.createElement("div");
  body.className = "overlay-body";
  const footer = document.createElement("div");
  footer.className = "overlay-footer";
  overlayPanel.appendChild(title);
  overlayPanel.appendChild(body);
  overlayPanel.appendChild(footer);

  switch (ovl.type) {
    case "approval": renderApproval(ovl, body, footer); break;
    case "ask": renderAsk(ovl, body, footer); break;
    case "select": renderSelect(ovl, body, footer); break;
    case "help": renderHelp(body, footer); break;
  }
}

function renderApproval(ovl, body, footer) {
  const req = ovl.req;
  const agentPrefix = req.agent_id ? "【子 agent " + req.agent_id + "】" : "";
  body.innerHTML =
    '<div class="approval-detail">' +
    agentPrefix + ' 工具 <span class="tool">' + esc(req.tool) + "</span>" +
    ' 请求执行（模式 <span class="approval-mode">' + esc(req.mode) + "</span>）" +
    (req.summary ? '<div class="summary">' + esc(req.summary) + "</div>" : "") +
    "</div>";
  const allow = mkBtn("允许 (y)");
  allow.addEventListener("click", () => approve(ovl.request_id, "allow"));
  const session = mkBtn("本会话记住 (s)");
  session.addEventListener("click", () => approve(ovl.request_id, "session"));
  const deny = mkBtn("拒绝 (n)", true);
  deny.addEventListener("click", () => approve(ovl.request_id, "deny"));
  footer.append(deny, session, allow);
}

async function approve(requestId, decision) {
  try {
    await api("/api/approve", { request_id: requestId, decision });
    closeOverlay();
  } catch (e) {
    // 已在其他页面处理（404）→ 关闭弹窗提示
    closeOverlay();
    toast(e.status === 404 ? "该请求已在其他页面处理" : e.message, true);
  }
}

function renderAsk(ovl, body, footer) {
  const req = ovl.req;
  body.innerHTML =
    '<div class="ask-question">' + esc(req.question) + "</div>" +
    '<div class="ask-options"></div>';
  if (req.allow_custom) {
    const custom = document.createElement("input");
    custom.className = "ask-custom";
    custom.placeholder = "自定义回答（可选）";
    custom.addEventListener("keydown", (e) => {
      if (e.key === "Enter") submitAsk(ovl);
    });
    body.appendChild(custom);
    ovl.customEl = custom;
  }
  const options = body.querySelector(".ask-options");
  ovl.selected = new Set();
  (req.options || []).forEach((opt, i) => {
    const b = mkBtn(opt.label);
    if (opt.description) b.innerHTML += '<span class="desc">' + esc(opt.description) + "</span>";
    b.classList.add("ask-option");
    b.addEventListener("click", () => {
      if (req.multiple) {
        if (ovl.selected.has(i)) { ovl.selected.delete(i); b.classList.remove("selected"); }
        else { ovl.selected.add(i); b.classList.add("selected"); }
      } else {
        submitAsk(ovl, [opt.label]);
      }
    });
    options.appendChild(b);
  });
  const cancel = mkBtn("取消", true);
  cancel.addEventListener("click", () => submitAsk(ovl, []));
  footer.appendChild(cancel);
  if (req.allow_custom) {
    const submit = mkBtn("提交");
    submit.addEventListener("click", () => submitAsk(ovl));
    footer.appendChild(submit);
  }
}

async function submitAsk(ovl, selection) {
  let sel = selection;
  let custom = "";
  if (ovl.customEl && ovl.customEl.value.trim()) {
    custom = ovl.customEl.value.trim();
  }
  if (!sel && ovl.selected && ovl.selected.size) {
    sel = [...ovl.selected].map((i) => (ovl.req.options[i] || {}).label);
  }
  if (!sel && !custom && (ovl.req.options || []).length === 0 && !ovl.req.allow_custom) {
    return; // 无选项无自定义，空提交
  }
  try {
    await api("/api/ask", { request_id: ovl.request_id, selection: sel || [], custom });
    closeOverlay();
  } catch (e) {
    closeOverlay();
    toast(e.status === 404 ? "该提问已在其他页面处理" : e.message, true);
  }
}

function renderSelect(ovl, body, footer) {
  const list = document.createElement("div");
  list.className = "select-list";
  (ovl.items || []).forEach((item) => {
    const b = document.createElement("button");
    b.className = "select-item" + (item.value === ovl.current ? " current" : "");
    b.innerHTML =
      '<span class="current-mark">' + (item.value === ovl.current ? "✓ " : "") + "</span>" +
      esc(item.label) +
      (item.description ? '<span class="desc">' + esc(item.description) + "</span>" : "");
    b.addEventListener("click", () => submitCommand(ovl.command + " " + item.value));
    list.appendChild(b);
  });
  body.appendChild(list);
  const cancel = mkBtn("取消", true);
  cancel.addEventListener("click", closeOverlay);
  footer.appendChild(cancel);
}

function renderHelp(body, footer) {
  const commands = [
    ["/switch", "切换会话"],
    ["/model", "切换模型"],
    ["/effort", "设置推理档位"],
    ["/thinking", "开启/关闭思考"],
    ["/permission", "设置审批模式"],
    ["/plan", "切换规划模式 / 查看计划"],
    ["/subagents", "查看子 agent"],
    ["/usage", "查看用量"],
    ["/compact", "压缩上下文"],
    ["/rename", "重命名会话"],
    ["/exit", "退出服务"],
  ];
  const grid = document.createElement("div");
  grid.className = "help-grid";
  for (const [cmd, desc] of commands) {
    const c = document.createElement("span");
    c.className = "cmd";
    c.textContent = cmd;
    const d = document.createElement("span");
    d.className = "desc";
    d.textContent = desc;
    grid.append(c, d);
  }
  body.appendChild(grid);
  const close = mkBtn("关闭");
  close.addEventListener("click", closeOverlay);
  footer.appendChild(close);
}

function mkBtn(text, ghost) {
  const b = document.createElement("button");
  b.className = "btn" + (ghost ? " btn-ghost" : "");
  b.textContent = text;
  return b;
}

/* ---------- 输入提交 ---------- */
async function submitCommand(line) {
  closeOverlay();
  const res = await sendInput(line);
  handleInputResult(res);
}

async function sendInput(line) {
  try {
    return await api("/api/input", { line });
  } catch (e) {
    toast(e.message, true);
    return null;
  }
}

function handleInputResult(res) {
  if (!res) return;
  if (!res.ok) {
    toast(res.error || "命令失败", true);
    appendSystem(res.error || "命令失败", true);
    return;
  }
  switch (res.kind) {
    case "started":
      state.running = true;
      renderComposerState();
      break;
    case "queued":
      state.queueLen = res.queue_len;
      toast("已排队（" + res.queue_len + "）");
      renderComposerState();
      break;
    case "ok":
      if (res.message) appendSystem(res.message, false);
      break;
    case "select":
      openOverlay({ type: "select", title: res.title, items: res.items, current: res.current, command: res.command || "" });
      break;
    case "compact_started":
      state.running = true;
      appendSystem("压缩中…", false);
      renderComposerState();
      break;
    case "exit":
      toast("关闭此页面即可退出服务");
      break;
    case "help":
      openOverlay({ type: "help" });
      break;
  }
}

function renderComposerState() {
  const sendBtn = $("btn-send");
  // running 时不禁用：提交会入队（后端排队，对位 TUI）
  sendBtn.textContent = "发送";
  $("btn-interrupt").hidden = !state.running;
  $("queue-hint").hidden = state.queueLen === 0;
  if (state.queueLen > 0) $("queue-hint").textContent = "队列：" + state.queueLen;
}

async function submit() {
  const line = inputEl.value.trim();
  if (!line) return;
  inputEl.value = "";
  updateComposerHeight();
  // 命令显示为系统行（对位 TUI：命令落 Command 行，非用户消息）。
  if (line.startsWith("/")) {
    appendSystem("Command  " + line, false);
  } else {
    appendUserMessage(line);
  }
  const res = await sendInput(line);
  handleInputResult(res);
}

function appendUserMessage(line) {
  const item = {
    kind: "message",
    role: "user",
    id: "msg-" + state.msgSeq++,
    text: line,
    done: true,
  };
  const el = makeMessageEl(item);
  timelineInner.appendChild(el);
  el.querySelector(".msg-content").textContent = line;
  state.timeline.push(item);
  scrollToBottom();
}

async function interrupt() {
  try {
    await api("/api/interrupt", {});
    toast("正在中断…");
    $("btn-interrupt").disabled = true;
  } catch (e) {
    toast(e.message, true);
  }
}

async function backToParent() {
  // /switch 无参：后端检测查看模式退出回父会话（对位 TUI）
  const res = await sendInput("/switch");
  handleInputResult(res);
}

/* ---------- 全量重拉（state_changed / 重连 / 刷新） ---------- */
async function reloadState() {
  state.reconnecting = true; // 丢弃在途增量
  try {
    const res = await fetch("/api/state");
    if (!res.ok) throw new Error("HTTP " + res.status);
    const data = await res.json();
    applyState(data);
  } catch (e) {
    toast("加载状态失败: " + e.message, true);
  } finally {
    state.reconnecting = false;
  }
}

function applyState(data) {
  state.sessions = data.sessions || [];
  state.active = data.active || null;
  state.running = !!data.running;
  state.queueLen = data.queue_len || 0;

  // 重建 timeline（保留展开状态：state.expanded 按 id）
  timelineInner.innerHTML = "";
  state.timeline = [];
  state.msgSeq = 0;
  for (const item of data.timeline || []) {
    if (item.kind === "message") {
      const el = appendMessage({
        id: item.id,
        role: item.role,
        text: item.text || "",
        html: item.html || "",
        thinking: item.thinking || "",
        done: item.done !== false,
      });
      // 历史消息的 thinking 折叠
      if (item.thinking) {
        const body = el.querySelector(".msg-body");
        const t = document.createElement("div");
        t.className = "thinking";
        t.innerHTML =
          '<button class="thinking-toggle" data-expanded="0">💭 已思考 · 点击展开</button>' +
          '<div class="thinking-body" hidden></div>';
        t.querySelector(".thinking-toggle").addEventListener("click", (e) => {
          const tb = t.querySelector(".thinking-body");
          const expanded = e.target.dataset.expanded === "1";
          e.target.dataset.expanded = expanded ? "0" : "1";
          e.target.textContent = expanded ? "💭 已思考 · 点击展开" : "💭 收起";
          tb.hidden = expanded;
          tb.textContent = item.thinking;
        });
        body.appendChild(t);
      }
    } else if (item.kind === "tool") {
      const t = {
        kind: "tool",
        id: item.id,
        name: item.tool ? item.tool.name : "",
        summary: item.tool ? item.tool.summary : "",
        done: item.done !== false,
      };
      const el = makeToolEl(t);
      timelineInner.appendChild(el);
      state.timeline.push(t);
      if (item.tool) updateToolEl(t, item.tool);
      if (!t.done) el.classList.add("running");
    } else if (item.kind === "system") {
      appendSystem(item.text || "", !!item.error);
    }
  }
  // pending 恢复（重连/刷新后弹窗）
  for (const p of data.pending || []) {
    if (p.kind === "approval") openOverlay({ type: "approval", request_id: p.request_id, req: p.req, title: "审批" });
    else if (p.kind === "ask") openOverlay({ type: "ask", request_id: p.request_id, req: p.req, title: "提问" });
  }
  renderSessions();
  renderStatusbar();
  renderComposerState();
  emptyState.hidden = !!(state.active && state.timeline.length);
  scrollToBottom();
}

/* ---------- SSE ---------- */
function connectEvents() {
  const es = new EventSource("/api/events");
  es.onopen = () => {
    offlineBanner.hidden = true;
    // 重连后重拉对齐（A3）
    reloadState();
  };
  es.onerror = () => {
    offlineBanner.hidden = false;
    // EventSource 自动重连
  };
  es.addEventListener("agent", (e) => {
    if (state.reconnecting) return; // 重拉期间丢弃增量
    const data = JSON.parse(e.data);
    // 会话过滤：只处理当前 active 会话事件（多标签页/子查看）
    if (state.active && data.session_id && data.session_id !== state.active.id) return;
    handleAgentEvent(data.ev, data);
  });
  es.addEventListener("approval", (e) => {
    const data = JSON.parse(e.data);
    openOverlay({ type: "approval", request_id: data.request_id, req: data.req, title: "审批" });
  });
  es.addEventListener("ask", (e) => {
    const data = JSON.parse(e.data);
    openOverlay({ type: "ask", request_id: data.request_id, req: data.req, title: "提问" });
  });
  es.addEventListener("run_done", (e) => {
    const data = JSON.parse(e.data);
    state.running = false;
    state.queueLen = data.queue_len || 0;
    if (data.interrupted) appendSystem("Turn interrupted", false);
    if (data.error) appendSystem("Error: " + data.error, true);
    renderComposerState();
    // 队列有后续：后端自动消费，前端仅更新状态
    if (state.queueLen > 0) toast("继续执行队列…");
  });
  es.addEventListener("state_changed", (e) => {
    const data = JSON.parse(e.data);
    reloadState();
    if (data.reason === "compact") {
      // 压缩完成：后端已广播；reloadState 会重建（摘要占位）
    }
  });
  es.addEventListener("system", (e) => {
    const data = JSON.parse(e.data);
    if (data.text !== "connected") appendSystem(data.text, !!data.error);
  });
}

/* ---------- 交互绑定 ---------- */
function init() {
  $("btn-send").addEventListener("click", submit);
  $("btn-interrupt").addEventListener("click", interrupt);
  $("btn-back").addEventListener("click", backToParent);
  $("btn-new").addEventListener("click", newSession);

  inputEl.addEventListener("keydown", (e) => {
    // 命令补全导航（对位 TUI completion：↑/↓ 移动、Tab/Enter 接受、Esc 关闭）。
    if (completionVisible()) {
      switch (e.key) {
        case "ArrowUp":
          e.preventDefault();
          moveCompletion(-1);
          return;
        case "ArrowDown":
          e.preventDefault();
          moveCompletion(1);
          return;
        case "Tab":
          e.preventDefault();
          acceptCompletion();
          return;
        case "Enter":
          e.preventDefault();
          acceptCompletion();
          return;
        case "Escape":
          completionEl.hidden = true;
          completionIndex = -1;
          return;
      }
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submit();
    } else if (e.key === "Escape" && state.running) {
      interrupt();
    }
  });

  inputEl.addEventListener("input", () => {
    updateComposerHeight();
    renderCompletion(); // 实时刷新补全候选
  });

  inputEl.addEventListener("blur", () => {
    // 点击候选时 blur 先触发会关掉弹窗——延迟到点击处理完再隐藏。
    setTimeout(() => {
      if (completionEl.hidden === false && !completionEl.matches(":hover")) {
        completionEl.hidden = true;
        completionIndex = -1;
      }
    }, 150);
  });

  overlayPanel.addEventListener("click", (e) => e.stopPropagation());
  overlayRoot.querySelector(".overlay-backdrop").addEventListener("click", closeOverlay);

  // 移动端：会话栏抽屉开关（侧边栏点击外部关闭）
  document.addEventListener("click", (e) => {
    const sb = $("sidebar");
    if (sb.classList.contains("open") && !sb.contains(e.target)) {
      sb.classList.remove("open");
    }
  });

  reloadState();
  connectEvents();
}

document.addEventListener("DOMContentLoaded", init);
