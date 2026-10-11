-- caller → app 改名迁移脚本（v1，2026-10）
-- 适用：数据库已按旧版 init.sql（caller 命名时代）初始化过的存量环境；
--       全新环境直接执行最新 sql/init.sql，无需本脚本。
-- 内容：
--   1) 表/列/索引改名：tblLlmCaller→tblLlmApp、caller_key→app_key（数据原样保留）；
--   2) tblLlmApp 新增 bind_default_capabilities（默认能力池开关，存量 app 默认绑定）；
--   3) tblLlmRuntimeSetting 新增 app_key 维度（app 级运行时配置覆盖）；
--   4) 记忆 owner_type 数据迁移：caller→app、caller_user→app_user（owner_key 值不变）；
--   5) 默认能力池：存量工具/Skill 全部迁入 default 伪 app 作用域（对绑定默认能力的
--      全部 app 可见；未绑定默认能力的 app 只解析自身资源）。
-- 语义说明：
--   * 会话/运行/消息/产物等历史行的归属值不变（callerKey 字符串即 appKey），
--     会话五元组校验、回放、MCP 连接绑定关系全部保持；
--   * MCP 同步工具 tool_id 中的 `__<appKey>` 后缀为不透明 ID，存量格式继续有效；
--   * 工具/Skill 迁入 default 后，原属主 app（默认绑定默认能力）依旧可见，
--     访问范围只增不减；如需恢复某 app 独占，请把该工具/Skill 改回其 app_key。
-- 执行前建议先备份数据库并在测试环境验证。整体在一个低峰窗口执行。

-- ---------------------------------------------------------------------------
-- 1) 表改名
-- ---------------------------------------------------------------------------
RENAME TABLE `tblLlmCaller` TO `tblLlmApp`;
RENAME TABLE `tblLlmMcpServerCaller` TO `tblLlmMcpServerApp`;

-- ---------------------------------------------------------------------------
-- 2) caller_key → app_key 列改名（数据保留）+ caller 命名索引改名
--    MySQL 8.0+ 支持 RENAME INDEX；列上的索引随 CHANGE 自动指向新列名。
-- ---------------------------------------------------------------------------
ALTER TABLE `tblLlmApp`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT 'app唯一标识',
    RENAME INDEX `uk_caller_key` TO `uk_app_key`;

ALTER TABLE `tblLlmSystemPrompt`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '所属app',
    RENAME INDEX `idx_caller_route` TO `idx_app_route`;

ALTER TABLE `tblLlmTool`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '所属app',
    RENAME INDEX `idx_caller_route` TO `idx_app_route`;

ALTER TABLE `tblLlmSkill`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '所属app',
    RENAME INDEX `idx_caller_route` TO `idx_app_route`,
    RENAME INDEX `idx_caller_status` TO `idx_app_status`;

ALTER TABLE `tblLlmAgent`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '所属app(default=全部app可用)',
    RENAME INDEX `uk_caller_agent` TO `uk_app_agent`,
    RENAME INDEX `idx_caller_route` TO `idx_app_route`;

ALTER TABLE `tblLlmMcpServer`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '所属app(属主)',
    RENAME INDEX `idx_caller` TO `idx_app`;

ALTER TABLE `tblLlmMcpServerApp`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '绑定的app(工具同步到该app名下)',
    RENAME INDEX `uk_server_caller` TO `uk_server_app`,
    RENAME INDEX `idx_caller` TO `idx_app`;

ALTER TABLE `tblLlmApiKey`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '所属app',
    RENAME INDEX `idx_caller_route` TO `idx_app_route`;

ALTER TABLE `tblLlmConnection`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL DEFAULT '' COMMENT '所属app(空=不限)',
    RENAME INDEX `idx_caller_route` TO `idx_app_route`;

ALTER TABLE `tblLlmReactSession`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '应用标识',
    RENAME INDEX `idx_caller_route_user_state` TO `idx_app_route_user_state`;

ALTER TABLE `tblLlmReactRun`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '应用标识';

ALTER TABLE `tblLlmReactMessage`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '应用标识';

ALTER TABLE `tblLlmReactArtifact`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(128) NOT NULL DEFAULT '' COMMENT '应用标识';

ALTER TABLE `tblLlmPlanExecution`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(32) NOT NULL COMMENT '应用标识';

ALTER TABLE `tblLlmWorkflow`
    CHANGE COLUMN `caller_key` `app_key` VARCHAR(64) NOT NULL COMMENT '触发的run用的app(解析提示词/工具/Skill/ApiKey)';

-- ---------------------------------------------------------------------------
-- 3) tblLlmApp 默认能力池开关（存量 app 默认绑定 = 保持现有可见性）
-- ---------------------------------------------------------------------------
ALTER TABLE `tblLlmApp`
    ADD COLUMN `bind_default_capabilities` TINYINT NOT NULL DEFAULT 1
        COMMENT '是否绑定default伪app的默认能力池(工具/Skill): 1=绑定(存量行为) 0=只解析自身资源'
        AFTER `allow_plan`;

-- ---------------------------------------------------------------------------
-- 4) tblLlmRuntimeSetting 增加 app 维度（空=全局行；非空=该 app 专属覆盖）
--    存量行 app_key 落默认 ''，语义不变（全局覆盖）。
-- ---------------------------------------------------------------------------
ALTER TABLE `tblLlmRuntimeSetting`
    ADD COLUMN `app_key` VARCHAR(32) NOT NULL DEFAULT ''
        COMMENT '应用维度(空=全局行;非空=该app专属覆盖,优先于全局)'
        AFTER `setting_key`,
    DROP INDEX `uk_setting_key`,
    ADD UNIQUE KEY `uk_setting_app` (`setting_key`, `app_key`);

-- ---------------------------------------------------------------------------
-- 5) 记忆归属维度数据迁移（owner_key 值不变：appKey 字符串与原 callerKey 相同）
-- ---------------------------------------------------------------------------
UPDATE `tblLlmMemoryItem` SET `owner_type` = 'app' WHERE `owner_type` = 'caller';
UPDATE `tblLlmMemoryItem` SET `owner_type` = 'app_user' WHERE `owner_type` = 'caller_user';

-- ---------------------------------------------------------------------------
-- 6) 默认能力池：存量工具/Skill 全部迁入 default 作用域
--    （对绑定默认能力的全部 app 生效；app_key='default' 是保留的公共作用域）
-- ---------------------------------------------------------------------------
UPDATE `tblLlmTool` SET `app_key` = 'default';
UPDATE `tblLlmSkill` SET `app_key` = 'default';

-- 验证建议：
--   SELECT app_key, COUNT(*) FROM tblLlmTool GROUP BY app_key;   -- 应只剩 default（及迁移后新注册的）
--   SELECT app_key, COUNT(*) FROM tblLlmSkill GROUP BY app_key;
--   SELECT owner_type, COUNT(*) FROM tblLlmMemoryItem GROUP BY owner_type;  -- 应只剩 app/app_user
--   SELECT setting_key, app_key, COUNT(*) FROM tblLlmRuntimeSetting GROUP BY setting_key, app_key;
