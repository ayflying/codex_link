<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from "vue";
import {
  ArrowLeft,
  Check,
  ChevronDown,
  ChevronRight,
  CircleStop,
  CircleAlert,
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
  Pencil,
  Plus,
  Play,
  Power,
  RefreshCw,
  Send,
  Save,
  ShieldCheck,
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
  getPortMappings,
  getSettings,
  getThreads,
  getTokens,
  login,
  register,
  refreshToken,
  logout,
  deleteAdminUser,
  resumeThread,
  sendApproval,
  sendMessage,
  createPortMapping,
  deletePortMapping,
  updatePortMapping,
  updateAdminUser,
  updateSettings,
  uploadImage,
  type AppSettings,
  type AdminUser,
  type Attachment,
  type AccessToken,
  type AuthStatus,
  type ModelOption,
  type PortMapping,
  type PortMappingDraft,
  type RemoteDevice,
  type RemoteEvent,
  type SessionRecord,
  type TokenUsage
} from "./api";
import { P2PRemoteError, P2PTransport } from "./p2p";

const auth = ref<AuthStatus>({ authenticated: false, registrationOpen: true });
const authChecked = ref(false);
const authUsername = ref("");
const loginPassword = ref("");
const registerMode = ref(false);
const currentPassword = ref("");
const newPassword = ref("");
const userMenuOpen = ref(false);
const modalView = ref<"user" | "tokens" | null>(null);
const systemManagementOpen = ref(false);
const systemSection = ref<"users" | "ports">("users");
const adminUsers = ref<AdminUser[]>([]);
const adminUsersLoading = ref(false);
const adminError = ref("");
const adminMessage = ref("");
const portMappings = ref<PortMapping[]>([]);
const portMappingsLoading = ref(false);
const portMappingEditingId = ref("");
const portMappingForm = ref<PortMappingDraft>({ deviceId: "", name: "", targetHost: "127.0.0.1", targetPort: 3000, listenPort: 19022, protocol: "tcp", enabled: true });
const passwordMessage = ref("");
const tokens = ref<AccessToken[]>([]);
const tokenName = ref("");
const tokenMessage = ref("");
const noticeMessage = ref("");
const sessions = ref<SessionRecord[]>([]);
const activeSessionId = ref("");
const events = ref<RemoteEvent[]>([]);
const draft = ref("");
const firstPrompt = ref("");
const attachments = ref<Attachment[]>([]);
const settings = ref<AppSettings>({ approvalMode: "on-request", workMode: "edit", model: "" });
const models = ref<ModelOption[]>([]);
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
const renderLimit = ref(160);
const forceScrollToBottom = ref(false);
const sidebarOpen = ref(false);
const collapsedProjects = ref<Set<string>>(new Set());
const projectOrder = ref<string[]>([]);
const recentOrder = ref<string[]>([]);
const theme = ref<"light" | "dark">("dark");
const composerDropActive = ref(false);
const dragState = ref<{ kind: "project" | "recent"; id: string } | null>(null);
const dragOverKey = ref("");
const pendingRemoteEvents = new Map<string, RemoteEvent>();

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
const groupedSessions = computed(() => {
  const groups = new Map<string, { cwd: string; label: string; sessions: SessionRecord[] }>();
  for (const session of sessions.value) {
    if (!session.cwd) continue;
    const cwd = session.cwd;
    if (!groups.has(cwd)) groups.set(cwd, { cwd, label: projectLabel(cwd), sessions: [] });
    groups.get(cwd)!.sessions.push(session);
  }
  return applyStoredOrder([...groups.values()], projectOrder.value, (group) => group.cwd);
});
const recentSessions = computed(() => {
  const recent = sessions.value.filter((session) => !session.cwd);
  return applyStoredOrder(recent, recentOrder.value, (session) => session.id);
});
const orderedSessionCount = computed(() => groupedSessions.value.reduce((total, group) => total + group.sessions.length, 0) + recentSessions.value.length);
const activeProjectCwd = computed(() => activeSession.value?.cwd || "");
const visibleSessionCount = computed(() =>
  groupedSessions.value.reduce((total, group) => total + (isProjectCollapsed(group.cwd) ? 0 : group.sessions.length), 0) + recentSessions.value.length
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
const visibleEvents = computed(() => displayEvents.value.slice(-renderLimit.value));

onMounted(async () => {
  loadSidebarPreferences();
  applyTheme();
  await checkAuth();
  if (auth.value.authenticated) await refresh();
});

watch(theme, () => {
  applyTheme();
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
      backToDevices();
      return;
    }
    const direct = await connectP2P(activeDeviceId.value);
    const [sessionResult, settingsResult, modelResult] = await Promise.all([
      direct
        ? p2p.request<{ sessions: SessionRecord[] }>("threads.list", {})
        : getThreads(activeDeviceId.value),
      direct
        ? p2p.request<AppSettings>("settings.get", {})
        : getSettings(activeDeviceId.value),
      (direct
        ? p2p.request<{ models: ModelOption[] }>("models.list", {})
        : getModels(activeDeviceId.value)).catch(() => ({ models: [] }))
    ]);
    sessions.value = sessionResult.sessions;
    settings.value = settingsResult;
    models.value = modelResult.models;
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
}

function closeNotice() {
  noticeMessage.value = "";
}

async function openSystemManagement() {
  userMenuOpen.value = false;
  modalView.value = null;
  systemManagementOpen.value = true;
  systemSection.value = "users";
  await Promise.all([refreshAdminUsers(), refreshPortMappings()]);
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
  if (!auth.value.isAdmin) return;
  portMappingsLoading.value = true;
  adminError.value = "";
  try {
    portMappings.value = (await getPortMappings()).mappings;
  } catch (reason) {
    adminError.value = reason instanceof Error ? reason.message : String(reason);
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
  adminError.value = "";
  adminMessage.value = "";
}

function cancelPortMapping() {
  portMappingEditingId.value = "";
  portMappingForm.value = emptyPortMappingDraft();
}

async function savePortMapping() {
  const form = portMappingForm.value;
  if (!form.deviceId) {
    adminError.value = "请选择目标设备";
    return;
  }
  if (!form.name.trim()) {
    adminError.value = "请输入映射名称";
    return;
  }
  if (form.targetPort < 1 || form.targetPort > 65535 || form.listenPort < 1 || form.listenPort > 65535) {
    adminError.value = "端口必须是 1 到 65535";
    return;
  }
  portMappingsLoading.value = true;
  adminError.value = "";
  adminMessage.value = "";
  try {
    if (portMappingEditingId.value) {
      await updatePortMapping(portMappingEditingId.value, { ...form });
      adminMessage.value = "端口映射已更新";
    } else {
      await createPortMapping({ ...form });
      adminMessage.value = "端口映射已创建";
    }
    cancelPortMapping();
    await refreshPortMappings();
  } catch (reason) {
    adminError.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    portMappingsLoading.value = false;
  }
}

async function removePortMapping(mapping: PortMapping) {
  if (!window.confirm(`确认删除端口映射“${mapping.name}”吗？公开端口会立即停止监听。`)) return;
  portMappingsLoading.value = true;
  adminError.value = "";
  try {
    await deletePortMapping(mapping.id);
    adminMessage.value = `映射 ${mapping.name} 已删除`;
    await refreshPortMappings();
  } catch (reason) {
    adminError.value = reason instanceof Error ? reason.message : String(reason);
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
  activeDeviceId.value = device.id;
  activeSessionId.value = "";
  events.value = [];
  sidebarOpen.value = false;
  await refresh();
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

function backToDevices() {
  p2p.close(false);
  eventSource.value?.close();
  eventSource.value = null;
  activeDeviceId.value = "";
  activeSessionId.value = "";
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
    const prompt = firstPrompt.value.trim() || undefined;
    const { session } = await commandWithFallback(
      "sessions.create",
      { prompt },
      () => createSession(prompt, activeDeviceId.value)
    );
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
  if (session.id === activeSessionId.value || loading.value || openingThreadId.value) return;
  openingThreadId.value = session.id;
  loading.value = true;
  error.value = "";
  try {
    const { session: resumed } = await commandWithFallback(
      "threads.resume",
      { id: session.id },
      () => resumeThread(session.id)
    );
    upsertSession(resumed);
    forceScrollToBottom.value = true;
    activeSessionId.value = resumed.id;
    sidebarOpen.value = false;
    await nextTick();
    await scrollTranscriptToBottom();
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  } finally {
    openingThreadId.value = "";
    loading.value = false;
  }
}

async function removeThread(session: SessionRecord) {
  if (!window.confirm(`确认删除“${session.title}”吗？该对话会同步从 Codex 列表中归档。`)) return;
  loading.value = true;
  error.value = "";
  try {
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
  if (!activeSession.value || (!draft.value.trim() && attachments.value.length === 0)) return;
  loading.value = true;
  error.value = "";
  try {
    const messageAttachments = attachments.value;
    if (p2p.isConnected() && messageAttachments.every((attachment) => attachment.transport === "p2p" && Boolean(attachment.path))) {
      const directAttachments = messageAttachments.map(({ dataUrl: _dataUrl, previewUrl: _previewUrl, transport: _transport, ...attachment }) => attachment);
      await commandWithFallback(
        "sessions.message",
        { id: activeSession.value.id, text: draft.value.trim(), attachments: directAttachments },
        async () => sendMessage(activeSession.value!.id, draft.value.trim(), await relayAttachments(messageAttachments))
      );
    } else {
      await sendMessage(activeSession.value.id, draft.value.trim(), await relayAttachments(messageAttachments));
    }
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
    settings.value = await commandWithFallback(
      "settings.update",
      settings.value,
      () => updateSettings(settings.value, activeDeviceId.value)
    );
  } catch (reason) {
    settings.value = previous;
    error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

function handleComposerKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
  event.preventDefault();
  void submitMessage();
}

function selectModel(event: Event) {
  const model = (event.target as HTMLSelectElement).value;
  void saveSettings({ model });
}

async function addImageFiles(files: File[]) {
  try {
    for (const file of files) await addImageFile(file);
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : String(reason);
  }
}

async function handlePaste(event: ClipboardEvent) {
  const items = Array.from(event.clipboardData?.items || []);
  const imageItems = items.filter((item) => item.type.startsWith("image/"));
  if (imageItems.length === 0) return;
  event.preventDefault();
  await addImageFiles(imageItems.map((item) => item.getAsFile()).filter((file): file is File => Boolean(file)));
}

async function handleFilePicked(event: Event) {
  const input = event.target as HTMLInputElement;
  const files = Array.from(input.files || []);
  await addImageFiles(files);
  input.value = "";
}

function handleComposerDragOver(event: DragEvent) {
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
  event.preventDefault();
  composerDropActive.value = false;
  const files = Array.from(event.dataTransfer?.files || []).filter((file) => file.type.startsWith("image/"));
  await addImageFiles(files);
}

async function addImageFile(file: File) {
  if (!file.type.startsWith("image/")) return;
  const dataUrl = await fileToDataURL(file);
  const name = file.name || `image-${Date.now()}.png`;
  if (!p2p.isConnected() && p2p.isP2POnly()) {
    throw new Error("P2P 连接未建立，已禁止服务端图片中转");
  }
  if (p2p.isConnected()) {
    try {
      const attachment = await p2p.uploadAttachment(name, file.type, dataUrl);
      attachments.value.push({ ...attachment, dataUrl, previewUrl: dataUrl, transport: "p2p" });
      return;
    } catch {
      p2p.close(true);
      if (p2p.isP2POnly()) throw new Error("P2P 图片传输失败，已禁止服务端中转");
    }
  }
  const { attachment } = await uploadImage(name, file.type, dataUrl);
  attachments.value.push({ ...attachment, transport: "relay" });
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
    const uploaded = await uploadImage(attachment.name || `image-${Date.now()}.png`, attachment.mimeType || "image/png", attachment.dataUrl);
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

async function cancel() {
  if (!activeSession.value) return;
  await commandWithFallback(
    "sessions.cancel",
    { id: activeSession.value.id },
    () => cancelTurn(activeSession.value!.id)
  );
}

function connectEvents(sessionId: string, reset = true) {
  eventSource.value?.close();
  if (reset) {
    events.value = [];
    renderLimit.value = 160;
    forceScrollToBottom.value = true;
  }
  if (!sessionId) return;

  drainPendingEvents(sessionId);

  if (p2p.isConnected()) {
    streamState.value = "connected";
    return;
  }

  if (p2p.isP2POnly()) {
    streamState.value = "idle";
    transportState.value = "failed";
    return;
  }

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
    "context.usage",
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
  collapsedProjects.value = new Set(groupedSessions.value.map((group) => group.cwd));
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
    "context.usage": "上下文",
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
      <section class="modal" role="dialog" aria-modal="true" :aria-labelledby="`${modalView}-modal-title`">
        <header class="modal-header">
          <div>
            <p class="eyebrow">{{ modalView === "user" ? "Account" : "Access keys" }}</p>
            <h2 :id="`${modalView}-modal-title`">{{ modalView === "user" ? "用户管理" : "秘钥管理" }}</h2>
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

        <div v-else class="modal-body">
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
          <button class="system-nav-item" :class="{ active: systemSection === 'ports' }" type="button" @click="systemSection = 'ports'; refreshPortMappings()">
            <Network :size="17" />
            <span>端口映射</span>
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
            <p class="eyebrow">{{ systemSection === 'users' ? 'User Directory' : 'P2P Port Forwarding' }}</p>
            <h2>{{ systemSection === 'users' ? '用户管理' : '端口映射' }}</h2>
            <p>{{ systemSection === 'users' ? '管理账号角色和访问范围。' : '公开 TCP 端口只通过 WebRTC P2P 连接到目标设备。' }}</p>
          </div>
          <button class="icon-button" type="button" :title="systemSection === 'users' ? '刷新用户列表' : '刷新端口映射'" :disabled="systemSection === 'users' ? adminUsersLoading : portMappingsLoading" @click="systemSection === 'users' ? refreshAdminUsers() : refreshPortMappings()">
            <RefreshCw :size="19" />
          </button>
        </header>

        <p v-if="adminError" class="error">{{ adminError }}</p>
        <p v-if="adminMessage" class="admin-message">{{ adminMessage }}</p>

        <div v-if="systemSection === 'users' && adminUsers.length" class="admin-user-table" role="table" aria-label="用户列表">
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
        <div v-else-if="systemSection === 'users' && !adminUsersLoading" class="admin-empty">
          <Users :size="28" />
          <strong>暂无用户</strong>
        </div>
        <div v-else-if="systemSection === 'users'" class="admin-empty">
          <RefreshCw :size="28" class="spin" />
          <strong>正在读取用户</strong>
        </div>

        <template v-if="systemSection === 'ports'">
          <form class="port-mapping-form" @submit.prevent="savePortMapping">
            <div class="port-form-heading">
              <div>
                <strong>{{ portMappingEditingId ? '编辑端口映射' : '新建端口映射' }}</strong>
                <p>打洞失败时直接拒绝连接，不会回退到服务端控制通道。</p>
              </div>
              <button v-if="portMappingEditingId" class="icon-button compact" type="button" title="取消编辑" @click="cancelPortMapping">
                <X :size="16" />
              </button>
            </div>
            <div class="port-form-grid">
              <label class="field-label"><span>映射名称</span><input v-model="portMappingForm.name" type="text" maxlength="120" placeholder="例如：远程调试" /></label>
              <label class="field-label"><span>目标设备</span><select v-model="portMappingForm.deviceId"><option value="" disabled>选择在线或离线设备</option><option v-for="device in devices" :key="device.id" :value="device.id">{{ device.name }}{{ device.online ? '（在线）' : '（离线）' }}</option></select></label>
              <label class="field-label"><span>本地目标地址</span><input v-model="portMappingForm.targetHost" type="text" maxlength="255" placeholder="127.0.0.1" /></label>
              <label class="field-label"><span>本地目标端口</span><input v-model.number="portMappingForm.targetPort" type="number" min="1" max="65535" /></label>
              <label class="field-label"><span>服务端公开端口</span><input v-model.number="portMappingForm.listenPort" type="number" min="1" max="65535" /><small>需在 Docker Compose 和宿主机防火墙中发布此 TCP 端口。</small></label>
              <label class="port-enabled"><input v-model="portMappingForm.enabled" type="checkbox" /><span>启用监听</span></label>
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
          <div v-else-if="!portMappingsLoading" class="admin-empty"><Network :size="28" /><strong>还没有端口映射</strong><span>创建一个公开 TCP 入口，用于远程调试。</span></div>
          <div v-else class="admin-empty"><RefreshCw :size="28" class="spin" /><strong>正在读取端口映射</strong></div>
        </template>
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
        <form class="sidebar-new-session" @submit.prevent="startSession">
          <input v-model="firstPrompt" type="text" placeholder="新任务第一句话" aria-label="新任务第一句话" />
          <button class="primary icon-text" type="submit" aria-label="新对话" title="新对话" :disabled="loading">
            <MessageSquarePlus :size="17" />
            <span>新对话</span>
          </button>
        </form>
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
                v-for="group in groupedSessions"
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
                    @click="toggleProject(group.cwd)"
                  >
                    <ChevronRight v-if="isProjectCollapsed(group.cwd)" :size="16" />
                    <ChevronDown v-else :size="16" />
                    <span>{{ group.label }}</span>
                    <small>{{ group.sessions.length }} 条</small>
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
                  v-for="session in group.sessions"
                  v-show="!isProjectCollapsed(group.cwd)"
                  :key="session.id"
                  :data-session-id="session.id"
                  class="session-pill"
                  :class="{ active: session.id === activeSessionId }"
                >
                  <button class="session-open" type="button" :disabled="loading" @click="openThread(session)">
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
            <div v-if="!groupedSessions.length" class="sidebar-empty">还没有项目对话</div>
          </section>

          <section class="sidebar-section recent-section">
            <div class="sidebar-section-heading">
              <span><Clock3 :size="15" />最近对话</span>
              <small>{{ recentSessions.length }}</small>
            </div>
            <div class="recent-list">
              <article
                v-for="session in recentSessions"
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
                <span class="drag-handle recent-drag-handle" title="拖动对话排序"><GripVertical :size="15" /></span>
                <button class="session-open" type="button" :disabled="loading" @click="openThread(session)">
                  <span class="session-title">{{ session.title }}</span>
                  <span class="session-columns">
                    <small>状态：{{ statusLabel(session.status) }}</small>
                    <small>更新：{{ formatShortDate(session.updatedAt) }}</small>
                  </span>
                </button>
                <button class="delete-thread" type="button" title="删除对话" @click="removeThread(session)">
                  <Trash2 :size="15" />
                </button>
              </article>
            </div>
            <div v-if="!recentSessions.length" class="sidebar-empty">暂无非项目对话</div>
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
          <label class="select-control model-control">
            <Cpu :size="16" />
            <select :value="settings.model" :disabled="!models.length" @change="selectModel">
              <option value="">默认模型</option>
              <option v-for="model in models" :key="model.id" :value="model.model" :title="model.description">
                {{ model.displayName || model.model }}
              </option>
            </select>
          </label>
          <div class="status-item context-usage" :title="contextUsageTitle(activeSession)">
            <Gauge :size="16" />
            <span>{{ contextUsageLabel(activeSession) }}</span>
          </div>
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

        <form
          class="composer"
          :class="{ 'drop-active': composerDropActive }"
          @submit.prevent="submitMessage"
          @paste="handlePaste"
          @dragover="handleComposerDragOver"
          @dragleave="handleComposerDragLeave"
          @drop="handleComposerDrop"
        >
          <div v-if="composerDropActive" class="composer-drop-hint" aria-live="polite">
            <ImagePlus :size="18" />
            <span>释放图片以上传</span>
          </div>
          <div v-if="attachments.length" class="attachment-strip">
            <figure v-for="(attachment, index) in attachments" :key="attachment.id || attachment.path || index">
              <img v-if="attachment.url || attachment.previewUrl" :src="attachment.url || attachment.previewUrl" :alt="attachment.name || '图片附件'" />
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
            rows="3"
            placeholder="继续给 Codex 发消息"
            :disabled="!activeSession"
            @keydown="handleComposerKeydown"
          />
          <button class="primary send" type="submit" title="发送" :disabled="!activeSession || (!draft.trim() && !attachments.length) || loading">
            <Send :size="19" />
          </button>
        </form>
      </section>
    </div>
    </template>
  </main>
</template>
