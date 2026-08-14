// Package e2e 是进程外端到端测试：termtest 驱动真实 harness 进程，
// LLM 端点指向 mock HTTP server（确定性，不依赖真实 API）。
// 验证：单轮 run 的 turn_done 锚点、交互式 TUI。
package e2e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ActiveState/termtest"
	"github.com/agent-project/harness/internal/session"
)

// harnessExe 是 TestMain 构建的二进制路径。
var harnessExe string

// TestMain 先构建 harness 二进制（一次），再跑测试。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "harness-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(1)
	}
	harnessExe = filepath.Join(dir, "harness.exe")
	cmd := exec.Command("go", "build", "-o", harnessExe, "../../cmd/harness")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build harness:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// sendKeys 模拟用户在 TUI 里输入一行并回车。termtest.SendLine 追加 OS 行尾
// （POSIX 为 \n），bubbletea 把 LF 当作 ctrl+j 而非 enter（enter = CR \r），
// 导致输入被打进输入框却不提交（Bug01，2026-08-10）。TUI 测试统一用它；
// run 模式走 ReadStdinEvents（input.go 认 \r/\n 两种行尾），保持 SendLine。
func sendKeys(cp *termtest.ConsoleProcess, s string) {
	cp.SendUnterminated(s + "\r")
}

// sse 格式化 Anthropic 风格 SSE 事件（SDK 按 event: 字段路由）。
func sse(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
}

// writeToolUse 追加一个 tool_use 内容块到 SSE 输出（content_block_start/stop）。
func writeToolUse(sb *strings.Builder, id, name, input string) {
	sb.WriteString(sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"`+id+`","name":"`+name+`","input":`+input+`}}`))
	sb.WriteString(sse("content_block_stop", `{"type":"content_block_stop","index":0}`))
}

// writeText 追加一个文本内容块到 SSE 输出（start/delta/stop）。
func writeText(sb *strings.Builder, text string) {
	sb.WriteString(sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	sb.WriteString(sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+text+`"}}`))
	sb.WriteString(sse("content_block_stop", `{"type":"content_block_stop","index":0}`))
}

// mockLLMPlanServer 模拟 plan 模式闭环（ADR-036）的确定性请求序列：
// 1. write_file（plan 模式下被拒）→ 2. write_plan（写计划）→
// 3. plan_done（弹 HITL，用户批准）→ 4. write_file（退出后放行）→ 5. 文本回复。
func mockLLMPlanServer(t *testing.T) *httptest.Server {
	t.Helper()
	var reqCount atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(sse("message_start", msgStart))
		switch reqCount.Add(1) {
		case 1:
			writeToolUse(&sb, "call_1", "write_file", `{"path":"a.txt","content":"x"}`)
		case 2:
			writeToolUse(&sb, "call_2", "write_plan", `{"content":"## 实施计划\n1. 改 a.txt"}`)
		case 3:
			writeToolUse(&sb, "call_3", "plan_done", `{}`)
		case 4:
			writeToolUse(&sb, "call_4", "write_file", `{"path":"a.txt","content":"y"}`)
		default:
			writeText(&sb, "计划已批准，执行完成。")
		}
		sb.WriteString(sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`))
		sb.WriteString(sse("message_stop", `{"type":"message_stop"}`))
		_, _ = w.Write([]byte(sb.String()))
	}))
}

const msgStart = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`

// mockLLMServer 起一个确定性 mock：第 1 次请求返回 list_dir 工具调用，
// 之后返回固定文本回复。
func mockLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	var reqCount atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(sse("message_start", msgStart))
		if reqCount.Add(1) == 1 {
			// 首轮：工具调用 list_dir。
			sb.WriteString(sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"list_dir","input":{}}}`))
		} else {
			// 次轮：文本回复。
			sb.WriteString(sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
			sb.WriteString(sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"目录已列出。"}}`))
		}
		sb.WriteString(sse("content_block_stop", `{"type":"content_block_stop","index":0}`))
		sb.WriteString(sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`))
		sb.WriteString(sse("message_stop", `{"type":"message_stop"}`))
		_, _ = w.Write([]byte(sb.String()))
	}))
}

// writeTestConfig 写一个指向 mock 端点的测试配置（thinking 关闭，简化流式），
// 返回配置文件路径。
func writeTestConfig(t *testing.T, baseURL string) string {
	t.Helper()
	return writeConfigTo(t, baseURL, filepath.Join(t.TempDir(), "config.yaml"))
}

// writeConfigTo 把测试配置写到指定路径。
func writeConfigTo(t *testing.T, baseURL, path string) string {
	t.Helper()
	content := fmt.Sprintf(`providers:
  mock:
    base_url: %s
    api_key: test-key
    models:
      m:
        context_window: 128000
`, baseURL)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestRunSingleTurnE2E 验证单轮 run：mock 首轮工具调用 → 工具执行回填 →
// 次轮文本 → turn_done 锚点 → 退出码 0。
func TestRunSingleTurnE2E(t *testing.T) {
	srv := mockLLMServer(t)
	defer srv.Close()
	cfg := writeTestConfig(t, srv.URL)

	cp, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		Args:           []string{"run", "--config", cfg, "--json", "读取当前目录文件列表"},
		WorkDirectory:  t.TempDir(),
		DefaultTimeout: 30 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + filepath.Join(t.TempDir(), ".harness-e2e")},
	})
	if err != nil {
		t.Fatalf("newtest: %v", err)
	}
	defer cp.Close()

	// JSON 事件流：工具调用 → 回合边界。
	if _, err := cp.Expect("tool_call"); err != nil {
		t.Fatalf("expect tool_call: %v", err)
	}
	if _, err := cp.Expect("turn_done"); err != nil {
		t.Fatalf("expect turn_done: %v", err)
	}
	if _, err := cp.ExpectExitCode(0); err != nil {
		t.Fatalf("expect exit 0: %v", err)
	}
}

// TestTUIInteractiveE2E 验证 TUI 交互闭环：输入 prompt → 回合（工具 + 回复）→ /exit 退出。
// 交互式入口用默认 config 查找（项目级 config.local.yaml），故把测试配置写到工作目录。
func TestTUIInteractiveE2E(t *testing.T) {
	srv := mockLLMServer(t)
	defer srv.Close()
	workDir := t.TempDir()
	writeConfigTo(t, srv.URL, filepath.Join(workDir, "config.local.yaml"))

	cp, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		WorkDirectory:  workDir,
		DefaultTimeout: 30 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + filepath.Join(t.TempDir(), ".harness-e2e")},
	})
	if err != nil {
		t.Fatalf("newtest: %v", err)
	}
	defer cp.Close()

	if _, err := cp.Expect("Ask anything"); err != nil {
		t.Fatalf("expect TUI 输入区: %v", err)
	}
	sendKeys(cp, "你好")
	// mock 首轮 list_dir 工具 + 次轮文本回复。
	if _, err := cp.Expect("目录已列出"); err != nil {
		t.Fatalf("expect reply: %v", err)
	}
	sendKeys(cp, "/exit")
	if _, err := cp.ExpectExitCode(0); err != nil {
		t.Fatalf("expect exit 0: %v", err)
	}
}

// TestTUIApprovalE2E 验证 TUI 审批交互（ADR-029/030）：readonly 下 write_file
// 触发审批弹窗 → y 允许 → 工具执行 → 次轮回复。
func TestTUIApprovalE2E(t *testing.T) {
	srv := mockLLMServerWriteFile(t)
	defer srv.Close()
	workDir := t.TempDir()
	writeConfigApprovalTo(t, srv.URL, filepath.Join(workDir, "config.local.yaml"), "readonly")

	cp, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		WorkDirectory:  workDir,
		DefaultTimeout: 30 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + filepath.Join(t.TempDir(), ".harness-e2e")},
	})
	if err != nil {
		t.Fatalf("newtest: %v", err)
	}
	defer cp.Close()

	if _, err := cp.Expect("Ask anything"); err != nil {
		t.Fatalf("expect TUI 输入区: %v", err)
	}
	sendKeys(cp, "写一个文件")
	// 审批弹窗出现（readonly 下 write_file 询问）。
	if _, err := cp.Expect("PERMISSION REQUIRED"); err != nil {
		t.Fatalf("expect approval popup: %v", err)
	}
	sendKeys(cp, "y")
	if _, err := cp.Expect("文件已写入"); err != nil {
		t.Fatalf("expect reply: %v", err)
	}
	// readonly 下用户允许后工具确实执行（文件写入工作目录）。
	if _, err := os.Stat(filepath.Join(workDir, "out.txt")); err != nil {
		t.Errorf("write_file 未执行: %v", err)
	}
	sendKeys(cp, "/exit")
	if _, err := cp.ExpectExitCode(0); err != nil {
		t.Fatalf("expect exit 0: %v", err)
	}
}

// TestTUIPlanModeE2E 验证 plan 模式闭环（ADR-036）：/plan on → write_file 被拒 →
// write_plan 写计划 → plan_done 弹 ask 弹窗批准 → 退出 plan 模式 → write_file 放行
// → 文本回复。
func TestTUIPlanModeE2E(t *testing.T) {
	srv := mockLLMPlanServer(t)
	defer srv.Close()
	workDir := t.TempDir()
	writeConfigTo(t, srv.URL, filepath.Join(workDir, "config.local.yaml"))

	cp, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		WorkDirectory:  workDir,
		DefaultTimeout: 60 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + filepath.Join(t.TempDir(), ".harness-e2e")},
	})
	if err != nil {
		t.Fatalf("newtest: %v", err)
	}
	defer cp.Close()

	if _, err := cp.Expect("Ask anything"); err != nil {
		t.Fatalf("expect TUI 输入区: %v", err)
	}
	// 进入 plan 模式。
	sendKeys(cp, "/plan on")
	if _, err := cp.Expect("Plan 模式已开启"); err != nil {
		t.Fatalf("expect /plan on 反馈: %v", err)
	}
	// 触发回合。plan 模式下：write_file 被拒（非 Fatal，循环继续）→ write_plan
	// 写计划 → plan_done 弹 HITL。中间工具块可能被弹窗覆盖（termtest 读不到），
	// 直接期待 ask 弹窗——它出现即证明 plan 模式激活 + 只读阶段完成 + plan_done
	// 走到 HITL（若 plan 模式未激活，write_file 会放行执行、plan_done 会被拒，
	// 弹窗不会出现）。
	sendKeys(cp, "规划一个功能")
	if _, err := cp.Expect("PLAN APPROVAL"); err != nil {
		t.Fatalf("expect plan_done ask 弹窗: %v", err)
	}
	sendKeys(cp, "") // 仅 CR = Enter，确认光标处"批准执行"
	if _, err := cp.Expect("已批准"); err != nil {
		t.Fatalf("expect plan_done 批准: %v", err)
	}
	// 退出 plan 模式后 write_file 放行执行（工具块摘要 "created <path>"）。
	if _, err := cp.Expect("created a.txt"); err != nil {
		t.Fatalf("expect write_file 执行: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "a.txt")); err != nil {
		t.Errorf("write_file 未执行（退出 plan 模式后应放行）: %v", err)
	}
	// 文本回复收尾。
	if _, err := cp.Expect("执行完成"); err != nil {
		t.Fatalf("expect 文本回复: %v", err)
	}
	sendKeys(cp, "/exit")
	if _, err := cp.ExpectExitCode(0); err != nil {
		t.Fatalf("expect exit 0: %v", err)
	}
}

// TestTUIResumeE2E 验证 resume --last 进入 TUI 并载入历史首屏（ADR-030 全量替换）。
func TestTUIResumeE2E(t *testing.T) {
	srv := mockLLMServer(t)
	defer srv.Close()
	workDir := t.TempDir()
	writeConfigTo(t, srv.URL, filepath.Join(workDir, "config.local.yaml"))
	home := filepath.Join(t.TempDir(), ".harness-e2e")

	// 先用 run（非 TTY）产生一个会话（历史含"目录已列出"）。
	cmd := exec.Command(harnessExe, "run", "你好")
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "HARNESS_HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run 产会话失败: %v\n%s", err, out)
	}

	// resume --last → TUI 载入历史首屏。
	cp, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		Args:           []string{"resume", "--last"},
		WorkDirectory:  workDir,
		DefaultTimeout: 30 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + home},
	})
	if err != nil {
		t.Fatalf("newtest: %v", err)
	}
	defer cp.Close()
	if _, err := cp.Expect("目录已列出"); err != nil {
		t.Fatalf("resume 首屏应含历史回复: %v", err)
	}
	sendKeys(cp, "/exit")
	if _, err := cp.ExpectExitCode(0); err != nil {
		t.Fatalf("expect exit 0: %v", err)
	}
}

// TestTUIExitsE2E 验证 TUI 能起 + /exit 退出（W2：需有效 config + 输入区渲染）。
func TestTUIExitsE2E(t *testing.T) {
	srv := mockLLMServer(t)
	defer srv.Close()
	workDir := t.TempDir()
	writeConfigTo(t, srv.URL, filepath.Join(workDir, "config.local.yaml"))

	cp, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		WorkDirectory:  workDir,
		DefaultTimeout: 15 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + filepath.Join(t.TempDir(), ".harness-e2e")},
	})
	if err != nil {
		t.Fatalf("newtest: %v", err)
	}
	defer cp.Close()

	// TUI 输入区占位符渲染后，/exit 退出（退出仅此命令，ADR-030）。
	if _, err := cp.Expect("Ask anything"); err != nil {
		t.Fatalf("expect TUI 输入区: %v", err)
	}
	sendKeys(cp, "/exit")
	if _, err := cp.ExpectExitCode(0); err != nil {
		t.Fatalf("expect exit 0: %v", err)
	}
}

// mockLLMServerWriteFile 首轮返回 write_file 工具调用（审批 e2e 用），
// 之后返回固定文本回复。
func mockLLMServerWriteFile(t *testing.T) *httptest.Server {
	t.Helper()
	var reqCount atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		var sb strings.Builder
		sb.WriteString(sse("message_start", msgStart))
		if reqCount.Add(1) == 1 {
			// 首轮：write_file 工具调用（readonly 下触发审批）。
			sb.WriteString(sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_1","name":"write_file","input":{"path":"out.txt","content":"hello"}}}`))
		} else {
			// 次轮：文本回复。
			sb.WriteString(sse("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
			sb.WriteString(sse("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"文件已写入。"}}`))
		}
		sb.WriteString(sse("content_block_stop", `{"type":"content_block_stop","index":0}`))
		sb.WriteString(sse("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`))
		sb.WriteString(sse("message_stop", `{"type":"message_stop"}`))
		_, _ = w.Write([]byte(sb.String()))
	}))
}

// writeConfigApprovalTo 写带 approval.mode 的测试配置。
func writeConfigApprovalTo(t *testing.T, baseURL, path, mode string) string {
	t.Helper()
	content := fmt.Sprintf(`providers:
  mock:
    base_url: %s
    api_key: test-key
    models:
      m:
        context_window: 128000
approval:
  mode: %s
`, baseURL, mode)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestApprovalE2E 验证真实 TTY 审批交互（ADR-029）：write_file 在 readonly
// 下触发审批 UI → 用户 y → 工具执行 → 次轮回复。termtest SendLine 模拟按键。
func TestApprovalE2E(t *testing.T) {
	srv := mockLLMServerWriteFile(t)
	defer srv.Close()
	workDir := t.TempDir()
	cfg := writeConfigApprovalTo(t, srv.URL, filepath.Join(t.TempDir(), "config.yaml"), "readonly")

	cp, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		Args:           []string{"run", "--config", cfg, "写一个文件"},
		WorkDirectory:  workDir,
		DefaultTimeout: 30 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + filepath.Join(t.TempDir(), ".harness-e2e")},
	})
	if err != nil {
		t.Fatalf("newtest: %v", err)
	}
	defer cp.Close()

	// 审批 UI 出现（摘要含写入路径）。
	if _, err := cp.Expect("写入文件: out.txt"); err != nil {
		t.Fatalf("expect approval UI: %v", err)
	}
	// 用户允许本次。
	cp.SendLine("y")
	if _, err := cp.Expect("文件已写入"); err != nil {
		t.Fatalf("expect reply: %v", err)
	}
	if _, err := cp.ExpectExitCode(0); err != nil {
		t.Fatalf("expect exit 0: %v", err)
	}
	// readonly 下用户允许后工具确实执行（文件写入工作目录）。
	if _, err := os.Stat(filepath.Join(workDir, "out.txt")); err != nil {
		t.Errorf("write_file 未执行: %v", err)
	}
}

// TestSessionPersistenceE2E 验证 CLI 全链路落盘：run 后 workspace 下有会话
// （historys + agentstate.json），`harness sessions` 能列出。
func TestSessionPersistenceE2E(t *testing.T) {
	srv := mockLLMServer(t)
	defer srv.Close()
	cfg := writeTestConfig(t, srv.URL)
	workDir := t.TempDir()
	home := filepath.Join(t.TempDir(), ".harness-e2e")

	// 1) run 落盘。
	cp, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		Args:           []string{"run", "--config", cfg, "你好"},
		WorkDirectory:  workDir,
		DefaultTimeout: 30 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + home},
	})
	if err != nil {
		t.Fatalf("newtest: %v", err)
	}
	defer cp.Close()
	if _, err := cp.Expect("目录已列出"); err != nil {
		t.Fatalf("expect reply: %v", err)
	}
	if _, err := cp.ExpectExitCode(0); err != nil {
		t.Fatalf("expect exit 0: %v", err)
	}

	// 2) sessions 列出会话。
	cp2, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		Args:           []string{"sessions"},
		WorkDirectory:  workDir,
		DefaultTimeout: 30 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + home},
	})
	if err != nil {
		t.Fatalf("newtest sessions: %v", err)
	}
	defer cp2.Close()
	if _, err := cp2.Expect("model=m"); err != nil {
		t.Fatalf("expect sessions listing: %v", err)
	}
	if _, err := cp2.ExpectExitCode(0); err != nil {
		t.Fatalf("expect exit 0: %v", err)
	}

	// 3) workspace 文件存在。
	store := session.NewAt(home)
	// macOS 上 /var -> /private/var 符号链接：子进程 getcwd 返回物理路径
	// （/private/var/...），而 t.TempDir 返回逻辑路径（/var/...）——不解析
	// 符号链接会分到两个项目桶。先解析再 FindProject（与 CLI 进程内 Getwd 同源）。
	resolved, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	proj, err := store.FindProject(resolved)
	if err != nil {
		t.Fatalf("find project: %v", err)
	}
	list, err := proj.Sessions()
	if err != nil || len(list) != 1 {
		t.Fatalf("sessions: %v len=%d", err, len(list))
	}
	for _, p := range []string{
		filepath.Join(list[0].Path, "historys", "history-1.jsonl"),
		filepath.Join(list[0].Path, session.FileAgentState),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("缺文件 %s: %v", p, err)
		}
	}
}

// TestInitE2E 验证 harness init：创建 workspace 骨架 + 注释版 config.yaml
// 模板；幂等重跑退出 0、不覆盖用户编辑（进程外验证，2026-08-14）。
func TestInitE2E(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	workDir := t.TempDir()
	cp, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		Args:           []string{"init"},
		WorkDirectory:  workDir,
		DefaultTimeout: 30 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + home},
	})
	if err != nil {
		t.Fatalf("newtest init: %v", err)
	}
	defer cp.Close()
	if _, err := cp.Expect("初始化完成"); err != nil {
		t.Fatalf("expect init output: %v", err)
	}
	if _, err := cp.ExpectExitCode(0); err != nil {
		t.Fatalf("expect exit 0: %v", err)
	}
	for _, p := range []string{
		filepath.Join(home, "workspaces"), filepath.Join(home, "subagents"),
		filepath.Join(home, "memory"), filepath.Join(home, "logs"),
		filepath.Join(home, "agents.md"), filepath.Join(home, "config.yaml"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("缺 %s: %v", p, err)
		}
	}
	// 幂等 + 不覆盖：写入用户编辑后再 init。
	cfg := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(cfg, []byte("# user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp2, err := termtest.NewTest(t, termtest.Options{
		CmdName:        harnessExe,
		Args:           []string{"init"},
		WorkDirectory:  workDir,
		DefaultTimeout: 30 * time.Second,
		Environment:    []string{"HARNESS_HOME=" + home},
	})
	if err != nil {
		t.Fatalf("newtest init2: %v", err)
	}
	defer cp2.Close()
	if _, err := cp2.ExpectExitCode(0); err != nil {
		t.Fatalf("重复 init 退出: %v", err)
	}
	data, _ := os.ReadFile(cfg)
	if string(data) != "# user\n" {
		t.Errorf("用户编辑不应被覆盖: %q", data)
	}
}
