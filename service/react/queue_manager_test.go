package react

import (
	"testing"

	model "react-base-service/models/llm"
)

func queuedRow(id uint, seq int) model.ReactPendingInput {
	return model.ReactPendingInput{ID: id, Seq: seq, Kind: model.ReactPendingKindUserInput, Status: model.ReactPendingStatusQueued}
}

// 重排（纯函数）：请求顺序决定新 seq，从当前最小 seq 起连续分配；
// 非排列（漏项/多项/重复/非法 id）一律拒绝。
func TestPlanQueueReorder(t *testing.T) {
	queued := []model.ReactPendingInput{queuedRow(11, 3), queuedRow(12, 7), queuedRow(13, 9)}

	assign, err := planQueueReorder(queued, []uint{13, 11, 12})
	if err != nil {
		t.Fatalf("合法排列应通过: %v", err)
	}
	if assign[13] != 3 || assign[11] != 4 || assign[12] != 5 {
		t.Fatalf("新 seq 应从最小 seq(3) 起连续分配: %+v", assign)
	}

	if _, err := planQueueReorder(queued, []uint{13, 11}); err == nil {
		t.Fatalf("漏项应拒绝")
	}
	if _, err := planQueueReorder(queued, []uint{13, 11, 12, 14}); err == nil {
		t.Fatalf("多项应拒绝")
	}
	if _, err := planQueueReorder(queued, []uint{13, 13, 11}); err == nil {
		t.Fatalf("重复应拒绝")
	}
	if _, err := planQueueReorder(queued, []uint{13, 11, 14}); err == nil {
		t.Fatalf("未知 id 应拒绝")
	}
	// 空队列 + 空请求是合法 no-op（服务层已在调用前拒绝空队列，这里防御兜底）。
	if assign, err := planQueueReorder(nil, []uint{}); err != nil || len(assign) != 0 {
		t.Fatalf("空对空应为 no-op: %+v, err=%v", assign, err)
	}
}
