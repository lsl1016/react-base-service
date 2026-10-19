package command

import (
	"strings"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		prompt string
		name   string
		args   string
		ok     bool
	}{
		{"/compact", "compact", "", true},
		{"/compact  附加指令 ", "compact", "附加指令", true},
		{"/compress", "compress", "", true},
		{"/workflow 每天 8 点跑一次", "workflow", "每天 8 点跑一次", true},
		{"/a", "a", "", true},
		// 非 command：消息中间的路径、大写、非法字符、多级路径
		{"看下 /etc/hosts 这个文件", "", "", false},
		{"/Compact", "", "", false},
		{"/etc/hosts 看一下", "", "", false},
		{"普通消息", "", "", false},
		{"/", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		name, args, ok := SplitCommand(c.prompt)
		if ok != c.ok || name != c.name || args != c.args {
			t.Fatalf("SplitCommand(%q) = (%q, %q, %v), want (%q, %q, %v)", c.prompt, name, args, ok, c.name, c.args, c.ok)
		}
	}
}

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a b c", []string{"a", "b", "c"}},
		{`a "b c" d`, []string{"a", "b c", "d"}},
		{`"未闭合引号 整体`, []string{"未闭合引号 整体"}},
		{"  多个   空白  ", []string{"多个", "空白"}},
	}
	for _, c := range cases {
		got := SplitArgs(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("SplitArgs(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("SplitArgs(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestExpandTemplate(t *testing.T) {
	// $ARGUMENTS 替换 + 包装头
	out := ExpandTemplate("demo", "请处理：$ARGUMENTS", "任务甲")
	if !strings.Contains(out, "执行自定义命令 /demo") || !strings.Contains(out, "请处理：任务甲") {
		t.Fatalf("unexpected expansion: %s", out)
	}
	// 位置参数
	out = ExpandTemplate("demo", "第一 $1 第二 $2 缺省 $3", `a b`)
	if !strings.Contains(out, "第一 a 第二 b 缺省") {
		t.Fatalf("unexpected positional expansion: %s", out)
	}
	// 无占位符：参数附尾
	out = ExpandTemplate("demo", "固定模板正文", "参数甲")
	if !strings.Contains(out, "固定模板正文") || !strings.Contains(out, "User arguments:\n参数甲") {
		t.Fatalf("unexpected tail-args expansion: %s", out)
	}
	// 无占位符且无参数：不附尾
	out = ExpandTemplate("demo", "固定模板正文", "")
	if strings.Contains(out, "User arguments") {
		t.Fatalf("unexpected tail-args for empty args: %s", out)
	}
	// 明确不做 shell 展开：!`cmd` 语法原样保留（未执行也未剥离）
	out = ExpandTemplate("demo", "!`echo hi` $ARGUMENTS", "x")
	if !strings.Contains(out, "!`echo hi`") {
		t.Fatalf("shell syntax should be preserved verbatim, got: %s", out)
	}
}

func TestBuiltinRegistry(t *testing.T) {
	if def := LookupBuiltin("compress"); def == nil || def.Name != "compact" {
		t.Fatalf("alias compress should resolve to compact")
	}
	if def := LookupBuiltin("compact"); def == nil || def.Kind != KindFlaggedRun {
		t.Fatalf("compact should be a flagged-run command")
	}
	for _, reserved := range []string{"compact", "compress", "workflow", "plan", "react", "help", "new"} {
		if !IsReservedName(reserved) {
			t.Fatalf("%s should be reserved", reserved)
		}
	}
	if IsReservedName("mycommand") {
		t.Fatalf("mycommand should not be reserved")
	}
}
