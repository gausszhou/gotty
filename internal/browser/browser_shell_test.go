//go:build browser_e2e

package browser

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// shellStep 是"原样往 stdout 写一段字节,然后等待"这样的一步。
type shellStep struct {
	write string
	sleep time.Duration
}

// shellCmd 把若干 shellStep 翻译成一条当前平台的命令。
//
// 为什么需要这层:这些用例原先直接硬编码 `Command: "/bin/sh"`,在 Windows 上
// 根本没有 /bin/sh —— 于是 `make test-browser` 不是"跳过"而是"直接失败"。
// 而这两件事都必须成立才能验到渲染:
//   - **逐字节**把 ESC / BEL 这类控制字符写进 PTY(IIP 图片用例整条序列都是控制字符);
//   - 能在中间"等待",让 quiet/marker 的判定发生在进程还活着的时候。
//
// 所以两端各用一种可靠写法,而不是去找"两边都有的 shell"(并不存在):
//
//	unix    → `sh -c "printf '%s' '...'"`:单引号内 shell 不做任何转义,字节原样;
//	windows → PowerShell 把字节数组写到 stdout 句柄。**不能**用 cmd 的
//	          echo / set /p:控制字符会被吃掉,行长也有限制。
func shellCmd(steps ...shellStep) (string, []string) {
	if runtime.GOOS == "windows" {
		return "powershell.exe", []string{
			"-NoProfile", "-NonInteractive", "-Command", windowsScript(steps),
		}
	}
	return "/bin/sh", []string{"-c", unixScript(steps)}
}

func unixScript(steps []shellStep) string {
	var b strings.Builder
	for _, s := range steps {
		if s.write != "" {
			// 先写字节,再交给 printf 的 %s 原样输出(格式串本身不含用户数据)
			fmt.Fprintf(&b, "printf '%%s' %s; ", singleQuote(s.write))
		}
		if s.sleep > 0 {
			fmt.Fprintf(&b, "sleep %s; ", strconv.FormatFloat(s.sleep.Seconds(), 'f', -1, 64))
		}
	}
	return b.String()
}

func windowsScript(steps []shellStep) string {
	var b strings.Builder
	b.WriteString("$o=[Console]::OpenStandardOutput(); ")
	for _, s := range steps {
		if s.write != "" {
			codes := make([]string, 0, len(s.write))
			for i := 0; i < len(s.write); i++ {
				codes = append(codes, strconv.Itoa(int(s.write[i])))
			}
			fmt.Fprintf(&b, "$b=[byte[]]@(%s); $o.Write($b,0,$b.Length); $o.Flush(); ",
				strings.Join(codes, ","))
		}
		if s.sleep > 0 {
			fmt.Fprintf(&b, "Start-Sleep -Milliseconds %d; ", s.sleep.Milliseconds())
		}
	}
	return b.String()
}

// singleQuote 用单引号包裹一段文本供 POSIX shell 使用。
func singleQuote(s string) string {
	// 内部单引号按 POSIX 惯用写法断开:闭引号、反斜杠转义的单引号、再开引号。
	// 注意别把那个字面序列写进**文档注释**:gofmt 会对 doc comment 做
	// "smart quotes" 替换,会把它改成一个语法错误的弯引号(实测踩过)。
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestShellCmdScript 直接断言两个编码器。这是本文件里唯一"在任何平台都能跑"的检查,
// 因此也是 unix 分支唯一的自动化覆盖 —— 用例本身在 Windows 上永远走 Windows 分支,
// 光靠跑用例证明不了 sh -c 那串拼得对(反过来在 Linux 上同理)。
func TestShellCmdScript(t *testing.T) {
	// 单引号必须断开转义(否则 shell 报未闭合),ESC 必须原样保留
	gotUnix := unixScript([]shellStep{
		{write: "a'b\x1b"},
		{sleep: 400 * time.Millisecond},
		{write: "c"},
		{sleep: 2 * time.Second},
	})
	wantUnix := `printf '%s' 'a'\''b` + "\x1b" + `'; sleep 0.4; printf '%s' 'c'; sleep 2; `
	if gotUnix != wantUnix {
		t.Errorf("unixScript =\n  %q\nwant\n  %q", gotUnix, wantUnix)
	}

	// Windows:控制字符走字节数组(ESC=27, BEL=7),不经过任何字符串编码
	gotWin := windowsScript([]shellStep{{write: "\x1b\x07"}, {sleep: 2 * time.Second}})
	wantWin := `$o=[Console]::OpenStandardOutput(); ` +
		`$b=[byte[]]@(27,7); $o.Write($b,0,$b.Length); $o.Flush(); ` +
		`Start-Sleep -Milliseconds 2000; `
	if gotWin != wantWin {
		t.Errorf("windowsScript =\n  %q\nwant\n  %q", gotWin, wantWin)
	}

	// 空步骤不该产生多余命令
	if got := unixScript([]shellStep{{sleep: 0}}); got != "" {
		t.Errorf("empty step produced %q, want empty", got)
	}
}
