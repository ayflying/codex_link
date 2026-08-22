export type SessionMode = "desktop-attached" | "host-new-session" | "disconnected" | "error";

export type AgentEvent = {
  id?: number;
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

export type AgentSession = {
  id: string;
  title: string;
  mode: SessionMode;
  status: "idle" | "running" | "waiting-approval" | "done" | "error" | "cancelled";
  createdAt: string;
  updatedAt: string;
  cwd?: string;
  note?: string;
};
