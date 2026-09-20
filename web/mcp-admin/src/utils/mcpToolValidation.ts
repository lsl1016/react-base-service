const REQUEST_CONFIG_FIELDS = new Set(["method", "timeout_ms", "headers"]);
const HTTP_METHODS = new Set(["GET", "POST"]);

function parseObjectJson(value: string, label: string): { value?: Record<string, unknown>; error?: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(value);
  } catch {
    return { error: `${label} 不是合法 JSON` };
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    return { error: `${label} 顶层必须是对象` };
  }
  return { value: parsed as Record<string, unknown> };
}

export function validateHttpUrl(value: string): string {
  try {
    const url = new URL(value.trim());
    return url.protocol === "http:" || url.protocol === "https:" ? "" : "请求 URL 仅支持 HTTP 或 HTTPS";
  } catch {
    return "请求 URL 必须是完整的 HTTP/HTTPS 地址";
  }
}

export function validateObjectSchema(value: string, label: string): string {
  const parsed = parseObjectJson(value, label);
  if (parsed.error) return parsed.error;
  return parsed.value?.type === "object" ? "" : `${label} 顶层 type 必须为 object`;
}

export function validateRequestConfig(value: string): string {
  if (!value.trim()) return "";
  const parsed = parseObjectJson(value, "Request Config");
  if (parsed.error || !parsed.value) return parsed.error || "Request Config 无效";

  const unknownField = Object.keys(parsed.value).find((field) => !REQUEST_CONFIG_FIELDS.has(field));
  if (unknownField) return `Request Config 不支持字段 ${unknownField}`;

  const { method, timeout_ms: timeout, headers } = parsed.value;
  if (method !== undefined && (typeof method !== "string" || !HTTP_METHODS.has(method))) {
    return "method 仅支持 GET、POST";
  }
  if (timeout !== undefined && (typeof timeout !== "number" || !Number.isInteger(timeout) || timeout < 100 || timeout > 120000)) {
    return "timeout_ms 必须是 100 到 120000 之间的整数";
  }
  if (headers !== undefined) {
    if (!headers || typeof headers !== "object" || Array.isArray(headers)) return "headers 必须是字符串键值对象";
    for (const [key, headerValue] of Object.entries(headers)) {
      if (typeof headerValue !== "string") return `Header ${key} 的值必须是字符串`;
      if (/[\r\n]/.test(headerValue)) return `Header ${key} 的值不能包含换行符`;
    }
  }
  return "";
}

export function compactJson(value: string): string {
  return value.trim() ? JSON.stringify(JSON.parse(value)) : "";
}

export function prettyJson(value: string, fallback = "{}"): string {
  try {
    return JSON.stringify(JSON.parse(value || fallback), null, 2);
  } catch {
    return value || "";
  }
}
