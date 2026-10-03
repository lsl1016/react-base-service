package cos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// minioStore 自部署 MinIO（S3 兼容协议）对象存储后端：配置 provider: minio 后替代腾讯云 COS。
// 复用 cos.Config 的 SecretID/SecretKey 作为 AccessKey/SecretKey，Endpoint 指向 MinIO S3 API 地址。
// bucket 在首次访问时自动创建（幂等），新部署的 MinIO 无需手工建桶。
type minioStore struct {
	client     *minio.Client
	bucket     string
	region     string
	ensureOnce sync.Once
	ensureErr  error
}

const (
	providerLocal = "local"
	providerMinio = "minio"
	// providerS3 是 providerMinio 的别名，任意标准 S3 兼容服务（含 MinIO）均可。
	providerS3  = "s3"
	providerCos = "cos"
)

func newMinioStore(config Config) (*minioStore, error) {
	accessKey := strings.TrimSpace(config.SecretID)
	secretKey := strings.TrimSpace(config.SecretKey)
	bucket := strings.TrimSpace(config.Bucket)
	if accessKey == "" || secretKey == "" || bucket == "" {
		return nil, fmt.Errorf("minio配置不完整: secretID/secretKey/bucket 均不能为空")
	}
	endpoint, secure, err := parseS3Endpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	// MinIO 自部署默认 us-east-1；显式 region 用于 MakeBucket 与签名。
	region := strings.TrimSpace(config.Region)
	if region == "" {
		region = "us-east-1"
	}

	options := &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
		Region: region,
	}
	if config.Timeout > 0 {
		// Transport 只接受 http.RoundTripper；连接/握手/响应头超时覆盖主要时延，
		// 请求整体超时仍由调用方 ctx 控制（包一层会提前取消 body 读取）。
		timeout := time.Duration(config.Timeout) * time.Second
		options.Transport = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			IdleConnTimeout:       90 * time.Second,
		}
	}
	client, err := minio.New(endpoint, options)
	if err != nil {
		return nil, fmt.Errorf("init minio client failed: %w", err)
	}
	return &minioStore{client: client, bucket: bucket, region: region}, nil
}

// parseS3Endpoint 解析 endpoint：支持 http:// / https:// 前缀，裸地址默认走 HTTP
// （容器内网自部署的常规形态）；桶不参与 host 拼接（与腾讯 COS 虚拟主机风格不同）。
func parseS3Endpoint(endpoint string) (string, bool, error) {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return "", false, fmt.Errorf("minio配置不完整: endpoint 不能为空")
	}
	if strings.HasPrefix(ep, "https://") {
		return strings.Trim(strings.TrimPrefix(ep, "https://"), "/"), true, nil
	}
	if strings.HasPrefix(ep, "http://") {
		return strings.Trim(strings.TrimPrefix(ep, "http://"), "/"), false, nil
	}
	return strings.Trim(ep, "/"), false, nil
}

// ensureBucket 首次访问时确保 bucket 存在；sync.Once 保证全进程只探测一次，
// 失败不缓存以外的问题——建桶失败会在下一次进程生命周期重试。
func (m *minioStore) ensureBucket(ctx context.Context) error {
	m.ensureOnce.Do(func() {
		exists, err := m.client.BucketExists(ctx, m.bucket)
		if err != nil {
			m.ensureErr = fmt.Errorf("minio check bucket failed: %w", err)
			return
		}
		if exists {
			return
		}
		if err := m.client.MakeBucket(ctx, m.bucket, minio.MakeBucketOptions{Region: m.region}); err != nil {
			// 多实例并发建桶的兜底：他人刚建成功视为成功。
			if exists, checkErr := m.client.BucketExists(ctx, m.bucket); checkErr == nil && exists {
				return
			}
			m.ensureErr = fmt.Errorf("minio make bucket failed: %w", err)
		}
	})
	return m.ensureErr
}

func isS3NotFound(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchKey"
	}
	return false
}

func (m *minioStore) uploadFile(ctx context.Context, localPath, key string) error {
	if err := m.ensureBucket(ctx); err != nil {
		return err
	}
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local file failed: %w", err)
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat local file failed: %w", err)
	}
	_, err = m.client.PutObject(ctx, m.bucket, key, f, stat.Size(), minio.PutObjectOptions{
		ContentType: detectContentType(localPath),
	})
	if err != nil {
		return fmt.Errorf("minio put object failed: %w", err)
	}
	return nil
}

func (m *minioStore) uploadData(ctx context.Context, data []byte, key, contentType string) error {
	if err := m.ensureBucket(ctx); err != nil {
		return err
	}
	if contentType == "" {
		contentType = detectContentType(key)
	}
	_, err := m.client.PutObject(ctx, m.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("minio put data failed: %w", err)
	}
	return nil
}

func (m *minioStore) downloadData(ctx context.Context, key string) ([]byte, error) {
	if err := m.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return m.readObject(ctx, key, minio.GetObjectOptions{})
}

func (m *minioStore) downloadFile(ctx context.Context, key, localPath string) error {
	data, err := m.downloadData(ctx, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("mkdir local dir failed: %w", err)
	}
	if err := os.WriteFile(localPath, data, 0o644); err != nil {
		return fmt.Errorf("write local file failed: %w", err)
	}
	return nil
}

func (m *minioStore) downloadRange(ctx context.Context, key string, start, end int64) ([]byte, error) {
	if err := m.ensureBucket(ctx); err != nil {
		return nil, err
	}
	if start < 0 || end < start {
		return nil, fmt.Errorf("invalid minio byte range: %d-%d", start, end)
	}
	opts := minio.GetObjectOptions{}
	if err := opts.SetRange(start, end); err != nil {
		return nil, fmt.Errorf("minio set range failed: %w", err)
	}
	return m.readObject(ctx, key, opts, end-start+1)
}

// readObject 读取对象内容；want>0 时按 Range 语义校验返回长度（对齐 COS 后端的强校验）。
func (m *minioStore) readObject(ctx context.Context, key string, opts minio.GetObjectOptions, want ...int64) ([]byte, error) {
	object, err := m.client.GetObject(ctx, m.bucket, key, opts)
	if err != nil {
		if isS3NotFound(err) {
			return nil, fmt.Errorf("minio object not found: %s", key)
		}
		return nil, fmt.Errorf("minio get object failed: %w", err)
	}
	defer func() { _ = object.Close() }()

	data, err := io.ReadAll(object)
	if err != nil {
		if isS3NotFound(err) {
			return nil, fmt.Errorf("minio object not found: %s", key)
		}
		return nil, fmt.Errorf("read minio object failed: %w", err)
	}
	if len(want) > 0 && int64(len(data)) != want[0] {
		return nil, fmt.Errorf("minio range length mismatch: want=%d actual=%d", want[0], len(data))
	}
	return data, nil
}

func (m *minioStore) deleteObject(ctx context.Context, key string) error {
	if err := m.ensureBucket(ctx); err != nil {
		return err
	}
	if err := m.client.RemoveObject(ctx, m.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("minio delete object failed: %w", err)
	}
	return nil
}

func (m *minioStore) deleteObjects(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := m.ensureBucket(ctx); err != nil {
		return err
	}
	objectsCh := make(chan minio.ObjectInfo, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		objectsCh <- minio.ObjectInfo{Key: key}
	}
	close(objectsCh)

	for err := range m.client.RemoveObjects(ctx, m.bucket, objectsCh, minio.RemoveObjectsOptions{}) {
		return fmt.Errorf("minio delete objects failed: %w", err.Err)
	}
	return nil
}

func (m *minioStore) isExist(ctx context.Context, key string) (bool, error) {
	if err := m.ensureBucket(ctx); err != nil {
		return false, err
	}
	_, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if isS3NotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("minio stat object failed: %w", err)
	}
	return true, nil
}

func (m *minioStore) listObjects(ctx context.Context, prefix string, maxCount int) ([]string, error) {
	if err := m.ensureBucket(ctx); err != nil {
		return nil, err
	}
	if maxCount <= 0 {
		maxCount = 1000
	}
	keys := make([]string, 0)
	for object := range m.client.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
		MaxKeys:   maxCount,
	}) {
		if object.Err != nil {
			return nil, fmt.Errorf("minio list objects failed: %w", object.Err)
		}
		keys = append(keys, object.Key)
		if len(keys) >= maxCount {
			break
		}
	}
	return keys, nil
}

func (m *minioStore) getURL(ctx context.Context, key string, expire time.Duration) (string, error) {
	if err := m.ensureBucket(ctx); err != nil {
		return "", err
	}
	u, err := m.client.PresignedGetObject(ctx, m.bucket, key, expire, nil)
	if err != nil {
		return "", fmt.Errorf("minio get presigned url failed: %w", err)
	}
	return u.String(), nil
}

func (m *minioStore) appendObject(ctx context.Context, key string, appendData []byte, contentType string) error {
	exists, err := m.isExist(ctx, key)
	if err != nil {
		return err
	}
	newData := appendData
	if exists {
		origin, err := m.downloadData(ctx, key)
		if err != nil {
			return err
		}
		newData = append(origin, appendData...)
	}
	return m.uploadData(ctx, newData, key, contentType)
}

func (m *minioStore) clearObject(ctx context.Context, key string) error {
	exists, err := m.isExist(ctx, key)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	contentType := detectContentType(key)
	if stat, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{}); err == nil && stat.ContentType != "" {
		contentType = stat.ContentType
	}
	return m.uploadData(ctx, []byte{}, key, contentType)
}
