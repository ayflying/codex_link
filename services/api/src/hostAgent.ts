import type { EventRecord, SessionRecord } from "./store.js";

export class HostAgentClient {
  private baseUrl: string;
  private token?: string;

  constructor() {
    this.baseUrl = (process.env.HOST_AGENT_URL || "http://host.docker.internal:8790").replace(/\/$/, "");
    this.token = process.env.HOST_AGENT_TOKEN;
  }

  async health() {
    return this.request<unknown>("/health");
  }

  async listSessions() {
    return this.request<{ sessions: SessionRecord[] }>("/sessions");
  }

  async createSession(prompt?: string) {
    return this.request<{ session: SessionRecord }>("/sessions", {
      method: "POST",
      body: JSON.stringify({ prompt })
    });
  }

  async listThreads() {
    return this.request<{ sessions: SessionRecord[] }>("/threads");
  }

  async resumeThread(threadId: string) {
    return this.request<{ session: SessionRecord }>(`/threads/${threadId}/resume`, { method: "POST" });
  }

  async archiveThread(threadId: string) {
    return this.request(`/threads/${threadId}`, { method: "DELETE" });
  }

  async events(sessionId: string, after = 0) {
    return this.request<{ events: EventRecord[] }>(`/sessions/${sessionId}/events?after=${after}`);
  }

  async message(sessionId: string, text: string) {
    return this.request(`/sessions/${sessionId}/messages`, {
      method: "POST",
      body: JSON.stringify({ text })
    });
  }

  async approval(sessionId: string, approvalId: string, decision: "approved" | "rejected") {
    return this.request(`/sessions/${sessionId}/approvals`, {
      method: "POST",
      body: JSON.stringify({ approvalId, decision })
    });
  }

  async cancel(sessionId: string) {
    return this.request(`/sessions/${sessionId}/cancel`, { method: "POST" });
  }

  private async request<T>(path: string, init: RequestInit = {}) {
    const headers = new Headers(init.headers);
    headers.set("content-type", "application/json");
    if (this.token) headers.set("authorization", `Bearer ${this.token}`);

    const response = await fetch(`${this.baseUrl}${path}`, {
      ...init,
      headers
    });

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
}
