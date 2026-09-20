<template>
  <div class="flex min-h-0 flex-1 flex-col gap-4">
    <div class="flex flex-wrap items-start justify-between gap-3 border-b border-[#e8e8e8] pb-4">
      <div>
        <h1 class="m-0 text-lg font-semibold">MCP 工具配置</h1>
        <p class="mb-0 mt-1 text-xs leading-5 text-[rgba(0,0,0,0.45)]">
          维护工具定义和上线状态；任意 HTTP JSON 接口均可配置为 MCP 工具。应用的工具权限请前往 MCP 工具授权。
        </p>
      </div>
      <ElButton type="primary" @click="$router.push('/tools/create')">新增工具</ElButton>
    </div>

    <div class="flex flex-wrap items-end gap-3">
      <div class="min-w-[220px] flex-1 sm:max-w-[340px]">
        <label class="mb-1.5 block text-xs text-[rgba(0,0,0,0.55)]">搜索工具</label>
        <ElInput v-model="keyword" clearable placeholder="名称、标题或描述" @keyup.enter="search" @clear="search" />
      </div>
      <div class="w-[170px]">
        <label class="mb-1.5 block text-xs text-[rgba(0,0,0,0.55)]">业务标识</label>
        <ElSelect v-model="bizTag" clearable placeholder="全部业务" :disabled="isSearching" @change="changeFilter">
          <ElOption v-for="tag in bizTagOptions" :key="tag" :label="tag" :value="tag" />
        </ElSelect>
      </div>
      <div class="w-[150px]">
        <label class="mb-1.5 block text-xs text-[rgba(0,0,0,0.55)]">工具状态</label>
        <ElSelect v-model="statusFilter" @change="resetTableSelection">
          <ElOption label="全部状态" value="all" />
          <ElOption label="已上线" :value="MCP_STATUS_ENABLED" />
          <ElOption label="已下线" :value="MCP_STATUS_DISABLED" />
        </ElSelect>
      </div>
      <ElButton type="primary" @click="search">查询</ElButton>
      <ElButton @click="resetFilters">重置</ElButton>
      <div class="hidden h-7 w-px bg-[#e8e8e8] sm:block" />
      <ElButton :disabled="!canEnable" :loading="changingStatus" @click="changeStatus(MCP_STATUS_ENABLED)">批量上线</ElButton>
      <ElButton :disabled="!canDisable" :loading="changingStatus" @click="changeStatus(MCP_STATUS_DISABLED)">批量下线</ElButton>
    </div>

    <ElAlert
      v-if="selectedRows.length && !canEnable && !canDisable"
      type="warning"
      show-icon
      :closable="false"
      title="选中工具状态不一致，请拆分为全部上线或全部下线的工具后再操作。"
    />

    <div class="min-h-0 flex-1 overflow-hidden border border-[#e8e8e8]">
      <ElTable
        ref="tableRef"
        v-loading="loading"
        :data="filteredRows"
        row-key="id"
        height="100%"
        min-height="360"
        stripe
        show-overflow-tooltip
        empty-text="暂无 MCP 工具"
        :span-method="bizTagSpanMethod"
        @selection-change="selectedRows = $event"
      >
        <ElTableColumn type="selection" width="46" />
        <ElTableColumn prop="id" label="ID" width="66" />
        <ElTableColumn label="工具" min-width="220" fixed="left">
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
        <ElTableColumn prop="url" label="请求 URL" min-width="260">
          <template #default="{ row }">
            <div class="flex items-center gap-2">
              <span class="mono min-w-0 flex-1 overflow-hidden text-ellipsis text-xs">{{ row.url }}</span>
              <ElButton link type="primary" @click="copyText(row.url)">复制</ElButton>
            </div>
          </template>
        </ElTableColumn>
        <ElTableColumn prop="description" label="描述" min-width="220" />
        <ElTableColumn label="配置" width="100">
          <template #default="{ row }"><ElButton link type="primary" @click="showDetails(row)">查看详情</ElButton></template>
        </ElTableColumn>
        <ElTableColumn label="性质" width="90">
          <template #default="{ row }">
            <ElTag :type="row.readOnly === MCP_READ_ONLY ? 'success' : 'warning'" size="small" effect="plain">
              {{ row.readOnly === MCP_READ_ONLY ? "只读" : "写操作" }}
            </ElTag>
          </template>
        </ElTableColumn>
        <ElTableColumn label="状态" width="90">
          <template #default="{ row }">
            <ElTag :type="row.status === MCP_STATUS_ENABLED ? 'success' : 'info'" size="small">
              {{ row.status === MCP_STATUS_ENABLED ? "已上线" : "已下线" }}
            </ElTag>
          </template>
        </ElTableColumn>
        <ElTableColumn prop="owner" label="Owner" min-width="120" />
        <ElTableColumn label="操作" width="150" fixed="right" align="right">
          <template #default="{ row }">
            <ElButton link type="primary" @click="editTool(row)">修改</ElButton>
            <ElButton
              link
              :type="row.status === MCP_STATUS_ENABLED ? 'danger' : 'primary'"
              @click="changeOneStatus(row)"
            >
              {{ row.status === MCP_STATUS_ENABLED ? "下线" : "上线" }}
            </ElButton>
          </template>
        </ElTableColumn>
      </ElTable>
    </div>

    <ElDialog v-model="detailsVisible" :title="`${detailTool?.title || ''} · 工具配置`" width="min(760px, 92vw)">
      <ElTabs v-model="detailTab">
        <ElTabPane label="Request Config" name="requestConfig" />
        <ElTabPane label="Input Schema" name="inputSchema" />
        <ElTabPane label="Output Schema" name="outputSchema" />
      </ElTabs>
      <pre class="mono m-0 max-h-[58vh] min-h-[180px] overflow-auto rounded bg-[#f5f5f5] p-4 text-xs leading-5">{{ formattedDetail }}</pre>
      <template #footer>
        <ElButton @click="copyText(detailValue)">复制配置</ElButton>
        <ElButton type="primary" @click="detailsVisible = false">关闭</ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { ElMessage, ElMessageBox, type TableInstance } from "element-plus";
import {
  batchUpdateToolStatus,
  listAllTools,
  searchAllTools,
  MCP_READ_ONLY,
  MCP_STATUS_DISABLED,
  MCP_STATUS_ENABLED,
  type McpStatus,
  type McpTool,
} from "@/api/mcpTools";
import { prettyJson } from "@/utils/mcpToolValidation";

type DetailField = "requestConfig" | "inputSchema" | "outputSchema";

const router = useRouter();
const tableRef = ref<TableInstance>();
const loading = ref(false);
const changingStatus = ref(false);
const keyword = ref("");
const bizTag = ref("");
const statusFilter = ref<"all" | McpStatus>("all");
const bizTagOptions = ref<string[]>([]);
const rows = ref<McpTool[]>([]);
const selectedRows = ref<McpTool[]>([]);
const detailsVisible = ref(false);
const detailTool = ref<McpTool | null>(null);
const detailTab = ref<DetailField>("requestConfig");

const isSearching = computed(() => !!keyword.value.trim());
const filteredRows = computed(() => {
  const statusRows = statusFilter.value === "all"
    ? rows.value
    : rows.value.filter((row) => row.status === statusFilter.value);

  return [...statusRows].sort((a, b) => a.bizTag.localeCompare(b.bizTag, "zh-CN"));
});
const canEnable = computed(() => selectedRows.value.length > 0 && selectedRows.value.every((row) => row.status === MCP_STATUS_DISABLED));
const canDisable = computed(() => selectedRows.value.length > 0 && selectedRows.value.every((row) => row.status === MCP_STATUS_ENABLED));
const detailValue = computed(() => detailTool.value?.[detailTab.value] || "");
const formattedDetail = computed(() =>
  detailValue.value ? prettyJson(detailValue.value) : detailTab.value === "requestConfig" ? "未配置，将使用后端兼容配置。" : "未提供配置。",
);

function bizTagSpanMethod({
  rowIndex,
  column,
}: {
  rowIndex: number;
  column: { property?: string };
}): { rowspan: number; colspan: number } {
  if (column.property !== "bizTag") return { rowspan: 1, colspan: 1 };

  const tableRows = filteredRows.value;
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

async function loadTools() {
  loading.value = true;
  resetTableSelection();
  try {
    const trimmed = keyword.value.trim();
    const result = trimmed
      ? await searchAllTools({ keyword: trimmed, enabledOnly: false })
      : await listAllTools({ enabledOnly: false, bizTag: bizTag.value || undefined });
    rows.value = result;
    bizTagOptions.value = [...new Set([...bizTagOptions.value, ...rows.value.map((row) => row.bizTag)])].sort();
  } finally {
    loading.value = false;
  }
}

function search() {
  if (keyword.value.trim()) bizTag.value = "";
  void loadTools();
}

function changeFilter() {
  void loadTools();
}

function resetFilters() {
  keyword.value = "";
  bizTag.value = "";
  statusFilter.value = "all";
  void loadTools();
}

function resetTableSelection() {
  selectedRows.value = [];
  tableRef.value?.clearSelection();
}

async function changeStatus(status: McpStatus, targets = selectedRows.value) {
  if (!targets.length) return;
  const action = status === MCP_STATUS_ENABLED ? "上线" : "下线";
  try {
    await ElMessageBox.confirm(`确认${action}选中的 ${targets.length} 个 MCP 工具？`, `${action}工具`, { type: "warning" });
  } catch {
    return;
  }
  changingStatus.value = true;
  try {
    await batchUpdateToolStatus(targets.map((row) => row.id), status);
    ElMessage.success(`已${action} ${targets.length} 个工具`);
    await loadTools();
  } finally {
    changingStatus.value = false;
  }
}

function changeOneStatus(row: McpTool) {
  void changeStatus(row.status === MCP_STATUS_ENABLED ? MCP_STATUS_DISABLED : MCP_STATUS_ENABLED, [row]);
}

function showDetails(row: McpTool) {
  detailTool.value = row;
  detailTab.value = "requestConfig";
  detailsVisible.value = true;
}

function editTool(row: McpTool) {
  void router.push({ name: "tool-edit", params: { id: row.id } });
}

async function copyText(value: string) {
  if (!value) {
    ElMessage.info("当前配置为空");
    return;
  }
  await navigator.clipboard.writeText(value);
  ElMessage.success("已复制");
}

onMounted(loadTools);
</script>
