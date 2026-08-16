package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agent-project/harness/internal/agent"
	"github.com/agent-project/harness/internal/config"
	"github.com/agent-project/harness/internal/middleware"
	"github.com/agent-project/harness/internal/session"
	"github.com/agent-project/harness/internal/subagent"
	"github.com/gin-gonic/gin"
)

// assets 是前端静态资源（零依赖原生 HTML/CSS/JS；go:embed 单二进制交付）。
//
//go:embed assets/*
var assets embed.FS

// Server 是装配完成的 Web UI 实例（显式三阶段生命周期：Assemble → Run →
// Close，对齐 tui.App）。Run 启动 HTTP 服务并等待 ctx 取消（信号）；
// 收尾顺序（review H5/M3）：Hub.Close（断开 SSE 长连接）→ http.Server
// Shutdown（带超时）→ WaitRuns → SaveActiveState → CloseAll。
type Server struct {
	controller *Controller
	hub        *Hub
	httpSrv    *http.Server
	addr       string
	closed     bool
}

// Assemble 装配 Web 服务：controller → hub → gin 路由。参数（agent/project/
// sess/newSession）即装配产物 HarnessAgent 移交的零件（app→web 接缝；web 不
// 反向依赖 app，防环，分层见 internal/app 注释）。
func Assemble(a *agent.Agent, project *session.Project, cfg config.Config, sess *session.Session, newSession func() (*session.Session, error), ctx context.Context, host string, port int) *Server {
	return AssembleWith(a, project, cfg, sess, newSession, ctx, true, nil, host, port)
}

// AssembleWith 是全参版本（showThinking 当前仅影响渲染缺省；subagents 为子
// agent 管理器，nil = 不启用 /subagents 与查看功能——纯单元测试用）。
func AssembleWith(a *agent.Agent, project *session.Project, cfg config.Config, sess *session.Session, newSession func() (*session.Session, error), ctx context.Context, showThinking bool, subagents *subagent.Manager, host string, port int) *Server {
	hub := NewHub()
	controller := NewController(a, project, cfg, sess, newSession, ctx)
	controller.SetSubagents(subagents)
	controller.SetHub(hub)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	return &Server{
		controller: controller,
		hub:        hub,
		addr:       addr,
	}
}

// Run 运行阶段：启动 HTTP 服务，阻塞等待顶层 ctx 取消（信号），随后收尾
// （Shutdown → WaitRuns → SaveActiveState）。Close 由外部（HarnessAgent
// runWeb 的 srv.Close()）调用。
func (s *Server) Run() error {
	r := s.routes()
	s.httpSrv = &http.Server{
		Addr:    s.addr,
		Handler: r,
		// ⚠ 不设 WriteTimeout：SSE 长连接会被切断（review L5）。
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.httpSrv.ListenAndServe()
	}()

	// 启动即打印访问地址（用户可复制到浏览器）。
	fmt.Printf("harness web: http://%s\n", s.addr)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("web: listen %s: %w", s.addr, err)
		}
		return nil
	case <-s.controller.ctx.Done():
		// 信号（SIGINT/SIGTERM）→ 优雅收尾。
		s.shutdown()
		<-errCh // 等 ListenAndServe 返回
		return nil
	}
}

// shutdown 是收尾第一段：Hub.Close 断开全部 SSE 长连接 → http.Server
// Shutdown（带超时 context，防卡死）。
func (s *Server) shutdown() {
	s.hub.Close()
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.httpSrv.Shutdown(sctx)
}

// Close 拆除阶段（对位 tui.App.Close）：收尾第二段——WaitRuns（run goroutine
// 可能仍在 emit，须等其退出再关 writer，Bug09）→ SaveActiveState（退出兜底
// 落盘，ADR-038）→ CloseAll（flush 全部打开会话）。与 Run 的 shutdown 对称；
// closed 守卫幂等，外部 Teardown 兜底可重复调用。
func (s *Server) Close() {
	if s.closed {
		return
	}
	s.closed = true
	s.controller.WaitRuns()
	_ = s.controller.SaveActiveState()
	s.controller.CloseAll()
}

// routes 构建 gin 路由：/api/* 全部先注册，StaticFS 最后（StaticFS 会吞
// 未注册路径，review L6）。路由在 Run 内调用（gin 可安全多实例）。
func (s *Server) routes() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// 静态资源（embed）：/ 与 /index.html /style.css /app.js。
	// ⚠ 不用 r.StaticFS("/")——根通配符会与 /api/* 路由冲突 panic；
	// 挂 /static 前缀 + 手动注册 /（返回 index.html）。
	sub, err := fs.Sub(assets, "assets")
	if err == nil {
		r.StaticFS("/static", http.FS(sub))
	}
	r.GET("/", func(c *gin.Context) {
		b, err := assets.ReadFile("assets/index.html")
		if err != nil {
			c.String(http.StatusInternalServerError, "index.html: %v", err)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", b)
	})

	api := r.Group("/api")
	api.Use(sameOrigin()) // CSRF/DNS rebinding 防护（review M11）
	{
		api.GET("/state", s.handleState)
		api.GET("/events", s.handleEvents)
		api.POST("/input", s.handleInput)
		api.POST("/interrupt", s.handleInterrupt)
		api.POST("/approve", s.handleApprove)
		api.POST("/ask", s.handleAsk)
		api.POST("/new", s.handleNew)
		api.POST("/switch", s.handleSwitch)
	}
	return r
}

// sameOrigin 校验 POST 的 Origin/Sec-Fetch-Site（非 same-origin 拒绝 403；
// 防恶意网页驱动本机 agent，review M11）。无 Origin 头（curl 等）放行——
// 本机 CLI 工具场景；浏览器跨站必带 Origin。
func sameOrigin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		if secFetch := c.GetHeader("Sec-Fetch-Site"); secFetch != "" && secFetch != "same-origin" && secFetch != "none" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-origin request rejected"})
			return
		}
		if origin := c.GetHeader("Origin"); origin != "" && !strings.HasPrefix(origin, "http://127.0.0.1") && !strings.HasPrefix(origin, "http://localhost") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "cross-origin request rejected"})
			return
		}
		c.Next()
	}
}

// handleState GET /api/state → 全量快照。
func (s *Server) handleState(c *gin.Context) {
	c.JSON(http.StatusOK, s.controller.State())
}

// handleEvents GET /api/events → SSE 长连接。
func (s *Server) handleEvents(c *gin.Context) {
	ch, unsub := s.hub.Subscribe()
	defer unsub()
	// 浏览器关闭/断网 → Request.Context 取消 → 退订（review L4）。
	go func() {
		<-c.Request.Context().Done()
		unsub()
	}()

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// 连接建立即发一个初始事件（前端可确认 SSE 已就绪）。
	s.hub.Broadcast("system", map[string]any{"text": "connected", "error": false})

	flusher, _ := c.Writer.(http.Flusher)
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return // Hub 关闭（Server 收尾）
			}
			if _, err := c.Writer.Write(msg); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-heartbeat.C:
			// SSE 保活（代理/中间层空闲超时防护）。
			if _, err := c.Writer.WriteString(": ping\n\n"); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// handleInput POST /api/input {line} → 同步命令结果 / 启动回合。
func (s *Server) handleInput(c *gin.Context) {
	var req struct {
		Line string `json:"line"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, s.controller.HandleInput(req.Line))
}

// handleInterrupt POST /api/interrupt → 中断当前回合。
func (s *Server) handleInterrupt(c *gin.Context) {
	s.controller.Interrupt()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleApprove POST /api/approve {request_id, decision} → 回填审批。
func (s *Server) handleApprove(c *gin.Context) {
	var req struct {
		RequestID string `json:"request_id"`
		Decision  string `json:"decision"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}
	if err := s.controller.Approve(req.RequestID, req.Decision); err != nil {
		if strings.Contains(err.Error(), "unknown") {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleAsk POST /api/ask {request_id, selection, custom} → 回填提问。
func (s *Server) handleAsk(c *gin.Context) {
	var req struct {
		RequestID string   `json:"request_id"`
		Selection []string `json:"selection"`
		Custom    string   `json:"custom"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}
	res := middleware.AskResult{Selection: req.Selection, Custom: req.Custom}
	if err := s.controller.AnswerAsk(req.RequestID, res); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleNew POST /api/new → 新建会话（running 时拒绝）。
func (s *Server) handleNew(c *gin.Context) {
	if err := s.controller.NewSession(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.controller.broadcastStateChanged("new")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// handleSwitch POST /api/switch {session_id} → 切换会话（running 时拒绝）。
func (s *Server) handleSwitch(c *gin.Context) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON: " + err.Error()})
		return
	}
	if err := s.controller.SwitchTo(req.SessionID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.controller.broadcastStateChanged("switch")
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
