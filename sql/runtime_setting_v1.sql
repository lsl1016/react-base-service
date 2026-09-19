-- 运行时设置表（存量环境增量脚本）
-- 适用：数据库已经初始化过、不会再次执行 sql/init.sql 的环境。
-- 使用 CREATE TABLE IF NOT EXISTS，可重复执行。
-- 用途：管理面板「运行时配置」在线调整 subagent 委派策略（enabled/max_depth/
--       default_max_steps/max_parallel），未覆盖字段仍取 custom.yaml / 内置默认值。
-- 执行前建议先备份数据库并在测试环境验证。

-- ---------------------------------------------------------------------------
-- 运行时设置（setting_key 单行 JSON，当前仅 subagent）
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS `tblLlmRuntimeSetting` (
    `id`          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `setting_key` VARCHAR(64)  NOT NULL COMMENT '设置键(当前仅subagent,预留扩展)',
    `value_json`  MEDIUMTEXT   NOT NULL COMMENT '设置值JSON(可空字段=未覆盖,回落yaml)',
    `updated_by`  VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '最近更新人',
    `created_at`  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at`  DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE KEY `uk_setting_key` (`setting_key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='LLM运行时设置表(管理面板在线覆盖yaml策略)';
