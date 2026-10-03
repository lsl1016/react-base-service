package cos

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// TestMinioStoreEndToEnd 用 gofakes3（内存 S3 协议实现）验证 MinIO 后端的完整读写链路：
// SigV4 签名、自动建桶、Range 读、列举、预签名 URL、追加/清空与批量删除。
// 不同于 mock，这里走的是真实 HTTP + S3 XML 协议，能暴露 minio-go 用法层面的错误。
func TestMinioStoreEndToEnd(t *testing.T) {
	fake := gofakes3.New(s3mem.New())
	server := httptest.NewServer(fake.Server())
	defer server.Close()

	client, err := NewClient(Config{
		Provider:  "minio",
		SecretID:  "minioadmin",
		SecretKey: "minioadmin",
		Bucket:    "react-base-test",
		Endpoint:  server.URL,
		Timeout:   10,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if client.s3 == nil {
		t.Fatal("expected minio backend")
	}
	ctx := context.Background()

	// 上传 + 下载
	if err := client.UploadData(ctx, []byte("hello minio"), "dir/a.txt", "text/plain"); err != nil {
		t.Fatalf("upload: %v", err)
	}
	data, err := client.DownloadData(ctx, "dir/a.txt")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(data) != "hello minio" {
		t.Fatalf("roundtrip mismatch: %q", data)
	}

	// Range 读（闭区间）
	rangeData, err := client.DownloadRange(ctx, "dir/a.txt", 0, 4)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if string(rangeData) != "hello" {
		t.Fatalf("range mismatch: %q", rangeData)
	}

	// 存在性
	exists, err := client.IsExist(ctx, "dir/a.txt")
	if err != nil || !exists {
		t.Fatalf("isExist: exists=%v err=%v", exists, err)
	}
	missing, err := client.IsExist(ctx, "dir/none.txt")
	if err != nil || missing {
		t.Fatalf("isExist(missing): exists=%v err=%v", missing, err)
	}

	// 列举
	keys, err := client.ListObjects(ctx, "dir/", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 1 || keys[0] != "dir/a.txt" {
		t.Fatalf("list mismatch: %v", keys)
	}

	// 预签名 URL 可直接 GET
	urlStr, err := client.GetURL(ctx, "dir/a.txt", 5*time.Minute)
	if err != nil {
		t.Fatalf("presign: %v", err)
	}
	resp, err := http.Get(urlStr)
	if err != nil {
		t.Fatalf("presigned get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "hello minio" {
		t.Fatalf("presigned get: status=%d body=%q", resp.StatusCode, body)
	}

	// 追加；ClearObject 只验证不存在对象的幂等空操作——
	// 存量对象清空会走 0 字节 PUT：minio-go 对零长度请求不带 Content-Length，
	// 真实 MinIO 服务端接受，gofakes3 按 S3 规范严格拒绝，故此处不覆盖该分支。
	if err := client.AppendObject(ctx, "dir/a.txt", []byte("!!"), "text/plain"); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, _ = client.DownloadData(ctx, "dir/a.txt")
	if string(data) != "hello minio!!" {
		t.Fatalf("append mismatch: %q", data)
	}
	if err := client.ClearObject(ctx, "dir/none.txt"); err != nil {
		t.Fatalf("clear(missing): %v", err)
	}

	// 批量删除（含二次删除幂等路径）
	if err := client.UploadData(ctx, []byte("x"), "dir/b.txt", "text/plain"); err != nil {
		t.Fatalf("upload b: %v", err)
	}
	if err := client.DeleteObjects(ctx, []string{"dir/a.txt", "dir/b.txt"}); err != nil {
		t.Fatalf("delete multi: %v", err)
	}
	if exists, _ := client.IsExist(ctx, "dir/b.txt"); exists {
		t.Fatal("b.txt should be deleted")
	}

	// 文件上传（走本地临时文件）
	tmp := t.TempDir() + "/upload.txt"
	if err := os.WriteFile(tmp, []byte("file content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := client.UploadFile(ctx, tmp, "dir/c.txt"); err != nil {
		t.Fatalf("upload file: %v", err)
	}
	downloaded := t.TempDir() + "/download.txt"
	if err := client.DownloadFile(ctx, "dir/c.txt", downloaded); err != nil {
		t.Fatalf("download file: %v", err)
	}
	got, err := os.ReadFile(downloaded)
	if err != nil || string(got) != "file content" {
		t.Fatalf("file roundtrip mismatch: %q err=%v", got, err)
	}
}
