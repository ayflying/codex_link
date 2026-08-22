import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

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

export type EventRecord = {
  id: number;
  sessionId: string;
  type: string;
  ts: string;
  payload: Record<string, unknown>;
};

type StoreShape = {
  sessions: SessionRecord[];
  events: EventRecord[];
};

export class Store {
  private filePath: string;
  private data: StoreShape = { sessions: [], events: [] };
  private ready: Promise<void>;

  constructor(dataDir: string) {
    this.filePath = path.join(dataDir, "store.json");
    this.ready = this.load(dataDir);
  }

  async getSessions() {
    await this.ready;
    return [...this.data.sessions].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
  }

  async upsertSession(session: SessionRecord) {
    await this.ready;
    const existing = this.data.sessions.findIndex((item) => item.id === session.id);
    if (existing >= 0) this.data.sessions[existing] = session;
    else this.data.sessions.push(session);
    await this.persist();
  }

  async removeSession(sessionId: string) {
    await this.ready;
    this.data.sessions = this.data.sessions.filter((session) => session.id !== sessionId);
    this.data.events = this.data.events.filter((event) => event.sessionId !== sessionId);
    await this.persist();
  }

  async clearEvents(sessionId: string) {
    await this.ready;
    this.data.events = this.data.events.filter((event) => event.sessionId !== sessionId);
    await this.persist();
  }

  async getEvents(sessionId: string, after = 0) {
    await this.ready;
    const limit = Number(process.env.EVENT_BACKLOG_LIMIT || 200);
    const events = this.data.events.filter((event) => event.sessionId === sessionId && event.id > after);
    return events.slice(-limit);
  }

  async appendEvents(events: Omit<EventRecord, "id">[]) {
    await this.ready;
    const next: EventRecord[] = [];
    for (const event of events) {
      if (this.hasEvent(event)) continue;
      const id = (this.data.events.at(-1)?.id || 0) + 1;
      const record = { ...event, id };
      this.data.events.push(record);
      next.push(record);
    }
    if (this.data.events.length > 3000) this.data.events = this.data.events.slice(-3000);
    if (next.length) await this.persist();
    return next;
  }

  private async load(dataDir: string) {
    await mkdir(dataDir, { recursive: true });
    try {
      const raw = await readFile(this.filePath, "utf8");
      this.data = JSON.parse(raw) as StoreShape;
    } catch {
      this.data = { sessions: [], events: [] };
      await this.persist();
    }
  }

  private async persist() {
    await writeFile(this.filePath, JSON.stringify(this.data, null, 2), "utf8");
  }

  private hasEvent(event: Omit<EventRecord, "id">) {
    const payload = JSON.stringify(event.payload);
    return this.data.events.some(
      (existing) =>
        existing.sessionId === event.sessionId &&
        existing.type === event.type &&
        existing.ts === event.ts &&
        JSON.stringify(existing.payload) === payload
    );
  }
}
