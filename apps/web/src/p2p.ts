import type { Attachment, RemoteEvent, SessionRecord } from "./api";

type P2PEnvelope = {
  type: string;
  requestId?: string;
  action?: string;
  payload?: unknown;
  error?: string;
};

type P2PSignal = {
  clientId: string;
  kind: "offer" | "answer" | "candidate" | "error";
  sdp?: string;
  candidate?: string;
  sdpMid?: string | null;
  sdpMLineIndex?: number | null;
  usernameFragment?: string | null;
  iceServers?: string[];
};

type P2PCallbacks = {
  onEvent: (event: RemoteEvent) => void;
  onSession: (session: SessionRecord) => void;
  onClosed: () => void;
};

type PendingRequest = {
  resolve: (value: unknown) => void;
  reject: (reason: Error) => void;
  timer: number;
};

export class P2PRemoteError extends Error {}

const requestTimeoutMs = 35_000;
const uploadChunkBytes = 48 * 1024;

export class P2PTransport {
  private readonly callbacks: P2PCallbacks;
  private signaling: WebSocket | null = null;
  private peerConnection: RTCPeerConnection | null = null;
  private channel: RTCDataChannel | null = null;
  private pending = new Map<string, PendingRequest>();
  private connectPromise: Promise<void> | null = null;
  private connectionResolve: (() => void) | null = null;
  private connectionReject: ((reason: Error) => void) | null = null;
  private clientId = "";
  private deviceIdValue = "";
  private opened = false;
  private p2pOnlyValue = false;
  private remoteDescriptionSet = false;
  private pendingRemoteCandidates: RTCIceCandidateInit[] = [];

  constructor(callbacks: P2PCallbacks) {
    this.callbacks = callbacks;
  }

  get deviceId() {
    return this.deviceIdValue;
  }

  isConnected() {
    return this.opened && this.channel?.readyState === "open";
  }

  isP2POnly() {
    return this.p2pOnlyValue;
  }

  setP2POnly(value: boolean) {
    this.p2pOnlyValue = value;
  }

  async connect(deviceId: string) {
    if (this.isConnected() && this.deviceIdValue === deviceId) return;
    if (this.connectPromise && this.deviceIdValue === deviceId) return this.connectPromise;
    this.close(false);
    if (!window.RTCPeerConnection) throw new Error("当前浏览器不支持 WebRTC");

    this.deviceIdValue = deviceId;
    this.clientId = randomID();
    this.remoteDescriptionSet = false;
    this.pendingRemoteCandidates = [];
    this.connectPromise = new Promise<void>((resolve, reject) => {
      this.connectionResolve = resolve;
      this.connectionReject = reject;
      const endpoint = new URL("/api/p2p/ws", window.location.origin);
      endpoint.protocol = endpoint.protocol === "https:" ? "wss:" : "ws:";
      endpoint.searchParams.set("deviceId", deviceId);
      endpoint.searchParams.set("clientId", this.clientId);
      const signaling = new WebSocket(endpoint);
      this.signaling = signaling;
      signaling.onopen = () => undefined;
      signaling.onmessage = (event) => this.handleSignalingMessage(String(event.data));
      signaling.onerror = () => this.failConnection(new Error("P2P 信令连接失败"));
      signaling.onclose = () => {
        this.signaling = null;
        if (!this.isConnected()) this.failConnection(new Error("P2P 信令连接已断开"));
      };
      window.setTimeout(() => {
        if (!this.isConnected()) this.failConnection(new Error("P2P 连接超时"));
      }, 8_000);
    });
    try {
      await this.connectPromise;
    } finally {
      this.connectPromise = null;
      this.connectionResolve = null;
      this.connectionReject = null;
    }
  }

  async request<T>(action: string, payload: unknown): Promise<T> {
    if (!this.isConnected() || !this.channel) throw new Error("P2P 通道未连接");
    const requestId = randomID();
    return new Promise<T>((resolve, reject) => {
      const timer = window.setTimeout(() => {
        this.pending.delete(requestId);
        this.close(true);
        reject(new Error(`P2P 请求超时: ${action}`));
      }, requestTimeoutMs);
      this.pending.set(requestId, {
        resolve: (value) => resolve(value as T),
        reject,
        timer
      });
      try {
        this.channel!.send(JSON.stringify({ type: "command", requestId, action, payload } satisfies P2PEnvelope));
      } catch (reason) {
        window.clearTimeout(timer);
        this.pending.delete(requestId);
        reject(reason instanceof Error ? reason : new Error(String(reason)));
      }
    });
  }

  async uploadAttachment(name: string, mimeType: string, dataUrl: string) {
    const raw = decodeDataURL(dataUrl);
    if (raw.byteLength === 0 || raw.byteLength > 16 * 1024 * 1024) throw new Error("单个文件必须小于 16MB");
    const uploadId = randomID();
    await this.request("uploads.start", { uploadId, name, mimeType, size: raw.byteLength });
    for (let offset = 0; offset < raw.byteLength; offset += uploadChunkBytes) {
      const chunk = raw.slice(offset, Math.min(raw.byteLength, offset + uploadChunkBytes));
      await this.request("uploads.chunk", { uploadId, data: encodeBase64(chunk) });
    }
    return this.request<Attachment>("uploads.finish", { uploadId });
  }

  close(notify: boolean) {
    const wasConnected = this.isConnected();
    this.opened = false;
    this.rejectPending(new Error("P2P 通道已关闭"));
    try {
      this.signaling?.close();
    } catch {
      // The socket may already be closed.
    }
    try {
      this.channel?.close();
      this.peerConnection?.close();
    } catch {
      // The peer may already be closed.
    }
    this.signaling = null;
    this.channel = null;
    this.peerConnection = null;
    this.connectPromise = null;
    this.remoteDescriptionSet = false;
    this.pendingRemoteCandidates = [];
    if (notify && wasConnected) this.callbacks.onClosed();
  }

  private handleSignalingMessage(raw: string) {
    let message: P2PEnvelope;
    try {
      message = JSON.parse(raw) as P2PEnvelope;
    } catch {
      return;
    }
    if (message.type === "connected") {
      const payload = message.payload as { iceServers?: unknown; p2pOnly?: unknown };
      this.p2pOnlyValue = payload?.p2pOnly === true;
      void this.createPeerConnection(Array.isArray(payload?.iceServers)
        ? (payload.iceServers as string[])
        : []).catch((reason) => this.failConnection(reason instanceof Error ? reason : new Error(String(reason))));
      return;
    }
    if (message.type !== "p2p.signal") return;
    const signal = message.payload as P2PSignal;
    if (!signal || signal.clientId !== this.clientId) return;
    if (signal.kind === "answer" && signal.sdp && this.peerConnection) {
      void this.peerConnection.setRemoteDescription({ type: "answer", sdp: signal.sdp }).then(() => {
        this.remoteDescriptionSet = true;
        const candidates = this.pendingRemoteCandidates;
        this.pendingRemoteCandidates = [];
        return Promise.all(candidates.map((candidate) => this.peerConnection!.addIceCandidate(candidate)));
      }).catch((reason) => this.failConnection(reason instanceof Error ? reason : new Error(String(reason))));
    } else if (signal.kind === "candidate" && this.peerConnection && signal.candidate) {
      const candidate = {
        candidate: signal.candidate,
        sdpMid: signal.sdpMid,
        sdpMLineIndex: signal.sdpMLineIndex,
        usernameFragment: signal.usernameFragment
      } satisfies RTCIceCandidateInit;
      if (this.remoteDescriptionSet) void this.peerConnection.addIceCandidate(candidate);
      else this.pendingRemoteCandidates.push(candidate);
    } else if (signal.kind === "error") {
      this.failConnection(new Error(signal.sdp || "P2P 协商失败"));
    }
  }

  private async createPeerConnection(iceServers: string[]) {
    if (this.peerConnection) return;
    const peerConnection = new RTCPeerConnection({
      iceServers: iceServers.map((urls) => ({ urls }))
    });
    this.peerConnection = peerConnection;
    peerConnection.onicecandidate = (event) => {
      if (event.candidate) this.sendSignal({
        clientId: this.clientId,
        kind: "candidate",
        candidate: event.candidate.candidate,
        sdpMid: event.candidate.sdpMid,
        sdpMLineIndex: event.candidate.sdpMLineIndex,
        usernameFragment: event.candidate.usernameFragment
      });
    };
    peerConnection.onconnectionstatechange = () => {
      if (peerConnection.connectionState === "failed" || peerConnection.connectionState === "closed") {
        this.failConnection(new Error("P2P 连接失败"));
      }
    };
    const channel = peerConnection.createDataChannel("codex-control", { ordered: true });
    this.channel = channel;
    channel.onmessage = (event) => this.handleDataMessage(String(event.data));
    channel.onerror = () => this.failConnection(new Error("P2P 数据通道错误"));
    channel.onclose = () => {
      if (this.opened) this.close(true);
    };
    channel.onopen = () => {
      this.opened = true;
      this.connectionResolve?.();
    };
    const offer = await peerConnection.createOffer();
    await peerConnection.setLocalDescription(offer);
    this.sendSignal({ clientId: this.clientId, kind: "offer", sdp: offer.sdp || "", iceServers });
  }

  private handleDataMessage(raw: string) {
    let message: P2PEnvelope;
    try {
      message = JSON.parse(raw) as P2PEnvelope;
    } catch {
      return;
    }
    if (message.type === "response" && message.requestId) {
      const pending = this.pending.get(message.requestId);
      if (!pending) return;
      this.pending.delete(message.requestId);
      window.clearTimeout(pending.timer);
      if (message.error) pending.reject(new P2PRemoteError(message.error));
      else pending.resolve(message.payload);
      return;
    }
    if (message.type === "event" && message.payload) {
      this.callbacks.onEvent(message.payload as RemoteEvent);
    } else if (message.type === "session" && message.payload) {
      this.callbacks.onSession(message.payload as SessionRecord);
    }
  }

  private sendSignal(signal: P2PSignal) {
    if (this.signaling?.readyState !== WebSocket.OPEN) return;
    this.signaling.send(JSON.stringify({ type: "p2p.signal", payload: signal } satisfies P2PEnvelope));
  }

  private failConnection(reason: Error) {
    const wasConnected = this.isConnected();
    if (this.connectionReject) this.connectionReject(reason);
    this.connectionReject = null;
    this.close(wasConnected);
  }

  private rejectPending(reason: Error) {
    for (const pending of this.pending.values()) {
      window.clearTimeout(pending.timer);
      pending.reject(reason);
    }
    this.pending.clear();
  }
}

function randomID() {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID().replaceAll("-", "");
  return `${Date.now().toString(16)}${Math.random().toString(16).slice(2)}`;
}

function decodeDataURL(value: string) {
  const comma = value.indexOf(",");
  if (comma < 0) throw new Error("图片数据格式不正确");
  const encoded = atob(value.slice(comma + 1));
  const bytes = new Uint8Array(encoded.length);
  for (let index = 0; index < encoded.length; index += 1) bytes[index] = encoded.charCodeAt(index);
  return bytes;
}

function encodeBase64(value: Uint8Array) {
  let binary = "";
  for (let index = 0; index < value.length; index += 1) binary += String.fromCharCode(value[index]);
  return btoa(binary).replaceAll("=", "");
}
