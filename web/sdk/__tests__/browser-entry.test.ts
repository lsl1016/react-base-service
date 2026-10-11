import { describe, expect, it, vi, beforeEach } from 'vitest';

const mocks = vi.hoisted(() => ({
  connect: vi.fn(),
  disconnect: vi.fn(),
  subscribe: vi.fn(),
  uiUnmount: vi.fn(),
  uiSetFeedback: vi.fn(),
  uiPatchFeedback: vi.fn(),
  uiFillInput: vi.fn(async () => ({ status: 'filled' as const })),
  uiSetTheme: vi.fn(),
  uiGetTheme: vi.fn(() => 'light'),
}));

vi.mock('../runtime/agent-client', () => ({
  createAgentClient: vi.fn(() => ({
    connect: mocks.connect,
    disconnect: mocks.disconnect,
    subscribe: mocks.subscribe,
  })),
}));

vi.mock('../ui/mount', () => ({
  mountAgentUI: vi.fn(() => ({
    unmount: mocks.uiUnmount,
    setFeedback: mocks.uiSetFeedback,
    patchFeedback: mocks.uiPatchFeedback,
    fillInput: mocks.uiFillInput,
    setTheme: mocks.uiSetTheme,
    getTheme: mocks.uiGetTheme,
  })),
}));

describe('browser entry', () => {
  beforeEach(() => {
    vi.resetModules();
    mocks.connect.mockReset();
    mocks.disconnect.mockReset();
    mocks.subscribe.mockReset();
    mocks.uiUnmount.mockReset();
    mocks.uiSetFeedback.mockReset();
    mocks.uiPatchFeedback.mockReset();
    mocks.uiFillInput.mockReset();
    mocks.uiFillInput.mockResolvedValue({ status: 'filled' });
    mocks.uiSetTheme.mockReset();
    mocks.uiGetTheme.mockReset();
    mocks.uiGetTheme.mockReturnValue('light');
  });

  it('registers window.AgentWebSDK without dropping existing namespace fields', async () => {
    const previous = (window as Window & { AgentWebSDK?: Record<string, unknown> }).AgentWebSDK;
    (window as Window & { AgentWebSDK?: Record<string, unknown> }).AgentWebSDK = { existing: true };

    await import('../browser');

    const sdk = (window as Window & { AgentWebSDK?: Record<string, unknown> }).AgentWebSDK;
    expect(sdk).toBeTruthy();
    expect(sdk?.existing).toBe(true);
    expect(typeof sdk?.mount).toBe('function');
    expect(typeof sdk?.createClient).toBe('function');

    (window as Window & { AgentWebSDK?: Record<string, unknown> }).AgentWebSDK = previous;
  });

  it('connects on mount, unmounts UI, and disconnects only on destroy', async () => {
    const { mount } = await import('../browser');
    const container = document.createElement('div');

    const handle = mount(container, {
      baseUrl: '/react-base-service/react',
      appKey: 'bw-report',
    });

    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(mocks.disconnect).not.toHaveBeenCalled();

    handle.setTheme('dark');
    expect(mocks.uiSetTheme).toHaveBeenCalledWith('dark');
    expect(handle.getTheme()).toBe('light');

    await expect(handle.fillInput('预填内容', { submit: false })).resolves.toEqual({ status: 'filled' });
    expect(mocks.uiFillInput).toHaveBeenCalledWith('预填内容', { submit: false });

    handle.unmount();
    expect(mocks.uiUnmount).toHaveBeenCalledTimes(1);
    expect(mocks.disconnect).not.toHaveBeenCalled();

    handle.destroy();
    expect(mocks.disconnect).toHaveBeenCalledTimes(1);
    expect(mocks.uiUnmount).toHaveBeenCalledTimes(1);

    handle.destroy();
    expect(mocks.disconnect).toHaveBeenCalledTimes(1);
    expect(mocks.uiUnmount).toHaveBeenCalledTimes(1);
  });
});
