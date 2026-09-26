-- 模型配置面板增量脚本（存量环境）
-- 适用：数据库已经初始化过、不会再次执行 sql/init.sql 的环境。
-- 用途：tblLlmUserModel 扩展「模型配置面板」所需字段——自定义接入面(api_url)、
--       上下文容量(context_tokens)、最大输出(max_output_tokens)与能力开关
--       (support_thinking/support_tools/support_vision)。
-- 注意：MySQL 不支持 ADD COLUMN IF NOT EXISTS，列已存在时会报 Duplicate column，
--       可忽略该错误或先查 information_schema 再执行。
-- 执行前建议先备份数据库并在测试环境验证。

ALTER TABLE `tblLlmUserModel`
    ADD COLUMN `api_url`            VARCHAR(512) NOT NULL DEFAULT '' COMMENT '自定义接入面base url(空=走api.yaml全局端点)' AFTER `api_key`,
    ADD COLUMN `context_tokens`     INT          NOT NULL DEFAULT 0  COMMENT '上下文容量token(0=回退模型目录)' AFTER `api_url`,
    ADD COLUMN `max_output_tokens`  INT          NOT NULL DEFAULT 0  COMMENT '单次最大输出token(0=回退端点/目录默认)' AFTER `context_tokens`,
    ADD COLUMN `support_thinking`   TINYINT      NOT NULL DEFAULT 0  COMMENT '能力开关:支持思考模式' AFTER `max_output_tokens`,
    ADD COLUMN `support_tools`      TINYINT      NOT NULL DEFAULT 0  COMMENT '能力开关:支持函数调用' AFTER `support_thinking`,
    ADD COLUMN `support_vision`     TINYINT      NOT NULL DEFAULT 0  COMMENT '能力开关:支持视觉输入' AFTER `support_tools`;
