import { registerSQLClientTools } from './sql-client-tools.js';

window.REACT_AGENT_HOST_CONFIG = window.REACT_AGENT_HOST_CONFIG || undefined;

const $ = (id) => document.getElementById(id);
const container = $('agent-root');
const configBar = $('config-bar');
const callerKeyInput = $('caller-key-input');
const routeValuesInput = $('route-values-input');
const controlContextInput = $('control-context-input');
const llmContextInput = $('llm-context-input');
const moreCapabilitiesPanel = $('more-capabilities-panel');
const toggleMoreCapabilitiesButton = $('toggle-more-capabilities');
const toggleSDKThemeButton = $('toggle-sdk-theme');
const applyButton = $('apply-config');
const copyCallerButton = $('copy-caller');
const deleteCallerButton = $('delete-caller');
const copyCallerModalMask = $('copy-caller-modal-mask');
const copySourceCallerInput = $('copy-source-caller');
const copyTargetCallerInput = $('copy-target-caller');
const copyTargetNameInput = $('copy-target-name');
const copyTargetPlatformInput = $('copy-target-platform');
const copyTargetDescriptionInput = $('copy-target-description');
const copyCallerModalState = $('copy-caller-modal-state');
const copyCallerModalClose = $('copy-caller-modal-close');
const copyCallerModalCancel = $('copy-caller-modal-cancel');
const copyCallerModalConfirm = $('copy-caller-modal-confirm');
const deleteCallerModalMask = $('delete-caller-modal-mask');
const deleteCallerConfirmLabel = $('delete-caller-confirm-label');
const deleteCallerConfirmInput = $('delete-caller-confirm-input');
const deleteCallerModalState = $('delete-caller-modal-state');
const deleteCallerModalClose = $('delete-caller-modal-close');
const deleteCallerModalCancel = $('delete-caller-modal-cancel');
const deleteCallerModalConfirm = $('delete-caller-modal-confirm');
const inputAPIDemoValue = $('input-api-demo-value');
const inputAPIFillButton = $('input-api-fill');
const inputAPISubmitButton = $('input-api-submit');
const inputAPIDemoState = $('input-api-demo-state');
const openReplayLink = $('open-replay');
const replayModalMask = $('replay-modal-mask');
const replayScopeInput = $('replay-scope-input');
const replayModalState = $('replay-modal-state');
const replayModalClose = $('replay-modal-close');
const replayModalCancel = $('replay-modal-cancel');
const replayModalConfirm = $('replay-modal-confirm');
const baseUrl = `${location.origin}/react-base-service/react`;
const managementBaseUrl = `${location.origin}/react-base-service`;
let currentHandle = null;
let disposeSQLClientTools = null;
let sdkTheme = window.REACT_AGENT_HOST_CONFIG?.theme === 'dark' ? 'dark' : 'light';
const feedbackByRunId = {};

const loadMockAnalysisThemeOptions = async () => {
  await new Promise((resolve) => setTimeout(resolve, 500));
  return [
    {
      id: 'budget-2026-core',
      label: '2026 核心业务预算',
      children: [
        { id: 'scope-revenue', label: '收入与利润', payload: { budgetId: 202601, scopeId: 101 } },
        { id: 'scope-retention', label: '用户留存', payload: { budgetId: 202601, scopeId: 102 } },
      ],
    },
    {
      id: 'budget-2026-growth',
      label: '2026 增长专项预算',
      selectable: false,
      children: [
        { id: 'scope-acquisition', label: '获客转化', payload: { budgetId: 202602, scopeId: 201 } },
        { id: 'scope-disabled', label: '实验主题（暂不可用）', disabled: true, payload: { budgetId: 202602, scopeId: 202 } },
      ],
    },
  ];
};

const formatMockTreeSelectionLabel = (selected) => {
  const firstLabel = selected[0]?.path.map((item) => item.label).join(' / ') ?? '';
  const preview = Array.from(firstLabel).slice(0, 7).join('');
  return selected.length > 1 ? `${preview} 等${selected.length}个` : preview;
};

const formatMockTreeSelectionNodes = (selected) => {
  const nodeIds = new Set();
  return selected
    .flatMap(({ path }) => path)
    .filter((node) => {
      if (nodeIds.has(node.id)) return false;
      nodeIds.add(node.id);
      return true;
    })
    .map((node) => node.label)
    .join(',');
};

const quickInsertItems = [
  {
    id: 'analysis-theme-select',
    label: '/按分析主题选表',
    group: '分析主题选表',
    description: 'Mock：按预算和分析主题选择数据范围',
    kind: 'tree-select',
    picker: {
      multiple: true,
      leafOnly: true,
      loadOptions: loadMockAnalysisThemeOptions,
      toShortcut: (selected) => {
        return {
        id: `analysis-theme-${selected.map(({ node }) => `${node.payload.budgetId}-${node.payload.scopeId}`).join('_')}`,
        label: `主题：${formatMockTreeSelectionLabel(selected)}`,
        group: '分析主题选表',
        description: '已选择的 Mock 分析主题',
        data: {
          tag: 'user_select_analysis_theme',
          proto: {
           system_remider: '当用户当前选择了一些分析主题',
           selections: formatMockTreeSelectionNodes(selected)
          },
        },
      }
      },
      onConfirm: (selected) => console.info('[playground] tree-select confirmed', selected),
      onCancel: (selected) => console.info('[playground] tree-select cancelled', selected),
    },
  },
   {
    id: 'user-input-table-name',
    label: '/t快捷选表:',
    group: 'Skills',
    description: '输入AI能理解的库名表名，eg: default.yike_notice_action_hour',
    data: { tag: 'skill', proto: { system_remider: '当用户使用此命令表示想要快捷选表，后续会使用自然语言跟随表名' } },
  },
  {
    id: 'skills-create',
    label: '/创建技能',
    group: 'Skills',
    description: '你可以使用此技能来创建一个技能。',
    data: { tag: 'skill', proto: { system_remider: '当用户使用此命令时，调用createSkill来为他的要求创建skill。' } },
  },
  ...Array.from({ length: 10 }, (_, index) => ({
    id: `skills-${index}`,
    label: `/蓝鲸技能包-${index}`,
    group: 'Skills',
    description: `当用户提到“蓝鲸技能包-${index}”时使用。`,
    data: { tag: 'skill', proto: { path: `/a/b/c/蓝鲸技能包-${index}.md` } },
  })),
  ...Array.from({ length: 10 }, (_, index) => ({
    id: `commands-${index}`,
    label: `/命令${index}`,
    group: 'Commands',
    description: `当用户提到“${index}”时使用。`,
    data: { tag: 'command', proto: { path: `/a/b/c/${index}.md` } },
  })),
];

const parseRouteValues = (value) => value
  .split(',')
  .map((item) => item.trim())
  .filter(Boolean);

const parseJSONObject = (value, fieldName) => {
  const text = value.trim();
  if (!text) {
    return undefined;
  }

  const parsed = JSON.parse(text);
  if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') {
    throw new Error(`${fieldName} 必须是 JSON 对象`);
  }

  return parsed;
};

const resolveMountConfig = () => {
  const hostConfig = window.REACT_AGENT_HOST_CONFIG;
  if (hostConfig && hostConfig.callerKey) {
    return {
      callerKey: hostConfig.callerKey,
      routeValues: Array.isArray(hostConfig.routeValues) ? hostConfig.routeValues : [],
      controlContext: hostConfig.controlContext && typeof hostConfig.controlContext === 'object' && !Array.isArray(hostConfig.controlContext)
        ? hostConfig.controlContext
        : undefined,
      llmContext: hostConfig.llmContext && typeof hostConfig.llmContext === 'object' && !Array.isArray(hostConfig.llmContext)
        ? hostConfig.llmContext
        : undefined,
      editable: hostConfig.editable === true,
    };
  }

  return {
    callerKey: callerKeyInput.value.trim() || 'demo-app',
    routeValues: parseRouteValues(routeValuesInput.value),
    controlContext: parseJSONObject(controlContextInput.value, 'controlContext'),
    llmContext: parseJSONObject(llmContextInput.value, 'llmContext'),
    editable: true,
  };
};



const mountAgent = () => {
  const hostConfig = resolveMountConfig();

  if (currentHandle && typeof currentHandle.destroy === 'function') {
    disposeSQLClientTools?.();
    disposeSQLClientTools = null;
    disposeContextUsageSubscription?.();
    disposeContextUsageSubscription = null;
    currentHandle.destroy();
  }

  currentHandle = window.AgentWebSDK.mount(container, {
    baseUrl,
    callerKey: hostConfig.callerKey,
    routeValues: hostConfig.routeValues,
    controlContext: hostConfig.controlContext,
    llmContext: hostConfig.llmContext,
    modelKey: 'claude',
    modelVersion: 'glm-4.6',
    maxSteps: 25,
    theme: sdkTheme,
    title: 'ReAct Playground',
    showSidebar: true,
    placeholder: '输入 / 插入快捷节点，描述你的需求...',
    quickInsertItems,
    serializeInput: window.AgentWebSDK.serializeAgentInputParts,
  });

  disposeSQLClientTools = registerSQLClientTools(currentHandle.client);
  subscribeContextUsage();

  currentHandle?.ui?.setFeedback?.(feedbackByRunId);
  configBar.classList.toggle('rp-hidden', !hostConfig.editable);
  return management.reload();
};

const setInputAPIDemoState = (message, isError = false) => {
  inputAPIDemoState.textContent = message;
  inputAPIDemoState.classList.toggle('rp-input-api-state-error', isError);
};

const runInputAPIDemo = async (submit) => {
  if (!currentHandle || typeof currentHandle.fillInput !== 'function') {
    setInputAPIDemoState('当前 SDK 未提供 fillInput，请重新构建并刷新页面', true);
    return;
  }

  inputAPIFillButton.disabled = true;
  inputAPISubmitButton.disabled = true;
  setInputAPIDemoState(submit ? '正在调用 fillInput(..., { submit: true })...' : '正在预填输入框...');

  try {
    const result = await currentHandle.fillInput(inputAPIDemoValue.value, { submit });
    if (result.status === 'submitted') {
      setInputAPIDemoState('已通过 fillInput 立即发送');
      return;
    }
    if (result.status === 'filled') {
      setInputAPIDemoState(submit
        ? '当前 run 未结束，submit 已忽略，仅更新了输入框'
        : '已填充输入框，请在聊天框中确认后发送');
      return;
    }
    setInputAPIDemoState(`调用未执行：${result.reason}`, true);
  } catch (error) {
    setInputAPIDemoState(error?.message || 'fillInput 调用失败', true);
  } finally {
    inputAPIFillButton.disabled = false;
    inputAPISubmitButton.disabled = false;
  }
};

const applySDKTheme = (theme) => {
  sdkTheme = theme === 'dark' ? 'dark' : 'light';
  currentHandle?.setTheme?.(sdkTheme);
  // SDK 只在自己容器上挂 data-agent-ui-theme；同步到 <html> 让聊天区之外的
  // 页面元素（如上下文容量卡片）也能用同一份暗色变量。
  document.documentElement.setAttribute('data-agent-ui-theme', sdkTheme);
  const dark = sdkTheme === 'dark';
  const label = dark ? '切换到亮色主题' : '切换到暗色主题';
  toggleSDKThemeButton.setAttribute('aria-pressed', dark ? 'true' : 'false');
  toggleSDKThemeButton.setAttribute('aria-label', label);
  toggleSDKThemeButton.title = label;
  toggleSDKThemeButton.querySelector('[aria-hidden="true"]').textContent = dark ? '☀' : '☾';
};

const parseReplayScope = () => {
  let scope;
  try {
    scope = JSON.parse(replayScopeInput.value.trim());
  } catch {
    throw new Error('请输入合法的 JSON');
  }

  if (!scope || Array.isArray(scope) || typeof scope !== 'object') {
    throw new Error('会话定位信息必须是 JSON 对象');
  }

  const sessionId = typeof scope.sessionId === 'string' ? scope.sessionId.trim() : '';
  const callerKey = typeof scope.callerKey === 'string' ? scope.callerKey.trim() : '';
  if (!sessionId) throw new Error('sessionId 不能为空');
  if (!callerKey) throw new Error('callerKey 不能为空');
  if (!Array.isArray(scope.routeValues) || scope.routeValues.some((item) => typeof item !== 'string')) {
    throw new Error('routeValues 必须是字符串数组');
  }

  return { sessionId, callerKey, routeValues: scope.routeValues };
};

const buildReplayURL = (scope) => {
  const replayURL = new URL('/react-base-service/react/replay', location.origin);
  replayURL.searchParams.set('sessionId', scope.sessionId);
  replayURL.searchParams.set('callerKey', scope.callerKey);
  replayURL.searchParams.set('routeValues', JSON.stringify(scope.routeValues));
  replayURL.searchParams.set('theme', sdkTheme);
  return replayURL;
};

const closeReplayModal = () => {
  replayModalMask.classList.remove('rp-visible');
  replayModalState.textContent = '';
};

const openReplayModal = () => {
  replayModalState.textContent = '';
  replayModalMask.classList.add('rp-visible');
  replayScopeInput.focus();
};

const confirmReplay = () => {
  try {
    const replayURL = buildReplayURL(parseReplayScope());
    window.open(replayURL.href, '_blank', 'noopener,noreferrer');
    closeReplayModal();
  } catch (error) {
    replayModalState.textContent = error?.message || '无法打开重放页面';
    replayScopeInput.focus();
  }
};

const normalizeEnvelope = (payload) => {
  const code = payload.errno ?? payload.errNo ?? payload.code ?? 0;
  if (code !== 0) {
    throw new Error(payload.errmsg ?? payload.errMsg ?? payload.message ?? '请求失败');
  }
  return payload.data;
};

const post = async (path, body) => {
  const response = await fetch(`${managementBaseUrl}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify(body),
  });
  if (!response.ok) throw new Error(`请求失败：${response.status}`);
  return normalizeEnvelope(await response.json());
};

const setOperationState = (element, message, isError = false) => {
  element.textContent = message || '';
  element.classList.toggle('rp-error', isError);
};

const closeCopyCallerModal = () => {
  copyCallerModalMask.classList.remove('rp-visible');
  setOperationState(copyCallerModalState, '');
};

const openCopyCallerModal = async () => {
  const sourceCallerKey = callerKeyInput.value.trim();
  if (!sourceCallerKey) {
    alert('请先填写源 callerKey');
    callerKeyInput.focus();
    return;
  }
  copySourceCallerInput.value = sourceCallerKey;
  copyTargetCallerInput.value = `${sourceCallerKey.slice(0, 27)}_copy`;
  copyTargetNameInput.value = `${sourceCallerKey} 副本`;
  copyTargetPlatformInput.value = '';
  copyTargetDescriptionInput.value = `从 ${sourceCallerKey} 复制`;
  setOperationState(copyCallerModalState, '');
  copyCallerModalMask.classList.add('rp-visible');
  copyTargetCallerInput.focus();
  copyTargetCallerInput.select();

  try {
    const callers = await post('/caller/list', {});
    const source = Array.isArray(callers) ? callers.find((item) => item.callerKey === sourceCallerKey) : null;
    if (source && copyCallerModalMask.classList.contains('rp-visible') && copySourceCallerInput.value === sourceCallerKey) {
      copyTargetNameInput.value = `${source.name || sourceCallerKey} 副本`;
      copyTargetPlatformInput.value = source.platform || '';
    }
  } catch {
    // 列表预填失败不阻塞复制，用户仍可手动填写目标信息。
  }
};

const confirmCopyCaller = async () => {
  const sourceCallerKey = copySourceCallerInput.value.trim();
  const targetCallerKey = copyTargetCallerInput.value.trim();
  const name = copyTargetNameInput.value.trim();
  const platform = copyTargetPlatformInput.value.trim();
  if (!targetCallerKey || !name || !platform) {
    setOperationState(copyCallerModalState, '目标 callerKey、名称和平台不能为空', true);
    return;
  }

  copyCallerModalConfirm.disabled = true;
  setOperationState(copyCallerModalState, '正在复制...');
  try {
    const result = await post('/caller/copy_config', {
      sourceCallerKey,
      targetCaller: {
        callerKey: targetCallerKey,
        name,
        description: copyTargetDescriptionInput.value,
        platform,
      },
    });
    closeCopyCallerModal();
    callerKeyInput.value = result.targetCallerKey;
    await remountAgent();
    const copied = result.copied || {};
    management.setState(`复制完成：Skill ${copied.skills || 0}，系统提示词 ${copied.systemPrompts || 0}，Tool ${copied.tools || 0}，Tool 策略 ${copied.toolUserPolicies || 0}，API Key ${copied.apiKeys || 0}`);
  } catch (error) {
    setOperationState(copyCallerModalState, error?.message || '复制失败', true);
  } finally {
    copyCallerModalConfirm.disabled = false;
  }
};

const closeDeleteCallerModal = () => {
  deleteCallerModalMask.classList.remove('rp-visible');
  deleteCallerConfirmInput.value = '';
  setOperationState(deleteCallerModalState, '');
};

const openDeleteCallerModal = () => {
  const callerKey = callerKeyInput.value.trim();
  if (!callerKey) {
    alert('请先填写要删除的 callerKey');
    callerKeyInput.focus();
    return;
  }
  deleteCallerConfirmLabel.textContent = `请输入 ${callerKey} 确认删除`;
  deleteCallerConfirmInput.value = '';
  setOperationState(deleteCallerModalState, '');
  deleteCallerModalMask.classList.add('rp-visible');
  deleteCallerConfirmInput.focus();
};

const clearDeletedCaller = () => {
  disposeSQLClientTools?.();
  disposeSQLClientTools = null;
  currentHandle?.destroy?.();
  currentHandle = null;
  callerKeyInput.value = '';
  routeValuesInput.value = '';
  container.innerHTML = '';
  management.items = [];
  management.renderTable();
  management.setState('Caller 已删除');
};

const confirmDeleteCaller = async () => {
  const callerKey = callerKeyInput.value.trim();
  if (deleteCallerConfirmInput.value.trim() !== callerKey) {
    setOperationState(deleteCallerModalState, `请输入完整的 ${callerKey}`, true);
    return;
  }

  deleteCallerModalConfirm.disabled = true;
  setOperationState(deleteCallerModalState, '正在删除...');
  try {
    await post('/caller/batch_delete', { callerKey });
    closeDeleteCallerModal();
    clearDeletedCaller();
  } catch (error) {
    setOperationState(deleteCallerModalState, error?.message || '删除失败', true);
  } finally {
    deleteCallerModalConfirm.disabled = false;
  }
};

const shortText = (value, max = 56) => {
  const text = typeof value === 'string' ? value : JSON.stringify(value ?? '');
  return text.length > max ? `${text.slice(0, max)}...` : text;
};

const escapeHtml = (value) => String(value ?? '')
  .replace(/&/g, '&amp;')
  .replace(/</g, '&lt;')
  .replace(/>/g, '&gt;')
  .replace(/"/g, '&quot;');

const isEnabled = (item) => Number(item.status) === 1;
const isPlanEnabled = (item) => item.planEnabled === true || Number(item.planEnabled) === 1;

const mcpStatusBadge = (item) => {
  if (!isEnabled(item)) return '<span class="rp-mcp-badge rp-mcp-badge-off">已停用</span>';
  if (item.lastCheckStatus === 'connected') return '<span class="rp-mcp-badge rp-mcp-badge-ok">已连接</span>';
  if (item.lastCheckStatus === 'disconnected') return `<span class="rp-mcp-badge rp-mcp-badge-bad" title="${escapeHtml(item.lastCheckMessage || '')}">连接异常</span>`;
  return '<span class="rp-mcp-badge rp-mcp-badge-off">未检测</span>';
};

const getConfig = () => resolveMountConfig();

// 管理面板 Caller 筛选：'' = 全部（跨 caller 列出），'default' = 默认作用域，其余为具体 callerKey。
const callerFilterValue = () => document.getElementById('management-caller-filter')?.value ?? '';
// 新建资源的目标 caller：跟随 Caller 筛选（全部时回落到左上角全局 callerKey）。
const createTargetCallerKey = () => callerFilterValue() || getConfig().callerKey;

// 弹窗「适用路由」字段与 routeValues 数组的互转（逗号分隔，留空 = [] 通用）。
const splitRouteText = (text) => String(text ?? '').split(',').map((value) => value.trim()).filter(Boolean);
const joinRouteValues = (routeValues) => (Array.isArray(routeValues) ? routeValues : []).join(',');
// 新建时的默认路由：带出左上角全局输入框的值，弹窗内可改。
const createDefaultRouteText = () => joinRouteValues(getConfig().routeValues);

const resources = {
  caller: {
    title: 'Caller 管理',
    addText: '注册 Caller',
    idKey: 'callerKey',
    listPath: '/caller/list',
    createPath: '/caller/register',
    updatePath: '/caller/update',
    // 不提供 deletePath：Caller 删除连带其全部资源，统一走「更多能力」里的强确认入口。
    columns: [
      ['callerKey', 'callerKey'], ['name', '名称'], ['description', '描述'], ['platform', '平台'], ['createdAt', '创建时间'], ['planEnabled', 'Plan'], ['status', '状态'], ['actions', '操作'],
    ],
    empty: () => ({ callerKey: '', name: '', description: '', platform: '', planEnabled: false, status: 1 }),
    toDraft: (item) => ({ ...item }),
    toPayload: (draft) => ({
      callerKey: draft.callerKey,
      name: draft.name,
      description: draft.description,
      platform: draft.platform,
      planEnabled: isPlanEnabled(draft),
      status: Number(draft.status),
    }),
    fields: (mode) => mode === 'create'
      ? [['callerKey', 'callerKey'], ['name', '名称'], ['platform', '平台'], ['planEnabled', 'Plan', 'select'], ['description', '描述', 'textarea']]
      : [['callerKey', 'callerKey', 'readonly'], ['name', '名称'], ['platform', '平台'], ['planEnabled', 'Plan', 'select'], ['status', '状态', 'select'], ['description', '描述', 'textarea']],
  },
  tool: {
    title: '工具管理',
    addText: '新增工具',
    idKey: 'toolId',
    // 工具页支持「只看 MCP」筛选（toolbar 按钮），筛出 MCP 连接同步进注册表的工具。
    mcpFilter: true,
    // 支持 Caller 筛选：全部（跨 caller）/ 默认作用域 / 各 caller。
    callerFilter: true,
    listPath: '/tool/list',
    createPath: '/tool/register',
    updatePath: '/tool/update',
    deletePath: '/tool/delete',
    deleteBody: (item) => ({ toolId: item.toolId }),
    columns: [
      ['toolId', '工具标识'], ['name', '工具名称'], ['description', '工具描述'], ['toolType', '工具类型'], ['callerKey', '归属 caller'], ['config', '工具配置'], ['routeValues', '适用路由'], ['status', '状态'], ['actions', '操作'],
    ],
    empty: () => ({ name: '', description: '', toolType: 'client', callerKey: createTargetCallerKey(), routeText: createDefaultRouteText(), configText: '{}', status: 1 }),
    toDraft: (item) => ({ ...item, routeText: joinRouteValues(item.routeValues), configText: JSON.stringify(item.config ?? {}, null, 2) }),
    toPayload: (draft, config) => {
      const payload = {
        toolId: draft.toolId,
        callerKey: draft.callerKey || config.callerKey,
        routeValues: splitRouteText(draft.routeText),
        name: draft.name,
        description: draft.description,
        toolType: draft.toolType,
        status: Number(draft.status),
      };
      // MCP 工具的配置 JSON 由「MCP 连接」同步管理，更新请求不携带 config（后端同样拒绝）。
      if (draft.toolType !== 'mcp') payload.config = JSON.parse(draft.configText || '{}');
      return payload;
    },
    fields: (mode, draft) => {
      const isMcp = draft?.toolType === 'mcp';
      return [
        ['name', '工具名称'],
        ['callerKey', '归属 caller', 'readonly'],
        ['routeText', '适用路由（逗号分隔）'],
        ['toolType', '工具类型', isMcp ? 'readonly' : 'text'],
        ['status', '状态', 'select'],
        ['description', '工具描述', 'textarea'],
        ['configText', '工具配置 JSON', isMcp ? 'readonlyTextarea' : 'textarea'],
      ];
    },
    modalNote: (mode, draft) => {
      if (mode === 'edit' && draft?.toolType === 'mcp') {
        return 'MCP 工具由「MCP 连接」面板同步管理：配置 JSON 只读，名称/描述在下次连接同步时会被服务端清单覆盖，删除请在「MCP 连接」面板操作。';
      }
      return null;
    },
  },
  systemPrompt: {
    title: '系统提示词管理',
    addText: '新增提示词',
    idKey: 'id',
    callerFilter: true,
    listPath: '/system-prompt/list',
    createPath: '/system-prompt/register',
    updatePath: '/system-prompt/update',
    deletePath: '/system-prompt/delete',
    deleteBody: (item) => ({ id: item.id }),
    columns: [
      ['id', 'ID'], ['name', '名称'], ['content', '内容'], ['callerKey', '归属 caller'], ['routeValues', '适用路由'], ['status', '状态'], ['actions', '操作'],
    ],
    empty: () => ({ name: '', content: '', callerKey: createTargetCallerKey(), routeText: createDefaultRouteText(), status: 1 }),
    toDraft: (item) => ({ ...item, routeText: joinRouteValues(item.routeValues) }),
    toPayload: (draft, config) => ({
      id: draft.id,
      callerKey: draft.callerKey || config.callerKey,
      routeValues: splitRouteText(draft.routeText),
      name: draft.name,
      content: draft.content,
      status: Number(draft.status),
    }),
    fields: (mode) => [
      ['name', '名称'], ['callerKey', '归属 caller', 'readonly'], ['routeText', '适用路由（逗号分隔）'], ['status', '状态', 'select'], ['content', '内容', 'textarea'],
    ],
  },
  skill: {
    title: 'skill 管理',
    addText: '新增 skill',
    idKey: 'skillId',
    callerFilter: true,
    listPath: '/skill/list',
    createPath: '/skill/create',
    updatePath: '/skill/update',
    deletePath: '/skill/delete',
    deleteBody: (item) => ({ skillId: item.skillId }),
    // P2-2 SKILL.md 粘贴导入（frontmatter：name/description/triggers + 正文；同名覆盖）。
    importPath: '/skill/import',
    importNote: '粘贴「frontmatter + 正文」格式的 SKILL.md（frontmatter 支持 name/description/triggers/caller_key/route_values，正文即技能说明）。同 caller 同名覆盖更新。',
    columns: [
      ['skillId', 'skill 标识'], ['name', '名称'], ['description', '描述'], ['triggerCondition', '触发条件'], ['isDefault', '默认'], ['callerKey', '归属 caller'], ['routeValues', '适用路由'], ['status', '状态'], ['actions', '操作'],
    ],
    empty: () => ({
      name: '', description: '', triggerCondition: '', forbiddenCondition: '', executionSteps: '', businessContext: '', promptSupplement: '', isDefault: 0, callerKey: createTargetCallerKey(), routeText: createDefaultRouteText(), status: 1,
    }),
    toDraft: (item) => ({ ...item, routeText: joinRouteValues(item.routeValues) }),
    toPayload: (draft, config) => ({
      skillId: draft.skillId,
      callerKey: draft.callerKey || config.callerKey,
      routeValues: splitRouteText(draft.routeText),
      name: draft.name,
      description: draft.description,
      triggerCondition: draft.triggerCondition,
      forbiddenCondition: draft.forbiddenCondition,
      executionSteps: draft.executionSteps,
      businessContext: draft.businessContext,
      promptSupplement: draft.promptSupplement,
      isDefault: Number(draft.isDefault),
      status: Number(draft.status),
    }),
    fields: (mode) => [
      ['name', '名称'], ['callerKey', '归属 caller', 'readonly'], ['routeText', '适用路由（逗号分隔）'], ['status', '状态', 'select'], ['isDefault', '默认', 'defaultSelect'], ['description', '描述', 'textarea'], ['triggerCondition', '触发条件', 'textarea'], ['forbiddenCondition', '禁用条件', 'textarea'], ['executionSteps', '执行步骤', 'textarea'], ['businessContext', '业务上下文', 'textarea'], ['promptSupplement', '补充提示词', 'textarea'],
    ],
  },
  apiKey: {
    title: 'API Key 管理',
    addText: '新增 API Key',
    idKey: 'id',
    listPath: '/apikey/list',
    createPath: '/apikey/register',
    updatePath: '/apikey/update',
    deletePath: '/apikey/delete',
    deleteBody: (item) => ({ id: item.id }),
    columns: [
      ['id', 'ID'], ['name', '名称'], ['apiKeyDisplay', 'API Key'], ['routeValues', '适用方'], ['status', '状态'], ['actions', '操作'],
    ],
    empty: () => ({ name: '', apiKey: '', routeText: createDefaultRouteText(), status: 1 }),
    toDraft: (item) => ({ ...item, routeText: joinRouteValues(item.routeValues), apiKeyDisplay: item.apiKey || '******', apiKey: '' }),
    toPayload: (draft, config, mode) => {
      const payload = {
        id: draft.id,
        callerKey: draft.callerKey || config.callerKey,
        routeValues: splitRouteText(draft.routeText),
        name: draft.name,
        status: Number(draft.status),
      };
      if (mode === 'create' || draft.apiKey) payload.apiKey = draft.apiKey;
      return payload;
    },
    fields: (mode) => (mode === 'create'
      ? [['name', '名称'], ['routeText', '适用路由（逗号分隔）'], ['apiKey', 'API Key', 'password']]
      : [['name', '名称'], ['routeText', '适用路由（逗号分隔）'], ['status', '状态', 'select'], ['apiKey', '新 API Key', 'password']]),
  },
  planTemplate: {
    title: '模板列表',
    tabText: '模板列表',
    itemName: '模板',
    addText: '新增模板',
    idKey: 'templateId',
    listPath: '/react/plan_template/list',
    detailPath: '/react/plan_template/detail',
    createPath: '/react/plan_template/create',
    updatePath: '/react/plan_template/update',
    columns: [
      ['templateId', '模板 ID'], ['description', '描述'], ['revision', 'Revision'], ['status', '状态'], ['updatedBy', '更新人'], ['updatedAt', '更新时间'], ['actions', '操作'],
    ],
    empty: () => ({
      templateId: '',
      status: 1,
      templateText: JSON.stringify({
        description: '请填写模板用途',
        inputs: {
          type: 'object',
          properties: {},
          additionalProperties: false,
        },
        steps: [{
          id: 'execute_task',
          order: 1,
          name: '执行任务',
          goal: '根据输入完成目标',
          depends_on: [],
          input: {},
          tool_names: ['*'],
          max_rounds: 20,
          timeout_seconds: 300,
          output_schema: {
            type: 'object',
            properties: { result: { type: 'string' } },
            required: ['result'],
            additionalProperties: false,
          },
        }],
        output: { result: '${steps.execute_task.result.result}' },
        output_schema: {
          type: 'object',
          properties: { result: { type: 'string' } },
          required: ['result'],
          additionalProperties: false,
        },
      }, null, 2),
    }),
    toDraft: (item) => ({ ...item }),
    loadDetail: async (item, config, resource) => {
      const detail = await post(resource.detailPath, {
        callerKey: config.callerKey,
        templateId: item.templateId,
      });
      return {
        ...detail,
        templateText: JSON.stringify(detail?.template ?? {}, null, 2),
      };
    },
    toPayload: (draft, config) => ({
      callerKey: config.callerKey,
      templateId: draft.templateId,
      revision: draft.revision,
      template: JSON.parse(draft.templateText || '{}'),
      status: Number(draft.status),
    }),
    fields: (mode) => mode === 'create'
      ? [['templateId', '模板 ID'], ['status', '状态', 'select'], ['templateText', '模板 JSON', 'codeTextarea']]
      : [['templateId', '模板 ID', 'readonly'], ['revision', 'Revision', 'readonly'], ['status', '状态', 'select'], ['templateText', '模板 JSON', 'codeTextarea']],
  },
  agent: {
    title: '子 Agent 管理',
    tabText: '子 Agent',
    itemName: '子 Agent',
    addText: '新增子 Agent',
    idKey: 'agentId',
    callerFilter: true,
    listPath: '/agent/list',
    createPath: '/agent/create',
    updatePath: '/agent/update',
    deletePath: '/agent/delete',
    deleteBody: (item) => ({ agentId: item.agentId }),
    // P1 Markdown 导入（frontmatter + 正文即 system_prompt）。
    importPath: '/agent/import',
    importNote: '粘贴「frontmatter + 正文」格式的子 Agent 定义（frontmatter 支持 agent_key/name/description/caller_key/route_values/model_key/model_version/tools/skills/max_steps/max_tokens_per_run/permission_mode，正文即系统提示词）。同 caller 同 agent_key 重复会报错。',
    columns: [
      ['agentKey', 'agent_key'], ['name', '名称'], ['description', '委派说明'], ['callerKey', '归属 caller'], ['maxSteps', '步数上限'], ['maxTokensPerRun', 'token 预算'], ['permissionMode', '权限模式'], ['status', '状态'], ['actions', '操作'],
    ],
    empty: () => ({
      agentKey: '', name: '', description: '', callerKey: createTargetCallerKey(), routeText: createDefaultRouteText(),
      systemPrompt: '', modelKey: '', modelVersion: '', toolsText: '', skillsText: '', maxSteps: 8, maxTokensPerRun: 0, permissionMode: 'inherit', status: 1,
    }),
    toDraft: (item) => ({
      ...item,
      routeText: joinRouteValues(item.routeValues),
      toolsText: Array.isArray(item.tools) ? item.tools.join(',') : '',
      skillsText: Array.isArray(item.skills) ? item.skills.join(',') : '',
    }),
    toPayload: (draft, config) => ({
      agentId: draft.agentId,
      agentKey: draft.agentKey,
      name: draft.name,
      description: draft.description,
      callerKey: draft.callerKey || config.callerKey,
      routeValues: splitRouteText(draft.routeText),
      systemPrompt: draft.systemPrompt,
      modelKey: draft.modelKey || '',
      modelVersion: draft.modelVersion || '',
      tools: splitRouteText(draft.toolsText),
      skills: splitRouteText(draft.skillsText),
      maxSteps: Number(draft.maxSteps) || 0,
      maxTokensPerRun: Number(draft.maxTokensPerRun) || 0,
      permissionMode: draft.permissionMode || 'inherit',
      status: Number(draft.status),
    }),
    fields: (mode) => [
      ['agentKey', 'agent_key', mode === 'edit' ? 'readonly' : 'text'],
      ['name', '名称'],
      ['callerKey', '归属 caller', 'readonly'],
      ['routeText', '适用路由（逗号分隔）'],
      ['status', '状态', 'select'],
      ['maxSteps', '步数上限（0=默认）'],
      ['maxTokensPerRun', 'token 预算（0=不限）'],
      ['permissionMode', '权限模式', 'enumSelect', [['inherit', '继承父 run'], ['auto', '自动执行'], ['confirm', '每次确认'], ['confirm_risky', '高风险确认']]],
      ['modelKey', '模型种类（空=继承）'],
      ['modelVersion', '模型版本（空=默认）'],
      ['toolsText', '工具白名单（逗号分隔，空=全部）'],
      ['skillsText', 'Skill 白名单（逗号分隔，空=不注入）'],
      ['description', '委派说明（适用/不适用）', 'textarea'],
      ['systemPrompt', '系统提示词', 'textarea'],
    ],
  },
  mcp: {
    title: 'MCP 连接管理',
    tabText: 'MCP 连接',
    itemName: 'MCP 连接',
    addText: '手动配置',
    idKey: 'serverId',
    cardList: true,
    toDraft: (item) => ({ ...item }),
    listPath: '/react/mcp/list',
    detailPath: '/react/mcp/detail',
    createPath: '/react/mcp/create',
    updatePath: '/react/mcp/update',
    deletePath: '/react/mcp/delete',
    deleteBody: (item) => ({ callerKey: getConfig().callerKey, serverId: item.serverId }),
    empty: () => ({
      rawConfig: JSON.stringify({
        mcpServers: {
          'mcp-server': {
            url: 'http://127.0.0.1:18080/api/mcp',
            headers: { Authorization: 'Bearer <token>' },
          },
        },
      }, null, 2),
    }),
    modalNote: (mode) => (mode === 'create'
      ? '请从 MCP Servers 的介绍页面复制配置 JSON，并粘贴到输入框中。仅支持 url 形式（HTTP MCP）；command/args 形式（npx/uvx）不在基座开放范围内。<span class="rp-note-warning">配置前请确认来源，甄别风险。</span>'
      : '修改端点、请求头、超时或绑定 caller 后会自动重连并重新同步工具清单；停用会同时下线其同步的全部注册工具。绑定 caller 填逗号分隔的 callerKey（属主恒定生效，不用填）。'),
    loadDetail: async (item, config) => {
      const detail = await post('/react/mcp/detail', { callerKey: config.callerKey, serverId: item.serverId });
      const server = detail?.server ?? {};
      const bound = Array.isArray(server.boundCallers) ? server.boundCallers : [];
      return { ...item, ...server, boundCallersText: bound.slice(1).join(','), headersText: JSON.stringify(detail?.headers ?? {}, null, 2) };
    },
    toPayload: (draft, config, mode) => (mode === 'create'
      ? { callerKey: config.callerKey, rawConfig: draft.rawConfig }
      : {
        callerKey: config.callerKey,
        serverId: draft.serverId,
        endpoint: draft.endpoint,
        headers: JSON.parse(draft.headersText || '{}'),
        timeoutMs: Number(draft.timeoutMs) || 30000,
        description: draft.description || '',
        boundCallers: splitRouteText(draft.boundCallersText),
        status: Number(draft.status),
      }),
    fields: (mode) => (mode === 'create'
      ? [['rawConfig', '原始配置（mcpServers JSON）', 'codeTextarea']]
      : [['name', '名称', 'readonly'], ['kind', '传输', 'readonly'], ['boundCallersText', '绑定 caller（逗号分隔）'], ['endpoint', '端点 URL'], ['timeoutMs', '超时(ms)'], ['status', '状态', 'select'], ['description', '描述', 'textarea'], ['headersText', '请求头 JSON', 'codeTextarea']]),
  },
  // MCP 应用（服务端网关接入凭证）：卡片列表 + 创建/重置密钥（secret 一次性展示）+ 工具绑定 + 调用审计。
  mcpapp: {
    title: 'MCP 应用管理',
    tabText: 'MCP 应用',
    itemName: 'MCP 应用',
    addText: '新增应用',
    idKey: 'appId',
    cardList: true,
    cardKind: 'mcpapp',
    listPath: '/react/mcpapp/list',
    createPath: '/react/mcpapp/create',
    updatePath: '/react/mcpapp/update',
    deletePath: '/react/mcpapp/delete',
    deleteBody: (item) => ({ appId: item.appId }),
    toDraft: (item) => ({ ...item }),
    empty: () => ({ appName: '', status: 1 }),
    toPayload: (draft, config, mode) => (mode === 'create'
      ? { appName: draft.appName, status: Number(draft.status) }
      : { appId: draft.appId, appName: draft.appName, status: Number(draft.status) }),
    modalNote: () => '应用凭证供外部 MCP 客户端接入服务端网关（Authorization: Bearer <app_key>:<app_secret>）。工具是全局基础集合，应用可见的工具 = 为其绑定的工具子集（新应用默认 0 个，创建后点「工具授权」勾选）。appSecret 仅创建/重置时完整展示一次，请妥善保存。',
    fields: () => [
      ['appName', '应用名称'],
      ['status', '状态', 'select'],
    ],
  },
  // P3 Agent Bundle 插件包：安装表单 + 已装卡片（来源白名单内 git URL 或本地路径）。
  bundle: {
    title: 'Bundle 插件包',
    tabText: 'Bundle',
    itemName: 'Bundle',
    addText: '安装 Bundle',
    idKey: 'bundleId',
    cardList: true,
    cardKind: 'bundle',
    listPath: '/react/bundle/list',
    toDraft: (item) => ({ ...item }),
    columns: [],
  },
  // P3 workspace 运行视图：活跃 worktree 只读清单（run 终态即释放，空列表为正常态）。
  workspace: {
    title: '代码工作区',
    tabText: '工作区',
    idKey: 'runId',
    cardList: true,
    cardKind: 'workspace',
    noCreate: true,
    listPath: '/react/workspace/active',
    toDraft: (item) => ({ ...item }),
    columns: [],
  },
  // 运行时配置：custom.yaml 策略的在线覆盖面板（DB 覆盖 > yaml > 默认；写后新 run 生效）。
  runtimeSetting: {
    title: '运行时配置',
    tabText: '运行时配置',
    idKey: 'settingKey',
    cardList: true,
    cardKind: 'setting',
    noCreate: true,
    listPath: '/setting/subagent/get',
    columns: [],
  },
};

const management = {
  type: 'tool',
  items: [],
  draft: null,
  mode: 'create',
  expandedMcp: new Set(),
  mcpConnection: '',
  init() {
    $('management-tabs').innerHTML = Object.entries(resources).map(([key, resource]) => (
      `<button class="rp-button rp-management-tab" data-type="${key}" type="button">${resource.tabText ?? resource.title.replace('管理', '')}</button>`
    )).join('');
    $('management-tabs').addEventListener('click', (event) => {
      const button = event.target.closest('[data-type]');
      if (!button) return;
      this.type = button.dataset.type;
      this.mcpConnection = '';
      this.renderShell();
      this.reload();
    });
    $('management-keyword').addEventListener('input', () => this.renderTable());
    $('management-status').addEventListener('change', () => this.renderTable());
    $('management-caller-filter').addEventListener('change', () => this.reload());
    this.loadCallerFilterOptions();
    $('management-mcp-filter').addEventListener('change', () => {
      this.mcpConnection = $('management-mcp-filter').value;
      this.renderTable();
    });
    $('management-add').addEventListener('click', () => this.openCreate());
    $('management-import').addEventListener('click', () => this.openImport());
    $('management-gwadmin').addEventListener('click', () => window.open('/react-base-service/react/mcp-admin', '_blank'));
    $('management-refresh').addEventListener('click', () => this.refresh());
    $('management-modal-body').addEventListener('click', (event) => {
      const logsAction = event.target.closest('[data-logs-action]')?.dataset.logsAction;
      if (logsAction) {
        if (logsAction === 'prev') this.logsState.page = Math.max(1, this.logsState.page - 1);
        if (logsAction === 'next') this.logsState.page += 1;
        this.loadMcpAppLogs();
        return;
      }
      const grantsAction = event.target.closest('[data-grants-action]')?.dataset.grantsAction;
      if (grantsAction) {
        $('management-modal-body').querySelectorAll('[data-grant-tool]').forEach((box) => {
          box.checked = grantsAction === 'all';
        });
      }
    });
    $('management-modal-close').addEventListener('click', () => this.closeModal());
    $('management-modal-cancel').addEventListener('click', () => this.closeModal());
    $('management-modal-save').addEventListener('click', () => this.save());
    $('management-body').addEventListener('click', (event) => this.handleTableClick(event));
    $('management-cards').addEventListener('click', (event) => this.handleTableClick(event));
    // 运行时配置面板的数字输入（data-setting-input=字段名）走 input 事件委托。
    $('management-cards').addEventListener('input', (event) => {
      const field = event.target.closest('[data-setting-input]')?.dataset.settingInput;
      if (!field || !this.settingDraft?.[field]) return;
      this.settingDraft[field].value = event.target.value;
    });
    this.renderShell();
  },
  resource() { return resources[this.type]; },
  config() { return getConfig(); },
  setState(text, isError = false) {
    const el = $('management-state');
    el.textContent = text || '';
    el.classList.toggle('rp-hidden', !text);
    el.classList.toggle('rp-error', isError);
  },
  renderShell() {
    const resource = this.resource();
    const useCards = Boolean(resource.cardList);
    $('management-title').textContent = resource.title;
    $('management-add').textContent = resource.addText;
    $('management-add').hidden = Boolean(resource.noCreate);
    $('management-import').hidden = !resource.importPath;
    document.querySelectorAll('.rp-management-tab').forEach((tab) => tab.classList.toggle('rp-active', tab.dataset.type === this.type));
    $('management-table-wrap').classList.toggle('rp-hidden', useCards);
    $('management-cards').classList.toggle('rp-hidden', !useCards);
    document.querySelector('.rp-caller-filter-field')?.classList.toggle('rp-hidden', !resource.callerFilter);
    document.querySelector('.rp-mcp-filter-field')?.classList.toggle('rp-hidden', !resource.mcpFilter);
    $('management-head').innerHTML = useCards ? '' : `<tr class="rp-table-row">${resource.columns.map(([, label]) => `<th class="rp-table-cell rp-table-header-cell">${label}</th>`).join('')}</tr>`;
    // MCP 相关页提供「网关管理台」入口（mcp-server 风格的独立管理页，
    // 工具批量注册/编辑/上下线/应用授权都在管理台里做）。
    $('management-gwadmin').hidden = !(this.type === 'mcp' || this.type === 'mcpapp');
  },
  // 拉取 caller 清单填充筛选下拉：固定「全部 / 默认」+ 扁平的 caller 列表（平台并入文案）。
  async loadCallerFilterOptions() {
    const select = $('management-caller-filter');
    const current = select.value;
    try {
      const fetched = await post('/caller/list', {});
      const callers = Array.isArray(fetched) ? fetched : [];
      let html = '<option value="">全部</option><option value="default">默认（全 caller 通用）</option>';
      for (const item of callers) {
        const platform = String(item.platform || '').trim();
        const suffix = [
          item.name && item.name !== item.callerKey ? item.name : '',
          platform,
        ].filter(Boolean).map(escapeHtml).join(' · ');
        html += `<option value="${escapeHtml(item.callerKey)}">${escapeHtml(item.callerKey)}${suffix ? `（${suffix}）` : ''}</option>`;
      }
      select.innerHTML = html;
      if ([...select.options].some((option) => option.value === current)) select.value = current;
    } catch (error) {
      this.setState(`Caller 清单加载失败：${error.message || error}`, true);
    }
  },
  async reload() {
    const resource = this.resource();
    const config = this.config();
    this.setState('加载中...');
    try {
      // Caller 筛选资源：选中值优先（空 = 全部 caller），未启用筛选的资源沿用全局 callerKey。
      const callerKey = resource.callerFilter ? callerFilterValue() : config.callerKey;
      const data = await post(resource.listPath, { callerKey, routeValues: config.routeValues });
      // 运行时配置返回单个生效视图对象而非列表，单独落 draft。
      if (resource.cardKind === 'setting') {
        this.settingData = data;
        this.setState('');
        this.renderTable();
        return;
      }
      this.items = Array.isArray(data) ? data.map(resource.toDraft) : [];
      this.setState('');
      this.renderTable();
    } catch (error) {
      this.items = [];
      this.setState(error.message || '加载失败', true);
      this.renderTable();
    }
  },
  // 刷新按钮：MCP 连接页先做一轮重连 + 工具清单同步（描述/入参出参 schema/上下线状态
  // 落库，失联连接的工具会被下线），其余页保持纯列表重拉。
  async refresh() {
    if (this.type !== 'mcp') {
      this.reload();
      return;
    }
    this.setState('正在重新检测 MCP 连接并同步工具清单...');
    try {
      const summary = await post('/react/mcp/refresh', { callerKey: this.config().callerKey });
      await this.reload();
      const entries = Array.isArray(summary?.results) ? summary.results : [];
      const failed = entries.filter((item) => item.status === 'disconnected');
      const skipped = entries.filter((item) => item.status === 'skipped').length;
      const suffix = skipped ? `，${skipped} 个已停用跳过` : '';
      if (!failed.length) {
        this.setState(`刷新完成：${summary?.connected ?? 0}/${summary?.checked ?? 0} 个连接正常${suffix}`);
        return;
      }
      const detail = failed.map((item) => `${item.name}：${shortText(item.message || '连接失败', 80)}`).join('；');
      this.setState(`刷新完成：${failed.length} 个连接失联（${detail}），其工具已下线${suffix}`, true);
    } catch (error) {
      this.setState(error.message || '刷新失败', true);
      this.renderTable();
    }
  },
  filteredItems() {
    const keyword = $('management-keyword').value.trim().toLowerCase();
    const status = $('management-status').value;
    const mcpConnection = this.resource().mcpFilter ? (this.mcpConnection || '') : '';
    return this.items.filter((item) => {
      const text = JSON.stringify(item).toLowerCase();
      const statusMatched = status === 'all' || (status === 'enabled' ? isEnabled(item) : !isEnabled(item));
      const mcpMatched = !mcpConnection || (item.toolType === 'mcp' && item.config?.mcpServer === mcpConnection);
      return (!keyword || text.includes(keyword)) && statusMatched && mcpMatched;
    });
  },
  // 按当前数据里出现的 MCP 连接名刷新「MCP 连接」筛选下拉（基于未过滤的全量 items，避免选中后选项塌缩）。
  refreshMcpConnectionOptions() {
    const select = $('management-mcp-filter');
    if (!select || !this.resource().mcpFilter) return;
    const names = [...new Set(this.items.filter((item) => item.toolType === 'mcp' && item.config?.mcpServer).map((item) => item.config.mcpServer))];
    const current = this.mcpConnection;
    select.innerHTML = '<option value="">全部</option>' + names.map((name) => `<option value="${escapeHtml(name)}">${escapeHtml(name)}</option>`).join('');
    this.mcpConnection = names.includes(current) ? current : '';
    select.value = this.mcpConnection;
  },
  renderValue(key, item) {
    if (key === 'actions') {
      // MCP 工具随连接生命周期管理：不可单独删除（修改仍可用，但配置 JSON 只读）。
      const deleteButton = this.resource().deletePath && item.toolType !== 'mcp'
        ? '<button class="rp-button rp-link-btn rp-danger-link" data-action="delete" type="button">删除</button>'
        : '';
      return `<div class="rp-row-actions"><button class="rp-button rp-link-btn" data-action="edit" type="button">修改</button>${deleteButton}</div>`;
    }
    if (key === 'status') return `<button class="rp-button rp-switch ${isEnabled(item) ? 'rp-on' : ''}" data-action="toggle" type="button" title="${isEnabled(item) ? '启用' : '停用'}"></button>`;
    if (key === 'planEnabled') return `<button class="rp-button rp-switch ${isPlanEnabled(item) ? 'rp-on' : ''}" data-action="toggle-plan" type="button" title="Plan ${isPlanEnabled(item) ? '启用' : '停用'}"></button>`;
    if (key === 'callerKey' && this.resource().callerFilter) return item.callerKey === 'default' ? '<span class="rp-mcp-badge rp-mcp-badge-ok" title="挂在 default 伪 caller 下，全部 caller 的请求都能解析到">默认（全 caller）</span>' : escapeHtml(shortText(item.callerKey, 24));
    if (key === 'toolType' && item.toolType === 'mcp') {
      const server = item.config?.mcpServer || '';
      return `<span class="rp-mcp-kind-badge" title="MCP 连接：${escapeHtml(server)}">MCP·${escapeHtml(server)}</span>`;
    }
    if (key === 'routeValues') return escapeHtml(Array.isArray(item.routeValues) && item.routeValues.length ? item.routeValues.join(',') : '[]');
    if (key === 'config') return escapeHtml(shortText(item.configText ?? item.config, 64));
    return escapeHtml(shortText(item[key], 72));
  },
  renderTable() {
    const resource = this.resource();
    if (resource.cardKind === 'setting') {
      this.renderSettingPanel();
      return;
    }
    if (resource.cardKind === 'bundle') {
      this.renderBundleCards();
      return;
    }
    if (resource.cardKind === 'workspace') {
      this.renderWorkspaceCards();
      return;
    }
    if (resource.cardKind === 'mcpapp') {
      this.renderMcpAppCards();
      return;
    }
    if (resource.cardList) {
      this.renderMcpCards();
      return;
    }
    const rows = this.filteredItems();
    this.refreshMcpConnectionOptions();
    if (!rows.length) {
      $('management-body').innerHTML = `<tr class="rp-table-row"><td class="rp-table-cell rp-table-empty-cell" colspan="${resource.columns.length}">暂无数据</td></tr>`;
      return;
    }
    $('management-body').innerHTML = rows.map((item, index) => (
      `<tr class="rp-table-row" data-index="${index}">${resource.columns.map(([key]) => `<td class="rp-table-cell" title="${escapeHtml(typeof item[key] === 'object' ? JSON.stringify(item[key] ?? '') : item[key] ?? '')}">${this.renderValue(key, item)}</td>`).join('')}</tr>`
    )).join('');
    this.visibleItems = rows;
  },
  // MCP 连接卡片列表：每张卡片 = 头部（名称/传输/状态/端点/工具数/操作）+ 可展开工具清单。
  renderMcpCards() {
    const rows = this.filteredItems();
    const container = $('management-cards');
    if (!rows.length) {
      container.innerHTML = '<div class="rp-mcp-empty">暂无 MCP 连接，点击「手动配置」粘贴 mcpServers JSON 登记</div>';
      this.visibleItems = rows;
      return;
    }
    container.innerHTML = rows.map((item, index) => this.renderMcpCard(item, index)).join('');
    this.visibleItems = rows;
    // 刷新后展开态保留但工具明细丢失（列表接口不返回 tools），异步补拉详情。
    rows.forEach((item) => {
      if (this.expandedMcp.has(item.serverId) && !Array.isArray(item.tools)) {
        this.loadMcpTools(item);
      }
    });
  },
  async loadMcpTools(item) {
    try {
      const detail = await post('/react/mcp/detail', { callerKey: this.config().callerKey, serverId: item.serverId });
      item.tools = detail?.server?.tools ?? [];
    } catch (error) {
      this.setState(error.message || '加载工具清单失败', true);
      return;
    }
    if (this.type === 'mcp') this.renderTable();
  },
  renderMcpCard(item, index) {
    const enabled = isEnabled(item);
    const expanded = this.expandedMcp.has(item.serverId);
    const tools = Array.isArray(item.tools) ? item.tools : [];
    // 工具同步目标 caller：属主（绿）+ 绑定 caller（蓝）。
    const boundCallers = Array.isArray(item.boundCallers) ? item.boundCallers : [];
    const boundLine = boundCallers.length
      ? `<div class="rp-mcp-bound">同步 caller：${boundCallers.map((caller, i) => `<span class="rp-mcp-bound-chip${i === 0 ? ' rp-mcp-bound-owner' : ''}" title="${i === 0 ? '属主 caller（登记方，恒定生效）' : '绑定 caller'}">${escapeHtml(caller)}</span>`).join('')}</div>`
      : '';
    return `
      <div class="rp-mcp-card${enabled ? '' : ' rp-mcp-disabled'}${expanded ? ' rp-expanded' : ''}" data-index="${index}">
        <div class="rp-mcp-card-head" data-action="expand" title="展开/收起工具清单">
          <span class="rp-mcp-chevron" aria-hidden="true">▸</span>
          <span class="rp-mcp-name">${escapeHtml(item.name)}</span>
          <span class="rp-mcp-kind">${escapeHtml(item.kind)}</span>
          ${mcpStatusBadge(item)}
          <span class="rp-mcp-endpoint" title="${escapeHtml(item.endpoint)}">${escapeHtml(shortText(item.endpoint, 36))}</span>
          <span class="rp-mcp-toolcount">${item.toolCount ?? tools.length} 工具</span>
          <div class="rp-row-actions rp-mcp-actions">
            <button class="rp-button rp-link-btn" data-action="connect" type="button">测试连接</button>
            <button class="rp-button rp-link-btn" data-action="edit" type="button">修改</button>
            <button class="rp-button rp-link-btn rp-danger-link" data-action="delete" type="button">删除</button>
            <button class="rp-button rp-switch ${enabled ? 'rp-on' : ''}" data-action="toggle" type="button" title="${enabled ? '停用' : '启用'}"></button>
          </div>
        </div>
        <div class="rp-mcp-card-body">
          ${boundLine}
          ${tools.length ? `<div class="rp-mcp-tools-grid">${tools.map((tool) => `
            <div class="rp-mcp-tool-item">
              <span class="rp-mcp-tool-name">${escapeHtml(tool.name)}</span>
              <span class="rp-mcp-tool-desc">${escapeHtml(tool.description || '暂无描述')}</span>
            </div>`).join('')}</div>`
    : '<div class="rp-mcp-tools-empty">尚未同步到工具清单，点击「测试连接」拉取。</div>'}
        </div>
      </div>`;
  },
  async toggleMcpExpand(event) {
    const item = this.itemFromEvent(event);
    if (!item) return;
    if (this.expandedMcp.has(item.serverId)) {
      this.expandedMcp.delete(item.serverId);
      this.renderTable();
      return;
    }
    this.expandedMcp.add(item.serverId);
    // 列表不带工具明细，首次展开时按需拉取详情。
    if (!Array.isArray(item.tools)) {
      await this.loadMcpTools(item);
      return;
    }
    this.renderTable();
  },
  // MCP 应用（网关接入凭证）卡片：名称/key/打码 secret/绑定工具数/接入端点/操作。
  renderMcpAppCards() {
    const rows = this.filteredItems();
    const container = $('management-cards');
    if (!rows.length) {
      container.innerHTML = '<div class="rp-mcp-empty">暂无 MCP 应用。新增应用获得 appKey/appSecret 后，外部 MCP 客户端即可经 /mcp 端点接入（Bearer appKey:appSecret）</div>';
      this.visibleItems = rows;
      return;
    }
    container.innerHTML = rows.map((item, index) => `
      <div class="rp-mcp-card${isEnabled(item) ? '' : ' rp-mcp-disabled'}" data-index="${index}">
        <div class="rp-mcp-card-head">
          <span class="rp-mcp-name">${escapeHtml(item.appName)}</span>
          <span class="rp-mcp-kind">工具×${escapeHtml(String(item.grantedToolCount ?? 0))}</span>
          <span class="rp-mcp-badge ${isEnabled(item) ? 'rp-mcp-badge-ok' : 'rp-mcp-badge-off'}">${isEnabled(item) ? '启用' : '停用'}</span>
          <span class="rp-mcp-endpoint" title="外部 MCP 客户端接入端点">${escapeHtml(item.endpoint)}</span>
          <div class="rp-row-actions rp-mcp-actions">
            <button class="rp-button rp-link-btn" data-action="copy-key" type="button" title="复制 appKey">key:${escapeHtml(shortText(item.appKey, 14))}</button>
            <button class="rp-button rp-link-btn" data-action="grants" type="button">工具授权</button>
            <button class="rp-button rp-link-btn" data-action="reset-secret" type="button">重置密钥</button>
            <button class="rp-button rp-link-btn" data-action="logs" type="button">调用记录</button>
            <button class="rp-button rp-link-btn" data-action="edit" type="button">修改</button>
            <button class="rp-button rp-link-btn rp-danger-link" data-action="delete" type="button">删除</button>
            <button class="rp-button rp-switch ${isEnabled(item) ? 'rp-on' : ''}" data-action="toggle" type="button" title="${isEnabled(item) ? '停用' : '启用'}"></button>
          </div>
        </div>
        <div class="rp-mcp-card-body">
          <div class="rp-mcp-bound">appKey：<code>${escapeHtml(item.appKey)}</code> · secret：<code>${escapeHtml(item.maskedSecret)}</code> · 接入头：<code>Authorization: Bearer &lt;appKey&gt;:&lt;appSecret&gt;</code></div>
          <div class="rp-mcp-bound">已绑定工具：<span class="rp-mcp-badge ${Number(item.grantedToolCount) > 0 ? 'rp-mcp-badge-ok' : 'rp-mcp-badge-bad'}">${item.grantedToolCount ?? 0} 个</span>${Number(item.grantedToolCount) > 0 ? '' : '（未绑定任何工具，该连接 tools/list 为空，点击「工具授权」勾选）'}</div>
        </div>
      </div>`).join('');
    this.visibleItems = rows;
  },
  async resetMcpAppSecret(item) {
    if (!confirm(`确认重置「${item.appName}」的密钥吗？旧凭证立即失效，持有它的客户端会失联。`)) return;
    this.setState('正在重置密钥...');
    try {
      const data = await post('/react/mcpapp/reset_secret', { appId: item.appId });
      await this.reload();
      this.showSecretOnce(item, data?.appSecret ?? '');
      this.setState('密钥已重置，请立即保存新的 appSecret（仅本次展示）');
    } catch (error) {
      this.setState(error.message || '重置失败', true);
    }
  },
  // showSecretOnce 用管理弹窗一次性展示完整 secret（关闭后不可再查）。
  showSecretOnce(app, secret) {
    this.mode = 'secretOnce';
    this.draft = { appName: app.appName ?? '', appKey: app.appKey ?? '', appSecret: secret };
    this.renderModal();
  },
  async openMcpAppLogs(item) {
    this.mode = 'mcpLogs';
    this.draft = null;
    this.logsState = { appKey: item.appKey, appName: item.appName, page: 1, pageSize: 10, data: null };
    this.renderModal();
    await this.loadMcpAppLogs();
  },
  async loadMcpAppLogs() {
    const state = this.logsState;
    if (!state) return;
    try {
      state.data = await post('/react/mcpapp/logs', { appKey: state.appKey, page: state.page, pageSize: state.pageSize });
      if (this.mode === 'mcpLogs') this.renderModal();
    } catch (error) {
      this.setState(error.message || '加载调用记录失败', true);
    }
  },
  renderMcpAppLogsBody() {
    const state = this.logsState;
    if (!state) return '';
    const logs = Array.isArray(state.data?.logs) ? state.data.logs : [];
    const total = Number(state.data?.total ?? 0);
    const pages = Math.max(1, Math.ceil(total / state.pageSize));
    if (!logs.length) {
      return `<div class="rp-mcp-empty">该应用暂无调用记录（tools/call 审计异步落库，稍等 1 秒后刷新）</div>`;
    }
    const rowsHtml = logs.map((entry) => `
      <tr class="rp-table-row">
        <td class="rp-table-cell" title="${escapeHtml(entry.createdAt)}">${escapeHtml(String(entry.createdAt ?? '').slice(5))}</td>
        <td class="rp-table-cell">${escapeHtml(entry.toolName)}${entry.userName ? `（${escapeHtml(entry.userName)}）` : ''}</td>
        <td class="rp-table-cell">${entry.resultCode === 0 ? '<span class="rp-mcp-badge rp-mcp-badge-ok">成功</span>' : `<span class="rp-mcp-badge rp-mcp-badge-bad" title="${escapeHtml(entry.errorMsg)}">失败 ${entry.resultCode}</span>`}</td>
        <td class="rp-table-cell">${entry.costMs}ms</td>
        <td class="rp-table-cell" title="${escapeHtml(entry.arguments ?? '')}">${escapeHtml(shortText(entry.arguments, 26))}</td>
        <td class="rp-table-cell" title="${escapeHtml(entry.responseText ?? '')}">${escapeHtml(shortText(entry.responseText, 26))}</td>
      </tr>`).join('');
    return `
      <div class="rp-mcp-bound">应用「${escapeHtml(state.appName)}」调用审计（appKey: ${escapeHtml(state.appKey)}）</div>
      <table class="rp-management-table"><thead class="rp-table-head"><tr class="rp-table-row">
        <th class="rp-table-cell rp-table-header-cell">时间</th><th class="rp-table-cell rp-table-header-cell">工具</th>
        <th class="rp-table-cell rp-table-header-cell">结果</th><th class="rp-table-cell rp-table-header-cell">耗时</th>
        <th class="rp-table-cell rp-table-header-cell">入参</th><th class="rp-table-cell rp-table-header-cell">响应</th>
      </tr></thead><tbody>${rowsHtml}</tbody></table>
      <div class="rp-row-actions" style="margin-top:8px;justify-content:flex-end">
        <button class="rp-button" data-logs-action="prev" type="button" ${state.page <= 1 ? 'disabled' : ''}>上一页</button>
        <span class="rp-mcp-endpoint">第 ${state.page}/${pages} 页 · 共 ${total} 条</span>
        <button class="rp-button" data-logs-action="next" type="button" ${state.page >= pages ? 'disabled' : ''}>下一页</button>
      </div>`;
  },
  // 工具授权：勾选绑定工具基础集合中的 http 工具（显式白名单，空=该连接看不到任何工具）。
  async openMcpAppGrants(item) {
    this.mode = 'mcpGrants';
    this.draft = null;
    this.grantsState = { app: item, data: null };
    this.renderModal();
    try {
      this.grantsState.data = await post('/react/mcpapp/list_tools', { appId: item.appId });
      if (this.mode === 'mcpGrants') this.renderModal();
    } catch (error) {
      this.setState(error.message || '加载可绑定工具失败', true);
    }
  },
  renderMcpAppGrantsBody() {
    const state = this.grantsState;
    if (!state) return '';
    const tools = Array.isArray(state.data?.tools) ? state.data.tools : [];
    if (!tools.length) {
      return '<div class="rp-mcp-empty">工具基础集合中暂无 http 工具，先到「工具管理」或网关管理台注册。</div>';
    }
    const rowsHtml = tools.map((tool) => `
      <label class="rp-field rp-span-all" style="flex-direction:row;align-items:center;gap:8px">
        <input type="checkbox" data-grant-tool="${escapeHtml(tool.toolId)}" ${tool.granted ? 'checked' : ''} />
        <span style="flex:1"><b>${escapeHtml(tool.name)}</b> <span class="rp-mcp-endpoint">${escapeHtml(tool.callerKey)}</span>${Number(tool.status) !== 1 ? ' <span class="rp-mcp-badge rp-mcp-badge-off">已下线</span>' : ''}${tool.description ? ` — ${escapeHtml(shortText(tool.description, 60))}` : ''}</span>
      </label>`).join('');
    const grantedCount = tools.filter((tool) => tool.granted).length;
    return `
      <div class="rp-operation-note">显式白名单：只有勾选的工具会出现在该应用 MCP 连接的 tools/list 里（已下线工具须重新上线后才对外可见）。全不勾=清空绑定。</div>
      <div class="rp-row-actions" style="margin-bottom:8px">
        <button class="rp-button" data-grants-action="all" type="button">全选</button>
        <button class="rp-button" data-grants-action="none" type="button">清空</button>
        <span class="rp-mcp-endpoint">当前绑定 ${grantedCount}/${tools.length} 个</span>
      </div>
      <div class="rp-form-grid">${rowsHtml}</div>`;
  },
  async saveMcpAppGrants() {
    const state = this.grantsState;
    if (!state) return;
    const toolIds = [...$('management-modal-body').querySelectorAll('[data-grant-tool]:checked')].map((box) => box.dataset.grantTool);
    try {
      await post('/react/mcpapp/grant_tools', { appId: state.app.appId, toolIds });
      this.closeModal();
      await this.reload();
      this.setState(`已更新绑定：${toolIds.length} 个工具（${state.app.appName}）`);
    } catch (error) {
      this.setState(error.message || '保存绑定失败', true);
    }
  },
  // P3 Bundle 已装卡片：名称/版本/来源与钉住的 commit/资源计数/卸载。
  renderBundleCards() {
    const rows = this.filteredItems();
    const container = $('management-cards');
    if (!rows.length) {
      container.innerHTML = '<div class="rp-mcp-empty">暂无已安装 Bundle，点击「安装 Bundle」从白名单来源安装插件包</div>';
      this.visibleItems = rows;
      return;
    }
    container.innerHTML = rows.map((item, index) => `
      <div class="rp-mcp-card" data-index="${index}">
        <div class="rp-mcp-card-head">
          <span class="rp-mcp-name">${escapeHtml(item.name)}</span>
          <span class="rp-mcp-kind">v${escapeHtml(item.version || '1.0.0')}</span>
          <span class="rp-mcp-endpoint" title="${escapeHtml(item.source)}${item.resolvedRef ? `@${escapeHtml(item.resolvedRef)}` : ''}">${escapeHtml(shortText(item.source, 40))}${item.resolvedRef ? ` @${escapeHtml(shortText(item.resolvedRef, 8))}` : ''}</span>
          <span class="rp-mcp-toolcount">${item.agentCount ?? 0} agent · ${item.skillCount ?? 0} skill · ${item.mcpServerCount ?? 0} mcp</span>
          <div class="rp-row-actions rp-mcp-actions">
            <button class="rp-button rp-link-btn rp-danger-link" data-action="uninstall" type="button">卸载（回滚）</button>
          </div>
        </div>
        <div class="rp-mcp-card-body">
          ${item.description ? `<div class="rp-mcp-bound">${escapeHtml(item.description)}</div>` : ''}
          <div class="rp-mcp-bound">安装人：${escapeHtml(item.installedBy || '-')} · ${escapeHtml(item.installedAt || '')}</div>
        </div>
      </div>`).join('');
    this.visibleItems = rows;
  },
  // P3 workspace 运行视图：活跃 worktree 只读卡片。
  renderWorkspaceCards() {
    const rows = this.filteredItems();
    const container = $('management-cards');
    if (!rows.length) {
      container.innerHTML = '<div class="rp-mcp-empty">当前没有活跃的代码工作区（run 结束即释放，空列表为正常态）</div>';
      this.visibleItems = rows;
      return;
    }
    container.innerHTML = rows.map((item, index) => `
      <div class="rp-mcp-card" data-index="${index}">
        <div class="rp-mcp-card-head">
          <span class="rp-mcp-name">${escapeHtml(item.service)}${item.env ? `（${escapeHtml(item.env)}）` : ''}</span>
          <span class="rp-mcp-kind">${escapeHtml(shortText(item.commit, 10))}</span>
          <span class="rp-mcp-endpoint" title="${escapeHtml(item.path)}">${escapeHtml(shortText(item.path, 44))}</span>
          <span class="rp-mcp-toolcount">${escapeHtml(item.tools || '')}</span>
        </div>
        <div class="rp-mcp-card-body">
          <div class="rp-mcp-bound">runId：${escapeHtml(item.runId)} · caller：${escapeHtml(item.callerKey)} · 挂载于 ${escapeHtml(item.loadedAt ? String(item.loadedAt).replace('T', ' ').slice(0, 19) : '-')}</div>
        </div>
      </div>`).join('');
    this.visibleItems = rows;
  },
  // 运行时配置面板：DB 覆盖 > custom.yaml > 内置默认。每字段「覆盖开关 + 值控件 + 来源徽标」，
  // 覆盖关闭 = 该字段回落 yaml/默认（保存时进 clearFields）；来源徽标展示当前生效值从哪来。
  renderSettingPanel() {
    const container = $('management-cards');
    const data = this.settingData;
    if (!data) {
      container.innerHTML = '<div class="rp-mcp-empty">暂无配置数据</div>';
      return;
    }
    if (!this.settingDraft || this.settingDraftKey !== 'subagent') {
      const override = data.override ?? {};
      this.settingDraftKey = 'subagent';
      this.settingDraft = {
        enabled: { useOverride: override.enabled != null, value: override.enabled ?? data.effective.enabled },
        maxParallel: { useOverride: override.maxParallel != null, value: override.maxParallel ?? data.effective.maxParallel },
        defaultMaxSteps: { useOverride: override.defaultMaxSteps != null, value: override.defaultMaxSteps ?? data.effective.defaultMaxSteps },
        maxDepth: { useOverride: override.maxDepth != null, value: override.maxDepth ?? data.effective.maxDepth },
      };
    }
    const draft = this.settingDraft;
    const sourceLabel = { override: '覆盖', yaml: 'yaml', default: '默认' };
    const rows = [
      { field: 'enabled', label: '委派总开关', hint: 'subagent.enabled：开启后主 Agent 才会装配 delegate_agent 工具', type: 'switch', range: '' },
      { field: 'maxParallel', label: '并行上限', hint: '同一轮多个 delegate_agent 调用的并行度（1-8）', type: 'number', range: '1-8' },
      { field: 'defaultMaxSteps', label: '子 run 默认步数上限', hint: 'agent 未配置 max_steps 时的子 run 步数上限（1-64）', type: 'number', range: '1-64' },
      { field: 'maxDepth', label: '委派嵌套深度上限', hint: '子 Agent 再委派的最大深度，防递归失控（1-4）', type: 'number', range: '1-4' },
    ].map(({ field, label, hint, type, range }) => {
      const state = draft[field];
      const source = data.sources?.[field]?.source ?? 'default';
      const effective = data.effective[field];
      const control = type === 'switch'
        ? `<button class="rp-button rp-switch ${state.value ? 'rp-on' : ''}" data-action="setting-switch" data-field="${field}" type="button" title="${state.value ? '开启' : '关闭'}" ${state.useOverride ? '' : 'disabled'}></button>`
        : `<input class="rp-setting-input" data-setting-input="${field}" type="number" min="1" value="${state.value}" placeholder="${effective}" ${state.useOverride ? '' : 'disabled'}>`;
      return `
      <div class="rp-setting-row">
        <label class="rp-button rp-switch rp-smaller-switch ${state.useOverride ? 'rp-on' : ''}" data-action="setting-override" data-field="${field}" type="button" title="${state.useOverride ? '覆盖中：保存时提交面板值' : '未覆盖：保存时清除覆盖，回落 yaml/默认值'}"></label>
        <span class="rp-setting-label">${escapeHtml(label)}<span class="rp-setting-hint">${escapeHtml(hint)}${range ? `（${range}）` : ''}</span></span>
        ${control}
        <span class="rp-mcp-badge ${source === 'override' ? 'rp-mcp-badge-ok' : 'rp-mcp-badge-off'}" title="当前生效值来源：${source === 'override' ? 'DB 覆盖（本面板可改）' : source === 'yaml' ? 'custom.yaml 配置' : '内置默认值'}">${sourceLabel[source] || source}·生效 ${type === 'switch' ? (effective ? '开' : '关') : effective}</span>
      </div>`;
    }).join('');
    const audit = data.updatedBy
      ? `<span class="rp-mcp-endpoint">最近更新：${escapeHtml(data.updatedBy)} · ${escapeHtml(data.updatedAt || '-')}</span>`
      : '<span class="rp-mcp-endpoint">尚未通过面板设置过（当前全部回落 yaml/默认值）</span>';
    container.innerHTML = `
      <div class="rp-mcp-card">
        <div class="rp-mcp-card-head">
          <span class="rp-mcp-name">subagent 委派策略</span>
          <span class="rp-mcp-kind">delegate_agent</span>
          ${data.effective.enabled ? '<span class="rp-mcp-badge rp-mcp-badge-ok">已开启</span>' : '<span class="rp-mcp-badge rp-mcp-badge-off">已关闭</span>'}
          ${audit}
        </div>
        <div class="rp-mcp-card-body">
          <div class="rp-setting-rows">${rows}</div>
          <div class="rp-setting-note">每行左侧小开关 = 「覆盖」：开启时保存面板值进 DB，关闭时清除该字段覆盖、回落 custom.yaml / 默认值。保存后新 run / 下一次委派即生效，运行中的 run 不受影响；多实例部署约 10 秒内拉平。</div>
          <div class="rp-setting-actions">
            <button class="rp-button rp-primary-btn" data-action="setting-save" type="button">保存</button>
            <button class="rp-button" data-action="setting-reload" type="button">放弃修改</button>
          </div>
        </div>
      </div>`;
  },
  handleSettingClick(event, action) {
    const field = event.target.closest('[data-field]')?.dataset.field;
    if (action === 'setting-override' && field) {
      const state = this.settingDraft[field];
      state.useOverride = !state.useOverride;
      // 打开覆盖时以当前生效值/DB 覆盖值为起点，避免一打开就提交出界值。
      if (state.useOverride && (state.value == null || state.value === '')) {
        state.value = this.settingData.effective[field];
      }
      this.renderSettingPanel();
      return;
    }
    if (action === 'setting-switch' && field) {
      this.settingDraft[field].value = !this.settingDraft[field].value;
      this.renderSettingPanel();
      return;
    }
    if (action === 'setting-reload') {
      this.settingDraft = null;
      this.reload();
      return;
    }
    if (action === 'setting-save') {
      this.saveSetting();
    }
  },
  async saveSetting() {
    const draft = this.settingDraft;
    const payload = { clearFields: [] };
    for (const [field, state] of Object.entries(draft)) {
      if (!state.useOverride) {
        payload.clearFields.push(field);
        continue;
      }
      if (field === 'enabled') {
        payload.enabled = Boolean(state.value);
        continue;
      }
      const num = Number(state.value);
      if (!Number.isFinite(num) || num < 1) {
        this.setState(`${field} 需要填写 ≥1 的数字`, true);
        return;
      }
      payload[field] = num;
    }
    this.setState('保存中...');
    try {
      const data = await post('/setting/subagent/update', payload);
      this.settingData = data;
      this.settingDraft = null;
      this.setState('已保存（新 run / 下一次委派生效）');
      this.renderSettingPanel();
    } catch (error) {
      this.setState(error.message || '保存失败', true);
    }
  },
  async uninstallBundle(item) {
    if (!confirm(`确认卸载 Bundle「${item.name}」吗？安装时新建的资源将被删除，覆盖的资源会恢复到安装前状态。`)) return;
    this.setState(`正在卸载 ${item.name}...`);
    try {
      await post('/react/bundle/uninstall', { name: item.name });
      await this.reload();
      this.setState(`${item.name} 已卸载并回滚`);
    } catch (error) {
      this.setState(error.message || '卸载失败', true);
    }
  },
  async connectMcp(item) {
    this.setState(`正在连接 ${item.name}...`);
    try {
      const result = await post('/react/mcp/connect', { callerKey: this.config().callerKey, serverId: item.serverId });
      await this.reload();
      this.expandedMcp.add(item.serverId);
      this.setState(`${item.name} 连接成功，已同步 ${result?.toolCount ?? 0} 个工具`);
      this.renderTable();
    } catch (error) {
      // 连接失败时工具已被后端下线，重拉列表让卡片状态与工具数立即反映现状。
      await this.reload();
      this.expandedMcp.add(item.serverId);
      this.setState(`${error.message || '连接失败'}（该连接的工具已下线，恢复连接后重新测试即可）`, true);
      this.renderTable();
    }
  },
  itemFromEvent(event) {
    const row = event.target.closest('[data-index]');
    if (!row) return null;
    return this.visibleItems[Number(row.dataset.index)];
  },
  handleTableClick(event) {
    const action = event.target.closest('[data-action]')?.dataset.action;
    if (!action) return;
    // 运行时配置面板的行内控件没有 data-index（非列表项），先于通用行分发处理。
    if (this.type === 'runtimeSetting') {
      this.handleSettingClick(event, action);
      return;
    }
    const item = this.itemFromEvent(event);
    if (!item) return;
    if (action === 'edit') this.openEdit(item);
    if (action === 'delete') this.remove(item);
    if (action === 'toggle') this.toggle(item);
    if (action === 'toggle-plan') this.togglePlan(item);
    if (action === 'expand') this.toggleMcpExpand(event);
    if (action === 'connect') this.connectMcp(item);
    if (action === 'uninstall') this.uninstallBundle(item);
    if (action === 'reset-secret') this.resetMcpAppSecret(item);
    if (action === 'logs') this.openMcpAppLogs(item);
    if (action === 'grants') this.openMcpAppGrants(item);
    if (action === 'copy-key') {
      navigator.clipboard?.writeText(item.appKey ?? '');
      this.setState(`appKey 已复制：${item.appKey}`);
    }
  },
  openCreate() {
    // Bundle 的「新增」即安装表单。
    if (this.type === 'bundle') {
      this.openBundleInstall();
      return;
    }
    this.mode = 'create';
    this.draft = this.resource().empty();
    this.renderModal();
  },
  // 通用「导入定义」入口（agent / skill 的 Markdown 粘贴导入）。
  openImport() {
    const resource = this.resource();
    if (!resource.importPath) return;
    this.mode = 'import';
    this.draft = {
      markdown: '',
      callerKey: callerFilterValue() || this.config().callerKey || 'default',
      routeText: createDefaultRouteText(),
    };
    this.renderModal();
  },
  openBundleInstall() {
    this.mode = 'bundleInstall';
    this.draft = {
      source: '',
      ref: '',
      repoPath: '',
      callerKey: callerFilterValue() || this.config().callerKey || 'default',
      routeText: createDefaultRouteText(),
    };
    this.renderModal();
  },
  async openEdit(item) {
    const resourceType = this.type;
    const resource = this.resource();
    this.mode = 'edit';
    this.setState(resource.loadDetail ? '正在加载模板详情...' : '');
    try {
      const draft = resource.loadDetail
        ? await resource.loadDetail(item, this.config(), resource)
        : { ...item };
      if (this.type !== resourceType) return;
      this.draft = draft;
      this.setState('');
      this.renderModal();
    } catch (error) {
      if (this.type !== resourceType) return;
      this.draft = null;
      this.setState(error.message || '加载详情失败', true);
    }
  },
  closeModal() {
    this.draft = null;
    $('management-modal-mask').classList.remove('rp-visible');
  },
  renderModal() {
    const resource = this.resource();
    const saveButton = $('management-modal-save');
    const cancelButton = $('management-modal-cancel');
    // 自定义模式：MCP 应用密钥一次性展示 / 调用审计分页 / 工具批量注册（多草稿）。
    if (this.mode === 'secretOnce') {
      $('management-modal-title').textContent = '应用密钥（仅本次展示）';
      $('management-modal-body').innerHTML = `
        <div class="rp-operation-note">请立即保存 appKey/appSecret（关闭后 secret 不可再查询，遗失只能重置）。外部 MCP 客户端接入：URL 填服务地址 <code>/react-base-service/mcp</code>，请求头 <code>Authorization: Bearer &lt;appKey&gt;:&lt;appSecret&gt;</code>。</div>
        <div class="rp-form-grid">
          <label class="rp-field"><span class="rp-field-label">应用名称</span><input class="rp-control rp-control-size-default" value="${escapeHtml(this.draft.appName)}" readonly disabled /></label>
          <label class="rp-field"><span class="rp-field-label">appKey</span><input class="rp-control rp-control-size-default" value="${escapeHtml(this.draft.appKey)}" readonly disabled /></label>
          <label class="rp-field rp-span-all"><span class="rp-field-label">appSecret</span><input class="rp-control rp-control-size-default" value="${escapeHtml(this.draft.appSecret)}" readonly /></label>
        </div>`;
      saveButton.hidden = true;
      cancelButton.textContent = '关闭';
      $('management-modal-mask').classList.add('rp-visible');
      return;
    }
    if (this.mode === 'mcpLogs') {
      $('management-modal-title').textContent = 'MCP 调用记录';
      $('management-modal-body').innerHTML = this.renderMcpAppLogsBody();
      saveButton.hidden = true;
      cancelButton.textContent = '关闭';
      $('management-modal-mask').classList.add('rp-visible');
      return;
    }
    if (this.mode === 'mcpGrants') {
      $('management-modal-title').textContent = `工具授权 — ${this.grantsState?.app?.appName ?? ''}`;
      $('management-modal-body').innerHTML = this.renderMcpAppGrantsBody();
      saveButton.hidden = false;
      saveButton.textContent = '保存绑定';
      cancelButton.textContent = '取消';
      $('management-modal-mask').classList.add('rp-visible');
      return;
    }
    saveButton.hidden = false;
    saveButton.textContent = '保存';
    cancelButton.textContent = '取消';
    let fields = typeof resource.fields === 'function' ? resource.fields(this.mode, this.draft) : resource.fields;
    let note = typeof resource.modalNote === 'function' ? resource.modalNote(this.mode, this.draft) : resource.modalNote;
    let title;
    if (this.mode === 'import') {
      fields = [
        ['callerKey', '兜底 caller（frontmatter 缺省时生效）', 'readonly'],
        ['routeText', '兜底路由（逗号分隔）'],
        ['markdown', '定义 Markdown（frontmatter + 正文）', 'codeTextarea'],
      ];
      note = resource.importNote || null;
      title = `导入${resource.itemName ?? resource.title.replace('管理', '')}定义`;
    } else if (this.mode === 'bundleInstall') {
      fields = [
        ['source', '来源（白名单内 git URL 或本地路径）'],
        ['ref', 'git ref（空=HEAD，本地路径忽略）'],
        ['repoPath', '包内子目录（可选）'],
        ['callerKey', '兜底 caller', 'readonly'],
        ['routeText', '兜底路由（逗号分隔）'],
      ];
      note = '安装会把包内 agents/skills/mcp.json 展开写入注册表（同名覆盖）；卸载可整体回滚。来源必须在 llm.react.bundle.allowed_source_prefixes 白名单内。';
      title = '安装 Bundle';
    } else {
      title = this.mode === 'create' ? resource.addText : `编辑${resource.itemName ?? resource.title.replace('管理', '')}`;
    }
    $('management-modal-title').textContent = title;
    $('management-modal-body').innerHTML = `${note ? `<div class="rp-operation-note">${note}</div>` : ''}<div class="rp-form-grid">${fields.map((field) => {
      const [key, label, type] = field;
      const value = this.draft[key] ?? '';
      if (type === 'select') {
        return `<label class="rp-field"><span class="rp-field-label">${label}</span><select class="rp-control rp-control-size-default" data-field="${key}"><option value="1" ${Number(value) === 1 ? 'selected' : ''}>启用</option><option value="0" ${Number(value) === 0 ? 'selected' : ''}>停用</option></select></label>`;
      }
      if (type === 'enumSelect') {
        const options = Array.isArray(field[3]) ? field[3] : [];
        return `<label class="rp-field"><span class="rp-field-label">${label}</span><select class="rp-control rp-control-size-default" data-field="${key}">${options.map(([optionValue, optionLabel]) => `<option value="${escapeHtml(optionValue)}" ${String(value) === String(optionValue) ? 'selected' : ''}>${escapeHtml(optionLabel)}</option>`).join('')}</select></label>`;
      }
      if (type === 'defaultSelect') {
        return `<label class="rp-field"><span class="rp-field-label">${label}</span><select class="rp-control rp-control-size-default" data-field="${key}"><option value="0" ${Number(value) === 0 ? 'selected' : ''}>否</option><option value="1" ${Number(value) === 1 ? 'selected' : ''}>是</option></select></label>`;
      }
      if (type === 'textarea' || type === 'codeTextarea' || type === 'readonlyTextarea') {
        const codeClass = type !== 'textarea' ? ' rp-code-textarea' : '';
        const spellcheck = type !== 'textarea' ? ' spellcheck="false"' : '';
        const frozen = type === 'readonlyTextarea' ? ' readonly disabled' : '';
        return `<label class="rp-field rp-span-all"><span class="rp-field-label">${label}</span><textarea class="rp-control rp-textarea${codeClass}" data-field="${key}"${spellcheck}${frozen}>${escapeHtml(value)}</textarea></label>`;
      }
      if (type === 'password') {
        const placeholder = this.mode === 'edit' ? '留空表示不修改' : '请输入 API Key';
        return `<label class="rp-field rp-span-all"><span class="rp-field-label">${label}</span><input type="password" class="rp-control rp-control-size-default" data-field="${key}" value="" placeholder="${placeholder}" autocomplete="new-password" /></label>`;
      }
      if (type === 'readonly') {
        return `<label class="rp-field"><span class="rp-field-label">${label}</span><input class="rp-control rp-control-size-default" value="${escapeHtml(value)}" readonly disabled /></label>`;
      }
      return `<label class="rp-field"><span class="rp-field-label">${label}</span><input class="rp-control rp-control-size-default" data-field="${key}" value="${escapeHtml(value)}" /></label>`;
    }).join('')}</div>`;
    $('management-modal-mask').classList.add('rp-visible');
  },
  syncDraftFromModal() {
    $('management-modal-body').querySelectorAll('[data-field]').forEach((field) => {
      this.draft[field.dataset.field] = field.value;
    });
  },
  async save() {
    const resource = this.resource();
    this.syncDraftFromModal();
    try {
      // 工具授权弹窗的保存：全量替换应用绑定白名单。
      if (this.mode === 'mcpGrants') {
        await this.saveMcpAppGrants();
        return;
      }
      const config = this.config();
      if (this.mode === 'import') {
        if (!this.draft.markdown || !this.draft.markdown.trim()) throw new Error('请粘贴定义 Markdown');
        await post(resource.importPath, {
          callerKey: this.draft.callerKey || config.callerKey || 'default',
          routeValues: splitRouteText(this.draft.routeText),
          markdown: this.draft.markdown,
        });
        this.closeModal();
        await this.reload();
        return;
      }
      if (this.mode === 'bundleInstall') {
        if (!this.draft.source || !this.draft.source.trim()) throw new Error('请填写安装来源');
        this.setState('正在安装 Bundle...');
        await post('/react/bundle/install', {
          source: this.draft.source.trim(),
          ref: this.draft.ref || '',
          repoPath: this.draft.repoPath || '',
          callerKey: this.draft.callerKey || config.callerKey || 'default',
          routeValues: splitRouteText(this.draft.routeText),
        });
        this.closeModal();
        await this.reload();
        this.setState('Bundle 安装完成');
        return;
      }
      const payload = resource.toPayload(this.draft, config, this.mode);
      const isEdit = this.mode === 'edit' && this.draft[resource.idKey];
      const data = await post(isEdit ? resource.updatePath : resource.createPath, payload);
      // MCP 应用创建成功：完整 secret 仅本次响应返回，弹窗一次性展示。
      if (!isEdit && this.type === 'mcpapp' && data?.appSecret) {
        this.closeModal();
        await this.reload();
        this.showSecretOnce(data.app ?? {}, data.appSecret);
        this.setState('应用已创建，请立即保存 appSecret（仅本次展示）');
        return;
      }
      this.closeModal();
      resource.afterSave?.();
      await this.reload();
    } catch (error) {
      this.setState(error.message || '保存失败', true);
    }
  },
  async remove(item) {
    if (!this.resource().deletePath) return;
    if (item.toolType === 'mcp') return; // MCP 工具由「MCP 连接」管理，不可单独删除
    if (!confirm('确认删除吗？')) return;
    try {
      await post(this.resource().deletePath, this.resource().deleteBody(item));
      await this.reload();
    } catch (error) {
      this.setState(error.message || '删除失败', true);
    }
  },
  async toggle(item) {
    const resource = this.resource();
    try {
      const draft = resource.loadDetail
        ? await resource.loadDetail(item, this.config(), resource)
        : { ...item };
      const payload = resource.toPayload({ ...draft, status: isEnabled(item) ? 0 : 1 }, this.config());
      await post(resource.updatePath, payload);
      await this.reload();
    } catch (error) {
      this.setState(error.message || '切换失败', true);
    }
  },
  async togglePlan(item) {
    const resource = this.resource();
    try {
      const payload = resource.toPayload({ ...item, planEnabled: !isPlanEnabled(item) }, this.config());
      await post(resource.updatePath, payload);
      await this.reload();
    } catch (error) {
      this.setState(error.message || 'Plan 开关切换失败', true);
    }
  },
};

const initialConfig = window.REACT_AGENT_HOST_CONFIG;
if (initialConfig && initialConfig.callerKey) {
  callerKeyInput.value = initialConfig.callerKey;
  routeValuesInput.value = Array.isArray(initialConfig.routeValues)
    ? initialConfig.routeValues.join(',')
    : '';
  controlContextInput.value = initialConfig.controlContext && typeof initialConfig.controlContext === 'object' && !Array.isArray(initialConfig.controlContext)
    ? JSON.stringify(initialConfig.controlContext, null, 2)
    : '';
  llmContextInput.value = initialConfig.llmContext && typeof initialConfig.llmContext === 'object' && !Array.isArray(initialConfig.llmContext)
    ? JSON.stringify(initialConfig.llmContext, null, 2)
    : '';
}

const hasContextConfigValue = () => !!(controlContextInput.value.trim() || llmContextInput.value.trim());

const setMoreCapabilitiesExpanded = (expanded) => {
  moreCapabilitiesPanel.classList.toggle('rp-hidden', !expanded);
  toggleMoreCapabilitiesButton.setAttribute('aria-expanded', expanded ? 'true' : 'false');
  toggleMoreCapabilitiesButton.textContent = expanded ? '收起更多能力 ▴' : '更多能力 ▾';
};

setMoreCapabilitiesExpanded(hasContextConfigValue());

toggleMoreCapabilitiesButton.addEventListener('click', () => {
  setMoreCapabilitiesExpanded(moreCapabilitiesPanel.classList.contains('rp-hidden'));
});

toggleSDKThemeButton.addEventListener('click', () => {
  applySDKTheme(sdkTheme === 'dark' ? 'light' : 'dark');
});

applySDKTheme(sdkTheme);

// ============================================================
// 上下文容量挂件（挂在对话栏右上角）：
// 收起态胶囊 + 悬浮/点击展开的详情卡片（容量进度条 / 分类占比 / 缓存命中率）。
// 数据来自 POST /react/usage/context（会话最近一轮模型调用的聚合），
// SDK 状态变化、悬浮与固定展开时刷新；分类 key 与后端 ContextBreakdown 对齐。
// ============================================================
const ctxCategoryMeta = {
  messages: { label: '消息', color: '#2563eb' },
  mcpTools: { label: 'MCP 工具', color: '#7c3aed' },
  systemTools: { label: '系统工具', color: '#0891b2' },
  skills: { label: '技能', color: '#d97706' },
  systemPrompt: { label: '系统提示词', color: '#059669' },
  others: { label: '其他', color: '#94a3b8' },
};

const ctxEmptyUsage = () => ({
  usedTokens: 0,
  maxTokens: 0,
  cacheHitRate: 0,
  categories: [],
  updatedAt: 0,
  hasData: false,
});

let ctxUsage = ctxEmptyUsage();
let ctxSessionId = null;
let ctxUsageTimer = null;
let disposeContextUsageSubscription = null;

const formatWanTokens = (tokens) => (
  tokens >= 10000 ? `${+(tokens / 10000).toFixed(1)}万` : String(tokens)
);

const renderContextUsageCard = (data = ctxUsage) => {
  if (!$('context-usage-card')) return;
  const used = Math.max(0, data.usedTokens || 0);
  const total = Math.max(1, data.maxTokens || 1);
  const capacityShare = Math.min(100, (used / total) * 100);
  const categories = (data.categories || []).filter((item) => ctxCategoryMeta[item.key]);
  if (!data.hasData || categories.length === 0) {
    $('ctx-summary').textContent = data.hasData
      ? `${formatWanTokens(used)}/${formatWanTokens(total)}（${capacityShare.toFixed(1)}%）`
      : '暂无会话数据';
    $('ctx-bar').innerHTML = '';
    $('ctx-breakdown').innerHTML = '<li class="rp-ctx-row rp-ctx-row-empty">发送新消息后统计上下文构成</li>';
    $('ctx-cache-fill').style.width = data.hasData ? `${(Math.max(0, Math.min(1, data.cacheHitRate || 0)) * 100).toFixed(1)}%` : '0%';
    $('ctx-cache-value').textContent = data.hasData ? `${Math.round(Math.max(0, Math.min(1, data.cacheHitRate || 0)) * 100)}%` : '--';
    if ($('ctx-trigger-bar')) $('ctx-trigger-bar').innerHTML = '';
    if ($('ctx-trigger-text')) $('ctx-trigger-text').textContent = `上下文 ${data.hasData ? capacityShare.toFixed(1) + '%' : '--'}`;
    return;
  }
  const segmentsHtml = categories.map(({ key, tokens }) => {
    const width = Math.min(100, (Math.max(0, tokens || 0) / total) * 100);
    if (width <= 0) return '';
    return `<span class="rp-ctx-seg" style="width:${width.toFixed(3)}%;background:${ctxCategoryMeta[key].color}"></span>`;
  }).join('');
  // 构成占比的分母是各分类合计（usedTokens 在 last/估算两种口径间取大，可能与合计不同）。
  const breakdownTotal = categories.reduce((sum, { tokens }) => sum + Math.max(0, tokens || 0), 0);
  $('ctx-summary').textContent = `${formatWanTokens(used)}/${formatWanTokens(total)}（${capacityShare.toFixed(1)}%）`;
  $('ctx-bar').innerHTML = segmentsHtml;
  $('ctx-breakdown').innerHTML = categories.map(({ key, tokens }) => {
    const safeTokens = Math.max(0, tokens || 0);
    const usedShare = breakdownTotal > 0 ? (safeTokens / breakdownTotal) * 100 : 0;
    return `<li class="rp-ctx-row">
      <span class="rp-ctx-dot" style="background:${ctxCategoryMeta[key].color}"></span>
      <span class="rp-ctx-cat">${escapeHtml(ctxCategoryMeta[key].label)}</span>
      <span class="rp-ctx-nums">${formatWanTokens(safeTokens)}<em>${usedShare.toFixed(1)}%</em></span>
    </li>`;
  }).join('');
  const cacheHit = Math.max(0, Math.min(1, data.cacheHitRate || 0));
  $('ctx-cache-fill').style.width = `${(cacheHit * 100).toFixed(1)}%`;
  $('ctx-cache-value').textContent = `${Math.round(cacheHit * 100)}%`;
  const triggerBar = $('ctx-trigger-bar');
  if (triggerBar) triggerBar.innerHTML = segmentsHtml;
  const triggerText = $('ctx-trigger-text');
  if (triggerText) triggerText.textContent = `上下文 ${capacityShare.toFixed(1)}%`;
};

const refreshContextUsage = async () => {
  if (!ctxSessionId) {
    ctxUsage = ctxEmptyUsage();
    renderContextUsageCard();
    return;
  }
  try {
    const data = await post('/react/usage/context', { sessionId: ctxSessionId });
    const merged = { ...ctxEmptyUsage(), ...data };
    merged.hasData = Boolean((data?.usedTokens || 0) > 0 || (data?.categories || []).length > 0);
    ctxUsage = merged;
    renderContextUsageCard();
  } catch {
    // 拉取失败保留上一次渲染，不打断页面
  }
};

// SDK 状态驱动：sessionId 变化或 run 状态翻转时刷新容量数据。
// reducer.subscribe 不回放当前态，冷启动依赖挂件展开/定时器兜底触发。
const subscribeContextUsage = () => {
  const client = currentHandle?.client;
  if (!client || typeof client.subscribe !== 'function') return;
  let lastStatus = null;
  let lastSessionId = null;
  disposeContextUsageSubscription = client.subscribe((state) => {
    const sessionId = state?.sessionId ?? null;
    if (sessionId !== lastSessionId) {
      lastSessionId = sessionId;
      ctxSessionId = sessionId;
      refreshContextUsage();
      return;
    }
    if (sessionId && state?.status !== lastStatus) {
      refreshContextUsage();
    }
    lastStatus = state?.status ?? null;
  });
};

renderContextUsageCard();

// 交互：悬浮展开交给 CSS :hover（渲染器原生跟随指针，无 mouseleave 依赖，
// 不会出现卡在展开态的问题）；点击固定（再点一次或 Esc 收起）由这里接管。
// 悬浮/固定时拉取最新数据，固定展开期间每 15s 自刷新。
(() => {
  const widget = $('ctx-widget');
  const trigger = $('ctx-trigger');
  if (!widget || !trigger) return;
  const setPinned = (pinned) => {
    widget.classList.toggle('rp-pinned', pinned);
    widget.classList.toggle('rp-open', pinned);
    trigger.setAttribute('aria-expanded', pinned ? 'true' : 'false');
    if (pinned) {
      refreshContextUsage();
      if (!ctxUsageTimer) ctxUsageTimer = setInterval(refreshContextUsage, 15000);
    } else if (ctxUsageTimer) {
      clearInterval(ctxUsageTimer);
      ctxUsageTimer = null;
    }
  };
  widget.addEventListener('mouseenter', () => refreshContextUsage());
  trigger.addEventListener('click', () => {
    setPinned(!widget.classList.contains('rp-pinned'));
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && widget.classList.contains('rp-pinned')) {
      setPinned(false);
    }
  });
})();

const remountAgent = async () => {
  try {
    await mountAgent();
  } catch (error) {
    const message = error?.message || '配置解析失败';
    alert(message);
  }
};

applyButton.addEventListener('click', remountAgent);
copyCallerButton.addEventListener('click', openCopyCallerModal);
deleteCallerButton.addEventListener('click', openDeleteCallerModal);
copyCallerModalClose.addEventListener('click', closeCopyCallerModal);
copyCallerModalCancel.addEventListener('click', closeCopyCallerModal);
copyCallerModalConfirm.addEventListener('click', confirmCopyCaller);
copyCallerModalMask.addEventListener('click', (event) => {
  if (event.target === copyCallerModalMask) closeCopyCallerModal();
});
copyTargetDescriptionInput.addEventListener('keydown', (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') confirmCopyCaller();
});
deleteCallerModalClose.addEventListener('click', closeDeleteCallerModal);
deleteCallerModalCancel.addEventListener('click', closeDeleteCallerModal);
deleteCallerModalConfirm.addEventListener('click', confirmDeleteCaller);
deleteCallerModalMask.addEventListener('click', (event) => {
  if (event.target === deleteCallerModalMask) closeDeleteCallerModal();
});
deleteCallerConfirmInput.addEventListener('keydown', (event) => {
  if (event.key === 'Enter') confirmDeleteCaller();
});
openReplayLink.addEventListener('click', openReplayModal);
replayModalClose.addEventListener('click', closeReplayModal);
replayModalCancel.addEventListener('click', closeReplayModal);
replayModalConfirm.addEventListener('click', confirmReplay);
replayModalMask.addEventListener('click', (event) => {
  if (event.target === replayModalMask) closeReplayModal();
});
replayScopeInput.addEventListener('keydown', (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') {
    event.preventDefault();
    confirmReplay();
  }
});
replayScopeInput.addEventListener('input', () => {
  replayModalState.textContent = '';
});
inputAPIFillButton.addEventListener('click', () => runInputAPIDemo(false));
inputAPISubmitButton.addEventListener('click', () => runInputAPIDemo(true));
inputAPIDemoValue.addEventListener('keydown', (event) => {
  if (event.key !== 'Enter') return;
  event.preventDefault();
  runInputAPIDemo(event.metaKey || event.ctrlKey);
});
[routeValuesInput, callerKeyInput].forEach((input) => {
  input.addEventListener('keydown', (event) => {
    if (event.key === 'Enter') remountAgent();
  });
});

const layoutSplitter = $('layout-splitter');
const playgroundRoot = document.querySelector('.rp-root');
const LEFT_WIDTH_STORAGE_KEY = 'react-playground.left-width';
const LEFT_WIDTH_DEFAULT = 520;
const LEFT_WIDTH_MIN = 320;
const LEFT_WIDTH_KEYBOARD_STEP = 24;
let currentLeftWidth = LEFT_WIDTH_DEFAULT;
let splitterDragging = false;

const leftWidthMax = () => Math.max(LEFT_WIDTH_MIN + 200, Math.floor(window.innerWidth * 0.8));

const applyLeftWidth = (width, { persist = true } = {}) => {
  currentLeftWidth = Math.min(leftWidthMax(), Math.max(LEFT_WIDTH_MIN, Math.round(width)));
  document.documentElement.style.setProperty('--rp-left-width', `${currentLeftWidth}px`);
  layoutSplitter.setAttribute('aria-valuenow', String(currentLeftWidth));
  layoutSplitter.setAttribute('aria-valuemax', String(leftWidthMax()));
  if (persist) {
    try {
      window.localStorage.setItem(LEFT_WIDTH_STORAGE_KEY, String(currentLeftWidth));
    } catch {
      // 隐私模式等场景下写入失败可忽略
    }
  }
};

const restoreLeftWidth = () => {
  let width = LEFT_WIDTH_DEFAULT;
  try {
    const saved = Number(window.localStorage.getItem(LEFT_WIDTH_STORAGE_KEY));
    if (Number.isFinite(saved) && saved >= LEFT_WIDTH_MIN) width = saved;
  } catch {
    // 读取失败时回退默认宽度
  }
  applyLeftWidth(width, { persist: false });
};

layoutSplitter.addEventListener('pointerdown', (event) => {
  if (event.pointerType === 'mouse' && event.button !== 0) return;
  splitterDragging = true;
  layoutSplitter.classList.add('rp-splitter-active');
  playgroundRoot.classList.add('rp-splitting');
  layoutSplitter.setPointerCapture(event.pointerId);
});

layoutSplitter.addEventListener('pointermove', (event) => {
  if (!splitterDragging) return;
  applyLeftWidth(event.clientX);
});

const stopSplitterDrag = (event) => {
  if (!splitterDragging) return;
  splitterDragging = false;
  layoutSplitter.classList.remove('rp-splitter-active');
  playgroundRoot.classList.remove('rp-splitting');
  if (layoutSplitter.hasPointerCapture(event.pointerId)) {
    layoutSplitter.releasePointerCapture(event.pointerId);
  }
};

layoutSplitter.addEventListener('pointerup', stopSplitterDrag);
layoutSplitter.addEventListener('pointercancel', stopSplitterDrag);

layoutSplitter.addEventListener('dblclick', () => applyLeftWidth(LEFT_WIDTH_DEFAULT));

layoutSplitter.addEventListener('keydown', (event) => {
  const step = event.shiftKey ? LEFT_WIDTH_KEYBOARD_STEP * 4 : LEFT_WIDTH_KEYBOARD_STEP;
  if (event.key === 'ArrowLeft') {
    event.preventDefault();
    applyLeftWidth(currentLeftWidth - step);
  } else if (event.key === 'ArrowRight') {
    event.preventDefault();
    applyLeftWidth(currentLeftWidth + step);
  } else if (event.key === 'Home') {
    event.preventDefault();
    applyLeftWidth(LEFT_WIDTH_MIN);
  } else if (event.key === 'End') {
    event.preventDefault();
    applyLeftWidth(leftWidthMax());
  }
});

window.addEventListener('resize', () => {
  if (!splitterDragging) applyLeftWidth(currentLeftWidth, { persist: false });
});

restoreLeftWidth();

management.init();
remountAgent();
