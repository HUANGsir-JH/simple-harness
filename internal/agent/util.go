package agent

import "time"

// timeNowNanos 是一个小间接层，便于测试注入确定性的 ID；
// 生产环境使用墙钟。
func timeNowNanos() int64 { return time.Now().UnixNano() }
