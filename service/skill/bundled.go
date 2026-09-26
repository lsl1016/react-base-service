package skill

// 内置技能包（WP3，参考 ZCode bundled-skills 的"引擎+数据分离"模式，
// 方案 docs/参考ZCode的运行时增强方案.md）：
//
//   - 技能正文随仓库 bundled-skills/ 目录经 go:embed 打进二进制，与 web/sdk/dist 同模式；
//   - caller 创建（service/caller）与服务启动（main 种子存量 caller）时幂等导入；
//   - DB 同名行（同 caller_key + name）存在即跳过——用户覆盖优先，内置内容不再回写；
//   - 单条技能导入失败只记日志不阻断（种子是增强不是依赖），其余技能继续。
//
// 与 Bundle 安装的区别：Bundle 是用户/管理员安装的可回滚插件包；内置技能是随版本演进的
// 官方基线内容，不做卸载（面板停用即可，status=0 不删除）。

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"react-base-service/components/params"
	model "react-base-service/models/llm"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

//go:embed bundled-skills
var bundledSkillFS embed.FS

// BundledSkillCodeInvestigation 是「代码工作区调查」内置技能名；
// code-reader 内置子代理（service/agent）的 skills 白名单按此名引用，与 manifest.json 保持一致。
const BundledSkillCodeInvestigation = "代码工作区调查"

// bundledSkillEntry 是 manifest 里的一条内置技能声明。
type bundledSkillEntry struct {
	Name               string   `json:"name"`
	File               string   `json:"file"`
	TriggerCondition   string   `json:"triggerCondition"`
	ExecutionSteps     string   `json:"executionSteps"`
	BusinessContext    string   `json:"businessContext"`
	Triggers           []string `json:"triggers"`
}

// bundledSkillManifest 是 bundled-skills/manifest.json 的结构。
type bundledSkillManifest struct {
	Version int                 `json:"version"`
	Skills  []bundledSkillEntry `json:"skills"`
}

// loadBundledSkillManifest 读取并校验内置技能清单；清单损坏按空包处理（记日志）。
func loadBundledSkillManifest() bundledSkillManifest {
	var manifest bundledSkillManifest
	data, err := bundledSkillFS.ReadFile("bundled-skills/manifest.json")
	if err != nil {
		zlog.Warnf(nil, "[Skill.Bundled] 内置技能清单读取失败(跳过种子): err=%v", err)
		return manifest
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		zlog.Warnf(nil, "[Skill.Bundled] 内置技能清单解析失败(跳过种子): err=%v", err)
		return manifest
	}
	return manifest
}

// readBundledSkillBody 读取一条内置技能的 SKILL.md 正文。
func readBundledSkillBody(entry bundledSkillEntry) (string, error) {
	file, err := bundledSkillFS.Open("bundled-skills/" + entry.File)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(file.(io.Reader))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// EnsureBundledSkillsForCaller 为一个 caller 幂等导入内置技能：
// 同 caller_key + name 的活跃行存在即跳过（用户覆盖优先），缺失才新建。
// 返回本次新建的技能数；错误按条隔离，单条失败不影响其余。
func EnsureBundledSkillsForCaller(ctx *gin.Context, callerKey, createdBy string) (int, error) {
	manifest := loadBundledSkillManifest()
	if len(manifest.Skills) == 0 {
		return 0, nil
	}
	created := 0
	var firstErr error
	for _, entry := range manifest.Skills {
		if strings.TrimSpace(entry.Name) == "" {
			continue
		}
		existing, err := model.FindActiveSkillByCallerAndName(ctx, callerKey, entry.Name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			zlog.Warnf(ctx, "[Skill.Bundled] 查询已有技能失败(跳过): caller=%s, name=%s, err=%v", callerKey, entry.Name, err)
			continue
		}
		if existing != nil {
			continue
		}
		body, err := readBundledSkillBody(entry)
		if err != nil {
			zlog.Warnf(ctx, "[Skill.Bundled] 内置技能正文读取失败(跳过): caller=%s, file=%s, err=%v", callerKey, entry.File, err)
			continue
		}
		enabled := 1
		_, err = CreateSkill(ctx, &params.CreateSkillReq{
			Name:             entry.Name,
			Description:      bundledSkillDescription(entry),
			TriggerCondition: entry.TriggerCondition,
			ExecutionSteps:   entry.ExecutionSteps,
			BusinessContext:  entry.BusinessContext,
			Triggers:         entry.Triggers,
			Content:          body,
			CallerKey:        callerKey,
			RouteValues:      []string{},
			Status:           &enabled,
		}, createdBy)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			zlog.Warnf(ctx, "[Skill.Bundled] 内置技能导入失败(跳过): caller=%s, name=%s, err=%v", callerKey, entry.Name, err)
			continue
		}
		created++
		zlog.Infof(ctx, "[Skill.Bundled] 内置技能已导入: caller=%s, name=%s", callerKey, entry.Name)
	}
	return created, firstErr
}

// bundledSkillDescription 由清单字段合成技能描述（摘要索引给模型看的判断依据）。
func bundledSkillDescription(entry bundledSkillEntry) string {
	parts := make([]string, 0, 3)
	if strings.TrimSpace(entry.BusinessContext) != "" {
		parts = append(parts, strings.TrimSpace(entry.BusinessContext))
	}
	if strings.TrimSpace(entry.ExecutionSteps) != "" {
		parts = append(parts, "步骤："+strings.TrimSpace(entry.ExecutionSteps))
	}
	if strings.TrimSpace(entry.TriggerCondition) != "" {
		parts = append(parts, "触发："+strings.TrimSpace(entry.TriggerCondition))
	}
	return strings.Join(parts, "；")
}

// SeedBundledSkillsForAllCallers 服务启动时为全部已有 caller 幂等补种内置技能（版本升级后生效）。
// 使用脱离 HTTP 连接的 headless gin ctx；失败整体降级为日志，不阻断启动。
func SeedBundledSkillsForAllCallers() {
	manifest := loadBundledSkillManifest()
	if len(manifest.Skills) == 0 {
		return
	}
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/internal/bundled-skills-seed", nil).WithContext(context.Background())
	callers, err := model.ListCallers(ginCtx)
	if err != nil {
		zlog.Warnf(nil, "[Skill.Bundled] 启动种子读取 caller 列表失败(跳过): err=%v", err)
		return
	}
	// default 是默认作用域伪 caller（全 caller 可见），同样补种一份。
	scopes := []string{model.DefaultCallerKey}
	for _, caller := range callers {
		scopes = append(scopes, caller.CallerKey)
	}
	for _, callerKey := range scopes {
		created, err := EnsureBundledSkillsForCaller(ginCtx, callerKey, "bundled-skills-seed")
		if err != nil {
			zlog.Warnf(nil, "[Skill.Bundled] 启动种子部分失败(继续): caller=%s, err=%v", callerKey, err)
		}
		if created > 0 {
			zlog.Infof(nil, "[Skill.Bundled] 启动种子完成: caller=%s, created=%d", callerKey, created)
		}
	}
}
