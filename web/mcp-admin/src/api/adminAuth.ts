import { reactive, ref } from "vue";

const TOKEN_KEY = "mcp_web_admin_token";
const OPERATOR_KEY = "mcp_web_admin_operator";

function readStored(key: string): string {
  try {
    return localStorage.getItem(key) || "";
  } catch {
    return "";
  }
}

/** 令牌校验失败时置 true，布局监听后弹出设置对话框。 */
export const tokenInvalid = ref(false);

export const adminAuth = reactive({
  /** 管理端令牌，对应后端 custom.yaml admin.tokens。 */
  token: readStored(TOKEN_KEY),
  /** 可选操作者标识，写入绑定与审计的操作人字段。 */
  operator: readStored(OPERATOR_KEY),
});

export function saveAdminAuth(token: string, operator: string) {
  adminAuth.token = token.trim();
  adminAuth.operator = operator.trim();
  try {
    localStorage.setItem(TOKEN_KEY, adminAuth.token);
    localStorage.setItem(OPERATOR_KEY, adminAuth.operator);
  } catch {
    // 隐私模式等场景下 localStorage 不可用，仅保留内存态。
  }
  tokenInvalid.value = false;
}

export function markTokenInvalid() {
  tokenInvalid.value = true;
}
