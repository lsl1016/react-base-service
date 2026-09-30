package react

// web_search：内置网页检索工具（WP4，方案 docs/参考ZCode的运行时增强方案.md）。
//
// 实现形态与方案的分歧说明：方案首选 provider 原生搜索（模型能力位 + 服务端工具配置下发），
// 需要逐 provider 改协议层且无法本地验证，暂缓；本版经外部搜索服务（SearXNG JSON API，
// 自建实例免密钥）检索——模型侧获得同一能力，服务端零爬虫，全文内容用 web_fetch 抓取。
// provider 原生路径作为后续演进（届时按模型能力位注册，本工具不动）。
//
// 安全边界：目标服务地址来自管理员配置（非模型输入），无 SSRF 面；APIKey 仅透传给
// 配置的搜索服务；结果只含 title/url/snippet 文本，url 供模型用 web_fetch 跟进。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	llm "react-base-service/api/llm"
	"react-base-service/conf"

	"react-base-service/golib/zlog"
)

const metaToolWebSearch = "web_search"

// webSearchProfileEnabled 是执行档案侧的门控：生效开关（合并管理面板 DB 覆盖）与配置完整性
// 同时满足才允许执行（与 internalMetaToolDefinitions 的注册门控同口径，半配置状态不注册也不可执行）。
func webSearchProfileEnabled() bool {
	cfg := conf.GetReactRuntimeConfig().WebSearch
	return cfg.WebSearchEnabled() && cfg.WebSearchConfigured()
}

// webSearchMaxOutputRunes 是检索结果回填的字符上限（结果清单本身体积可控，超出按 rune 截断）。
const webSearchMaxOutputRunes = 6000

func webSearchToolDefinition() llm.ToolDefinition {
	return objectTool(metaToolWebSearch,
		"搜索公开网页，返回标题、链接与摘要清单（按相关度排序）。适用于查资料、找文档、了解时效性信息等；"+
			"需要某个结果的完整内容时，用 web_fetch 抓取对应 url。仅支持公开网页检索，不能搜索内网系统。",
		map[string]interface{}{
			"query":       stringSchema("搜索关键词。与通用搜索引擎相同的写法；面向中文内容时直接用中文。"),
			"max_results": numberSchema("返回条数上限，可选，默认 8。"),
		})
}

// executeWebSearch 执行 web_search：调用配置的搜索服务 → 解析结果 → 渲染为清单。
// 第二返回值 meta 是 UI 旁路（结构化结果清单，前端渲染为可点击链接），不进模型上下文。
func (s *reactEngineState) executeWebSearch(input json.RawMessage) (string, json.RawMessage, bool, error) {
	var req struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	_ = json.Unmarshal(input, &req)
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return "", nil, true, fmt.Errorf("query is required")
	}
	// 取运行时生效配置（合并管理面板「运行时配置」的 DB 覆盖，与注册门控同口径）。
	cfg := conf.GetReactRuntimeConfig().WebSearch
	if !cfg.WebSearchConfigured() {
		return "", nil, true, fmt.Errorf("web_search 未配置或配置不完整（kind=searxng + base_url 必填）")
	}
	maxResults := cfg.EffectiveMaxResults()
	if req.MaxResults > 0 && req.MaxResults < maxResults {
		maxResults = req.MaxResults
	}
	if maxResults > 20 {
		maxResults = 20
	}

	results, err := searxngSearch(s.ctx.Request.Context(), cfg, query, maxResults)
	if err != nil {
		return "", nil, true, fmt.Errorf("web_search 检索失败: %v", err)
	}
	if len(results) == 0 {
		return fmt.Sprintf("web_search 未找到与 %q 相关的结果。建议换关键词、拆短查询或改用英文重试。", query), nil, false, nil
	}
	rendered := renderWebSearchResults(query, results)
	zlog.Infof(s.ctx, "[React.WebSearch] 检索完成: runId=%s, query=%q, results=%d", s.runID, query, len(results))
	meta, _ := json.Marshal(map[string]interface{}{"webSearchResults": results})
	return rendered, meta, false, nil
}

// webSearchResult 是单条检索结果（解析与渲染的中间结构）。
type webSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// searxngSearch 调用 SearXNG JSON API：GET {base}/search?q=...&format=json&language=zh-CN。
func searxngSearch(ctx context.Context, cfg conf.ReactWebSearchConfig, query string, maxResults int) ([]webSearchResult, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	searchURL, err := url.Parse(base + "/search")
	if err != nil {
		return nil, fmt.Errorf("base_url 非法: %v", err)
	}
	params := searchURL.Query()
	params.Set("q", query)
	params.Set("format", "json")
	params.Set("language", "zh-CN")
	searchURL.RawQuery = params.Encode()

	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.EffectiveTimeoutSec())*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(requestCtx, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := webFetchHTTPClient().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("搜索服务返回状态 %d（自建 SearXNG 需在 settings.yml 开启 json format）", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxDownloadBytes))
	if err != nil {
		return nil, err
	}
	return parseSearxngResponse(body, maxResults)
}

// parseSearxngResponse 解析 SearXNG JSON 响应（{results:[{title,url,content}]}），纯函数便于单测。
func parseSearxngResponse(body []byte, maxResults int) ([]webSearchResult, error) {
	var payload struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("搜索服务响应解析失败: %v", err)
	}
	results := make([]webSearchResult, 0, maxResults)
	for _, item := range payload.Results {
		title := strings.TrimSpace(item.Title)
		link := strings.TrimSpace(item.URL)
		if title == "" || link == "" {
			continue
		}
		results = append(results, webSearchResult{
			Title:   title,
			URL:     link,
			Snippet: truncateRunes(strings.TrimSpace(item.Content), 300),
		})
		if len(results) >= maxResults {
			break
		}
	}
	return results, nil
}

// renderWebSearchResults 把结果渲染为模型可读清单（纯函数便于单测）。
func renderWebSearchResults(query string, results []webSearchResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "web_search %q 共 %d 条结果：\n", query, len(results))
	for i, item := range results {
		fmt.Fprintf(&sb, "%d. %s\n   url: %s\n", i+1, item.Title, item.URL)
		if item.Snippet != "" {
			fmt.Fprintf(&sb, "   摘要: %s\n", item.Snippet)
		}
	}
	sb.WriteString("需要某条结果的完整内容时，用 web_fetch 抓取对应 url。")
	rendered := sb.String()
	if runeCount := len([]rune(rendered)); runeCount > webSearchMaxOutputRunes {
		rendered = headRunes(rendered, webSearchMaxOutputRunes) + "\n…（结果过长已截断）"
	}
	return rendered
}
