package pyexec

import (
	"strings"

	"react-base-service/components"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// LoadedArtifact 是下载接口需要的产物内容与展示元信息。
type LoadedArtifact struct {
	FileName string
	MimeType string
	Data     []byte
}

// LoadArtifactForDownload 按 artifactID 取产物元数据并从 COS 读取文件内容，供后端下载接口回吐给前端。
// 产物不存在时返回 (nil, nil)；COS 读取失败返回错误。
func LoadArtifactForDownload(ctx *gin.Context, artifactID string) (*LoadedArtifact, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return nil, nil
	}
	record, err := model.GetReactArtifactByArtifactID(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	if err := helpers.EnsureCos(); err != nil {
		return nil, components.ErrorSystemError
	}
	data, err := helpers.CosClient.DownloadData(ctx.Request.Context(), record.CosKey)
	if err != nil {
		return nil, err
	}
	mimeType := strings.TrimSpace(record.MimeType)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return &LoadedArtifact{
		FileName: record.FileName,
		MimeType: mimeType,
		Data:     data,
	}, nil
}
