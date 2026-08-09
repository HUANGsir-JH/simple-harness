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

// sse 格式化 Anthropic 风格 SSE 事件（SDK 按 event: 字段路由）。
func sse(eventType, data string) string {
	return "event: " + eventType + "\ndata: " + data + "\n\n"
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
        thinking:
          enabled: false
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
	cp.SendLine("你好")
	// mock 首轮 list_dir 工具 + 次轮文本回复。
	if _, err := cp.Expect("目录已列出"); err != nil {
		t.Fatalf("expect reply: %v", err)
	}
	cp.SendLine("/exit")
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
	cp.SendLine("写一个文件")
	// 审批弹窗出现（readonly 下 write_file 询问）。
	if _, err := cp.Expect("PERMISSION REQUIRED"); err != nil {
		t.Fatalf("expect approval popup: %v", err)
	}
	cp.SendLine("y")
	if _, err := cp.Expect("文件已写入"); err != nil {
		t.Fatalf("expect reply: %v", err)
	}
	// readonly 下用户允许后工具确实执行（文件写入工作目录）。
	if _, err := os.Stat(filepath.Join(workDir, "out.txt")); err != nil {
		t.Errorf("write_file 未执行: %v", err)
	}
	cp.SendLine("/exit")
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
	cp.SendLine("/exit")
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
	cp.SendLine("/exit")
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
        thinking:
          enabled: false
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
	proj, err := store.FindProject(workDir)
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
