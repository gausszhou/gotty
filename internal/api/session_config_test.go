package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gausszhou/gotty/internal/session"
)

// per-session 启动参数(0005 §2.4)的验收测试:env/cwd/term/scrollback 必须
// 真的作用到子进程与镜像上,而非法值要在创建端点被 400 拦下(0005 §4)。
//
// "生效"不能只看 HTTP 201——201 只证明参数被接受。这里让会话里的 shell 把
// 注入的变量、`pwd -P` 与 $TERM 打印到屏幕上,再经镜像(/screen)读回;历史
// 容量则经 /screen?part=scrollback 的 total 断言。
//
// Windows 默认没有 sh,凡是要读子进程环境的子用例按平台跳过;纯校验用例与
// 平台无关,仍然全平台跑。

// cfgScrollScript 写出 30 行可辨认的历史,供容量断言使用。开头的 sleep 是
// 刻意的:`scrollback` 由创建请求返回后紧接着调 SetScrollbackLines 应用,而
// 子进程在创建时就已经启动,留出余量才能断言"精确容量"而不是"至少几行"。
const cfgScrollScript = `sleep 0.2; i=1; while [ "$i" -le 30 ]; do echo "cfgsh-$i"; i=$((i+1)); done; echo cfgsh-done`

// cfgNewSessionServer 起一个带屏幕镜像的测试 server,镜像的服务端默认
// scrollback 可配——会话级 `scrollback` 的意义正是覆盖这个服务端默认值。
// 与 newAgentTestServer 一样不启动 manager 的清扫循环:用例里的命令很快退出,
// 但镜像保留最终画面,读屏与读历史仍然有效。
func cfgNewSessionServer(t *testing.T, scrollback int) *httptest.Server {
	t.Helper()

	options := &Options{
		Address:        "127.0.0.1",
		Port:           "0",
		TitleFormat:    "test-title",
		PermitWrite:    true,
		DefaultArgs:    []string{},
		TitleVariables: map[string]interface{}{"hostname": "testhost"},
	}
	manager := session.NewManager(session.WithMirrorFactory(MirrorFactory(true, scrollback)))

	srv, err := New(manager, options)
	if err != nil {
		t.Fatalf("failed to create server: %s", err)
	}
	ts := httptest.NewServer(srv.setupHandlers())
	t.Cleanup(ts.Close)
	return ts
}

// cfgShCreateBody 构造一个 `sh -c script` 会话的创建请求。cols/rows 同时决定
// PTY 与镜像尺寸:读屏用例要装得下完整的目录路径,scrollback 用例则要求输出
// 能滚出可见屏,所以两处传不同尺寸。
func cfgShCreateBody(script string, cols, rows int) map[string]interface{} {
	return map[string]interface{}{
		"command": "sh",
		"args":    []string{"-c", script},
		"width":   cols,
		"height":  rows,
	}
}

// cfgCreateWithParams 用给定的创建参数新建会话,要求 201 并返回 id。
func cfgCreateWithParams(t *testing.T, ts *httptest.Server, body map[string]interface{}) string {
	t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("failed to encode create request: %s", err)
	}
	status, resp := doJSON(t, http.MethodPost, ts.URL+"/api/sessions", string(raw))
	if status != http.StatusCreated {
		t.Fatalf("create session: status %d (want %d), resp %v", status, http.StatusCreated, resp)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatal("session id must not be empty")
	}
	return id
}

// cfgCreateStatus 提交原始创建请求体,回传状态码与错误文本:校验类断言只关心
// 这两者,不必解码成会话状态。
func cfgCreateStatus(t *testing.T, ts *httptest.Server, body []byte) (int, string) {
	t.Helper()

	status, resp := doJSON(t, http.MethodPost, ts.URL+"/api/sessions", string(body))
	msg, _ := resp["error"].(string)
	return status, msg
}

// cfgRequirePOSIXShell 跳过需要 POSIX shell 的子用例:Windows 默认镜像里没有
// sh(不保证装了 Git Bash),而这类用例要看的是子进程环境本身,不是平台差异。
func cfgRequirePOSIXShell(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX sh to print the child environment")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not available: %s", err)
	}
}

// cfgQuickCommand 返回一个立刻成功退出的命令,给"只关心创建是否被接受"的
// 用例用:会话不必长命,请求返回 201 就证明参数通过了校验。
func cfgQuickCommand(t *testing.T) (string, []string) {
	t.Helper()

	if _, err := exec.LookPath("sh"); err == nil {
		return "sh", []string{"-c", "exit 0"}
	}
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/c", "exit", "0"}
	}
	t.Skip("no shell available to start a throwaway session")
	return "", nil
}

// cfgScrollbackStatusTotal 读会话历史,回传 HTTP 状态与保留行数:会话级
// scrollback 是否改写了容量,就看 total。
func cfgScrollbackStatusTotal(t *testing.T, ts *httptest.Server, id string) (int, int) {
	t.Helper()

	status, resp := doJSON(t, http.MethodGet,
		ts.URL+"/api/sessions/"+id+"/screen?part=scrollback&format=json", "")
	total, _ := resp["total"].(float64)
	return status, int(total)
}

// cfgStartScrollSession 起一个 40x10 的会话跑 cfgScrollScript,等输出全部落地
// 后再返回 id。scrollback 为 nil 表示不传该字段(沿用服务端默认值)。
func cfgStartScrollSession(t *testing.T, ts *httptest.Server, scrollback *int) string {
	t.Helper()

	body := cfgShCreateBody(cfgScrollScript, 40, 10)
	if scrollback != nil {
		body["scrollback"] = *scrollback
	}
	id := cfgCreateWithParams(t, ts, body)
	// 必须等最后一个标记出现再读历史,否则 total 可能只数到一半的行。
	waitForAgentText(t, ts, id, "cfgsh-done")
	return id
}

// TestSessionConfigEnvCwdTermTakeEffect:会话级 env/cwd/term 必须真正作用到
// 子进程(0005 §4),而不只是被服务端接受。
func TestSessionConfigEnvCwdTermTakeEffect(t *testing.T) {
	cfgRequirePOSIXShell(t)
	ts := cfgNewSessionServer(t, 0)

	dir := t.TempDir()
	// `pwd -P` 会解析符号链接(macOS 的 /var → /private/var),期望值也要取
	// 真实路径,否则会拿符号链接路径误报失败。
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("failed to resolve %q: %s", dir, err)
	}

	const script = `echo "cfg-env=$CFG_TEST_FOO"; echo "cfg-cwd=$(pwd -P)"; echo "cfg-term=$TERM"; echo cfg-env-done`
	// 200 列:临时目录的完整路径要能整行放下,折行会让子串断言漏掉后半段。
	body := cfgShCreateBody(script, 200, 20)
	body["env"] = []string{"CFG_TEST_FOO=cfg-value-42"}
	body["cwd"] = dir
	body["term"] = "screen-256color"
	id := cfgCreateWithParams(t, ts, body)

	waitForAgentText(t, ts, id, "cfg-env-done")

	status, screen := doGet(t, ts.URL+"/api/sessions/"+id+"/screen")
	if status != http.StatusOK {
		t.Fatalf("screen: status %d (want %d)", status, http.StatusOK)
	}
	for _, want := range []string{
		"cfg-env=cfg-value-42",     // env 注入
		"cfg-cwd=" + wantDir,       // cwd 生效
		"cfg-term=screen-256color", // term 覆盖默认的 xterm-256color
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("screen missing %q; screen:\n%s", want, screen)
		}
	}
}

// TestSessionConfigScrollbackTakeEffect:会话级 scrollback 既能把服务端默认的
// 历史容量放大,也能显式归零(0005 §4)。
func TestSessionConfigScrollbackTakeEffect(t *testing.T) {
	cfgRequirePOSIXShell(t)

	// 服务端默认 3 行:小于 30 行输出,这样"沿用默认"与"会话级覆盖"两种
	// 情况的 total 都落在会滚动的区间内,断言才有区分度。
	const serverDefault = 3
	ts := cfgNewSessionServer(t, serverDefault)

	// 1. 不传 scrollback:沿用服务端默认容量。
	id := cfgStartScrollSession(t, ts, nil)
	status, total := cfgScrollbackStatusTotal(t, ts, id)
	if status != http.StatusOK {
		t.Fatalf("default scrollback: status %d (want %d)", status, http.StatusOK)
	}
	if total != serverDefault {
		t.Errorf("default scrollback total = %d, want %d (serve default)", total, serverDefault)
	}

	// 2. 会话级容量更大:历史被多留几行,证明该参数被应用而不是被忽略。
	want := serverDefault + 6
	id = cfgStartScrollSession(t, ts, &want)
	status, total = cfgScrollbackStatusTotal(t, ts, id)
	if status != http.StatusOK {
		t.Fatalf("scrollback=%d: status %d (want %d)", want, status, http.StatusOK)
	}
	if total != want {
		t.Errorf("scrollback=%d total = %d, want %d", want, total, want)
	}

	// 3. 会话级 0:显式关闭,即使服务端默认开着,读历史也应是 503(关闭
	//    语义与 --scrollback=0 一致,而不是"空结果")。
	off := 0
	id = cfgStartScrollSession(t, ts, &off)
	status, total = cfgScrollbackStatusTotal(t, ts, id)
	if status != http.StatusServiceUnavailable {
		t.Errorf("scrollback=0: status %d (want %d), total %d", status, http.StatusServiceUnavailable, total)
	}
}

// TestValidateSessionParams 逐条钉住 validateSessionParams 的规则与边界:
// HTTP 层只负责把错误映射成 400(见 TestValidateSessionParamsHTTP),阈值本身
// 在这里覆盖,避免上限被悄悄放宽或收紧。
func TestValidateSessionParams(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to create %q: %s", file, err)
	}

	longName := strings.Repeat("A", maxEnvNameLen+1)
	longValue := strings.Repeat("v", maxEnvValueLen+1)

	tooMany := make([]string, 0, maxSessionEnvVars+1)
	for i := 0; i <= maxSessionEnvVars; i++ {
		tooMany = append(tooMany, fmt.Sprintf("CFG_%d=1", i))
	}

	// 单条都合法(值不超 maxEnvValueLen),只有总量超限:证明"条目过多"与
	// "总量超限"是两个独立的上限。
	tooLarge := []string{
		"A=" + strings.Repeat("v", maxEnvValueLen),
		"B=" + strings.Repeat("v", maxEnvValueLen),
		"C=" + strings.Repeat("v", maxEnvValueLen),
		"D=" + strings.Repeat("v", maxEnvValueLen),
	}
	// 总量恰好等于 maxSessionEnvSize:证明总量检查用的是 ">" 而非 ">="。
	// 3*(1+1+maxEnvValueLen) + (1+1+maxEnvValueLen-8) == maxSessionEnvSize。
	atLimitEnv := []string{
		"A=" + strings.Repeat("v", maxEnvValueLen),
		"B=" + strings.Repeat("v", maxEnvValueLen),
		"C=" + strings.Repeat("v", maxEnvValueLen),
		"D=" + strings.Repeat("v", maxEnvValueLen-8),
	}

	zero, neg := 0, -1
	// 上限本身合法、上限 +1 拒绝:边界用包内常量,上限漂移会直接失败。
	scrollbackAtLimit, scrollbackTooLarge := maxSessionScrollback, maxSessionScrollback+1
	termAtLimit := strings.Repeat("t", maxTermLen)

	cases := []struct {
		name    string
		req     createSessionRequest
		wantErr string // 空 = 合法
	}{
		{"empty request", createSessionRequest{}, ""},
		{"all params valid", createSessionRequest{
			Env:        []string{"FOO=bar", "A_B9=x"},
			Cwd:        dir,
			Term:       "xterm-256color",
			Scrollback: &zero,
		}, ""},
		{"scrollback nil is valid", createSessionRequest{Cwd: dir}, ""},
		{"env total at the limit", createSessionRequest{Env: atLimitEnv}, ""},
		{"term at the limit", createSessionRequest{Term: termAtLimit}, ""},

		{"cwd missing", createSessionRequest{Cwd: filepath.Join(dir, "nope")}, "cwd does not exist"},
		{"cwd not a directory", createSessionRequest{Cwd: file}, "cwd is not a directory"},

		{"env without =", createSessionRequest{Env: []string{"FOO"}}, "expected K=V"},
		{"env empty", createSessionRequest{Env: []string{""}}, "expected K=V"},
		{"env empty name", createSessionRequest{Env: []string{"=bar"}}, "expected K=V"},
		{"env name starts with a digit", createSessionRequest{Env: []string{"1FOO=bar"}}, "bad variable name"},
		{"env name with dash", createSessionRequest{Env: []string{"FOO-BAR=1"}}, "bad variable name"},
		{"env name with space", createSessionRequest{Env: []string{"FOO BAR=1"}}, "bad variable name"},
		{"env name too long", createSessionRequest{Env: []string{longName + "=1"}}, "variable name too long"},
		{"env value too long", createSessionRequest{Env: []string{"A=" + longValue}}, "value too long"},
		{"env too many entries", createSessionRequest{Env: tooMany}, "too many env entries"},
		{"env total too large", createSessionRequest{Env: tooLarge}, "env too large"},

		{"term with ESC", createSessionRequest{Term: "xterm\x1b[31m"}, "control characters"},
		{"term with newline", createSessionRequest{Term: "xterm\n"}, "control characters"},
		{"term with DEL", createSessionRequest{Term: "xterm\x7f"}, "control characters"},
		{"term too long", createSessionRequest{Term: strings.Repeat("x", maxTermLen+1)}, "term too long"},

		{"scrollback negative", createSessionRequest{Scrollback: &neg}, "scrollback must be >= 0"},
		{"scrollback at the limit", createSessionRequest{Scrollback: &scrollbackAtLimit}, ""},
		{"scrollback too large", createSessionRequest{Scrollback: &scrollbackTooLarge}, "scrollback too large"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSessionParams(&tc.req)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSessionParams() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateSessionParams() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateSessionParams() = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestValidateSessionParamsHTTP:同一套规则必须在创建端点上表现为 400(0005
// §4 的验收写法),并带上可读的 error 文本;合法组合则必须是 201,证明校验
// 没有过度拒绝。
func TestValidateSessionParamsHTTP(t *testing.T) {
	ts := cfgNewSessionServer(t, 0)

	dir := t.TempDir()
	file := filepath.Join(dir, "regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to create %q: %s", file, err)
	}

	// 校验发生在启动进程之前:失败用例故意用一个不存在的命令,这样 400 只
	// 可能来自参数校验;若校验被漏掉,返回的会是 500 "failed to create
	// session",断言会直接暴露问题。
	const neverRun = "cfg-command-that-must-never-run"

	tooMany := make([]string, 0, maxSessionEnvVars+1)
	for i := 0; i <= maxSessionEnvVars; i++ {
		tooMany = append(tooMany, fmt.Sprintf("CFG_%d=1", i))
	}
	tooLarge := []string{
		"A=" + strings.Repeat("v", maxEnvValueLen),
		"B=" + strings.Repeat("v", maxEnvValueLen),
		"C=" + strings.Repeat("v", maxEnvValueLen),
		"D=" + strings.Repeat("v", maxEnvValueLen),
	}
	neg := -1

	cases := []struct {
		name    string
		mutate  func(*createSessionRequest)
		wantErr string
	}{
		{"cwd missing", func(r *createSessionRequest) { r.Cwd = filepath.Join(dir, "nope") }, "cwd does not exist"},
		{"cwd not a directory", func(r *createSessionRequest) { r.Cwd = file }, "cwd is not a directory"},
		{"env not K=V", func(r *createSessionRequest) { r.Env = []string{"FOO"} }, "expected K=V"},
		{"env bad variable name", func(r *createSessionRequest) { r.Env = []string{"1FOO=bar"} }, "bad variable name"},
		{"env name too long", func(r *createSessionRequest) {
			r.Env = []string{strings.Repeat("A", maxEnvNameLen+1) + "=1"}
		}, "variable name too long"},
		{"env value too long", func(r *createSessionRequest) {
			r.Env = []string{"A=" + strings.Repeat("v", maxEnvValueLen+1)}
		}, "value too long"},
		{"env too many entries", func(r *createSessionRequest) { r.Env = tooMany }, "too many env entries"},
		{"env total too large", func(r *createSessionRequest) { r.Env = tooLarge }, "env too large"},
		{"term with control character", func(r *createSessionRequest) { r.Term = "xterm\x1b[31m" }, "control characters"},
		{"term too long", func(r *createSessionRequest) {
			r.Term = strings.Repeat("x", maxTermLen+1)
		}, "term too long"},
		{"scrollback negative", func(r *createSessionRequest) { r.Scrollback = &neg }, "scrollback must be >= 0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := createSessionRequest{Command: neverRun}
			tc.mutate(&req)

			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("failed to encode request: %s", err)
			}
			status, msg := cfgCreateStatus(t, ts, raw)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d (want %d), error %q", status, http.StatusBadRequest, msg)
			}
			if !strings.Contains(msg, tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", msg, tc.wantErr)
			}
		})
	}

	// 合法组合(四个参数一起传)必须被接受:这是上面所有 400 的对照,
	// 防止校验把正常请求也挡掉。
	cmd, args := cfgQuickCommand(t)
	zero := 0
	valid := createSessionRequest{
		Command:    cmd,
		Args:       args,
		Env:        []string{"CFG_HTTP_OK=1"},
		Cwd:        dir,
		Term:       "xterm-256color",
		Scrollback: &zero,
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("failed to encode request: %s", err)
	}
	if status, msg := cfgCreateStatus(t, ts, raw); status != http.StatusCreated {
		t.Fatalf("valid params: status %d (want %d), error %q", status, http.StatusCreated, msg)
	}
}
