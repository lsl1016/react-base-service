const agentRoot = document.getElementById('replay-agent-root');
const replayState = document.getElementById('replay-state');
const replayTitle = document.getElementById('replay-title');
const replayFromStartButton = document.getElementById('replay-from-start');
const replaySkipToEndButton = document.getElementById('replay-skip-to-end');
const replayProgress = document.getElementById('replay-progress');
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

const setPlaying = (playing, progressText) => {
  replayFromStartButton.disabled = playing || !replayResponse;
  replayFromStartButton.textContent = playing ? '播放中…' : '从头播放';
  replaySkipToEndButton.hidden = !playing;
  if (playing && progressText) {
    replayProgress.textContent = progressText;
    replayProgress.hidden = false;
  } else {
    replayProgress.hidden = true;
  }
};

const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

// 播放节奏约束（TEST-REPORT-2026-10-27 D-5）：逐事件 applyEvent+setState 全量重渲染，
// 大会话要 6~7 分钟且大 markdown 段会长时间冻结。改为按帧分片批处理：
// 整段播放收敛到 ≤50 个渲染帧（每帧批量应用若干事件后只渲染一次，实测单帧渲染
// 成本随会话内容增长到 ~1s，帧数再高只是拉长总时长），帧间保留可感知的推进节奏；
// 进度实时显示，并随时可"跳到结尾"一次到位（replayEvents）。
const PLAYBACK_MAX_FRAMES = 50;
const PLAYBACK_FRAME_BUDGET_MS = 2500;

const playFromStart = async () => {
  if (!replayResponse || !replayClient) return;

  const version = ++playbackVersion;
  const reducer = new window.AgentWebSDK.EventReducer();
  replayClient.setReducer(reducer);
  const events = replayResponse.events;
  const total = Math.max(events.length, 1);
  const frames = Math.min(total, PLAYBACK_MAX_FRAMES);
  const batch = Math.max(1, Math.ceil(total / frames));
  const interval = Math.max(16, Math.min(60, Math.round(PLAYBACK_FRAME_BUDGET_MS / frames)));
  setPlaying(true, `0/${total}`);

  try {
    for (let applied = 0; applied < events.length; applied += batch) {
      if (version !== playbackVersion) return;
      const end = Math.min(applied + batch, events.length);
      for (let index = applied; index < end; index += 1) {
        reducer.applyEvent(events[index]);
      }
      replayClient.setState(withReplayIdentity(reducer.getState()));
      setPlaying(true, `${end}/${total}`);
      await delay(interval);
    }

    if (version !== playbackVersion) return;
    reducer.replayEvents(events);
    replayClient.setState(withReplayIdentity(reducer.getState()));
  } finally {
    if (version === playbackVersion) setPlaying(false);
  }
};

// 跳到结尾：废弃在播循环，一次性重放全部事件直达终态。
const skipToEnd = () => {
  if (!replayResponse || !replayClient) return;
  playbackVersion += 1;
  const reducer = new window.AgentWebSDK.EventReducer();
  reducer.replayEvents(replayResponse.events);
  replayClient.setReducer(reducer);
  replayClient.setState(withReplayIdentity(reducer.getState()));
  setPlaying(false);
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
replaySkipToEndButton.addEventListener('click', () => skipToEnd());
themeButton.addEventListener('click', () => applyTheme(currentTheme === 'dark' ? 'light' : 'dark'));

void loadReplay().catch(showError);
window.addEventListener('beforeunload', () => {
  playbackVersion += 1;
  replayHandle?.unmount();
}, { once: true });
