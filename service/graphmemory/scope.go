package graphmemory

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
)

// Graphiti 校验 group_id：^[a-zA-Z0-9_-]+$（空串为默认组）。但 FalkorDB 的全文检索
// 解析器不接受组 ID 中的连字符（实测 "(@group_id:\"demo-app\")" 直接语法错误，向量路径不受影响），
// 因此本基座生成组 ID 时把 `-` 一并视为非法，只保留 [a-zA-Z0-9_]；
// 清洗造成信息损失时追加内容哈希后缀，防不同原文清洗后碰撞（如两个等长中文名）；
// 超长截断防索引键膨胀。
const (
	groupIDMaxLen    = 64
	groupIDHashLen   = 8
	groupIDSeparator = "__"
)

var groupIDInvalidChars = regexp.MustCompile(`[^a-zA-Z0-9_]`)

// SanitizeGroupID 把任意标识归一为合法 group_id 片段：非法字符→_，有损失时追加哈希后缀，
// 超长截断（哈希后缀保留在尾部），空串回落 default。
func SanitizeGroupID(part string) string {
	trimmed := strings.TrimSpace(part)
	sanitized := groupIDInvalidChars.ReplaceAllString(trimmed, "_")
	if sanitized != trimmed {
		sum := sha256.Sum256([]byte(trimmed))
		base := strings.TrimRight(sanitized, "_")
		if len(base) > groupIDMaxLen-groupIDHashLen-1 {
			base = base[:groupIDMaxLen-groupIDHashLen-1]
		}
		sanitized = base + "_" + hex.EncodeToString(sum[:])[:groupIDHashLen]
	}
	sanitized = strings.TrimLeft(sanitized, "_-")
	if len(sanitized) > groupIDMaxLen {
		sanitized = sanitized[:groupIDMaxLen]
	}
	if sanitized == "" {
		return "default"
	}
	return sanitized
}

// AppGroupID 构造 app 级组：该 app 全部会话共享的图谱分区。
func AppGroupID(appKey string) string {
	return SanitizeGroupID(appKey)
}

// AppUserGroupID 构造 app+user 级组：仅该 app 下该用户的图谱分区。
// 与记忆模块 owner 两级作用域同构（app 做公共底座、user 级为写入目标）。
func AppUserGroupID(appKey, userName string) string {
	return SanitizeGroupID(appKey + groupIDSeparator + userName)
}

// Scope 描述一次 run 的图谱可见组与写入目标组。
type Scope struct {
	// Groups 是检索可见组（app 组在前；userScope 开启时追加 user 组）。
	Groups []string
	// WriteGroup 是 AddEpisode 的写入目标组。
	WriteGroup string
}

// ResolveScope 解析当前 run 的图谱作用域：app 级做公共底座，user 级（启用时）为写入目标。
func ResolveScope(appKey, userName string, userScope bool) Scope {
	appGroup := AppGroupID(appKey)
	scope := Scope{Groups: []string{appGroup}, WriteGroup: appGroup}
	if userScope {
		userGroup := AppUserGroupID(appKey, userName)
		scope.Groups = append(scope.Groups, userGroup)
		scope.WriteGroup = userGroup
	}
	return scope
}

// sharedClient 按 (endpoint, timeout) 缓存的进程级客户端；配置变化（如测试覆写 conf）时重建。
var sharedClient struct {
	sync.Mutex
	config  Config
	client  *Client
}

// SharedClient 返回当前配置对应的共享客户端。endpoint 为空时返回 nil（调用方视为未启用）。
func SharedClient(cfg Config) *Client {
	sharedClient.Lock()
	defer sharedClient.Unlock()
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil
	}
	if sharedClient.client != nil && sharedClient.config.Endpoint == endpoint && sharedClient.config.TimeoutMs == cfg.TimeoutMs {
		return sharedClient.client
	}
	sharedClient.config = Config{Endpoint: endpoint, TimeoutMs: cfg.TimeoutMs}
	sharedClient.client = NewClient(cfg)
	return sharedClient.client
}
