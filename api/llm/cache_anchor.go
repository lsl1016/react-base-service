package llm

import "context"

type cacheAnchorContextKey struct{}

// WithCacheAnchorSkip 声明请求消息尾部的 N 条为临时消息（本轮提醒 / 续写指令等下一轮
// 就会消失的合成消息）。provider 端 prompt 缓存锚点会跳过它们，落在最后的稳定消息上：
// 若锚点落在临时消息上，下一轮同位置换成真实历史消息，缓存断点每轮移动，
// provider 需要每轮全量重写缓存；跳过后锚点固定在稳定历史末尾，下一轮可增量命中。
func WithCacheAnchorSkip(ctx context.Context, skip int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, cacheAnchorContextKey{}, skip)
}

func cacheAnchorSkipFromContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	skip, _ := ctx.Value(cacheAnchorContextKey{}).(int)
	return skip
}
