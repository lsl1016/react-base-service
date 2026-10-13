-- app 默认模型迁移脚本（v1，2026-10）
-- 适用：已按旧版 init.sql / app_rename_v1.sql 初始化过的存量环境；
--       全新环境直接执行最新 sql/init.sql，无需本脚本。
-- 内容：
--   tblLlmApp 新增 default_model_hash（app 默认模型）：
--   无 ModelHash 的 run 与 workflow 凭证解析第一优先——厂商（连接）与模型一次选定，
--   解决同一 app 配多个厂商连接后「模型与接入点错配」的问题；
--   解析顺序：app 默认模型 → LLM 连接（app+路由前缀）→ 旧 app 级 API Key。
-- 语义说明：
--   * 空 = 未配置（保持现有连接解析兜底，行为不变）；
--   * 指向的用户模型被删除、或其引用连接被停用/删除时，运行时自动回退连接解析，
--     不阻断 run（配置期 /app/register、/app/update 会校验可解析性并拒绝写入）。
-- 配置入口：管理面板「App 管理」编辑弹窗的「默认模型」下拉（按连接分组）。
-- 执行前建议先备份数据库。

ALTER TABLE `tblLlmApp`
    ADD COLUMN `default_model_hash` VARCHAR(128) NOT NULL DEFAULT ''
        COMMENT 'app默认模型(tblLlmUserModel.model_hash): 无ModelHash的run/workflow凭证解析第一优先,空=未配置走连接解析兜底'
        AFTER `bind_default_capabilities`;

-- 验证建议：
--   SELECT app_key, default_model_hash FROM tblLlmApp WHERE default_model_hash <> '';
--   （应只包含配置过的 app；对应 model_hash 须存在于 tblLlmUserModel）
