package react

import (
	"strings"
	"testing"
)

func TestHumanizeBytes(t *testing.T) {
	cases := map[int64]string{
		512:             "512B",
		2048:            "2.0KB",
		3 * 1024 * 1024: "3.0MB",
	}
	for size, want := range cases {
		if got := humanizeBytes(size); got != want {
			t.Fatalf("humanizeBytes(%d) = %q, want %q", size, got, want)
		}
	}
}

func TestValidateReactAttachmentTotalSize(t *testing.T) {
	if err := validateReactAttachmentTotalSize(50 * 1024 * 1024); err != nil {
		t.Fatalf("50MB should be accepted: %v", err)
	}
	if err := validateReactAttachmentTotalSize(50*1024*1024 + 1); err == nil || !strings.Contains(err.Error(), "附件总大小不能超过 50MB") {
		t.Fatalf("size over 50MB should be rejected, got: %v", err)
	}
}

func TestAttachmentHeadPreview(t *testing.T) {
	// 普通多行：全部在 maxLines 内，不截断。
	head, total, truncated := attachmentHeadPreview("a,b,c\n1,2,3\n4,5,6\n", 30, 8192)
	if total != 3 {
		t.Fatalf("total lines = %d, want 3", total)
	}
	if len(head) != 3 || head[0] != "a,b,c" || head[2] != "4,5,6" {
		t.Fatalf("unexpected head: %#v", head)
	}
	if truncated {
		t.Fatalf("should not be truncated")
	}

	// 行数超过 maxLines：截断且总行数如实统计。
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString("line\n")
	}
	head, total, truncated = attachmentHeadPreview(sb.String(), 30, 8192)
	if total != 100 {
		t.Fatalf("total lines = %d, want 100", total)
	}
	if len(head) != 30 {
		t.Fatalf("head len = %d, want 30", len(head))
	}
	if !truncated {
		t.Fatalf("should be truncated")
	}

	// 空内容。
	head, total, truncated = attachmentHeadPreview("", 30, 8192)
	if total != 0 || len(head) != 0 || truncated {
		t.Fatalf("empty content: total=%d head=%d truncated=%v", total, len(head), truncated)
	}

	// 单行超长：受 maxBytes 约束截断。
	head, total, truncated = attachmentHeadPreview(strings.Repeat("x", 20000), 30, 8192)
	if total != 1 {
		t.Fatalf("single long line total = %d, want 1", total)
	}
	if !truncated {
		t.Fatalf("single long line should mark truncated by byte cap")
	}
}

func TestRenderAttachmentManifestEmpty(t *testing.T) {
	if got := renderAttachmentManifest(nil); got != "" {
		t.Fatalf("expected empty manifest, got %q", got)
	}
}

func TestRenderAttachmentManifestMetadataOnly(t *testing.T) {
	snapshots := []reactAttachmentSnapshot{
		{FileID: "file_a", FileName: "sales.csv", Ext: "csv", Size: 8_400_000, Description: "全年销售"},
		{FileID: "file_b", FileName: "note.txt", Ext: "txt", Size: 1200},
	}
	manifest := renderAttachmentManifest(snapshots)
	for _, expected := range []string{
		"<attachments>", "</attachments>",
		"file_a", "sales.csv", "csv", "8.0MB", "全年销售",
		"file_b", "note.txt",
		"read_attachment", "inspect_attachment", "python_exec",
	} {
		if !strings.Contains(manifest, expected) {
			t.Fatalf("manifest missing %q:\n%s", expected, manifest)
		}
	}
	// 清单只给元信息，不应回退到旧的逐文件内容注入标记（<attachment index=...>文件内容：...）。
	if strings.Contains(manifest, "<attachment index=") {
		t.Fatalf("manifest must not embed per-file content block:\n%s", manifest)
	}
}
