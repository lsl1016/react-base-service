<template>
  <div v-loading="loading" class="mx-auto w-full max-w-[900px] space-y-5">
    <div class="flex flex-wrap items-start justify-between gap-3 border-b border-[#e8e8e8] pb-4">
      <div class="flex items-start gap-3">
        <ElButton @click="$router.push('/tools')">返回</ElButton>
        <div>
          <h1 class="m-0 text-lg font-semibold">修改 MCP 工具</h1>
          <div v-if="original" class="mt-1 flex flex-wrap items-center gap-2 text-xs text-[rgba(0,0,0,0.45)]">
            <strong class="text-[rgba(0,0,0,0.65)]">{{ original.title }}</strong>
            <span class="mono">{{ original.name }}</span>
            <ElTag :type="original.status === MCP_STATUS_ENABLED ? 'success' : 'info'" size="small">
              {{ original.status === MCP_STATUS_ENABLED ? "已上线" : "已下线" }}
            </ElTag>
          </div>
        </div>
      </div>
      <ElButton type="primary" :disabled="!changeCount" :loading="submitting" @click="submitChanges">
        提交变更<span v-if="changeCount">（{{ changeCount }}）</span>
      </ElButton>
    </div>

    <ElAlert type="info" show-icon :closable="false" title="修改字段后请先记录该字段；状态变更统一在工具列表执行。" />

    <ElForm v-if="original" label-position="top" class="grid gap-x-5 md:grid-cols-2">
      <ElFormItem label="业务标识">
        <div class="flex w-full gap-2">
          <ElSelect
            v-model="draft.bizTag"
            class="min-w-0 flex-1"
            filterable
            allow-create
            default-first-option
            :reserve-keyword="false"
            placeholder="选择已有业务标识或输入新标识"
          >
            <ElOption v-for="tag in bizTagOptions" :key="tag" :label="tag" :value="tag" />
          </ElSelect>
          <ElButton @click="recordField('bizTag')">记录变更</ElButton>
        </div>
        <ElTag v-if="isRecorded('bizTag')" class="mt-1" size="small" type="success" effect="plain">已记录</ElTag>
      </ElFormItem>
      <ElFormItem label="Owner（不可编辑）">
        <ElInput :model-value="original.owner" disabled />
      </ElFormItem>
      <ElFormItem label="工具名" required>
        <ElInput v-model="draft.name" maxlength="80" class="mono">
          <template #append><ElButton @click="recordField('name')">记录变更</ElButton></template>
        </ElInput>
        <div class="mt-1 flex w-full flex-wrap items-center justify-between gap-2">
          <span class="text-xs text-[#ad6800]">工具名全局唯一，请谨慎修改。</span>
          <ElTag v-if="isRecorded('name')" size="small" type="success" effect="plain">已记录</ElTag>
        </div>
      </ElFormItem>
      <ElFormItem label="工具标题" required>
        <ElInput v-model="draft.title" maxlength="64">
          <template #append><ElButton @click="recordField('title')">记录变更</ElButton></template>
        </ElInput>
        <ElTag v-if="isRecorded('title')" size="small" type="success" effect="plain">已记录</ElTag>
      </ElFormItem>
      <ElFormItem label="请求 URL" required class="md:col-span-2">
        <ElInput v-model="draft.url" maxlength="512" class="mono">
          <template #append><ElButton @click="recordField('url')">记录变更</ElButton></template>
        </ElInput>
        <div class="mt-1 flex w-full flex-wrap items-center justify-between gap-2">
          <span v-if="urlError" class="text-xs text-[#cf1322]">{{ urlError }}</span>
          <ElTag v-if="isRecorded('url')" size="small" type="success" effect="plain">已记录</ElTag>
        </div>
      </ElFormItem>
      <ElFormItem label="Request Config（JSON，可选）" class="md:col-span-2">
        <ElInput v-model="draft.requestConfig" type="textarea" :rows="7" class="mono schema-input" spellcheck="false" />
        <div class="mt-2 flex w-full flex-wrap items-center justify-between gap-2">
          <span class="text-xs" :class="requestConfigError ? 'text-[#cf1322]' : 'text-[#389e0d]'">
            {{ requestConfigError || (draft.requestConfig.trim() ? "请求配置有效" : "留空时使用后端兼容配置") }}
          </span>
          <div class="flex items-center gap-2">
            <ElTag v-if="isRecorded('requestConfig')" size="small" type="success" effect="plain">已记录</ElTag>
            <ElButton size="small" :disabled="!draft.requestConfig.trim()" @click="formatJsonField('requestConfig')">格式化</ElButton>
            <ElButton size="small" :disabled="!!requestConfigError" @click="recordField('requestConfig')">记录变更</ElButton>
          </div>
        </div>
      </ElFormItem>
      <ElFormItem label="工具描述" required class="md:col-span-2">
        <ElInput v-model="draft.description" type="textarea" :rows="4" maxlength="2048" show-word-limit />
        <div class="mt-2 flex w-full justify-end gap-2">
          <ElTag v-if="isRecorded('description')" size="small" type="success" effect="plain">已记录</ElTag>
          <ElButton size="small" @click="recordField('description')">记录变更</ElButton>
        </div>
      </ElFormItem>
      <ElFormItem label="操作性质" required class="md:col-span-2">
        <div class="flex w-full flex-wrap items-center gap-3">
          <ElSegmented v-model="draft.readOnly" :options="readOnlyOptions" />
          <ElButton size="small" @click="recordField('readOnly')">记录变更</ElButton>
          <ElTag v-if="isRecorded('readOnly')" size="small" type="success" effect="plain">已记录</ElTag>
        </div>
      </ElFormItem>
      <ElFormItem label="Input Schema（JSON Schema 2020-12）" required class="md:col-span-2">
        <ElInput v-model="draft.inputSchema" type="textarea" :rows="14" class="mono schema-input" spellcheck="false" />
        <div class="mt-2 flex w-full flex-wrap items-center justify-between gap-2">
          <span class="text-xs" :class="inputSchemaError ? 'text-[#cf1322]' : 'text-[#389e0d]'">
            {{ inputSchemaError || "Schema 有效，顶层类型为 object" }}
          </span>
          <div class="flex items-center gap-2">
            <ElTag v-if="isRecorded('inputSchema')" size="small" type="success" effect="plain">已记录</ElTag>
            <ElButton size="small" @click="formatJsonField('inputSchema')">格式化</ElButton>
            <ElButton size="small" :disabled="!!inputSchemaError" @click="recordField('inputSchema')">记录变更</ElButton>
          </div>
        </div>
      </ElFormItem>
      <ElFormItem label="Output Schema（JSON Schema 2020-12）" required class="md:col-span-2">
        <ElInput v-model="draft.outputSchema" type="textarea" :rows="14" class="mono schema-input" spellcheck="false" />
        <div class="mt-2 flex w-full flex-wrap items-center justify-between gap-2">
          <span class="text-xs" :class="outputSchemaError ? 'text-[#cf1322]' : 'text-[#389e0d]'">
            {{ outputSchemaError || "Schema 有效，顶层类型为 object" }}
          </span>
          <div class="flex items-center gap-2">
            <ElTag v-if="isRecorded('outputSchema')" size="small" type="success" effect="plain">已记录</ElTag>
            <ElButton size="small" @click="formatJsonField('outputSchema')">格式化</ElButton>
            <ElButton size="small" :disabled="!!outputSchemaError" @click="recordField('outputSchema')">记录变更</ElButton>
          </div>
        </div>
      </ElFormItem>
    </ElForm>

    <div v-if="changeCount" class="border-t border-[#e8e8e8] pt-4">
      <div class="mb-2 text-sm font-semibold">本次待提交字段</div>
      <div class="flex flex-wrap gap-2">
        <ElTag v-for="field in recordedFields" :key="field" closable @close="removeRecorded(field)">{{ fieldLabels[field] }}</ElTag>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from "vue";
import { useRoute, useRouter } from "vue-router";
import { ElMessage } from "element-plus";
import {
  batchUpdateTools,
  listAllTools,
  MCP_READ_ONLY,
  MCP_STATUS_ENABLED,
  MCP_WRITE_OPERATION,
  type McpReadOnly,
  type McpTool,
  type McpToolUpdate,
} from "@/api/mcpTools";
import { compactJson, prettyJson, validateHttpUrl, validateObjectSchema, validateRequestConfig } from "@/utils/mcpToolValidation";

type EditableField =
  | "bizTag"
  | "url"
  | "requestConfig"
  | "name"
  | "title"
  | "description"
  | "inputSchema"
  | "outputSchema"
  | "readOnly";

const route = useRoute();
const router = useRouter();
const loading = ref(false);
const submitting = ref(false);
const original = ref<McpTool | null>(null);
const bizTagOptions = ref<string[]>([]);
const draft = reactive({
  bizTag: "",
  url: "",
  requestConfig: "",
  name: "",
  title: "",
  description: "",
  inputSchema: "",
  outputSchema: "",
  readOnly: MCP_READ_ONLY as McpReadOnly,
});
const changes = reactive<Partial<Record<EditableField, string | McpReadOnly>>>({});

const fieldLabels: Record<EditableField, string> = {
  bizTag: "业务标识",
  url: "请求 URL",
  requestConfig: "Request Config",
  name: "工具名",
  title: "工具标题",
  description: "工具描述",
  inputSchema: "Input Schema",
  outputSchema: "Output Schema",
  readOnly: "操作性质",
};
const readOnlyOptions = [
  { label: "只读", value: MCP_READ_ONLY },
  { label: "写操作", value: MCP_WRITE_OPERATION },
];

const recordedFields = computed(() => Object.keys(changes) as EditableField[]);
const changeCount = computed(() => recordedFields.value.length);
const urlError = computed(() => validateHttpUrl(draft.url));
const requestConfigError = computed(() => validateRequestConfig(draft.requestConfig));
const inputSchemaError = computed(() => validateObjectSchema(draft.inputSchema, "Input Schema"));
const outputSchemaError = computed(() => validateObjectSchema(draft.outputSchema, "Output Schema"));

function setDraft(tool: McpTool) {
  draft.bizTag = tool.bizTag;
  draft.url = tool.url;
  draft.requestConfig = prettyJson(tool.requestConfig, "{}");
  draft.name = tool.name;
  draft.title = tool.title;
  draft.description = tool.description;
  draft.inputSchema = prettyJson(tool.inputSchema);
  draft.outputSchema = prettyJson(tool.outputSchema);
  draft.readOnly = tool.readOnly;
}

function normalize(field: EditableField, value: string | McpReadOnly) {
  if (field === "readOnly") return value;
  if (field === "requestConfig" || field === "inputSchema" || field === "outputSchema") return compactJson(String(value));
  return String(value).trim();
}

function recordField(field: EditableField) {
  if (!original.value) return;
  const validationError = validateField(field);
  if (validationError) {
    ElMessage.warning(validationError);
    return;
  }
  const value = normalize(field, draft[field]);
  if (field !== "requestConfig" && field !== "bizTag" && typeof value === "string" && !value) {
    ElMessage.warning(`${fieldLabels[field]}不能为空`);
    return;
  }
  const originalValue = normalize(field, original.value[field]);
  if (value === originalValue) {
    delete changes[field];
    ElMessage.info(`${fieldLabels[field]}与原值一致`);
    return;
  }
  changes[field] = value;
  ElMessage.success(`已记录${fieldLabels[field]}变更`);
}

function isRecorded(field: EditableField) {
  return Object.prototype.hasOwnProperty.call(changes, field);
}

function removeRecorded(field: EditableField) {
  delete changes[field];
}

function validateField(field: EditableField) {
  if (field === "url") return validateHttpUrl(draft.url);
  if (field === "requestConfig") return validateRequestConfig(draft.requestConfig);
  if (field === "inputSchema") return validateObjectSchema(draft.inputSchema, "Input Schema");
  if (field === "outputSchema") return validateObjectSchema(draft.outputSchema, "Output Schema");
  return "";
}

function formatJsonField(field: "requestConfig" | "inputSchema" | "outputSchema") {
  try {
    draft[field] = JSON.stringify(JSON.parse(draft[field] || "{}"), null, 2);
  } catch {
    ElMessage.warning("当前内容不是合法 JSON");
  }
}

async function submitChanges() {
  if (!original.value || !changeCount.value) return;
  submitting.value = true;
  try {
    const payload = { id: original.value.id, ...changes } as McpToolUpdate;
    await batchUpdateTools([payload]);
    ElMessage.success("工具信息已更新");
    await router.push("/tools");
  } finally {
    submitting.value = false;
  }
}

onMounted(async () => {
  const id = Number(route.params.id);
  if (!Number.isInteger(id) || id <= 0) {
    ElMessage.error("工具 ID 无效");
    await router.replace("/tools");
    return;
  }
  loading.value = true;
  try {
    const result = await listAllTools({ enabledOnly: false });
    bizTagOptions.value = [...new Set(result.map((tool) => tool.bizTag).filter(Boolean))].sort();
    const tool = result.find((item) => item.id === id);
    if (!tool) {
      ElMessage.error("未找到该 MCP 工具");
      await router.replace("/tools");
      return;
    }
    original.value = tool;
    setDraft(tool);
  } finally {
    loading.value = false;
  }
});
</script>

<style scoped>
.schema-input :deep(textarea) {
  font-family: ui-monospace, "SF Mono", Menlo, Monaco, Consolas, monospace;
  font-size: 12px;
  line-height: 1.65;
}
</style>
