import axios, {
  type AxiosInstance,
  type AxiosRequestConfig,
  type InternalAxiosRequestConfig,
} from "axios";
import { ElMessage } from "element-plus";
import { adminAuth, markTokenInvalid } from "./adminAuth";

export interface ApiEnvelope<T = unknown> {
  errNo: number;
  errMsg: string;
  data: T;
}

/** 在 AxiosRequestConfig 上扩展业务字段。 */
declare module "axios" {
  export interface AxiosRequestConfig {
    /** true 时跳过默认的 ElMessage 错误提示，由调用方自行处理。 */
    silentError?: boolean;
  }
  export interface InternalAxiosRequestConfig {
    silentError?: boolean;
  }
}

export const ERR_OK = 0;
export const ERR_NO_ADMIN = 2001;

const apiBase = (import.meta.env.VITE_API_BASE as string | undefined)?.replace(/\/$/, "") || "";

/** 模块内部实例；对外仅暴露 post/get 两种请求方式。 */
const http: AxiosInstance = axios.create({
  baseURL: `${apiBase}/api`,
  timeout: 15000,
  headers: { "Content-Type": "application/json" },
});

http.interceptors.request.use((config: InternalAxiosRequestConfig) => {
  if (!config.method) config.method = "post";
  if (adminAuth.token) config.headers.set("X-Admin-Token", adminAuth.token);
  if (adminAuth.operator) config.headers.set("X-Admin-User", adminAuth.operator);
  return config;
});

http.interceptors.response.use(
  (resp) => {
    const body = resp.data as ApiEnvelope | undefined;
    if (!body || typeof body.errNo !== "number") {
      return resp.data;
    }
    if (body.errNo === ERR_OK) {
      return body.data;
    }
    if (body.errNo === ERR_NO_ADMIN) {
      if (!resp.config?.silentError) {
        ElMessage.error("管理令牌无效或未配置，请先在右上角设置管理令牌");
      }
      markTokenInvalid();
      return Promise.reject(body);
    }
    if (!resp.config?.silentError) {
      ElMessage.error(body.errMsg || `请求失败（${body.errNo}）`);
    }
    return Promise.reject(body);
  },
  (err) => {
    if (!err?.config?.silentError) {
      ElMessage.error(err?.message || "网络异常");
    }
    return Promise.reject(err);
  },
);

export function post<TResp = unknown, TBody = unknown>(
  path: string,
  body?: TBody,
  config?: AxiosRequestConfig,
): Promise<TResp> {
  return http.post(path, body ?? {}, config) as unknown as Promise<TResp>;
}
