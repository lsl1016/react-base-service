---
title: Attachment 模块功能文档
date: 2026-09-19
version: v1.0
type: system
module: attachment
maintainer: react-base-service 项目组
status: active
related_code:
  - router/http.go
  - controllers/http/attachment/file_upload.go
  - service/skillchatfile/upload.go
  - service/skillchatfile/meta.go
  - service/skillchatfile/encoding.go
  - models/llm/chat_file_record.go
  - components/params/chat_file.go
  - service/react/attachments.go
summary: ReAct 对话附件上传：csv/md/txt 文本统一转 UTF-8 存 COS 并双写元数据，fileId 供 run payload 的 attachments 引用后按需读取分析
---

# Attachment 模块功能文档

## 1. 模块概述

Attachment 模块提供 ReAct 对话附件的上传入口。上传仅接受 `csv`/`md`/`txt` 纯文本文件（单文件上限 50MB），检测字符集并统一转为 UTF-8 后存入 COS，元数据同时写入 Redis 与 MySQL；接口返回 `fileId`，由前端放入 ReAct run payload 的 `attachments` 引用。

Run 启动时只校验附件并注入元信息清单，文件内容不进上下文；模型按任务类型经 `read_attachment`、`inspect_attachment` 或 `python_exec` 按 `fileId` 按需读取。

## 2. 接口清单

| 路径 | 方法 | 功能 | 控制器 |
|---|---|---|---|
| `/react-base-service/api/chat/files/upload` | POST | 上传对话文本附件，返回 `fileId` | `attachment.UploadChatFile` |

请求为 `multipart/form-data`，文件字段名 `file`；操作人取 IPS 登录用户名，为空、`unknown` 或 `system` 时拒绝。响应 `ChatFileUploadResp` 只含 `fileId`/`fileName`/`ext`/`size`/`uploadedAt`，不返回 COS 路径。

## 3. 核心逻辑

### 3.1 上传与编码归一化

`skillchatfile.SaveChatUploadFile` 的处理顺序：

1. 校验扩展名属于 `txt`/`md`/`csv`（`IsSupportedChatFileExtension`）；校验大小不超过 `ChatFileUploadMaxBytes`（50MB），并用 `LimitReader` 读取后二次校验实际字节数。
2. `DetectChatFileCharset` 判定字符集，判定链：UTF-16 BOM → 含 `0x00` 字节判非纯文本拒绝 → `utf8.Valid` → GB18030 兜底。Big5/Shift-JIS 等若恰好能被 GB18030 干净解码，会被判为 `gb18030`（既有识别限制）。
3. `DecodeChatFileToUTF8` 统一转 UTF-8 并剥离 BOM 后再上传 COS：COS 上的文本附件恒为 UTF-8，下游读取无需再处理编码。转码失败返回「无法识别文件编码」类参数错误。
4. 上传 COS 并双写元数据（见 3.2），MySQL 记录 `status` 写为 `parsed`。

`fileId` 格式为 `file_` + 去横线 uuid；文件名经 `sanitizeFileName` 取 basename 并把 `/`、`\` 替换为 `_`。

### 3.2 存储位置与元数据双写

- COS 对象键：`{cos.pathPrefix（未配置兜底 react-base/llm）}/chat-files/{yyyy-MM-dd}/{fileId}/{清洗后文件名}`；`buildChatFileCOSPrefix` 在 pathPrefix 末尾自动补 `/chat-files`（已以该后缀结尾时不重复追加）。ContentType 按扩展名映射为 `text/csv`/`text/markdown`/`text/plain` 并附 charset。对象 URI 记录为 `cos://{bucket}/{key}`，桶为 `resource.yaml` 的 `cos.bucket`。
- Redis 元数据：`ChatFileMeta` 整体 JSON 写入键 `llm:chat:file:meta:{fileId}`，TTL `MetaTTLSeconds`（24 小时）。该 Redis 副本当前没有运行期读取方（`GetMeta` 无调用者），ReAct 消费链路一律查 MySQL 记录。
- MySQL 记录：`model.CreateChatFileRecord` 写 `tblLlmChatFileRecord`，含 `cos_key`/`cos_uri`/`charset`/`size`/`owner`。

`ChatFileStatusDeleted`、`ChatFileStatusExpired` 常量已定义但当前没有任何代码写入这两个状态；不存在过期清理任务，COS 对象与 MySQL 记录无 TTL 清理路径，运行期仅靠 `status` 字段恒为 `parsed` 放行。

### 3.3 ReAct Run 中的消费

Run 启动（`service/react/runtime.go`）调用 `prepareReactAttachments` 校验 payload 的 `attachments`（`params.ReactAttachmentRef`：`fileId`/`fileName`/`description`）并产出元信息快照：

- 数量上限 `reactAttachmentMaxCount` = 5；`fileId` 去重，重复即拒绝。
- 每条经 `resolveAttachmentRecord` 按 MySQL 记录校验：记录存在、`owner` 与当前用户一致（不一致报用户名不匹配）、`status` 必须为 `parsed`、扩展名在白名单内、大小不超过 50MB。
- 单轮累计大小上限 `reactAttachmentMaxTotalBytes` = 50MB（与单文件上传上限同值）。

校验通过后 `renderAttachmentManifest` 把附件元信息（fileId、文件名、类型、大小、描述）渲染为 `<attachments>` 块追加进用户消息，只给清单和使用方式指引，不展开内容。模型侧三条消费路径共用 `resolveAttachmentRecord` 做归属与状态校验：

| 路径 | 行为 |
|---|---|
| `read_attachment` | 下载 COS 对象并按记录 charset 解码为 UTF-8 文本，结果走标准截断管线，大内容自动 preview + `resultRef` 翻页 |
| `inspect_attachment` | 返回头部原始预览：前 30 行（且受 32K 字符预算约束，含行内截断）、总行数、大小；不做任何解析 |
| `python_exec` | inputs 中 `{type: "attachment", fileId}` 由 `resolvePythonExecAttachmentInput` 解析为 `cos://{bucket}/{key}` 的 `cos_ref` 输入交沙箱读取，后端只做 fileId 到 COS 路径的解析与归属校验、不下载内容，charset 作为解码提示随行 |

## 4. 数据模型

`tblLlmChatFileRecord`（`model.ChatFileRecord`）：`file_id`（唯一索引）、`owner`（上传人用户名，运行期归属校验依据）、`file_name`、`ext`、`mime_type`、`charset`、`size`、`cos_key`、`cos_uri`、`status`（`parsed`/`deleted`/`expired`，当前仅 `parsed` 会被写入和放行）、`created_at`/`updated_at`。无软删字段。

Redis 侧 `ChatFileMeta`（`service/skillchatfile/meta.go`）字段与 MySQL 记录基本一致，另含 `uploadedAt`，24 小时自动过期。

## 5. 配置项与限制

| 项 | 值 / 来源 | 说明 |
|---|---|---|
| 上传大小上限 | 50MB（`ChatFileUploadMaxBytes`） | 超限拒绝 |
| 允许扩展名 | `txt`/`md`/`csv` | 其他扩展名拒绝 |
| 支持字符集 | UTF-8、带 BOM 的 UTF-16、GB18030 | 统一转 UTF-8 落 COS |
| 单轮附件数量 | 5（`reactAttachmentMaxCount`） | 超限拒绝 Run |
| 单轮累计大小 | 50MB（`reactAttachmentMaxTotalBytes`） | 超限拒绝 Run |
| COS 配置 | `resource.yaml` 的 `cos.*`（`bucket`、`pathPrefix` 等） | `pathPrefix` 未配置兜底 `react-base/llm` |
| Redis 元数据 TTL | 24 小时 | 过期后仅影响 Redis 副本，MySQL 记录不受影响 |

`ChatFileMaxBytes`（200KB）只约束「把内容读进模型上下文」的路径，不是上传上限；大文件不走上下文注入，仅按 `fileId` 引用交给 python 分析。

## 6. 历史版本

| 版本 | 日期 | 修改人 | 变更说明 |
|---|---|---|---|
| v1.0 | 2026-09-19 | react-base-service 项目组 | 初始版本 |
