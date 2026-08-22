import cors from "cors";
import express from "express";
import { HostAgentClient } from "./hostAgent.js";
import { Store, type EventRecord } from "./store.js";

const app = express();
const port = Number(process.env.PORT || 8788);
const dataDir = process.env.DATA_DIR || "data";
const store = new Store(dataDir);
const host = new HostAgentClient();
const sseClients = new Map<string, Set<express.Response>>();
const pollTimers = new Map<string, NodeJS.Timeout>();

app.use(cors());
app.use(express.json({ limit: "1mb" }));

app.get("/api/health", async (_req, res) => {
  try {
    const hostHealth = await host.health();
    res.json({ ok: true, backend: { dataDir }, hostAgent: hostHealth });
  } catch (error) {
    res.status(503).json({
      ok: false,
      backend: { dataDir },
      hostAgent: { error: error instanceof Error ? error.message : String(error) }
    });
  }
});

app.get("/api/sessions", async (_req, res) => {
  await syncSessions();
  res.json({ sessions: await store.getSessions() });
});

app.post("/api/sessions", async (req, res) => {
  try {
    const prompt = typeof req.body?.prompt === "string" ? req.body.prompt : undefined;
    const { session } = await host.createSession(prompt);
    await store.upsertSession(session);
    startPolling(session.id);
    res.status(201).json({ session });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.get("/api/threads", async (_req, res) => {
  try {
    const { sessions } = await host.listThreads();
    res.json({ sessions });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.post("/api/threads/:id/resume", async (req, res) => {
  try {
    await store.clearEvents(req.params.id);
    const { session } = await host.resumeThread(req.params.id);
    await store.upsertSession(session);
    startPolling(session.id);
    res.json({ session });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.delete("/api/threads/:id", async (req, res) => {
  try {
    await host.archiveThread(req.params.id);
    await store.removeSession(req.params.id);
    res.json({ ok: true });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.get("/api/sessions/:id/events", async (req, res) => {
  const sessionId = req.params.id;
  const after = Number(req.query.after || req.header("last-event-id") || 0);
  const backlog = await store.getEvents(sessionId, after);

  res.writeHead(200, {
    "content-type": "text/event-stream",
    "cache-control": "no-cache, no-transform",
    connection: "keep-alive",
    "x-accel-buffering": "no"
  });

  for (const event of backlog) {
    writeSse(res, event);
  }

  if (!sseClients.has(sessionId)) sseClients.set(sessionId, new Set());
  sseClients.get(sessionId)!.add(res);
  startPolling(sessionId);

  const keepAlive = setInterval(() => res.write(": ping\n\n"), 15_000);
  req.on("close", () => {
    clearInterval(keepAlive);
    sseClients.get(sessionId)?.delete(res);
  });
});

app.post("/api/sessions/:id/messages", async (req, res) => {
  try {
    const text = String(req.body?.text || "").trim();
    if (!text) {
      res.status(400).json({ error: "Message text is required" });
      return;
    }
    await host.message(req.params.id, text);
    startPolling(req.params.id);
    res.status(202).json({ ok: true });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.post("/api/sessions/:id/approvals", async (req, res) => {
  try {
    const approvalId = String(req.body?.approvalId || "");
    const decision = req.body?.decision === "approved" ? "approved" : "rejected";
    await host.approval(req.params.id, approvalId, decision);
    startPolling(req.params.id);
    res.json({ ok: true });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.post("/api/sessions/:id/cancel", async (req, res) => {
  try {
    await host.cancel(req.params.id);
    startPolling(req.params.id);
    res.json({ ok: true });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.listen(port, "0.0.0.0", () => {
  console.log(`codex remote api listening on http://0.0.0.0:${port}`);
});

async function syncSessions() {
  try {
    const { sessions } = await host.listSessions();
    for (const session of sessions) {
      await store.upsertSession(session);
      startPolling(session.id);
    }
  } catch {
    // Health endpoint exposes connectivity failures; session list still returns cached data.
  }
}

function startPolling(sessionId: string) {
  if (pollTimers.has(sessionId)) return;
  const timer = setInterval(() => pollHostEvents(sessionId).catch(() => undefined), 1000);
  pollTimers.set(sessionId, timer);
  void pollHostEvents(sessionId);
}

async function pollHostEvents(sessionId: string) {
  const { events } = await host.events(sessionId, 0);
  if (!events.length) return;
  const appended = await store.appendEvents(events.map(({ id: _id, ...event }) => event));
  broadcast(sessionId, appended);
}

function broadcast(sessionId: string, events: EventRecord[]) {
  const clients = sseClients.get(sessionId);
  if (!clients?.size) return;
  for (const event of events) {
    for (const client of clients) writeSse(client, event);
  }
}

function writeSse(res: express.Response, event: EventRecord) {
  res.write(`id: ${event.id}\n`);
  res.write(`event: ${event.type}\n`);
  res.write(`data: ${JSON.stringify(event)}\n\n`);
}
