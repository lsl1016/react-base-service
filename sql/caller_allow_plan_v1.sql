-- Caller 级 allow_plan 开关（存量环境增量脚本）
-- 适用：数据库已初始化过、不会再次执行 sql/init.sql 的环境。
-- 语义：NULL=跟随全局 custom.yaml llm.react.allow_plan（默认开）；0=caller 级强制关
--       （create_plan 工具不可见 + executionMode=plan 入口拒绝）；1=caller 级强制开。
-- 注意：MySQL 8.0 不支持 ADD COLUMN IF NOT EXISTS，重复执行本脚本会报列已存在，
--       报错可忽略；执行前建议先备份并在测试环境验证。

ALTER TABLE `tblLlmCaller`
    ADD COLUMN `allow_plan` TINYINT NULL DEFAULT NULL
    COMMENT 'create_plan/Plan模式能力: NULL=跟随全局llm.react.allow_plan 0=caller级强制关 1=caller级强制开'
    AFTER `platform`;

-- 常用示例：QQ 机器人等纯文本端 caller 关闭 Plan 能力
-- UPDATE `tblLlmCaller` SET `allow_plan` = 0 WHERE `caller_key` = 'qq-bot';
