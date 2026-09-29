/**
 * SettingsDrawer - 模型配置面板
 *
 * 四个 Tab：
 * - 厂商与密钥：按厂商(modelKey)分组管理 API Key / 自定义接入面，支持连通性检测与新增模型
 * - 模型与参数：上下文容量 / 最大输出 / 能力开关（思考、函数调用、视觉）
 * - 上下文与压缩：压缩触发/目标水位等运行时策略（DB 覆盖 yaml，写后立即生效）
 * - 高级：参数 schema 参考（声明式 min/max/说明）
 *
 * 数据全部来自 AgentClient 的模型配置封装（/model/*、/setting/context/*、/react/config/schema）。
 */
import { createEffect, createMemo, createSignal, For, Show } from "solid-js";
import IconMdiClose from "~icons/mdi/close";
import type { AgentClient } from "../../runtime/agent-client";
import type { ConfigParamSchema, ConfigSchemaResp, ConnectionItem, ConnectionModelItem, ConnectionProtocol, UserModelItem, VendorModelInfo, VendorModelsResp } from "../../protocol/types";

export interface SettingsDrawerProps {
  /** 是否展开 */
  open: boolean;
  onClose: () => void;
  /** AgentClient 实例（模型配置端点封装） */
  client: AgentClient;
  /** 模型配置发生变化（新增/编辑/删除），宿主据此刷新模型选择列表 */
  onModelsChanged?: () => void;
}

type SettingsTab = "providers" | "models" | "context" | "advanced";

const TABS: Array<{ key: SettingsTab; label: string }> = [
  { key: "providers", label: "连接与密钥" },
  { key: "models", label: "模型与参数" },
  { key: "context", label: "上下文与压缩" },
  { key: "advanced", label: "高级" },
];

const DEFAULT_BIZ_SCENES = ["通用"];
const REASONING_BUDGET_MAX = 131072;

interface Feedback {
  kind: "ok" | "error" | "info";
  text: string;
}

export function SettingsDrawer(props: SettingsDrawerProps) {
  const [tab, setTab] = createSignal<SettingsTab>("providers");
  const [userModels, setUserModels] = createSignal<UserModelItem[]>([]);
  const [connections, setConnections] = createSignal<ConnectionItem[]>([]);
  const [vendors, setVendors] = createSignal<VendorModelInfo[]>([]);
  const [vendorEnum, setVendorEnum] = createSignal<string[]>([]);
  const [schema, setSchema] = createSignal<ConfigParamSchema[]>([]);
  const [feedback, setFeedback] = createSignal<Feedback>();

  let feedbackTimer: ReturnType<typeof setTimeout> | undefined;

  const showFeedback = (kind: Feedback["kind"], text: string) => {
    if (feedbackTimer) clearTimeout(feedbackTimer);
    setFeedback({ kind, text });
    feedbackTimer = setTimeout(() => setFeedback(undefined), kind === "error" ? 6000 : 3000);
  };

  const schemaParam = (key: string): ConfigParamSchema | undefined =>
    schema().find((item) => item.key === key);

  const refreshAll = async () => {
    try {
      const [models, connList, vendorResp, schemaResp] = await Promise.all([
        props.client.listUserModels(),
        props.client.listConnections().catch(() => [] as ConnectionItem[]),
        props.client.listVendorModels().catch(() => ({ models: [] }) as VendorModelsResp),
        props.client.getConfigSchema().catch(() => ({ params: [] }) as ConfigSchemaResp),
      ]);
      setUserModels(models ?? []);
      setConnections(connList ?? []);
      setVendors(vendorResp.models ?? []);
      setVendorEnum(vendorResp.vendors ?? []);
      setSchema(schemaResp.params ?? []);
      props.onModelsChanged?.();
    } catch (error) {
      showFeedback("error", `加载模型配置失败: ${error instanceof Error ? error.message : String(error)}`);
    }
  };

  createEffect(() => {
    if (props.open && userModels().length === 0 && vendors().length === 0 && connections().length === 0) {
      void refreshAll();
    }
  });

  return (
    <Show when={props.open}>
      <div class="agent-ui-settings-overlay" onClick={() => props.onClose()}>
        <div class="agent-ui-settings-drawer" onClick={(event) => event.stopPropagation()}>
          <div class="agent-ui-settings-header">
            <span class="agent-ui-settings-title">模型配置</span>
            <button
              type="button"
              class="agent-ui-settings-close"
              title="关闭"
              aria-label="关闭模型配置"
              onClick={() => props.onClose()}
            >
              <IconMdiClose width="16" height="16" />
            </button>
          </div>

          <div class="agent-ui-settings-tabs" role="tablist">
            <For each={TABS}>
              {(item) => (
                <button
                  type="button"
                  role="tab"
                  aria-selected={tab() === item.key}
                  classList={{ "agent-ui-settings-tab-active": tab() === item.key }}
                  onClick={() => setTab(item.key)}
                >
                  {item.label}
                </button>
              )}
            </For>
          </div>

          <Show when={feedback()}>
            {(fb) => (
              <div class={`agent-ui-settings-feedback agent-ui-settings-feedback-${fb().kind}`}>{fb().text}</div>
            )}
          </Show>

          <div class="agent-ui-settings-body">
            <Show when={tab() === "providers"}>
              <ConnectionsSection
                client={props.client}
                connections={connections()}
                userModels={userModels()}
                vendors={vendors()}
                vendorEnum={vendorEnum()}
                onFeedback={showFeedback}
                onChanged={refreshAll}
                schemaParam={schemaParam}
              />
            </Show>
            <Show when={tab() === "models"}>
              <ModelParamsTab
                client={props.client}
                userModels={userModels()}
                onFeedback={showFeedback}
                onChanged={refreshAll}
                schemaParam={schemaParam}
              />
            </Show>
            <Show when={tab() === "context"}>
              <ContextTab client={props.client} schemaParam={schemaParam} onFeedback={showFeedback} />
            </Show>
            <Show when={tab() === "advanced"}>
              <AdvancedTab schema={schema()} />
            </Show>
          </div>
        </div>
      </div>
    </Show>
  );
}

// ─── Tab 1：连接与密钥 ─────────────────────────────────────────────
// 连接（协议 + 接入地址 + Key）是模型配置的主体形态：
//   添加连接（选预设/自定义）→ 测试连通 → 拉取模型列表 → 勾选启用；
// 自包含模型（自带 key/接入地址的旧形态）保留在下方兜底。

/** 连接预设：预填协议与接入地址，降低「不知道贴哪个 URL」的成本 */
const CONNECTION_PRESETS: Array<{ key: string; label: string; protocol: ConnectionProtocol; baseUrl: string }> = [
  { key: "custom", label: "自定义", protocol: "openai", baseUrl: "" },
  { key: "zhipu-anthropic", label: "智谱 GLM（Anthropic 协议）", protocol: "anthropic", baseUrl: "https://open.bigmodel.cn/api/anthropic" },
  { key: "zhipu-openai", label: "智谱 GLM（OpenAI 兼容）", protocol: "openai", baseUrl: "https://open.bigmodel.cn/api/coding/paas/v4" },
  { key: "deepseek-anthropic", label: "DeepSeek（Anthropic 协议）", protocol: "anthropic", baseUrl: "https://api.deepseek.com/anthropic" },
  { key: "openrouter", label: "OpenRouter", protocol: "openai", baseUrl: "https://openrouter.ai/api/v1" },
  { key: "ollama", label: "Ollama 本地", protocol: "openai", baseUrl: "http://127.0.0.1:11434/v1" },
];

interface ConnectionsSectionProps {
  client: AgentClient;
  connections: ConnectionItem[];
  userModels: UserModelItem[];
  vendors: VendorModelInfo[];
  vendorEnum: string[];
  schemaParam: (key: string) => ConfigParamSchema | undefined;
  onFeedback: (kind: Feedback["kind"], text: string) => void;
  onChanged: () => Promise<void> | void;
}

function ConnectionsSection(props: ConnectionsSectionProps) {
  const legacyModels = createMemo(() => props.userModels.filter((item) => !item.connectionId));

  const groupedLegacy = createMemo(() => {
    const byKey = new Map<string, UserModelItem[]>();
    for (const item of legacyModels()) {
      const list = byKey.get(item.modelKey) ?? [];
      list.push(item);
      byKey.set(item.modelKey, list);
    }
    return [...byKey.entries()].map(([modelKey, items]) => ({ modelKey, items }));
  });

  const catalogVersions = (modelKey: string): string[] => {
    const vendor = props.vendors.find((item) => item.key === modelKey);
    return vendor?.versions?.length ? vendor.versions : [];
  };

  return (
    <div class="agent-ui-settings-stack">
      <div class="agent-ui-settings-hint">
        连接 = 协议 + 接入地址 + API Key。测试连通后可一键拉取可用模型并勾选启用；
        无 ModelHash 的 run 也会按 caller+路由解析到连接（优先于旧版 API Key）。
      </div>
      <For each={props.connections}>
        {(item) => (
          <ConnectionCard
            client={props.client}
            item={item}
            boundModels={props.userModels.filter((m) => m.connectionId === item.id)}
            onFeedback={props.onFeedback}
            onChanged={props.onChanged}
          />
        )}
      </For>
      <Show when={props.connections.length === 0}>
        <div class="agent-ui-settings-empty">
          暂无连接。通过下方「添加连接」选择预设（或自定义）并填写 API Key 创建第一条连接。
        </div>
      </Show>
      <AddConnectionRow client={props.client} onFeedback={props.onFeedback} onChanged={props.onChanged} />

      <Show when={groupedLegacy().length > 0}>
        <div class="agent-ui-settings-card">
          <div class="agent-ui-settings-card-head">
            <span class="agent-ui-settings-card-title">自包含模型（未挂连接，自带 Key）</span>
            <span class="agent-ui-settings-card-sub">{legacyModels().length} 个模型</span>
          </div>
          <For each={groupedLegacy()}>
            {(group) => (
              <For each={group.items}>
                {(item) => (
                  <ProviderModelRow
                    client={props.client}
                    item={item}
                    schemaParam={props.schemaParam}
                    onFeedback={props.onFeedback}
                    onChanged={props.onChanged}
                  />
                )}
              </For>
            )}
          </For>
          <AddModelRow
            client={props.client}
            modelKey={groupedLegacy()[0]?.modelKey}
            versions={catalogVersions(groupedLegacy()[0]?.modelKey ?? "")}
            vendorOptions={props.vendorEnum}
            onFeedback={props.onFeedback}
            onChanged={props.onChanged}
          />
        </div>
      </Show>
    </div>
  );
}

// ─── 连接卡片 ──────────────────────────────────────────────────────

interface ConnectionCardProps {
  client: AgentClient;
  item: ConnectionItem;
  boundModels: UserModelItem[];
  onFeedback: (kind: Feedback["kind"], text: string) => void;
  onChanged: () => Promise<void> | void;
}

function ConnectionCard(props: ConnectionCardProps) {
  const [expanded, setExpanded] = createSignal(false);
  const [name, setName] = createSignal("");
  const [baseUrl, setBaseUrl] = createSignal("");
  const [apiKey, setApiKey] = createSignal("");
  const [testing, setTesting] = createSignal(false);
  const [saving, setSaving] = createSignal(false);
  const [testResult, setTestResult] = createSignal<Feedback>();
  const [fetching, setFetching] = createSignal(false);
  const [discovered, setDiscovered] = createSignal<ConnectionModelItem[]>([]);
  const [selected, setSelected] = createSignal<Set<string>>(new Set());
  const [adding, setAdding] = createSignal(false);

  const startEdit = async () => {
    setExpanded((value) => !value);
  };

  const runTest = async () => {
    setTesting(true);
    setTestResult(undefined);
    try {
      await props.client.checkConnection(props.item.id);
      setTestResult({ kind: "ok", text: "连通成功" });
    } catch (error) {
      setTestResult({ kind: "error", text: `连通失败: ${error instanceof Error ? error.message : String(error)}` });
    } finally {
      setTesting(false);
    }
  };

  const save = async () => {
    setSaving(true);
    try {
      await props.client.updateConnection({
        id: props.item.id,
        name: name() || undefined,
        baseUrl: baseUrl() || undefined,
        apiKey: apiKey() || undefined,
      });
      props.onFeedback("ok", "已保存");
      setApiKey("");
      await props.onChanged();
    } catch (error) {
      props.onFeedback("error", `保存失败: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!confirm(`删除连接「${props.item.name}」后不可恢复，确认继续吗？`)) return;
    try {
      await props.client.deleteConnection(props.item.id);
      props.onFeedback("ok", "已删除");
      await props.onChanged();
    } catch (error) {
      props.onFeedback("error", `删除失败: ${error instanceof Error ? error.message : String(error)}`);
    }
  };

  const fetchModels = async () => {
    setFetching(true);
    setDiscovered([]);
    try {
      const resp = await props.client.fetchConnectionModels(props.item.id);
      setDiscovered(resp.models ?? []);
      const existing = new Set(props.boundModels.map((m) => m.modelVersion));
      setSelected(new Set((resp.models ?? []).filter((item) => !existing.has(item.id)).slice(0, 1).map((item) => item.id)));
    } catch (error) {
      props.onFeedback("error", `拉取模型列表失败: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setFetching(false);
    }
  };

  const toggleModel = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const addSelected = async () => {
    const ids = [...selected()];
    if (ids.length === 0) {
      props.onFeedback("error", "请先勾选要启用的模型");
      return;
    }
    setAdding(true);
    let created = 0;
    try {
      for (const id of ids) {
        // 能力位来自 fetch_models 的目录自动填充；未命中目录时为 0（保守），可在「模型与参数」修正。
        const caps = discovered().find((item) => item.id === id);
        try {
          await props.client.createUserModel({
            modelName: id,
            modelKey: props.item.protocol === "anthropic" ? "Anthropic" : "OpenAI",
            modelVersion: id,
            apiKey: "",
            bizScenes: DEFAULT_BIZ_SCENES,
            connectionId: props.item.id,
            supportTools: caps?.supportTools ?? 0,
            supportThinking: caps?.supportThinking ?? 0,
            supportVision: caps?.supportVision ?? 0,
            contextTokens: caps?.contextTokens ?? 0,
            maxOutputTokens: caps?.maxOutputTokens ?? 0,
          });
          created++;
        } catch (error) {
          // 单个失败（如重名）不阻断批量，汇总提示。
          props.onFeedback("error", `模型 ${id} 创建失败: ${error instanceof Error ? error.message : String(error)}`);
        }
      }
      if (created > 0) props.onFeedback("ok", `已启用 ${created} 个模型，可在模型选择器使用`);
      setDiscovered([]);
      await props.onChanged();
    } finally {
      setAdding(false);
    }
  };

  return (
    <div class="agent-ui-settings-card">
      <div class="agent-ui-settings-card-head">
        <span class="agent-ui-settings-card-title">
          {props.item.name}
          <span class="agent-ui-settings-card-sub">
            {props.item.protocol === "anthropic" ? "Anthropic 协议" : "OpenAI 兼容"} · {props.boundModels.length} 个模型
          </span>
        </span>
        <div class="agent-ui-settings-row-actions">
          <button type="button" class="agent-ui-settings-btn" disabled={testing()} onClick={() => void runTest()}>
            {testing() ? "检测中…" : "测试连通"}
          </button>
          <button type="button" class="agent-ui-settings-btn" disabled={fetching()} onClick={() => void fetchModels()}>
            {fetching() ? "拉取中…" : "拉取模型"}
          </button>
          <button type="button" class="agent-ui-settings-btn" onClick={() => void startEdit()}>
            {expanded() ? "收起" : "编辑"}
          </button>
          <button type="button" class="agent-ui-settings-btn agent-ui-settings-btn-danger" onClick={() => void remove()}>
            删除
          </button>
        </div>
      </div>
      <div class="agent-ui-settings-card-sub" title={props.item.baseUrl}>
        {props.item.baseUrl} · {props.item.apiKey} · caller: {props.item.callerKey || "（空）"}
      </div>
      <Show when={testResult()}>
        {(result) => (
          <div class={`agent-ui-settings-feedback agent-ui-settings-feedback-${result().kind}`}>{result().text}</div>
        )}
      </Show>
      <Show when={expanded()}>
        <div class="agent-ui-settings-form">
          <label class="agent-ui-settings-field">
            <span>名称</span>
            <input
              type="text"
              placeholder={props.item.name}
              value={name()}
              onInput={(event) => setName(event.currentTarget.value)}
            />
          </label>
          <label class="agent-ui-settings-field">
            <span>接入地址</span>
            <input
              type="text"
              placeholder={props.item.baseUrl}
              value={baseUrl()}
              onInput={(event) => setBaseUrl(event.currentTarget.value)}
            />
          </label>
          <label class="agent-ui-settings-field">
            <span>新 API Key（留空 = 保持原值）</span>
            <input
              type="password"
              autocomplete="off"
              placeholder="不修改则留空"
              value={apiKey()}
              onInput={(event) => setApiKey(event.currentTarget.value)}
            />
          </label>
          <div class="agent-ui-settings-form-actions">
            <button type="button" class="agent-ui-settings-btn agent-ui-settings-btn-primary" disabled={saving()} onClick={() => void save()}>
              {saving() ? "保存中…" : "保存"}
            </button>
          </div>
        </div>
      </Show>
      <Show when={discovered().length > 0}>
        <div class="agent-ui-settings-form">
          <div class="agent-ui-settings-field">
            <span>可用模型（勾选后启用；标签为能力目录自动识别：工具/思考/视觉）</span>
            <div class="agent-ui-settings-model-list">
              <For each={discovered()}>
                {(item) => {
                  const bound = () => props.boundModels.some((m) => m.modelVersion === item.id);
                  return (
                    <label class="agent-ui-settings-model-option">
                      <input
                        type="checkbox"
                        checked={selected().has(item.id)}
                        disabled={bound()}
                        onChange={() => toggleModel(item.id)}
                      />
                      <span class="agent-ui-settings-model-id">{item.id}</span>
                      <span class="agent-ui-settings-model-caps">
                        <Show when={item.supportTools === 1}>
                          <em class="agent-ui-settings-cap">工具</em>
                        </Show>
                        <Show when={item.supportThinking === 1}>
                          <em class="agent-ui-settings-cap">思考</em>
                        </Show>
                        <Show when={item.supportVision === 1}>
                          <em class="agent-ui-settings-cap agent-ui-settings-cap-vision">视觉</em>
                        </Show>
                      </span>
                      <Show when={bound()}>
                        <em>（已在用）</em>
                      </Show>
                    </label>
                  );
                }}
              </For>
            </div>
          </div>
          <div class="agent-ui-settings-form-actions">
            <button type="button" class="agent-ui-settings-btn agent-ui-settings-btn-primary" disabled={adding()} onClick={() => void addSelected()}>
              {adding() ? "添加中…" : `启用所选（${selected().size}）`}
            </button>
            <button type="button" class="agent-ui-settings-btn" onClick={() => setDiscovered([])}>
              收起
            </button>
          </div>
        </div>
      </Show>
    </div>
  );
}

// ─── 添加连接 ──────────────────────────────────────────────────────

function AddConnectionRow(props: {
  client: AgentClient;
  onFeedback: (kind: Feedback["kind"], text: string) => void;
  onChanged: () => Promise<void> | void;
}) {
  const [expanded, setExpanded] = createSignal(false);
  const [preset, setPreset] = createSignal("zhipu-anthropic");
  const [name, setName] = createSignal("");
  const [baseUrl, setBaseUrl] = createSignal("");
  const [apiKey, setApiKey] = createSignal("");
  const [callerKey, setCallerKey] = createSignal("");
  const [creating, setCreating] = createSignal(false);

  const activePreset = () => CONNECTION_PRESETS.find((item) => item.key === preset()) ?? CONNECTION_PRESETS[0];
  const effectiveProtocol = () => activePreset().protocol;
  const effectiveBaseUrl = () => baseUrl().trim() || activePreset().baseUrl;

  const applyPreset = (key: string) => {
    setPreset(key);
    const found = CONNECTION_PRESETS.find((item) => item.key === key);
    if (found?.baseUrl) setBaseUrl(found.baseUrl);
    if (!name().trim() && found && found.key !== "custom") setName(found.label.replace(/（.*）/, ""));
  };

  const canSubmit = () => name().trim() && effectiveBaseUrl() && apiKey().trim();

  const create = async () => {
    if (!canSubmit()) {
      props.onFeedback("error", "名称、接入地址与 API Key 均必填");
      return;
    }
    setCreating(true);
    try {
      await props.client.createConnection({
        name: name().trim(),
        protocol: effectiveProtocol(),
        baseUrl: effectiveBaseUrl().trim(),
        apiKey: apiKey().trim(),
        callerKey: callerKey().trim(),
        routeValues: [],
      });
      props.onFeedback("ok", "连接已创建，可点「测试连通」验证后拉取模型");
      setName("");
      setApiKey("");
      setExpanded(false);
      await props.onChanged();
    } catch (error) {
      props.onFeedback("error", `创建失败: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setCreating(false);
    }
  };

  return (
    <div class="agent-ui-settings-add-row">
      <Show
        when={expanded()}
        fallback={
          <button type="button" class="agent-ui-settings-btn" onClick={() => { setExpanded(true); applyPreset(preset()); }}>
            + 添加连接
          </button>
        }
      >
        <div class="agent-ui-settings-form">
          <label class="agent-ui-settings-field">
            <span>预设</span>
            <select value={preset()} onChange={(event) => applyPreset(event.currentTarget.value)}>
              <For each={CONNECTION_PRESETS}>{(item) => <option value={item.key}>{item.label}</option>}</For>
            </select>
          </label>
          <label class="agent-ui-settings-field">
            <span>名称</span>
            <input type="text" placeholder="如 智谱 GLM" value={name()} onInput={(event) => setName(event.currentTarget.value)} />
          </label>
          <label class="agent-ui-settings-field">
            <span>协议</span>
            <input type="text" readonly value={effectiveProtocol() === "anthropic" ? "Anthropic 兼容" : "OpenAI 兼容"} />
          </label>
          <label class="agent-ui-settings-field">
            <span>接入地址</span>
            <input
              type="text"
              placeholder={activePreset().baseUrl || "https://example.com/v1"}
              value={baseUrl()}
              onInput={(event) => setBaseUrl(event.currentTarget.value)}
            />
          </label>
          <label class="agent-ui-settings-field">
            <span>API Key</span>
            <input
              type="password"
              autocomplete="off"
              placeholder="sk-..."
              value={apiKey()}
              onInput={(event) => setApiKey(event.currentTarget.value)}
            />
          </label>
          <label class="agent-ui-settings-field">
            <span>作用 caller（可选；留空 = 全局通用，无 ModelHash 的 run 按 caller+路由解析）</span>
            <input type="text" placeholder="如 demo-app" value={callerKey()} onInput={(event) => setCallerKey(event.currentTarget.value)} />
          </label>
          <div class="agent-ui-settings-form-actions">
            <button type="button" class="agent-ui-settings-btn agent-ui-settings-btn-primary" disabled={creating()} onClick={() => void create()}>
              {creating() ? "创建中…" : "创建"}
            </button>
            <button type="button" class="agent-ui-settings-btn" onClick={() => setExpanded(false)}>
              取消
            </button>
          </div>
        </div>
      </Show>
    </div>
  );
}

// ─── 自包含模型行（旧形态：自带 Key/接入地址，P0-3 修复后保留兜底） ──

interface ProviderModelRowProps {
  client: AgentClient;
  item: UserModelItem;
  schemaParam: (key: string) => ConfigParamSchema | undefined;
  onFeedback: (kind: Feedback["kind"], text: string) => void;
  onChanged: () => Promise<void> | void;
}

function ProviderModelRow(props: ProviderModelRowProps) {
  const [expanded, setExpanded] = createSignal(false);
  const [apiKey, setApiKey] = createSignal("");
  const [apiUrl, setApiUrl] = createSignal(props.item.apiUrl ?? "");
  const [testing, setTesting] = createSignal(false);
  const [saving, setSaving] = createSignal(false);
  const [testResult, setTestResult] = createSignal<Feedback>();

  const startEdit = async () => {
    setExpanded((value) => !value);
    if (!expanded() && props.item.isPlatformDefault === 0) {
      try {
        const detail = await props.client.getUserModelDetail(props.item.id);
        setApiKey(detail.apiKey ?? "");
        setApiUrl(detail.apiUrl ?? "");
      } catch (error) {
        props.onFeedback("error", `加载密钥失败: ${error instanceof Error ? error.message : String(error)}`);
      }
    }
  };

  // P0 修复：输入为空时取真实 key（detail 回填或现拉），绝不把列表的脱敏占位值发给后端。
  const resolveRealApiKey = async (): Promise<string | null> => {
    const typed = apiKey().trim();
    if (typed) return typed;
    if (props.item.apiKey && !props.item.apiKey.includes("*")) return props.item.apiKey;
    try {
      const detail = await props.client.getUserModelDetail(props.item.id);
      if (detail.apiKey) {
        setApiKey(detail.apiKey);
        return detail.apiKey;
      }
    } catch {
      // 拉取失败走下方提示
    }
    return null;
  };

  const runTest = async () => {
    const realKey = await resolveRealApiKey();
    if (!realKey) {
      setTestResult({ kind: "error", text: "请填写 API Key（留空无法检测）" });
      return;
    }
    setTesting(true);
    setTestResult(undefined);
    try {
      await props.client.checkModelConnectivity({
        modelKey: props.item.modelKey,
        modelVersion: props.item.modelVersion,
        apiKey: realKey,
        apiUrl: apiUrl() || undefined,
      });
      setTestResult({ kind: "ok", text: "连通成功" });
    } catch (error) {
      setTestResult({ kind: "error", text: `连通失败: ${error instanceof Error ? error.message : String(error)}` });
    } finally {
      setTesting(false);
    }
  };

  const save = async () => {
    const realKey = await resolveRealApiKey();
    if (!realKey) {
      props.onFeedback("error", "请填写 API Key（留空无法保存）");
      return;
    }
    setSaving(true);
    try {
      await props.client.updateUserModel({
        id: props.item.id,
        modelName: props.item.modelName,
        modelKey: props.item.modelKey,
        modelVersion: props.item.modelVersion,
        apiKey: realKey,
        bizScenes: props.item.bizScenes?.length ? props.item.bizScenes : DEFAULT_BIZ_SCENES,
        apiUrl: apiUrl(),
        connectionId: props.item.connectionId ?? 0,
        contextTokens: props.item.contextTokens ?? 0,
        maxOutputTokens: props.item.maxOutputTokens ?? 0,
        supportThinking: props.item.supportThinking ?? 0,
        supportTools: props.item.supportTools ?? 0,
        supportVision: props.item.supportVision ?? 0,
        isPlatformDefault: props.item.isPlatformDefault,
      });
      props.onFeedback("ok", "已保存");
      await props.onChanged();
    } catch (error) {
      props.onFeedback("error", `保存失败: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!confirm(`删除模型「${props.item.modelName}」后不可恢复，确认继续吗？`)) return;
    try {
      await props.client.deleteUserModel(props.item.id);
      props.onFeedback("ok", "已删除");
      await props.onChanged();
    } catch (error) {
      props.onFeedback("error", `删除失败: ${error instanceof Error ? error.message : String(error)}`);
    }
  };

  return (
    <div class="agent-ui-settings-row">
      <div class="agent-ui-settings-row-head">
        <span class="agent-ui-settings-row-title" title={props.item.modelHash}>
          {props.item.modelName}
          <span class="agent-ui-settings-row-sub">{props.item.modelVersion}</span>
        </span>
        <div class="agent-ui-settings-row-actions">
          <button type="button" class="agent-ui-settings-btn" onClick={() => void startEdit()}>
            {expanded() ? "收起" : "编辑"}
          </button>
          <Show when={props.item.isPlatformDefault === 0}>
            <button type="button" class="agent-ui-settings-btn agent-ui-settings-btn-danger" onClick={() => void remove()}>
              删除
            </button>
          </Show>
        </div>
      </div>
      <Show when={expanded()}>
        <div class="agent-ui-settings-form">
          <label class="agent-ui-settings-field">
            <span>API Key</span>
            <input
              type="password"
              autocomplete="off"
              placeholder={props.item.apiKey || "sk-..."}
              value={apiKey()}
              onInput={(event) => setApiKey(event.currentTarget.value)}
            />
          </label>
          <label class="agent-ui-settings-field">
            <span>接入地址（可选，覆盖全局端点）</span>
            <input
              type="text"
              placeholder="https://open.bigmodel.cn/api/anthropic"
              value={apiUrl()}
              onInput={(event) => setApiUrl(event.currentTarget.value)}
            />
          </label>
          <div class="agent-ui-settings-form-actions">
            <button type="button" class="agent-ui-settings-btn" disabled={testing()} onClick={() => void runTest()}>
              {testing() ? "检测中…" : "测试连通性"}
            </button>
            <button type="button" class="agent-ui-settings-btn agent-ui-settings-btn-primary" disabled={saving()} onClick={() => void save()}>
              {saving() ? "保存中…" : "保存"}
            </button>
            <Show when={testResult()}>
              {(result) => (
                <span class={`agent-ui-settings-inline-${result().kind}`}>{result().text}</span>
              )}
            </Show>
          </div>
        </div>
      </Show>
    </div>
  );
}

interface AddModelRowProps {
  client: AgentClient;
  /** 固定厂商（分组卡片内使用）；不传时渲染厂商选择（vendorOptions）或自由填写 */
  modelKey?: string;
  versions: string[];
  /** 厂商枚举候选；仅未固定 modelKey 时使用 */
  vendorOptions?: string[];
  onFeedback: (kind: Feedback["kind"], text: string) => void;
  onChanged: () => Promise<void> | void;
}

function AddModelRow(props: AddModelRowProps) {
  const [expanded, setExpanded] = createSignal(false);
  const [vendor, setVendor] = createSignal("");
  const [modelName, setModelName] = createSignal("");
  const [version, setVersion] = createSignal("");
  const [apiKey, setApiKey] = createSignal("");
  const [apiUrl, setApiUrl] = createSignal("");
  const [creating, setCreating] = createSignal(false);

  const vendorLocked = () => !!props.modelKey;
  const effectiveVendor = () => (vendorLocked() ? props.modelKey! : vendor());
  const versionOptions = () => props.versions;
  const canSubmit = () => effectiveVendor() && modelName().trim() && version().trim() && apiKey().trim();

  const create = async () => {
    if (!effectiveVendor()) {
      props.onFeedback("error", "请选择厂商");
      return;
    }
    if (!canSubmit()) {
      props.onFeedback("error", "模型名称、版本与 API Key 均必填");
      return;
    }
    setCreating(true);
    try {
      await props.client.createUserModel({
        modelName: modelName().trim(),
        modelKey: effectiveVendor(),
        modelVersion: version().trim(),
        apiKey: apiKey().trim(),
        bizScenes: DEFAULT_BIZ_SCENES,
        apiUrl: apiUrl().trim() || undefined,
      });
      props.onFeedback("ok", "模型已创建，已在模型选择器可用");
      setVendor("");
      setModelName("");
      setVersion("");
      setApiKey("");
      setApiUrl("");
      setExpanded(false);
      await props.onChanged();
    } catch (error) {
      props.onFeedback("error", `创建失败: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setCreating(false);
    }
  };

  return (
    <div class="agent-ui-settings-add-row">
      <Show
        when={expanded()}
        fallback={
          <button type="button" class="agent-ui-settings-btn" onClick={() => setExpanded(true)}>
            + 添加模型{vendorLocked() ? `（${props.modelKey}）` : ""}
          </button>
        }
      >
        <div class="agent-ui-settings-form">
          <Show when={!vendorLocked()}>
            <label class="agent-ui-settings-field">
              <span>厂商</span>
              <Show
                when={(props.vendorOptions?.length ?? 0) > 0}
                fallback={
                  <input
                    type="text"
                    placeholder="如 智谱 / DeepSeek Anthropic / OpenAI"
                    value={vendor()}
                    onInput={(event) => setVendor(event.currentTarget.value)}
                  />
                }
              >
                <select value={vendor()} onChange={(event) => setVendor(event.currentTarget.value)}>
                  <option value="">选择厂商</option>
                  <For each={props.vendorOptions}>{(item) => <option value={item}>{item}</option>}</For>
                </select>
              </Show>
            </label>
          </Show>
          <label class="agent-ui-settings-field">
            <span>展示名称</span>
            <input type="text" value={modelName()} onInput={(event) => setModelName(event.currentTarget.value)} />
          </label>
          <label class="agent-ui-settings-field">
            <span>版本</span>
            <Show
              when={versionOptions().length > 0}
              fallback={
                <input
                  type="text"
                  placeholder="如 glm-4.6"
                  value={version()}
                  onInput={(event) => setVersion(event.currentTarget.value)}
                />
              }
            >
              <select value={version()} onChange={(event) => setVersion(event.currentTarget.value)}>
                <option value="">选择版本</option>
                <For each={versionOptions()}>{(item) => <option value={item}>{item}</option>}</For>
              </select>
            </Show>
          </label>
          <label class="agent-ui-settings-field">
            <span>API Key</span>
            <input
              type="password"
              autocomplete="off"
              value={apiKey()}
              onInput={(event) => setApiKey(event.currentTarget.value)}
            />
          </label>
          <label class="agent-ui-settings-field">
            <span>接入地址（可选，覆盖全局端点）</span>
            <input type="text" value={apiUrl()} onInput={(event) => setApiUrl(event.currentTarget.value)} />
          </label>
          <div class="agent-ui-settings-form-actions">
            <button type="button" class="agent-ui-settings-btn agent-ui-settings-btn-primary" disabled={creating()} onClick={() => void create()}>
              {creating() ? "创建中…" : "创建"}
            </button>
            <button type="button" class="agent-ui-settings-btn" onClick={() => setExpanded(false)}>
              取消
            </button>
          </div>
        </div>
      </Show>
    </div>
  );
}

// ─── Tab 2：模型与参数 ─────────────────────────────────────────────

interface ModelParamsTabProps {
  client: AgentClient;
  userModels: UserModelItem[];
  schemaParam: (key: string) => ConfigParamSchema | undefined;
  onFeedback: (kind: Feedback["kind"], text: string) => void;
  onChanged: () => Promise<void> | void;
}

function ModelParamsTab(props: ModelParamsTabProps) {
  return (
    <div class="agent-ui-settings-stack">
      <div class="agent-ui-settings-hint">
        上下文容量是压缩触发阈值的推导基准；最大输出是单次模型调用的输出上限。0 = 回退平台目录配置。
      </div>
      <For each={props.userModels}>
        {(item) => (
          <ModelParamsRow
            client={props.client}
            item={item}
            schemaParam={props.schemaParam}
            onFeedback={props.onFeedback}
            onChanged={props.onChanged}
          />
        )}
      </For>
      <Show when={props.userModels.length === 0}>
        <div class="agent-ui-settings-empty">暂无模型配置，请先在「厂商与密钥」中添加。</div>
      </Show>
    </div>
  );
}

interface ModelParamsRowProps {
  client: AgentClient;
  item: UserModelItem;
  schemaParam: (key: string) => ConfigParamSchema | undefined;
  onFeedback: (kind: Feedback["kind"], text: string) => void;
  onChanged: () => Promise<void> | void;
}

function ModelParamsRow(props: ModelParamsRowProps) {
  const [contextTokens, setContextTokens] = createSignal(props.item.contextTokens ?? 0);
  const [maxOutputTokens, setMaxOutputTokens] = createSignal(props.item.maxOutputTokens ?? 0);
  const [supportThinking, setSupportThinking] = createSignal((props.item.supportThinking ?? 0) === 1);
  const [supportTools, setSupportTools] = createSignal((props.item.supportTools ?? 0) === 1);
  const [supportVision, setSupportVision] = createSignal((props.item.supportVision ?? 0) === 1);
  const [saving, setSaving] = createSignal(false);

  const contextSchema = () => props.schemaParam("model.contextTokens");
  const outputSchema = () => props.schemaParam("model.maxOutputTokens");

  const save = async () => {
    setSaving(true);
    try {
      // 本 tab 只编辑容量/能力参数：key 不回写脱敏值——挂连接模型传空，
      // 自包含模型回拉 detail 取真实 key（与 ProviderModelRow 的 P0 修复同口径）。
      let apiKey = props.item.connectionId ? "" : props.item.apiKey ?? "";
      if (apiKey.includes("*")) {
        try {
          const detail = await props.client.getUserModelDetail(props.item.id);
          apiKey = detail.apiKey ?? "";
        } catch {
          apiKey = "";
        }
      }
      await props.client.updateUserModel({
        id: props.item.id,
        modelName: props.item.modelName,
        modelKey: props.item.modelKey,
        modelVersion: props.item.modelVersion,
        apiKey,
        bizScenes: props.item.bizScenes?.length ? props.item.bizScenes : DEFAULT_BIZ_SCENES,
        apiUrl: props.item.apiUrl ?? "",
        connectionId: props.item.connectionId ?? 0,
        contextTokens: Math.max(0, Math.floor(contextTokens() || 0)),
        maxOutputTokens: Math.max(0, Math.floor(maxOutputTokens() || 0)),
        supportThinking: supportThinking() ? 1 : 0,
        supportTools: supportTools() ? 1 : 0,
        supportVision: supportVision() ? 1 : 0,
        isPlatformDefault: props.item.isPlatformDefault,
      });
      props.onFeedback("ok", "已保存");
      await props.onChanged();
    } catch (error) {
      props.onFeedback("error", `保存失败: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <div class="agent-ui-settings-card">
      <div class="agent-ui-settings-card-head">
        <span class="agent-ui-settings-card-title">
          {props.item.modelName}
          <span class="agent-ui-settings-card-sub">{props.item.modelVersion}</span>
        </span>
        <button type="button" class="agent-ui-settings-btn agent-ui-settings-btn-primary" disabled={saving()} onClick={() => void save()}>
          {saving() ? "保存中…" : "保存"}
        </button>
      </div>
      <div class="agent-ui-settings-grid">
        <label class="agent-ui-settings-field">
          <span>上下文容量{contextSchema() ? `（${contextSchema()!.min ?? 0}-${contextSchema()!.max ?? 0}）` : ""}</span>
          <input
            type="number"
            min={0}
            step={contextSchema()?.step ?? 4096}
            value={contextTokens()}
            onInput={(event) => setContextTokens(event.currentTarget.valueAsNumber)}
          />
        </label>
        <label class="agent-ui-settings-field">
          <span>最大输出{outputSchema() ? `（${outputSchema()!.min ?? 0}-${outputSchema()!.max ?? 0}）` : ""}</span>
          <input
            type="number"
            min={0}
            step={outputSchema()?.step ?? 1024}
            value={maxOutputTokens()}
            onInput={(event) => setMaxOutputTokens(event.currentTarget.valueAsNumber)}
          />
        </label>
      </div>
      <div class="agent-ui-settings-checks">
        <label class="agent-ui-settings-check">
          <input type="checkbox" checked={supportThinking()} onChange={(event) => setSupportThinking(event.currentTarget.checked)} />
          <span>思考模式</span>
        </label>
        <label class="agent-ui-settings-check">
          <input type="checkbox" checked={supportTools()} onChange={(event) => setSupportTools(event.currentTarget.checked)} />
          <span>函数调用</span>
        </label>
        <label class="agent-ui-settings-check">
          <input type="checkbox" checked={supportVision()} onChange={(event) => setSupportVision(event.currentTarget.checked)} />
          <span>视觉输入</span>
        </label>
      </div>
    </div>
  );
}

// ─── Tab 3：上下文与压缩 ───────────────────────────────────────────

interface ContextTabProps {
  client: AgentClient;
  schemaParam: (key: string) => ConfigParamSchema | undefined;
  onFeedback: (kind: Feedback["kind"], text: string) => void;
}

const CONTEXT_NUMBER_FIELDS: Array<{ key: string; field: keyof Omit<UpdateContextFields, "enabled" | "microcompactEnabled" | "clearFields"> }> = [
  { key: "compact.tokenTrigger", field: "tokenTrigger" },
  { key: "compact.tokenTarget", field: "tokenTarget" },
  { key: "compact.summaryLimit", field: "summaryLimit" },
  { key: "compact.outputReserveTokens", field: "outputReserveTokens" },
  { key: "compact.bufferTokens", field: "bufferTokens" },
  { key: "compact.microcompactKeepRecent", field: "microcompactKeepRecent" },
];

interface UpdateContextFields {
  enabled?: boolean;
  tokenTrigger?: number;
  tokenTarget?: number;
  summaryLimit?: number;
  outputReserveTokens?: number;
  bufferTokens?: number;
  microcompactEnabled?: boolean;
  microcompactKeepRecent?: number;
  clearFields?: string[];
}

function ContextTab(props: ContextTabProps) {
  const [loaded, setLoaded] = createSignal(false);
  const [enabled, setEnabled] = createSignal(true);
  const [microcompactEnabled, setMicrocompactEnabled] = createSignal(false);
  const [numbers, setNumbers] = createSignal<Record<string, number>>({});
  const [sources, setSources] = createSignal<Record<string, string>>({});
  const [saving, setSaving] = createSignal(false);

  createEffect(() => {
    void props.client.getContextSetting().then((resp) => {
      setEnabled(resp.effective.enabled);
      setMicrocompactEnabled(resp.effective.microcompactEnabled);
      setNumbers({
        tokenTrigger: resp.effective.tokenTrigger,
        tokenTarget: resp.effective.tokenTarget,
        summaryLimit: resp.effective.summaryLimit,
        outputReserveTokens: resp.effective.outputReserveTokens,
        bufferTokens: resp.effective.bufferTokens,
        microcompactKeepRecent: resp.effective.microcompactKeepRecent,
      });
      const sourceMap: Record<string, string> = {};
      for (const [key, value] of Object.entries(resp.sources ?? {})) {
        sourceMap[key] = value.source;
      }
      setSources(sourceMap);
      setLoaded(true);
    }).catch((error) => {
      props.onFeedback("error", `加载上下文策略失败: ${error instanceof Error ? error.message : String(error)}`);
    });
  });

  const save = async () => {
    setSaving(true);
    try {
      await props.client.updateContextSetting({
        enabled: enabled(),
        microcompactEnabled: microcompactEnabled(),
        tokenTrigger: Math.floor(numbers().tokenTrigger || 0),
        tokenTarget: Math.floor(numbers().tokenTarget || 0),
        summaryLimit: Math.floor(numbers().summaryLimit || 0),
        outputReserveTokens: Math.floor(numbers().outputReserveTokens || 0),
        bufferTokens: Math.floor(numbers().bufferTokens || 0),
        microcompactKeepRecent: Math.floor(numbers().microcompactKeepRecent || 0),
      });
      props.onFeedback("ok", "已保存，下一个 run 生效");
    } catch (error) {
      props.onFeedback("error", `保存失败: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setSaving(false);
    }
  };

  const resetOverride = async () => {
    setSaving(true);
    try {
      await props.client.updateContextSetting({
        clearFields: ["enabled", "tokenTrigger", "tokenTarget", "summaryLimit", "outputReserveTokens", "bufferTokens", "microcompactEnabled", "microcompactKeepRecent"],
      });
      props.onFeedback("ok", "已清除覆盖，回落 yaml/默认值");
      setLoaded(false);
    } catch (error) {
      props.onFeedback("error", `清除失败: ${error instanceof Error ? error.message : String(error)}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Show when={loaded()} fallback={<div class="agent-ui-settings-empty">加载中…</div>}>
      <div class="agent-ui-settings-stack">
        <div class="agent-ui-settings-card">
          <div class="agent-ui-settings-card-head">
            <span class="agent-ui-settings-card-title">压缩策略</span>
            <div class="agent-ui-settings-row-actions">
              <button type="button" class="agent-ui-settings-btn" disabled={saving()} onClick={() => void resetOverride()}>
                清除覆盖回落 yaml
              </button>
              <button type="button" class="agent-ui-settings-btn agent-ui-settings-btn-primary" disabled={saving()} onClick={() => void save()}>
                {saving() ? "保存中…" : "保存"}
              </button>
            </div>
          </div>
          <div class="agent-ui-settings-checks">
            <label class="agent-ui-settings-check">
              <input type="checkbox" checked={enabled()} onChange={(event) => setEnabled(event.currentTarget.checked)} />
              <span>启用自动压缩</span>
              <SourceBadge source={sources()["enabled"]} />
            </label>
            <label class="agent-ui-settings-check">
              <input type="checkbox" checked={microcompactEnabled()} onChange={(event) => setMicrocompactEnabled(event.currentTarget.checked)} />
              <span>低压力微压缩（旧工具结果替换为占位引用）</span>
              <SourceBadge source={sources()["microcompactEnabled"]} />
            </label>
          </div>
          <div class="agent-ui-settings-grid">
            <For each={CONTEXT_NUMBER_FIELDS}>
              {(entry) => {
                const schemaDef = props.schemaParam(entry.key);
                return (
                  <label class="agent-ui-settings-field">
                    <span>
                      {schemaDef?.label ?? entry.field}
                      {schemaDef?.unit ? `（${schemaDef.unit}）` : ""}
                      <SourceBadge source={sources()[entry.field]} />
                    </span>
                    <input
                      type="number"
                      min={schemaDef?.min ?? 0}
                      max={schemaDef?.max}
                      step={schemaDef?.step ?? 1024}
                      value={numbers()[entry.field] ?? 0}
                      onInput={(event) => setNumbers((current) => ({ ...current, [entry.field]: event.currentTarget.valueAsNumber }))}
                    />
                    <Show when={schemaDef?.description}>
                      <em class="agent-ui-settings-field-desc">{schemaDef!.description}</em>
                    </Show>
                  </label>
                );
              }}
            </For>
          </div>
        </div>
      </div>
    </Show>
  );
}

function SourceBadge(props: { source?: string }) {
  return (
    <Show when={props.source}>
      <span class={`agent-ui-settings-source agent-ui-settings-source-${props.source}`}>
        {props.source === "override" ? "覆盖" : props.source === "yaml" ? "yaml" : "默认"}
      </span>
    </Show>
  );
}

// ─── Tab 4：高级（参数 schema 参考） ────────────────────────────────

function AdvancedTab(props: { schema: ConfigParamSchema[] }) {
  return (
    <div class="agent-ui-settings-stack">
      <div class="agent-ui-settings-hint">
        参数范围由服务端 schema 声明式下发（含 min/max/step），前端据此渲染控件；新增参数无需改前端。
      </div>
      <div class="agent-ui-settings-card">
        <table class="agent-ui-settings-table">
          <thead>
            <tr>
              <th>参数</th>
              <th>说明</th>
              <th>范围</th>
            </tr>
          </thead>
          <tbody>
            <For each={props.schema}>
              {(param) => (
                <tr>
                  <td>
                    <code>{param.key}</code>
                  </td>
                  <td>{param.description}</td>
                  <td>
                    {param.type === "int" && param.min != null && param.max != null
                      ? `${param.min} - ${param.max}${param.unit ? ` ${param.unit}` : ""}`
                      : param.enum?.join(" / ") ?? param.type}
                  </td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      </div>
      <div class="agent-ui-settings-hint">
        思考预算上限 {REASONING_BUDGET_MAX} tokens；OpenAI 系协议按预算折算 reasoning_effort 档位，
        Anthropic 系协议直接作为 thinking.budget_tokens。
      </div>
    </div>
  );
}
