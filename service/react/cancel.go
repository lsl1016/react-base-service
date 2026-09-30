package react

import (
	"context"
	"sync"

	core "react-base-service/service/react/internal/core"
)

var activeRunCancels sync.Map // runID -> context.CancelCauseFunc

func registerReactRunCancel(runID string, cancel context.CancelCauseFunc) {
	activeRunCancels.Store(runID, cancel)
}

func unregisterReactRunCancel(runID string) {
	activeRunCancels.Delete(runID)
}

func getActiveReactRunCancel(runID string) (context.CancelCauseFunc, bool) {
	value, ok := activeRunCancels.Load(runID)
	if !ok {
		return nil, false
	}
	cancel, ok := value.(context.CancelCauseFunc)
	return cancel, ok
}

func IsReactRunCancelled(err error) bool       { return core.IsReactRunCancelled(err) }
func IsReactClientDisconnected(err error) bool { return core.IsReactClientDisconnected(err) }
