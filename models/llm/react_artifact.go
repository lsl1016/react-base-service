package model

import (
	"context"
	"errors"
	"time"

	"react-base-service/components"
	"react-base-service/helpers"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ReactArtifact 保存 python_exec 生成的文件产物元数据；文件本体存 COS，前端经后端下载接口按 artifact_id 取用。
type ReactArtifact struct {
	ID         uint      `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	ArtifactID string    `json:"artifactId" gorm:"column:artifact_id;not null"`
	SessionID  string    `json:"sessionId" gorm:"column:session_id;not null"`
	RunID      string    `json:"runId" gorm:"column:run_id;not null"`
	ToolUseID  string    `json:"toolUseId" gorm:"column:tool_use_id;not null;default:''"`
	UserName   string    `json:"userName" gorm:"column:user_name;not null;default:''"`
	CallerKey  string    `json:"callerKey" gorm:"column:caller_key;not null;default:''"`
	FileName   string    `json:"fileName" gorm:"column:file_name;not null;default:''"`
	MimeType   string    `json:"mimeType" gorm:"column:mime_type;not null;default:''"`
	CosKey     string    `json:"cosKey" gorm:"column:cos_key;not null"`
	SizeBytes  int       `json:"sizeBytes" gorm:"column:size_bytes;not null;default:0"`
	CreatedAt  time.Time `json:"createdAt" gorm:"column:created_at"`
}

func (r *ReactArtifact) TableName() string {
	return "tblLlmReactArtifact"
}

func CreateReactArtifact(ctx *gin.Context, artifact *ReactArtifact) error {
	err := helpers.MysqlClientLLM.Model(&ReactArtifact{}).WithContext(ctx).Create(artifact).Error
	if err != nil {
		return components.ErrorDbInsert.Wrap(err)
	}
	return nil
}

func GetReactArtifactByArtifactID(ctx *gin.Context, artifactID string) (*ReactArtifact, error) {
	var artifact ReactArtifact
	err := helpers.MysqlClientLLM.Model(&ReactArtifact{}).WithContext(ctx).
		Where("artifact_id = ?", artifactID).First(&artifact).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return &artifact, nil
}

// ListReactArtifactsBySessionID 按会话返回产物元数据（id 升序；通知/报告链接用）。
func ListReactArtifactsBySessionID(ctx context.Context, sessionID string) ([]ReactArtifact, error) {
	var artifacts []ReactArtifact
	err := helpers.MysqlClientLLM.Model(&ReactArtifact{}).WithContext(ctx).
		Where("session_id = ?", sessionID).Order("id ASC").Find(&artifacts).Error
	if err != nil {
		return nil, components.ErrorDbSelect.Wrap(err)
	}
	return artifacts, nil
}
