import cors from "cors";
import express from "express";
import { CodexBridge } from "./codexBridge.js";
import type { AgentEvent } from "./types.js";

const app = express();
const port = Number(process.env.HOST_AGENT_PORT || 8790);
const token = process.env.HOST_AGENT_TOKEN;
const events: AgentEvent[] = [];

app.use(cors());
app.use(express.json({ limit: "1mb" }));

app.use((req, res, next) => {
  if (!token) return next();
  const header = req.header("authorization");
  if (header !== `Bearer ${token}`) {
    res.status(401).json({ error: "Unauthorized" });
    return;
  }
  next();
});

const bridge = new CodexBridge((event) => {
  event.id = events.length + 1;
  events.push(event);
  if (events.length > 1000) events.shift();
});

app.get("/health", async (_req, res) => {
  res.json({
    ok: true,
    desktopAttach: {
      available: false,
      reason: "当前没有稳定公开的 Codex 桌面任务附着接口。"
    },
    codex: await bridge.health()
  });
});

app.get("/sessions", (_req, res) => {
  const session = bridge.getSession();
  res.json({ sessions: session ? [session] : [] });
});

app.get("/threads", async (_req, res) => {
  try {
    res.json({ sessions: await bridge.listThreads() });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.post("/threads/:id/resume", async (req, res) => {
  try {
    res.json({ session: await bridge.resumeThread(req.params.id) });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.delete("/threads/:id", async (req, res) => {
  try {
    await bridge.archiveThread(req.params.id);
    res.json({ ok: true });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.post("/sessions", async (req, res) => {
  try {
    const session = await bridge.createSession(req.body?.prompt);
    res.status(201).json({ session });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.get("/sessions/:id/events", (req, res) => {
  const after = Number(req.query.after || 0);
  res.json({ events: events.filter((event) => event.sessionId === req.params.id && Number(event.id || 0) > after) });
});

app.post("/sessions/:id/messages", async (req, res) => {
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

app.post("/sessions/:id/approvals", async (req, res) => {
  try {
    const approvalId = String(req.body?.approvalId || "");
    const decision = req.body?.decision === "approved" ? "approved" : "rejected";
    await bridge.resolveApproval(approvalId, decision);
    res.json({ ok: true });
  } catch (error) {
    res.status(500).json({ error: error instanceof Error ? error.message : String(error) });
  }
});

app.post("/sessions/:id/cancel", async (_req, res) => {
  await bridge.cancel();
  res.json({ ok: true });
});

app.listen(port, "127.0.0.1", () => {
  console.log(`codex host-agent listening on http://127.0.0.1:${port}`);
});
