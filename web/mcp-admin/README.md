# MCP 网关管理台（web/mcp-admin）

MCP 服务端网关的管理控制台，Vue 3 + TypeScript + Vite + Element Plus + Tailwind CSS。
**源码内嵌本仓库**（自 mcp-server 项目复制而来，此后在本仓独立演进，不依赖外部工程），
构建产物 `dist/` 经 `web/embed.go` 的 `go:embed all:mcp-admin/dist` 内嵌进二进制，
页面挂在 `/react-base-service/react/mcp-admin`，接口走 `/api/manage/*` 协议兼容层
（`controllers/http/mcpadmin`）。

## 页面

| 路由 | 视图 | 功能 |
|---|---|---|
| 工具列表 | `ToolListView` | http 工具检索 / 批量上下线（工具是全局基础集合，bizTag 仅作归属标记） |
| 新增工具 | `ToolCreateView` | 多草稿批量注册（工具名全局唯一） |
| 编辑工具 | `ToolEditView` | 改 URL / method / schema / 描述 |
| 工具授权 | `ToolPermissionsView` | 应用凭证管理 + **按应用勾选绑定工具白名单**（`grantTools` 持久化到 `tblLlmMcpAppTool`） |

## 开发与构建

```bash
cd web/mcp-admin
npm install
npm run dev     # 开发服务器 :5173，/api 代理到 VITE_DEV_PROXY_TARGET（默认 127.0.0.1:8080）
npm run build   # vue-tsc 类型检查 + vite build，产出 dist/（提交入库，供 go:embed）
```

改完前端务必 `npm run build` 并把新的 `dist/` 一并提交——Go 侧只认嵌入的构建产物；
`node_modules/` 已被仓库 .gitignore 忽略。

## 与后端的契约

- 鉴权：`X-Admin-Token`（白名单见 `mcp_server.admin_tokens`），`X-Admin-User` 记录操作者；
  无效令牌返回 errNo 2001，前端弹「管理令牌」对话框。
- 工具可见性模型：工具 = 全局基础集合（全部启用 http 工具）；应用可见工具 =
  `tblLlmMcpAppTool` 白名单子集。`app/listTools` 返回应用当前绑定清单（status 2=已绑定
  且工具上线 / 1=已绑定但工具下线），`app/grantTools` 全量替换绑定（空清单=清空）。
