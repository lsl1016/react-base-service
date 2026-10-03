package cos

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// localFS 本地目录存储模式：配置 cos.localDir 后替代腾讯云 COS，用于无 COS 凭证的本地开发。
// cosKey 作为相对路径存放于 localDir 下；key 的每一段禁止 "." / ".." / 盘符样式，防止越出根目录。
type localFS struct {
	root string
}

func newLocalFS(dir string) (*localFS, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("cos localDir 为空")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve cos localDir failed: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir cos localDir failed: %w", err)
	}
	return &localFS{root: root}, nil
}

func (l *localFS) path(cosKey string) (string, error) {
	key := strings.Trim(strings.TrimSpace(cosKey), "/")
	if key == "" {
		return "", fmt.Errorf("empty cos key")
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "\\:") {
			return "", fmt.Errorf("invalid cos key segment: %q", seg)
		}
	}
	return filepath.Join(l.root, filepath.FromSlash(key)), nil
}

func (l *localFS) uploadData(_ context.Context, data []byte, cosKey, _ string) error {
	full, err := l.path(cosKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("mkdir local object dir failed: %w", err)
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return fmt.Errorf("write local object failed: %w", err)
	}
	return nil
}

func (l *localFS) uploadFile(_ context.Context, localPath, cosKey string) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("read local upload file failed: %w", err)
	}
	return l.uploadData(context.Background(), data, cosKey, "")
}

func (l *localFS) downloadData(_ context.Context, cosKey string) ([]byte, error) {
	full, err := l.path(cosKey)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("local object not found: %s", cosKey)
		}
		return nil, fmt.Errorf("read local object failed: %w", err)
	}
	return data, nil
}

func (l *localFS) downloadFile(_ context.Context, cosKey, localPath string) error {
	data, err := l.downloadData(context.Background(), cosKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("mkdir local download dir failed: %w", err)
	}
	return os.WriteFile(localPath, data, 0o644)
}

func (l *localFS) downloadRange(_ context.Context, cosKey string, start, end int64) ([]byte, error) {
	if start < 0 || end < start {
		return nil, fmt.Errorf("invalid local byte range: %d-%d", start, end)
	}
	data, err := l.downloadData(context.Background(), cosKey)
	if err != nil {
		return nil, err
	}
	if end >= int64(len(data)) {
		return nil, fmt.Errorf("local range out of bounds: want=%d-%d size=%d", start, end, len(data))
	}
	return data[start : end+1], nil
}

func (l *localFS) deleteObject(_ context.Context, cosKey string) error {
	full, err := l.path(cosKey)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove local object failed: %w", err)
	}
	return nil
}

func (l *localFS) isExist(_ context.Context, cosKey string) (bool, error) {
	full, err := l.path(cosKey)
	if err != nil {
		return false, err
	}
	_, statErr := os.Stat(full)
	if statErr == nil {
		return true, nil
	}
	if os.IsNotExist(statErr) {
		return false, nil
	}
	return false, statErr
}

func (l *localFS) listObjects(prefix string, maxCount int) ([]string, error) {
	if maxCount <= 0 {
		maxCount = 1000
	}
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	var keys []string
	err := filepath.Walk(l.root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		rel, relErr := filepath.Rel(l.root, path)
		if relErr != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		if prefix == "" || strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk local objects failed: %w", err)
	}
	sort.Strings(keys)
	if len(keys) > maxCount {
		keys = keys[:maxCount]
	}
	return keys, nil
}

func (l *localFS) getURL(cosKey string) (string, error) {
	full, err := l.path(cosKey)
	if err != nil {
		return "", err
	}
	return "file:///" + filepath.ToSlash(full), nil
}

func (l *localFS) appendObject(ctx context.Context, cosKey string, appendData []byte, contentType string) error {
	exists, err := l.isExist(ctx, cosKey)
	if err != nil {
		return err
	}
	var merged []byte
	if exists {
		origin, err := l.downloadData(ctx, cosKey)
		if err != nil {
			return err
		}
		merged = append(origin, appendData...)
	} else {
		merged = appendData
	}
	return l.uploadData(ctx, merged, cosKey, contentType)
}

func (l *localFS) clearObject(ctx context.Context, cosKey string) error {
	exists, err := l.isExist(ctx, cosKey)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return l.uploadData(ctx, []byte{}, cosKey, "")
}
