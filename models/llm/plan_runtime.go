package model

import "time"

const (
	PlanExecutionStatusPlanning         = "PLANNING"
	PlanExecutionStatusRunning          = "RUNNING"
	PlanExecutionStatusWaitUserInput    = "WAIT_USER_INPUT"
	PlanExecutionStatusWaitUserAction   = "WAIT_USER_ACTION"
	PlanExecutionStatusWaitExternalTask = "WAIT_EXTERNAL_TASK"
	PlanExecutionStatusSucceeded        = "SUCCEEDED"
	PlanExecutionStatusFailed           = "FAILED"
	PlanExecutionStatusCancelled        = "CANCELLED"

	PlanStepStatusPending   = "PENDING"
	PlanStepStatusRunning   = "RUNNING"
	PlanStepStatusWaiting   = "WAITING"
	PlanStepStatusSucceeded = "SUCCEEDED"
	PlanStepStatusFailed    = "FAILED"
	PlanStepStatusCancelled = "CANCELLED"
	PlanStepStatusSkipped   = "SKIPPED"

	PlanStepTypeAgent      = "AGENT"
	PlanStepTypeUserInput  = "USER_INPUT"
	PlanStepTypeUserAction = "USER_ACTION"

	PlanAttemptStatusRunning   = "RUNNING"
	PlanAttemptStatusSucceeded = "SUCCEEDED"
	PlanAttemptStatusFailed    = "FAILED"
	PlanAttemptStatusCancelled = "CANCELLED"

	PlanWaitTypeUserInput    = "USER_INPUT"
	PlanWaitTypeUserAction   = "USER_ACTION"
	PlanWaitTypeExternalTask = "EXTERNAL_TASK"

	PlanWaitStatusPending   = "PENDING"
	PlanWaitStatusResolved  = "RESOLVED"
	PlanWaitStatusCancelled = "CANCELLED"
)

type PlanExecution struct {
	ID               uint       `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	PlanExecutionID  string     `json:"planExecutionId" gorm:"column:plan_execution_id;not null"`
	SessionID        string     `json:"sessionId" gorm:"column:session_id;not null"`
	OuterRunID       string     `json:"outerRunId" gorm:"column:outer_run_id;not null"`
	CallerKey        string     `json:"callerKey" gorm:"column:caller_key;not null"`
	UserName         string     `json:"userName" gorm:"column:user_name;not null"`
	Status           string     `json:"status" gorm:"column:status;not null"`
	CurrentVersionID string     `json:"currentVersionId" gorm:"column:current_version_id;not null;default:''"`
	CurrentStepID    string     `json:"currentStepId" gorm:"column:current_step_id;not null;default:''"`
	Summary          string     `json:"summary" gorm:"column:summary;type:text"`
	ExecutionMode    string     `json:"executionMode" gorm:"column:execution_mode;not null;default:'plan'"`
	ResultJSON       string     `json:"resultJson" gorm:"column:result_json;type:mediumtext"`
	ErrorCode        string     `json:"errorCode" gorm:"column:error_code;not null;default:''"`
	ErrorSummary     string     `json:"errorSummary" gorm:"column:error_summary;type:text"`
	FinishedAt       *time.Time `json:"finishedAt,omitempty" gorm:"column:finished_at"`
	CreatedAt        time.Time  `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt        time.Time  `json:"updatedAt" gorm:"column:updated_at"`
}
func (*PlanExecution) TableName() string { return "tblLlmPlanExecution" }

type PlanVersion struct {
	ID              uint      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	PlanVersionID   string    `json:"planVersionId" gorm:"column:plan_version_id;not null"`
	PlanExecutionID string    `json:"planExecutionId" gorm:"column:plan_execution_id;not null"`
	VersionNo       int       `json:"versionNo" gorm:"column:version_no;not null"`
	Title           string    `json:"title" gorm:"column:title;not null;default:''"`
	Overview        string    `json:"overview" gorm:"column:overview;type:text"`
	Reason          string    `json:"reason" gorm:"column:reason;not null;default:'initial'"`
	CreatedBy       string    `json:"createdBy" gorm:"column:created_by;not null;default:''"`
	CreatedAt       time.Time `json:"createdAt" gorm:"column:created_at"`
}
func (*PlanVersion) TableName() string { return "tblLlmPlanVersion" }

type PlanStep struct {
	ID                uint       `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	StepID            string     `json:"stepId" gorm:"column:step_id;not null"`
	PlanExecutionID   string     `json:"planExecutionId" gorm:"column:plan_execution_id;not null"`
	PlanVersionID     string     `json:"planVersionId" gorm:"column:plan_version_id;not null"`
	StepOrder         int        `json:"stepOrder" gorm:"column:step_order;not null"`
	StepKey           string     `json:"stepKey" gorm:"column:step_key;not null"`
	StepName          string     `json:"stepName" gorm:"column:step_name;not null"`
	Instruction       string     `json:"instruction" gorm:"column:instruction;type:text"`
	ExpectedOutput    string     `json:"expectedOutput" gorm:"column:expected_output;type:text"`
	SuccessCriteria   string     `json:"successCriteria" gorm:"column:success_criteria_json;type:text"`
	Status            string     `json:"status" gorm:"column:status;not null"`
	StepType          string     `json:"stepType" gorm:"column:step_type;not null"`
	Required          bool       `json:"required" gorm:"column:required;not null;default:1"`
	MaxAttempts       int        `json:"maxAttempts" gorm:"column:max_attempts;not null;default:1"`
	TimeoutSeconds    int        `json:"timeoutSeconds" gorm:"column:timeout_seconds;not null;default:0"`
	DependsOnJSON     string     `json:"dependsOnJson" gorm:"column:depends_on_json;type:text"`
	InputJSON         string     `json:"inputJson" gorm:"column:input_json;type:mediumtext"`
	ResultSummary     string     `json:"resultSummary" gorm:"column:result_summary;type:text"`
	ResultRef         string     `json:"resultRef" gorm:"column:result_ref;not null;default:''"`
	StartedAt         *time.Time `json:"startedAt,omitempty" gorm:"column:started_at"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty" gorm:"column:finished_at"`
	CreatedAt         time.Time  `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt         time.Time  `json:"updatedAt" gorm:"column:updated_at"`
}
func (*PlanStep) TableName() string { return "tblLlmPlanStep" }

type PlanStepAttempt struct {
	ID              uint       `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	StepAttemptID   string     `json:"stepAttemptId" gorm:"column:step_attempt_id;not null"`
	PlanExecutionID string     `json:"planExecutionId" gorm:"column:plan_execution_id;not null"`
	PlanVersionID   string     `json:"planVersionId" gorm:"column:plan_version_id;not null"`
	StepID          string     `json:"stepId" gorm:"column:step_id;not null"`
	AttemptNo       int        `json:"attemptNo" gorm:"column:attempt_no;not null"`
	Status          string     `json:"status" gorm:"column:status;not null"`
	StepRunID       string     `json:"stepRunId" gorm:"column:step_run_id;not null;default:''"`
	ErrorCode       string     `json:"errorCode" gorm:"column:error_code;not null;default:''"`
	ErrorSummary    string     `json:"errorSummary" gorm:"column:error_summary;type:text"`
	StartedAt       *time.Time `json:"startedAt,omitempty" gorm:"column:started_at"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty" gorm:"column:finished_at"`
	CreatedAt       time.Time  `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt       time.Time  `json:"updatedAt" gorm:"column:updated_at"`
}
func (*PlanStepAttempt) TableName() string { return "tblLlmPlanStepAttempt" }

type PlanStepResult struct {
	ID              uint      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	StepResultID    string    `json:"stepResultId" gorm:"column:step_result_id;not null"`
	PlanExecutionID string    `json:"planExecutionId" gorm:"column:plan_execution_id;not null"`
	PlanVersionID   string    `json:"planVersionId" gorm:"column:plan_version_id;not null"`
	StepID          string    `json:"stepId" gorm:"column:step_id;not null"`
	StepAttemptID   string    `json:"stepAttemptId" gorm:"column:step_attempt_id;not null"`
	Status          string    `json:"status" gorm:"column:status;not null"`
	Summary         string    `json:"summary" gorm:"column:summary;type:text"`
	ResultJSON      string    `json:"resultJson" gorm:"column:result_json;type:mediumtext"`
	ResultRef       string    `json:"resultRef" gorm:"column:result_ref;not null;default:''"`
	CreatedAt       time.Time `json:"createdAt" gorm:"column:created_at"`
}
func (*PlanStepResult) TableName() string { return "tblLlmPlanStepResult" }

type PlanWait struct {
	ID                 uint       `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	WaitRequestID      string     `json:"waitRequestId" gorm:"column:wait_request_id;not null"`
	PlanExecutionID    string     `json:"planExecutionId" gorm:"column:plan_execution_id;not null"`
	StepID             string     `json:"stepId" gorm:"column:step_id;not null"`
	StepAttemptID      string     `json:"stepAttemptId" gorm:"column:step_attempt_id;not null;default:''"`
	WaitType           string     `json:"waitType" gorm:"column:wait_type;not null"`
	Question           string     `json:"question" gorm:"column:question;type:text"`
	ResponseSchemaJSON string     `json:"responseSchemaJson" gorm:"column:response_schema_json;type:text"`
	DataJSON           string     `json:"dataJson" gorm:"column:data_json;type:mediumtext"`
	Status             string     `json:"status" gorm:"column:status;not null"`
	ResponseJSON       string     `json:"responseJson" gorm:"column:response_json;type:mediumtext"`
	ResolvedAt         *time.Time `json:"resolvedAt,omitempty" gorm:"column:resolved_at"`
	CreatedAt          time.Time  `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt          time.Time  `json:"updatedAt" gorm:"column:updated_at"`
}
func (*PlanWait) TableName() string { return "tblLlmPlanWait" }
