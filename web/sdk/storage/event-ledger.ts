/**
 * 会话事件账本
 *
 * IndexedDB 中的事件事实与 /session/events 保持同形：每个 session 保存一份原始 events[]。
 * - 服务端 /session/events 返回后直接覆盖当前 session 的 events[]
 * - WS 直播细块不直接入账；只有聚合完成后的 /session/events 风格事件才追加
 * - 单个 event 不增加本地私有字段
 */
import type { ReactEvent } from '../protocol/types';
import type { HistoryEvent, SessionItem, SessionListParams } from '../session/types';

export type CachedReactEvent = HistoryEvent;

/** 会话元数据（存储在 sessions 表，对齐服务端 ReactSessionItem，并补充本地统计） */
export interface SessionMeta {
  sessionId: string;
  appKey: string;
  routeValues: string[];
  type: string;
  title: string;
  lastRunId: string;
  lastMessage: string;
  state: string;
  /** 本地 events[] 的最大 seq */
  lastSeq: number;
  /** 本地 events[] 长度 */
  eventCount: number;
  /** 服务端时间字符串优先；本地生成时使用 ISO 字符串 */
  createdAt: string;
  /** 服务端时间字符串优先；本地生成时使用 ISO 字符串 */
  updatedAt: string;
}

interface SessionEventsRecord {
  sessionId: string;
  events: HistoryEvent[];
}

const DB_NAME = 'agent-sdk-ledger';
const DB_VERSION = 3;
const SESSIONS_STORE = 'sessions';
const EVENTS_STORE = 'events';
const SESSION_EVENTS_STORE = 'session_events';

function nowString(): string {
  return new Date().toISOString();
}

function toTime(value?: string): number {
  if (!value) return 0;
  const normalized = value.includes('T') ? value : value.replace(' ', 'T');
  const time = new Date(normalized).getTime();
  return Number.isFinite(time) ? time : 0;
}

function sameRouteValues(a?: string[], b?: string[]): boolean {
  return JSON.stringify(a ?? []) === JSON.stringify(b ?? []);
}

function normalizeSessionMeta(
  meta: Partial<SessionMeta> & { sessionId: string },
  existing?: SessionMeta,
): SessionMeta {
  const now = nowString();
  return {
    sessionId: meta.sessionId,
    appKey: meta.appKey ?? existing?.appKey ?? '',
    routeValues: meta.routeValues ?? existing?.routeValues ?? [],
    type: meta.type ?? existing?.type ?? 'chat',
    title: meta.title ?? existing?.title ?? '',
    lastRunId: meta.lastRunId ?? existing?.lastRunId ?? '',
    lastMessage: meta.lastMessage ?? existing?.lastMessage ?? '',
    state: meta.state ?? existing?.state ?? 'active',
    lastSeq: meta.lastSeq ?? existing?.lastSeq ?? 0,
    eventCount: meta.eventCount ?? existing?.eventCount ?? 0,
    createdAt: meta.createdAt ?? existing?.createdAt ?? now,
    updatedAt: meta.updatedAt ?? now,
  };
}

function sessionItemToMeta(item: SessionItem): SessionMeta {
  return normalizeSessionMeta({
    sessionId: item.sessionId,
    appKey: item.appKey,
    routeValues: item.routeValues ?? [],
    type: item.type,
    title: item.title,
    lastRunId: item.lastRunId,
    lastMessage: item.lastMessage,
    state: item.state,
    createdAt: item.createdAt,
    updatedAt: item.updatedAt,
  });
}

function toHistoryEvent(
  event: ReactEvent | HistoryEvent,
  sessionId: string,
  seq: number,
): HistoryEvent {
  const source = event as HistoryEvent;
  return {
    type: event.type,
    seq,
    runId: event.runId,
    sessionId: event.sessionId ?? sessionId,
    stepIndex: event.stepIndex,
    // 多 Agent 事件归属必须随事件持久化，否则本地 ledger 恢复的会话丢失
    // agentPath 徽章与泳道分组（服务端 HTTP 历史带该字段，ledger 缓存形态曾漏掉）。
    agentPath: source.agentPath,
    payload: event.payload,
    createdAt: source.createdAt,
  };
}

function lastEventTime(events: HistoryEvent[]): string | undefined {
  for (let index = events.length - 1; index >= 0; index -= 1) {
    if (events[index].createdAt) return events[index].createdAt;
  }
  return undefined;
}

export class EventLedger {
  private db: IDBDatabase | null = null;
  private openPromise: Promise<IDBDatabase> | null = null;

  /** 打开数据库连接（幂等，多次调用只打开一次） */
  private async open(): Promise<IDBDatabase> {
    if (this.db) return this.db;
    if (this.openPromise) return this.openPromise;

    this.openPromise = new Promise<IDBDatabase>((resolve, reject) => {
      const request = indexedDB.open(DB_NAME, DB_VERSION);

      request.onupgradeneeded = () => {
        const db = request.result;

        if (!db.objectStoreNames.contains(SESSIONS_STORE)) {
          db.createObjectStore(SESSIONS_STORE, { keyPath: 'sessionId' });
        }

        if (db.objectStoreNames.contains(EVENTS_STORE)) {
          db.deleteObjectStore(EVENTS_STORE);
        }

        if (!db.objectStoreNames.contains(SESSION_EVENTS_STORE)) {
          db.createObjectStore(SESSION_EVENTS_STORE, { keyPath: 'sessionId' });
        }
      };

      request.onsuccess = () => {
        this.db = request.result;
        resolve(this.db);
      };

      request.onerror = () => {
        reject(request.error);
      };
    });

    return this.openPromise;
  }

  // ─── 事件写入 ─────────────────────────────────────────

  /** 追加一条已完整的本地事件；不会持久化 WS 细块 */
  append(event: ReactEvent | HistoryEvent): void {
    this.appendEvents([event]);
  }

  /** 追加一组已完整的本地事件，seq 会按当前 events[] 长度重新生成 */
  appendEvents(events: Array<ReactEvent | HistoryEvent>): void {
    const candidates = events.filter((event) => Boolean(event.sessionId));
    if (candidates.length === 0) return;

    this.open().then((db) => {
      const tx = db.transaction([SESSION_EVENTS_STORE, SESSIONS_STORE], 'readwrite');
      const eventStore = tx.objectStore(SESSION_EVENTS_STORE);
      const sessionStore = tx.objectStore(SESSIONS_STORE);
      const sessionId = candidates[0].sessionId!;

      const getEventsReq = eventStore.get(sessionId);
      getEventsReq.onsuccess = () => {
        const existingRecord = getEventsReq.result as SessionEventsRecord | undefined;
        const existingEvents = existingRecord?.events ?? [];
        const appended = candidates.map((event, index) => {
          return toHistoryEvent(event, sessionId, existingEvents.length + index + 1);
        });
        const nextEvents = [...existingEvents, ...appended];
        eventStore.put({ sessionId, events: nextEvents } satisfies SessionEventsRecord);

        const getSessionReq = sessionStore.get(sessionId);
        getSessionReq.onsuccess = () => {
          const existing = getSessionReq.result as SessionMeta | undefined;
          sessionStore.put(normalizeSessionMeta({
            sessionId,
            lastSeq: nextEvents[nextEvents.length - 1]?.seq ?? 0,
            eventCount: nextEvents.length,
            updatedAt: lastEventTime(nextEvents) ?? nowString(),
          }, existing));
        };
      };

      tx.onerror = () => {
        console.warn('[EventLedger] append failed:', tx.error);
      };
    }).catch((err) => {
      console.warn('[EventLedger] open failed:', err);
    });
  }

  /** 兼容旧调用；服务端事件请使用 replaceSessionEvents 直接覆盖 */
  appendServerEvent(event: HistoryEvent): void {
    this.append(event);
  }

  /** 用服务端完整历史事件直接覆盖当前 session 的本地 events[] */
  async replaceSessionEvents(
    sessionId: string,
    events: Array<HistoryEvent | ReactEvent>,
    meta?: Partial<SessionMeta> & { sessionId?: string },
  ): Promise<HistoryEvent[]> {
    const historyEvents = events.map((event) => {
      return toHistoryEvent({ ...event, sessionId: event.sessionId ?? sessionId }, sessionId, event.seq);
    });

    const db = await this.open();
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction([SESSION_EVENTS_STORE, SESSIONS_STORE], 'readwrite');
      tx.objectStore(SESSION_EVENTS_STORE).put({ sessionId, events: historyEvents } satisfies SessionEventsRecord);

      const sessionStore = tx.objectStore(SESSIONS_STORE);
      const getReq = sessionStore.get(sessionId);
      getReq.onsuccess = () => {
        const existing = getReq.result as SessionMeta | undefined;
        sessionStore.put(normalizeSessionMeta({
          ...(meta ?? {}),
          sessionId,
          lastSeq: historyEvents[historyEvents.length - 1]?.seq ?? 0,
          eventCount: historyEvents.length,
          updatedAt: meta?.updatedAt ?? lastEventTime(historyEvents) ?? nowString(),
        }, existing));
      };

      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
    });

    return historyEvents;
  }

  // ─── 事件读取 ─────────────────────────────────────────

  /** 读取指定会话的全部事件，保持 events[] 原始顺序 */
  async getEvents(sessionId: string, _source?: 'live' | 'history'): Promise<HistoryEvent[]> {
    const db = await this.open();

    return new Promise((resolve, reject) => {
      const tx = db.transaction(SESSION_EVENTS_STORE, 'readonly');
      const request = tx.objectStore(SESSION_EVENTS_STORE).get(sessionId);
      request.onsuccess = () => {
        const record = request.result as SessionEventsRecord | undefined;
        resolve(record?.events ? [...record.events] : []);
      };
      request.onerror = () => reject(request.error);
    });
  }

  /** 获取指定会话的本地最大 seq，没有缓存则返回 null */
  async getLastSeq(sessionId: string): Promise<number | null> {
    const meta = await this.getSession(sessionId);
    return meta?.lastSeq ?? null;
  }

  // ─── Session 管理 ─────────────────────────────────────

  /** 创建或更新会话元数据 */
  async upsertSession(meta: Partial<SessionMeta> & { sessionId: string }): Promise<void> {
    const db = await this.open();

    return new Promise((resolve, reject) => {
      const tx = db.transaction(SESSIONS_STORE, 'readwrite');
      const store = tx.objectStore(SESSIONS_STORE);

      const getReq = store.get(meta.sessionId);
      getReq.onsuccess = () => {
        const existing = getReq.result as SessionMeta | undefined;
        store.put(normalizeSessionMeta(meta, existing));
      };

      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
    });
  }

  /** 批量同步服务端会话摘要 */
  async upsertServerSessions(sessions: SessionItem[]): Promise<void> {
    await Promise.all(sessions.map((session) => this.upsertSession(sessionItemToMeta(session))));
  }

  /** 获取指定会话的元数据 */
  async getSession(sessionId: string): Promise<SessionMeta | null> {
    const db = await this.open();

    return new Promise((resolve, reject) => {
      const tx = db.transaction(SESSIONS_STORE, 'readonly');
      const request = tx.objectStore(SESSIONS_STORE).get(sessionId);
      request.onsuccess = () => resolve(request.result ?? null);
      request.onerror = () => reject(request.error);
    });
  }

  /** 获取指定 appKey 下最近的会话 */
  async getLatestSession(appKey: string, routeValues?: string[]): Promise<SessionMeta | null> {
    const sessions = await this.listSessions(appKey, routeValues);
    return sessions[0] ?? null;
  }

  /** 获取指定 appKey 下的所有会话，按 updatedAt 倒序 */
  async listSessions(
    appKey: string,
    routeValues?: string[],
    filters?: Pick<SessionListParams, 'type' | 'keyword'>,
  ): Promise<SessionMeta[]> {
    const db = await this.open();

    return new Promise((resolve, reject) => {
      const tx = db.transaction(SESSIONS_STORE, 'readonly');
      const request = tx.objectStore(SESSIONS_STORE).getAll();

      request.onsuccess = () => {
        const all = (request.result || []) as SessionMeta[];
        const keyword = filters?.keyword?.trim();
        const matched = all.filter((session) => {
          if (session.appKey !== appKey) return false;
          if (routeValues && routeValues.length > 0 && !sameRouteValues(session.routeValues, routeValues)) {
            return false;
          }
          if (filters?.type && session.type !== filters.type) return false;
          if (keyword && !session.title.includes(keyword) && !session.lastMessage.includes(keyword)) return false;
          return session.state !== 'deleted';
        });
        matched.sort((a, b) => toTime(b.updatedAt) - toTime(a.updatedAt));
        resolve(matched);
      };

      request.onerror = () => reject(request.error);
    });
  }

  /** 清除指定会话的全部事件和元数据 */
  async clearSession(sessionId: string): Promise<void> {
    const db = await this.open();

    return new Promise((resolve, reject) => {
      const tx = db.transaction([SESSION_EVENTS_STORE, SESSIONS_STORE], 'readwrite');
      tx.objectStore(SESSION_EVENTS_STORE).delete(sessionId);
      tx.objectStore(SESSIONS_STORE).delete(sessionId);
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
    });
  }

  /** 清除指定 appKey 和 routeValues 作用域下的全部本地会话 */
  async clearSessions(appKey: string, routeValues?: string[]): Promise<void> {
    const sessions = await this.listSessions(appKey, routeValues);
    await Promise.all(sessions.map((session) => this.clearSession(session.sessionId)));
  }

  /** 关闭数据库连接 */
  async close(): Promise<void> {
    if (this.db) {
      this.db.close();
      this.db = null;
      this.openPromise = null;
    }
  }
}