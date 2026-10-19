package gateway

import (
	"encoding/json"
	"strings"
)

// 白名单（tblLlmChannelConfig.allowed_users，JSON 字符串数组）：
// bot 公开可搜索，任何陌生人都能消耗模型额度，安全上默认应收紧——
// 空清单 = 不限（兼容 M1 存量配置），配置后仅命中条目可交互。
//
// 条目语义（统一小写比较）：
//   - Telegram：数字 user ID / 群 chat ID（负数）/@username；
//   - 飞书：open_id（ou_）/ chat_id（oc_）。
//
// 候选集合由渠道侧构造（发送者 ID、chat ID、@username 等），任一命中即放行；
// 群聊场景把群 chat ID 配入白名单等于放行该群全体成员。

// parseAllowedUsers 解析配置的白名单清单；空/非法配置返回 nil（语义 = 不限制）。
func parseAllowedUsers(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var list []string
	if err := json.Unmarshal([]byte(trimmed), &list); err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		if normalized := normalizeAllowedUser(entry); normalized != "" {
			out = append(out, normalized)
		}
	}
	return out
}

// normalizeAllowedUser 归一化白名单条目/候选值（去空白 + 小写，@Username 大小写不敏感）。
func normalizeAllowedUser(entry string) string {
	return strings.ToLower(strings.TrimSpace(entry))
}

// channelAllowsSender 白名单判定：清单空放行；任一候选命中放行。
func channelAllowsSender(allowed []string, candidates ...string) bool {
	if len(allowed) == 0 {
		return true
	}
	set := make(map[string]bool, len(allowed))
	for _, entry := range allowed {
		set[entry] = true
	}
	for _, candidate := range candidates {
		if set[normalizeAllowedUser(candidate)] {
			return true
		}
	}
	return false
}
