// Package web 实现 Web UI 模式运行时（feat/webui 阶段）：本地 HTTP 服务承载
// TUI 全部功能（消息流/工具块/审批/提问/斜杠命令/队列/中断/唤醒/子 agent/
// plan/todo/用量），浏览器为客户端。分层对齐 internal/ui/tui：
//
//	Server     —— gin 路由 + 静态资源 + SSE 端点（HTTP 层）
//	Hub        —— SSE 订阅者集合 + 广播（事件推送层）
//	Controller —— 会话注册表 + 输入路由 + run 编排（模式运行时，对位 tui.Controller
//	              去掉 bubbletea 依赖，事件走 Hub 而非 tea.Msg）
//	approver   —— 审批/ask 桥（request_id + pending 表 + Hub 广播 + 决策回填）
//	command    —— 斜杠命令分发（对位 tui/command.go + popup.go 的语义）
//	state      —— /api/state 快照（会话列表 + timeline + 状态栏 + pending）
//	md/toolview—— markdown→HTML 渲染 + 工具块展示数据（后端渲染，前端零依赖）
//
// 依赖方向（无环）：web → agent/session/subagent/middleware（同 tui 包）；
// app → web（app 是 Composition Root）。
package web

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Hub 是 SSE 订阅者集合：多客户端订阅事件流，Broadcast 向全部订阅者推送。
// 与 subagent.Manager 的 subs 同构（锁内快照、锁外分发），但订阅者是 SSE
// 长连接通道而非回调。
//
// 并发模型：Subscribe/Broadcast/Close 均线程安全；Broadcast 在锁内快照
// 订阅者列表、锁外逐个发送（慢订阅者不阻塞其它订阅者；通道有缓冲，满则
// 丢弃该事件——SSE 客户端断连/慢消费时丢弃可接受，重连由前端重拉 state
// 对齐）。
type Hub struct {
	mu     sync.Mutex
	subs   map[chan []byte]struct{}
	closed bool
}

// NewHub 创建空 Hub。
func NewHub() *Hub {
	return &Hub{subs: make(map[chan []byte]struct{})}
}

// Subscribe 注册一个新订阅者，返回事件通道与退订函数（幂等；Close 后
// 退订 no-op）。SSE handler 在连接建立时调用，`c.Request.Context().Done()`
// 时退订（浏览器关闭/断网清理，防向死连接广播 + goroutine 泄漏）。
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// Len 返回当前订阅者数（approver 无订阅者自释放判定用；页面全关时
// approval/ask 直接 ctx cancel，防回合卡死）。
func (h *Hub) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Broadcast 向全部订阅者推送一条 SSE 事件（`event: <type>\ndata: <json>\n\n`）。
// payload 序列化失败静默跳过（事件已尽力而为）。锁内快照、锁外发送。
func (h *Hub) Broadcast(eventType string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, data)
	h.mu.Lock()
	subs := make([]chan []byte, 0, len(h.subs))
	for ch := range h.subs {
		subs = append(subs, ch)
	}
	h.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- []byte(msg):
		default: // 通道满：丢弃（慢/断连订阅者），不阻塞
		}
	}
}

// Close 关闭 Hub：全部订阅者通道关闭（SSE handler 读到关闭 → 连接结束），
// 后续 Subscribe 返回已关闭通道。Server.Shutdown 前调用——先断开 SSE 长
// 连接，http.Server.Shutdown 才不会永久等待。
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	for ch := range h.subs {
		close(ch)
	}
	h.subs = nil
	h.mu.Unlock()
}
