package memory

import (
	"strings"
	"testing"
)

func TestItemKeyNormalizesWhitespaceAndCase(t *testing.T) {
	a := ItemKey("用户希望被称为  陈总")
	b := ItemKey("  用户希望被称为 陈总 ")
	c := ItemKey("User Prefers HKD")
	if a != b {
		t.Fatalf("whitespace variants should converge: %s vs %s", a, b)
	}
	if c != ItemKey("user prefers hkd") {
		t.Fatalf("ascii case should converge")
	}
	if a == c {
		t.Fatalf("different content should produce different keys")
	}
	if len(a) != 16 {
		t.Fatalf("itemKey should be 16 hex chars, got %d", len(a))
	}
}

func TestNormalizeTagsTrimsAndDedupes(t *testing.T) {
	got := NormalizeTags(" 偏好 ,报表, 报表 ,, ")
	if got != "偏好,报表" {
		t.Fatalf("unexpected tags: %q", got)
	}
	if NormalizeTags("") != "" {
		t.Fatalf("empty tags should stay empty")
	}
}

func TestValidatePayloadRules(t *testing.T) {
	if err := ValidatePayload("", "内容", "提示"); err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("empty title should fail, got %v", err)
	}
	if err := ValidatePayload(strings.Repeat("标", 33), "内容", "提示"); err == nil || !strings.Contains(err.Error(), "title 超长") {
		t.Fatalf("long title should fail, got %v", err)
	}
	if err := ValidatePayload("标题", strings.Repeat("文", 501), "提示"); err == nil || !strings.Contains(err.Error(), "content 超长") {
		t.Fatalf("long content should fail, got %v", err)
	}
	if err := ValidatePayload("标题", "内容", "提示"); err != nil {
		t.Fatalf("valid payload should pass, got %v", err)
	}
}

func TestScanSensitiveContentBlocksCredentialShapes(t *testing.T) {
	cases := []struct {
		name  string
		field string
		hit   string
	}{
		{"openai-key", "密钥是 sk-abcdef0123456789abcdef0123456789 请保管", "API Key"},
		{"aws-access-key", "AKIAIOSFODNN7EXAMPLE 是示例凭证", "AccessKey"},
		{"github-token", "token ghp_0123456789abcdefghijklmnopqrstuv 已泄露", "Token"},
		{"jwt", "鉴权头 eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.SflKxwRJSMeKKF2QT4", "JWT"},
		{"private-key", "-----BEGIN RSA PRIVATE KEY-----", "私钥"},
		{"id-card", "证件号 11010519491231002X 归档", "身份证"},
		{"phone", "联系 13812345678 处理", "手机号"},
	}
	for _, tc := range cases {
		err := ScanSensitiveContent(tc.field)
		if err == nil || !strings.Contains(err.Error(), "敏感信息") {
			t.Fatalf("%s should be blocked, got %v", tc.name, err)
		}
	}
}

func TestScanSensitiveContentAllowsNormalText(t *testing.T) {
	if err := ScanSensitiveContent("用户希望被称为陈总", "报表口径用 HKD", "涉及报表输出时", ""); err != nil {
		t.Fatalf("normal memory text should pass, got %v", err)
	}
	if err := ScanSensitiveContent("", "", ""); err != nil {
		t.Fatalf("empty fields should pass, got %v", err)
	}
}

func TestOwnerScopeKeyStable(t *testing.T) {
	if OwnerScopeKey("app", "demo-app") != "app|demo-app" {
		t.Fatalf("unexpected key format")
	}
	if OwnerScopeKey("app_user", "demo-app|zhangsan") == OwnerScopeKey("app", "demo-app|zhangsan") {
		t.Fatalf("different owner types must not collide")
	}
}

func TestItemHasLockedTag(t *testing.T) {
	if !ItemHasLockedTag("偏好,locked,报表") {
		t.Fatalf("locked tag should be detected")
	}
	if ItemHasLockedTag("偏好,lockeddown,报表") || ItemHasLockedTag("mylocked") || ItemHasLockedTag("") {
		t.Fatalf("locked detection must be exact tag match")
	}
}
