<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from "vue";
import DOMPurify from "dompurify";
import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";
import css from "highlight.js/lib/languages/css";
import diff from "highlight.js/lib/languages/diff";
import dockerfile from "highlight.js/lib/languages/dockerfile";
import go from "highlight.js/lib/languages/go";
import javascript from "highlight.js/lib/languages/javascript";
import json from "highlight.js/lib/languages/json";
import markdown from "highlight.js/lib/languages/markdown";
import powershell from "highlight.js/lib/languages/powershell";
import python from "highlight.js/lib/languages/python";
import sql from "highlight.js/lib/languages/sql";
import typescript from "highlight.js/lib/languages/typescript";
import xml from "highlight.js/lib/languages/xml";
import yaml from "highlight.js/lib/languages/yaml";
import MarkdownIt from "markdown-it";
import {
  ArrowLeft,
  ArrowDown,
  ArrowUp,
  Check,
  ChevronDown,
  ChevronRight,
  CircleStop,
  CircleAlert,
  CircleHelp,
  Copy,
  Cpu,
  Gauge,
  GripVertical,
  Folder,
  Clock3,
  ImagePlus,
  KeyRound,
  Menu,
  MessageSquarePlus,
  Network,
  Paperclip,
  Pencil,
  Plus,
  Play,
  Power,
  RefreshCw,
  Send,
  Save,
  ShieldCheck,
  Target,
  Trash2,
  User,
  Users,
  LogOut,
  Moon,
  Sun,
  X
} from "@lucide/vue";
import {
  changePassword,
  cancelTurn,
  createToken,
  createSession,
  deleteDevice,
  deleteToken,
  deleteThread,
  getAuthStatus,
  getAdminUsers,
  getDevices,
  getHealth,
  getModels,
  getSkills,
  getSessionHistory,
  getPortMappings,
  getSettings,
  getThreads,
  getTokens,
  login,
  register,
  releaseThread,
  refreshToken,
  logout,
  deleteAdminUser,
  resumeThread,
  sendApproval,
  sendUserInput,
  sendMessage,
  createPortMapping,
  deletePortMapping,
  updatePortMapping,
  updateAdminUser,
  updateSettings,
  addQueue,
  clearGoal,
  deleteQueue,
  getGoal,
  getQueue,
  promoteQueue,
  reorderQueue,
  setGoal,
  updateQueue,
  uploadAttachment,
  type AppSettings,
  type AdminUser,
  type Attachment,
  type AccessToken,
  type AuthStatus,
  type ModelOption,
  type PortMapping,
  type PortMappingDraft,
  type QueuedInput,
  type QueuedSubmission,
  type RemoteDevice,
  type RemoteEvent,
  type SessionRecord,
  type SkillOption,
  type ThreadGoal,
  type TokenUsage
} from "./api";
import { P2PRemoteError, P2PTransport } from "./p2p";

for (const [name, language] of Object.entries({ bash, css, diff, dockerfile, go, javascript, json, markdown, powershell, python, sql, typescript, xml, yaml })) {
  hljs.registerLanguage(name, language);
}

const markdownRenderer = new MarkdownIt({
  breaks: true,
  html: false,
  linkify: true,
  highlight(code, info) {
    const language = info.trim().split(/\s+/)[0]?.toLowerCase() || "";
    const highlighted = language && hljs.getLanguage(language)
      ? hljs.highlight(code, { language, ignoreIllegals: true }).value
      : escapeHtml(code);
    const languageClass = language ? ` language-${escapeAttr(language)}` : "";
    return `<pre class="code-block"><code class="hljs${languageClass}">${highlighted}</code></pre>`;
  }
});

const defaultLinkOpen = markdownRenderer.renderer.rules.link_open
  ?? ((tokens, index, options, _environment, renderer) => renderer.renderToken(tokens, index, options));
markdownRenderer.renderer.rules.link_open = (tokens, index, options, environment, renderer) => {
  tokens[index].attrSet("target", "_blank");
  tokens[index].attrSet("rel", "noopener noreferrer");
  return defaultLinkOpen(tokens, index, options, environment, renderer);
};

const auth = ref<AuthStatus>({ authenticated: false, registrationOpen: true });
const authChecked = ref(false);
const authUsername = ref("");
const loginPassword = ref("");
const registerMode = ref(false);
const currentPassword = ref("");
const newPassword = ref("");
const userMenuOpen = ref(false);
const modalView = ref<"user" | "tokens" | "ports" | null>(null);
const systemManagementOpen = ref(false);
const systemSection = ref<"users">("users");
const adminUsers = ref<AdminUser[]>([]);
const adminUsersLoading = ref(false);
const adminError = ref("");
const adminMessage = ref("");
const portMappings = ref<PortMapping[]>([]);
const portMappingsLoading = ref(false);
const portMappingEditingId = ref("");
const portMappingForm = ref<PortMappingDraft>({ deviceId: "", name: "", targetHost: "127.0.0.1", targetPort: 3000, listenPort: 19022, protocol: "tcp", enabled: true });
const portMappingError = ref("");
const portMappingMessage = ref("");
type PortMappingHelp = "name" | "device" | "targetHost" | "targetPort" | "listenPort" | "enabled";
const portMappingHelp = ref<PortMappingHelp | null>(null);
const passwordMessage = ref("");
const tokens = ref<AccessToken[]>([]);
const tokenName = ref("");
const tokenMessage = ref("");
const noticeMessage = ref("");
const sessions = ref<SessionRecord[]>([]);
const activeSessionId = ref("");
const events = ref<RemoteEvent[]>([]);
const draft = ref("");
const attachments = ref<Attachment[]>([]);
const queuedSubmissions = ref<QueuedSubmission[]>([]);
const queueLoading = ref(false);
const queueActionID = ref("");
const editingQueueID = ref("");
const queuedMessageDraft = ref("");
const queuedMessageInput = ref<QueuedInput[]>([]);
const threadGoal = ref<ThreadGoal | null>(null);
const goalEditorOpen = ref(false);
const goalDraft = ref("");
const composerMenuOpen = ref(false);
const modelMenuOpen = ref(false);
const slashMenuOpen = ref(false);
const slashSelectionIndex = ref(0);
const composerBusy = ref(false);
const fileInput = ref<HTMLInputElement | null>(null);
const composerTextarea = ref<HTMLTextAreaElement | null>(null);
const defaultAppSettings: AppSettings = { approvalMode: "on-request", workMode: "edit", model: "", reasoningEffort: "" };
const settings = ref<AppSettings>({ ...defaultAppSettings });
const models = ref<ModelOption[]>([]);
const skills = ref<SkillOption[]>([]);
const devices = ref<RemoteDevice[]>([]);
const activeDeviceId = ref("");
const health = ref<unknown>(null);
const error = ref("");
const loading = ref(false);
const openingThreadId = ref("");
const eventSource = ref<EventSource | null>(null);
const streamState = ref<"idle" | "connected" | "reconnecting">("idle");
const transportState = ref<"idle" | "connecting" | "p2p" | "relay" | "failed">("idle");
const transcriptEl = ref<HTMLElement | null>(null);
const historySentinelEl = ref<HTMLElement | null>(null);
const renderLimit = ref(160);
const forceScrollToBottom = ref(false);
const sidebarOpen = ref(false);
const selectedProjectCwd = ref("");
const collapsedProjects = ref<Set<string>>(new Set());
const listPageSize = 6;
const visibleProjectCount = ref(listPageSize);
const visibleRecentCount = ref(listPageSize);
const visibleProjectSessionCounts = ref<Record<string, number>>({});
const projectOrder = ref<string[]>([]);
const recentOrder = ref<string[]>([]);
const theme = ref<"light" | "dark">("dark");
const composerDropActive = ref(false);
const dragState = ref<{ kind: "project" | "recent"; id: string } | null>(null);
const dragOverKey = ref("");
const historyLoading = ref(false);
const historyHasMore = ref(false);
const pendingRemoteEvents = new Map<string, RemoteEvent>();
const userInputSelections = ref<Record<string, Record<string, string>>>({});
let historyObserver: IntersectionObserver | null = null;
let historyScrollArmed = false;

const projectOrderStorageKey = "codex-link-project-order";
const recentOrderStorageKey = "codex-link-recent-order";

const p2p = new P2PTransport({
  onEvent: (event) => acceptRemoteEvent(event),
  onSession: (session) => upsertSession(session),
  onClosed: () => {
    if (p2p.isP2POnly()) {
      eventSource.value?.close();
      eventSource.value = null;
      transportState.value = "failed";
      streamState.value = "idle";
      return;
    }
    transportState.value = "relay";
    if (activeSessionId.value) connectEvents(activeSessionId.value);
  }
});

const activeSession = computed(() => sessions.value.find((session) => session.id === activeSessionId.value));
const activeDevice = computed(() => devices.value.find((device) => device.id === activeDeviceId.value));
const activeSessionReadOnly = computed(() => activeSession.value?.mode === "host-readonly");
const canModifyActiveSession = computed(() => Boolean(activeSession.value) && !activeSessionReadOnly.value);
const composerHasInput = computed(() => Boolean(draft.value.trim()) || attachments.value.length > 0);
const activeSessionRunning = computed(() => activeSession.value?.status === "running");
const showStopButton = computed(() => activeSessionRunning.value && !composerHasInput.value && !activeSessionReadOnly.value);
const selectedModelOption = computed(() => {
  const selectedModel = settings.value.model;
  return models.value.find((model) => model.model === selectedModel)
    ?? models.value.find((model) => model.isDefault)
    ?? models.value[0];
});
const selectedModelLabel = computed(() => {
  const model = selectedModelOption.value;
  return model?.displayName || model?.model || "默认模型";
});
const slashToken = computed(() => {
  const match = /(^|\s)\/([^\s/]*)$/.exec(draft.value);
  if (!match) return null;
  return {
    start: match.index + match[1].length,
    end: draft.value.length,
    query: match[2].toLowerCase()
  };
});
const slashSuggestions = computed(() => {
  const token = slashToken.value;
  if (!token) return [];
  const filtered = skills.value.filter((skill) => {
    const search = `${skill.command} ${skill.name} ${skill.description || ""}`.toLowerCase();
    return search.includes(token.query);
  });
  return filtered.slice(0, 12);
});
const slashMenuVisible = computed(() => slashMenuOpen.value && slashSuggestions.value.length > 0 && canModifyActiveSession.value);
const supportedReasoningEfforts = computed(() => selectedModelOption.value?.supportedReasoningEfforts || []);
const groupedSessions = computed(() => {
  const groups = new Map<string, { cwd: string; label: string; sessions: SessionRecord[] }>();
  for (const session of sessions.value) {
    const cwd = session.cwd;
    if (!cwd || isRecentSession(session)) continue;
    if (!groups.has(cwd)) groups.set(cwd, { cwd, label: projectLabel(cwd), sessions: [] });
    groups.get(cwd)!.sessions.push(session);
  }
  const orderedGroups = [...groups.values()].map((group) => ({
    ...group,
    sessions: sortSessionsByUpdatedAt(group.sessions)
  }));
  orderedGroups.sort((a, b) => compareUpdatedAt(a.sessions[0]?.updatedAt, b.sessions[0]?.updatedAt));
  return applyStoredOrder(orderedGroups, projectOrder.value, (group) => group.cwd);
});
const recentSessions = computed(() => {
  const recent = sortSessionsByUpdatedAt(sessions.value.filter(isRecentSession));
  return applyStoredOrder(recent, recentOrder.value, (session) => session.id);
});
const visibleProjectGroups = computed(() => groupedSessions.value.slice(0, visibleProjectCount.value));
const visibleRecentSessions = computed(() => recentSessions.value.slice(0, visibleRecentCount.value));
const hasMoreProjects = computed(() => visibleProjectCount.value < groupedSessions.value.length);
const hasMoreRecentSessions = computed(() => visibleRecentCount.value < recentSessions.value.length);
const orderedSessionCount = computed(() => groupedSessions.value.reduce((total, group) => total + group.sessions.length, 0) + recentSessions.value.length);
const activeProjectCwd = computed(() => (activeSession.value && !isRecentSession(activeSession.value) ? activeSession.value.cwd || "" : ""));
const visibleSessionCount = computed(() =>
  visibleProjectGroups.value.reduce((total, group) => total + (isProjectCollapsed(group.cwd) ? 0 : visibleProjectSessions(group).length), 0) + visibleRecentSessions.value.length
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
const pendingUserInputs = computed(() => {
  const resolved = new Set(
    events.value
      .filter((event) => event.type === "user.input.resolved")
      .map((event) => String(event.payload.requestId || ""))
  );
  return events.value.filter((event) => event.type === "user.input.requested" && !resolved.has(String(event.payload.requestId || "")));
});
const displayEvents = computed(() => {
  const filtered = [...events.value]
    .sort((a, b) => a.id - b.id)
    .filter((event) => {
      if (event.type === "session.status") return false;
      if (event.type === "approval.resolved") return false;
      if (event.type === "user.input.resolved") return false;
      if (event.type === "turn.done") return false;
      if (event.type === "context.usage") return false;
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
const hasEarlierEvents = computed(() => historyHasMore.value || hiddenEventCount.value > 0);
const visibleEvents = computed(() => displayEvents.value.slice(-renderLimit.value));

onMounted(async () => {
  loadSidebarPreferences();
  applyTheme();
  window.addEventListener("pagehide", releaseActiveThreadOnPageHide);
  await checkAuth();
  if (auth.value.authenticated) await refresh();
});

onBeforeUnmount(() => {
  historyObserver?.disconnect();
  historyObserver = null;
  window.removeEventListener("pagehide", releaseActiveThreadOnPageHide);
  releaseActiveThreadOnPageHide();
});

watch(theme, () => {
  applyTheme();
});

watch(activeSessionId, (sessionId) => {
  connectEvents(sessionId);
	queuedSubmissions.value = [];
	threadGoal.value = null;
	composerMenuOpen.value = false;
	modelMenuOpen.value = false;
	slashMenuOpen.value = false;
	goalEditorOpen.value = false;
	if (sessionId && !activeSessionReadOnly.value) {
		void loadQueue(sessionId);
		void loadGoal(sessionId);
	}
});

watch(() => events.value.length, async () => {
  await nextTick();
  if (historyLoading.value) return;
  if (forceScrollToBottom.value) {
    await scrollTranscriptToBottom();
    forceScrollToBottom.value = false;
    return;
  }
  const element = transcriptEl.value;
  if (!element) return;
  const nearBottom = element.scrollHeight - element.clientHeight - element.scrollTop < 96;
  if (!nearBottom) return;
  element.scrollTo({ top: element.scrollHeight, behavior: "smooth" });
});

async function refresh() {
  if (!auth.value.authenticated) return;
  error.value = "";
  try {
    const [healthResult, deviceResult, tokenResult] = await Promise.all([
      getHealth().catch((reason) => ({ error: String(reason) })),
      getDevices(),
      getTokens()
    ]);
    health.value = healthResult;
    if (typeof healthResult === "object" && healthResult !== null && "p2pOnly" in healthResult) {
      p2p.setP2POnly(Boolean((healthResult as { p2pOnly?: unknown }).p2pOnly));
    }
    devices.value = deviceResult.devices;
    tokens.value = tokenResult.tokens;
    if (!activeDeviceId.value) {
      sessions.value = [];
      events.value = [];
      return;
    }
    const selectedDevice = devices.value.find((device) => device.id === activeDeviceId.value);
    if (!selectedDevice?.online) {
		await backToDevices();
      return;
    }
    const direct = await connectP2P(activeDeviceId.value);
    const [sessionResult, settingsResult, modelResult, skillResult] = await Promise.all([
      direct
        ? p2p.request<{ sessions: SessionRecord[] }>("threads.list", {})
        : getThreads(activeDeviceId.value),
      direct
        ? p2p.request<AppSettings>("settings.get", {})
        : getSettings(activeDeviceId.value),
      (direct
        ? p2p.request<{ models: ModelOption[] }>("models.list", {})
        : getModels(activeDeviceId.value)).catch(() => ({ models: [] })),
      (direct
        ? p2p.request<{ skills: SkillOption[] }>("skills.list", {})
        : getSkills(activeDeviceId.value)).catch(() => ({ skills: [] }))
    ]);
    sessions.value = sessionResult.sessions;
    settings.value = { ...defaultAppSettings, ...settingsResult };
    models.value = modelResult.models;
    skills.value = skillResult.skills;
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
    activeDeviceId.value = "";
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
  error.value = "";
  try {
    await releaseActiveThread();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
    return;
  }
  p2p.close(false);
  p2p.setP2POnly(false);
  await logout();
  auth.value = await getAuthStatus();
  activeSessionId.value = "";
  events.value = [];
  sessions.value = [];
  devices.value = [];
  tokens.value = [];
  activeDeviceId.value = "";
  userMenuOpen.value = false;
  modalView.value = null;
  systemManagementOpen.value = false;
  adminUsers.value = [];
}

function openModal(view: "user" | "tokens") {
  userMenuOpen.value = false;
  error.value = "";
  modalView.value = view;
}

function closeModal() {
  modalView.value = null;
  portMappingHelp.value = null;
}

async function openPortMappingModal() {
  error.value = "";
  portMappingError.value = "";
  portMappingMessage.value = "";
  portMappingHelp.value = null;
  cancelPortMapping();
  modalView.value = "ports";
  await refreshPortMappings();
}

function closeNotice() {
  noticeMessage.value = "";
}

async function openSystemManagement() {
  userMenuOpen.value = false;
  modalView.value = null;
  systemManagementOpen.value = true;
  systemSection.value = "users";
  await refreshAdminUsers();
}

function closeSystemManagement() {
  systemManagementOpen.value = false;
  adminError.value = "";
  adminMessage.value = "";
}

function emptyPortMappingDraft(): PortMappingDraft {
  const device = devices.value.find((item) => item.online) || devices.value[0];
  return {
    deviceId: device?.id || "",
    name: "",
    targetHost: "127.0.0.1",
    targetPort: 3000,
    listenPort: 19022,
    protocol: "tcp",
    enabled: true
  };
}

async function refreshAdminUsers() {
  if (!auth.value.isAdmin) return;
  adminUsersLoading.value = true;
  adminError.value = "";
  try {
    const result = await getAdminUsers();
    adminUsers.value = result.users;
  } catch (reason) {
    adminError.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    adminUsersLoading.value = false;
  }
}

async function refreshPortMappings() {
  portMappingsLoading.value = true;
  portMappingError.value = "";
  try {
    portMappings.value = (await getPortMappings()).mappings;
  } catch (reason) {
    portMappingError.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    portMappingsLoading.value = false;
  }
}

function beginPortMapping(mapping?: PortMapping) {
  portMappingEditingId.value = mapping?.id || "";
  portMappingForm.value = mapping
    ? {
        deviceId: mapping.deviceId,
        name: mapping.name,
        targetHost: mapping.targetHost,
        targetPort: mapping.targetPort,
        listenPort: mapping.listenPort,
        protocol: "tcp",
        enabled: mapping.enabled
      }
    : emptyPortMappingDraft();
  portMappingError.value = "";
  portMappingMessage.value = "";
}

function cancelPortMapping() {
  portMappingEditingId.value = "";
  portMappingForm.value = emptyPortMappingDraft();
  portMappingHelp.value = null;
}

function togglePortMappingHelp(help: PortMappingHelp) {
  portMappingHelp.value = portMappingHelp.value === help ? null : help;
}

async function savePortMapping() {
  const form = portMappingForm.value;
  if (!form.deviceId) {
    portMappingError.value = "请选择目标设备";
    return;
  }
  if (!form.name.trim()) {
    portMappingError.value = "请输入映射名称";
    return;
  }
  if (form.targetPort < 1 || form.targetPort > 65535 || form.listenPort < 1 || form.listenPort > 65535) {
    portMappingError.value = "端口必须是 1 到 65535";
    return;
  }
  portMappingsLoading.value = true;
  portMappingError.value = "";
  portMappingMessage.value = "";
  try {
    if (portMappingEditingId.value) {
      await updatePortMapping(portMappingEditingId.value, { ...form });
      portMappingMessage.value = "端口映射已更新";
    } else {
      await createPortMapping({ ...form });
      portMappingMessage.value = "端口映射已创建";
    }
    cancelPortMapping();
    await refreshPortMappings();
  } catch (reason) {
    portMappingError.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    portMappingsLoading.value = false;
  }
}

async function removePortMapping(mapping: PortMapping) {
  if (!window.confirm(`确认删除端口映射“${mapping.name}”吗？公开端口会立即停止监听。`)) return;
  portMappingsLoading.value = true;
  portMappingError.value = "";
  try {
    await deletePortMapping(mapping.id);
    portMappingMessage.value = `映射 ${mapping.name} 已删除`;
    await refreshPortMappings();
  } catch (reason) {
    portMappingError.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    portMappingsLoading.value = false;
  }
}

async function togglePortMapping(mapping: PortMapping) {
  portMappingForm.value = {
    deviceId: mapping.deviceId,
    name: mapping.name,
    targetHost: mapping.targetHost,
    targetPort: mapping.targetPort,
    listenPort: mapping.listenPort,
    protocol: "tcp",
    enabled: !mapping.enabled
  };
  portMappingEditingId.value = mapping.id;
  await savePortMapping();
}

async function toggleAdminRole(user: AdminUser) {
  if (user.id === auth.value.userId) {
    adminError.value = "当前登录账号不能在系统管理中修改";
    return;
  }
  adminUsersLoading.value = true;
  adminError.value = "";
  adminMessage.value = "";
  try {
    await updateAdminUser(user.id, !user.isAdmin);
    adminMessage.value = `${user.username} 已${user.isAdmin ? "取消管理员" : "设为管理员"}`;
    await refreshAdminUsers();
  } catch (reason) {
    adminError.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    adminUsersLoading.value = false;
  }
}

async function removeAdminUser(user: AdminUser) {
  if (user.id === auth.value.userId) {
    adminError.value = "当前登录账号不能在系统管理中修改";
    return;
  }
  if (!window.confirm(`确认删除用户“${user.username}”吗？该用户的设备、会话和秘钥也会被删除。`)) return;
  adminUsersLoading.value = true;
  adminError.value = "";
  adminMessage.value = "";
  try {
    await deleteAdminUser(user.id);
    adminMessage.value = `用户 ${user.username} 已删除`;
    await refreshAdminUsers();
  } catch (reason) {
    adminError.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    adminUsersLoading.value = false;
  }
}

async function selectDevice(device: RemoteDevice) {
  if (!device.online) return;
  loading.value = true;
  error.value = "";
  try {
    if (activeSessionId.value) await releaseActiveThread();
    activeDeviceId.value = device.id;
    selectedProjectCwd.value = "";
    sidebarOpen.value = false;
    await refresh();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function removeDevice(device: RemoteDevice) {
  if (device.online) return;
  if (!window.confirm(`确认删除离线设备“${device.name}”吗？该设备的历史会话记录会保留。`)) return;
  loading.value = true;
  error.value = "";
  try {
    await deleteDevice(device.id);
    await refresh();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function backToDevices() {
  error.value = "";
  try {
    await releaseActiveThread();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
    return;
  }
  p2p.close(false);
  eventSource.value?.close();
  eventSource.value = null;
  activeDeviceId.value = "";
  activeSessionId.value = "";
  selectedProjectCwd.value = "";
  events.value = [];
  sessions.value = [];
  streamState.value = "idle";
  transportState.value = "idle";
}

async function connectP2P(deviceId: string) {
  if (p2p.isConnected() && p2p.deviceId === deviceId) {
    transportState.value = "p2p";
    return true;
  }
  transportState.value = "connecting";
  try {
    await p2p.connect(deviceId);
    transportState.value = "p2p";
    return true;
  } catch (reason) {
    if (p2p.isP2POnly()) {
      transportState.value = "failed";
      throw reason;
    }
    transportState.value = "relay";
    return false;
  }
}

async function createAccessToken() {
  loading.value = true;
  error.value = "";
  tokenMessage.value = "";
  try {
    const result = await createToken(tokenName.value.trim());
    tokenName.value = "";
    tokenMessage.value = `Token“${result.token.name}”已创建`;
    await refreshTokens();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function rotateAccessToken(token: AccessToken) {
  if (!window.confirm(`确认刷新“${token.name}”吗？旧 Token 将无法在客户端下次启动或重连时使用。`)) return;
  loading.value = true;
  error.value = "";
  try {
    await refreshToken(token.id);
    tokenMessage.value = `Token“${token.name}”已刷新`;
    await refreshTokens();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function revokeAccessToken(token: AccessToken) {
  if (!window.confirm(`确认删除“${token.name}”吗？使用该 Token 的客户端下次启动或重连会失败。`)) return;
  loading.value = true;
  error.value = "";
  try {
    await deleteToken(token.id);
    tokenMessage.value = `Token“${token.name}”已删除`;
    await refreshTokens();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function refreshTokens() {
  const result = await getTokens();
  tokens.value = result.tokens;
}

async function commandWithFallback<T>(action: string, payload: unknown, fallback: () => Promise<T>) {
  if (!p2p.isConnected()) {
    if (p2p.isP2POnly()) throw new Error("P2P 连接未建立，已禁止服务端中转");
    return fallback();
  }
  try {
    return await p2p.request<T>(action, payload);
  } catch (reason) {
    if (reason instanceof P2PRemoteError) throw reason;
    p2p.close(true);
    if (p2p.isP2POnly()) throw reason;
    return fallback();
  }
}

async function copyText(text: string) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return;
    } catch {
      // HTTP and restricted browser contexts can reject the modern clipboard API.
    }
  }

  const textarea = document.createElement("textarea");
  textarea.value = text;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "-9999px";
  textarea.style.opacity = "0";
  let copied = false;
  try {
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    textarea.setSelectionRange(0, text.length);
    copied = document.execCommand("copy");
  } finally {
    textarea.remove();
  }
  if (!copied) throw new Error("clipboard unavailable");
}

async function copyToken(token: string) {
  error.value = "";
  tokenMessage.value = "";
  noticeMessage.value = "";
  try {
    await copyText(token);
    tokenMessage.value = "Token 已复制";
  } catch {
    noticeMessage.value = "复制失败，请检查浏览器剪贴板权限，或直接选中 Token 文本后复制。";
  }
}

function loadSidebarPreferences() {
  try {
    const storedProjects = JSON.parse(localStorage.getItem(projectOrderStorageKey) || "[]");
    const storedRecent = JSON.parse(localStorage.getItem(recentOrderStorageKey) || "[]");
    if (Array.isArray(storedProjects)) projectOrder.value = storedProjects.filter((item): item is string => typeof item === "string");
    if (Array.isArray(storedRecent)) recentOrder.value = storedRecent.filter((item): item is string => typeof item === "string");
    const storedTheme = localStorage.getItem("codex-link-theme");
    if (storedTheme === "light" || storedTheme === "dark") theme.value = storedTheme;
  } catch {
    projectOrder.value = [];
    recentOrder.value = [];
  }
}

function applyTheme() {
  document.documentElement.dataset.theme = theme.value;
  try {
    localStorage.setItem("codex-link-theme", theme.value);
  } catch {
    // Private browsing can make localStorage unavailable.
  }
}

function toggleTheme() {
  theme.value = theme.value === "dark" ? "light" : "dark";
}

function applyStoredOrder<T>(items: T[], order: string[], key: (item: T) => string) {
  const positions = new Map(order.map((id, index) => [id, index]));
  return items
    .map((item, index) => ({ item, index }))
    .sort((a, b) => {
      const aPosition = positions.get(key(a.item)) ?? order.length + a.index;
      const bPosition = positions.get(key(b.item)) ?? order.length + b.index;
      return aPosition - bPosition;
    })
    .map(({ item }) => item);
}

function sortSessionsByUpdatedAt(items: SessionRecord[]) {
  return [...items].sort((a, b) => compareUpdatedAt(a.updatedAt, b.updatedAt));
}

function compareUpdatedAt(a?: string, b?: string) {
  const aTime = Date.parse(a || "");
  const bTime = Date.parse(b || "");
  if (!Number.isNaN(aTime) && !Number.isNaN(bTime) && aTime !== bTime) return bTime - aTime;
  if (!Number.isNaN(aTime) && Number.isNaN(bTime)) return -1;
  if (Number.isNaN(aTime) && !Number.isNaN(bTime)) return 1;
  return 0;
}

function moveItem<T>(items: T[], source: string, target: string, key: (item: T) => string) {
  const ids = items.map(key);
  const sourceIndex = ids.indexOf(source);
  const targetIndex = ids.indexOf(target);
  if (sourceIndex < 0 || targetIndex < 0 || sourceIndex === targetIndex) return ids;
  const next = [...ids];
  const [moved] = next.splice(sourceIndex, 1);
  next.splice(next.indexOf(target), 0, moved);
  return next;
}

function dragKey(kind: "project" | "recent", id: string) {
  return `${kind}:${id}`;
}

function beginDrag(event: DragEvent, kind: "project" | "recent", id: string) {
  if (!event.dataTransfer) return;
  dragState.value = { kind, id };
  dragOverKey.value = "";
  event.dataTransfer.effectAllowed = "move";
  event.dataTransfer.setData("text/plain", `${kind}:${id}`);
}

function trackDragOver(event: DragEvent, kind: "project" | "recent", id: string) {
  if (!dragState.value || dragState.value.kind !== kind || dragState.value.id === id) return;
  event.preventDefault();
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
  dragOverKey.value = dragKey(kind, id);
}

function finishDrop(kind: "project" | "recent", target: string) {
  const source = dragState.value;
  if (!source || source.kind !== kind || source.id === target) {
    endDrag();
    return;
  }
  if (kind === "project") {
    projectOrder.value = moveItem(groupedSessions.value, source.id, target, (group) => group.cwd);
    try {
      localStorage.setItem(projectOrderStorageKey, JSON.stringify(projectOrder.value));
    } catch {
      // The order remains active for this page even when storage is unavailable.
    }
  } else {
    recentOrder.value = moveItem(recentSessions.value, source.id, target, (session) => session.id);
    try {
      localStorage.setItem(recentOrderStorageKey, JSON.stringify(recentOrder.value));
    } catch {
      // The order remains active for this page even when storage is unavailable.
    }
  }
  endDrag();
}

function endDrag() {
  dragState.value = null;
  dragOverKey.value = "";
}

async function startSession() {
  loading.value = true;
  error.value = "";
  try {
    await releaseActiveThread();
    const cwd = selectedProjectCwd.value || undefined;
    const { session } = await commandWithFallback(
      "sessions.create",
      { cwd },
      () => createSession(undefined, activeDeviceId.value, cwd)
    );
    upsertSession(session);
    activeSessionId.value = session.id;
    sidebarOpen.value = false;
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    loading.value = false;
  }
}

async function openThread(session: SessionRecord) {
  if (session.id === activeSessionId.value || loading.value || openingThreadId.value) return;
  openingThreadId.value = session.id;
  loading.value = true;
  error.value = "";
  try {
    await releaseActiveThread();
    const { session: resumed } = await commandWithFallback(
      "threads.resume",
      { id: session.id },
      () => resumeThread(session.id)
    );
    upsertSession(resumed);
    selectedProjectCwd.value = isRecentSession(resumed) ? "" : resumed.cwd || "";
    forceScrollToBottom.value = true;
    activeSessionId.value = resumed.id;
		if (resumed.mode === "host-readonly") {
			draft.value = "";
			attachments.value = [];
		}
    sidebarOpen.value = false;
    await nextTick();
    await scrollTranscriptToBottom();
  } catch (reason) {
    if (isActiveWriterError(reason)) {
      const readOnlySession: SessionRecord = {
        ...session,
        mode: "host-readonly",
        note: "该对话正在由本机 Codex 使用，当前仅可查看历史。"
      };
      upsertSession(readOnlySession);
      forceScrollToBottom.value = true;
      activeSessionId.value = readOnlySession.id;
      draft.value = "";
      attachments.value = [];
      sidebarOpen.value = false;
      await nextTick();
      await scrollTranscriptToBottom();
      return;
    }
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    openingThreadId.value = "";
    loading.value = false;
  }
}

async function removeThread(session: SessionRecord) {
	if (session.mode === "host-readonly") return;
  if (!window.confirm(`确认删除“${session.title}”吗？该对话会同步从 Codex 列表中归档。`)) return;
  loading.value = true;
  error.value = "";
  try {
    if (activeSessionId.value === session.id) await releaseActiveThread();
    await commandWithFallback(
      "threads.archive",
      { id: session.id },
      () => deleteThread(session.id)
    );
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
	const session = activeSession.value;
	if (!session || activeSessionReadOnly.value || !composerHasInput.value || composerBusy.value) return;
  composerBusy.value = true;
  error.value = "";
  try {
		const sessionID = session.id;
    const text = draft.value.trim();
    const messageAttachments = [...attachments.value];
    if (activeSessionRunning.value) {
      const result = p2p.isConnected() && messageAttachments.every((attachment) => attachment.transport === "p2p" && Boolean(attachment.path))
        ? await commandWithFallback<{ queuedSubmission: QueuedSubmission }>(
            "queue.add",
            { id: sessionID, text, attachments: messageAttachments.map(({ dataUrl: _dataUrl, previewUrl: _previewUrl, transport: _transport, ...attachment }) => attachment) },
            async () => addQueue(sessionID, text, await relayAttachments(messageAttachments))
          )
        : await addQueue(sessionID, text, await relayAttachments(messageAttachments));
      queuedSubmissions.value.push(result.queuedSubmission);
    } else {
      if (p2p.isConnected() && messageAttachments.every((attachment) => attachment.transport === "p2p" && Boolean(attachment.path))) {
        const directAttachments = messageAttachments.map(({ dataUrl: _dataUrl, previewUrl: _previewUrl, transport: _transport, ...attachment }) => attachment);
        await commandWithFallback(
          "sessions.message",
          { id: sessionID, text, attachments: directAttachments },
          async () => sendMessage(sessionID, text, await relayAttachments(messageAttachments))
        );
      } else {
        await sendMessage(sessionID, text, await relayAttachments(messageAttachments));
      }
    }
    draft.value = "";
    attachments.value = [];
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    composerBusy.value = false;
  }
}

function queueText(item: QueuedSubmission) {
  return item.input.filter((part) => part.type === "text").map((part) => part.text || "").join("\n");
}

function queueAttachments(item: QueuedSubmission): Attachment[] {
  return item.input
    .filter((part) => part.type === "localImage" || part.type === "mention")
    .map((part) => ({ name: part.name || part.path?.split(/[\\/]/).pop() || "附件", path: part.path, mimeType: part.type === "localImage" ? "image/*" : "" }));
}

async function loadQueue(sessionID: string) {
	if (activeSessionReadOnly.value) return;
  queueLoading.value = true;
  try {
    const result = await commandWithFallback<{ queue: QueuedSubmission[] }>("queue.list", { id: sessionID }, () => getQueue(sessionID));
    if (activeSessionId.value === sessionID) queuedSubmissions.value = result.queue || [];
  } catch (reason) {
    if (activeSessionId.value === sessionID) error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    queueLoading.value = false;
  }
}

async function loadGoal(sessionID: string) {
	if (activeSessionReadOnly.value) return;
  try {
    const result = await commandWithFallback<{ goal: ThreadGoal | null }>("goal.get", { id: sessionID }, () => getGoal(sessionID));
    if (activeSessionId.value === sessionID) threadGoal.value = result.goal;
  } catch (reason) {
    if (activeSessionId.value === sessionID) error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

function editQueue(item: QueuedSubmission) {
  editingQueueID.value = item.id;
  queuedMessageDraft.value = queueText(item);
  queuedMessageInput.value = item.input;
}

function cancelEditQueue() {
  editingQueueID.value = "";
  queuedMessageDraft.value = "";
  queuedMessageInput.value = [];
}

async function saveQueueEdit() {
  if (!activeSession.value || !editingQueueID.value || !queuedMessageDraft.value.trim()) return;
  queueActionID.value = editingQueueID.value;
  try {
    const input = queuedMessageInput.value.map((part) => part.type === "text" ? { ...part, text: queuedMessageDraft.value.trim() } : part);
    const result = await commandWithFallback<{ queuedSubmission: QueuedSubmission }>(
      "queue.update",
      { id: activeSession.value.id, submissionId: editingQueueID.value, input },
      () => updateQueue(activeSession.value!.id, editingQueueID.value, input)
    );
    const index = queuedSubmissions.value.findIndex((item) => item.id === editingQueueID.value);
    if (index >= 0) queuedSubmissions.value[index] = result.queuedSubmission;
    cancelEditQueue();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    queueActionID.value = "";
  }
}

async function removeQueue(item: QueuedSubmission) {
  if (!activeSession.value) return;
  queueActionID.value = item.id;
  try {
    await commandWithFallback("queue.delete", { id: activeSession.value.id, submissionId: item.id }, () => deleteQueue(activeSession.value!.id, item.id));
    queuedSubmissions.value = queuedSubmissions.value.filter((entry) => entry.id !== item.id);
    if (editingQueueID.value === item.id) cancelEditQueue();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    queueActionID.value = "";
  }
}

async function moveQueue(item: QueuedSubmission, direction: -1 | 1) {
  if (!activeSession.value) return;
  const index = queuedSubmissions.value.findIndex((entry) => entry.id === item.id);
  const next = index + direction;
  if (index < 0 || next < 0 || next >= queuedSubmissions.value.length) return;
  const ids = queuedSubmissions.value.map((entry) => entry.id);
  [ids[index], ids[next]] = [ids[next], ids[index]];
  queueActionID.value = item.id;
  try {
    await commandWithFallback("queue.reorder", { id: activeSession.value.id, submissionIds: ids }, () => reorderQueue(activeSession.value!.id, ids));
    const items = [...queuedSubmissions.value];
    [items[index], items[next]] = [items[next], items[index]];
    queuedSubmissions.value = items;
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    queueActionID.value = "";
  }
}

async function promoteQueued(item: QueuedSubmission) {
  if (!activeSession.value) return;
  queueActionID.value = item.id;
  try {
    await commandWithFallback("queue.promote", { id: activeSession.value.id, submissionId: item.id }, () => promoteQueue(activeSession.value!.id, item.id));
    queuedSubmissions.value = queuedSubmissions.value.filter((entry) => entry.id !== item.id);
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    queueActionID.value = "";
  }
}

async function saveGoalDraft() {
  if (!activeSession.value || !goalDraft.value.trim()) return;
  try {
    const result = await commandWithFallback<{ goal: ThreadGoal }>("goal.set", { id: activeSession.value.id, objective: goalDraft.value }, () => setGoal(activeSession.value!.id, goalDraft.value));
    threadGoal.value = result.goal;
    goalEditorOpen.value = false;
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

async function removeGoal() {
  if (!activeSession.value) return;
  try {
    await commandWithFallback("goal.clear", { id: activeSession.value.id }, () => clearGoal(activeSession.value!.id));
    threadGoal.value = null;
    goalEditorOpen.value = false;
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

async function saveSettings(next: Partial<AppSettings>) {
  const previous = settings.value;
  settings.value = { ...settings.value, ...next };
  try {
    const saved = await commandWithFallback(
      "settings.update",
      settings.value,
      () => updateSettings(settings.value, activeDeviceId.value)
    );
    settings.value = { ...defaultAppSettings, ...saved };
  } catch (reason) {
    settings.value = previous;
    error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

function handleComposerInput() {
  const visible = slashToken.value !== null;
  slashMenuOpen.value = visible;
  if (visible) {
    slashSelectionIndex.value = 0;
    composerMenuOpen.value = false;
    modelMenuOpen.value = false;
  }
}

function handleComposerKeydown(event: KeyboardEvent) {
  if (slashMenuVisible.value) {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      slashSelectionIndex.value = (slashSelectionIndex.value + 1) % slashSuggestions.value.length;
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      slashSelectionIndex.value = (slashSelectionIndex.value - 1 + slashSuggestions.value.length) % slashSuggestions.value.length;
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      slashMenuOpen.value = false;
      return;
    }
    if ((event.key === "Enter" || event.key === "Tab") && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      insertSlashSuggestion(slashSuggestions.value[slashSelectionIndex.value]);
      return;
    }
  }
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
  event.preventDefault();
  void submitMessage();
}

function insertSlashSuggestion(option?: SkillOption) {
  const token = slashToken.value;
  if (!token || !option) return;
  const insertion = `${option.command} `;
  draft.value = `${draft.value.slice(0, token.start)}${insertion}${draft.value.slice(token.end)}`;
  slashMenuOpen.value = false;
  nextTick(() => {
    const textarea = composerTextarea.value;
    if (!textarea) return;
    const cursor = token.start + insertion.length;
    textarea.focus();
    textarea.setSelectionRange(cursor, cursor);
  });
}

function selectModel(event: Event) {
  const model = (event.target as HTMLSelectElement).value;
  const selected = models.value.find((item) => item.model === model)
    ?? models.value.find((item) => item.isDefault)
    ?? models.value[0];
  const supported = new Set((selected?.supportedReasoningEfforts || []).map((item) => item.reasoningEffort));
  const currentEffort = settings.value.reasoningEffort;
  const reasoningEffort = currentEffort !== "" && supported.has(currentEffort) ? currentEffort : "";
  void saveSettings({ model, reasoningEffort });
}

function selectReasoningEffort(event: Event) {
  void saveSettings({ reasoningEffort: (event.target as HTMLSelectElement).value as AppSettings["reasoningEffort"] });
}

function toggleModelMenu() {
  if (!canModifyActiveSession.value) return;
  modelMenuOpen.value = !modelMenuOpen.value;
  if (modelMenuOpen.value) {
    composerMenuOpen.value = false;
    goalEditorOpen.value = false;
  }
}

function selectWorkMode(event: Event) {
  void saveSettings({ workMode: (event.target as HTMLSelectElement).value as AppSettings["workMode"] });
}

function reasoningEffortLabel(effort: AppSettings["reasoningEffort"]) {
  switch (effort) {
    case "minimal": return "最小";
    case "low": return "低";
    case "medium": return "中";
    case "high": return "高";
    case "xhigh": return "极高";
    default: return "默认推理";
  }
}

function openFilePicker() {
	if (!canModifyActiveSession.value) return;
  composerMenuOpen.value = false;
  modelMenuOpen.value = false;
  fileInput.value?.click();
}

function openGoalEditor() {
	if (!canModifyActiveSession.value) return;
  goalDraft.value = threadGoal.value?.objective || "";
  goalEditorOpen.value = true;
  composerMenuOpen.value = false;
  modelMenuOpen.value = false;
}

async function addFiles(files: File[]) {
	if (!canModifyActiveSession.value) return;
  try {
    for (const file of files) await addAttachmentFile(file);
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

async function handlePaste(event: ClipboardEvent) {
	if (!canModifyActiveSession.value) return;
  const items = Array.from(event.clipboardData?.items || []);
  const imageItems = items.filter((item) => item.type.startsWith("image/"));
  if (imageItems.length === 0) return;
  event.preventDefault();
  await addFiles(imageItems.map((item) => item.getAsFile()).filter((file): file is File => Boolean(file)));
}

async function handleFilePicked(event: Event) {
	if (!canModifyActiveSession.value) return;
  const input = event.target as HTMLInputElement;
  const files = Array.from(input.files || []);
  await addFiles(files);
  input.value = "";
}

function handleComposerDragOver(event: DragEvent) {
	if (!canModifyActiveSession.value) return;
  const types = Array.from(event.dataTransfer?.types || []);
  if (!types.includes("Files")) return;
  event.preventDefault();
  composerDropActive.value = true;
  if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
}

function handleComposerDragLeave(event: DragEvent) {
  const current = event.currentTarget as HTMLElement;
  const related = event.relatedTarget as Node | null;
  if (!related || !current.contains(related)) composerDropActive.value = false;
}

async function handleComposerDrop(event: DragEvent) {
	if (!canModifyActiveSession.value) return;
  event.preventDefault();
  composerDropActive.value = false;
  try {
    const files = await droppedFiles(event.dataTransfer);
    await addFiles(files);
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

async function droppedFiles(dataTransfer: DataTransfer | null): Promise<File[]> {
  if (!dataTransfer) return [];
  const entries = Array.from(dataTransfer.items)
    .map((item) => item.webkitGetAsEntry?.())
    .filter((entry): entry is FileSystemEntry => entry !== null);
  if (entries.length === 0) return Array.from(dataTransfer.files);
  return (await Promise.all(entries.map(entryFiles))).flat();
}

async function entryFiles(entry: FileSystemEntry): Promise<File[]> {
  if (entry.isFile) {
    const fileEntry = entry as FileSystemFileEntry;
    return new Promise((resolve) => fileEntry.file((file) => resolve([file]), () => resolve([])));
  }
  if (!entry.isDirectory) return [];
  const reader = (entry as FileSystemDirectoryEntry).createReader();
  const children: FileSystemEntry[] = [];
  while (true) {
    const batch = await new Promise<FileSystemEntry[]>((resolve) => reader.readEntries(resolve, () => resolve([])));
    if (batch.length === 0) break;
    children.push(...batch);
  }
  return (await Promise.all(children.map(entryFiles))).flat();
}

async function addAttachmentFile(file: File) {
  const dataUrl = await fileToDataURL(file);
  const name = file.name || `attachment-${Date.now()}.bin`;
  if (!p2p.isConnected() && p2p.isP2POnly()) {
    throw new Error("P2P 连接未建立，已禁止服务端文件中转");
  }
  if (p2p.isConnected()) {
    try {
      const attachment = await p2p.uploadAttachment(name, file.type, dataUrl);
      attachments.value.push({ ...attachment, dataUrl, previewUrl: dataUrl, transport: "p2p" });
      return;
    } catch {
      p2p.close(true);
      if (p2p.isP2POnly()) throw new Error("P2P 文件传输失败，已禁止服务端中转");
    }
  }
  const { attachment } = await uploadAttachment(name, file.type || "application/octet-stream", dataUrl);
  attachments.value.push({ ...attachment, dataUrl, previewUrl: file.type.startsWith("image/") ? dataUrl : undefined, transport: "relay" });
}

async function relayAttachments(source: Attachment[]) {
  const result: Attachment[] = [];
  for (const attachment of source) {
    if (attachment.transport !== "p2p") {
      if (p2p.isP2POnly()) throw new Error("P2P-only 模式不允许使用服务端图片");
      result.push(attachment);
      continue;
    }
    if (p2p.isP2POnly()) throw new Error("P2P 图片无法回退到服务端，请重新选择图片");
    if (!attachment.dataUrl) throw new Error("P2P 图片无法回退到服务端，请重新选择图片");
    const uploaded = await uploadAttachment(attachment.name || `attachment-${Date.now()}.bin`, attachment.mimeType || "application/octet-stream", attachment.dataUrl);
    result.push({ ...uploaded.attachment, transport: "relay" });
  }
  return result;
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
  await commandWithFallback(
    "sessions.approval",
    { id: activeSession.value.id, approvalId, decision },
    () => sendApproval(activeSession.value!.id, approvalId, decision)
  );
}

type UserInputOption = { label: string; description: string };
type UserInputQuestion = {
  id: string;
  header: string;
  question: string;
  isOther?: boolean;
  options?: UserInputOption[] | null;
};

function userInputRequestID(event: RemoteEvent) {
  return String(event.payload.requestId || "");
}

function userInputQuestions(event: RemoteEvent): UserInputQuestion[] {
  return Array.isArray(event.payload.questions) ? event.payload.questions as UserInputQuestion[] : [];
}

function userInputSelection(event: RemoteEvent, questionID: string) {
  return userInputSelections.value[userInputRequestID(event)]?.[questionID] || "";
}

function chooseUserInput(event: RemoteEvent, questionID: string, value: string) {
  const requestID = userInputRequestID(event);
  userInputSelections.value = {
    ...userInputSelections.value,
    [requestID]: {
      ...(userInputSelections.value[requestID] || {}),
      [questionID]: value
    }
  };
}

function updateUserInput(event: RemoteEvent, questionID: string, inputEvent: Event) {
  chooseUserInput(event, questionID, (inputEvent.target as HTMLInputElement).value);
}

function canSubmitUserInput(event: RemoteEvent) {
  const selections = userInputSelections.value[userInputRequestID(event)] || {};
  return userInputQuestions(event).length > 0 && userInputQuestions(event).every((question) => Boolean(selections[question.id]?.trim()));
}

async function submitUserInput(event: RemoteEvent) {
  if (!activeSession.value || !canSubmitUserInput(event)) return;
  const requestID = userInputRequestID(event);
  const selected = userInputSelections.value[requestID] || {};
  const answers = Object.fromEntries(Object.entries(selected).map(([questionID, answer]) => [questionID, [answer]]));
  try {
    await commandWithFallback(
      "sessions.user-input",
      { id: activeSession.value.id, requestId: requestID, answers },
      () => sendUserInput(activeSession.value!.id, requestID, answers)
    );
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

async function cancel() {
  if (!activeSession.value) return;
  await commandWithFallback(
    "sessions.cancel",
    { id: activeSession.value.id },
    () => cancelTurn(activeSession.value!.id)
  );
}

function mergeEvents(existing: RemoteEvent[], incoming: RemoteEvent[]) {
  const merged = new Map<string, RemoteEvent>();
  for (const event of [...existing, ...incoming]) merged.set(eventKey(event), event);
  return [...merged.values()].sort((a, b) => a.id - b.id);
}

async function loadEventPage(sessionId: string, before: number, replace = false) {
  if (historyLoading.value) return false;
  historyLoading.value = true;
  try {
    const result = activeSessionReadOnly.value && !p2p.isP2POnly()
      ? await getSessionHistory(sessionId, before, listPageSize)
      : await commandWithFallback<{ events: RemoteEvent[]; hasMore: boolean }>(
          "events.list",
          { id: sessionId, before, limit: listPageSize },
          () => getSessionHistory(sessionId, before, listPageSize)
        );
    if (activeSessionId.value !== sessionId) return false;
    events.value = mergeEvents(events.value, result.events || []);
    historyHasMore.value = Boolean(result.hasMore);
    if (replace) renderLimit.value = listPageSize;
    else renderLimit.value += listPageSize;
    return true;
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
    return false;
  } finally {
    historyLoading.value = false;
  }
}

async function observeHistorySentinel() {
  await nextTick();
  historyObserver?.disconnect();
  historyObserver = null;
  if (!hasEarlierEvents.value || !transcriptEl.value || !historySentinelEl.value || !("IntersectionObserver" in window)) return;
  historyObserver = new IntersectionObserver((entries) => {
    if (entries.some((entry) => entry.isIntersecting)) void loadOlderEvents();
  }, {
    root: transcriptEl.value,
    rootMargin: "160px 0px 0px",
    threshold: 0
  });
  historyObserver.observe(historySentinelEl.value);
}

async function loadOlderEvents() {
  if (!activeSession.value || historyLoading.value || !hasEarlierEvents.value) return;
  const element = transcriptEl.value;
  const previousHeight = element?.scrollHeight || 0;
  const previousTop = element?.scrollTop || 0;
  if (!historyHasMore.value) {
    renderLimit.value += listPageSize;
    await nextTick();
    if (element) element.scrollTop = previousTop + element.scrollHeight - previousHeight;
    void observeHistorySentinel();
    return;
  }
  const oldest = [...events.value].sort((a, b) => a.id - b.id)[0]?.id || 0;
  if (!oldest) {
    historyHasMore.value = false;
    return;
  }
  const loaded = await loadEventPage(activeSession.value.id, oldest);
  if (!loaded || !element) return;
  await nextTick();
  element.scrollTop = previousTop + element.scrollHeight - previousHeight;
  void observeHistorySentinel();
}

function connectEvents(sessionId: string, reset = true) {
  eventSource.value?.close();
  if (reset) {
    historyObserver?.disconnect();
    historyObserver = null;
    historyScrollArmed = false;
    events.value = [];
    renderLimit.value = listPageSize;
    historyHasMore.value = false;
    forceScrollToBottom.value = true;
  }
  if (!sessionId) return;

  drainPendingEvents(sessionId);
  void loadEventPage(sessionId, 0, true);

  if (p2p.isConnected() && !activeSessionReadOnly.value) {
    streamState.value = "connected";
    return;
  }

  if (p2p.isP2POnly()) {
    streamState.value = "idle";
    transportState.value = "failed";
    return;
  }

  streamState.value = "reconnecting";
  const source = new EventSource(`/api/sessions/${sessionId}/events?limit=${listPageSize}`);
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
    "user.input.requested",
    "user.input.resolved",
    "turn.done",
    "context.usage",
    "queue.changed",
    "goal.updated",
    "goal.cleared",
    "error"
  ];
  for (const type of types) {
    source.addEventListener(type, (message) => {
      const event = JSON.parse((message as MessageEvent).data) as RemoteEvent;
      acceptRemoteEvent(event);
    });
  }
}

function acceptRemoteEvent(event: RemoteEvent) {
  if (event.sessionId !== activeSessionId.value) {
    pendingRemoteEvents.set(eventKey(event), event);
    if (pendingRemoteEvents.size > 500) pendingRemoteEvents.delete(pendingRemoteEvents.keys().next().value || "");
    return;
  }
  if (!events.value.some((item) => eventKey(item) === eventKey(event))) events.value.push(event);
  if (events.value.length > 500) events.value = events.value.slice(-500);
  if (event.type === "context.usage" && activeSession.value) {
    const usage = event.payload.usage as TokenUsage | undefined;
    if (usage?.last && usage?.total) {
      upsertSession({ ...activeSession.value, tokenUsage: usage, updatedAt: event.ts });
    }
  }
  if (event.type === "session.status" && activeSession.value) {
    upsertSession({
      ...activeSession.value,
      status: String(event.payload.status || activeSession.value.status),
      mode: String(event.payload.mode || activeSession.value.mode) as SessionRecord["mode"],
      updatedAt: event.ts
    });
  }
  if (event.type === "queue.changed") void loadQueue(event.sessionId);
  if (event.type === "goal.updated") threadGoal.value = (event.payload.goal as ThreadGoal | undefined) || null;
  if (event.type === "goal.cleared") threadGoal.value = null;
}

function drainPendingEvents(sessionId: string) {
  for (const [key, event] of pendingRemoteEvents) {
    if (event.sessionId !== sessionId) continue;
    pendingRemoteEvents.delete(key);
    acceptRemoteEvent(event);
  }
}

function eventKey(event: RemoteEvent) {
  return `${event.sessionId}:${event.type}:${event.ts}:${JSON.stringify(event.payload)}`;
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

function formatTokenCount(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0";
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(value >= 10_000_000 ? 0 : 1)}m`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(value >= 10_000 ? 0 : 1)}k`;
  return String(value);
}

function contextUsageLabel(session?: SessionRecord) {
  const usage = session?.tokenUsage;
  if (!usage) return "上下文 --";
  const used = (usage.last.inputTokens || 0) + (usage.last.cachedInputTokens || 0);
  const window = usage.modelContextWindow || 0;
  return window > 0
    ? `上下文 ${formatTokenCount(used)} / ${formatTokenCount(window)}`
    : `上下文 ${formatTokenCount(used)}`;
}

function contextUsageTitle(session?: SessionRecord) {
  const usage = session?.tokenUsage;
  if (!usage) return "尚未收到 Codex 的上下文统计";
  const used = (usage.last.inputTokens || 0) + (usage.last.cachedInputTokens || 0);
  const window = usage.modelContextWindow || 0;
  return window > 0 ? `当前上下文 ${used.toLocaleString("zh-CN")} / ${window.toLocaleString("zh-CN")} tokens` : `当前上下文 ${used.toLocaleString("zh-CN")} tokens`;
}

function projectLabel(cwd: string) {
  if (!cwd || cwd === "未知项目") return "未知项目";
  const parts = cwd.split(/[\\/]+/).filter(Boolean);
  return parts.at(-1) || cwd;
}

function isRecentSession(session: SessionRecord) {
  return !session.cwd || projectLabel(session.cwd).toLowerCase() === "new-chat";
}

function isProjectCollapsed(cwd: string) {
  return collapsedProjects.value.has(cwd);
}

function visibleProjectSessions(group: { cwd: string; sessions: SessionRecord[] }) {
  return group.sessions.slice(0, visibleProjectSessionCounts.value[group.cwd] || listPageSize);
}

function hasMoreProjectSessions(group: { cwd: string; sessions: SessionRecord[] }) {
  return visibleProjectSessions(group).length < group.sessions.length;
}

function showMoreProjects() {
  visibleProjectCount.value += listPageSize;
}

function showMoreRecentSessions() {
  visibleRecentCount.value += listPageSize;
}

function showMoreProjectSessions(cwd: string) {
  visibleProjectSessionCounts.value = {
    ...visibleProjectSessionCounts.value,
    [cwd]: (visibleProjectSessionCounts.value[cwd] || listPageSize) + listPageSize
  };
}

function armHistoryLoad() {
  historyScrollArmed = true;
}

function onTranscriptScroll() {
  const element = transcriptEl.value;
  if (!historyScrollArmed || historyObserver || !element || element.scrollTop > 160) return;
  historyScrollArmed = false;
  void loadOlderEvents();
}

function selectProject(cwd: string) {
  selectedProjectCwd.value = selectedProjectCwd.value === cwd ? "" : cwd;
  toggleProject(cwd);
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
  const available = new Set(groupedSessions.value.map((group) => group.cwd));
  if (selectedProjectCwd.value && !available.has(selectedProjectCwd.value)) selectedProjectCwd.value = "";
  collapsedProjects.value = new Set(
    groupedSessions.value.map((group) => group.cwd).filter((cwd) => cwd !== selectedProjectCwd.value)
  );
}

function collapseOtherProjects() {
  const keepOpen = selectedProjectCwd.value || activeProjectCwd.value || groupedSessions.value[0]?.cwd || "";
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
  if (event.type === "user.input.requested") {
    return userInputQuestions(event).map((question) => question.question).join("\n");
  }
  if (event.type === "error") return String(event.payload.message || JSON.stringify(event.payload));
  return compactPayload(event.payload);
}

function isActiveWriterError(reason: unknown) {
  const message = reason instanceof Error ? reason.message : String(reason);
  return /already has an active writer/i.test(message);
}

async function releaseActiveThread() {
  const session = activeSession.value;
  if (!session) return;
  if (!activeSessionReadOnly.value) {
    await commandWithFallback(
      "threads.release",
      { id: session.id },
      () => releaseThread(session.id)
    );
  }
  if (activeSessionId.value !== session.id) return;
  eventSource.value?.close();
  eventSource.value = null;
  activeSessionId.value = "";
  events.value = [];
  queuedSubmissions.value = [];
  threadGoal.value = null;
  streamState.value = "idle";
}

function releaseActiveThreadOnPageHide() {
  const session = activeSession.value;
  if (!session || activeSessionReadOnly.value) return;
  p2p.notify("threads.release", { id: session.id });
  const endpoint = `/api/threads/${encodeURIComponent(session.id)}/release`;
  const body = new Blob(["{}"], { type: "application/json" });
  if (navigator.sendBeacon?.(endpoint, body)) return;
  void fetch(endpoint, { method: "POST", body, credentials: "same-origin", keepalive: true });
}

function eventHtml(event: RemoteEvent) {
  return markdownToHtml(eventText(event));
}

function isCollapsibleEvent(event: RemoteEvent) {
  return event.type === "tool.started" || event.type === "tool.output";
}

function eventPreview(event: RemoteEvent) {
  const text = eventText(event).replace(/\s+/g, " ").trim();
  if (!text) return eventLabel(event);
  if (text.length <= 140) return text;
  return `${text.slice(0, 140)}...`;
}

function markdownToHtml(markdown: string) {
  return DOMPurify.sanitize(markdownRenderer.render(markdown), {
    USE_PROFILES: { html: true }
  });
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
    "user.input.requested": "需要选择",
    "user.input.resolved": "选择结果",
    "turn.done": "完成",
    "context.usage": "上下文",
    "queue.changed": "队列",
    "goal.updated": "目标已更新",
    "goal.cleared": "目标已清除",
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
		"host-readonly": "仅查看历史",
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
    "waiting-user-input": "等待选择",
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

function transportLabel(status: typeof transportState.value) {
  return {
    idle: "未连接",
    connecting: "P2P 协商中",
    p2p: "P2P 直连",
    relay: "服务端中转",
    failed: "P2P 失败，未中转"
  }[status];
}
</script>

<template>
  <main v-if="!authChecked" class="auth-shell">
    <section class="auth-panel">
      <div class="brand-lockup">
        <img class="brand-mark" src="/codex-link.svg" alt="" />
        <span class="brand-copy"><strong>CODEX LINK</strong><small>REMOTE WORKSPACE</small></span>
      </div>
      <div class="auth-loading">
        <span class="loading-line" />
        <h1>正在连接</h1>
      </div>
    </section>
  </main>

  <main v-else-if="!auth.authenticated" class="auth-shell">
    <section class="auth-intro">
      <div class="brand-lockup">
        <img class="brand-mark" src="/codex-link.svg" alt="" />
        <span class="brand-copy"><strong>CODEX LINK</strong><small>REMOTE WORKSPACE</small></span>
      </div>
      <div class="auth-intro-copy">
        <p class="auth-kicker"><span class="signal-dot" /> SECURE CONTROL SURFACE</p>
        <h2>把 Codex 带到<br /><em>你的屏幕上。</em></h2>
        <p>连接本机客户端，继续处理远程工作区里的每一段会话。</p>
      </div>
      <div class="auth-intro-meta">
        <span><strong>01</strong><small>账号登录</small></span>
        <span><strong>02</strong><small>选择设备</small></span>
        <span><strong>03</strong><small>开始工作</small></span>
      </div>
    </section>
    <form class="auth-panel" @submit.prevent="submitLogin">
      <div class="auth-panel-heading">
        <span class="panel-index">01</span>
        <div>
          <p class="eyebrow">Account access</p>
          <h1>{{ registerMode ? "创建服务端账号" : "登录控制台" }}</h1>
        </div>
      </div>
      <p class="auth-note">网页与本机客户端使用同一个服务端账号，连接后会自动同步。</p>
      <label class="field-label">
        <span>用户名</span>
        <input v-model="authUsername" type="text" autocomplete="username" placeholder="输入用户名" autofocus />
      </label>
      <label class="field-label">
        <span>密码</span>
        <input v-model="loginPassword" type="password" autocomplete="current-password" placeholder="输入密码" />
      </label>
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
      <div class="topbar-title">
        <button v-if="activeDeviceId" class="icon-button compact" type="button" title="返回设备列表" @click="backToDevices">
          <ArrowLeft :size="18" />
        </button>
        <div class="brand-lockup app-brand">
          <img class="brand-mark" src="/codex-link.svg" alt="" />
          <span class="brand-copy"><strong>CODEX LINK</strong><small>REMOTE WORKSPACE</small></span>
        </div>
        <div class="topbar-heading">
          <p class="eyebrow">{{ systemManagementOpen ? "Administration" : activeDevice ? "Connected workspace" : "Device directory" }}</p>
          <h1>{{ systemManagementOpen ? "系统管理" : activeDevice ? `${activeDevice.name} 控制台` : "选择设备" }}</h1>
        </div>
      </div>
      <div class="top-actions">
        <button v-if="activeDeviceId" class="icon-button mobile-sidebar-toggle" type="button" title="打开对话列表" @click="sidebarOpen = true">
          <Menu :size="19" />
        </button>
        <button class="icon-button" type="button" title="刷新状态" @click="refresh">
          <RefreshCw :size="19" />
        </button>
        <div class="user-menu">
          <button class="user-menu-trigger" type="button" :aria-expanded="userMenuOpen" aria-haspopup="menu" @click="userMenuOpen = !userMenuOpen">
            <User :size="17" />
            <span>{{ auth.username || "用户" }}</span>
            <ChevronDown :size="16" :class="{ open: userMenuOpen }" />
          </button>
          <div v-if="userMenuOpen" class="user-menu-panel" role="menu">
            <button v-if="auth.isAdmin" class="user-menu-item" type="button" role="menuitem" @click="openSystemManagement">
              <ShieldCheck :size="16" />
              <span>系统管理</span>
            </button>
            <button class="user-menu-item" type="button" role="menuitem" @click="openModal('user')">
              <User :size="16" />
              <span>用户管理</span>
            </button>
            <button class="user-menu-item" type="button" role="menuitem" @click="openModal('tokens')">
              <KeyRound :size="16" />
              <span>秘钥管理</span>
            </button>
            <button class="user-menu-item user-menu-logout" type="button" role="menuitem" @click="signOut">
              <LogOut :size="16" />
              <span>退出登录</span>
            </button>
          </div>
        </div>
      </div>
    </header>

    <p v-if="error" class="error">{{ error }}</p>

    <div v-if="noticeMessage" class="modal-backdrop notice-backdrop" role="presentation" @click.self="closeNotice">
      <section class="modal notice-modal" role="alertdialog" aria-modal="true" aria-labelledby="notice-title" aria-describedby="notice-message">
        <header class="modal-header">
          <div>
            <p class="eyebrow">操作提示</p>
            <h2 id="notice-title">复制失败</h2>
          </div>
          <button class="icon-button compact" type="button" title="关闭提示" @click="closeNotice">
            <X :size="17" />
          </button>
        </header>
        <div class="notice-body">
          <div class="notice-message">
            <span class="notice-icon"><CircleAlert :size="20" /></span>
            <p id="notice-message">{{ noticeMessage }}</p>
          </div>
          <div class="notice-actions">
            <button class="primary" type="button" @click="closeNotice">知道了</button>
          </div>
        </div>
      </section>
    </div>

    <div v-if="modalView" class="modal-backdrop" role="presentation" @click.self="closeModal">
      <section class="modal" :class="{ 'port-mapping-modal': modalView === 'ports' }" role="dialog" aria-modal="true" :aria-labelledby="`${modalView}-modal-title`">
        <header class="modal-header">
          <div>
            <p class="eyebrow">{{ modalView === "user" ? "Account" : modalView === "tokens" ? "Access keys" : "P2P only" }}</p>
            <h2 :id="`${modalView}-modal-title`">{{ modalView === "user" ? "用户管理" : modalView === "tokens" ? "秘钥管理" : "P2P 端口映射" }}</h2>
          </div>
          <button class="icon-button compact" type="button" title="关闭弹框" @click="closeModal">
            <X :size="17" />
          </button>
        </header>

        <div v-if="modalView === 'user'" class="modal-body user-modal-body">
          <p class="modal-description">当前登录用户：{{ auth.username || "已登录" }}</p>
          <form class="password-form" @submit.prevent="savePassword">
            <input v-model="currentPassword" type="password" autocomplete="current-password" placeholder="当前密码" />
            <input v-model="newPassword" type="password" autocomplete="new-password" placeholder="新密码，至少 8 个字符" />
            <button class="primary icon-text" type="submit" :disabled="loading || !newPassword.trim()">
              <KeyRound :size="18" />
              <span>修改密码</span>
            </button>
          </form>
          <small v-if="passwordMessage" class="modal-message">{{ passwordMessage }}</small>
        </div>

        <div v-else-if="modalView === 'tokens'" class="modal-body">
          <div class="token-manager">
            <div class="token-heading">
              <div>
                <strong>客户端秘钥</strong>
                <p>客户端使用秘钥登录并绑定到设备。</p>
              </div>
              <span>{{ tokens.length }} 个</span>
            </div>
            <form class="token-create" @submit.prevent="createAccessToken">
              <input v-model="tokenName" type="text" placeholder="秘钥名称，例如办公室电脑" />
              <button class="primary icon-text" type="submit" :disabled="loading">
                <Plus :size="18" />
                <span>创建秘钥</span>
              </button>
            </form>
            <div v-if="tokens.length" class="token-list">
              <article v-for="token in tokens" :key="token.id" class="token-card">
                <div class="token-card-heading">
                  <strong>{{ token.name }}</strong>
                  <small>{{ token.prefix }}</small>
                </div>
                <div class="token-value-row">
                  <code>{{ token.token }}</code>
                  <button class="icon-button compact" type="button" title="复制秘钥" @click="copyToken(token.token)">
                    <Copy :size="16" />
                  </button>
                </div>
                <p>创建于 {{ formatShortDate(token.createdAt) }}<span v-if="token.lastUsedAt">，最后使用于 {{ formatShortDate(token.lastUsedAt) }}</span></p>
                <div class="token-card-actions">
                  <button class="icon-button icon-text" type="button" :disabled="loading" @click="rotateAccessToken(token)">
                    <RefreshCw :size="16" />
                    <span>刷新</span>
                  </button>
                  <button class="danger icon-text" type="button" :disabled="loading" @click="revokeAccessToken(token)">
                    <Trash2 :size="16" />
                    <span>删除</span>
                  </button>
                </div>
              </article>
            </div>
            <p v-else class="token-empty">还没有秘钥，请先创建一个供客户端登录。</p>
          </div>
          <small v-if="tokenMessage" class="modal-message">{{ tokenMessage }}</small>
        </div>

        <div v-else class="modal-body port-mapping-modal-body">
          <p class="modal-description">仅通过 WebRTC P2P 连接到你的设备。打洞失败、设备离线或连接中断时会直接拒绝访问，不会使用服务端中转。</p>
          <p v-if="portMappingMessage" class="port-mapping-message">{{ portMappingMessage }}</p>
          <p v-if="portMappingError" class="error">{{ portMappingError }}</p>

          <form class="port-mapping-form" @submit.prevent="savePortMapping">
            <div class="port-form-heading">
              <div>
                <strong>{{ portMappingEditingId ? '编辑端口映射' : '新建端口映射' }}</strong>
                <p>公开端口需要预先在服务端 Docker Compose 和防火墙中发布。</p>
              </div>
              <button v-if="portMappingEditingId" class="icon-button compact" type="button" title="取消编辑" @click="cancelPortMapping">
                <X :size="16" />
              </button>
            </div>
            <div class="port-form-grid">
              <div class="field-label">
                <span class="field-label-heading"><label for="port-mapping-name">映射名称</label><button class="field-help" type="button" aria-label="查看映射名称说明" :aria-expanded="portMappingHelp === 'name'" aria-controls="port-mapping-help-name" @click="togglePortMappingHelp('name')"><CircleHelp :size="14" /></button></span>
                <input id="port-mapping-name" v-model="portMappingForm.name" type="text" maxlength="120" placeholder="例如：远程调试" />
                <small v-if="portMappingHelp === 'name'" id="port-mapping-help-name" class="field-tip" role="tooltip">仅用于识别这条映射，例如“办公室 NAS”或“远程调试”。</small>
              </div>
              <div class="field-label">
                <span class="field-label-heading"><label for="port-mapping-device">目标设备</label><button class="field-help" type="button" aria-label="查看目标设备说明" :aria-expanded="portMappingHelp === 'device'" aria-controls="port-mapping-help-device" @click="togglePortMappingHelp('device')"><CircleHelp :size="14" /></button></span>
                <select id="port-mapping-device" v-model="portMappingForm.deviceId"><option value="" disabled>选择你的设备</option><option v-for="device in devices" :key="device.id" :value="device.id">{{ device.name }}{{ device.online ? '（在线）' : '（离线）' }}</option></select>
                <small v-if="portMappingHelp === 'device'" id="port-mapping-help-device" class="field-tip" role="tooltip">选择运行 Codex Link 客户端的设备。它负责从所在网络连接目标主机。</small>
              </div>
              <div class="field-label">
                <span class="field-label-heading"><label for="port-mapping-target-host">目标主机地址</label><button class="field-help" type="button" aria-label="查看目标主机地址说明" :aria-expanded="portMappingHelp === 'targetHost'" aria-controls="port-mapping-help-target-host" @click="togglePortMappingHelp('targetHost')"><CircleHelp :size="14" /></button></span>
                <input id="port-mapping-target-host" v-model="portMappingForm.targetHost" type="text" maxlength="255" placeholder="例如：192.168.1.20 或 127.0.0.1" />
                <small v-if="portMappingHelp === 'targetHost'" id="port-mapping-help-target-host" class="field-tip" role="tooltip">可填设备本机的 <code>127.0.0.1</code>，也可填设备能访问的局域网 IP 或主机名，例如 <code>192.168.1.20</code>、<code>nas.local</code>。</small>
              </div>
              <div class="field-label">
                <span class="field-label-heading"><label for="port-mapping-target-port">目标主机端口</label><button class="field-help" type="button" aria-label="查看目标主机端口说明" :aria-expanded="portMappingHelp === 'targetPort'" aria-controls="port-mapping-help-target-port" @click="togglePortMappingHelp('targetPort')"><CircleHelp :size="14" /></button></span>
                <input id="port-mapping-target-port" v-model.number="portMappingForm.targetPort" type="number" min="1" max="65535" />
                <small v-if="portMappingHelp === 'targetPort'" id="port-mapping-help-target-port" class="field-tip" role="tooltip">目标主机上实际提供服务的 TCP 端口，例如 NAS 管理端口、数据库端口或开发调试端口。</small>
              </div>
              <div class="field-label">
                <span class="field-label-heading"><label for="port-mapping-listen-port">服务端公开端口</label><button class="field-help" type="button" aria-label="查看服务端公开端口说明" :aria-expanded="portMappingHelp === 'listenPort'" aria-controls="port-mapping-help-listen-port" @click="togglePortMappingHelp('listenPort')"><CircleHelp :size="14" /></button></span>
                <input id="port-mapping-listen-port" v-model.number="portMappingForm.listenPort" type="number" min="1" max="65535" />
                <small v-if="portMappingHelp === 'listenPort'" id="port-mapping-help-listen-port" class="field-tip" role="tooltip">外部访问使用的 TCP 端口。需先在 Docker Compose 映射该端口，并在公网防火墙放行；不能使用网页服务端口。</small>
              </div>
              <div class="port-enabled">
                <label for="port-mapping-enabled"><input id="port-mapping-enabled" v-model="portMappingForm.enabled" type="checkbox" /><span>启用监听</span></label>
                <button class="field-help" type="button" aria-label="查看启用监听说明" :aria-expanded="portMappingHelp === 'enabled'" aria-controls="port-mapping-help-enabled" @click="togglePortMappingHelp('enabled')"><CircleHelp :size="14" /></button>
                <small v-if="portMappingHelp === 'enabled'" id="port-mapping-help-enabled" class="field-tip" role="tooltip">关闭后服务端立即停止监听公开端口；保存映射配置但不会接受外部连接。</small>
              </div>
            </div>
            <div class="port-form-actions">
              <button class="primary icon-text" type="submit" :disabled="portMappingsLoading"><Save :size="17" /><span>{{ portMappingEditingId ? '保存修改' : '创建映射' }}</span></button>
              <button v-if="portMappingEditingId" class="icon-button icon-text" type="button" @click="cancelPortMapping"><X :size="17" /><span>取消</span></button>
            </div>
          </form>

          <div v-if="portMappings.length" class="port-mapping-table" role="table" aria-label="端口映射列表">
            <div class="port-mapping-row port-mapping-header" role="row"><span>映射</span><span>目标</span><span>公开入口</span><span>状态</span><span>操作</span></div>
            <div v-for="mapping in portMappings" :key="mapping.id" class="port-mapping-row" role="row">
              <div class="port-mapping-name"><strong>{{ mapping.name }}</strong><small>{{ mapping.deviceName }}</small></div>
              <code>{{ mapping.targetHost }}:{{ mapping.targetPort }}</code>
              <code>{{ mapping.listenAddress || `0.0.0.0:${mapping.listenPort}` }}</code>
              <div class="port-mapping-status" :class="{ ready: mapping.listening && mapping.enabled, connected: mapping.p2pConnected }"><span class="status-dot" />{{ mapping.p2pConnected ? 'P2P 已连接' : mapping.listening ? '等待 P2P' : mapping.enabled ? '监听失败' : '已停用' }}<small v-if="mapping.lastError">{{ mapping.lastError }}</small></div>
              <div class="port-mapping-actions">
                <button class="icon-button compact" type="button" :title="mapping.enabled ? '停用映射' : '启用映射'" :disabled="portMappingsLoading" @click="togglePortMapping(mapping)"><Power :size="16" /></button>
                <button class="icon-button compact" type="button" title="编辑映射" :disabled="portMappingsLoading" @click="beginPortMapping(mapping)"><Pencil :size="16" /></button>
                <button class="danger compact" type="button" title="删除映射" :disabled="portMappingsLoading" @click="removePortMapping(mapping)"><Trash2 :size="16" /></button>
              </div>
            </div>
          </div>
          <div v-else-if="!portMappingsLoading" class="admin-empty"><Network :size="28" /><strong>还没有端口映射</strong><span>创建一个仅 P2P 的公开 TCP 入口。</span></div>
          <div v-else class="admin-empty"><RefreshCw :size="28" class="spin" /><strong>正在读取端口映射</strong></div>
        </div>
      </section>
    </div>

    <section v-if="systemManagementOpen" class="system-page">
      <aside class="system-sidebar">
        <div class="system-sidebar-heading">
          <p class="eyebrow">Administration</p>
          <strong>系统管理</strong>
        </div>
        <nav class="system-nav" aria-label="系统管理菜单">
          <button class="system-nav-item" :class="{ active: systemSection === 'users' }" type="button" @click="systemSection = 'users'; refreshAdminUsers()">
            <User :size="17" />
            <span>用户管理</span>
          </button>
        </nav>
        <button class="system-back" type="button" @click="closeSystemManagement">
          <ArrowLeft :size="17" />
          <span>返回控制台</span>
        </button>
      </aside>

      <section class="system-content">
        <header class="system-content-heading">
          <div>
            <p class="eyebrow">User Directory</p>
            <h2>用户管理</h2>
            <p>管理账号角色和访问范围。</p>
          </div>
          <button class="icon-button" type="button" title="刷新用户列表" :disabled="adminUsersLoading" @click="refreshAdminUsers">
            <RefreshCw :size="19" />
          </button>
        </header>

        <p v-if="adminError" class="error">{{ adminError }}</p>
        <p v-if="adminMessage" class="admin-message">{{ adminMessage }}</p>

        <div v-if="adminUsers.length" class="admin-user-table" role="table" aria-label="用户列表">
          <div class="admin-user-row admin-user-header" role="row">
            <span>用户</span>
            <span>角色</span>
            <span>注册时间</span>
            <span>操作</span>
          </div>
          <div v-for="adminUser in adminUsers" :key="adminUser.id" class="admin-user-row" role="row">
            <div class="admin-user-identity">
              <span class="admin-user-avatar"><User :size="16" /></span>
              <strong>{{ adminUser.username }}</strong>
              <small v-if="adminUser.id === auth.userId">当前账号</small>
            </div>
            <span class="admin-role" :class="{ admin: adminUser.isAdmin }">
              <ShieldCheck v-if="adminUser.isAdmin" :size="15" />
              <User v-else :size="15" />
              {{ adminUser.isAdmin ? "管理员" : "普通用户" }}
            </span>
            <time class="admin-created-at">{{ formatShortDate(adminUser.createdAt) }}</time>
            <div class="admin-user-actions">
              <button
                class="icon-button icon-text"
                type="button"
                :disabled="adminUsersLoading || adminUser.id === auth.userId"
                @click="toggleAdminRole(adminUser)"
              >
                <ShieldCheck :size="16" />
                <span>{{ adminUser.isAdmin ? "取消管理员" : "设为管理员" }}</span>
              </button>
              <button
                class="danger icon-text"
                type="button"
                :disabled="adminUsersLoading || adminUser.id === auth.userId"
                @click="removeAdminUser(adminUser)"
              >
                <Trash2 :size="16" />
                <span>删除</span>
              </button>
            </div>
          </div>
        </div>
        <div v-else-if="!adminUsersLoading" class="admin-empty">
          <Users :size="28" />
          <strong>暂无用户</strong>
        </div>
        <div v-else class="admin-empty">
          <RefreshCw :size="28" class="spin" />
          <strong>正在读取用户</strong>
        </div>

      </section>
    </section>

    <template v-else>
    <section v-if="!activeDeviceId" class="device-page">
      <div class="device-page-heading">
        <div>
          <p class="eyebrow">Remote Devices</p>
          <h2>选择要控制的电脑</h2>
          <p>在线设备可以进入 Codex 控制台，离线设备需要先启动客户端。</p>
        </div>
        <button class="icon-button" type="button" title="刷新设备列表" @click="refresh">
          <RefreshCw :size="19" />
        </button>
      </div>
      <div v-if="devices.length" class="device-list">
        <div
          v-for="device in devices"
          :key="device.id"
          class="device-card"
          :class="{ offline: !device.online }"
        >
          <button class="device-card-select" type="button" :disabled="!device.online" @click="selectDevice(device)">
            <span class="device-icon"><Cpu :size="22" /></span>
            <span class="device-card-main">
              <strong>{{ device.name }}</strong>
              <small>{{ device.online ? "在线，可进入控制台" : "离线，请先启动客户端" }}</small>
              <small v-if="device.tokenName">Token：{{ device.tokenName }}（{{ device.tokenPrefix }}）</small>
            </span>
            <span class="device-status" :class="{ online: device.online }">{{ device.online ? "在线" : "离线" }}</span>
            <ChevronRight :size="19" />
          </button>
          <button v-if="!device.online" class="icon-button compact device-remove" type="button" title="删除离线设备" :disabled="loading" @click="removeDevice(device)">
            <Trash2 :size="16" />
          </button>
        </div>
      </div>
      <div v-else class="device-empty">
        <Cpu :size="30" />
        <strong>还没有客户端设备</strong>
        <p>先在安装 Codex 的电脑上使用 Token 登录并启动客户端。</p>
      </div>
    </section>

    <div v-else class="workspace" :class="{ 'sidebar-open': sidebarOpen }">
      <button v-if="sidebarOpen" class="sidebar-backdrop" type="button" aria-label="关闭对话列表" @click="sidebarOpen = false" />
      <aside class="sidebar">
        <div class="sidebar-heading">
          <div class="sidebar-heading-label">
            <strong>工作区</strong>
            <small>{{ orderedSessionCount }} 个对话</small>
          </div>
          <div class="sidebar-actions">
            <button class="icon-button compact" type="button" :title="theme === 'dark' ? '切换浅色主题' : '切换黑色主题'" @click="toggleTheme">
              <Sun v-if="theme === 'dark'" :size="16" />
              <Moon v-else :size="16" />
            </button>
            <button class="icon-button compact" type="button" title="刷新对话列表" @click="refresh">
              <RefreshCw :size="16" />
            </button>
            <button class="icon-button compact sidebar-close" type="button" title="关闭对话列表" @click="sidebarOpen = false">
              <X :size="16" />
            </button>
          </div>
        </div>
        <div class="sidebar-command-row">
          <button class="primary icon-text sidebar-new-session" type="button" aria-label="新对话" title="新对话" :disabled="loading" @click="startSession">
            <MessageSquarePlus :size="17" />
            <span>新对话</span>
          </button>
          <button class="icon-button compact sidebar-port-mapping" type="button" title="管理 P2P 端口映射" aria-label="管理 P2P 端口映射" @click="openPortMappingModal">
            <Network :size="17" />
          </button>
        </div>
        <div class="project-tools">
          <button type="button" @click="expandAllProjects">展开全部</button>
          <button type="button" @click="collapseOtherProjects">折叠其它</button>
          <span>{{ visibleSessionCount }} / {{ sessions.length }}</span>
        </div>
        <div class="session-list" aria-label="Sessions">
          <section class="sidebar-section">
            <div class="sidebar-section-heading">
              <span><Folder :size="15" />项目</span>
              <small>{{ groupedSessions.length }}</small>
            </div>
            <div class="project-list">
              <section
                v-for="group in visibleProjectGroups"
                :key="group.cwd"
                class="session-project-group"
                :class="{ 'drag-over': dragOverKey === dragKey('project', group.cwd) }"
                @dragover="trackDragOver($event, 'project', group.cwd)"
                @drop.prevent="finishDrop('project', group.cwd)"
              >
                <div class="project-heading-row">
                  <button
                    class="project-heading"
                    type="button"
                    :title="group.cwd"
                    :aria-expanded="!isProjectCollapsed(group.cwd)"
                    :class="{ selected: selectedProjectCwd === group.cwd }"
                    @click="selectProject(group.cwd)"
                  >
                    <ChevronRight v-if="isProjectCollapsed(group.cwd)" :size="16" />
                    <ChevronDown v-else :size="16" />
                    <span>{{ group.label }}</span>
                  </button>
                  <span
                    class="drag-handle project-drag-handle"
                    draggable="true"
                    title="拖动项目排序"
                    @dragstart="beginDrag($event, 'project', group.cwd)"
                    @dragend="endDrag"
                  ><GripVertical :size="15" /></span>
                </div>
                <article
                  v-for="session in visibleProjectSessions(group)"
                  v-show="!isProjectCollapsed(group.cwd)"
                  :key="session.id"
                  :data-session-id="session.id"
                  class="session-pill"
                  :class="{ active: session.id === activeSessionId }"
                >
                  <button class="session-open" type="button" :title="session.title" :disabled="loading" @click="openThread(session)">
                    <span class="session-title">{{ session.title }}</span>
                  </button>
                  <button class="delete-thread" type="button" title="删除对话" :disabled="session.mode === 'host-readonly'" @click="removeThread(session)">
                    <Trash2 :size="15" />
                  </button>
                </article>
                <button
                  v-if="!isProjectCollapsed(group.cwd) && hasMoreProjectSessions(group)"
                  class="sidebar-load-more"
                  type="button"
                  @click="showMoreProjectSessions(group.cwd)"
                >加载更多对话</button>
              </section>
            </div>
            <div v-if="!groupedSessions.length" class="sidebar-empty">还没有项目对话</div>
            <button v-if="hasMoreProjects" class="sidebar-load-more" type="button" @click="showMoreProjects">加载更多项目</button>
          </section>

          <section class="sidebar-section recent-section">
            <div class="sidebar-section-heading">
              <span><Clock3 :size="15" />最近对话</span>
              <small>{{ recentSessions.length }}</small>
            </div>
            <div class="recent-list">
              <article
                v-for="session in visibleRecentSessions"
                :key="session.id"
                :data-session-id="session.id"
                class="session-pill recent-session-pill"
                :class="{ active: session.id === activeSessionId, 'drag-over': dragOverKey === dragKey('recent', session.id) }"
                draggable="true"
                @dragstart="beginDrag($event, 'recent', session.id)"
                @dragover="trackDragOver($event, 'recent', session.id)"
                @drop.prevent="finishDrop('recent', session.id)"
                @dragend="endDrag"
              >
                <button class="session-open" type="button" :title="session.title" :disabled="loading" @click="openThread(session)">
                  <span class="session-title">{{ session.title }}</span>
                </button>
                <button class="delete-thread" type="button" title="删除对话" :disabled="session.mode === 'host-readonly'" @click="removeThread(session)">
                  <Trash2 :size="15" />
                </button>
              </article>
            </div>
            <div v-if="!recentSessions.length" class="sidebar-empty">暂无最近对话</div>
            <button v-if="hasMoreRecentSessions" class="sidebar-load-more" type="button" @click="showMoreRecentSessions">加载更多对话</button>
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
            <span>{{ transportLabel(transportState) }}</span>
          </div>
          <div class="status-item current-device">
            <Cpu :size="16" />
            <span>{{ activeDevice?.name || "当前设备" }}</span>
          </div>
          <div class="status-item context-usage" :title="contextUsageTitle(activeSession)">
            <Gauge :size="16" />
            <span>{{ contextUsageLabel(activeSession) }}</span>
          </div>
        </section>

        <section v-if="activeSession" class="conversation">
          <article class="event-block session-summary">
            <div>
              <strong>{{ activeSession.title }}</strong>
              <p>{{ activeSession.note || activeSession.cwd || "Host workspace" }}</p>
            </div>
            <span>{{ statusLabel(activeSession.status) }}</span>
          </article>

          <section ref="transcriptEl" class="transcript" tabindex="0" @wheel.passive="armHistoryLoad" @touchstart.passive="armHistoryLoad" @pointerdown.passive="armHistoryLoad" @scroll.passive="onTranscriptScroll">
            <div v-if="hasEarlierEvents" ref="historySentinelEl" class="history-load-sentinel" :aria-busy="historyLoading" aria-live="polite" />
            <template v-for="event in visibleEvents" :key="event.id">
              <details v-if="isCollapsibleEvent(event)" class="event-block timeline-event collapsible-event" :class="eventClass(event)">
                <summary class="event-meta tool-event-summary">
                  <time>{{ formatTime(event.ts) }}</time>
                  <span>{{ eventLabel(event) }}</span>
                  <small>{{ eventPreview(event) }}</small>
                </summary>
                <div class="markdown-body" v-html="eventHtml(event)" />
              </details>
              <article
                v-else
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
        <div v-if="event.type === 'user.input.requested' && pendingUserInputs.some((item) => userInputRequestID(item) === userInputRequestID(event))" class="user-input-request">
          <section v-for="question in userInputQuestions(event)" :key="question.id" class="user-input-question">
            <strong>{{ question.header }}</strong>
            <p>{{ question.question }}</p>
            <div v-if="question.options?.length" class="user-input-options">
              <button
                v-for="option in question.options"
                :key="option.label"
                type="button"
                class="user-input-option"
                :class="{ selected: userInputSelection(event, question.id) === option.label }"
                @click="chooseUserInput(event, question.id, option.label)"
              >
                <span>{{ option.label }}</span>
                <small>{{ option.description }}</small>
              </button>
            </div>
            <input
              v-if="question.isOther || !question.options?.length"
              :value="userInputSelection(event, question.id)"
              type="text"
              placeholder="输入自定义回答"
              @input="updateUserInput(event, question.id, $event)"
            />
          </section>
          <button class="primary icon-text" type="button" :disabled="!canSubmitUserInput(event)" @click="submitUserInput(event)">
            <Check :size="17" />
            <span>提交选择</span>
          </button>
        </div>
              </article>
            </template>
          </section>
        </section>

        <section v-else class="empty-state">
          <Play :size="30" />
          <p>选择左侧历史对话，或启动一个新的 Codex 会话。</p>
        </section>

        <form
          class="composer"
          :class="{ 'drop-active': composerDropActive }"
          @submit.prevent="submitMessage"
          @paste="handlePaste"
          @dragover="handleComposerDragOver"
          @dragleave="handleComposerDragLeave"
          @drop="handleComposerDrop"
        >
          <section v-if="!activeSessionReadOnly && queuedSubmissions.length" class="queue-panel" aria-label="待发送消息">
            <div class="queue-panel-heading">
              <span><Clock3 :size="15" />待发送 {{ queuedSubmissions.length }}</span>
              <small v-if="queueLoading">正在同步</small>
            </div>
            <article v-for="(item, index) in queuedSubmissions" :key="item.id" class="queue-item">
              <template v-if="editingQueueID === item.id">
                <textarea v-model="queuedMessageDraft" rows="2" aria-label="编辑排队消息" />
                <div class="queue-item-actions">
                  <button type="button" class="primary icon-text" :disabled="queueActionID === item.id" @click="saveQueueEdit"><Save :size="14" /><span>保存</span></button>
                  <button type="button" class="ghost icon-text" @click="cancelEditQueue"><X :size="14" /><span>取消</span></button>
                </div>
              </template>
              <template v-else>
                <div class="queue-item-main">
                  <span class="queue-item-index">{{ index + 1 }}</span>
                  <span class="queue-item-text">{{ queueText(item) || "附件" }}</span>
                  <span v-if="queueAttachments(item).length" class="queue-item-attachment">{{ queueAttachments(item).length }} 个附件</span>
                </div>
                <div class="queue-item-actions">
                  <button type="button" class="queue-direction icon-text" title="调整方向并立即发送" :disabled="queueActionID === item.id" @click="promoteQueued(item)"><ArrowUp :size="14" /><span>调整方向</span></button>
                  <button type="button" class="queue-icon" title="上移" :disabled="index === 0 || queueActionID === item.id" @click="moveQueue(item, -1)"><ArrowUp :size="15" /></button>
                  <button type="button" class="queue-icon" title="下移" :disabled="index === queuedSubmissions.length - 1 || queueActionID === item.id" @click="moveQueue(item, 1)"><ArrowDown :size="15" /></button>
                  <button type="button" class="queue-icon" title="编辑排队消息" @click="editQueue(item)"><Pencil :size="15" /></button>
                  <button type="button" class="queue-icon queue-remove" title="删除排队消息" :disabled="queueActionID === item.id" @click="removeQueue(item)"><Trash2 :size="15" /></button>
                </div>
              </template>
            </article>
          </section>
          <div v-if="composerDropActive" class="composer-drop-hint" aria-live="polite">
            <ImagePlus :size="18" />
            <span>释放文件以上传</span>
          </div>
          <div v-if="attachments.length" class="attachment-strip">
            <figure v-for="(attachment, index) in attachments" :key="attachment.id || attachment.path || index">
              <img v-if="attachment.url || attachment.previewUrl" :src="attachment.url || attachment.previewUrl" :alt="attachment.name || '图片附件'" />
              <figcaption>{{ attachment.name || "附件" }}</figcaption>
              <button type="button" title="移除附件" @click="removeAttachment(index)">
                <X :size="14" />
              </button>
            </figure>
          </div>
          <div v-if="slashMenuVisible" class="slash-menu" role="listbox" aria-label="技能和插件候选">
            <div class="slash-menu-heading"><span>斜杠命令</span><small>技能与插件</small></div>
            <button
              v-for="(skill, index) in slashSuggestions"
              :key="`${skill.kind}:${skill.command}`"
              class="slash-option"
              :class="{ selected: index === slashSelectionIndex }"
              type="button"
              role="option"
              :aria-selected="index === slashSelectionIndex"
              @mousedown.prevent="insertSlashSuggestion(skill)"
            >
              <span class="slash-option-command">{{ skill.command }}</span>
              <span class="slash-option-description">{{ skill.description || (skill.kind === "plugin" ? "插件" : "技能") }}</span>
              <small>{{ skill.kind === "plugin" ? "插件" : "技能" }}</small>
            </button>
          </div>
          <div class="composer-entry">
            <textarea
              ref="composerTextarea"
              v-model="draft"
              rows="3"
              :disabled="!canModifyActiveSession"
              :placeholder="activeSessionReadOnly ? '该对话正在被本机 Codex 使用，仅可查看历史' : '随心输入'"
              @input="handleComposerInput"
              @keydown="handleComposerKeydown"
            />
            <div class="composer-toolbar">
              <div class="composer-actions">
                <button class="icon-button composer-plus" type="button" title="添加文件或目标" :disabled="!canModifyActiveSession" @click="composerMenuOpen = !composerMenuOpen; modelMenuOpen = false"><Plus :size="19" /></button>
                <label class="composer-permission"><ShieldCheck :size="15" /><select v-model="settings.approvalMode" aria-label="权限模式" @change="saveSettings({ approvalMode: settings.approvalMode })"><option value="on-request">请求批准</option><option value="on-failure">按需批准</option><option value="never">完全访问</option></select></label>
                <label class="composer-mode" title="工作模式"><Clock3 :size="15" /><select :value="settings.workMode" aria-label="工作模式" :disabled="!canModifyActiveSession" @change="selectWorkMode"><option value="edit">编辑</option><option value="plan">计划</option></select></label>
              </div>
              <div class="composer-controls">
                <button
                  class="composer-model-trigger"
                  type="button"
                  :title="`模型与推理强度：${selectedModelLabel}`"
                  aria-haspopup="dialog"
                  :aria-expanded="modelMenuOpen"
                  :disabled="!models.length || !canModifyActiveSession"
                  @click="toggleModelMenu"
                >
                  <Cpu :size="15" />
                  <span>{{ selectedModelLabel }}</span>
                  <ChevronDown :size="14" :class="{ open: modelMenuOpen }" />
                </button>
                <button class="primary send composer-send" :class="{ stop: showStopButton }" type="button" :title="showStopButton ? '停止' : activeSessionRunning ? '加入队列' : '发送'" :disabled="!canModifyActiveSession || (!showStopButton && !composerHasInput) || composerBusy" @click="showStopButton ? cancel() : submitMessage()">
                  <CircleStop v-if="showStopButton" :size="18" />
                  <Send v-else :size="18" />
                </button>
              </div>
            </div>
          </div>
          <div v-if="composerMenuOpen && !activeSessionReadOnly" class="composer-menu">
            <div class="composer-menu-label">添加</div>
            <button type="button" class="composer-menu-item" @click="openFilePicker"><Paperclip :size="16" /><span>文件和文件夹</span></button>
            <button type="button" class="composer-menu-item" :class="{ active: threadGoal }" @click="openGoalEditor"><Target :size="16" /><span>目标</span><small>{{ threadGoal ? "已设置" : "设置要持续追踪的目标" }}</small></button>
          </div>
          <div v-if="modelMenuOpen && !activeSessionReadOnly" class="composer-model-menu" @keydown.esc="modelMenuOpen = false">
            <label class="composer-model-menu-field">
              <span>选择模型</span>
              <select :value="settings.model" aria-label="选择模型" :disabled="!models.length || !canModifyActiveSession" @change="selectModel">
                <option value="">默认模型</option>
                <option v-for="model in models" :key="model.id" :value="model.model" :title="model.description">
                  {{ model.displayName || model.model }}
                </option>
              </select>
            </label>
            <label class="composer-model-menu-field">
              <span>推理强度</span>
              <select :value="settings.reasoningEffort" aria-label="推理强度" :disabled="!supportedReasoningEfforts.length || !canModifyActiveSession" @change="selectReasoningEffort">
                <option value="">默认推理</option>
                <option v-for="effort in supportedReasoningEfforts" :key="effort.reasoningEffort" :value="effort.reasoningEffort" :title="effort.description">
                  {{ reasoningEffortLabel(effort.reasoningEffort) }}
                </option>
              </select>
            </label>
          </div>
          <div v-if="goalEditorOpen && !activeSessionReadOnly" class="goal-editor">
            <div class="goal-editor-heading"><Target :size="16" /><strong>目标</strong><button type="button" class="queue-icon" title="关闭" @click="goalEditorOpen = false"><X :size="15" /></button></div>
            <textarea v-model="goalDraft" rows="3" placeholder="告诉 Codex 这项工作的目标" />
            <div class="goal-editor-actions"><button type="button" class="primary icon-text" @click="saveGoalDraft"><Save :size="14" /><span>保存目标</span></button><button v-if="threadGoal" type="button" class="ghost icon-text" @click="removeGoal"><Trash2 :size="14" /><span>清除</span></button></div>
          </div>
          <input ref="fileInput" class="hidden-file-input" type="file" multiple @change="handleFilePicked" />
        </form>
      </section>
    </div>
    </template>
  </main>
</template>
