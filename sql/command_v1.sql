-- 斜杠命令支持 v1：自定义命令表（docs/plan/20261013_斜杠命令支持设计方案.md）
-- 存量环境手工执行；新环境由 init.sql 建表。

CREATE TABLE IF NOT EXISTS `tblLlmCommand` (
    `id`            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `command_key`   VARCHAR(64)  NOT NULL COMMENT '命令唯一标识(cmd_+UUID)',
    `name`          VARCHAR(64)  NOT NULL COMMENT '命令名(小写字母/数字/中划线/下划线,保留名校验)',
    `description`   VARCHAR(255) NOT NULL DEFAULT '' COMMENT '一行描述(缺省取正文首行)',
    `argument_hint` VARCHAR(128) NOT NULL DEFAULT '' COMMENT '参数提示(前端菜单展示)',
    `content`       MEDIUMTEXT   NOT NULL COMMENT '提示词模板正文(markdown,支持$ARGUMENTS/$1 $2)',
    `app_key`       VARCHAR(32)  NOT NULL COMMENT '所属app(default伪app全局)',
    `route_values`  TEXT         NULL COMMENT '路由路径(JSON数组,前缀IN匹配)',
    `status`        TINYINT      NOT NULL DEFAULT 1 COMMENT '状态: 0=禁用 1=启用',
    `created_by`    VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '创建人',
    `updated_by`    VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '更新人',
    `created_at`    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at`    DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    `deleted_at`    BIGINT UNSIGNED NOT NULL DEFAULT 0 COMMENT '删除标记(0=未删除)',
    UNIQUE KEY `uk_command_key` (`command_key`),
    INDEX `idx_app_route` (`app_key`, `route_values`(255)),
    INDEX `idx_app_status` (`app_key`, `status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='LLM自定义斜杠命令表';
