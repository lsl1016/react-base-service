import type {
    ContentDeltaPayload,
    ContentEndPayload,
    ReactEvent,
    ThoughtDeltaPayload,
    ThoughtEndPayload,
} from '../protocol/types';

interface SegmentDraft {
  start?: ReactEvent;
  text: string;
}

function segmentKey(event: ReactEvent): string {
  return `${event.runId ?? ''}:${event.stepIndex ?? 0}`;
}

function cloneEvent(event: ReactEvent): ReactEvent {
  return {
    type: event.type,
    seq: event.seq,
    runId: event.runId,
    sessionId: event.sessionId,
    stepIndex: event.stepIndex,
    agentPath: event.agentPath,
    payload: event.payload ? { ...event.payload } : undefined,
  };
}

function eventWithPayload(event: ReactEvent, payload: Record<string, unknown>): ReactEvent {
  return {
    ...cloneEvent(event),
    payload,
  };
}

/**
 * 将 WS 细块聚合成 /session/events 风格的完整事件段。
 * 原始 WS 事件仍然实时喂给 EventReducer，这里只负责产出可持久化 events[]。
 */
export class SessionEventAssembler {
  private thoughtDrafts = new Map<string, SegmentDraft>();
  private contentDrafts = new Map<string, SegmentDraft>();

  reset(): void {
    this.thoughtDrafts.clear();
    this.contentDrafts.clear();
  }

  consume(event: ReactEvent): ReactEvent[] {
    switch (event.type) {
      case 'run':
        return [cloneEvent(event)];

      case 'thought_start':
        this.thoughtDrafts.set(segmentKey(event), { start: cloneEvent(event), text: '' });
        return [];

      case 'thought_delta': {
        const key = segmentKey(event);
        const draft = this.thoughtDrafts.get(key) ?? { text: '' };
        const payload = event.payload as unknown as ThoughtDeltaPayload;
        draft.text += payload?.contentDelta ?? '';
        this.thoughtDrafts.set(key, draft);
        return [];
      }

      case 'thought_end': {
        const key = segmentKey(event);
        const draft = this.thoughtDrafts.get(key) ?? { text: '' };
        const payload = event.payload as unknown as ThoughtEndPayload;
        const content = payload?.content ?? draft.text;
        this.thoughtDrafts.delete(key);
        return [
          draft.start ?? { ...cloneEvent(event), type: 'thought_start', payload: undefined },
          eventWithPayload(event, { contentDelta: content }),
          eventWithPayload(event, { content }),
        ].map((item, index) => ({
          ...item,
          type: index === 0 ? 'thought_start' : index === 1 ? 'thought_delta' : 'thought_end',
        }));
      }

      case 'content_start':
        this.contentDrafts.set(segmentKey(event), { start: cloneEvent(event), text: '' });
        return [];

      case 'content_delta': {
        const key = segmentKey(event);
        const draft = this.contentDrafts.get(key) ?? { text: '' };
        const payload = event.payload as unknown as ContentDeltaPayload;
        draft.text += payload?.contentDelta ?? '';
        this.contentDrafts.set(key, draft);
        return [];
      }

      case 'content_end': {
        const key = segmentKey(event);
        const draft = this.contentDrafts.get(key) ?? { text: '' };
        const payload = event.payload as unknown as ContentEndPayload;
        const content = payload?.content ?? draft.text;
        this.contentDrafts.delete(key);
        return [
          draft.start ?? { ...cloneEvent(event), type: 'content_start', payload: undefined },
          eventWithPayload(event, { contentDelta: content }),
          eventWithPayload(event, { content }),
        ].map((item, index) => ({
          ...item,
          type: index === 0 ? 'content_start' : index === 1 ? 'content_delta' : 'content_end',
        }));
      }

      case 'model_fallback':
        if ((event.payload as { resetCurrentOutput?: boolean } | undefined)?.resetCurrentOutput) {
          this.thoughtDrafts.delete(segmentKey(event));
          this.contentDrafts.delete(segmentKey(event));
        }
        return [cloneEvent(event)];

      case 'tool_use_start':
      case 'tool_confirm_request':
      case 'client_tool_use_start':
      case 'tool_use_end':
      case 'client_tool_use_end':
      case 'todo_update':
      case 'plan_view_update':
      case 'plan_step_event':
      case 'compact_end':
      // Steering 准入/注入/结算与通知吸收事件（S1-S3/Q1-A1）：原样透传，不参与流式聚合。
      case 'steer_guided':
      case 'steer_queued':
      case 'steer_rejected':
      case 'steer_drained':
      case 'steer_delivery_changed':
      case 'steer_discarded':
      case 'notice_drained':
      case 'done':
      case 'error':
      case 'cancelled':
        return [cloneEvent(event)];

      case 'compact_start':
      case 'heartbeat':
        return [];
    }
  }
}