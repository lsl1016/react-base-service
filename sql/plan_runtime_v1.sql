-- Plan Runtime V1 存量环境增量脚本
-- 适用：数据库已经初始化过、不会再次执行 sql/init.sql 的环境。
-- 六张表均使用 CREATE TABLE IF NOT EXISTS，可重复执行。
-- 执行前建议先备份数据库并在测试环境验证。

-- ---------------------------------------------------------------------------
-- 五、Plan Runtime V1（Execution / Version / Step / Attempt / Result / Wait）
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS `tblLlmPlanExecution` (
    `id`                 BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `plan_execution_id`  VARCHAR(64)  NOT NULL COMMENT 'Plan执行实例ID(plan_exec_前缀)',
    `session_id`         VARCHAR(64)  NOT NULL COMMENT '所属React会话ID',
    `outer_run_id`       VARCHAR(64)  NOT NULL COMMENT '发起Plan的外层Run ID',
    `caller_key`         VARCHAR(32)  NOT NULL COMMENT '调用方标识',
    `user_name`          VARCHAR(64)  NOT NULL COMMENT '发起用户',
    `status`             VARCHAR(32)  NOT NULL COMMENT 'CREATED/PLANNING/RUNNING/WAIT_*/SUCCEEDED/FAILED/CANCELLED',
    `current_version_id` VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '当前生效Plan版本ID',
    `current_step_id`    VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '当前Step ID',
    `summary`            TEXT         COMMENT 'Plan标题/公开摘要',
    `execution_mode`     VARCHAR(16)  NOT NULL DEFAULT 'plan' COMMENT '执行模式，当前固定plan',
    `result_json`        MEDIUMTEXT   COMMENT 'Plan最终结构化结果JSON',
    `error_code`         VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '失败错误码',
    `error_summary`      TEXT         COMMENT '失败摘要',
    `finished_at`        DATETIME     DEFAULT NULL COMMENT '终态时间',
    `created_at`         DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at`         DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE KEY `uk_plan_execution_id` (`plan_execution_id`),
    KEY `idx_plan_session_created` (`session_id`, `created_at`),
    KEY `idx_plan_outer_run` (`outer_run_id`),
    KEY `idx_plan_status` (`status`, `updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Plan执行实例表';

CREATE TABLE IF NOT EXISTS `tblLlmPlanVersion` (
    `id`                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `plan_version_id`   VARCHAR(64)  NOT NULL COMMENT 'Plan版本ID(plan_ver_前缀)',
    `plan_execution_id` VARCHAR(64)  NOT NULL COMMENT '所属Plan执行实例ID',
    `version_no`        INT          NOT NULL COMMENT '版本号，从1递增',
    `title`             VARCHAR(255) NOT NULL DEFAULT '' COMMENT '计划标题',
    `overview`          TEXT         COMMENT '计划总览',
    `reason`            VARCHAR(128) NOT NULL DEFAULT 'initial' COMMENT '版本原因: initial/replan_*',
    `created_by`        VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '创建来源/runId/用户',
    `created_at`        DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    UNIQUE KEY `uk_plan_version_id` (`plan_version_id`),
    UNIQUE KEY `uk_plan_version_no` (`plan_execution_id`, `version_no`),
    KEY `idx_plan_version_execution` (`plan_execution_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Plan版本表';

CREATE TABLE IF NOT EXISTS `tblLlmPlanStep` (
    `id`                    BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `step_id`               VARCHAR(64)  NOT NULL COMMENT 'Step ID(plan_step_前缀)',
    `plan_execution_id`     VARCHAR(64)  NOT NULL COMMENT '所属Plan执行实例ID',
    `plan_version_id`       VARCHAR(64)  NOT NULL COMMENT '所属Plan版本ID',
    `step_order`            INT          NOT NULL COMMENT '线性执行顺序，从1开始',
    `step_key`              VARCHAR(128) NOT NULL COMMENT 'Planner生成的稳定语义键',
    `step_name`             VARCHAR(255) NOT NULL COMMENT '步骤名称',
    `instruction`           TEXT         COMMENT '当前Step给执行器的完整指令',
    `expected_output`       TEXT         COMMENT '预期产出说明',
    `success_criteria_json` TEXT         COMMENT '成功标准JSON数组',
    `status`                VARCHAR(32)  NOT NULL COMMENT 'PENDING/RUNNING/WAITING/SUCCEEDED/FAILED/CANCELLED/SKIPPED',
    `step_type`             VARCHAR(32)  NOT NULL COMMENT 'AGENT/USER_INPUT/USER_ACTION',
    `required`              TINYINT      NOT NULL DEFAULT 1 COMMENT '是否必需步骤',
    `max_attempts`          INT          NOT NULL DEFAULT 1 COMMENT '最大Attempt次数',
    `timeout_seconds`       INT          NOT NULL DEFAULT 0 COMMENT '单Attempt超时，0使用运行时默认',
    `depends_on_json`       TEXT         COMMENT '依赖step_key JSON数组；V1仅允许依赖前序步骤',
    `input_json`            MEDIUMTEXT   COMMENT '步骤结构化输入',
    `result_summary`        TEXT         COMMENT '最新成功/失败结果摘要',
    `result_ref`            VARCHAR(128) NOT NULL DEFAULT '' COMMENT '最新大结果引用',
    `started_at`            DATETIME     DEFAULT NULL COMMENT '首次开始时间',
    `finished_at`           DATETIME     DEFAULT NULL COMMENT '终态时间',
    `created_at`            DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at`            DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE KEY `uk_plan_step_id` (`step_id`),
    UNIQUE KEY `uk_plan_step_order` (`plan_version_id`, `step_order`),
    UNIQUE KEY `uk_plan_step_key` (`plan_version_id`, `step_key`),
    KEY `idx_plan_step_execution` (`plan_execution_id`, `status`, `step_order`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Plan步骤表';

CREATE TABLE IF NOT EXISTS `tblLlmPlanStepAttempt` (
    `id`                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `step_attempt_id`   VARCHAR(64)  NOT NULL COMMENT 'Attempt ID(plan_attempt_前缀)',
    `plan_execution_id` VARCHAR(64)  NOT NULL COMMENT '所属Plan执行实例ID',
    `plan_version_id`   VARCHAR(64)  NOT NULL COMMENT '所属Plan版本ID',
    `step_id`           VARCHAR(64)  NOT NULL COMMENT '所属Step ID',
    `attempt_no`        INT          NOT NULL COMMENT 'Step内Attempt序号，从1开始',
    `status`            VARCHAR(32)  NOT NULL COMMENT 'RUNNING/SUCCEEDED/FAILED/CANCELLED',
    `step_run_id`       VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '实际执行该Attempt的ReactRun ID',
    `error_code`        VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '失败错误码',
    `error_summary`     TEXT         COMMENT '失败摘要',
    `started_at`        DATETIME     DEFAULT NULL COMMENT '开始时间',
    `finished_at`       DATETIME     DEFAULT NULL COMMENT '终态时间',
    `created_at`        DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at`        DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE KEY `uk_plan_attempt_id` (`step_attempt_id`),
    UNIQUE KEY `uk_plan_attempt_no` (`step_id`, `attempt_no`),
    KEY `idx_plan_attempt_execution` (`plan_execution_id`, `created_at`),
    KEY `idx_plan_attempt_run` (`step_run_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Plan步骤执行尝试表';

CREATE TABLE IF NOT EXISTS `tblLlmPlanStepResult` (
    `id`                BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `step_result_id`    VARCHAR(64)  NOT NULL COMMENT 'Step Result ID(plan_result_前缀)',
    `plan_execution_id` VARCHAR(64)  NOT NULL COMMENT '所属Plan执行实例ID',
    `plan_version_id`   VARCHAR(64)  NOT NULL COMMENT '所属Plan版本ID',
    `step_id`           VARCHAR(64)  NOT NULL COMMENT '所属Step ID',
    `step_attempt_id`   VARCHAR(64)  NOT NULL COMMENT '产出该结果的Attempt ID',
    `status`            VARCHAR(32)  NOT NULL COMMENT 'SUCCEEDED/FAILED',
    `summary`           TEXT         COMMENT '供后续Step注入的短摘要',
    `result_json`       MEDIUMTEXT   COMMENT '结构化结果JSON',
    `result_ref`        VARCHAR(128) NOT NULL DEFAULT '' COMMENT '超长结果引用',
    `created_at`        DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    UNIQUE KEY `uk_plan_result_id` (`step_result_id`),
    KEY `idx_plan_result_step` (`step_id`, `id`),
    KEY `idx_plan_result_attempt` (`step_attempt_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Plan步骤结果表';

CREATE TABLE IF NOT EXISTS `tblLlmPlanWait` (
    `id`                   BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY COMMENT '自增主键ID',
    `wait_request_id`      VARCHAR(64)  NOT NULL COMMENT '等待请求ID(plan_wait_前缀)',
    `plan_execution_id`    VARCHAR(64)  NOT NULL COMMENT '所属Plan执行实例ID',
    `step_id`              VARCHAR(64)  NOT NULL COMMENT '所属Step ID',
    `step_attempt_id`      VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '关联Attempt；纯等待Step可为空',
    `wait_type`            VARCHAR(32)  NOT NULL COMMENT 'USER_INPUT/USER_ACTION/EXTERNAL_TASK',
    `question`             TEXT         COMMENT '前端展示的问题/确认说明',
    `response_schema_json` TEXT         COMMENT 'USER_INPUT响应Schema',
    `data_json`            MEDIUMTEXT   COMMENT '前端展示辅助数据/外部任务句柄',
    `status`               VARCHAR(16)  NOT NULL COMMENT 'PENDING/RESOLVED/CANCELLED',
    `response_json`        MEDIUMTEXT   COMMENT '用户恢复时提交的响应JSON',
    `resolved_at`          DATETIME     DEFAULT NULL COMMENT '解决/取消时间',
    `created_at`           DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
    `updated_at`           DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
    UNIQUE KEY `uk_plan_wait_id` (`wait_request_id`),
    KEY `idx_plan_wait_pending` (`plan_execution_id`, `status`, `id`),
    KEY `idx_plan_wait_step` (`step_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Plan暂停/恢复请求表';

-- 存量环境增量：直接执行上方六个 CREATE TABLE IF NOT EXISTS 即可，无需修改 ReAct 核心三表。

