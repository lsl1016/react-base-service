const agentRoot = document.getElementById('replay-agent-root');
const replayState = document.getElementById('replay-state');
const replayTitle = document.getElementById('replay-title');
const replayFromStartButton = document.getElementById('replay-from-start');
const themeButton = document.getElementById('toggle-replay-theme');
const baseUrl = `${location.origin}/react-base-service/react`;

let replayHandle = null;
let replayClient = null;
let replayResponse = null;
let replayScope = null;
let playbackVersion = 0;
let currentTheme = 'light';

const parseReplayScope = () => {
  const params = new URLSearchParams(location.search);
  const sessionId = params.get('sessionId')?.trim() ?? '';
  const appKey = params.get('appKey')?.trim() ?? '';
  const routeValuesText = params.get('routeValues') ?? '[]';
  let routeValues;

  try {
    routeValues = JSON.parse(routeValuesText);
  } catch {
    throw new Error('URL 中的 routeValues 不是合法 JSON');
  }

  if (!sessionId) throw new Error('URL 中缺少 sessionId');
  if (!appKey) throw new Error('URL 中缺少 appKey');
  if (!Array.isArray(routeValues) || routeValues.some((item) => typeof item !== 'string')) {
    throw new Error('URL 中的 routeValues 必须是字符串数组');
  }

  return {
    sessionId,
    appKey,
    routeValues,
    theme: params.get('theme') === 'dark' ? 'dark' : 'light',
  };
};

const withReplayIdentity = (state) => ({
  ...state,
  sessionId: replayResponse?.sessionId || replayScope.sessionId,
  connected: false,
});

const createReplayClient = (initialState, scope, initialReducer, manager) => {
  let state = initialState;
  let reducer = initialReducer;
  const listeners = new Set();

  const publishReducerState = () => {
    state = withReplayIdentity(reducer.getState());
    listeners.forEach((listener) => listener(state));
  };

  return {
    getState: () => state,
    subscribe: (listener) => {
      listeners.add(listener);
      listener(state);
      return () => listeners.delete(listener);
    },
    setState: (nextState) => {
      state = nextState;
      listeners.forEach((listener) => listener(state));
    },
    setReducer: (nextReducer) => {
      reducer = nextReducer;
    },
    loadPlanExecutionDetail: async (planExecutionId) => {
      const activeReducer = reducer;
      const detail = await manager.getPlanExecutionDetail(planExecutionId, scope.sessionId, scope.appKey);
      if (activeReducer !== reducer) return;
      reducer.applyPlanDetail(detail.view, detail.attempts ?? []);
      publishReducerState();
    },
    loadPlanStepEvents: async (planExecutionId, stepAttemptId) => {
      const activeReducer = reducer;
      const detail = await manager.getPlanStepEvents(planExecutionId, stepAttemptId, scope.sessionId, scope.appKey);
      if (activeReducer !== reducer) return;
      reducer.applyPlanStepEvents(
        detail.planExecutionId,
        detail.stepId,
        detail.stepAttemptId,
        detail.attemptNo,
        detail.events ?? [],
      );
      publishReducerState();
    },
    getRegisteredTool: () => undefined,
    getSessionScope: () => ({ appKey: scope.appKey, routeValues: [...scope.routeValues] }),
  };
};

const applyTheme = (theme) => {
  currentTheme = theme === 'dark' ? 'dark' : 'light';
  document.documentElement.dataset.theme = currentTheme;
  replayHandle?.setTheme(currentTheme);
  const dark = currentTheme === 'dark';
  themeButton.textContent = dark ? '浅色' : '暗色';
  themeButton.setAttribute('aria-pressed', String(dark));
};

const setPlaying = (playing) => {
  replayFromStartButton.disabled = playing || !replayResponse;
  replayFromStartButton.textContent = playing ? '播放中…' : '从头播放';
};

const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

const playFromStart = async () => {
  if (!replayResponse || !replayClient) return;

  const version = ++playbackVersion;
  const reducer = new window.AgentWebSDK.EventReducer();
  replayClient.setReducer(reducer);
  const events = replayResponse.events;
  const interval = Math.max(16, Math.min(120, Math.round(12000 / Math.max(events.length, 1))));
  setPlaying(true);

  try {
    for (const event of events) {
      if (version !== playbackVersion) return;
      reducer.applyEvent(event);
      replayClient.setState(withReplayIdentity(reducer.getState()));
      await delay(interval);
    }

    if (version !== playbackVersion) return;
    reducer.replayEvents(events);
    replayClient.setState(withReplayIdentity(reducer.getState()));
  } finally {
    if (version === playbackVersion) setPlaying(false);
  }
};

const showError = (error) => {
  replayState.textContent = error?.message || '读取会话事件失败';
  replayState.classList.add('rr-state-error');
};

const loadReplay = async () => {
  replayScope = parseReplayScope();
  applyTheme(replayScope.theme);

  const manager = new window.AgentWebSDK.SessionManager(baseUrl);
  replayResponse = await manager.getEvents(replayScope.sessionId);
  if (!replayResponse || !Array.isArray(replayResponse.events)) {
    throw new Error('事件接口返回格式不正确');
  }

  const reducer = new window.AgentWebSDK.EventReducer();
  reducer.replayEvents(replayResponse.events);
  replayClient = createReplayClient(withReplayIdentity(reducer.getState()), replayScope, reducer, manager);

  const title = replayResponse.title || '未命名会话';
  replayTitle.textContent = title;
  document.title = `${title} · 会话重放`;
  replayHandle = window.AgentWebSDK.mountAgentUI(
    agentRoot,
    replayClient,
    {
      title,
      theme: currentTheme,
      readOnly: true,
      feedback: false,
      attachmentUpload: false,
      showSidebar: false,
    },
  );

  setPlaying(false);
  replayState.classList.add('rr-hidden');
  agentRoot.classList.remove('rr-hidden');
};

replayFromStartButton.addEventListener('click', () => void playFromStart());
themeButton.addEventListener('click', () => applyTheme(currentTheme === 'dark' ? 'light' : 'dark'));

void loadReplay().catch(showError);
window.addEventListener('beforeunload', () => {
  playbackVersion += 1;
  replayHandle?.unmount();
}, { once: true });
