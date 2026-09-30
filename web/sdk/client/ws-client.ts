/**
 * WebSocket 客户端
 *
 * 负责与 react-base-service 的 WebSocket 连接管理。
 *
 * 核心能力：
 * - 连接建立与断开
 * - 消息收发（JSON 序列化/反序列化）
 * - 自动重连：指数退避（1s → 2s → 4s → 8s → ... → 最大 30s）
 * - 心跳检测：服务端每 20s 发 heartbeat，客户端收到任何消息重置计时器，
 *   超时未收到消息则主动关闭连接触发重连
 * - 断网宽限：offline 事件不立刻拆连接，宽限期（默认 5s）内网络恢复则原连接
 *   继续使用（TCP 重传补齐数据，上层无感知），超时才主动拆连接触发重连
 * - 消息缓冲：连接未就绪时，send 的消息进入队列，连接建立后自动 flush
 *
 * 生命周期：
 *   connect() → onOpen → [收发消息] → onClose → scheduleReconnect → connect() → ...
 *   disconnect() 时设置 intentionalClose=true，跳过自动重连
 */
import type { ReactEvent, WsMessage } from '../protocol/types';
import type { WsClientCallbacks, WsClientOptions } from './types';
import { track } from '../analytics';

const DEFAULT_HEARTBEAT_TIMEOUT = 30_000;
const DEFAULT_CONNECT_TIMEOUT = 10_000;
const DEFAULT_RECONNECT_MAX_DELAY = 30_000;
const INITIAL_RECONNECT_DELAY = 1_000;
const DEFAULT_OFFLINE_GRACE = 5_000;

type DisconnectTrigger =
  | 'close_event'
  | 'heartbeat_timeout'
  | 'offline'
  | 'offline_grace_expired'
  | 'intentional_disconnect'
  | 'connect_timeout'
  | 'unknown';

interface LastReceivedEvent {
  type: string;
  seq?: number;
  runId?: string;
  sessionId?: string;
  receivedAt: number;
}

export class WsClient {
  private ws: WebSocket | null = null;
  private url: string;
  private options: Required<WsClientOptions>;
  private callbacks: WsClientCallbacks;
  /** 当前重连尝试次数，连接成功后重置为 0 */
  private reconnectAttempts = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  /** WebSocket 建立阶段超时计时器 */
  private connectTimer: ReturnType<typeof setTimeout> | null = null;
  /** 心跳超时计时器，收到任何消息重置 */
  private heartbeatTimer: ReturnType<typeof setTimeout> | null = null;
  /** 断网宽限计时器，offline 后启动，online 或连接关闭时取消 */
  private offlineGraceTimer: ReturnType<typeof setTimeout> | null = null;
  /** 标记是否为主动断开（主动断开不触发重连） */
  private intentionalClose = false;
  /** 连接未就绪时缓存的消息队列 */
  private pendingMessages: string[] = [];
  /** 每次创建 WebSocket 都递增，复现日志可按 connectionId 串联。 */
  private connectionSequence = 0;
  private activeConnectionId: number | null = null;
  private activeConnectionAttemptId: string | null = null;
  private lastReceivedEvent: LastReceivedEvent | null = null;
  private disconnectTrigger: DisconnectTrigger = 'unknown';

  private log(event: string, details: Record<string, unknown> = {}): void {
    const runtimeContext = this.callbacks.getRuntimeContext?.();
    const payload = {
      timestamp: new Date().toISOString(),
      event,
      connectionId: this.activeConnectionId,
      connectionAttemptId: this.activeConnectionAttemptId,
      readyState: this.ws?.readyState ?? null,
      navigatorOnline: typeof navigator === 'undefined' ? null : navigator.onLine,
      visibilityState: typeof document === 'undefined' ? null : document.visibilityState,
      ...details,
      runId: runtimeContext?.runId ?? details.runId ?? null,
      runStatus: runtimeContext?.runStatus ?? null,
    };
    console.log(`[agent-sdk][ws] ${event} ${JSON.stringify(payload)}`);
    track('KKT_001', payload);
  }

  private handleVisibilityChange = (): void => {
    this.log('page:visibility-change');
  };

  private handlePageHide = (event: PageTransitionEvent): void => {
    this.log('page:hide', { persisted: event.persisted });
  };

  private handlePageShow = (event: PageTransitionEvent): void => {
    this.log('page:show', { persisted: event.persisted });
  };

  private handlePageFreeze = (): void => {
    this.log('page:freeze');
  };

  private handlePageResume = (): void => {
    this.log('page:resume');
  };

  private handleBeforeUnload = (): void => {
    this.log('page:beforeunload');
  };

  /**
   * 浏览器断网：不立刻拆连接，先进入宽限期等网络恢复。
   *
   * TCP 本身扛得住短暂抖动：只要两端都不主动关，IP 未变的瞬断恢复后内核重传
   * 会补齐数据，原连接继续可用，上层（包括后端的 run）完全无感。因此 offline
   * 只挂宽限计时器：宽限期内 online 到达则取消计时器；超时才主动拆连接。
   * 宽限期内心跳超时或 onclose 到达则走各自原有断开路径（onDisconnected 会
   * 清掉宽限计时器，不会重复拆）。offlineGraceMs 设为 0 恢复"立刻拆"行为。
   */
  private handleOffline = (): void => {
    this.log('network:offline', {
      offlineGraceMs: this.options.offlineGraceMs,
      hasSocket: !!this.ws,
      hasOfflineGraceTimer: !!this.offlineGraceTimer,
    });
    if (!this.ws) {
      return;
    }
    if (this.options.offlineGraceMs <= 0) {
      this.teardownSocket('offline');
      return;
    }
    if (this.offlineGraceTimer) {
      return;
    }
    this.offlineGraceTimer = setTimeout(() => {
      this.offlineGraceTimer = null;
      this.log('network:offline-grace-expired', {
        offlineGraceMs: this.options.offlineGraceMs,
      });
      this.teardownSocket('offline_grace_expired');
    }, this.options.offlineGraceMs);
  };

  /**
   * 主动拆除当前连接并走统一断开流程。
   *
   * 网络已断时调 ws.close() 后 onclose 可能迟迟不触发，所以主动走断开流程，
   * 并先解绑 socket 回调，避免随后的 onclose 再重复执行一遍。
   */
  private teardownSocket(trigger: DisconnectTrigger): void {
    if (!this.ws) {
      return;
    }
    this.disconnectTrigger = trigger;
    this.log('socket:teardown', { trigger });
    this.detachSocket(this.ws);
    try {
      this.ws.close();
    } catch (error) {
      this.log('socket:close-threw', {
        trigger,
        error: error instanceof Error ? error.message : String(error),
      });
      // 已断网，close 可能抛错，忽略
    }
    this.ws = null;
    this.onDisconnected(trigger);
  }

  /**
   * 浏览器恢复联网：先取消断网宽限（宽限内救回的连接原样继续用）；
   * 若非主动断开且当前未连接，立即重连并重置退避，避免还在指数退避等待中白等。
   */
  private handleOnline = (): void => {
    const rescuedDuringGrace = !!this.offlineGraceTimer;
    if (this.offlineGraceTimer) {
      clearTimeout(this.offlineGraceTimer);
      this.offlineGraceTimer = null;
    }
    this.log('network:online', {
      rescuedDuringGrace,
      intentionalClose: this.intentionalClose,
      isConnected: this.isConnected,
      hasReconnectTimer: !!this.reconnectTimer,
      reconnectAttempts: this.reconnectAttempts,
    });
    if (this.intentionalClose || this.isConnected) {
      return;
    }
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.reconnectAttempts = 0;
    this.connect();
  };

  constructor(url: string, callbacks: WsClientCallbacks = {}, options: WsClientOptions = {}) {
    this.url = url;
    this.callbacks = callbacks;
    this.options = {
      reconnect: options.reconnect ?? true,
      heartbeatTimeout: options.heartbeatTimeout ?? DEFAULT_HEARTBEAT_TIMEOUT,
      connectTimeout: options.connectTimeout ?? DEFAULT_CONNECT_TIMEOUT,
      reconnectMaxDelay: options.reconnectMaxDelay ?? DEFAULT_RECONNECT_MAX_DELAY,
      offlineGraceMs: options.offlineGraceMs ?? DEFAULT_OFFLINE_GRACE,
    };

    if (typeof window !== 'undefined') {
      window.addEventListener('offline', this.handleOffline);
      window.addEventListener('online', this.handleOnline);
      window.addEventListener('pagehide', this.handlePageHide);
      window.addEventListener('pageshow', this.handlePageShow);
      window.addEventListener('beforeunload', this.handleBeforeUnload);
    }
    if (typeof document !== 'undefined') {
      document.addEventListener('visibilitychange', this.handleVisibilityChange);
      document.addEventListener('freeze', this.handlePageFreeze);
      document.addEventListener('resume', this.handlePageResume);
    }
  }

  get isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  /** 连接是否建立中（OPEN 或 CONNECTING）：调用方可据此区分「首次/切换建连」与「断线」。 */
  get isConnecting(): boolean {
    return this.ws?.readyState === WebSocket.OPEN || this.ws?.readyState === WebSocket.CONNECTING;
  }

  /** 建立 WebSocket 连接（如果已连接或正在连接中则跳过） */
  connect(): void {
    if (this.ws && (this.ws.readyState === WebSocket.OPEN || this.ws.readyState === WebSocket.CONNECTING)) {
      this.log('connect:skipped', { reason: 'socket_open_or_connecting' });
      return;
    }

    this.intentionalClose = false;
    this.disconnectTrigger = 'unknown';
    this.lastReceivedEvent = null;
    const connectionStartedAt = Date.now();
    const connectionId = ++this.connectionSequence;
    this.activeConnectionId = connectionId;
    const connectionAttemptId = createConnectionAttemptId();
    this.activeConnectionAttemptId = connectionAttemptId;
    const reconnectAttempt = this.reconnectAttempts;
    let ws: WebSocket;
    try {
      ws = new WebSocket(withConnectionAttemptId(this.url, connectionAttemptId));
    } catch (error) {
      this.log('connect:constructor-error', {
        reconnectAttempt,
        error: error instanceof Error ? error.message : String(error),
      });
      throw error;
    }
    this.ws = ws;
    this.log('connect:start', {
      reconnectAttempt,
      connectionAttemptId,
      pendingMessageCount: this.pendingMessages.length,
    });
    this.connectTimer = setTimeout(() => {
      if (this.ws !== ws || ws.readyState !== WebSocket.CONNECTING) {
        return;
      }
      this.log('connect:timeout', {
        connectionId,
        connectionAttemptId,
        connectTimeoutMs: this.options.connectTimeout,
        reconnectAttempt,
      });
      this.teardownSocket('connect_timeout');
    }, this.options.connectTimeout);

    ws.onopen = () => {
      this.log('connect:open', {
        connectionId,
        connectionAttemptId,
        socketReadyState: ws.readyState,
        isActiveSocket: this.ws === ws,
        reconnectAttempt,
        connectDurationMs: Date.now() - connectionStartedAt,
        pendingMessageCount: this.pendingMessages.length,
      });
      // 过期 socket（已被后续 connect 取代）的迟到 onopen：忽略，避免污染当前连接。
      if (this.ws !== ws) {
        return;
      }
      if (this.connectTimer) {
        clearTimeout(this.connectTimer);
        this.connectTimer = null;
      }
      this.reconnectAttempts = 0;
      this.resetHeartbeat();
      this.flushPendingMessages();
      this.callbacks.onOpen?.();
      this.callbacks.onConnectionChange?.(true);
    };

    ws.onmessage = (event: MessageEvent) => {
      // 过期 socket 的迟到消息：忽略，避免重置当前连接心跳、把过期事件灌入事件流。
      if (this.ws !== ws) {
        return;
      }
      this.resetHeartbeat();
      try {
        const parsed = JSON.parse(event.data) as ReactEvent;
        this.lastReceivedEvent = {
          type: parsed.type,
          seq: parsed.seq,
          runId: parsed.runId,
          sessionId: parsed.sessionId,
          receivedAt: Date.now(),
        };
        if (parsed.type === 'heartbeat' || parsed.type === 'done' || parsed.type === 'error' || parsed.type === 'cancelled') {
          this.log('message:received', {
            connectionId,
            connectionAttemptId,
            socketReadyState: ws.readyState,
            isActiveSocket: this.ws === ws,
            type: parsed.type,
            seq: parsed.seq,
            runId: parsed.runId,
            sessionId: parsed.sessionId,
            dataBytes: typeof event.data === 'string' ? event.data.length : null,
          });
        }
        this.callbacks.onEvent?.(parsed);
      } catch (error) {
        this.log('message:parse-error', {
          connectionId,
          connectionAttemptId,
          socketReadyState: ws.readyState,
          isActiveSocket: this.ws === ws,
          dataType: typeof event.data,
          dataBytes: typeof event.data === 'string' ? event.data.length : null,
          error: error instanceof Error ? error.message : String(error),
        });
        // 忽略非 JSON 消息
      }
    };

    ws.onclose = (event: CloseEvent) => {
      const trigger = this.disconnectTrigger === 'unknown' ? 'close_event' : this.disconnectTrigger;
      this.log('connect:close', {
        connectionId,
        connectionAttemptId,
        socketReadyState: ws.readyState,
        isActiveSocket: this.ws === ws,
        trigger,
        code: event.code,
        reason: event.reason,
        wasClean: event.wasClean,
        connectedDurationMs: Date.now() - connectionStartedAt,
        lastReceivedEvent: this.describeLastReceivedEvent(),
        pendingMessageCount: this.pendingMessages.length,
      });
      // 过期 socket 的迟到 onclose：只记录不透传断开，避免误伤当前连接与正在进行的 run。
      if (this.ws !== ws) {
        return;
      }
      this.callbacks.onClose?.(event);
      this.onDisconnected(trigger);
    };

    ws.onerror = (event: Event) => {
      this.log('connect:error', {
        connectionId,
        connectionAttemptId,
        socketReadyState: ws.readyState,
        isActiveSocket: this.ws === ws,
        eventType: event.type,
        connectedDurationMs: Date.now() - connectionStartedAt,
        lastReceivedEvent: this.describeLastReceivedEvent(),
      });
      // 过期 socket 的迟到 onerror：忽略。
      if (this.ws !== ws) {
        return;
      }
      this.callbacks.onError?.(event);
    };
  }

  /**
   * 发送消息
   *
   * 如果连接已就绪则立即发送，否则加入 pendingMessages 队列等待 flush。
   */
  send(msg: WsMessage): void {
    const data = JSON.stringify(msg);
    if (this.isConnected) {
      try {
        this.ws!.send(data);
      } catch (error) {
        this.log('message:send-error', {
          type: msg.type,
          runId: msg.runId,
          sessionId: msg.sessionId,
          dataBytes: data.length,
          error: error instanceof Error ? error.message : String(error),
        });
        throw error;
      }
      this.log('message:sent', {
        type: msg.type,
        runId: msg.runId,
        sessionId: msg.sessionId,
        dataBytes: data.length,
        queued: false,
      });
    } else {
      this.pendingMessages.push(data);
      this.log('message:queued', {
        type: msg.type,
        runId: msg.runId,
        sessionId: msg.sessionId,
        dataBytes: data.length,
        pendingMessageCount: this.pendingMessages.length,
      });
    }
  }

  /** 主动断开连接，清空缓冲队列，不触发自动重连 */
  disconnect(): void {
    this.intentionalClose = true;
    this.disconnectTrigger = 'intentional_disconnect';
    this.log('disconnect:intentional', {
      pendingMessageCount: this.pendingMessages.length,
    });
    this.clearTimers();
    this.pendingMessages = [];
    if (typeof window !== 'undefined') {
      window.removeEventListener('offline', this.handleOffline);
      window.removeEventListener('online', this.handleOnline);
      window.removeEventListener('pagehide', this.handlePageHide);
      window.removeEventListener('pageshow', this.handlePageShow);
      window.removeEventListener('beforeunload', this.handleBeforeUnload);
    }
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', this.handleVisibilityChange);
      document.removeEventListener('freeze', this.handlePageFreeze);
      document.removeEventListener('resume', this.handlePageResume);
    }
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
  }

  /** 清理尚未发送的消息，供上层放弃一次待发送 run 时使用。 */
  clearPendingMessages(): void {
    this.pendingMessages = [];
  }

  /**
   * 统一的"已断开"处理：清计时器 → 通知上层断开 → 按需安排重连。
   * onclose 和 offline 主动断开都汇聚到这里，保证行为一致。
   */
  private onDisconnected(trigger: DisconnectTrigger): void {
    this.clearTimers();
    this.log('connection:disconnected', {
      trigger,
      intentionalClose: this.intentionalClose,
      reconnectEnabled: this.options.reconnect,
      willReconnect: !this.intentionalClose && this.options.reconnect,
      reconnectAttempts: this.reconnectAttempts,
      lastReceivedEvent: this.describeLastReceivedEvent(),
    });
    this.callbacks.onConnectionChange?.(false);
    if (!this.intentionalClose && this.options.reconnect) {
      this.scheduleReconnect();
    }
  }

  /** 解绑 socket 上的回调，避免主动断开后 onclose/onerror 再触发一遍逻辑 */
  private detachSocket(ws: WebSocket): void {
    ws.onopen = null;
    ws.onmessage = null;
    ws.onerror = null;
    ws.onclose = null;
  }

  /** 重置心跳计时器：超时后主动关闭连接（触发 onClose → scheduleReconnect） */
  private resetHeartbeat(): void {
    if (this.heartbeatTimer) {
      clearTimeout(this.heartbeatTimer);
    }
    this.heartbeatTimer = setTimeout(() => {
      this.disconnectTrigger = 'heartbeat_timeout';
      this.log('heartbeat:timeout', {
        heartbeatTimeoutMs: this.options.heartbeatTimeout,
        lastReceivedEvent: this.describeLastReceivedEvent(),
      });
      this.ws?.close();
    }, this.options.heartbeatTimeout);
  }

  /** 指数退避重连：delay = min(1000 * 2^attempts, maxDelay)，叠加 ±20% 抖动。
   *  多会话架构下每个会话各持一条连接，网络整体抖动时若不抖动化退避，
   *  所有连接会在同一时刻同步重连（重连风暴）。 */
  private scheduleReconnect(): void {
    const baseDelay = Math.min(
      INITIAL_RECONNECT_DELAY * Math.pow(2, this.reconnectAttempts),
      this.options.reconnectMaxDelay,
    );
    const delay = Math.round(baseDelay * (0.8 + Math.random() * 0.4));
    this.reconnectAttempts++;
    const reconnectAttempt = this.reconnectAttempts;
    this.log('reconnect:scheduled', {
      reconnectAttempt,
      delayMs: delay,
    });
    this.callbacks.onReconnectAttempt?.(reconnectAttempt);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.log('reconnect:attempt', { reconnectAttempt });
      this.connect();
    }, delay);
  }

  /** 连接建立后 flush 缓存队列中的消息 */
  private flushPendingMessages(): void {
    const pending = this.pendingMessages.splice(0);
    if (pending.length > 0) {
      this.log('message:flush-start', { pendingMessageCount: pending.length });
    }
    for (const data of pending) {
      if (this.isConnected) {
        this.ws!.send(data);
      }
    }
  }

  private describeLastReceivedEvent(): Record<string, unknown> | null {
    if (!this.lastReceivedEvent) {
      return null;
    }
    return {
      type: this.lastReceivedEvent.type,
      seq: this.lastReceivedEvent.seq,
      runId: this.lastReceivedEvent.runId,
      sessionId: this.lastReceivedEvent.sessionId,
      receivedAgoMs: Date.now() - this.lastReceivedEvent.receivedAt,
    };
  }

  private clearTimers(): void {
    if (this.connectTimer) {
      clearTimeout(this.connectTimer);
      this.connectTimer = null;
    }
    if (this.heartbeatTimer) {
      clearTimeout(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.offlineGraceTimer) {
      clearTimeout(this.offlineGraceTimer);
      this.offlineGraceTimer = null;
    }
  }
}

function createConnectionAttemptId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') {
    const bytes = new Uint8Array(16);
    crypto.getRandomValues(bytes);
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

function withConnectionAttemptId(url: string, connectionAttemptId: string): string {
  const baseUrl = typeof window === 'undefined' ? undefined : window.location.href;
  const parsed = baseUrl ? new URL(url, baseUrl) : new URL(url);
  parsed.searchParams.set('connection_attempt_id', connectionAttemptId);
  return parsed.toString();
}
