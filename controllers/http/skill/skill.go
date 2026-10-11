package skill

import (
	"io"
	"strings"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/helpers"
	skillService "react-base-service/service/skill"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
)

// CreateSkill 创建 Skill
// @Summary      创建 Skill
// @Description  创建指定 app 下的 Skill 配置。
// @Tags         skill
// @Accept       json
// @Produce      json
// @Param        req  body     params.CreateSkillReq  true  "创建 Skill 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SkillResp}  "成功返回 Skill 详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或创建失败"
// @Router       /skill/create [post]
func CreateSkill(ctx *gin.Context) {
	var req params.CreateSkillReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Skill.Create] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	userName := helpers.GetUserName(ctx)
	s, err := skillService.CreateSkill(ctx, &req, userName)
	if err != nil {
		zlog.Errorf(ctx, "[Skill.Create] 创建失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, skillService.ToSkillResp(s))
}

// UpdateSkill 更新 Skill
// @Summary      更新 Skill
// @Description  根据 skillId 更新 Skill 的配置内容或状态。
// @Tags         skill
// @Accept       json
// @Produce      json
// @Param        req  body     params.UpdateSkillReq  true  "更新 Skill 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SkillResp}  "更新成功并返回 Skill 详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或更新失败"
// @Router       /skill/update [post]
func UpdateSkill(ctx *gin.Context) {
	var req params.UpdateSkillReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Skill.Update] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	s, err := skillService.UpdateSkill(ctx, &req)
	if err != nil {
		zlog.Errorf(ctx, "[Skill.Update] 更新失败: skillId=%s, err=%v", req.SkillID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, skillService.ToSkillResp(s))
}

// DeleteSkill 删除 Skill
// @Summary      删除 Skill
// @Description  根据 skillId 删除指定 Skill。
// @Tags         skill
// @Accept       json
// @Produce      json
// @Param        req  body     params.DeleteSkillReq  true  "删除 Skill 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SkillResp}  "删除成功并返回 Skill 详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或删除失败"
// @Router       /skill/delete [post]
func DeleteSkill(ctx *gin.Context) {
	var req params.DeleteSkillReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Skill.Delete] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	s, err := skillService.DeleteSkill(ctx, req.SkillID)
	if err != nil {
		zlog.Errorf(ctx, "[Skill.Delete] 删除失败: skillId=%s, err=%v", req.SkillID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, skillService.ToSkillResp(s))
}

// ListSkills 查询 Skill 列表
// @Summary      查询 Skill 列表
// @Description  根据 appKey 查询该调用方下的 Skill 列表；传入 routeValues 时按 routeValues 层级前缀返回对应列表，未传时兼容旧逻辑返回 appKey 下全部列表。
// @Tags         skill
// @Accept       json
// @Produce      json
// @Param        req  body     params.ListSkillsReq  true  "查询 Skill 列表请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.SkillResp}  "成功返回 Skill 列表"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或查询失败"
// @Router       /skill/list [post]
func ListSkills(ctx *gin.Context) {
	var req params.ListSkillsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Skill.List] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	skills, err := skillService.ListByAppAndRoute(ctx, req.AppKey, req.RouteValues)
	if err != nil {
		zlog.Errorf(ctx, "[Skill.List] 查询失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}

	resp := make([]params.SkillResp, 0, len(skills))
	for i := range skills {
		resp = append(resp, skillService.ToSkillResp(&skills[i]))
	}
	components.RenderJsonSucc(ctx, resp)
}

// GetSkillDetail 查询 Skill 详情
// @Summary      查询 Skill 详情
// @Description  根据 skillId 查询单个 Skill 的详细配置。
// @Tags         skill
// @Accept       json
// @Produce      json
// @Param        req  body     params.SkillDetailReq  true  "查询 Skill 详情请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SkillResp}  "成功返回 Skill 详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "参数校验失败或查询失败"
// @Router       /skill/detail [post]
func GetSkillDetail(ctx *gin.Context) {
	var req params.SkillDetailReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Skill.Detail] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	s, err := skillService.GetDetail(ctx, req.SkillID)
	if err != nil {
		zlog.Errorf(ctx, "[Skill.Detail] 查询失败: skillId=%s, err=%v", req.SkillID, err)
		components.RenderJsonFail(ctx, err)
		return
	}

	components.RenderJsonSucc(ctx, skillService.ToSkillResp(s))
}

// ImportSkill 导入 SKILL.md 定义（粘贴）
// @Summary      导入 SKILL.md 定义（粘贴）
// @Description  解析「frontmatter + 正文」格式的 SKILL.md 并入库；正文入 content，triggers 关键词命中时在 run 装配期注入提示。同 app+name 同名覆盖。
// @Tags         skill
// @Accept       json
// @Produce      json
// @Param        req  body     params.ImportSkillReq  true  "导入 Skill 请求体"
// @Success      200  {object} components.DefaultRenderWithTrace{data=params.SkillResp}  "导入成功并返回 Skill 详情"
// @Failure      400  {object} components.DefaultRenderWithTrace  "定义文件非法或导入失败"
// @Router       /skill/import [post]
func ImportSkill(ctx *gin.Context) {
	var req params.ImportSkillReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		zlog.Errorf(ctx, "[Skill.Import] 请求参数绑定失败: %v", err)
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf(err.Error()))
		return
	}

	s, err := skillService.ImportFromMarkdown(ctx, &req, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[Skill.Import] 导入失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, skillService.ToSkillResp(s))
}

// ImportSkillZip 批量导入 zip 包内的 SKILL.md
// @Summary      批量导入 zip 包内的 SKILL.md
// @Description  multipart 上传 zip（表单字段 file；可选 appKey/routeValues/status 作为各 SKILL.md frontmatter 缺省兜底），遍历包内所有 SKILL.md 逐个导入，返回逐条结果（best-effort，单项失败不影响其余）。
// @Tags         skill
// @Accept       multipart/form-data
// @Produce      json
// @Param        file        formData file   true  "zip 包（内含若干 <skill目录>/SKILL.md）"
// @Param        appKey   formData string false "兜底 appKey"
// @Param        routeValues formData string false "兜底 routeValues（逗号分隔）"
// @Success      200  {object} components.DefaultRenderWithTrace{data=[]params.SkillImportItemResp}  "逐条导入结果"
// @Failure      400  {object} components.DefaultRenderWithTrace  "上传文件缺失或 zip 非法"
// @Router       /skill/import_zip [post]
func ImportSkillZip(ctx *gin.Context) {
	fileHeader, err := ctx.FormFile("file")
	if err != nil {
		components.RenderJsonFail(ctx, components.ErrorParamInvalid.Sprintf("缺少上传文件（表单字段 file）"))
		return
	}

	base := params.ImportSkillReq{
		AppKey: ctx.PostForm("appKey"),
	}
	if routeValuesText := strings.TrimSpace(ctx.PostForm("routeValues")); routeValuesText != "" {
		for _, value := range strings.Split(routeValuesText, ",") {
			if value = strings.TrimSpace(value); value != "" {
				base.RouteValues = append(base.RouteValues, value)
			}
		}
	}

	file, err := fileHeader.Open()
	if err != nil {
		components.RenderJsonFail(ctx, components.ErrorSkillImportInvalid.Sprintf("打开上传文件失败: %v", err))
		return
	}
	defer file.Close()
	zipBytes, err := io.ReadAll(io.LimitReader(file, skillService.MaxSkillZipBytes+1))
	if err != nil {
		components.RenderJsonFail(ctx, components.ErrorSkillImportInvalid.Sprintf("读取上传文件失败: %v", err))
		return
	}
	if len(zipBytes) > skillService.MaxSkillZipBytes {
		components.RenderJsonFail(ctx, components.ErrorSkillImportInvalid.Sprintf("zip 包超过大小上限 %d 字节", skillService.MaxSkillZipBytes))
		return
	}

	items, err := skillService.ImportFromZip(ctx, zipBytes, &base, helpers.GetUserName(ctx))
	if err != nil {
		zlog.Errorf(ctx, "[Skill.ImportZip] 导入失败: %v", err)
		components.RenderJsonFail(ctx, err)
		return
	}
	components.RenderJsonSucc(ctx, items)
}
