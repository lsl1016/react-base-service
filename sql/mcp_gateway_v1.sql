-- ============================================================================
-- MCP 服务端网关（mcp_gateway_v1）：应用凭证 + 应用工具绑定 + 调用审计
--
-- 说明：
-- 1. 网关把 tblLlmTool 中 tool_type=http 且 status=1 的工具视为全局基础集合
--    （不按 caller 划分，caller_key 仅作归属标记），通过 MCP Streamable HTTP
--    协议对外暴露；工具本身不建新表。
-- 2. tblLlmMcpApp 是接入凭证：Bearer <app_key>:<app_secret>。应用可见的工具
--    由 tblLlmMcpAppTool 白名单决定——给 app 绑定基础集合的一个子集，用该 app
--    接入的 MCP 连接就只看得到这个子集；未绑定任何工具则 tools/list 为空。
-- 3. tblLlmMcpCallLog 是 tools/call 审计，异步批量落库，只插不改。
--
-- 存量库迁移（v1.3 之前按 caller 作用域授权的版本，手工执行一次）：
--   ALTER TABLE `tblLlmMcpApp` DROP INDEX `idx_caller`, DROP COLUMN `caller_key`;
--   （若不愿丢列也可仅执行：
--      ALTER TABLE `tblLlmMcpApp` MODIFY `caller_key` VARCHAR(32) NOT NULL DEFAULT '' COMMENT '(已废弃)原caller作用域';
--    不再读写。）
-- ============================================================================

USE `llm`;

-- MCP 网关应用凭证表
CREATE TABLE IF NOT EXISTS `tblLlmMcpApp` (
    `id`         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `app_id`     VARCHAR(64)  NOT NULL COMMENT '应用唯一标识(mcpapp_前缀)',
    `app_name`   VARCHAR(128) NOT NULL COMMENT '应用名称(展示用,唯一)',
    `app_key`    VARCHAR(64)  NOT NULL COMMENT '接入凭证key(Bearer用户名,全局唯一)',
    `app_secret` VARCHAR(128) NOT NULL COMMENT '接入凭证secret(仅创建/重置时完整展示)',
    `status`     TINYINT      NOT NULL DEFAULT 1 COMMENT '状态: 0=停用 1=启用',
    `created_by` VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '创建人',
    `updated_by` VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '更新人',
    `created_at` DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    `deleted_at` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '删除标记(0=未删除)',
    UNIQUE KEY `uk_app_id` (`app_id`),
    UNIQUE KEY `uk_app_name` (`app_name`),
    UNIQUE KEY `uk_app_key` (`app_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='MCP网关应用凭证表';

-- MCP 网关应用工具绑定表（显式白名单：应用未绑定任何工具时 tools/list 为空）
CREATE TABLE IF NOT EXISTS `tblLlmMcpAppTool` (
    `id`         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `app_id`     VARCHAR(64) NOT NULL COMMENT 'tblLlmMcpApp.app_id',
    `tool_id`    VARCHAR(64) NOT NULL COMMENT 'tblLlmTool.tool_id',
    `status`     TINYINT     NOT NULL DEFAULT 1 COMMENT '状态: 0=停用 1=绑定生效',
    `created_by` VARCHAR(64) NOT NULL DEFAULT '' COMMENT '操作人',
    `created_at` DATETIME    NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at` DATETIME    NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    `deleted_at` BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '删除标记(0=未删除)',
    UNIQUE KEY `uk_app_tool` (`app_id`, `tool_id`),
    INDEX `idx_tool` (`tool_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='MCP网关应用工具绑定表';

-- MCP 网关调用审计表（异步批量写入；response_text/arguments 有截断上限）
CREATE TABLE IF NOT EXISTS `tblLlmMcpCallLog` (
    `id`           BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `request_id`   VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '请求链路ID',
    `app_key`      VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '调用方应用key',
    `user_name`    VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '调用方用户标识(X-MCP-User,仅审计)',
    `mcp_method`   VARCHAR(32)  NOT NULL DEFAULT '' COMMENT 'MCP方法名(如tools/call)',
    `tool_name`    VARCHAR(128) NOT NULL DEFAULT '' COMMENT '工具名',
    `arguments`    TEXT         NULL COMMENT '入参快照(服务端补全后,≤4096字节)',
    `response_text` TEXT        NULL COMMENT '响应摘要(≤32KB)',
    `result_code`  INT          NOT NULL DEFAULT 0 COMMENT '结果码(0=成功,上游错误码或-1)',
    `error_msg`    VARCHAR(255) NOT NULL DEFAULT '' COMMENT '失败摘要',
    `cost_ms`      INT          NOT NULL DEFAULT 0 COMMENT '耗时毫秒',
    `client_ip`    VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '调用方IP',
    `client_info`  VARCHAR(255) NOT NULL DEFAULT '' COMMENT '客户端信息',
    `created_at`   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    INDEX `idx_app_created` (`app_key`, `created_at`),
    INDEX `idx_tool_created` (`tool_name`, `created_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='MCP网关调用审计表';
