package llmmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"react-base-service/api/llm"
	"react-base-service/components"
	"react-base-service/components/params"
	model "react-base-service/models/llm"
	creditsService "react-base-service/service/credits"

	"react-base-service/golib/zlog"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// CheckConnectivity 连通性检测：构建 LLM 客户端并发送测试消息验证 API Key + 模型可用性
func CheckConnectivity(ctx *gin.Context, modelKey, modelVersion, apiKey string) error {
	return CheckConnectivityWithEndpoint(ctx, modelKey, modelVersion, apiKey, "")
}

// CheckConnectivityWithEndpoint 在 CheckConnectivity 基础上支持自定义接入面
//（模型配置面板「厂商与密钥」填写的 base url 优先于 api.yaml 全局端点）。
func CheckConnectivityWithEndpoint(ctx *gin.Context, modelKey, modelVersion, apiKey, apiURL string) error {
	// 校验 modelKey 合法性
	if !llm.IsValidModelKey(modelKey) {
		return components.ErrorUserModelCategoryNotSupported.Sprintf(modelKey)
	}

	client, err := llm.GetClientWithUserModelEndpoint(apiKey, modelKey, apiURL, 0)
	if err != nil {
		zlog.Errorf(ctx, "[user_model.CheckConnectivity] 构建客户端失败: modelKey=%s, err=%v", modelKey, err)
		return components.ErrorUserModelConnectivityFailed.Sprintf(fmt.Sprintf("构建客户端失败"))
	}

	// 使用带超时的 context 防止长时间阻塞
	testCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	chunkCh, err := client.ChatStream(testCtx, []llm.LLMMessage{
		{Role: "user", Content: "hi"},
	}, modelVersion)
	if err != nil {
		zlog.Errorf(ctx, "[user_model.CheckConnectivity] ChatStream 请求失败: modelKey=%s, modelVersion=%s, err=%v", modelKey, modelVersion, err)
		return components.ErrorUserModelConnectivityFailed.Sprintf(fmt.Sprintf("请求失败"))
	}

	// 读取流式响应，收到首个有效内容即视为连通成功
	for chunk := range chunkCh {
		if chunk.Error != nil {
			zlog.Errorf(ctx, "[user_model.CheckConnectivity] 响应错误: modelKey=%s, modelVersion=%s, err=%v", modelKey, modelVersion, chunk.Error)
			return components.ErrorUserModelConnectivityFailed.Sprintf(fmt.Sprintf("响应错误"))
		}
		if chunk.Content != "" {
			// 收到有效内容，连通成功
			return nil
		}
	}

	zlog.Errorf(ctx, "[user_model.CheckConnectivity] 未收到有效响应: modelKey=%s, modelVersion=%s", modelKey, modelVersion)
	return components.ErrorUserModelConnectivityFailed.Sprintf("未收到有效响应")
}

// CreateUserModel 创建用户模型
func CreateUserModel(ctx *gin.Context, userName string, req params.CreateUserModelReq) (*model.UserModel, error) {
	// 校验 model_key 为新枚举
	if !llm.IsValidModelKey(req.ModelKey) {
		return nil, components.ErrorUserModelCategoryNotSupported.Sprintf(req.ModelKey)
	}

	// 校验 biz_scenes 非空（binding:"min=1" 已保证，此处双重保险）
	if len(req.BizScenes) == 0 {
		return nil, components.ErrorUserModelInvalidBizScenes
	}

	// 将 biz_scenes 序列化为 JSON 字符串存储
	bizScenesJSON, err := json.Marshal(req.BizScenes)
	if err != nil {
		return nil, components.ErrorParamInvalid.Sprintf("bizScenes 序列化失败")
	}

	// 平台默认模型仅白名单用户可创建
	if req.IsPlatformDefault == 1 && !IsWhitelisted(userName) {
		return nil, components.ErrorUserModelNoPermission
	}

	// 平台默认模型的 user_name 填空字符串
	userNameForDB := userName
	if req.IsPlatformDefault == 1 {
		userNameForDB = ""
	}

	// 检查同用户下模型名唯一性
	exist, err := model.ExistUserModelByUserAndName(ctx, userNameForDB, req.ModelName)
	if err != nil {
		return nil, err
	}
	if exist {
		return nil, components.ErrorUserModelDuplicate.Sprintf(req.ModelName)
	}

	if err := validateModelCapacity(req.ContextTokens, req.MaxOutputTokens); err != nil {
		return nil, err
	}
	if err := validateCapabilityFlags(req.SupportThinking, req.SupportTools, req.SupportVision); err != nil {
		return nil, err
	}

	// 连通性检测
	if err := CheckConnectivityWithEndpoint(ctx, req.ModelKey, req.ModelVersion, req.ApiKey, req.ApiURL); err != nil {
		return nil, err
	}

	// 生成 model_hash 并创建记录
	m := &model.UserModel{
		ModelHash:         "model_" + strings.ReplaceAll(uuid.New().String(), "-", ""),
		UserName:          userNameForDB,
		ModelName:         req.ModelName,
		ModelKey:          req.ModelKey,
		ModelVersion:      req.ModelVersion,
		ApiKey:            req.ApiKey,
		BizScenes:         string(bizScenesJSON),
		ApiURL:            strings.TrimSpace(req.ApiURL),
		ContextTokens:     req.ContextTokens,
		MaxOutputTokens:   req.MaxOutputTokens,
		SupportThinking:   req.SupportThinking,
		SupportTools:      req.SupportTools,
		SupportVision:     req.SupportVision,
		IsPlatformDefault: req.IsPlatformDefault,
	}

	if err := model.CreateUserModel(ctx, m); err != nil {
		return nil, err
	}

	zlog.Infof(ctx, "[user_model.Create] 创建模型成功: modelHash=%s, userName=%s, modelName=%s, modelKey=%s",
		m.ModelHash, m.UserName, m.ModelName, m.ModelKey)

	return m, nil
}

// UpdateUserModel 编辑用户模型
func UpdateUserModel(ctx *gin.Context, userName string, req params.UpdateUserModelReq) error {
	// 查询模型
	record, err := model.GetUserModelByID(ctx, req.ID)
	if err != nil {
		return err
	}
	if record == nil {
		return components.ErrorUserModelNotFound.Sprintf(fmt.Sprintf("id=%d", req.ID))
	}

	// 权限校验：平台默认模型需白名单；普通模型需创建者本人
	if record.IsPlatformDefault == 1 {
		if !IsWhitelisted(userName) {
			return components.ErrorUserModelNoPermission
		}
	} else {
		if record.UserName != userName {
			return components.ErrorUserModelOwnerMismatch
		}
	}

	// 统一校验请求字段
	if err := validateUpdateReq(req); err != nil {
		return err
	}

	// 构建更新字段（通过与当前记录比较判断是否有变更）
	updates := make(map[string]interface{})
	needConnCheck := false

	if req.ModelName != record.ModelName {
		updates["model_name"] = req.ModelName
	}
	if req.ModelKey != record.ModelKey {
		updates["model_key"] = req.ModelKey
		needConnCheck = true
	}
	if req.ModelVersion != record.ModelVersion {
		updates["model_version"] = req.ModelVersion
		needConnCheck = true
	}
	if req.ApiKey != record.ApiKey {
		updates["api_key"] = model.EncryptAPIKey(req.ApiKey)
		needConnCheck = true
	}
	if req.ApiURL != record.ApiURL {
		updates["api_url"] = strings.TrimSpace(req.ApiURL)
		needConnCheck = true
	}
	if req.ContextTokens != record.ContextTokens {
		updates["context_tokens"] = req.ContextTokens
	}
	if req.MaxOutputTokens != record.MaxOutputTokens {
		updates["max_output_tokens"] = req.MaxOutputTokens
	}
	if req.SupportThinking != record.SupportThinking {
		updates["support_thinking"] = req.SupportThinking
	}
	if req.SupportTools != record.SupportTools {
		updates["support_tools"] = req.SupportTools
	}
	if req.SupportVision != record.SupportVision {
		updates["support_vision"] = req.SupportVision
	}
	bizScenesJSON, _ := json.Marshal(req.BizScenes)
	if string(bizScenesJSON) != record.BizScenes {
		updates["biz_scenes"] = string(bizScenesJSON)
	}

	// isPlatformDefault 变更处理（平台默认 ↔ 个人）
	if req.IsPlatformDefault != record.IsPlatformDefault {
		// 仅白名单用户可变更模型类型
		if !IsWhitelisted(userName) {
			return components.ErrorUserModelNoPermission
		}

		// 确定切换后的 user_name
		newUserName := ""
		if req.IsPlatformDefault == 0 {
			// 平台默认 → 个人：user_name 设为当前操作用户
			newUserName = userName
		}
		// 个人 → 平台默认：user_name 置空（newUserName 已为 ""）

		// 确定切换后的模型名称（可能同时修改了名称）
		newModelName := record.ModelName
		if _, ok := updates["model_name"]; ok {
			newModelName = req.ModelName
		}

		// 重名校验：切换后 user_name 范围变化，需确保不重名
		exist, existErr := model.ExistUserModelByUserAndName(ctx, newUserName, newModelName)
		if existErr != nil {
			return existErr
		}
		if exist {
			return components.ErrorUserModelDuplicate.Sprintf(newModelName)
		}

		updates["is_platform_default"] = req.IsPlatformDefault
		updates["user_name"] = newUserName
	}

	if len(updates) == 0 {
		return nil
	}

	// 如果涉及 apiKey/modelKey/modelVersion/apiURL 变更，重新做连通性检测
	if needConnCheck {
		checkKey := record.ModelKey
		if _, ok := updates["model_key"]; ok {
			checkKey = req.ModelKey
		}
		checkVersion := record.ModelVersion
		if _, ok := updates["model_version"]; ok {
			checkVersion = req.ModelVersion
		}
		checkApiKey := record.ApiKey
		if _, ok := updates["api_key"]; ok {
			checkApiKey = req.ApiKey
		}
		checkApiURL := record.ApiURL
		if _, ok := updates["api_url"]; ok {
			checkApiURL = req.ApiURL
		}
		if err := CheckConnectivityWithEndpoint(ctx, checkKey, checkVersion, checkApiKey, checkApiURL); err != nil {
			return err
		}
	}

	if err := model.UpdateUserModelByID(ctx, req.ID, updates); err != nil {
		return err
	}

	zlog.Infof(ctx, "[user_model.Update] 编辑模型成功: id=%d, userName=%s", req.ID, userName)
	return nil
}

// DeleteUserModel 删除用户模型
func DeleteUserModel(ctx *gin.Context, userName string, id uint) error {
	record, err := model.GetUserModelByID(ctx, id)
	if err != nil {
		return err
	}
	if record == nil {
		return components.ErrorUserModelNotFound.Sprintf(fmt.Sprintf("id=%d", id))
	}

	// 权限校验：创建者本人或白名单用户
	if record.UserName != userName && !IsWhitelisted(userName) {
		return components.ErrorUserModelOwnerMismatch
	}

	if err := model.SoftDeleteUserModelByID(ctx, id); err != nil {
		return err
	}

	zlog.Infof(ctx, "[user_model.Delete] 删除模型成功: id=%d, userName=%s, modelName=%s",
		id, userName, record.ModelName)
	return nil
}

// GetUserModelDetail 查询模型详情（API Key 不脱敏）
func GetUserModelDetail(ctx *gin.Context, userName string, id uint) (*params.UserModelItem, error) {
	record, err := model.GetUserModelByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, components.ErrorUserModelNotFound.Sprintf(fmt.Sprintf("id=%d", id))
	}

	// 权限校验：白名单用户可查看任意模型；普通用户仅能查看平台默认模型或自己创建的模型
	if !IsWhitelisted(userName) && record.IsPlatformDefault != 1 && record.UserName != userName {
		return nil, components.ErrorUserModelOwnerMismatch
	}

	var bizScenes []string
	_ = json.Unmarshal([]byte(record.BizScenes), &bizScenes)

	item := &params.UserModelItem{
		ID:                record.ID,
		ModelHash:         record.ModelHash,
		UserName:          record.UserName,
		ModelName:         record.ModelName,
		ModelKey:          record.ModelKey,
		ModelVersion:      record.ModelVersion,
		ApiKey:            record.ApiKey, // 不脱敏
		BizScenes:         bizScenes,
		ApiURL:            record.ApiURL,
		ContextTokens:     record.ContextTokens,
		MaxOutputTokens:   record.MaxOutputTokens,
		SupportThinking:   record.SupportThinking,
		SupportTools:      record.SupportTools,
		SupportVision:     record.SupportVision,
		IsPlatformDefault: record.IsPlatformDefault,
		CreatedAt:         record.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:         record.UpdatedAt.Format("2006-01-02 15:04:05"),
	}

	return item, nil
}

// ListUserModels 查询模型列表（白名单用户看全量，普通用户看平台默认 + 自己的）
func ListUserModels(ctx *gin.Context, userName string, req params.ListUserModelsReq) ([]params.UserModelItem, error) {
	var list []model.UserModel
	var err error

	if IsWhitelisted(userName) {
		// 白名单用户看全量
		if req.BizScene != "" {
			list, err = model.ListAllUserModelsByBizScene(ctx, req.BizScene)
		} else {
			list, err = model.ListAllUserModels(ctx)
		}
	} else {
		// 普通用户看平台默认 + 自己的
		if req.BizScene != "" {
			list, err = model.ListUserModelsByViewerAndBizScene(ctx, userName, req.BizScene)
		} else {
			list, err = model.ListUserModelsByViewer(ctx, userName)
		}
	}
	if err != nil {
		return nil, err
	}

	// 按模型名称模糊过滤
	if req.ModelName != "" {
		filtered := list[:0]
		for _, m := range list {
			if strings.Contains(m.ModelName, req.ModelName) {
				filtered = append(filtered, m)
			}
		}
		list = filtered
	}

	// 组装响应
	items := make([]params.UserModelItem, 0, len(list))
	for _, m := range list {
		var bizScenes []string
		_ = json.Unmarshal([]byte(m.BizScenes), &bizScenes)

		item := params.UserModelItem{
			ID:                m.ID,
			ModelHash:         m.ModelHash,
			UserName:          m.UserName,
			ModelName:         m.ModelName,
			ModelKey:          m.ModelKey,
			ModelVersion:      m.ModelVersion,
			ApiKey:            maskApiKey(m.ApiKey),
			BizScenes:         bizScenes,
			ApiURL:            m.ApiURL,
			ContextTokens:     m.ContextTokens,
			MaxOutputTokens:   m.MaxOutputTokens,
			SupportThinking:   m.SupportThinking,
			SupportTools:      m.SupportTools,
			SupportVision:     m.SupportVision,
			IsPlatformDefault: m.IsPlatformDefault,
			CreatedAt:         m.CreatedAt.Format("2006-01-02 15:04:05"),
			UpdatedAt:         m.UpdatedAt.Format("2006-01-02 15:04:05"),
		}

		// 平台默认模型返回积分信息
		if m.IsPlatformDefault == 1 {
			baseCredits, initErr := creditsService.GetOrInit(ctx, userName, m.ModelHash)
			if initErr != nil {
				zlog.Errorf(ctx, "[user_model.List] 获取基础积分失败: modelHash=%s, err=%v", m.ModelHash, initErr)
			} else {
				item.BaseCredits = &baseCredits
			}

			bonusRecord, bonusErr := model.GetBonusCredits(ctx, userName, m.ModelHash)
			if bonusErr != nil {
				zlog.Errorf(ctx, "[user_model.List] 获取赠送积分失败: modelHash=%s, err=%v", m.ModelHash, bonusErr)
			} else {
				bonusCredits := 0
				if bonusRecord != nil {
					bonusCredits = bonusRecord.Credits
				}
				item.BonusCredits = &bonusCredits
			}
		}

		items = append(items, item)
	}

	return items, nil
}

// validateUpdateReq 统一校验 UpdateUserModelReq 各字段
func validateUpdateReq(req params.UpdateUserModelReq) error {
	if req.ModelName == "" {
		return components.ErrorParamInvalid.Sprintf("modelName 不能为空")
	}
	if req.ModelKey == "" {
		return components.ErrorParamInvalid.Sprintf("modelKey 不能为空")
	}
	if !llm.IsValidModelKey(req.ModelKey) {
		return components.ErrorUserModelCategoryNotSupported.Sprintf(req.ModelKey)
	}
	if req.ModelVersion == "" {
		return components.ErrorParamInvalid.Sprintf("modelVersion 不能为空")
	}
	if req.ApiKey == "" {
		return components.ErrorParamInvalid.Sprintf("apiKey 不能为空")
	}
	if len(req.BizScenes) == 0 {
		return components.ErrorUserModelInvalidBizScenes
	}
	if req.IsPlatformDefault != 0 && req.IsPlatformDefault != 1 {
		return components.ErrorParamInvalid.Sprintf("isPlatformDefault 值只能为 0 或 1")
	}
	if err := validateModelCapacity(req.ContextTokens, req.MaxOutputTokens); err != nil {
		return err
	}
	if err := validateCapabilityFlags(req.SupportThinking, req.SupportTools, req.SupportVision); err != nil {
		return err
	}
	return nil
}

// validateModelCapacity 校验上下文容量/最大输出（模型配置面板「模型与参数」范围口径）。
func validateModelCapacity(contextTokens, maxOutputTokens int) error {
	if contextTokens < 0 || contextTokens > maxModelContextTokensLimit {
		return components.ErrorParamInvalid.Sprintf("contextTokens 取值范围 0-%d", maxModelContextTokensLimit)
	}
	if maxOutputTokens < 0 || maxOutputTokens > maxModelOutputTokensLimit {
		return components.ErrorParamInvalid.Sprintf("maxOutputTokens 取值范围 0-%d", maxModelOutputTokensLimit)
	}
	return nil
}

// validateCapabilityFlags 校验能力开关取值（0/1）。
func validateCapabilityFlags(flags ...int) error {
	for _, flag := range flags {
		if flag != 0 && flag != 1 {
			return components.ErrorParamInvalid.Sprintf("能力开关取值只能为 0 或 1")
		}
	}
	return nil
}

// maskApiKey API Key 脱敏：前4位 + **** + 后4位，总长 <=8 则全显 ****
func maskApiKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}
