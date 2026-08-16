package subagent

import (
	"reflect"

	"github.com/agent-project/harness/internal/events"
)

// sameFunc 比较两个函数是否同一（Unsubscribe 退订用）。
func sameFunc(a, b func(events.Event)) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}
