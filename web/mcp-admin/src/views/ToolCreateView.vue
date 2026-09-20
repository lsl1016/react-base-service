<template>
  <div class="mx-auto w-full max-w-[980px] space-y-5">
    <div class="flex flex-wrap items-start justify-between gap-3 border-b border-[#e8e8e8] pb-4">
      <div class="flex items-start gap-3">
        <ElButton @click="$router.push('/tools')">返回</ElButton>
        <div>
          <h1 class="m-0 text-lg font-semibold">批量新增 MCP 工具</h1>
          <p class="mb-0 mt-1 text-xs leading-5 text-[rgba(0,0,0,0.45)]">填写并保存当前工具后，可继续新增下一个工具。</p>
        </div>
      </div>
      <div class="flex gap-2">
        <ElButton :disabled="!allDraftsSaved" @click="addDraft">新增工具</ElButton>
        <ElButton type="primary" :disabled="!canSubmit" :loading="submitting" @click="submitAll">提交 {{ drafts.length }} 个工具</ElButton>
      </div>
    </div>

    <ElAlert
      v-if="!currentValid"
      type="info"
      show-icon
      :closable="false"
      :title="currentError || '请填写完整工具信息'"
    />

    <ElTabs v-model="activeId" type="card" @tab-remove="removeDraft">
      <ElTabPane v-for="(draft, index) in drafts" :key="draft.tabId" :name="draft.tabId" :closable="drafts.length > 1">
        <template #label>
          <span class="inline-flex max-w-[180px] items-center gap-2">
            <span class="truncate">{{ draft.title || draft.name || `新工具 ${index + 1}` }}</span>
            <span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="draft.saved ? 'bg-[#52c41a]' : 'bg-[#faad14]'" />
          </span>
        </template>
      </ElTabPane>
    </ElTabs>

    <ElForm v-if="current" label-position="top" class="grid gap-x-5 md:grid-cols-2">
      <ElFormItem label="业务标识">
        <ElSelect
          v-model="current.bizTag"
          class="w-full"
          filterable
          allow-create
          default-first-option
          :reserve-keyword="false"
          placeholder="选择已有业务标识或输入新标识，选填"
          @change="markDirty"
        >
          <ElOption v-for="tag in bizTagOptions" :key="tag" :label="tag" :value="tag" />
        </ElSelect>
        <div class="mt-1 text-xs leading-5 text-[rgba(0,0,0,0.45)]">业务标识仅用于列表分组展示。</div>
      </ElFormItem>
      <ElFormItem label="Owner" required>
        <ElInput v-model="current.owner" maxlength="64" show-word-limit class="mono" placeholder="工具负责人" @input="markDirty" />
      </ElFormItem>
      <ElFormItem label="工具名" required>
        <ElInput v-model="current.name" maxlength="80" show-word-limit class="mono" placeholder="全局唯一，如 knowledge_search" @input="markDirty" />
        <div class="mt-1 text-xs leading-5 text-[#ad6800]">工具名全局唯一，创建后请谨慎修改。</div>
      </ElFormItem>
      <ElFormItem label="工具标题" required>
        <ElInput v-model="current.title" maxlength="64" show-word-limit placeholder="面向用户的可读标题" @input="markDirty" />
      </ElFormItem>
      <ElFormItem label="请求 URL" required class="md:col-span-2">
        <ElInput v-model="current.url" maxlength="512" show-word-limit class="mono" placeholder="https://service.example.com/api/tool" @input="markDirty" />
      </ElFormItem>
      <ElFormItem label="Request Config（JSON，可选）" class="md:col-span-2">
        <ElInput
          v-model="current.requestConfig"
          type="textarea"
          :rows="7"
          class="mono schema-input"
          spellcheck="false"
          placeholder='{"method":"POST","timeout_ms":10000,"headers":{"Content-Type":"application/json"}}'
          @input="markDirty"
        />
        <div class="mt-2 flex w-full flex-wrap justify-between gap-2">
          <span class="text-xs" :class="requestConfigError ? 'text-[#cf1322]' : 'text-[#389e0d]'">
            {{ requestConfigError || (current.requestConfig.trim() ? "请求配置有效" : "留空时使用后端兼容配置") }}
          </span>
          <ElButton link type="primary" :disabled="!current.requestConfig.trim()" @click="formatJsonField('requestConfig')">格式化 JSON</ElButton>
        </div>
      </ElFormItem>
      <ElFormItem label="工具描述" required class="md:col-span-2">
        <ElInput v-model="current.description" type="textarea" :rows="3" maxlength="2048" show-word-limit @input="markDirty" />
      </ElFormItem>
      <ElFormItem label="操作性质" required>
        <ElSegmented v-model="current.readOnly" :options="readOnlyOptions" @change="markDirty" />
      </ElFormItem>
      <ElFormItem label="初始状态" required>
        <ElSegmented v-model="current.status" :options="statusOptions" @change="markDirty" />
      </ElFormItem>
      <ElFormItem label="Input Schema（JSON Schema 2020-12）" required class="md:col-span-2">
        <ElInput v-model="current.inputSchema" type="textarea" :rows="12" class="mono schema-input" spellcheck="false" @input="markDirty" />
        <div class="mt-2 flex w-full flex-wrap justify-between gap-2">
          <span class="text-xs" :class="inputSchemaError ? 'text-[#cf1322]' : 'text-[#389e0d]'">
            {{ inputSchemaError || "Schema 有效，顶层类型为 object" }}
          </span>
          <ElButton link type="primary" @click="formatJsonField('inputSchema')">格式化 JSON</ElButton>
        </div>
      </ElFormItem>
      <ElFormItem label="Output Schema（JSON Schema 2020-12）" required class="md:col-span-2">
        <ElInput v-model="current.outputSchema" type="textarea" :rows="12" class="mono schema-input" spellcheck="false" @input="markDirty" />
        <div class="mt-2 flex w-full flex-wrap justify-between gap-2">
          <span class="text-xs" :class="outputSchemaError ? 'text-[#cf1322]' : 'text-[#389e0d]'">
            {{ outputSchemaError || "Schema 有效，顶层类型为 object" }}
          </span>
          <ElButton link type="primary" @click="formatJsonField('outputSchema')">格式化 JSON</ElButton>
        </div>
      </ElFormItem>
    </ElForm>

    <div class="sticky bottom-0 flex flex-wrap items-center justify-between gap-3 border-t border-[#e8e8e8] bg-white py-3">
      <span class="text-xs text-[rgba(0,0,0,0.45)]">
        {{ current?.saved ? "当前工具已保存到待提交列表" : "当前工具存在未保存修改" }}
      </span>
      <ElButton type="primary" plain :disabled="!currentValid || current?.saved" @click="saveCurrent">保存当前工具</ElButton>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { useRouter } from "vue-router";
import { ElMessage, ElMessageBox } from "element-plus";
import {
  batchCreateTools,
  listAllTools,
  MCP_READ_ONLY,
  MCP_STATUS_DISABLED,
  MCP_STATUS_ENABLED,
  MCP_WRITE_OPERATION,
  type McpToolMutation,
} from "@/api/mcpTools";
import { adminAuth } from "@/api/adminAuth";
import { compactJson, validateHttpUrl, validateObjectSchema, validateRequestConfig } from "@/utils/mcpToolValidation";

type ToolDraft = Omit<McpToolMutation, "requestConfig"> & {
  requestConfig: string;
  tabId: string;
  saved: boolean;
};

const router = useRouter();
const submitting = ref(false);
const bizTagOptions = ref<string[]>([]);
let draftNumber = 1;

function makeDraft(): ToolDraft {
  return {
    tabId: `tool-${draftNumber++}`,
    saved: false,
    bizTag: "",
    url: "",
    requestConfig: JSON.stringify({ method: "POST", timeout_ms: 10000, headers: {} }, null, 2),
    name: "",
    title: "",
    description: "",
    inputSchema: JSON.stringify({ type: "object", properties: {}, required: [] }, null, 2),
    outputSchema: JSON.stringify({ type: "object", properties: {} }, null, 2),
    readOnly: MCP_READ_ONLY,
    status: MCP_STATUS_DISABLED,
    owner: adminAuth.operator || "admin",
  };
}

const drafts = ref<ToolDraft[]>([makeDraft()]);
const activeId = ref(drafts.value[0]!.tabId);
const current = computed(() => drafts.value.find((draft) => draft.tabId === activeId.value) ?? drafts.value[0]);
const requestConfigError = computed(() => validateRequestConfig(current.value?.requestConfig || ""));
const inputSchemaError = computed(() => validateObjectSchema(current.value?.inputSchema || "", "Input Schema"));
const outputSchemaError = computed(() => validateObjectSchema(current.value?.outputSchema || "", "Output Schema"));
const currentError = computed(() => validateDraft(current.value));
const currentValid = computed(() => !currentError.value);
const allDraftsSaved = computed(() => drafts.value.every((draft) => draft.saved));
const canSubmit = computed(() => drafts.value.length > 0 && allDraftsSaved.value);

const readOnlyOptions = [
  { label: "只读", value: MCP_READ_ONLY },
  { label: "写操作", value: MCP_WRITE_OPERATION },
];
const statusOptions = [
  { label: "下线", value: MCP_STATUS_DISABLED },
  { label: "上线", value: MCP_STATUS_ENABLED },
];

function validateDraft(draft?: ToolDraft) {
  if (!draft) return "请新增工具";
  if (!draft.owner.trim()) return "请填写 Owner";
  if (!draft.name.trim()) return "请填写工具名";
  if (!draft.title.trim()) return "请填写工具标题";
  if (!draft.url.trim()) return "请填写请求 URL";
  const urlError = validateHttpUrl(draft.url);
  if (urlError) return urlError;
  const configError = validateRequestConfig(draft.requestConfig || "");
  if (configError) return configError;
  if (!draft.description.trim()) return "请填写工具描述";
  const inputError = validateObjectSchema(draft.inputSchema, "Input Schema");
  if (inputError) return inputError;
  const outputError = validateObjectSchema(draft.outputSchema, "Output Schema");
  if (outputError) return outputError;
  return "";
}

function markDirty() {
  if (current.value) current.value.saved = false;
}

function saveCurrent() {
  if (!current.value || validateDraft(current.value)) return;
  current.value.saved = true;
  ElMessage.success("当前工具已加入待提交列表");
}

function addDraft() {
  if (!allDraftsSaved.value) return;
  const draft = makeDraft();
  drafts.value.push(draft);
  activeId.value = draft.tabId;
}

async function removeDraft(name: string | number) {
  const target = drafts.value.find((draft) => draft.tabId === String(name));
  if (!target) return;
  if (!target.saved && (target.name || target.title || target.bizTag)) {
    try {
      await ElMessageBox.confirm("当前工具有未保存内容，确认移除？", "移除工具", { type: "warning" });
    } catch {
      return;
    }
  }
  const index = drafts.value.indexOf(target);
  drafts.value.splice(index, 1);
  if (!drafts.value.length) drafts.value.push(makeDraft());
  activeId.value = drafts.value[Math.min(index, drafts.value.length - 1)]!.tabId;
}

function formatJsonField(field: "requestConfig" | "inputSchema" | "outputSchema") {
  if (!current.value) return;
  try {
    current.value[field] = JSON.stringify(JSON.parse(current.value[field] || "{}"), null, 2);
    current.value.saved = false;
  } catch {
    ElMessage.warning("当前内容不是合法 JSON");
  }
}

async function submitAll() {
  if (!canSubmit.value) return;
  submitting.value = true;
  try {
    const payload = drafts.value.map(({ tabId: _tabId, saved: _saved, ...tool }) => ({
      ...tool,
      bizTag: tool.bizTag.trim(),
      url: tool.url.trim(),
      requestConfig: compactJson(tool.requestConfig || ""),
      name: tool.name.trim(),
      title: tool.title.trim(),
      description: tool.description.trim(),
      inputSchema: JSON.stringify(JSON.parse(tool.inputSchema)),
      outputSchema: JSON.stringify(JSON.parse(tool.outputSchema)),
      owner: tool.owner.trim(),
    }));
    await batchCreateTools(payload);
    ElMessage.success(`已创建 ${payload.length} 个 MCP 工具`);
    await router.push("/tools");
  } finally {
    submitting.value = false;
  }
}

async function loadBizTagOptions() {
  try {
    const tools = await listAllTools({ enabledOnly: false });
    bizTagOptions.value = [...new Set(tools.map((tool) => tool.bizTag).filter(Boolean))].sort();
  } catch {
    // 业务标识选项加载失败不阻塞创建：允许直接输入新标识。
  }
}

onMounted(loadBizTagOptions);
</script>

<style scoped>
.schema-input :deep(textarea) {
  font-family: ui-monospace, "SF Mono", Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  line-height: 1.65;
}
</style>
