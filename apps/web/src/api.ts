export type SessionMode = "desktop-attached" | "host-new-session" | "disconnected" | "error";

export type SessionRecord = {
  id: string;
  title: string;
  mode: SessionMode;
  status: string;
  createdAt: string;
  updatedAt: string;
  cwd?: string;
  note?: string;
};

export type RemoteEvent = {
  id: number;
  sessionId: string;
  type:
    | "session.status"
    | "user.message"
    | "assistant.delta"
    | "tool.started"
    | "tool.output"
    | "approval.requested"
    | "approval.resolved"
    | "turn.done"
    | "error";
  ts: string;
  payload: Record<string, unknown>;
};

export type AuthStatus = {
  authenticated: boolean;
  username?: string;
  registrationOpen?: boolean;
  tokens?: AccessToken[];
};

export type AccessToken = {
  id: string;
  name: string;
  token: string;
  prefix: string;
  createdAt: string;
  updatedAt: string;
  refreshedAt?: string;
  lastUsedAt?: string;
  lastUsedDeviceId?: string;
};

export type RemoteDevice = {
  id: string;
  name: string;
  online: boolean;
  tokenId?: string;
  tokenName?: string;
  tokenPrefix?: string;
  createdAt: string;
  updatedAt: string;
  lastSeenAt?: string;
};

export type AppSettings = {
  approvalMode: "on-request" | "on-failure" | "never";
  workMode: "edit" | "plan";
};

export type Attachment = {
  id?: string;
  name?: string;
  mimeType?: string;
  path?: string;
  url?: string;
};

export async function getAuthStatus() {
  return request<AuthStatus>("/api/auth/status");
}

export async function register(username: string, password: string) {
  return request<AuthStatus & { ok: boolean }>("/api/auth/register", {
    method: "POST",
    body: JSON.stringify({ username, password })
  });
}

export async function login(username: string, password: string) {
  return request<AuthStatus & { ok: boolean }>("/api/auth/login", {
    method: "POST",
    body: JSON.stringify({ username, password })
  });
}

export async function logout() {
  return request<{ ok: boolean }>("/api/auth/logout", { method: "POST" });
}

export async function changePassword(currentPassword: string, newPassword: string) {
  return request<{ ok: boolean }>("/api/auth/password", {
    method: "POST",
    body: JSON.stringify({ currentPassword, newPassword })
  });
}

export async function getTokens() {
  return request<{ tokens: AccessToken[] }>('/api/auth/tokens');
}

export async function createToken(name: string) {
  return request<{ token: AccessToken }>('/api/auth/tokens', {
    method: 'POST',
    body: JSON.stringify({ name })
  });
}

export async function refreshToken(tokenId: string) {
  return request<{ token: AccessToken }>(`/api/auth/tokens/${encodeURIComponent(tokenId)}/refresh`, {
    method: 'POST'
  });
}

export async function deleteToken(tokenId: string) {
  return request<{ ok: boolean }>(`/api/auth/tokens/${encodeURIComponent(tokenId)}`, { method: 'DELETE' });
}

export async function getSettings(deviceId?: string) {
  return request<AppSettings>(withDevice("/api/settings", deviceId));
}

export async function updateSettings(settings: AppSettings, deviceId?: string) {
  return request<AppSettings>(withDevice("/api/settings", deviceId), {
    method: "POST",
    body: JSON.stringify(settings)
  });
}

export async function uploadImage(name: string, mimeType: string, dataUrl: string) {
  return request<{ attachment: Attachment }>("/api/uploads", {
    method: "POST",
    body: JSON.stringify({ name, mimeType, dataUrl })
  });
}

export async function getHealth() {
  return request<unknown>("/api/health");
}

export async function getSessions(deviceId?: string) {
  return request<{ sessions: SessionRecord[] }>(withDevice("/api/sessions", deviceId));
}

export async function getThreads(deviceId?: string) {
  return request<{ sessions: SessionRecord[] }>(withDevice("/api/threads", deviceId));
}

export async function getDevices() {
  return request<{ devices: RemoteDevice[] }>("/api/devices");
}

export async function createSession(prompt?: string, deviceId?: string) {
  return request<{ session: SessionRecord }>(withDevice("/api/sessions", deviceId), {
    method: "POST",
    body: JSON.stringify({ prompt })
  });
}

export async function sendMessage(sessionId: string, text: string, attachments: Attachment[] = []) {
  return request(`/api/sessions/${sessionId}/messages`, {
    method: "POST",
    body: JSON.stringify({ text, attachments })
  });
}

export async function sendApproval(sessionId: string, approvalId: string, decision: "approved" | "rejected") {
  return request(`/api/sessions/${sessionId}/approvals`, {
    method: "POST",
    body: JSON.stringify({ approvalId, decision })
  });
}

export async function cancelTurn(sessionId: string) {
  return request(`/api/sessions/${sessionId}/cancel`, { method: "POST" });
}

export async function resumeThread(threadId: string) {
  return request<{ session: SessionRecord }>(`/api/threads/${threadId}/resume`, { method: "POST" });
}

export async function deleteThread(threadId: string) {
  return request(`/api/threads/${threadId}`, { method: "DELETE" });
}

function withDevice(path: string, deviceId?: string) {
  if (!deviceId) return path;
  const separator = path.includes("?") ? "&" : "?";
  return `${path}${separator}deviceId=${encodeURIComponent(deviceId)}`;
}

async function request<T>(url: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers);
  headers.set("content-type", "application/json");
  const response = await fetch(url, { ...init, headers, credentials: "same-origin" });
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const body = (await response.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Keep HTTP status message.
    }
    throw new Error(message);
  }
  return (await response.json()) as T;
}
