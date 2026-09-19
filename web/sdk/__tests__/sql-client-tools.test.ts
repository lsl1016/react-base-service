import { describe, expect, it, vi } from 'vitest';
import type { ClientTool, ClientToolDiffEditorOptions, ClientToolUIContext } from '../tools/types';
import { clientToolUI } from '../ui/client-tool-ui';
// @ts-expect-error Playground integration is intentionally plain JavaScript.
import { registerSQLClientTools } from '../../react/sql-client-tools.js';

function editorView(options: ClientToolDiffEditorOptions) {
  return {
    ready: Promise.resolve(),
    getValue: () => options.value,
    setValue: vi.fn(),
    focus: vi.fn(),
    layout: vi.fn(),
    dispose: vi.fn(),
  };
}

function toolContext(
  toolUseId: string,
  input: Record<string, unknown>,
  create: ReturnType<typeof vi.fn>,
  state: Partial<ClientToolUIContext['toolCall']> = {},
): ClientToolUIContext {
  return {
    toolCall: {
      toolUseId,
      toolName: 'str_replace',
      input,
      status: 'waiting',
      executedBy: 'client',
      ...state,
    },
    ui: {
      codeEditor: { create: vi.fn() },
      diffEditor: { createPatch: clientToolUI.diffEditor.createPatch, create },
    },
  };
}

describe('playground SQL client tools', () => {
  it('diffs the last read_sql content against the cumulative replacement result', async () => {
    let tools: ClientTool[] = [];
    registerSQLClientTools({
      registerTools(value: ClientTool[]) {
        tools = value;
      },
    });
    const readTool = tools.find((tool) => tool.name === 'read_sql')!;
    const replaceTool = tools.find((tool) => tool.name === 'str_replace')!;
    const baseline = await readTool.execute({}, {
      toolUseId: 'read_1',
      toolName: 'read_sql',
    });
    const baselineSQL = String(baseline.content);

    const firstInput = {
      old_string: 'select  -- 订单日报示例',
      new_string: 'select  -- 订单日报示例（已修改）',
    };
    const firstExecution = replaceTool.execute(firstInput, {
      toolUseId: 'replace_1',
      toolName: 'str_replace',
    });
    let firstOptions: ClientToolDiffEditorOptions | undefined;
    const firstCreate = vi.fn((_container: HTMLElement, options: ClientToolDiffEditorOptions) => {
      firstOptions = options;
      options.onDiffChange?.({ addedLines: 1, removedLines: 1 });
      return editorView(options);
    });
    const firstHost = document.createElement('div');
    const firstView = replaceTool.ui!.type === 'custom'
      ? replaceTool.ui.renderer.mount(firstHost, toolContext('replace_1', firstInput, firstCreate))
      : undefined;

    await vi.waitFor(() => expect(firstCreate).toHaveBeenCalledTimes(1));
    expect(firstOptions?.fileName).toBe('current.sql');
    expect(firstOptions?.value).toContain('-select  -- 订单日报示例');
    expect(firstOptions?.value).toContain('+select  -- 订单日报示例（已修改）');
    expect(firstHost.textContent).toContain('current.sql');
    expect(firstHost.textContent).toContain('+1');
    expect(firstHost.textContent).toContain('-1');

    await vi.waitFor(() => expect(firstHost.querySelector<HTMLButtonElement>('.rp-sql-diff-accept')?.disabled).toBe(false));
    firstHost.querySelector<HTMLButtonElement>('.rp-sql-diff-accept')!.click();
    const firstResult = await firstExecution;
    expect(firstResult).toMatchObject({
      content: expect.stringContaining('SQL 修改已接受'),
      meta: {
        version: 1,
        kind: 'diff',
        fileName: 'current.sql',
        language: 'sql',
        original: baselineSQL,
        modified: baselineSQL.replace(firstInput.old_string, firstInput.new_string),
      },
    });
    firstView?.unmount();

    let replayOptions: ClientToolDiffEditorOptions | undefined;
    const replayCreate = vi.fn((_container: HTMLElement, options: ClientToolDiffEditorOptions) => {
      replayOptions = options;
      return editorView(options);
    });
    const replayHost = document.createElement('div');
    const replayView = replaceTool.ui!.type === 'custom'
      ? replaceTool.ui.renderer.mount(
          replayHost,
          toolContext('replace_1', firstInput, replayCreate, {
            status: 'done',
            result: String(firstResult.content),
            meta: firstResult.meta,
          }),
        )
      : undefined;

    await vi.waitFor(() => expect(replayCreate).toHaveBeenCalledTimes(1));
    expect(replayOptions?.value).toContain('-select  -- 订单日报示例');
    expect(replayOptions?.value).toContain('+select  -- 订单日报示例（已修改）');
    expect(replayHost.textContent).toContain('已接受');
    replayView?.unmount();

    const secondInput = {
      old_string: "where o.dt = '{@date}'",
      new_string: "where o.dt >= '{@date-7}'",
    };
    const secondExecution = replaceTool.execute(secondInput, {
      toolUseId: 'replace_2',
      toolName: 'str_replace',
    });
    let secondOptions: ClientToolDiffEditorOptions | undefined;
    const secondCreate = vi.fn((_container: HTMLElement, options: ClientToolDiffEditorOptions) => {
      secondOptions = options;
      return editorView(options);
    });
    const secondHost = document.createElement('div');
    const secondView = replaceTool.ui!.type === 'custom'
      ? replaceTool.ui.renderer.mount(secondHost, toolContext('replace_2', secondInput, secondCreate))
      : undefined;

    await vi.waitFor(() => expect(secondCreate).toHaveBeenCalledTimes(1));
    expect(secondOptions?.value).toContain('+select  -- 订单日报示例（已修改）');
    expect(secondOptions?.value).toMatch(/-\s*where o\.dt = '\{@date\}'/);
    expect(secondOptions?.value).toMatch(/\+\s*where o\.dt >= '\{@date-7\}'/);

    secondHost.querySelector<HTMLButtonElement>('.rp-sql-diff-reject')!.click();
    await expect(secondExecution).resolves.toMatchObject({ isError: true });
    secondView?.unmount();
  });
});
