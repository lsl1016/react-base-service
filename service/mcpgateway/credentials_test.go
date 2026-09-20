package mcpgateway

import "testing"

func TestParseBearerCredential(t *testing.T) {
	cases := []struct {
		name       string
		header     string
		wantKey    string
		wantSecret string
		wantOK     bool
	}{
		{"标准格式", "Bearer mcp_ab12:secret", "mcp_ab12", "secret", true},
		{"secret 含冒号", "Bearer key:se:cret", "key", "se:cret", true},
		{"小写 bearer", "bearer key:secret", "", "", false},
		{"缺少分隔符", "Bearer onlykey", "", "", false},
		{"key 为空", "Bearer :secret", "", "", false},
		{"secret 为空", "Bearer key:", "", "", false},
		{"空头部", "", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, secret, ok := parseBearerCredential(tc.header)
			if ok != tc.wantOK || key != tc.wantKey || secret != tc.wantSecret {
				t.Fatalf("parse(%q) = (%q,%q,%v)，期望 (%q,%q,%v)",
					tc.header, key, secret, ok, tc.wantKey, tc.wantSecret, tc.wantOK)
			}
		})
	}
}

func TestMaskSecret(t *testing.T) {
	if got := MaskSecret("abcdefghijklmnop"); got != "abcd****" {
		t.Fatalf("长 secret 打码不符合预期: %q", got)
	}
	if got := MaskSecret("short"); got != "****" {
		t.Fatalf("短 secret 应整体打码: %q", got)
	}
}

func TestGenerateCredentials(t *testing.T) {
	key := GenerateAppKey()
	secret := GenerateAppSecret()
	if len(key) < 8 || len(secret) < 32 {
		t.Fatalf("凭证长度不足: key=%q secret=%q", key, secret)
	}
	if key == GenerateAppKey() || secret == GenerateAppSecret() {
		t.Fatal("凭证生成应具备随机性")
	}
	if appID := NewMcpAppID(); len(appID) < len("mcpapp_") {
		t.Fatalf("应用 ID 不符合预期: %q", appID)
	}
}
