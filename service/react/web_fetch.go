package react

// web_fetch：内置网页抓取工具（参考 ZCode WebFetch，方案 docs/参考ZCode的运行时增强方案.md WP4）。
//
// 与 ZCode 的差异：ZCode 抓取后用小快模型按 prompt 代答；本版先做"抓取 → 正文提取 → 按预算截断"，
// 完整正文经 resultRef 落库、read_tool_result 可分页续读，LLM 代答形态后续按需叠加。
//
// 安全边界：
//   - 仅允许 http/https，拒绝内网/环回地址字面量（DNS rebinding 类 SSRF 不在本版防护范围，
//     生产环境建议经 egress 代理）；
//   - 响应体 2MB 硬上限（io.LimitReader），Content-Type 仅接受文本类（html/plain/json 等）；
//   - 工具元数据 SideEffect=network、ReadOnly=true（对目标站点无副作用）。
//
// 缓存：进程内 map + TTL（conf web_fetch.cache_ttl_sec，默认 15 分钟），同 run 重复抓同一 URL 零成本。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	llm "react-base-service/api/llm"
	"react-base-service/conf"

	"react-base-service/golib/zlog"
)

const (
	metaToolWebFetch = "web_fetch"

	// webFetchMaxDownloadBytes 是响应体下载硬上限（超限直接截断读取，防止内存失控）。
	webFetchMaxDownloadBytes = 2 * 1024 * 1024
	// webFetchCacheMaxEntries 是进程内缓存的条目上限（LRU 简化为超限整体清空，抓取是幂等只读操作）。
	webFetchCacheMaxEntries = 32
)

// webFetchToolDefinition 构造 web_fetch 工具声明。
func webFetchToolDefinition() llm.ToolDefinition {
	return objectTool(metaToolWebFetch,
		"抓取一个公开网页（http/https）并提取正文文本回填。适用于查阅文档、公告、博客等公开页面；"+
			"返回内容为正文提取结果（HTML 已去除脚本/样式与标签），超出预算的部分保留在 resultRef 中，"+
			"可用 read_tool_result 分页续读。不能访问需要登录的内网系统；对结构化 JSON 接口返回原始文本。",
		map[string]interface{}{
			"url": stringSchema("要抓取的页面地址（http/https），必须是完整 URL。"),
		})
}

// executeWebFetch 执行 web_fetch：解析 URL → 查缓存 → 抓取 → 提取正文 → 按预算截断。
func (s *reactEngineState) executeWebFetch(input json.RawMessage) (string, bool, error) {
	var req struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(input, &req)
	rawURL := strings.TrimSpace(req.URL)
	if rawURL == "" {
		return "", true, fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", true, fmt.Errorf("web_fetch 仅支持完整的 http/https URL: %s", rawURL)
	}
	if host, _, splitErr := net.SplitHostPort(parsed.Host); splitErr == nil {
		parsed.Host = host
	}
	if isPrivateHost(parsed.Hostname()) {
		return "", true, fmt.Errorf("web_fetch 拒绝访问内网/环回地址: %s", parsed.Hostname())
	}

	cfg := conf.CustomConf.LLM.React.WebFetch
	if content, ok := webFetchCacheGet(rawURL, cfg.EffectiveCacheTTLSec()); ok {
		return content, false, nil
	}

	fetchCtx, cancel := context.WithTimeout(s.ctx.Request.Context(), time.Duration(cfg.EffectiveTimeoutSec())*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", true, fmt.Errorf("web_fetch URL 非法: %v", err)
	}
	httpReq.Header.Set("User-Agent", "react-base-service-webfetch/1.0")
	httpReq.Header.Set("Accept", "text/html,application/json,text/plain;q=0.9,*/*;q=0.5")

	resp, err := webFetchHTTPClient().Do(httpReq)
	if err != nil {
		return "", true, fmt.Errorf("web_fetch 请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", true, fmt.Errorf("web_fetch 目标返回非 2xx 状态: %d", resp.StatusCode)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if !isWebFetchSupportedContent(contentType) {
		return "", true, fmt.Errorf("web_fetch 不支持的内容类型: %s（仅支持文本类页面）", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, webFetchMaxDownloadBytes))
	if err != nil {
		return "", true, fmt.Errorf("web_fetch 读取响应失败: %v", err)
	}

	content := extractWebFetchContent(contentType, string(body))
	if maxRunes := cfg.EffectiveMaxContentRunes(); maxRunes > 0 && utf8.RuneCountInString(content) > maxRunes {
		content = headRunes(content, maxRunes)
	}
	if strings.TrimSpace(content) == "" {
		return "", true, fmt.Errorf("web_fetch 未提取到正文内容（页面可能为空或纯脚本渲染）")
	}
	webFetchCachePut(rawURL, content)
	zlog.Infof(s.ctx, "[React.WebFetch] 抓取完成: runId=%s, url=%s, runes=%d", s.runID, rawURL, utf8.RuneCountInString(content))
	return content, false, nil
}

// webFetchHTTPClient 返回共享抓取客户端：禁用 cookie、限制重定向次数，不跟随到内网的跳转由字面量检查兜底。
func webFetchHTTPClient() *http.Client {
	client := &http.Client{
		Timeout: 2 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("web_fetch 重定向次数过多")
			}
			if host := req.URL.Hostname(); isPrivateHost(host) {
				return fmt.Errorf("web_fetch 重定向拒绝访问内网地址: %s", host)
			}
			return nil
		},
	}
	return client
}

var webFetchPrivateNets = []net.IPNet{
	{IP: net.IPv4(10, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(172, 16, 0, 0), Mask: net.CIDRMask(12, 32)},
	{IP: net.IPv4(192, 168, 0, 0), Mask: net.CIDRMask(16, 32)},
	{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(169, 254, 0, 0), Mask: net.CIDRMask(16, 32)},
	{IP: net.IPv4(0, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
}

// isPrivateHost 拒绝环回、私网、链路本地地址字面量与明显的本地主机名。
func isPrivateHost(hostname string) bool {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || strings.HasSuffix(hostname, ".local") || strings.HasSuffix(hostname, ".internal") {
		return true
	}
	ip := net.ParseIP(hostname)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	if ipV4 := ip.To4(); ipV4 != nil {
		for _, private := range webFetchPrivateNets {
			if private.Contains(ipV4) {
				return true
			}
		}
	}
	return false
}

func isWebFetchSupportedContent(contentType string) bool {
	if contentType == "" {
		return true
	}
	switch {
	case strings.HasPrefix(contentType, "text/"):
		return true
	case strings.HasPrefix(contentType, "application/json"), strings.HasPrefix(contentType, "application/xml"),
		strings.HasPrefix(contentType, "application/javascript"), strings.HasPrefix(contentType, "application/xhtml"),
		strings.HasSuffix(contentType, "+json"), strings.HasSuffix(contentType, "+xml"):
		return true
	default:
		return false
	}
}

// webFetchScriptRe 匹配脚本/样式等无正文价值块（RE2 不支持反向引用，闭合标签枚举同名单；
// 少数错配标签只影响提取质量，不影响正确性）。
var webFetchScriptRe = regexp.MustCompile(`(?is)<(script|style|noscript|svg|iframe|template)\b[^>]*>.*?</(script|style|noscript|svg|iframe|template)\s*>`)

var (
	webFetchCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	webFetchTagRe     = regexp.MustCompile(`(?s)<[^>]+>`)
	webFetchSpaceRe   = regexp.MustCompile(`[ \t]+`)
	webFetchBlankRe   = regexp.MustCompile(`\n{3,}`)
)

// extractWebFetchContent 把响应体转为可读文本：HTML 去脚本/样式/标签后压缩空白；其余文本类原样返回。
func extractWebFetchContent(contentType, body string) string {
	if !strings.Contains(contentType, "html") && !strings.Contains(contentType, "xml") {
		return strings.TrimSpace(body)
	}
	text := webFetchScriptRe.ReplaceAllString(body, " ")
	text = webFetchCommentRe.ReplaceAllString(text, "")
	// 块级标签换行，让段落结构可读。
	text = regexp.MustCompile(`(?i)</(p|div|li|tr|h[1-6]|section|article|br)\s*>`).ReplaceAllString(text, "\n")
	text = webFetchTagRe.ReplaceAllString(text, " ")
	text = htmlUnescape(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(webFetchSpaceRe.ReplaceAllString(line, " "))
	}
	cleaned := webFetchBlankRe.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(cleaned)
}

// htmlUnescape 转译 HTML 实体（stdlib html 包按需内联，避免只为一个函数引入依赖面）。
func htmlUnescape(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	replacer := strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"",
		"&#39;", "'", "&apos;", "'", "&copy;", "©", "&reg;", "®", "&mdash;", "—",
		"&ndash;", "–", "&hellip;", "…", "&middot;", "·", "&laquo;", "«", "&raquo;", "»",
	)
	text = replacer.Replace(text)
	// 数字实体统一交给 html 包语义：这里仅处理常见十进制/十六进制形式。
	return numericEntityRe.ReplaceAllStringFunc(text, func(match string) string {
		var code int
		if strings.HasPrefix(match, "&#x") || strings.HasPrefix(match, "&#X") {
			fmt.Sscanf(match, "&#x%x;", &code)
		} else {
			fmt.Sscanf(match, "&#%d;", &code)
		}
		if code <= 0 || code > utf8.MaxRune {
			return match
		}
		return string(rune(code))
	})
}

var numericEntityRe = regexp.MustCompile(`&#(x[0-9a-fA-F]+|[0-9]+);`)

// ---------- 进程内抓取缓存 ----------

type webFetchCacheEntry struct {
	content   string
	fetchedAt time.Time
}

var (
	webFetchCacheMu    sync.Mutex
	webFetchCacheStore = map[string]webFetchCacheEntry{}
)

// webFetchCacheKey 对 URL 归一哈希，避免超长 URL 作 map key。
func webFetchCacheKey(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:])
}

func webFetchCacheGet(rawURL string, ttlSec int) (string, bool) {
	if ttlSec < 0 {
		return "", false
	}
	webFetchCacheMu.Lock()
	defer webFetchCacheMu.Unlock()
	entry, ok := webFetchCacheStore[webFetchCacheKey(rawURL)]
	if !ok || time.Since(entry.fetchedAt) > time.Duration(ttlSec)*time.Second {
		return "", false
	}
	return entry.content, true
}

func webFetchCachePut(rawURL, content string) {
	webFetchCacheMu.Lock()
	defer webFetchCacheMu.Unlock()
	if len(webFetchCacheStore) >= webFetchCacheMaxEntries {
		for key := range webFetchCacheStore {
			delete(webFetchCacheStore, key)
			if len(webFetchCacheStore) < webFetchCacheMaxEntries/2 {
				break
			}
		}
	}
	webFetchCacheStore[webFetchCacheKey(rawURL)] = webFetchCacheEntry{content: content, fetchedAt: time.Now()}
}
