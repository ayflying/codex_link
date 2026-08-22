import express from "express";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { CodexBridge } from "../services/host-agent/dist/codexBridge.js";
import { Store } from "../services/api/dist/store.js";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const rootDir = path.resolve(__dirname, "..");
const webDist = path.join(rootDir, "apps", "web", "dist");
const dataDir = process.env.DATA_DIR || path.join(rootDir, "data-local");
const port = Number(process.env.PORT || 8787);
const host = process.env.HOST || "0.0.0.0";

process.env.CODEX_CWD ||= rootDir;
process.env.CODEX_HISTORY_TURN_LIMIT ||= "10";
process.env.EVENT_BACKLOG_LIMIT ||= "120";

const app = express();
const store = new Store(dataDir);
const sseClients = new Map();

const bridge = new CodexBridge((event) => {
  void store
    .appendEvents([
      {
        sessionId: event.sessionId,
        type: event.type,
        ts: event.ts,
        payload: event.payload
      }
    ])
    .then((events) => broadcast(event.sessionId, events))
    .catch((error) => console.error("store event append failed", error));
});

app.use(express.json({ limit: "1mb" }));

app.get("/api/health", async (_req, res) => {
  try {
    res.json({
      ok: true,
      backend: { dataDir, mode: "local-single-process" },
      hostAgent: {
        ok: true,
        desktopAttach: {
          available: false,
          reason: "当前没有稳定公开的 Codex 桌面任务附着接口。"
        },
        codex: await bridge.health()
      }
    });
  } catch (error) {
    res.status(503).json({ ok: false, error: error instanceof Error ? error.message : String(error) });
  }
});

app.get("/api/sessions", async (_req, res) => {
  const current = bridge.getSession();
  const cached = await store.getSessions();
  res.json({ sessions: current ? [current, ...cached.filter((session) => session.id !== current.id)] : cached });
});

app.post("/api/sessions", async (req, res) => {
  try {
    const prompt = typeof req.body?.prompt === "string" ? req.body.prompt : undefined;
    const session = await bridge.createSession(prompt);
    await store.upsertSession(session);
    res.status(201).json({ session });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.get("/api/threads", async (_req, res) => {
  try {
    const sessions = await bridge.listThreads();
    res.json({ sessions });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.post("/api/threads/:id/resume", async (req, res) => {
  try {
    await store.clearEvents(req.params.id);
    const session = await bridge.resumeThread(req.params.id);
    await store.upsertSession(session);
    res.json({ session });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.delete("/api/threads/:id", async (req, res) => {
  try {
    await bridge.archiveThread(req.params.id);
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

  for (const event of backlog) writeSse(res, event);

  if (!sseClients.has(sessionId)) sseClients.set(sessionId, new Set());
  sseClients.get(sessionId).add(res);

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
    await bridge.sendMessage(text, req.params.id);
    res.status(202).json({ ok: true });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.post("/api/sessions/:id/approvals", async (req, res) => {
  try {
    const approvalId = String(req.body?.approvalId || "");
    const decision = req.body?.decision === "approved" ? "approved" : "rejected";
    await bridge.resolveApproval(approvalId, decision);
    res.json({ ok: true });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.post("/api/sessions/:id/cancel", async (_req, res) => {
  await bridge.cancel();
  res.json({ ok: true });
});

app.use(express.static(webDist));
app.use((req, res, next) => {
  if (req.method !== "GET") {
    next();
    return;
  }
  res.sendFile(path.join(webDist, "index.html"));
});

app.listen(port, host, () => {
  console.log(`Codex local remote server: http://${host === "0.0.0.0" ? "127.0.0.1" : host}:${port}`);
  console.log(`Phone/Tailscale URL: http://<tailscale-ip>:${port}`);
  console.log(`Workspace: ${process.env.CODEX_CWD}`);
});

function broadcast(sessionId, events) {
  const clients = sseClients.get(sessionId);
  if (!clients?.size) return;
  for (const event of events) {
    for (const client of clients) writeSse(client, event);
  }
}

function writeSse(res, event) {
  res.write(`id: ${event.id}\n`);
  res.write(`event: ${event.type}\n`);
  res.write(`data: ${JSON.stringify(event)}\n\n`);
}
