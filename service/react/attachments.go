package react

import (
	"context"
	"fmt"
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	model "react-base-service/models/llm"
	"react-base-service/service/skillchatfile"

	"github.com/gin-gonic/gin"
)

const (
	// reactAttachmentMaxCount 单轮最多可携带的附件数量
	reactAttachmentMaxCount = 5
	// reactAttachmentMaxTotalBytes 单轮附件累计大小上限，当前与单文件上传上限保持一致。
	reactAttachmentMaxTotalBytes = skillchatfile.ChatFileUploadMaxBytes
)

type reactAttachmentSnapshot struct {
	FileID      string `json:"fileId"`
	FileName    string `json:"fileName"`
	Ext         string `json:"ext"`
	Size        int64  `json:"size"`
	Description string `json:"description,omitempty"`
}

// prepareReactAttachments 只做校验并产出附件元信息快照，不再下载解码内容。
// 内容按需通过 read_attachment / inspect_attachment 读取，或由 python_exec 按 fileId 引用分析。
func prepareReactAttachments(ctx *gin.Context, refs []params.ReactAttachmentRef, owner string) ([]reactAttachmentSnapshot, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > reactAttachmentMaxCount {
		return nil, components.ParamInvalidf("附件数量不能超过 %d 个", reactAttachmentMaxCount)
	}

	snapshots := make([]reactAttachmentSnapshot, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	var totalSize int64
	for _, ref := range refs {
		fileID := strings.TrimSpace(ref.FileID)
		if fileID == "" {
			return nil, components.ErrorParamInvalid.Sprintf("attachments.fileId 不能为空")
		}
		if seen[fileID] {
			return nil, components.ErrorParamInvalid.Sprintf("重复的附件 fileId: %s", fileID)
		}
		seen[fileID] = true

		record, err := resolveAttachmentRecord(ctx.Request.Context(), owner, fileID)
		if err != nil {
			return nil, err
		}
		totalSize += record.Size
		if err := validateReactAttachmentTotalSize(totalSize); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, reactAttachmentSnapshot{
			FileID:      record.FileID,
			FileName:    record.FileName,
			Ext:         record.Ext,
			Size:        record.Size,
			Description: strings.TrimSpace(ref.Description),
		})
	}
	return snapshots, nil
}

func validateReactAttachmentTotalSize(totalSize int64) error {
	if totalSize > reactAttachmentMaxTotalBytes {
		return components.ParamInvalidf("附件总大小不能超过 50MB")
	}
	return nil
}

// downloadDecodeAttachment 下载并按记录的 charset 解码成 UTF-8 文本，供 read_attachment/inspect_attachment 使用。
func downloadDecodeAttachment(ctx context.Context, record *model.ChatFileRecord) (string, error) {
	if err := helpers.EnsureCos(); err != nil {
		return "", components.ErrorSystemError
	}
	if helpers.CosClient == nil {
		return "", components.ErrorSystemError
	}
	data, err := helpers.CosClient.DownloadData(ctx, record.CosKey)
	if err != nil {
		return "", components.ErrorSystemError
	}
	text, err := skillchatfile.DecodeChatFileToUTF8(data, record.Charset)
	if err != nil {
		return "", components.ParamInvalidf("附件内容解码失败: %s", record.FileID)
	}
	return text, nil
}

// renderAttachmentManifest 渲染附件清单：只给元信息和使用方式，不展开内容。
// 模型按"任务类型"路由：理解内容→read_attachment；探列结构→inspect_attachment；计算分析→python_exec 按 fileId 引用。
func renderAttachmentManifest(snapshots []reactAttachmentSnapshot) string {
	if len(snapshots) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<attachments>\n")
	sb.WriteString("本轮用户上传了以下文件（内容未展开，按需获取）：\n")
	for i, item := range snapshots {
		sb.WriteString(fmt.Sprintf("%d. fileId: %s | 文件名: %s | 类型: %s | 大小: %s", i+1, item.FileID, item.FileName, item.Ext, humanizeBytes(item.Size)))
		if desc := strings.TrimSpace(item.Description); desc != "" {
			sb.WriteString(" | 描述: " + desc)
		}
		sb.WriteString("\n")
	}
	sb.WriteString(strings.Join([]string{
		"使用方式（按任务类型选择，不看文件大小）：",
		"· 需要理解、总结、改写、翻译或逐行判断文件内容 → 调 read_attachment(fileId) 读入内容（内容较大时自动返回预览，可用 read_tool_result 翻页）。",
		"· 需要统计、聚合、过滤、连表、计算等数据处理 → 用 python_exec，在 inputs 中以 {type:\"attachment\", fileId} 引用该文件分析。",
		"· 拿不准 csv 的列名/结构、要先搞清楚再写分析代码 → 调 inspect_attachment(fileId) 获取列结构、样例行和行数。",
		"</attachments>",
	}, "\n"))
	return sb.String()
}

func humanizeBytes(size int64) string {
	switch {
	case size >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(size)/(1<<10))
	default:
		return fmt.Sprintf("%dB", size)
	}
}

func reactAttachmentRefsFromSnapshots(snapshots []reactAttachmentSnapshot) []params.ReactAttachmentRef {
	if len(snapshots) == 0 {
		return nil
	}
	refs := make([]params.ReactAttachmentRef, 0, len(snapshots))
	for _, snapshot := range snapshots {
		refs = append(refs, params.ReactAttachmentRef{
			FileID:      snapshot.FileID,
			FileName:    snapshot.FileName,
			Description: snapshot.Description,
		})
	}
	return refs
}
