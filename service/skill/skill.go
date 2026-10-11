package skill

import (
	"bytes"
	"archive/zip"
	"encoding/json"
	"io"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/components/route"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

func CreateSkill(ctx *gin.Context, req *params.CreateSkillReq, createdBy string) (*model.Skill, error) {
	// app_key=default 是默认作用域伪 app（全 app 可用），不要求真实 app 存在。
	if !model.IsReservedAppKey(req.AppKey) {
		app, err := model.GetActiveAppByKey(ctx, req.AppKey)
		if err != nil {
			return nil, err
		}
		if app == nil {
			return nil, components.ErrorAppNotFound.Sprintf(req.AppKey)
		}
	}

	isDefault, err := resolveCreateSkillIsDefault(req.IsDefault)
	if err != nil {
		return nil, err
	}
	rv := req.RouteValues
	if rv == nil {
		rv = []string{}
	}
	routeValues, _ := json.Marshal(rv)

	triggers, err := normalizeSkillTriggers(req.Triggers)
	if err != nil {
		return nil, err
	}
	triggersJSON, _ := json.Marshal(triggers)

	if isDefault == 1 {
		existing, err := model.GetDefaultSkillByAppAndRoute(ctx, req.AppKey, string(routeValues))
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return nil, components.ErrorSkillDefaultExists.Sprintf(req.AppKey, string(routeValues))
		}
	}

	skillID := "skill_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	status, err := resolveSkillStatus(req.Status)
	if err != nil {
		return nil, err
	}

	skill := &model.Skill{
		SkillID:            skillID,
		Name:               req.Name,
		Description:        req.Description,
		TriggerCondition:   req.TriggerCondition,
		ForbiddenCondition: req.ForbiddenCondition,
		ExecutionSteps:     req.ExecutionSteps,
		BusinessContext:    req.BusinessContext,
		PromptSupplement:   req.PromptSupplement,
		TriggersJSON:       string(triggersJSON),
		Content:            req.Content,
		AppKey:          req.AppKey,
		RouteValues:        string(routeValues),
		IsDefault:          isDefault,
		Status:             status,
		CreatedBy:          createdBy,
		UpdatedBy:          createdBy,
	}

	if err := model.CreateSkill(ctx, skill); err != nil {
		return nil, err
	}
	return skill, nil
}

func resolveCreateSkillIsDefault(isDefault *int) (int, error) {
	if isDefault == nil {
		return 0, nil
	}
	if *isDefault != 0 && *isDefault != 1 {
		return 0, components.ErrorParamInvalid.Sprintf("isDefault 仅支持 0 或 1")
	}
	return *isDefault, nil
}

func resolveSkillStatus(status *int) (int, error) {
	if status == nil {
		return 0, components.ErrorParamInvalid.Sprintf("status不能为空")
	}
	if *status != 0 && *status != 1 {
		return 0, components.ErrorParamInvalid.Sprintf("status 仅支持 0 或 1")
	}
	return *status, nil
}

func UpdateSkill(ctx *gin.Context, req *params.UpdateSkillReq) (*model.Skill, error) {
	existing, err := model.GetSkillBySkillID(ctx, req.SkillID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, components.ErrorSkillNotFound.Sprintf(req.SkillID)
	}

	status, err := resolveSkillStatus(req.Status)
	if err != nil {
		return nil, err
	}

	updateRv := req.RouteValues
	if updateRv == nil {
		updateRv = []string{}
	}
	rv, _ := json.Marshal(updateRv)
	updatedBy := helpers.GetUserName(ctx)
	updates := map[string]interface{}{
		"name":              req.Name,
		"description":       req.Description,
		"trigger_condition": req.TriggerCondition,
		"execution_steps":   req.ExecutionSteps,
		"business_context":  req.BusinessContext,
		"route_values":      string(rv),
		"status":            status,
		"updated_by":        updatedBy,
	}
	if req.ForbiddenCondition != nil {
		updates["forbidden_condition"] = *req.ForbiddenCondition
	}
	if req.PromptSupplement != nil {
		updates["prompt_supplement"] = *req.PromptSupplement
	}
	if req.Triggers != nil {
		triggers, err := normalizeSkillTriggers(*req.Triggers)
		if err != nil {
			return nil, err
		}
		triggersJSON, _ := json.Marshal(triggers)
		updates["triggers_json"] = string(triggersJSON)
	}
	if req.Content != nil {
		updates["content"] = *req.Content
	}

	if err := model.UpdateSkillBySkillID(ctx, req.SkillID, updates); err != nil {
		return nil, err
	}

	updated, err := model.GetSkillBySkillID(ctx, req.SkillID)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, components.ErrorSkillNotFound.Sprintf(req.SkillID)
	}
	return updated, nil
}

func DeleteSkill(ctx *gin.Context, skillID string) (*model.Skill, error) {
	existing, err := model.GetSkillBySkillID(ctx, skillID)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, components.ErrorSkillNotFound.Sprintf(skillID)
	}
	updatedBy := helpers.GetUserName(ctx)
	if err := model.UpdateSkillBySkillID(ctx, skillID, map[string]interface{}{"updated_by": updatedBy}); err != nil {
		return nil, err
	}
	if err := model.SoftDeleteSkillBySkillID(ctx, skillID); err != nil {
		return nil, err
	}

	deleted, err := model.GetSkillBySkillIDUnscoped(ctx, skillID)
	if err != nil {
		return nil, err
	}
	if deleted == nil {
		return nil, components.ErrorSkillNotFound.Sprintf(skillID)
	}
	return deleted, nil
}

func GetDetail(ctx *gin.Context, skillID string) (*model.Skill, error) {
	s, err := model.GetSkillBySkillID(ctx, skillID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, components.ErrorSkillNotFound.Sprintf(skillID)
	}
	return s, nil
}

func ListByAppAndRoute(ctx *gin.Context, appKey string, routeValues []string) ([]model.Skill, error) {
	// appKey 为空：管理控制台「全部」视图，跨 app 列出，忽略路由过滤。
	if strings.TrimSpace(appKey) == "" {
		return model.ListAllSkills(ctx)
	}
	exact, parents := route.SplitRouteExactAndParents(routeValues)
	return model.ListSkillsByRouteWithFallback(ctx, appKey, exact, parents)
}

// ToSkillResp 将 model.Skill 转换为 params.SkillResp
func parseSkillRouteValues(routeValuesRaw string) []string {
	var routeValues []string
	_ = json.Unmarshal([]byte(routeValuesRaw), &routeValues)
	return routeValues
}

func parseSkillTriggers(triggersRaw string) []string {
	var triggers []string
	_ = json.Unmarshal([]byte(triggersRaw), &triggers)
	return triggers
}

func ToSkillResp(s *model.Skill) params.SkillResp {
	routeValues := parseSkillRouteValues(s.RouteValues)
	return params.SkillResp{
		SkillID:            s.SkillID,
		Name:               s.Name,
		Description:        s.Description,
		TriggerCondition:   s.TriggerCondition,
		ForbiddenCondition: s.ForbiddenCondition,
		ExecutionSteps:     s.ExecutionSteps,
		BusinessContext:    s.BusinessContext,
		PromptSupplement:   s.PromptSupplement,
		Triggers:           parseSkillTriggers(s.TriggersJSON),
		Content:            s.Content,
		AppKey:          s.AppKey,
		RouteValues:        routeValues,
		IsDefault:          s.IsDefault,
		Status:             s.Status,
		CreatedBy:          s.CreatedBy,
		UpdatedBy:          s.UpdatedBy,
		CreatedAt:          formatTime(s.CreatedAt),
		UpdatedAt:          formatTime(s.UpdatedAt),
		DeletedAt:          formatDeletedAt(int64(s.DeletedAt)),
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2006-01-02 15:04:05")
}

func formatDeletedAt(deletedAt int64) string {
	if deletedAt == 0 {
		return ""
	}
	return time.Unix(deletedAt, 0).Format("2006-01-02 15:04:05")
}

// ---------- SKILL.md 定义导入（P2-2，AgentSkills 文件标准对应物） ----------

// skillMarkdownFrontmatter 是 SKILL.md 的 frontmatter 字段（AgentSkills 兼容子集）。
type skillMarkdownFrontmatter struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Triggers    []string `yaml:"triggers"`
	AppKey   string   `yaml:"app_key"`
	RouteValues []string `yaml:"route_values"`
}

const (
	maxSkillTriggers        = 32
	maxSkillTriggerRuneLen  = 64
	skillDescriptionMaxRunes = 120
)

// ImportFromMarkdown 解析「frontmatter + 正文」格式的 SKILL.md 并入库（P2-2）：
// frontmatter 的 name/description/triggers 对应注册表字段，正文（第二个 --- 之后）入 content 列；
// frontmatter 中的 app_key / route_values 缺省时取请求体字段。
//
// 冲突语义是「同名覆盖」：同 app_key + name 存在未软删行时，只更新文件形态拥有的字段
// （description/triggers_json/content/status/updated_by，沿用 skill_id，不动 is_default 与
// 面板维护的 trigger_condition 等注册表字段），为 P3 Bundle 同名覆盖铺路；不存在则走 CreateSkill 全量校验新建。
func ImportFromMarkdown(ctx *gin.Context, req *params.ImportSkillReq, createdBy string) (*model.Skill, error) {
	skill, _, err := importFromMarkdown(ctx, req, createdBy)
	return skill, err
}

// UpsertFromMarkdownForBundle 是 Bundle 安装用的导入（P3）：语义同 ImportFromMarkdown 的
// 同名覆盖，额外返回覆盖前的整行快照 JSON（空串=本次新建），供 Bundle 卸载回滚。
func UpsertFromMarkdownForBundle(ctx *gin.Context, req *params.ImportSkillReq, updatedBy string) (*model.Skill, string, error) {
	return importFromMarkdown(ctx, req, updatedBy)
}

// importFromMarkdown 是导入的内部形态，额外返回覆盖前整行快照（空串=新建）。
func importFromMarkdown(ctx *gin.Context, req *params.ImportSkillReq, createdBy string) (*model.Skill, string, error) {
	frontmatter, body := splitSkillMarkdown(req.Markdown)
	if strings.TrimSpace(frontmatter) == "" {
		return nil, "", components.ErrorSkillImportInvalid.Sprintf("缺少 frontmatter（文件须以 --- 开头）")
	}
	var meta skillMarkdownFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &meta); err != nil {
		return nil, "", components.ErrorSkillImportInvalid.Sprintf("frontmatter 解析失败: %v", err)
	}
	if strings.TrimSpace(meta.Name) == "" {
		return nil, "", components.ErrorSkillImportInvalid.Sprintf("name 不能为空（frontmatter）")
	}
	if strings.TrimSpace(body) == "" {
		return nil, "", components.ErrorSkillImportInvalid.Sprintf("正文为空（第二个 --- 之后应为 Skill 正文）")
	}

	appKey := firstNonEmptyString(meta.AppKey, req.AppKey)
	if appKey == "" {
		return nil, "", components.ErrorSkillImportInvalid.Sprintf("app_key 不能为空（frontmatter 或请求体至少提供一处）")
	}
	routeValues := meta.RouteValues
	if routeValues == nil {
		routeValues = req.RouteValues
	}
	status := req.Status
	if status == nil {
		enabled := 1
		status = &enabled
	}
	triggers, err := normalizeSkillTriggers(meta.Triggers)
	if err != nil {
		return nil, "", err
	}
	triggersJSON, _ := json.Marshal(triggers)
	body = strings.TrimSpace(body)
	description := strings.TrimSpace(meta.Description)
	if description == "" {
		description = deriveSkillDescription(body)
	}

	existing, err := model.FindActiveSkillByAppAndName(ctx, appKey, strings.TrimSpace(meta.Name))
	if err != nil {
		return nil, "", err
	}
	if existing != nil {
		snapshot, _ := json.Marshal(existing)
		updates := map[string]interface{}{
			"description":   description,
			"triggers_json": string(triggersJSON),
			"content":       body,
			"status":        *status,
			"updated_by":    createdBy,
		}
		if err := model.UpdateSkillBySkillID(ctx, existing.SkillID, updates); err != nil {
			return nil, "", err
		}
		updated, err := model.GetSkillBySkillID(ctx, existing.SkillID)
		if err != nil {
			return nil, "", err
		}
		return updated, string(snapshot), nil
	}

	created, err := CreateSkill(ctx, &params.CreateSkillReq{
		Name:         strings.TrimSpace(meta.Name),
		Description:  description,
		Triggers:     triggers,
		Content:      body,
		AppKey:    appKey,
		RouteValues:  routeValues,
		Status:       status,
	}, createdBy)
	if err != nil {
		return nil, "", err
	}
	return created, "", nil
}

// splitSkillMarkdown 拆分 frontmatter 与正文：文件以 --- 行开头，到下一个 --- 行结束。
// 未闭合（只有开头 ---）时返回双空串，由调用方按缺少 frontmatter/正文报错。
func splitSkillMarkdown(markdown string) (frontmatter, body string) {
	normalized := strings.ReplaceAll(markdown, "\r\n", "\n")
	trimmed := strings.TrimLeft(normalized, "\n")
	if !strings.HasPrefix(trimmed, "---\n") {
		return "", strings.TrimSpace(trimmed)
	}
	rest := trimmed[len("---\n"):]
	if idx := strings.Index(rest, "\n---"); idx >= 0 {
		frontmatter = rest[:idx]
		after := rest[idx+len("\n---"):]
		after = strings.TrimLeft(after, "\n")
		return frontmatter, after
	}
	return "", ""
}

// normalizeSkillTriggers 清洗关键词触发器：去空白、去空项、去重（保序），超限报错而非静默截断。
func normalizeSkillTriggers(triggers []string) ([]string, error) {
	seen := make(map[string]struct{}, len(triggers))
	normalized := make([]string, 0, len(triggers))
	for _, trigger := range triggers {
		trigger = strings.TrimSpace(trigger)
		if trigger == "" {
			continue
		}
		if len([]rune(trigger)) > maxSkillTriggerRuneLen {
			return nil, components.ErrorSkillImportInvalid.Sprintf("触发关键词过长（上限 %d 字符）: %.32s…", maxSkillTriggerRuneLen, trigger)
		}
		if _, dup := seen[trigger]; dup {
			continue
		}
		seen[trigger] = struct{}{}
		normalized = append(normalized, trigger)
	}
	if len(normalized) > maxSkillTriggers {
		return nil, components.ErrorSkillImportInvalid.Sprintf("触发关键词数量超上限 %d: %d", maxSkillTriggers, len(normalized))
	}
	return normalized, nil
}

// deriveSkillDescription frontmatter 未写 description 时，取正文首个非空行（剥掉标题标记）截断生成。
func deriveSkillDescription(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimLeft(line, "#*-> ")
		if line == "" {
			continue
		}
		runes := []rune(line)
		if len(runes) > skillDescriptionMaxRunes {
			runes = runes[:skillDescriptionMaxRunes]
			return string(runes) + "…"
		}
		return string(runes)
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ---------- zip 目录式批量导入 ----------

const (
	// MaxSkillZipBytes zip 包原始体积上限（防上传滥用）。
	MaxSkillZipBytes = 10 << 20
	// maxSkillZipUncompressedBytes 解压后累计体积上限（防 zip 炸弹）。
	maxSkillZipUncompressedBytes = 10 << 20
	// maxSkillZipEntries zip 内 SKILL.md 数量上限。
	maxSkillZipEntries = 64
	// maxSkillMdBytes 单个 SKILL.md 解压后体积上限。
	maxSkillMdBytes = 1 << 20
)

// ImportFromZip 遍历 zip 包内所有 SKILL.md 逐个导入（best-effort：单项失败记录错误继续）。
// base 的 appKey/routeValues/status 作为各文件 frontmatter 缺省兜底；条目纯内存读取，不落盘。
func ImportFromZip(ctx *gin.Context, zipBytes []byte, base *params.ImportSkillReq, createdBy string) ([]params.SkillImportItemResp, error) {
	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, components.ErrorSkillImportInvalid.Sprintf("zip 解析失败: %v", err)
	}

	var items []params.SkillImportItemResp
	totalBytes := 0
	for _, entry := range reader.File {
		if !isSkillMdEntry(entry.Name) {
			continue
		}
		if len(items) >= maxSkillZipEntries {
			return nil, components.ErrorSkillImportInvalid.Sprintf("zip 内 SKILL.md 数量超过上限 %d", maxSkillZipEntries)
		}
		content, err := readZipEntry(entry, maxSkillMdBytes)
		if err != nil {
			items = append(items, params.SkillImportItemResp{Name: entry.Name, Error: err.Error()})
			continue
		}
		totalBytes += len(content)
		if totalBytes > maxSkillZipUncompressedBytes {
			return nil, components.ErrorSkillImportInvalid.Sprintf("zip 解压总量超过上限 %d 字节", maxSkillZipUncompressedBytes)
		}

		item := params.SkillImportItemResp{Name: entry.Name}
		skill, previousJSON, err := importFromMarkdown(ctx, &params.ImportSkillReq{
			AppKey:   base.AppKey,
			RouteValues: base.RouteValues,
			Status:      base.Status,
			Markdown:    string(content),
		}, createdBy)
		if err != nil {
			item.Error = err.Error()
		} else {
			item.Name = skill.Name
			item.SkillID = skill.SkillID
			item.Created = previousJSON == ""
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil, components.ErrorSkillImportInvalid.Sprintf("zip 内未找到 SKILL.md（期待 <skill目录>/SKILL.md 结构）")
	}
	return items, nil
}

// isSkillMdEntry 判定 zip 条目是否为待导入的 SKILL.md：文件名（最后一段）精确匹配（大小写不敏感），
// 跳过 macOS 打包残留目录与隐藏目录。
func isSkillMdEntry(name string) bool {
	normalized := strings.ReplaceAll(name, "\\", "/")
	base := normalized
	if slashIdx := strings.LastIndex(normalized, "/"); slashIdx >= 0 {
		base = normalized[slashIdx+1:]
	}
	if !strings.EqualFold(base, "skill.md") {
		return false
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "__MACOSX" || (segment != "" && strings.HasPrefix(segment, ".")) {
			return false
		}
	}
	return true
}

// readZipEntry 读取单个 zip 条目（纯内存），limit 为解压后体积上限。
func readZipEntry(entry *zip.File, limit int) ([]byte, error) {
	if entry.UncompressedSize64 > uint64(limit) {
		return nil, components.ErrorSkillImportInvalid.Sprintf("%s 解压后超过 %d 字节上限", entry.Name, limit)
	}
	rc, err := entry.Open()
	if err != nil {
		return nil, components.ErrorSkillImportInvalid.Sprintf("打开 %s 失败: %v", entry.Name, err)
	}
	defer rc.Close()
	content, err := io.ReadAll(io.LimitReader(rc, int64(limit)+1))
	if err != nil {
		return nil, components.ErrorSkillImportInvalid.Sprintf("读取 %s 失败: %v", entry.Name, err)
	}
	if len(content) > limit {
		return nil, components.ErrorSkillImportInvalid.Sprintf("%s 解压后超过 %d 字节上限", entry.Name, limit)
	}
	return content, nil
}
