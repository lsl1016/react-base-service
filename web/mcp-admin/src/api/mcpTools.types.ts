export const MCP_STATUS_DISABLED = 1;
export const MCP_STATUS_ENABLED = 2;
export const MCP_PERMISSION_UNBOUND = 0;
export const MCP_PERMISSION_DISABLED = 1;
export const MCP_PERMISSION_ENABLED = 2;
export const MCP_WRITE_OPERATION = 1;
export const MCP_READ_ONLY = 2;

export type McpStatus = typeof MCP_STATUS_DISABLED | typeof MCP_STATUS_ENABLED;
export type McpPermissionStatus =
  | typeof MCP_PERMISSION_UNBOUND
  | typeof MCP_PERMISSION_DISABLED
  | typeof MCP_PERMISSION_ENABLED;
export type McpReadOnly = typeof MCP_WRITE_OPERATION | typeof MCP_READ_ONLY;

export interface McpTool {
  id: number;
  bizTag: string;
  url: string;
  requestConfig: string;
  name: string;
  title: string;
  description: string;
  inputSchema: string;
  outputSchema: string;
  readOnly: McpReadOnly;
  status: McpStatus;
  owner: string;
}

export interface McpToolMutation {
  bizTag: string;
  url: string;
  requestConfig?: string;
  name: string;
  title: string;
  description: string;
  inputSchema: string;
  outputSchema: string;
  readOnly: McpReadOnly;
  status: McpStatus;
  owner: string;
}

export type McpToolUpdate = Partial<Omit<McpToolMutation, "status" | "owner">> & { id: number };

export interface McpToolListInput {
  enabledOnly: boolean;
  bizTag?: string;
  pageNo?: number;
  pageSize?: number;
}

export interface McpToolSearchInput {
  keyword: string;
  enabledOnly: boolean;
  pageNo?: number;
  pageSize?: number;
}

export interface PageResult<T> {
  list: T[];
  total: number;
  pageNo: number;
  pageSize: number;
}
