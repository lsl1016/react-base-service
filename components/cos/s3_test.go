package cos

import (
	"testing"
)

// 后端选择均为离线断言：minio 客户端建桶是首次访问时才发起的（lazy ensureBucket），
// NewClient 本身不产生网络请求。
func TestNewClientProviderSelection(t *testing.T) {
	t.Run("provider minio 选中 s3 后端", func(t *testing.T) {
		client, err := NewClient(Config{
			Provider:  "minio",
			SecretID:  "minioadmin",
			SecretKey: "minioadmin",
			Bucket:    "react-base",
			Endpoint:  "http://127.0.0.1:9000",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client.s3 == nil || client.local != nil || client.raw != nil {
			t.Fatalf("expected minio backend, got s3=%v local=%v raw=%v", client.s3, client.local, client.raw)
		}
	})

	t.Run("provider 优先于 localDir", func(t *testing.T) {
		client, err := NewClient(Config{
			Provider:  "s3",
			SecretID:  "minioadmin",
			SecretKey: "minioadmin",
			Bucket:    "react-base",
			Endpoint:  "http://127.0.0.1:9000",
			LocalDir:  t.TempDir(),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client.s3 == nil || client.local != nil {
			t.Fatalf("provider should take precedence over localDir, got s3=%v local=%v", client.s3, client.local)
		}
	})

	t.Run("留空 provider 且配置 localDir 兼容旧本地模式", func(t *testing.T) {
		client, err := NewClient(Config{LocalDir: t.TempDir()})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client.local == nil || client.s3 != nil {
			t.Fatalf("expected local backend, got local=%v s3=%v", client.local, client.s3)
		}
	})

	t.Run("provider local 缺少 localDir 报错", func(t *testing.T) {
		if _, err := NewClient(Config{Provider: "local"}); err == nil {
			t.Fatal("expected error for provider=local without localDir")
		}
	})

	t.Run("minio 配置不完整报错", func(t *testing.T) {
		if _, err := NewClient(Config{Provider: "minio", SecretID: "a", SecretKey: "b", Bucket: "c"}); err == nil {
			t.Fatal("expected error for missing endpoint")
		}
	})

	t.Run("未知 provider 报错", func(t *testing.T) {
		if _, err := NewClient(Config{Provider: "oss"}); err == nil {
			t.Fatal("expected error for unknown provider")
		}
	})
}

func TestParseS3Endpoint(t *testing.T) {
	tests := []struct {
		endpoint  string
		wantHost  string
		wantTLS   bool
		wantError bool
	}{
		{endpoint: "http://127.0.0.1:9000", wantHost: "127.0.0.1:9000"},
		{endpoint: "https://minio.internal:9000/", wantHost: "minio.internal:9000", wantTLS: true},
		{endpoint: "minio:9000", wantHost: "minio:9000"},
		{endpoint: "  ", wantError: true},
	}
	for _, tc := range tests {
		host, secure, err := parseS3Endpoint(tc.endpoint)
		if tc.wantError {
			if err == nil {
				t.Fatalf("endpoint %q: expected error", tc.endpoint)
			}
			continue
		}
		if err != nil {
			t.Fatalf("endpoint %q: unexpected error: %v", tc.endpoint, err)
		}
		if host != tc.wantHost || secure != tc.wantTLS {
			t.Fatalf("endpoint %q: got host=%s secure=%v", tc.endpoint, host, secure)
		}
	}
}
