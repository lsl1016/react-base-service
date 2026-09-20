import { post } from "./http";
import type {
  McpStatus,
  McpTool,
  McpToolListInput,
  McpToolMutation,
  McpToolSearchInput,
  McpToolUpdate,
  PageResult,
} from "./mcpTools.types";

export * from "./mcpTools.types";

export function listTools(input: McpToolListInput): Promise<PageResult<McpTool>> {
  return post("/manage/tools/list", input);
}

export function searchTools(input: McpToolSearchInput): Promise<PageResult<McpTool>> {
  return post("/manage/tools/search", input);
}

const MCP_TOOL_PAGE_SIZE = 100;

async function collectAllToolPages(fetchPage: (pageNo: number) => Promise<PageResult<McpTool>>): Promise<McpTool[]> {
  const firstPage = await fetchPage(1);
  const pageCount = Math.ceil(firstPage.total / firstPage.pageSize);
  if (pageCount <= 1) return firstPage.list;

  const remainingPages = await Promise.all(
    Array.from({ length: pageCount - 1 }, (_, index) => fetchPage(index + 2)),
  );
  return [firstPage, ...remainingPages].flatMap((page) => page.list);
}

export function listAllTools(
  input: Omit<McpToolListInput, "pageNo" | "pageSize">,
): Promise<McpTool[]> {
  return collectAllToolPages((pageNo) => listTools({ ...input, pageNo, pageSize: MCP_TOOL_PAGE_SIZE }));
}

export function searchAllTools(
  input: Omit<McpToolSearchInput, "pageNo" | "pageSize">,
): Promise<McpTool[]> {
  return collectAllToolPages((pageNo) => searchTools({ ...input, pageNo, pageSize: MCP_TOOL_PAGE_SIZE }));
}

export function batchCreateTools(tools: McpToolMutation[]): Promise<{ list: Array<{ id: number; name: string }> }> {
  return post("/manage/tools/batchCreate", { tools });
}

export function batchUpdateToolStatus(ids: number[], status: McpStatus): Promise<{ ids: number[]; status: McpStatus; count: number }> {
  return post("/manage/tools/batchUpdateStatus", { ids, status });
}

export function batchUpdateTools(tools: McpToolUpdate[]): Promise<{ tools: McpTool[] }> {
  return post("/manage/tools/batchUpdate", { tools });
}
