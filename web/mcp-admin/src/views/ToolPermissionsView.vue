<template>
  <div class="flex min-h-0 flex-1 flex-col gap-4">
    <div class="border-b border-[#e8e8e8] pb-4">
      <h1 class="m-0 text-lg font-semibold">MCP 工具授权</h1>
      <p class="mb-0 mt-1 text-xs leading-5 text-[rgba(0,0,0,0.45)]">按 App 管理其 MCP 工具绑定与授权状态，凭证供 MCP 客户端以 Bearer 方式接入。</p>
    </div>

    <section class="grid gap-4 lg:grid-cols-[minmax(320px,0.8fr)_minmax(420px,1.2fr)]">
      <div class="border-r-0 border-[#e8e8e8] pr-0 lg:border-r lg:pr-5">
        <div class="mb-3 text-sm font-semibold">查询应用</div>
        <div class="flex gap-2">
          <ElSelect
            v-model="selectedAppId"
            class="min-w-0 flex-1"
            filterable
            remote
            clearable
            :debounce="0"
            :remote-method="searchApps"
            :loading="appsLoading"
            placeholder="输入应用名称搜索"
            @change="changeApp"
          >
            <ElOption v-for="app in apps" :key="app.id" :label="app.appName" :value="app.id">
              <div class="flex items-center justify-between gap-4">
                <span class="min-w-0 truncate">{{ app.appName }}</span>
                <span class="flex shrink-0 items-center gap-2">
                  <span class="mono text-xs text-[rgba(0,0,0,0.45)]">#{{ app.id }}</span>
                  <span class="mono text-xs text-[rgba(0,0,0,0.45)]">{{ app.owner }}</span>
                </span>
              </div>
            </ElOption>
            <template #empty>
              <div class="px-3 py-2 text-xs text-[rgba(0,0,0,0.45)]">没有匹配的应用，可在下方新建</div>
            </template>
          </ElSelect>
        </div>
        <div v-if="selectedApp" class="mt-4 grid grid-cols-2 gap-3 bg-[#f7f8fa] p-3">
          <div>
            <div class="text-xs text-[rgba(0,0,0,0.45)]">应用名称</div>
            <div class="mono mt-1 font-semibold">{{ selectedApp.appName }}</div>
          </div>
          <div>
            <div class="text-xs text-[rgba(0,0,0,0.45)]">App ID</div>
            <div class="mono mt-1 font-semibold">{{ selectedApp.id }}</div>
          </div>
          <div>
            <div class="text-xs text-[rgba(0,0,0,0.45)]">绑定工具（白名单）</div>
            <div class="mono mt-1 font-semibold">{{ selectedApp.toolCount }} 个</div>
          </div>
          <div>
            <div class="text-xs text-[rgba(0,0,0,0.45)]">应用状态</div>
            <div class="mono mt-1 font-semibold">{{ selectedApp.status === MCP_STATUS_ENABLED ? "启用" : "停用" }}</div>
          </div>
        </div>

        <div class="mt-5 border-t border-dashed border-[#e8e8e8] pt-4">
          <div class="mb-3 text-sm font-semibold">新建应用</div>
          <ElForm label-position="top" class="grid gap-x-4 sm:grid-cols-2">
            <ElFormItem label="应用名称" required>
              <ElInput v-model="newApp.appName" maxlength="64" show-word-limit placeholder="如 search-agent" />
            </ElFormItem>
            <ElFormItem label="Owner" required>
              <ElInput v-model="newApp.owner" maxlength="64" class="mono" placeholder="应用负责人" />
            </ElFormItem>
            <ElFormItem label="描述（可选）">
              <ElInput v-model="newApp.description" maxlength="255" show-word-limit placeholder="用途说明" />
            </ElFormItem>
          </ElForm>
          <ElButton type="primary" plain :disabled="!newApp.appName.trim() || !newApp.owner.trim()" :loading="creating" @click="createNewApp">
            创建应用
          </ElButton>
          <p class="mb-0 mt-2 text-xs leading-5 text-[rgba(0,0,0,0.45)]">创建后自动选中，App Key / Secret 在下方凭证区展示。新应用默认未绑定任何工具（tools/list 为空），请在右侧勾选工具并保存绑定。</p>
        </div>
      </div>

      <div>
        <ElAlert type="info" show-icon :closable="false" title="工具可见性由应用绑定白名单决定"
          description="工具是全局基础集合（对全部 caller 可见），应用能看到并调用的工具 = 为它勾选绑定的工具子集且处于上线状态。要调整某个业务方的可见范围：改它所用应用的绑定清单，或对具体工具执行上线/下线。" />
      </div>
    </section>

    <section v-if="selectedApp" class="border border-[#b7eb8f] bg-[#f6ffed] p-4">
      <div class="mb-3 flex flex-wrap items-start justify-between gap-2">
        <div>
          <div class="font-semibold text-[#135200]">{{ selectedApp.appName }} 接入凭证</div>
          <div class="mt-1 text-xs text-[#3f6600]">MCP 客户端以 Bearer &lt;app_key&gt;:&lt;app_secret&gt; 访问 /api/mcp，请安全发放。</div>
        </div>
        <ElButton link type="primary" @click="credentialsVisible = !credentialsVisible">
          {{ credentialsVisible ? "收起" : "展开" }}
        </ElButton>
      </div>
      <div v-if="credentialsVisible" class="grid gap-3 md:grid-cols-2">
        <div>
          <div class="mb-1 text-xs text-[#3f6600]">App Key</div>
          <div class="flex items-center gap-2">
            <code class="mono min-w-0 flex-1 overflow-hidden text-ellipsis bg-white px-3 py-2 text-xs">{{ selectedApp.appKey }}</code>
            <ElButton size="small" @click="copyText(selectedApp.appKey)">复制</elButton>
          </div>
        </div>
        <div>
          <div class="mb-1 text-xs text-[#3f6600]">App Secret</div>
          <div class="flex items-center gap-2">
            <code class="mono min-w-0 flex-1 overflow-hidden text-ellipsis bg-white px-3 py-2 text-xs">
              {{ showSecret ? selectedApp.appSecret : maskSecret(selectedApp.appSecret || "") }}
            </code>
            <ElButton size="small" @click="showSecret = !showSecret">{{ showSecret ? "隐藏" : "显示" }}</ElButton>
            <ElButton size="small" @click="copyText(selectedApp.appSecret || '')">复制</ElButton>
          </div>
        </div>
      </div>
    </section>

    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <span class="text-sm font-semibold">应用可见工具（绑定白名单）</span>
        <span v-if="selectedAppId" class="ml-2 text-xs text-[rgba(0,0,0,0.45)]">
          已绑定 {{ checkedCount }} 个{{ dirty ? "（有未保存修改）" : "" }}
        </span>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <ElCheckbox v-model="includeUnbound" :disabled="!selectedAppId">同时显示未绑定的工具</ElCheckbox>
        <ElSelect v-model="toolStatusFilter" class="w-[150px]" :disabled="!selectedAppId">
          <ElOption label="全部工具状态" value="all" />
          <ElOption label="工具已上线" :value="MCP_STATUS_ENABLED" />
          <ElOption label="工具已下线" :value="MCP_STATUS_DISABLED" />
        </ElSelect>
        <ElButton size="small" :disabled="!selectedAppId" @click="checkVisibleRows">勾选当前列表</ElButton>
        <ElButton size="small" :disabled="!selectedAppId" @click="checkedToolIds = new Set()">清空勾选</ElButton>
        <ElButton type="primary" size="small" :loading="saving" :disabled="!selectedAppId || !dirty" @click="saveBindings">
          保存绑定
        </ElButton>
      </div>
    </div>

    <div class="min-h-[320px] flex-1 overflow-hidden border border-[#e8e8e8]">
      <ElTable
        v-loading="querying"
        :data="filteredPermissionRows"
        row-key="toolId"
        height="100%"
        stripe
        empty-text="选择应用后勾选工具绑定"
        :span-method="bizTagSpanMethod"
      >
        <ElTableColumn label="绑定" width="70">
          <template #default="{ row }">
            <ElCheckbox
              v-if="!row.unknown"
              :model-value="checkedToolIds.has(row.toolId)"
              @update:model-value="(value: string | number | boolean) => toggleChecked(row.toolId, Boolean(value))"
            />
            <span v-else class="text-xs text-[rgba(0,0,0,0.45)]">—</span>
          </template>
        </ElTableColumn>
        <ElTableColumn label="工具" min-width="220">
          <template #default="{ row }">
            <div class="py-1">
              <div class="font-semibold">{{ row.title }}</div>
              <div class="mono mt-1 text-xs text-[rgba(0,0,0,0.45)]">{{ row.name }}</div>
            </div>
          </template>
        </ElTableColumn>
        <ElTableColumn prop="bizTag" label="业务标识" min-width="130">
          <template #default="{ row }">{{ row.bizTag || "—" }}</template>
        </ElTableColumn>
        <ElTableColumn label="性质" width="90">
          <template #default="{ row }">{{ row.readOnly === MCP_READ_ONLY ? "只读" : "写操作" }}</template>
        </ElTableColumn>
        <ElTableColumn label="工具状态" width="110">
          <template #default="{ row }">
            <ElTag :type="row.toolStatus === MCP_STATUS_ENABLED ? 'success' : 'info'" size="small" effect="plain">
              {{ row.toolStatus === MCP_STATUS_ENABLED ? "已上线" : "已下线" }}
            </ElTag>
          </template>
        </ElTableColumn>
        <ElTableColumn label="对本应用" width="130">
          <template #default="{ row }">
            <ElTag :type="permissionTagType(row.permissionStatus)" size="small">
              {{ permissionLabel(row.permissionStatus) }}
            </ElTag>
          </template>
        </ElTableColumn>
      </ElTable>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { ElMessage } from "element-plus";
import {
  listAllTools,
  MCP_PERMISSION_DISABLED,
  MCP_PERMISSION_ENABLED,
  MCP_PERMISSION_UNBOUND,
  MCP_READ_ONLY,
  MCP_WRITE_OPERATION,
  MCP_STATUS_DISABLED,
  MCP_STATUS_ENABLED,
  type McpPermissionStatus,
  type McpStatus,
  type McpTool,
} from "@/api/mcpTools";
import { adminAuth } from "@/api/adminAuth";
import { createApp, grantAppTools, listAppTools, listApps, type McpApp } from "@/api/app";

/**
 * 表格行：绑定白名单在前端与全量工具（工具基础集合）做 join，
 * 补齐业务标识 / 性质 / 工具状态；unknown=工具已被删除但绑定残留。
 */
interface PermissionRow {
  toolId: number;
  name: string;
  title: string;
  bizTag: string;
  readOnly: number;
  toolStatus: number;
  permissionStatus: McpPermissionStatus;
  unknown?: boolean;
}

const appsLoading = ref(false);
const apps = ref<McpApp[]>([]);
const selectedAppId = ref<number | null>(null);
const credentialsVisible = ref(true);
const showSecret = ref(false);
const querying = ref(false);
const creating = ref(false);
const saving = ref(false);
const allTools = ref<McpTool[]>([]);
const bindings = ref<Map<number, McpPermissionStatus>>(new Map());
/** 编辑态勾选集合（保存前与服务端绑定状态隔离）。 */
const checkedToolIds = ref<Set<number>>(new Set());
const includeUnbound = ref(false);
const toolStatusFilter = ref<"all" | McpStatus>("all");
const newApp = reactive({
  appName: "",
  owner: adminAuth.operator || "admin",
  description: "",
});

const selectedApp = computed(() => apps.value.find((app) => app.id === selectedAppId.value) || null);

const permissionRows = computed<PermissionRow[]>(() => {
  const rows: PermissionRow[] = [];
  const seen = new Set<number>();
  for (const tool of allTools.value) {
    const status = bindings.value.get(tool.id);
    if (status === undefined && !includeUnbound.value) continue;
    seen.add(tool.id);
    rows.push({
      toolId: tool.id,
      name: tool.name,
      title: tool.title,
      bizTag: tool.bizTag,
      readOnly: tool.readOnly,
      toolStatus: tool.status,
      permissionStatus: status ?? MCP_PERMISSION_UNBOUND,
    });
  }
  // 工具被删但绑定仍在的兜底行（不可勾选；下次保存绑定即自动清理）。
  for (const [toolId, status] of bindings.value) {
    if (!seen.has(toolId)) {
      rows.push({
        toolId,
        name: `#${toolId}`,
        title: "未知工具（可能已删除）",
        bizTag: "",
        readOnly: MCP_WRITE_OPERATION,
        toolStatus: MCP_STATUS_DISABLED,
        permissionStatus: status,
        unknown: true,
      });
    }
  }
  return rows;
});

const filteredPermissionRows = computed(() => {
  const statusRows = toolStatusFilter.value === "all"
    ? permissionRows.value
    : permissionRows.value.filter((row) => row.toolStatus === toolStatusFilter.value);

  return [...statusRows].sort((a, b) => a.bizTag.localeCompare(b.bizTag, "zh-CN"));
});

const checkedCount = computed(() => checkedToolIds.value.size);

/** 勾选集合与服务端绑定清单不一致 = 有未保存修改。 */
const dirty = computed(() => {
  const saved = new Set(bindings.value.keys());
  if (saved.size !== checkedToolIds.value.size) return true;
  for (const toolId of saved) {
    if (!checkedToolIds.value.has(toolId)) return true;
  }
  return false;
});

function bizTagSpanMethod({
  rowIndex,
  column,
}: {
  rowIndex: number;
  column: { property?: string };
}): { rowspan: number; colspan: number } {
  if (column.property !== "bizTag") return { rowspan: 1, colspan: 1 };

  const tableRows = filteredPermissionRows.value;
  const current = tableRows[rowIndex];
  if (!current) return { rowspan: 1, colspan: 1 };
  if (rowIndex > 0 && tableRows[rowIndex - 1]!.bizTag === current.bizTag) {
    return { rowspan: 0, colspan: 0 };
  }

  let rowspan = 1;
  for (let index = rowIndex + 1; index < tableRows.length; index++) {
    if (tableRows[index]!.bizTag !== current.bizTag) break;
    rowspan++;
  }
  return { rowspan, colspan: 1 };
}

async function loadAllTools() {
  allTools.value = await listAllTools({ enabledOnly: false });
}

async function searchApps(keyword: string) {
  appsLoading.value = true;
  try {
    const result = await listApps(keyword.trim());
    apps.value = result.list;
  } finally {
    appsLoading.value = false;
  }
}

function changeApp() {
  credentialsVisible.value = true;
  showSecret.value = false;
  void loadBindings();
}

async function loadBindings() {
  if (!selectedAppId.value) {
    bindings.value = new Map();
    checkedToolIds.value = new Set();
    return;
  }
  querying.value = true;
  try {
    const result = await listAppTools(selectedAppId.value);
    bindings.value = new Map(result.list.map((binding) => [binding.toolId, binding.status as McpPermissionStatus]));
    checkedToolIds.value = new Set(bindings.value.keys());
  } finally {
    querying.value = false;
  }
}

function toggleChecked(toolId: number, checked: boolean) {
  const next = new Set(checkedToolIds.value);
  if (checked) next.add(toolId);
  else next.delete(toolId);
  checkedToolIds.value = next;
}

/** 勾选当前过滤后列表里的全部已知工具（配合「工具已上线」筛选可快速只绑在线工具）。 */
function checkVisibleRows() {
  const next = new Set(checkedToolIds.value);
  for (const row of filteredPermissionRows.value) {
    if (!row.unknown) next.add(row.toolId);
  }
  checkedToolIds.value = next;
}

async function saveBindings() {
  if (!selectedAppId.value) return;
  saving.value = true;
  try {
    await grantAppTools(selectedAppId.value, [...checkedToolIds.value]);
    ElMessage.success(`已保存绑定：${checkedToolIds.value.size} 个工具`);
    await loadBindings();
    await searchApps("");
  } finally {
    saving.value = false;
  }
}

async function createNewApp() {
  if (!newApp.appName.trim() || !newApp.owner.trim()) return;
  creating.value = true;
  try {
    const { app } = await createApp({
      appName: newApp.appName.trim(),
      owner: newApp.owner.trim(),
      description: newApp.description.trim() || undefined,
    });
    ElMessage.success(`应用 ${app.appName} 已创建`);
    newApp.appName = "";
    newApp.description = "";
    if (!apps.value.some((item) => item.id === app.id)) apps.value.unshift(app);
    selectedAppId.value = app.id;
    changeApp();
  } finally {
    creating.value = false;
  }
}

function permissionLabel(status: McpPermissionStatus) {
  if (status === MCP_PERMISSION_ENABLED) return "已绑定";
  if (status === MCP_PERMISSION_DISABLED) return "已下线";
  return "未绑定";
}

function permissionTagType(status: McpPermissionStatus) {
  if (status === MCP_PERMISSION_ENABLED) return "success";
  if (status === MCP_PERMISSION_DISABLED) return "warning";
  return "info";
}

function maskSecret(secret: string) {
  if (secret.length <= 8) return "••••••••";
  return `${secret.slice(0, 4)}••••••••${secret.slice(-4)}`;
}

async function copyText(value: string) {
  await navigator.clipboard.writeText(value);
  ElMessage.success("已复制");
}

onMounted(async () => {
  await Promise.all([loadAllTools(), searchApps("")]);
});
</script>
