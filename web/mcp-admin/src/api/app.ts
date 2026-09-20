import { post } from "./http";
import type { McpStatus, McpReadOnly } from "./mcpTools.types";

export interface McpApp {
  id: number;
  appName: string;
  appKey: string;
  appSecret: string;
  description: string;
  status: number;
  owner: string;
  toolCount: number;
}

export interface McpAppCreateInput {
  appName: string;
  owner: string;
  description?: string;
}

export interface McpAppToolBinding {
  toolId: number;
  name: string;
  title: string;
  status: McpStatus;
  readOnly: McpReadOnly;
}

export function listApps(keyword?: string): Promise<{ list: McpApp[]; total: number }> {
  return post("/manage/app/list", { keyword: keyword || "", pageNo: 1, pageSize: 100 });
}

export function createApp(input: McpAppCreateInput): Promise<{ app: McpApp }> {
  return post("/manage/app/create", input);
}

export function grantAppTools(appId: number, toolIds: number[]): Promise<{ appId: number; toolIds: number[]; count: number }> {
  return post("/manage/app/grantTools", { appId, toolIds });
}

export function updateAppToolStatus(
  appId: number,
  toolIds: number[],
  status: McpStatus,
): Promise<{ appId: number; toolIds: number[]; status: McpStatus }> {
  return post("/manage/app/updateToolStatus", { appId, toolIds, status });
}

export function listAppTools(appId: number): Promise<{ appId: number; list: McpAppToolBinding[] }> {
  return post("/manage/app/listTools", { appId });
}
