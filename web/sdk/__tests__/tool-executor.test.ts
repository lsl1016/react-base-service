import { describe, expect, it, vi } from 'vitest';
import type { ClientToolUseStartPayload, WsMessage } from '../protocol/types';
import { ClientToolExecutor } from '../tools/executor';
import { ClientToolRegistry } from '../tools/registry';
import type { ClientTool } from '../tools/types';

describe('ClientToolRegistry', () => {
  it('should register and get tools', () => {
    const registry = new ClientToolRegistry();
    const tool: ClientTool = {
      name: 'TestTool',
      execute: async () => ({ content: 'ok' }),
    };

    registry.register(tool);
    expect(registry.has('TestTool')).toBe(true);
    expect(registry.get('TestTool')).toBe(tool);
    expect(registry.size).toBe(1);
  });

  it('should return undefined for unregistered tools', () => {
    const registry = new ClientToolRegistry();
    expect(registry.has('Unknown')).toBe(false);
    expect(registry.get('Unknown')).toBeUndefined();
  });

  it('should registerMany tools', () => {
    const registry = new ClientToolRegistry();
    const tools: ClientTool[] = [
      { name: 'A', execute: async () => ({ content: '' }) },
      { name: 'B', execute: async () => ({ content: '' }) },
    ];

    registry.registerMany(tools);
    expect(registry.size).toBe(2);
    expect(registry.has('A')).toBe(true);
    expect(registry.has('B')).toBe(true);
  });

  it('should getAll tools', () => {
    const registry = new ClientToolRegistry();
    registry.register({ name: 'X', execute: async () => ({ content: '' }) });
    registry.register({ name: 'Y', execute: async () => ({ content: '' }) });

    const all = registry.getAll();
    expect(all).toHaveLength(2);
    expect(all.map((t) => t.name).sort()).toEqual(['X', 'Y']);
  });
  it('should register aliases without duplicating tool list', () => {
    const registry = new ClientToolRegistry();
    const tool: ClientTool = {
      name: 'MealTool',
      aliases: ['tool_cc878eae37504ccfae91c66af1bc4847'],
      execute: async () => ({ content: 'ok' }),
    };

    registry.register(tool);
    expect(registry.has('MealTool')).toBe(true);
    expect(registry.has('tool_cc878eae37504ccfae91c66af1bc4847')).toBe(true);
    expect(registry.get('tool_cc878eae37504ccfae91c66af1bc4847')).toBe(tool);
    expect(registry.size).toBe(1);
    expect(registry.getAll()).toEqual([tool]);
  });
});

describe('ClientToolExecutor', () => {
  it('should execute registered tool and send result', async () => {
    const registry = new ClientToolRegistry();
    const sentMessages: WsMessage[] = [];
    const sendFn = vi.fn((msg: WsMessage) => sentMessages.push(msg));

    const tool: ClientTool = {
      name: 'MutateChart',
      execute: async (input) => ({
        content: `Chart ${input.type} created`,
        meta: { chartType: String(input.type), revision: 2 },
      }),
    };

    registry.register(tool);
    const executor = new ClientToolExecutor(registry, sendFn);

    const payload: ClientToolUseStartPayload = {
      toolUseId: 'tool_1',
      toolName: 'MutateChart',
      toolInput: { type: 'bar' },
      status: 'waiting',
    };

    await executor.handleClientToolUseStart(payload);

    // Wait for async execution to complete
    await new Promise((r) => setTimeout(r, 10));

    expect(sendFn).toHaveBeenCalledTimes(1);
    expect(sentMessages[0].type).toBe('client_tool_use_end');
    expect((sentMessages[0].payload as any).toolOutputs[0].content).toBe('Chart bar created');
    expect((sentMessages[0].payload as any).toolOutputs[0].meta).toEqual({ chartType: 'bar', revision: 2 });
    expect((sentMessages[0].payload as any).toolOutputs[0].isError).toBeUndefined();
  });

  it('should preserve Plan execution attribution in client tool result', async () => {
    const registry = new ClientToolRegistry();
    const sentMessages: WsMessage[] = [];
    const sendFn = vi.fn((msg: WsMessage) => sentMessages.push(msg));

    registry.register({
      name: 'PlanClientTool',
      execute: async () => ({ content: 'plan-client-ok' }),
    });

    const executor = new ClientToolExecutor(registry, sendFn);
    await executor.handleClientToolUseStart({
      toolUseId: 'plan_tool_use_1',
      toolName: 'PlanClientTool',
      toolInput: {},
      status: 'waiting',
      planExecutionId: 'plan_exec_1',
      stepAttemptId: 'plan_attempt_1',
    });

    expect(sendFn).toHaveBeenCalledTimes(1);
    expect(sentMessages[0].type).toBe('client_tool_use_end');
    expect((sentMessages[0].payload as any).planExecutionId).toBe('plan_exec_1');
    expect((sentMessages[0].payload as any).stepAttemptId).toBe('plan_attempt_1');
    expect((sentMessages[0].payload as any).toolOutputs[0].content).toBe('plan-client-ok');
  });

  it('should execute tool matched by frontend hint alias', async () => {
    const registry = new ClientToolRegistry();
    const sentMessages: WsMessage[] = [];
    const sendFn = vi.fn((msg: WsMessage) => sentMessages.push(msg));

    const tool: ClientTool = {
      name: 'MealTool',
      aliases: ['tool_cc878eae37504ccfae91c66af1bc4847'],
      execute: async (_input, context) => ({
        content: `executed-${context.toolName}-${context.description}`,
      }),
    };

    registry.register(tool);
    const executor = new ClientToolExecutor(registry, sendFn);

    await executor.handleClientToolUseStart({
      toolUseId: 'call_d64bac195d6241df91f3681c',
      toolName: 'execute_tool',
      toolInput: {},
      description: '请你吃饭',
      frontendHint: 'tool_cc878eae37504ccfae91c66af1bc4847',
      status: 'waiting',
    });
    await new Promise((r) => setTimeout(r, 10));

    expect(sendFn).toHaveBeenCalledTimes(1);
    const output = (sentMessages[0].payload as any).toolOutputs[0];
    expect(output.isError).toBeUndefined();
    expect(output.content).toBe('executed-execute_tool-请你吃饭');
  });

  it('should execute tool matched by server tool id', async () => {
    const registry = new ClientToolRegistry();
    const sentMessages: WsMessage[] = [];
    const sendFn = vi.fn((msg: WsMessage) => sentMessages.push(msg));

    registry.register({
      name: 'tool_cc878eae37504ccfae91c66af1bc4847',
      execute: async () => ({ content: 'meal-ok' }),
    });

    const executor = new ClientToolExecutor(registry, sendFn);
    await executor.handleClientToolUseStart({
      toolUseId: 'call_d64bac195d6241df91f3681c',
      toolName: 'tool_cc878eae37504ccfae91c66af1bc4847',
      toolInput: {},
      description: '请你吃饭',
      frontendHint: 'tool_cc878eae37504ccfae91c66af1bc4847',
      status: 'waiting',
    });
    await new Promise((r) => setTimeout(r, 10));

    const output = (sentMessages[0].payload as any).toolOutputs[0];
    expect(output.content).toBe('meal-ok');
    expect(output.isError).toBeUndefined();
  });

  it('should handle unregistered tool with error', async () => {
    const registry = new ClientToolRegistry();
    const sentMessages: WsMessage[] = [];
    const sendFn = vi.fn((msg: WsMessage) => sentMessages.push(msg));

    const executor = new ClientToolExecutor(registry, sendFn);

    const payload: ClientToolUseStartPayload = {
      toolUseId: 'tool_1',
      toolName: 'UnknownTool',
      toolInput: {},
      status: 'waiting',
    };

    await executor.handleClientToolUseStart(payload);
    await new Promise((r) => setTimeout(r, 10));

    expect(sendFn).toHaveBeenCalledTimes(1);
    const output = (sentMessages[0].payload as any).toolOutputs[0];
    expect(output.isError).toBe(true);
    expect(output.content).toBe('Tool not registered: UnknownTool');
  });

  it('should pass structured JSON content through to the backend unchanged', async () => {
    const registry = new ClientToolRegistry();
    const sentMessages: WsMessage[] = [];
    const sendFn = vi.fn((msg: WsMessage) => sentMessages.push(msg));

    const tool: ClientTool = {
      name: 'JsonTool',
      execute: async () => ({
        content: {
          ok: true,
          rows: [{ id: 1, name: 'Alice' }],
        },
      }),
    };

    registry.register(tool);
    const executor = new ClientToolExecutor(registry, sendFn);

    await executor.handleClientToolUseStart({
      toolUseId: 'tool_json_1',
      toolName: 'JsonTool',
      toolInput: {},
      status: 'waiting',
    });
    await new Promise((r) => setTimeout(r, 10));

    const output = (sentMessages[0].payload as any).toolOutputs[0];
    expect(output.content).toEqual({
      ok: true,
      rows: [{ id: 1, name: 'Alice' }],
    });
  });

  it('should handle tool execution error', async () => {
    const registry = new ClientToolRegistry();
    const sentMessages: WsMessage[] = [];
    const sendFn = vi.fn((msg: WsMessage) => sentMessages.push(msg));

    const tool: ClientTool = {
      name: 'FailingTool',
      execute: async () => {
        throw new Error('Something went wrong');
      },
    };

    registry.register(tool);
    const executor = new ClientToolExecutor(registry, sendFn);

    await executor.handleClientToolUseStart({
      toolUseId: 'tool_1',
      toolName: 'FailingTool',
      toolInput: {},
      status: 'waiting',
    });
    await new Promise((r) => setTimeout(r, 10));

    const output = (sentMessages[0].payload as any).toolOutputs[0];
    expect(output.isError).toBe(true);
    expect(output.content).toBe('Something went wrong');
  });

  it('should handle tool returning isError', async () => {
    const registry = new ClientToolRegistry();
    const sentMessages: WsMessage[] = [];
    const sendFn = vi.fn((msg: WsMessage) => sentMessages.push(msg));

    const tool: ClientTool = {
      name: 'SoftError',
      execute: async () => ({
        content: 'Validation failed',
        isError: true,
      }),
    };

    registry.register(tool);
    const executor = new ClientToolExecutor(registry, sendFn);

    await executor.handleClientToolUseStart({
      toolUseId: 'tool_1',
      toolName: 'SoftError',
      toolInput: {},
      status: 'waiting',
    });
    await new Promise((r) => setTimeout(r, 10));

    const output = (sentMessages[0].payload as any).toolOutputs[0];
    expect(output.isError).toBe(true);
    expect(output.content).toBe('Validation failed');
  });

  it('should handle concurrent tool executions', async () => {
    const registry = new ClientToolRegistry();
    const sentMessages: WsMessage[] = [];
    const sendFn = vi.fn((msg: WsMessage) => sentMessages.push(msg));

    const tool: ClientTool = {
      name: 'SlowTool',
      execute: async (input) => {
        await new Promise((r) => setTimeout(r, 20));
        return { content: `done-${input.id}` };
      },
    };

    registry.register(tool);
    const executor = new ClientToolExecutor(registry, sendFn);

    // Fire 3 concurrent executions
    const p1 = executor.handleClientToolUseStart({
      toolUseId: 't1',
      toolName: 'SlowTool',
      toolInput: { id: 1 },
      status: 'waiting',
    });
    const p2 = executor.handleClientToolUseStart({
      toolUseId: 't2',
      toolName: 'SlowTool',
      toolInput: { id: 2 },
      status: 'waiting',
    });
    const p3 = executor.handleClientToolUseStart({
      toolUseId: 't3',
      toolName: 'SlowTool',
      toolInput: { id: 3 },
      status: 'waiting',
    });

    expect(executor.activeCount).toBe(3);

    await Promise.all([p1, p2, p3]);

    expect(sendFn).toHaveBeenCalledTimes(3);
    expect(executor.activeCount).toBe(0);
  });
});
