package react

import (
	"testing"

	"react-base-service/components/params"
)

// D-2 断连软脱管回归：Detach 后事件写入必须静默成功（返回 nil），
// 否则引擎会把写失败当成运行错误终止已脱离连接的 run（TEST-REPORT-2026-10-27 D-2）。
func TestWSEventWriterDetachedDropsWrites(t *testing.T) {
	writer := &wsEventWriter{}
	writer.Detach()
	// conn 为 nil：非 detach 路径会 panic，detach 路径不允许触碰 conn。
	for i := 1; i <= 3; i++ {
		if err := writer.Write(params.ReactEvent{Type: "thought_delta"}); err != nil {
			t.Fatalf("detached write should succeed silently, got %v", err)
		}
		if writer.seq != i {
			t.Fatalf("detached write should still advance seq: want %d got %d", i, writer.seq)
		}
	}
}

// Detach 必须幂等（心跳与读循环可能并发触发）。
func TestWSEventWriterDetachIdempotent(t *testing.T) {
	writer := &wsEventWriter{}
	writer.Detach()
	writer.Detach()
	if err := writer.Write(params.ReactEvent{Type: "done"}); err != nil {
		t.Fatalf("write after double detach failed: %v", err)
	}
}
