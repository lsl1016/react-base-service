package llm

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

// 模型能力目录：按模型 ID 声明 tools/thinking/vision 能力，供两处消费——
//  1. 连接「拉取模型」批量启用时自动填充能力位（fetch_models 响应）；
//  2. 运行时门禁的回退（用户模型未显式声明时）。
// 目录是本地种子数据（capability_catalog.json），声明可被每模型的能力开关覆盖。

//go:embed capability_catalog.json
var capabilityCatalogJSON []byte

// ModelCapabilities 模型能力声明。Known 表示本次查找是否命中目录（未命中时其余字段零值）。
type ModelCapabilities struct {
	Known            bool
	ContextTokens    int
	MaxOutputTokens  int
	SupportTools     bool
	SupportThinking  bool
	SupportVision    bool
}

type capabilityCatalogFile struct {
	Exact    map[string]ModelCapabilities       `json:"exact"`
	Prefixes []capabilityPrefixRule             `json:"prefixes"`
}

type capabilityPrefixRule struct {
	Prefix string            `json:"prefix"`
	Caps   ModelCapabilities `json:"caps"`
}

// rawCaps 是 JSON 反序列化的中间结构（Known 由命中与否决定，不由文件声明）。
type rawCaps struct {
	ContextTokens   int  `json:"contextTokens"`
	MaxOutputTokens int  `json:"maxOutputTokens"`
	Tools           bool `json:"tools"`
	Thinking        bool `json:"thinking"`
	Vision          bool `json:"vision"`
}

type rawCatalogFile struct {
	Exact    map[string]rawCaps     `json:"exact"`
	Prefixes []rawPrefixRule        `json:"prefixes"`
}

type rawPrefixRule struct {
	Prefix string  `json:"prefix"`
	Caps   rawCaps `json:"caps"`
}

var capabilityCatalog = func() *capabilityCatalogFile {
	var raw rawCatalogFile
	if err := json.Unmarshal(capabilityCatalogJSON, &raw); err != nil {
		// 内嵌文件随仓库走，解析失败属于构建期问题；运行期退化为空目录（全部未知）。
		return &capabilityCatalogFile{}
	}
	catalog := &capabilityCatalogFile{
		Exact:    make(map[string]ModelCapabilities, len(raw.Exact)),
		Prefixes: make([]capabilityPrefixRule, 0, len(raw.Prefixes)),
	}
	for id, caps := range raw.Exact {
		catalog.Exact[strings.ToLower(strings.TrimSpace(id))] = rawCapsToCaps(caps)
	}
	for _, rule := range raw.Prefixes {
		catalog.Prefixes = append(catalog.Prefixes, capabilityPrefixRule{
			Prefix: strings.ToLower(strings.TrimSpace(rule.Prefix)),
			Caps:   rawCapsToCaps(rule.Caps),
		})
	}
	return catalog
}()

func rawCapsToCaps(caps rawCaps) ModelCapabilities {
	return ModelCapabilities{
		ContextTokens:   caps.ContextTokens,
		MaxOutputTokens: caps.MaxOutputTokens,
		SupportTools:    caps.Tools,
		SupportThinking: caps.Thinking,
		SupportVision:   caps.Vision,
	}
}

// LookupModelCapabilities 按模型 ID 查能力目录：exact 全等命中优先，否则按 prefixes
// 顺序取首个前缀命中（大小写不敏感）。未命中返回 Known=false。
func LookupModelCapabilities(modelID string) ModelCapabilities {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return ModelCapabilities{}
	}
	if caps, ok := capabilityCatalog.Exact[id]; ok {
		caps.Known = true
		return caps
	}
	for _, rule := range capabilityCatalog.Prefixes {
		if rule.Prefix != "" && strings.HasPrefix(id, rule.Prefix) {
			caps := rule.Caps
			caps.Known = true
			return caps
		}
	}
	return ModelCapabilities{}
}

// ─── 运行时能力门禁的 context 传递 ───────────────────────────────
// 与 WithReasoning 同一套机制：引擎按「本次调用的是否主模型」注入用户模型能力声明，
// 协议 client（claude/gpt）在请求组装处消费——声明关闭 thinking 则不下发思考参数，
// 声明不支持 vision 则拒绝图片类 FilePayload。

type modelCapabilitiesContextKey struct{}

// WithModelCapabilities 把能力声明挂入 ctx（Known=false 的声明等价于不挂）。
func WithModelCapabilities(ctx context.Context, caps ModelCapabilities) context.Context {
	if ctx == nil || !caps.Known {
		return ctx
	}
	return context.WithValue(ctx, modelCapabilitiesContextKey{}, caps)
}

// ModelCapabilitiesFromContext 取 ctx 里的能力声明（未挂载时 Known=false）。
func ModelCapabilitiesFromContext(ctx context.Context) ModelCapabilities {
	if ctx == nil {
		return ModelCapabilities{}
	}
	if caps, ok := ctx.Value(modelCapabilitiesContextKey{}).(ModelCapabilities); ok {
		return caps
	}
	return ModelCapabilities{}
}

// rejectVisionFilesForCaps 能力门禁：run 声明模型不支持视觉输入时，拒绝 image/* 载荷
//（文件入模边界，供 claude/gpt client 的 ChatStreamWithFilePayloads 共用）。
func rejectVisionFilesForCaps(ctx context.Context, files []FilePayload) error {
	caps := ModelCapabilitiesFromContext(ctx)
	if !caps.Known || caps.SupportVision {
		return nil
	}
	for _, f := range files {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(f.MediaType)), "image/") {
			return fmt.Errorf("model capability gate: vision input not supported, rejected image file %s", f.FileName)
		}
	}
	return nil
}
