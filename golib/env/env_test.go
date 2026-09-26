package env

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandEnvRefs(t *testing.T) {
	t.Setenv("LLM_ENV_TEST_KEY", "abc123")
	t.Setenv("LLM_ENV_TEST_EMPTY", "")

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"普通文本不展开", "api_url: https://example.com", "api_url: https://example.com"},
		{"基础引用", `key: "${LLM_ENV_TEST_KEY}"`, `key: "abc123"`},
		{"带默认值且变量存在", `key: "${LLM_ENV_TEST_KEY:-fallback}"`, `key: "abc123"`},
		{"带默认值且变量为空", `key: "${LLM_ENV_TEST_EMPTY:-fallback}"`, `key: "fallback"`},
		{"未设置且无默认值展开为空", `key: "${LLM_ENV_TEST_MISSING}"`, `key: ""`},
		{"多处引用", `a: ${LLM_ENV_TEST_KEY}, b: ${LLM_ENV_TEST_KEY:-x}`, `a: abc123, b: abc123`},
		{"小写与数字变量名", "${lower_case_1}", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := expandEnvRefs(tc.input); got != tc.want {
				t.Fatalf("expandEnvRefs(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// LoadConf 端到端：写入含 ${VAR} 的临时配置文件，加载后应得到展开后的值。
func TestLoadConfExpandsEnvRefs(t *testing.T) {
	t.Setenv("LLM_ENV_TEST_APIKEY", "from-env")

	root := t.TempDir()
	mount := filepath.Join(root, "conf", "mount")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "llm:\n  api_keys:\n    default: \"${LLM_ENV_TEST_APIKEY}\"\n"
	if err := os.WriteFile(filepath.Join(mount, "test.yaml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	previousRoot := rootPath
	SetRootPath(root)
	t.Cleanup(func() { SetRootPath(previousRoot) })

	var target struct {
		LLM struct {
			ApiKeys map[string]string `yaml:"api_keys"`
		} `yaml:"llm"`
	}
	LoadConf("test.yaml", SubConfMount, &target)
	if got := target.LLM.ApiKeys["default"]; got != "from-env" {
		t.Fatalf("api key = %q, want %q", got, "from-env")
	}
}
