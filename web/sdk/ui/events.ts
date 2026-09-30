import type { AgentInputPart } from './editor/types';
import type { AskQuestionAnswerContent } from '../protocol/types';

export interface NextButtonAction {
  sourceRunId: string;
  sourceStepIndex: number;
  buttonText: string;
  buttonIndex: number;
}

export type AgentUICopyMethod = 'keyboard' | 'contextmenu';

export type AgentUIEvent =
  | {
      type: 'input_change';
      sessionId: string | null;
      content: string;
      displayParts: AgentInputPart[];
      isComposing: boolean;
    }
  | {
      type: 'new_session';
      previousSessionId: string | null;
      accepted: boolean;
    }
  | {
      type: 'session_select';
      previousSessionId: string | null;
      sessionId: string;
    }
  | {
      type: 'session_delete';
      previousSessionId: string | null;
      sessionId: string;
    }
  | {
      type: 'user_message_copy';
      sessionId: string | null;
      runId: string;
      content: string;
      displayParts?: AgentInputPart[];
    }
  | {
      type: 'code_copy';
      sessionId: string | null;
      runId: string;
      stepIndex: number;
      language: string;
      content: string;
      source: 'content';
    }
  | {
      type: 'code_selection_copy';
      sessionId: string | null;
      runId: string;
      stepIndex: number;
      language: string;
      content: string;
      copyMethod: AgentUICopyMethod;
    }
  | {
      type: 'attachment_upload_success';
      sessionId: string | null;
      fileId: string;
      fileName: string;
      size: number;
      mimeType?: string;
    }
  | {
      type: 'feedback_open';
      sessionId: string | null;
      runId: string;
      feedbackType: 'problem';
    }
  | {
      type: 'tool_confirm_submit';
      sessionId: string | null;
      runId: string;
      toolUseId: string;
      approved: boolean;
    }
  | {
      type: 'ask_question_submit';
      sessionId: string | null;
      runId: string;
      toolUseId: string;
      content: AskQuestionAnswerContent;
      skipped: boolean;
      questionCount: number;
    }
  | {
      type: 'quick_insert_select';
      sessionId: string | null;
      itemId: string;
      label: string;
      group: string;
      tag: string;
    }
  | ({
      type: 'next_button_click';
      sessionId: string | null;
    } & NextButtonAction);

export type AgentUIEventHandler = (event: AgentUIEvent) => void;
