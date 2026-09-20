<template>
  <div class="min-h-screen flex flex-col bg-[#f0f2f5] text-[rgba(0,0,0,0.85)]">
    <header
      class="flex h-14 shrink-0 items-center gap-7 border-b border-[#e8e8e8] bg-white px-6 shadow-[0_1px_4px_rgba(0,0,0,0.04)] z-20"
    >
      <div class="flex items-center gap-2 shrink-0">
        <span
          class="flex min-w-[34px] h-8 items-center justify-center rounded-md bg-gradient-to-br from-[#1890ff] to-[#096dd9] px-2 text-[11px] font-extrabold tracking-wide text-white"
          >MCP</span
        >
        <div class="leading-tight">
          <div class="text-[15px] font-bold text-[rgba(0,0,0,0.85)]">MCP Server 管理控制台</div>
        </div>
      </div>
      <div class="ml-auto flex items-center gap-3">
        <ElTag v-if="adminAuth.token" size="small" type="success" effect="plain">令牌已配置</ElTag>
        <ElTag v-else size="small" type="warning" effect="plain">令牌未配置</ElTag>
        <ElButton size="small" @click="openTokenDialog">管理令牌</ElButton>
      </div>
    </header>
    <div class="flex min-h-0 flex-1 flex-col md:flex-row">
      <aside class="flex w-full shrink-0 flex-col border-b border-[#e8e8e8] bg-white md:w-[236px] md:border-r md:border-b-0">
        <nav class="flex flex-1 flex-row gap-0.5 overflow-x-auto px-2 py-1 md:flex-col md:overflow-y-auto">
          <RouterLink
            v-for="item in adminNav"
            :key="item.path"
            :to="item.path"
            custom
            v-slot="{ href, navigate, isActive }"
          >
            <a
              :href="href"
              class="flex shrink-0 items-center gap-2 rounded px-3 py-2.5 text-[13px] no-underline transition-colors"
              :class="
                isActive
                  ? 'bg-[#e6f7ff] font-semibold text-[#1890ff]'
                  : 'text-[rgba(0,0,0,0.85)] hover:bg-[rgba(24,144,255,0.06)] hover:text-[#1890ff]'
              "
              @click="navigate"
            >
              <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-current opacity-40" />
              {{ item.label }}
            </a>
          </RouterLink>
        </nav>
      </aside>
      <main class="flex min-h-0 min-w-0 flex-1 flex-col p-3 pb-5 sm:p-4 md:p-6">
        <div
          class="flex min-h-[200px] flex-1 flex-col overflow-auto rounded border border-[#e8e8e8] bg-white p-4 shadow-sm sm:p-5"
        >
          <RouterView />
        </div>
      </main>
    </div>

    <ElDialog v-model="tokenDialogVisible" title="管理令牌设置" width="min(480px, 92vw)">
      <ElForm label-position="top">
        <ElFormItem label="管理令牌（X-Admin-Token）" required>
          <ElInput
            v-model="tokenDraft"
            type="password"
            show-password
            class="mono"
            placeholder="对应后端 conf/custom.yaml admin.tokens 中的任一令牌"
          />
          <div class="mt-1 text-xs leading-5 text-[rgba(0,0,0,0.45)]">
            令牌仅保存在浏览器 localStorage，随每个管理请求发送给 mcp-server。
          </div>
        </ElFormItem>
        <ElFormItem label="操作者（X-Admin-User，可选）">
          <ElInput v-model="operatorDraft" class="mono" placeholder="写入绑定与审计的操作人，缺省 admin" />
        </ElFormItem>
      </ElForm>
      <template #footer>
        <ElButton @click="tokenDialogVisible = false">取消</ElButton>
        <ElButton type="primary" @click="saveToken">保存</ElButton>
      </template>
    </ElDialog>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from "vue";
import { ElMessage } from "element-plus";
import { adminAuth, saveAdminAuth, tokenInvalid } from "@/api/adminAuth";

const adminNav = [
  { path: "/tools", label: "MCP 工具配置" },
  { path: "/permissions", label: "MCP 工具授权" },
];

const tokenDialogVisible = ref(false);
const tokenDraft = ref("");
const operatorDraft = ref("");

function openTokenDialog() {
  tokenDraft.value = adminAuth.token;
  operatorDraft.value = adminAuth.operator;
  tokenDialogVisible.value = true;
}

function saveToken() {
  if (!tokenDraft.value.trim()) {
    ElMessage.warning("请填写管理令牌");
    return;
  }
  saveAdminAuth(tokenDraft.value, operatorDraft.value);
  tokenDialogVisible.value = false;
  ElMessage.success("管理令牌已保存，请重新操作");
}

// 令牌缺失 / 失效时自动弹出设置对话框。
watch(
  tokenInvalid,
  (invalid) => {
    if (invalid && !tokenDialogVisible.value) openTokenDialog();
  },
  { immediate: true },
);
</script>
