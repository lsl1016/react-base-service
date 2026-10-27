package mcpclient

import (
	"net"
	"strings"
	"testing"
)

func TestValidateEndpoint_SchemeAndHost(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		wantErr  string
	}{
		{"空", "", "empty"},
		{"非http协议", "ftp://1.2.3.4/mcp", "http/https"},
		{"缺host", "https:///mcp", "missing host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateEndpoint(tc.endpoint)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateEndpoint(%q) = %v, want error containing %q", tc.endpoint, err, tc.wantErr)
			}
		})
	}
}

func TestValidateEndpoint_IPLiteral(t *testing.T) {
	cases := []struct {
		ip     string
		public bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"127.0.0.1", false},
		{"10.1.2.3", false},
		{"192.168.1.1", false},
		{"172.16.0.1", false},
		{"169.254.1.1", false},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"100.64.1.1", false},  // CGNAT
		{"192.0.2.1", false},   // TEST-NET
		{"240.0.0.1", false},   // 保留段
		{"::1", false},         // IPv6 环回
		{"fe80::1", false},     // IPv6 链路本地
		{"fd00::1", false},     // IPv6 ULA
		{"2001:db8::1", false}, // IPv6 文档段
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			endpoint := "https://" + tc.ip + "/mcp"
			if strings.Contains(tc.ip, ":") {
				endpoint = "https://[" + tc.ip + "]/mcp" // IPv6 字面量在 URL 中需方括号包裹
			}
			_, err := validateEndpoint(endpoint)
			if tc.public && err != nil {
				t.Fatalf("public ip %s rejected: %v", tc.ip, err)
			}
			if !tc.public && (err == nil || !strings.Contains(err.Error(), "loopback/private/reserved")) {
				t.Fatalf("non-public ip %s should be rejected, got %v", tc.ip, err)
			}
		})
	}
}

func TestValidateEndpoint_HostnameResolution(t *testing.T) {
	original := resolveHost
	defer func() { resolveHost = original }()

	resolveHost = func(host string) []net.IP {
		switch host {
		case "good.example.com":
			return []net.IP{net.ParseIP("8.8.8.8")}
		case "evil.example.com": // DNS 解析出私网地址（含公网混合）→ 拒绝
			return []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("10.0.0.5")}
		default:
			return nil
		}
	}

	if _, err := validateEndpoint("https://good.example.com/mcp"); err != nil {
		t.Fatalf("public hostname rejected: %v", err)
	}
	if _, err := validateEndpoint("https://evil.example.com/mcp"); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("hostname resolving to private ip should be rejected, got %v", err)
	}
	if _, err := validateEndpoint("https://unknown.example.com/mcp"); err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
		t.Fatalf("unresolvable hostname should be rejected, got %v", err)
	}
}

func TestNewHTTPClient_RejectsLocalEndpoint(t *testing.T) {
	if _, err := NewHTTPClient("local", "http://127.0.0.1:9999/mcp", 0); err == nil {
		t.Fatal("loopback endpoint should be rejected at construction")
	}
	if _, err := NewHTTPClient("private", "http://192.168.1.10:8080/mcp", 0); err == nil {
		t.Fatal("private endpoint should be rejected at construction")
	}
}

func TestValidateEndpointOpts_AllowPrivate(t *testing.T) {
	// 默认（严格）仍拒绝环回/私网
	if _, err := validateEndpointOpts("http://127.0.0.1:18080/api/mcp", false); err == nil {
		t.Fatal("strict mode should reject loopback")
	}
	// allowPrivate=true 放行环回与私网（本机开发环境开关）
	if _, err := validateEndpointOpts("http://127.0.0.1:18080/api/mcp", true); err != nil {
		t.Fatalf("allowPrivate should accept loopback: %v", err)
	}
	if _, err := validateEndpointOpts("http://192.168.1.10:8080/mcp", true); err != nil {
		t.Fatalf("allowPrivate should accept private: %v", err)
	}
	// 即便 allowPrivate 也仍拒绝非法 scheme 与空 host
	if _, err := validateEndpointOpts("file:///etc/passwd", true); err == nil {
		t.Fatal("allowPrivate should still reject non-http scheme")
	}
	if _, err := validateEndpointOpts("http://", true); err == nil {
		t.Fatal("allowPrivate should still reject missing host")
	}
}

func TestNewHTTPClientOpts_HeadersNormalization(t *testing.T) {
	client, err := NewHTTPClientOpts("demo", "http://127.0.0.1:18090/api/mcp", 1000, map[string]string{
		"Authorization":  "Bearer k:s",
		"HOST":           "evil.example.com",
		"Content-Type":   "text/plain",
		"Mcp-Session-Id": "hijack",
		"X-Custom":       "v",
	}, true)
	if err != nil {
		t.Fatalf("construction failed: %v", err)
	}
	if _, ok := client.headers["authorization"]; !ok || client.headers["authorization"] != "Bearer k:s" {
		t.Fatalf("authorization header should be kept lowercased: %v", client.headers)
	}
	if _, ok := client.headers["x-custom"]; !ok {
		t.Fatalf("custom header should be kept: %v", client.headers)
	}
	for _, managed := range []string{"host", "content-type", "mcp-session-id"} {
		if _, ok := client.headers[managed]; ok {
			t.Fatalf("managed header %s must be stripped", managed)
		}
	}
}
