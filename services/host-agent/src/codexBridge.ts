import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { randomUUID } from "node:crypto";
import { EventEmitter } from "node:events";
import { existsSync, readdirSync, statSync } from "node:fs";
import path from "node:path";
import readline from "node:readline";
import type { AgentEvent, AgentSession } from "./types.js";

type BridgeEventHandler = (event: AgentEvent) => void;

type JsonRpcMessage = {
  jsonrpc?: string;
  id?: number | string;
  method?: string;
  params?: unknown;
  result?: unknown;
  error?: unknown;
};

export class CodexBridge extends EventEmitter {
  private proc?: ChildProcessWithoutNullStreams;
  private nextRpcId = 1;
  private pending = new Map<number | string, { resolve: (value: unknown) => void; reject: (reason?: unknown) => void }>();
  private session?: AgentSession;
  private codexThreadId?: string;
  private activeTurnId?: string;
  private pendingApprovals = new Map<string, number | string>();
  private initialized = false;
  private readonly codexBin = process.env.CODEX_BIN || discoverCodexBin() || "codex";
  private readonly cwd = process.env.CODEX_CWD || process.cwd();
  private readonly mock = process.env.HOST_AGENT_MOCK === "1";

  constructor(private readonly onEvent: BridgeEventHandler) {
    super();
  }

  getSession() {
    return this.session;
  }

  async createSession(prompt?: string, sessionId?: string) {
    const now = new Date().toISOString();
    this.session = {
      id: sessionId || randomUUID(),
      title: prompt?.slice(0, 60) || "Codex 会话",
      mode: "host-new-session",
      status: "idle",
      createdAt: now,
      updatedAt: now,
      cwd: this.cwd,
      note: "当前没有稳定公开的 Codex 桌面任务附着接口，已降级为宿主机新建会话。"
    };

    this.emitEvent("session.status", {
      mode: this.session.mode,
      status: this.session.status,
      note: this.session.note
    });

    if (this.mock) {
      this.emitEvent("assistant.delta", {
        text: "模拟模式已启用，可以在手机端测试对话、输出和审批流程。\n"
      });
      this.emitEvent("turn.done", { status: "mock-ready" });
      return this.session;
    }

    await this.ensureReady();
    await this.startCodexThread();

    if (prompt) {
      await this.sendMessage(prompt);
    }

    return this.session;
  }

  async sendMessage(text: string, sessionId?: string) {
    if (!this.session || (sessionId && this.session.id !== sessionId)) {
      if (sessionId) {
        try {
          await this.resumeThread(sessionId);
        } catch {
          await this.createSession(undefined, sessionId);
        }
      } else {
        await this.createSession();
      }
    }

    this.emitEvent("user.message", { text });

    if (this.mock) {
      this.updateStatus("running");
      this.emitEvent("assistant.delta", { text: `收到：${text}\n` });
      if (needsMockApproval(text)) {
        this.emitEvent("tool.started", { command: "echo mock", cwd: this.cwd });
        this.emitEvent("tool.output", { text: "模拟命令输出\n" });
        this.emitEvent("approval.requested", {
          approvalId: randomUUID(),
          title: "需要确认执行命令",
          description: "这是模拟模式下的审批卡片，用来验证手机端批准/拒绝流程。",
          command: "echo mock"
        });
        this.updateStatus("waiting-approval");
      } else {
        this.emitEvent("turn.done", { status: "done" });
        this.updateStatus("done");
      }
      return;
    }

    if (!this.proc || !this.codexThreadId) {
      throw new Error("Codex app-server is not ready");
    }

    this.updateStatus("running");
    const result = await this.request("turn/start", {
      threadId: this.codexThreadId,
      input: [{ type: "text", text, text_elements: [] }]
    });
    if (typeof result === "object" && result && "turn" in result) {
      const turn = (result as { turn: { id?: unknown } }).turn;
      if (turn?.id) this.activeTurnId = String(turn.id);
    }
  }

  async resolveApproval(approvalId: string, decision: "approved" | "rejected") {
    this.emitEvent("approval.resolved", { approvalId, decision });

    if (this.mock) {
      this.updateStatus("running");
      this.emitEvent("assistant.delta", { text: decision === "approved" ? "已批准，继续执行。\n" : "已拒绝，操作已取消。\n" });
      this.emitEvent("turn.done", { status: "done" });
      this.updateStatus("done");
      return;
    }

    const requestId = this.pendingApprovals.get(approvalId);
    if (requestId === undefined) throw new Error("Approval request is no longer pending");
    this.pendingApprovals.delete(approvalId);
    this.write({
      jsonrpc: "2.0",
      id: requestId,
      result: { decision: decision === "approved" ? "accept" : "decline" }
    });
  }

  async cancel() {
    if (!this.session) return;
    if (!this.mock && this.activeTurnId) {
      await this.request("turn/interrupt", {
        threadId: this.codexThreadId,
        turnId: this.activeTurnId
      });
    }
    this.updateStatus("cancelled");
    this.emitEvent("turn.done", { status: "cancelled" });
  }

  async health() {
    return {
      available: Boolean(this.mock || this.proc),
      mode: this.session?.mode ?? "disconnected",
      codexBin: this.codexBin,
      cwd: this.cwd,
      mock: this.mock
    };
  }

  async listThreads() {
    await this.ensureReady();
    const result = await this.request("thread/list", {
      limit: 100,
      sortKey: "updated_at",
      sortDirection: "desc",
      archived: false
    });
    const threads = isRecord(result) && Array.isArray(result.data) ? result.data : [];
    return threads.map((thread) => this.threadToSession(asRecord(thread)));
  }

  async resumeThread(threadId: string) {
    await this.ensureReady();
    const result = await this.request("thread/resume", {
      threadId,
      developerInstructions:
        process.env.CODEX_DEVELOPER_INSTRUCTIONS ||
        "请全程使用中文与用户交流。除非用户明确要求其他语言，否则回复、解释、状态说明和审批说明均使用简体中文。"
    });
    if (!isRecord(result) || !isRecord(result.thread)) throw new Error("Codex app-server did not return a resumed thread");
    const thread = result.thread;
    this.codexThreadId = String(thread.id);
    this.session = this.threadToSession(thread);
    this.emitEvent("session.status", { status: this.session.status, mode: this.session.mode });
    this.hydrateThread(thread);
    return this.session;
  }

  async archiveThread(threadId: string) {
    await this.ensureReady();
    await this.request("thread/archive", { threadId });
    if (this.codexThreadId === threadId || this.session?.id === threadId) {
      this.session = undefined;
      this.codexThreadId = undefined;
      this.activeTurnId = undefined;
    }
  }

  private async startProcess() {
    if (this.proc) return;

    this.proc = spawn(this.codexBin, ["app-server"], {
      cwd: this.cwd,
      env: process.env
    });

    this.proc.on("error", (error) => {
      this.updateStatus("error");
      this.emitEvent("error", { message: error.message });
    });

    this.proc.on("exit", (code, signal) => {
      this.updateStatus("error");
      this.emitEvent("error", { message: "Codex app-server exited", code, signal });
      this.proc = undefined;
    });

    const rl = readline.createInterface({ input: this.proc.stdout });
    rl.on("line", (line) => this.handleLine(line));

    this.proc.stderr.on("data", (chunk: Buffer) => {
      const text = chunk.toString("utf8");
      if (text.includes('"level":"ERROR"')) this.emitEvent("error", { message: text });
    });
  }

  private async ensureReady() {
    await this.startProcess();
    if (!this.initialized) {
      await this.initialize();
      this.initialized = true;
    }
  }

  private async startCodexThread() {
    const result = await this.request("thread/start", {
      cwd: this.cwd,
      developerInstructions:
        process.env.CODEX_DEVELOPER_INSTRUCTIONS ||
        "请全程使用中文与用户交流。除非用户明确要求其他语言，否则回复、解释、状态说明和审批说明均使用简体中文。"
    });
    if (typeof result === "object" && result && "thread" in result) {
      const thread = (result as { thread: { id?: unknown } }).thread;
      if (thread?.id) this.codexThreadId = String(thread.id);
    } else {
      throw new Error("Codex app-server did not return a thread id");
    }
    if (!this.codexThreadId) throw new Error("Codex app-server did not return a thread id");
    if (this.session) this.session.id = this.codexThreadId;
  }

  private async initialize() {
    await this.request("initialize", {
      clientInfo: {
        name: "codex-mobile-remote",
        title: "Codex Mobile Remote",
        version: "0.1.0"
      },
      capabilities: {
        experimentalApi: true,
        requestAttestation: false
      }
    });
  }

  private threadToSession(thread: Record<string, unknown>): AgentSession {
    const status = isRecord(thread.status) ? String(thread.status.type ?? "idle") : String(thread.status ?? "idle");
    return {
      id: String(thread.id),
      title: redactSensitiveText(String(thread.name || thread.preview || "Codex 会话")).slice(0, 80),
      mode: "host-new-session",
      status: status === "active" ? "running" : status === "error" ? "error" : "done",
      createdAt: timestampToIso(thread.createdAt),
      updatedAt: timestampToIso(thread.updatedAt),
      cwd: typeof thread.cwd === "string" ? thread.cwd : undefined
    };
  }

  private hydrateThread(thread: Record<string, unknown>) {
    const turnLimit = Number(process.env.CODEX_HISTORY_TURN_LIMIT || 10);
    const turns = Array.isArray(thread.turns) ? thread.turns.slice(-turnLimit) : [];
    for (const rawTurn of turns) {
      const turn = asRecord(rawTurn);
      const ts = timestampToIso(turn.startedAt);
      const items = Array.isArray(turn.items) ? turn.items : [];
      for (const rawItem of items) {
        const item = asRecord(rawItem);
        if (item.type === "userMessage" && Array.isArray(item.content)) {
          const text = item.content
            .map((content) => asRecord(content))
            .filter((content) => content.type === "text")
            .map((content) => String(content.text ?? ""))
            .join("");
          if (text) this.emitEventAt("user.message", { text }, ts);
        } else if (item.type === "agentMessage" && item.text) {
          this.emitEventAt("assistant.delta", { text: String(item.text) }, ts);
        } else if (item.type === "commandExecution") {
          this.emitEventAt("tool.started", { command: String(item.command ?? "命令执行") }, ts);
          if (item.aggregatedOutput) this.emitEventAt("tool.output", { text: String(item.aggregatedOutput) }, ts);
        }
      }
    }
  }

  private request(method: string, params?: unknown) {
    const id = this.nextRpcId++;
    this.write({ jsonrpc: "2.0", id, method, params });
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      setTimeout(() => {
        if (this.pending.has(id)) {
          this.pending.delete(id);
          reject(new Error(`Timed out waiting for ${method}`));
        }
      }, 30_000);
    });
  }

  private async notify(method: string, params?: unknown) {
    this.write({ jsonrpc: "2.0", method, params });
  }

  private write(message: JsonRpcMessage) {
    if (!this.proc) throw new Error("Codex app-server process is not running");
    this.proc.stdin.write(`${JSON.stringify(message)}\n`);
  }

  private handleLine(line: string) {
    let msg: JsonRpcMessage;
    try {
      msg = JSON.parse(line) as JsonRpcMessage;
    } catch {
      this.emitEvent("tool.output", { text: `${line}\n` });
      return;
    }

    if (msg.id !== undefined && this.pending.has(msg.id) && !msg.method) {
      const pending = this.pending.get(msg.id)!;
      this.pending.delete(msg.id);
      if (msg.error) pending.reject(msg.error);
      else pending.resolve(msg.result);
      return;
    }

    if (msg.method) this.mapCodexEvent(msg.method, msg.params, msg.id);
  }

  private mapCodexEvent(method: string, params: unknown, requestId?: number | string) {
    const payload = typeof params === "object" && params ? (params as Record<string, unknown>) : { value: params };

    if (requestId !== undefined && method.includes("requestApproval")) {
      const approvalId = String(payload.approvalId ?? payload.itemId ?? requestId);
      this.pendingApprovals.set(approvalId, requestId);
      this.updateStatus("waiting-approval");
      this.emitEvent("approval.requested", {
        approvalId,
        ...payload
      });
      return;
    }

    if (method === "turn/completed") {
      this.updateStatus("done");
      this.emitEvent("turn.done", payload);
      return;
    }

    if (method === "item/agentMessage/delta") {
      this.emitEvent("assistant.delta", { text: String(payload.delta ?? "") });
      return;
    }

    if (method === "command/exec/outputDelta" || method === "item/commandExecution/outputDelta" || method === "process/outputDelta") {
      this.emitEvent("tool.output", { text: String(payload.delta ?? payload.text ?? "") });
      return;
    }

    if (method === "item/started") {
      const item = typeof payload.item === "object" && payload.item ? (payload.item as Record<string, unknown>) : {};
      const itemType = String(item.type ?? "");
      if (itemType.toLowerCase().includes("command")) {
        this.emitEvent("tool.started", {
          command: item.command ?? item.parsedCommand ?? "命令开始执行",
          ...payload
        });
      }
      return;
    }

    if (method === "error") {
      this.updateStatus("error");
      this.emitEvent("error", payload);
      return;
    }

    // App-server emits many lifecycle notifications. Keep them internal unless
    // they map to a user-facing message, tool output, approval, or error above.
  }

  private updateStatus(status: AgentSession["status"]) {
    if (!this.session) return;
    this.session.status = status;
    this.session.updatedAt = new Date().toISOString();
    this.emitEvent("session.status", {
      status,
      mode: this.session.mode
    });
  }

  private emitEvent(type: AgentEvent["type"], payload: Record<string, unknown>) {
    this.emitEventAt(type, payload, new Date().toISOString());
  }

  private emitEventAt(type: AgentEvent["type"], payload: Record<string, unknown>, ts: string) {
    if (!this.session) return;
    this.onEvent({
      sessionId: this.session.id,
      type,
      ts,
      payload
    });
  }
}

function needsMockApproval(text: string) {
  return /审批|批准|拒绝|命令|执行|运行|shell|terminal|cmd|powershell|测试审批/i.test(text);
}

function redactSensitiveText(text: string) {
  return text
    .replace(/(appsecret(?:\s*\([^)]*\))?\s*[:：=]?\s*)[a-z0-9_-]{12,}/gi, "$1[已隐藏]")
    .replace(/(\b(?:api[_-]?key|ccs[_-]?key|secret|token|password|passwd|pwd)\b\s*[\(:：=]?\s*)[^\s,，;；]+/gi, "$1[已隐藏]")
    .replace(/\bsk-[a-z0-9_-]{12,}\b/gi, "[已隐藏]");
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function asRecord(value: unknown) {
  return isRecord(value) ? value : {};
}

function timestampToIso(value: unknown) {
  const numeric = Number(value);
  if (!Number.isFinite(numeric) || numeric <= 0) return new Date().toISOString();
  return new Date(numeric < 10_000_000_000 ? numeric * 1000 : numeric).toISOString();
}

function discoverCodexBin() {
  if (process.platform !== "win32") return undefined;
  const localAppData = process.env.LOCALAPPDATA;
  if (!localAppData) return undefined;
  const binRoot = path.join(localAppData, "OpenAI", "Codex", "bin");
  if (!existsSync(binRoot)) return undefined;
  const candidates = readdirSync(binRoot)
    .map((entry) => path.join(binRoot, entry, "codex.exe"))
    .filter((candidate) => existsSync(candidate))
    .map((candidate) => ({ candidate, mtimeMs: statSync(candidate).mtimeMs }))
    .sort((a, b) => b.mtimeMs - a.mtimeMs);
  return candidates[0]?.candidate;
}
