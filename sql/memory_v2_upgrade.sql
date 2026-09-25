-- ---------------------------------------------------------------------------
-- 长期记忆 V2 Phase 1 增量脚本：tblLlmMemoryItem 类型化字段（memory_type/confidence/importance）
-- 适用：已按旧版 init.sql 建过 tblLlmMemoryItem 的存量环境；新环境直接跑 init.sql 无需本脚本。
-- 幂等性：ALTER 不幂等，重复执行会报 Duplicate column；可先查 information_schema 确认后再执行。
-- 设计文档：docs/plan/长期记忆V2实施计划-Phase1-2.md §4.1
-- ---------------------------------------------------------------------------

ALTER TABLE `tblLlmMemoryItem`
    ADD COLUMN `memory_type` VARCHAR(16) NOT NULL DEFAULT 'fact'
        COMMENT '记忆类型: preference=用户偏好(优先注入)/fact=事实/event=事件/procedure=经验方法' AFTER `layer`,
    ADD COLUMN `confidence` DECIMAL(4,3) NOT NULL DEFAULT 0.800
        COMMENT '可信度0-1: 模型/管理面写入默认0.80, extractor按抽取置信度写入' AFTER `memory_type`,
    ADD COLUMN `importance` TINYINT UNSIGNED NOT NULL DEFAULT 3
        COMMENT '重要程度1-5: 注入排序用(高优先)' AFTER `confidence`,
    ADD INDEX `idx_owner_mtype_state` (`owner_type`, `owner_key`, `memory_type`, `state`);

-- 存量数据回填：三列均带 NOT NULL DEFAULT，ALTER 后旧行自动为 fact/0.800/3，无需 UPDATE。
-- 注意：item_key 幂等指纹不参与类型计算，逻辑唯一键 uk_owner_item 保持不变。
