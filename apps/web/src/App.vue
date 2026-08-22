<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import {
  Check,
  ChevronDown,
  ChevronRight,
  CircleStop,
  Cpu,
  ImagePlus,
  KeyRound,
  Menu,
  MessageSquarePlus,
  Play,
  RefreshCw,
  Send,
  ShieldCheck,
  Trash2,
  X
} from "@lucide/vue";
import {
  changePassword,
  cancelTurn,
  createSession,
  deleteThread,
  getAuthStatus,
  getDevices,
  getHealth,
  getSettings,
  getThreads,
  login,
  register,
  logout,
  resumeThread,
  sendApproval,
  sendMessage,
  updateSettings,
  uploadImage,
  type AppSettings,
  type Attachment,
  type AuthStatus,
  type RemoteDevice,
  type RemoteEvent,
  type SessionRecord
} from "./api";

const auth = ref<AuthStatus>({ authenticated: false, registrationOpen: true });
const authChecked = ref(false);
const authUsername = ref("");
const loginPassword = ref("");
const registerMode = ref(false);
const currentPassword = ref("");
const newPassword = ref("");
const passwordPanelOpen = ref(false);
const passwordMessage = ref("");
const sessions = ref<SessionRecord[]>([]);
const activeSessionId = ref("");
const events = ref<RemoteEvent[]>([]);
const draft = ref("");
const firstPrompt = ref("");
const attachments = ref<Attachment[]>([]);
const settings = ref<AppSettings>({ approvalMode: "on-request", workMode: "edit" });
const devices = ref<RemoteDevice[]>([]);
const activeDeviceId = ref("");
const health = ref<unknown>(null);
const error = ref("");
const loading = ref(false);
const eventSource = ref<EventSource | null>(null);
const streamState = ref<"idle" | "connected" | "reconnecting">("idle");
const transcriptEl = ref<HTMLElement | null>(null);
const renderLimit = ref(160);
const forceScrollToBottom = ref(false);
const sidebarOpen = ref(false);
const collapsedProjects = ref<Set<string>>(new Set());

const activeSession = computed(() => sessions.value.find((session) => session.id === activeSessionId.value));
const groupedSessions = computed(() => {
  const groups = new Map<string, { cwd: string; label: string; sessions: SessionRecord[] }>();
  for (const session of sessions.value) {
    const cwd = session.cwd || "未知项目";
    if (!groups.has(cwd)) groups.set(cwd, { cwd, label: projectLabel(cwd), sessions: [] });
    groups.get(cwd)!.sessions.push(session);
  }
  return [...groups.values()];
});
const activeProjectCwd = computed(() => activeSession.value?.cwd || "");
const visibleSessionCount = computed(() =>
  groupedSessions.value.reduce((total, group) => total + (isProjectCollapsed(group.cwd) ? 0 : group.sessions.length), 0)
);
const pendingApprovals = computed(() => {
  const resolved = new Set(
    events.value
      .filter((event) => event.type === "approval.resolved")
      .map((event) => String(event.payload.approvalId || ""))
  );
  return new Set(
    events.value
      .filter((event) => event.type === "approval.requested" && !resolved.has(String(event.payload.approvalId || "")))
      .map((event) => String(event.payload.approvalId || event.id))
  );
});
const displayEvents = computed(() => {
  const filtered = [...events.value]
    .sort((a, b) => a.id - b.id)
    .filter((event) => {
      if (event.type === "session.status") return false;
      if (event.type === "approval.resolved") return false;
      if (event.type === "turn.done") return false;
      if (event.type === "assistant.delta") return Boolean(eventText(event).trim());
      if (event.type === "user.message") return Boolean(eventText(event).trim());
      return true;
    });
  return filtered.reduce<RemoteEvent[]>((merged, event) => {
    const previous = merged.at(-1);
    if (previous && previous.type === event.type && (event.type === "assistant.delta" || event.type === "tool.output")) {
      previous.payload = {
        ...previous.payload,
        text: `${eventText(previous)}${eventText(event)}`
      };
      return merged;
    }
    merged.push({ ...event, payload: { ...event.payload } });
    return merged;
  }, []);
});
const hiddenEventCount = computed(() => Math.max(0, displayEvents.value.length - renderLimit.value));
const visibleEvents = computed(() => displayEvents.value.slice(-renderLimit.value));

onMounted(async () => {
  await checkAuth();
  if (auth.value.authenticated) await refresh();
});

watch(activeSessionId, (sessionId) => {
  connectEvents(sessionId);
});

watch(() => events.value.length, async () => {
  await nextTick();
  if (forceScrollToBottom.value) {
    await scrollTranscriptToBottom();
    forceScrollToBottom.value = false;
    return;
  }
  const element = transcriptEl.value;
  if (!element) return;
  element.scrollTo({ top: element.scrollHeight, behavior: "smooth" });
});

async function refresh() {
  if (!auth.value.authenticated) return;
  error.value = "";
  try {
    const [healthResult, deviceResult] = await Promise.all([
      getHealth().catch((reason) => ({ error: String(reason) })),
      getDevices()
    ]);
    health.value = healthResult;
    devices.value = deviceResult.devices;
    if (!activeDeviceId.value || !devices.value.some((device) => device.id === activeDeviceId.value && device.online)) {
      activeDeviceId.value = devices.value.find((device) => device.online)?.id || "";
    }
    const [sessionResult, settingsResult] = await Promise.all([
      getThreads(activeDeviceId.value),
      getSettings(activeDeviceId.value)
    ]);
    sessions.value = sessionResult.sessions;
    settings.value = settingsResult;
    collapseProjectsByDefault();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

async function checkAuth() {
  error.value = "";
  try {
    auth.value = await getAuthStatus();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    authChecked.value = true;
  }
}

async function submitLogin() {
  loading.value = true;
  error.value = "";
  try {
    auth.value = registerMode.value
      ? await register(authUsername.value.trim(), loginPassword.value)
      : await login(authUsername.value.trim(), loginPassword.value);
    authUsername.value = "";
    loginPassword.value = "";
    await refresh();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function savePassword() {
  loading.value = true;
  error.value = "";
  passwordMessage.value = "";
  try {
    if (newPassword.value.trim().length < 8) throw new Error("新密码至少 8 个字符");
    if (!currentPassword.value.trim()) throw new Error("请输入当前密码");
    await changePassword(currentPassword.value, newPassword.value);
    auth.value = await getAuthStatus();
    currentPassword.value = "";
    newPassword.value = "";
    passwordMessage.value = "密码已更新";
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function signOut() {
  await logout();
  auth.value = await getAuthStatus();
  activeSessionId.value = "";
  events.value = [];
  sessions.value = [];
  passwordPanelOpen.value = false;
}

async function selectDevice() {
  activeSessionId.value = "";
  events.value = [];
  await refresh();
}

async function startSession() {
  loading.value = true;
  error.value = "";
  try {
    const { session } = await createSession(firstPrompt.value.trim() || undefined, activeDeviceId.value);
    upsertSession(session);
    activeSessionId.value = session.id;
    firstPrompt.value = "";
    sidebarOpen.value = false;
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function openThread(session: SessionRecord) {
  if (session.id === activeSessionId.value) return;
  loading.value = true;
  error.value = "";
  try {
    const { session: resumed } = await resumeThread(session.id);
    upsertSession(resumed);
    forceScrollToBottom.value = true;
    activeSessionId.value = resumed.id;
    sidebarOpen.value = false;
    await nextTick();
    await scrollTranscriptToBottom();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function removeThread(session: SessionRecord) {
  if (!window.confirm(`确认删除“${session.title}”吗？该对话会同步从 Codex 列表中归档。`)) return;
  loading.value = true;
  error.value = "";
  try {
    await deleteThread(session.id);
    sessions.value = sessions.value.filter((item) => item.id !== session.id);
    if (activeSessionId.value === session.id) {
      activeSessionId.value = "";
      events.value = [];
    }
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function submitMessage() {
  if (!activeSession.value || (!draft.value.trim() && attachments.value.length === 0)) return;
  loading.value = true;
  error.value = "";
  try {
    await sendMessage(activeSession.value.id, draft.value.trim(), attachments.value);
    draft.value = "";
    attachments.value = [];
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function saveSettings(next: Partial<AppSettings>) {
  const previous = settings.value;
  settings.value = { ...settings.value, ...next };
  try {
    settings.value = await updateSettings(settings.value, activeDeviceId.value);
  } catch (reason) {
    settings.value = previous;
    error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

function handleComposerKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" || event.shiftKey || event.ctrlKey || event.altKey || event.metaKey) return;
  event.preventDefault();
  void submitMessage();
}

async function handlePaste(event: ClipboardEvent) {
  const items = Array.from(event.clipboardData?.items || []);
  const imageItems = items.filter((item) => item.type.startsWith("image/"));
  if (imageItems.length === 0) return;
  event.preventDefault();
  for (const item of imageItems) {
    const file = item.getAsFile();
    if (file) await addImageFile(file);
  }
}

async function handleFilePicked(event: Event) {
  const input = event.target as HTMLInputElement;
  const files = Array.from(input.files || []);
  for (const file of files) await addImageFile(file);
  input.value = "";
}

async function addImageFile(file: File) {
  if (!file.type.startsWith("image/")) return;
  const dataUrl = await fileToDataURL(file);
  const { attachment } = await uploadImage(file.name || `image-${Date.now()}.png`, file.type, dataUrl);
  attachments.value.push(attachment);
}

function removeAttachment(index: number) {
  attachments.value.splice(index, 1);
}

function fileToDataURL(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result || ""));
    reader.onerror = () => reject(reader.error || new Error("图片读取失败"));
    reader.readAsDataURL(file);
  });
}

async function approve(event: RemoteEvent, decision: "approved" | "rejected") {
  if (!activeSession.value) return;
  const approvalId = String(event.payload.approvalId || event.id);
  await sendApproval(activeSession.value.id, approvalId, decision);
}

async function cancel() {
  if (!activeSession.value) return;
  await cancelTurn(activeSession.value.id);
}

function connectEvents(sessionId: string) {
  eventSource.value?.close();
  events.value = [];
  renderLimit.value = 160;
  forceScrollToBottom.value = true;
  if (!sessionId) return;

  streamState.value = "reconnecting";
  const source = new EventSource(`/api/sessions/${sessionId}/events`);
  eventSource.value = source;

  source.onopen = () => {
    streamState.value = "connected";
  };
  source.onerror = () => {
    streamState.value = "reconnecting";
  };

  const types: RemoteEvent["type"][] = [
    "session.status",
    "user.message",
    "assistant.delta",
    "tool.started",
    "tool.output",
    "approval.requested",
    "approval.resolved",
    "turn.done",
    "error"
  ];
  for (const type of types) {
    source.addEventListener(type, (message) => {
      const event = JSON.parse((message as MessageEvent).data) as RemoteEvent;
      if (!events.value.some((item) => item.id === event.id)) events.value.push(event);
      if (events.value.length > 500) events.value = events.value.slice(-500);
      if (event.type === "session.status" && activeSession.value) {
        upsertSession({
          ...activeSession.value,
          status: String(event.payload.status || activeSession.value.status),
          mode: String(event.payload.mode || activeSession.value.mode) as SessionRecord["mode"],
          updatedAt: event.ts
        });
      }
    });
  }
}

function upsertSession(session: SessionRecord) {
  const index = sessions.value.findIndex((item) => item.id === session.id);
  if (index >= 0) sessions.value[index] = session;
  else sessions.value.unshift(session);
}

async function scrollTranscriptToBottom() {
  for (let index = 0; index < 4; index += 1) {
    await nextTick();
    await new Promise((resolve) => requestAnimationFrame(resolve));
    const element = transcriptEl.value;
    if (element) element.scrollTop = element.scrollHeight;
  }
}

function formatTime(value: string) {
  return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(value));
}

function formatShortDate(value: string) {
  return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" }).format(new Date(value));
}

function projectLabel(cwd: string) {
  if (!cwd || cwd === "未知项目") return "未知项目";
  const parts = cwd.split(/[\\/]+/).filter(Boolean);
  return parts.at(-1) || cwd;
}

function isProjectCollapsed(cwd: string) {
  return collapsedProjects.value.has(cwd);
}

function toggleProject(cwd: string) {
  const next = new Set(collapsedProjects.value);
  if (next.has(cwd)) next.delete(cwd);
  else next.add(cwd);
  collapsedProjects.value = next;
}

function expandAllProjects() {
  collapsedProjects.value = new Set();
}

function collapseProjectsByDefault() {
  const cwdList = sessions.value.map((session) => session.cwd || "未知项目");
  collapsedProjects.value = new Set(cwdList);
}

function collapseOtherProjects() {
  const keepOpen = activeProjectCwd.value || groupedSessions.value[0]?.cwd || "";
  collapsedProjects.value = new Set(
    groupedSessions.value.map((group) => group.cwd).filter((cwd) => cwd !== keepOpen)
  );
}

function eventText(event: RemoteEvent) {
  if (event.type === "user.message") return String(event.payload.text || "");
  if (event.type === "assistant.delta") return String(event.payload.text || event.payload.delta || "");
  if (event.type === "tool.output") return String(event.payload.text || event.payload.output || JSON.stringify(event.payload));
  if (event.type === "tool.started") return String(event.payload.command || event.payload.method || "工具开始执行");
  if (event.type === "approval.requested") {
    const parts = [
      event.payload.title,
      event.payload.description,
      event.payload.command ? `命令：${event.payload.command}` : undefined
    ].filter(Boolean);
    return parts.join("\n");
  }
  if (event.type === "error") return String(event.payload.message || JSON.stringify(event.payload));
  return compactPayload(event.payload);
}

function eventHtml(event: RemoteEvent) {
  return markdownToHtml(eventText(event));
}

function markdownToHtml(markdown: string) {
  const lines = markdown.replace(/\r\n/g, "\n").split("\n");
  const html: string[] = [];
  let index = 0;

  while (index < lines.length) {
    const line = lines[index];
    if (!line.trim()) {
      index += 1;
      continue;
    }

    const fence = line.match(/^```([\w-]+)?\s*$/);
    if (fence) {
      const language = fence[1] ? ` language-${escapeAttr(fence[1])}` : "";
      const code: string[] = [];
      index += 1;
      while (index < lines.length && !/^```\s*$/.test(lines[index])) {
        code.push(lines[index]);
        index += 1;
      }
      if (index < lines.length) index += 1;
      html.push(`<pre class="code-block"><code class="${language.trim()}">${escapeHtml(code.join("\n"))}</code></pre>`);
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      const level = heading[1].length;
      html.push(`<h${level}>${inlineMarkdown(heading[2])}</h${level}>`);
      index += 1;
      continue;
    }

    if (/^>\s?/.test(line)) {
      const quote: string[] = [];
      while (index < lines.length && /^>\s?/.test(lines[index])) {
        quote.push(lines[index].replace(/^>\s?/, ""));
        index += 1;
      }
      html.push(`<blockquote>${markdownToHtml(quote.join("\n"))}</blockquote>`);
      continue;
    }

    if (/^[-*+]\s+/.test(line)) {
      const items: string[] = [];
      while (index < lines.length && /^[-*+]\s+/.test(lines[index])) {
        items.push(`<li>${inlineMarkdown(lines[index].replace(/^[-*+]\s+/, ""))}</li>`);
        index += 1;
      }
      html.push(`<ul>${items.join("")}</ul>`);
      continue;
    }

    if (/^\d+\.\s+/.test(line)) {
      const items: string[] = [];
      while (index < lines.length && /^\d+\.\s+/.test(lines[index])) {
        items.push(`<li>${inlineMarkdown(lines[index].replace(/^\d+\.\s+/, ""))}</li>`);
        index += 1;
      }
      html.push(`<ol>${items.join("")}</ol>`);
      continue;
    }

    const paragraph: string[] = [];
    while (
      index < lines.length &&
      lines[index].trim() &&
      !/^```/.test(lines[index]) &&
      !/^(#{1,6})\s+/.test(lines[index]) &&
      !/^>\s?/.test(lines[index]) &&
      !/^[-*+]\s+/.test(lines[index]) &&
      !/^\d+\.\s+/.test(lines[index])
    ) {
      paragraph.push(lines[index]);
      index += 1;
    }
    html.push(`<p>${inlineMarkdown(paragraph.join("\n")).replace(/\n/g, "<br>")}</p>`);
  }

  return html.join("");
}

function inlineMarkdown(value: string) {
  const placeholders: string[] = [];
  let html = escapeHtml(value).replace(/`([^`]+)`/g, (_, code: string) => {
    const token = `\u0000${placeholders.length}\u0000`;
    placeholders.push(`<code>${code}</code>`);
    return token;
  });

  html = html.replace(/\[([^\]]+)]\(([^)\s]+)\)/g, (match, label: string, href: string) => {
    const decodedHref = href.replace(/&amp;/g, "&");
    if (!isSafeUrl(decodedHref)) return match;
    return `<a href="${escapeAttr(decodedHref)}" target="_blank" rel="noreferrer">${label}</a>`;
  });
  html = html.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/(^|[^*])\*([^*\n]+)\*(?!\*)/g, "$1<em>$2</em>");

  return placeholders.reduce((result, replacement, index) => result.replace(`\u0000${index}\u0000`, replacement), html);
}

function isSafeUrl(url: string) {
  return /^(https?:\/\/|mailto:|\/|#)/i.test(url);
}

function escapeHtml(value: string) {
  return value
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function escapeAttr(value: string) {
  return escapeHtml(value).replace(/`/g, "&#96;");
}

function eventClass(event: RemoteEvent) {
  return event.type.replace(".", "-");
}

function eventLabel(event: RemoteEvent) {
  const labels: Record<RemoteEvent["type"], string> = {
    "session.status": "状态",
    "user.message": "你",
    "assistant.delta": "Codex",
    "tool.started": "工具",
    "tool.output": "输出",
    "approval.requested": "需要审批",
    "approval.resolved": "审批结果",
    "turn.done": "完成",
    error: "错误"
  };
  return labels[event.type];
}

function compactPayload(payload: Record<string, unknown>) {
  return Object.entries(payload)
    .map(([key, value]) => `${key}: ${typeof value === "string" ? value : JSON.stringify(value)}`)
    .join("\n");
}

function modeLabel(mode?: SessionRecord["mode"]) {
  const labels: Record<SessionRecord["mode"], string> = {
    "desktop-attached": "桌面任务",
    "host-new-session": "宿主机会话",
    disconnected: "未连接",
    error: "异常"
  };
  return mode ? labels[mode] : "未连接";
}

function statusLabel(status?: string) {
  const labels: Record<string, string> = {
    idle: "空闲",
    running: "运行中",
    "waiting-approval": "等待审批",
    done: "已完成",
    error: "异常",
    cancelled: "已取消"
  };
  return status ? labels[status] || status : "未连接";
}

function streamLabel(status: typeof streamState.value) {
  return {
    idle: "未连接",
    connected: "已连接",
    reconnecting: "重连中"
  }[status];
}
</script>

<template>
  <main v-if="!authChecked" class="auth-shell">
    <section class="auth-panel">
      <p class="eyebrow">Codex Remote</p>
      <h1>正在连接</h1>
    </section>
  </main>

  <main v-else-if="!auth.authenticated" class="auth-shell">
    <form class="auth-panel" @submit.prevent="submitLogin">
      <p class="eyebrow">Codex Remote</p>
      <h1>{{ registerMode ? "创建服务端账号" : "登录控制台" }}</h1>
      <p class="auth-note">网页与本机客户端使用同一个服务端账号，连接后会自动同步。</p>
      <input v-model="authUsername" type="text" autocomplete="username" placeholder="用户名" autofocus />
      <input v-model="loginPassword" type="password" autocomplete="current-password" placeholder="密码" />
      <button class="primary icon-text" type="submit" :disabled="loading">
        <KeyRound :size="18" />
        <span>{{ registerMode ? "注册并登录" : "登录" }}</span>
      </button>
      <button v-if="auth.registrationOpen" class="auth-switch" type="button" @click="registerMode = !registerMode">
        {{ registerMode ? "已有账号，去登录" : "没有账号，创建账号" }}
      </button>
      <p v-if="error" class="error">{{ error }}</p>
    </form>
  </main>

  <main v-else class="shell">
    <header class="topbar">
      <div>
        <p class="eyebrow">Codex Remote</p>
        <h1>本机控制台</h1>
      </div>
      <div class="top-actions">
        <button class="icon-button mobile-sidebar-toggle" type="button" title="打开对话列表" @click="sidebarOpen = true">
          <Menu :size="19" />
        </button>
        <button class="icon-button" type="button" title="刷新状态" @click="refresh">
          <RefreshCw :size="19" />
        </button>
        <button class="icon-button" type="button" title="安全设置" @click="passwordPanelOpen = !passwordPanelOpen">
          <KeyRound :size="19" />
        </button>
      </div>
    </header>

    <p v-if="error" class="error">{{ error }}</p>

    <section v-if="passwordPanelOpen" class="password-panel">
      <div>
        <strong>账号安全</strong>
        <p>当前账号：{{ auth.username || "已登录" }}</p>
      </div>
      <form class="password-form" @submit.prevent="savePassword">
        <input v-model="currentPassword" type="password" autocomplete="current-password" placeholder="当前密码" />
        <input v-model="newPassword" type="password" autocomplete="new-password" placeholder="新密码，至少 8 个字符" />
        <button class="primary icon-text" type="submit" :disabled="loading || !newPassword.trim()">
          <KeyRound :size="18" />
          <span>修改密码</span>
        </button>
        <button class="icon-button sign-out" type="button" title="退出登录" @click="signOut">
          <X :size="18" />
        </button>
      </form>
      <small v-if="passwordMessage">{{ passwordMessage }}</small>
    </section>

    <div class="workspace" :class="{ 'sidebar-open': sidebarOpen }">
      <button v-if="sidebarOpen" class="sidebar-backdrop" type="button" aria-label="关闭对话列表" @click="sidebarOpen = false" />
      <aside class="sidebar">
        <div class="sidebar-heading">
          <strong>对话</strong>
          <div class="sidebar-actions">
            <button class="icon-button compact" type="button" title="刷新对话列表" @click="refresh">
              <RefreshCw :size="16" />
            </button>
            <button class="icon-button compact sidebar-close" type="button" title="关闭对话列表" @click="sidebarOpen = false">
              <X :size="16" />
            </button>
          </div>
        </div>
        <div class="project-tools">
          <button type="button" @click="expandAllProjects">展开全部</button>
          <button type="button" @click="collapseOtherProjects">折叠其它</button>
          <span>{{ visibleSessionCount }} / {{ sessions.length }}</span>
        </div>
        <div class="session-list" aria-label="Sessions">
          <section v-for="group in groupedSessions" :key="group.cwd" class="session-project-group">
            <button
              class="project-heading"
              type="button"
              :title="group.cwd"
              :aria-expanded="!isProjectCollapsed(group.cwd)"
              @click="toggleProject(group.cwd)"
            >
              <ChevronRight v-if="isProjectCollapsed(group.cwd)" :size="16" />
              <ChevronDown v-else :size="16" />
              <span>{{ group.label }}</span>
              <small>{{ group.sessions.length }} 条</small>
            </button>
            <article
              v-for="session in group.sessions"
              v-show="!isProjectCollapsed(group.cwd)"
              :key="session.id"
              :data-session-id="session.id"
              class="session-pill"
              :class="{ active: session.id === activeSessionId }"
            >
              <button class="session-open" type="button" @click="openThread(session)">
                <span class="session-title">{{ session.title }}</span>
                <span class="session-columns">
                  <small>项目：{{ projectLabel(session.cwd || "") }}</small>
                  <small>状态：{{ statusLabel(session.status) }}</small>
                  <small>更新：{{ formatShortDate(session.updatedAt) }}</small>
                </span>
              </button>
              <button class="delete-thread" type="button" title="删除对话" @click="removeThread(session)">
                <Trash2 :size="15" />
              </button>
            </article>
          </section>
        </div>
      </aside>

      <section class="content">
        <section class="status-band">
          <div class="status-item">
            <Cpu :size="17" />
            <span>{{ modeLabel(activeSession?.mode) }}</span>
          </div>
          <div class="status-item">
            <ShieldCheck :size="17" />
            <span>{{ streamLabel(streamState) }}</span>
          </div>
          <label class="select-control device-select">
            <Cpu :size="16" />
            <select v-model="activeDeviceId" @change="selectDevice">
              <option value="" disabled>{{ devices.length ? "选择客户端" : "暂无客户端" }}</option>
              <option v-for="device in devices" :key="device.id" :value="device.id" :disabled="!device.online">
                {{ device.name }}{{ device.online ? "" : "（离线）" }}
              </option>
            </select>
          </label>
          <div class="segmented-control" aria-label="工作模式">
            <button type="button" :class="{ active: settings.workMode === 'edit' }" @click="saveSettings({ workMode: 'edit' })">编辑</button>
            <button type="button" :class="{ active: settings.workMode === 'plan' }" @click="saveSettings({ workMode: 'plan' })">计划</button>
          </div>
          <label class="select-control">
            <ShieldCheck :size="16" />
            <select v-model="settings.approvalMode" @change="saveSettings({ approvalMode: settings.approvalMode })">
              <option value="on-request">请求批准</option>
              <option value="on-failure">按需批准</option>
              <option value="never">完全访问权限</option>
            </select>
          </label>
        </section>

        <section class="new-session">
          <input v-model="firstPrompt" type="text" placeholder="新任务第一句话" @keydown.enter="startSession" />
          <button class="primary icon-text" type="button" :disabled="loading" @click="startSession">
            <MessageSquarePlus :size="18" />
            <span>新会话</span>
          </button>
        </section>

        <section v-if="activeSession" ref="transcriptEl" class="transcript">
      <article class="event-block session-summary">
        <div>
          <strong>{{ activeSession.title }}</strong>
          <p>{{ activeSession.cwd || "Host workspace" }}</p>
        </div>
        <span>{{ statusLabel(activeSession.status) }}</span>
      </article>

      <button v-if="hiddenEventCount" class="load-older" type="button" @click="renderLimit += 160">
        显示更早 {{ Math.min(hiddenEventCount, 160) }} 条
      </button>

      <article
        v-for="event in visibleEvents"
        :key="event.id"
        class="event-block timeline-event"
        :class="[eventClass(event), { pending: pendingApprovals.has(String(event.payload.approvalId || event.id)) }]"
      >
        <div class="event-meta">
          <time>{{ formatTime(event.ts) }}</time>
          <span>{{ eventLabel(event) }}</span>
        </div>
        <div class="markdown-body" v-html="eventHtml(event)" />
        <div v-if="event.type === 'approval.requested' && pendingApprovals.has(String(event.payload.approvalId || event.id))" class="approval-actions">
          <button class="primary icon-text" type="button" @click="approve(event, 'approved')">
            <Check :size="18" />
            <span>批准</span>
          </button>
          <button class="danger icon-text" type="button" @click="approve(event, 'rejected')">
            <X :size="18" />
            <span>拒绝</span>
          </button>
        </div>
      </article>
        </section>

        <section v-else class="empty-state">
          <Play :size="30" />
          <p>选择左侧历史对话，或启动一个新的 Codex 会话。</p>
        </section>

        <form class="composer" @submit.prevent="submitMessage">
          <div v-if="attachments.length" class="attachment-strip">
            <figure v-for="(attachment, index) in attachments" :key="attachment.id || attachment.path || index">
              <img v-if="attachment.url" :src="attachment.url" :alt="attachment.name || '图片附件'" />
              <figcaption>{{ attachment.name || "图片" }}</figcaption>
              <button type="button" title="移除图片" @click="removeAttachment(index)">
                <X :size="14" />
              </button>
            </figure>
          </div>
          <button class="icon-button stop" type="button" title="取消当前 turn" :disabled="!activeSession" @click="cancel">
            <CircleStop :size="20" />
          </button>
          <label class="icon-button attach-button" title="添加图片">
            <ImagePlus :size="20" />
            <input type="file" accept="image/*" multiple @change="handleFilePicked" />
          </label>
          <textarea
            v-model="draft"
            rows="1"
            placeholder="继续给 Codex 发消息"
            :disabled="!activeSession"
            @keydown="handleComposerKeydown"
            @paste="handlePaste"
          />
          <button class="primary send" type="submit" title="发送" :disabled="!activeSession || (!draft.trim() && !attachments.length) || loading">
            <Send :size="19" />
          </button>
        </form>
      </section>
    </div>
  </main>
</template>
