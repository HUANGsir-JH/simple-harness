package agent

import "time"

// timeNowNanos is a tiny indirection so tests can inject deterministic IDs
// if needed; production uses the wall clock.
func timeNowNanos() int64 { return time.Now().UnixNano() }
