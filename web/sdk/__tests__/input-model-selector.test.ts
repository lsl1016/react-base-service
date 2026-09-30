import { createComponent } from 'solid-js';
import { render } from 'solid-js/web';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { InputArea } from '../ui/components/InputArea';

const models = [
  { modelKey: 'DeepSeek', modelVersion: 'deepseek-v4-pro', displayName: 'DeepSeek V4 Pro' },
  { modelKey: '通义千问', modelVersion: 'qwen3.8-max', displayName: '通义千问 Qwen3.8 Max' },
];

afterEach(() => {
  document.body.innerHTML = '';
});

describe('InputArea model selector', () => {
  it('renders before context usage and reports model changes', () => {
    const host = document.createElement('div');
    const onModelChange = vi.fn();
    const dispose = render(
      () => createComponent(InputArea, {
        isRunning: false,
        onSend: vi.fn(),
        onCancel: vi.fn(),
        models,
        selectedModel: models[0],
        onModelChange,
        stats: {
          inputTokens: 10,
          outputTokens: 2,
          cacheReadTokens: 5,
          cacheCreateTokens: 0,
          contextUsedTokens: 12,
          maxContextTokens: 100,
        },
      }),
      host,
    );

    const right = host.querySelector('.agent-ui-input-actions-right')!;
    expect(right.children[0]?.classList.contains('agent-ui-model-selector')).toBe(true);
    expect(right.children[1]?.classList.contains('agent-ui-usage-ring')).toBe(true);

    const select = host.querySelector<HTMLSelectElement>('[aria-label="选择模型"]')!;
    select.value = '通义千问\u0000qwen3.8-max';
    select.dispatchEvent(new Event('change', { bubbles: true }));
    expect(onModelChange).toHaveBeenCalledWith(models[1]);
    dispose();
  });

  it('disables model switching while a run is active', () => {
    const host = document.createElement('div');
    const dispose = render(
      () => createComponent(InputArea, {
        isRunning: true,
        onSend: vi.fn(),
        onCancel: vi.fn(),
        models,
        selectedModel: models[0],
      }),
      host,
    );
    expect(host.querySelector<HTMLSelectElement>('[aria-label="选择模型"]')?.disabled).toBe(true);
    dispose();
  });

  it('triggers dropdown refresh callback on focus', () => {
    const host = document.createElement('div');
    const onModelDropdownRefresh = vi.fn();
    const dispose = render(
      () => createComponent(InputArea, {
        isRunning: false,
        onSend: vi.fn(),
        onCancel: vi.fn(),
        models,
        selectedModel: models[0],
        onModelDropdownRefresh,
      }),
      host,
    );
    host.querySelector<HTMLSelectElement>('[aria-label="选择模型"]')!
      .dispatchEvent(new Event('focus', { bubbles: true }));
    expect(onModelDropdownRefresh).toHaveBeenCalledTimes(1);
    dispose();
  });
});
