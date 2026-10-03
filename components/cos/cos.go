package cos

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tencentcos "github.com/tencentyun/cos-go-sdk-v5"
)

// Config COS 配置
type Config struct {
	// Provider 存储后端选择：minio/s3（自部署 MinIO，S3 兼容）、local（本地目录）；
	// 留空走腾讯云 COS（需云凭证）。显式指定时优先级高于 localDir。
	Provider  string `yaml:"provider"`
	SecretID  string `yaml:"secretID"`
	SecretKey string `yaml:"secretKey"`
	Bucket    string `yaml:"bucket"`
	AppID     string `yaml:"appID"`
	Region    string `yaml:"region"`
	Endpoint  string `yaml:"endpoint"`
	Timeout   int    `yaml:"timeout"`
	Path      string `yaml:"path"`     // 可选：默认路径前缀
	LocalDir  string `yaml:"localDir"` // 可选：本地目录存储模式，配置后无需腾讯云凭证（本地开发用）
}

// Client COS 客户端封装
type Client struct {
	raw    *tencentcos.Client
	config Config
	local  *localFS
	s3     *minioStore
}

// NewClient 创建对象存储客户端：按 provider 选择后端，localDir 兼容旧配置。
// 三种后端（腾讯 COS / MinIO / 本地目录）共享同一套 Client 方法，业务层无感知。
func NewClient(config Config) (*Client, error) {
	provider := strings.TrimSpace(strings.ToLower(config.Provider))
	if provider == "" || provider == providerLocal {
		dir := strings.TrimSpace(config.LocalDir)
		if dir == "" && provider == providerLocal {
			return nil, fmt.Errorf("cos provider=local 需要配置 localDir")
		}
		if dir != "" {
			store, err := newLocalFS(dir)
			if err != nil {
				return nil, err
			}
			return &Client{config: config, local: store}, nil
		}
	}
	if provider == providerMinio || provider == providerS3 {
		store, err := newMinioStore(config)
		if err != nil {
			return nil, err
		}
		return &Client{config: config, s3: store}, nil
	}
	if provider != "" && provider != providerCos {
		return nil, fmt.Errorf("未支持的 cos provider: %s（可选 minio/s3/local/cos）", provider)
	}
	if strings.TrimSpace(config.SecretID) == "" ||
		strings.TrimSpace(config.SecretKey) == "" ||
		strings.TrimSpace(config.Bucket) == "" {
		return nil, fmt.Errorf("cos配置不完整")
	}
	if config.Timeout <= 0 {
		config.Timeout = 30
	}

	host, err := buildBucketHost(config.Bucket, config.AppID, config.Endpoint, config.Region)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(fmt.Sprintf("https://%s", host))
	if err != nil {
		return nil, fmt.Errorf("parse cos url failed: %w", err)
	}

	baseURL := &tencentcos.BaseURL{BucketURL: u}
	httpClient := &http.Client{
		Transport: &tencentcos.AuthorizationTransport{
			SecretID:  config.SecretID,
			SecretKey: config.SecretKey,
		},
		Timeout: time.Duration(config.Timeout) * time.Second,
	}
	return &Client{
		raw:    tencentcos.NewClient(baseURL, httpClient),
		config: config,
	}, nil
}

// UploadFile 上传本地文件到 COS
func (c *Client) UploadFile(ctx context.Context, localPath, cosKey string) error {
	if c.local != nil {
		return c.local.uploadFile(ctx, localPath, c.normalizeKey(cosKey))
	}
	if c.s3 != nil {
		return c.s3.uploadFile(ctx, localPath, c.normalizeKey(cosKey))
	}
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local file failed: %w", err)
	}
	defer func() { _ = f.Close() }()

	key := c.normalizeKey(cosKey)
	opt := &tencentcos.ObjectPutOptions{
		ObjectPutHeaderOptions: &tencentcos.ObjectPutHeaderOptions{
			ContentType: detectContentType(localPath),
		},
	}
	if _, err := c.raw.Object.Put(ctx, key, f, opt); err != nil {
		return fmt.Errorf("cos put object failed: %w", err)
	}
	return nil
}

// UploadData 上传字节数据到 COS
func (c *Client) UploadData(ctx context.Context, data []byte, cosKey, contentType string) error {
	if c.local != nil {
		return c.local.uploadData(ctx, data, c.normalizeKey(cosKey), contentType)
	}
	if c.s3 != nil {
		return c.s3.uploadData(ctx, data, c.normalizeKey(cosKey), contentType)
	}
	key := c.normalizeKey(cosKey)
	if contentType == "" {
		contentType = detectContentType(cosKey)
	}
	opt := &tencentcos.ObjectPutOptions{
		ObjectPutHeaderOptions: &tencentcos.ObjectPutHeaderOptions{
			ContentType: contentType,
		},
	}
	reader := bytes.NewReader(data)
	if _, err := c.raw.Object.Put(ctx, key, reader, opt); err != nil {
		return fmt.Errorf("cos put data failed: %w", err)
	}
	return nil
}

// DownloadFile 从 COS 下载对象到本地文件
func (c *Client) DownloadFile(ctx context.Context, cosKey, localPath string) error {
	if c.local != nil {
		return c.local.downloadFile(ctx, c.normalizeKey(cosKey), localPath)
	}
	if c.s3 != nil {
		return c.s3.downloadFile(ctx, c.normalizeKey(cosKey), localPath)
	}
	key := c.normalizeKey(cosKey)
	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir local dir failed: %w", err)
	}

	f, err := os.Create(localPath)
	if err != nil {
		return fmt.Errorf("create local file failed: %w", err)
	}
	defer func() { _ = f.Close() }()

	resp, err := c.raw.Object.Get(ctx, key, nil)
	if err != nil {
		return fmt.Errorf("cos get object failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write local file failed: %w", err)
	}
	return nil
}

// DownloadData 从 COS 下载对象到内存
func (c *Client) DownloadData(ctx context.Context, cosKey string) ([]byte, error) {
	if c.local != nil {
		return c.local.downloadData(ctx, c.normalizeKey(cosKey))
	}
	if c.s3 != nil {
		return c.s3.downloadData(ctx, c.normalizeKey(cosKey))
	}
	key := c.normalizeKey(cosKey)
	resp, err := c.raw.Object.Get(ctx, key, nil)
	if err != nil {
		return nil, fmt.Errorf("cos get object failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read cos object failed: %w", err)
	}
	return data, nil
}

// DownloadRange 按闭区间读取 COS 对象字节，避免大文件分页读取时重复下载完整对象。
func (c *Client) DownloadRange(ctx context.Context, cosKey string, start, end int64) ([]byte, error) {
	if c.local != nil {
		return c.local.downloadRange(ctx, c.normalizeKey(cosKey), start, end)
	}
	if c.s3 != nil {
		return c.s3.downloadRange(ctx, c.normalizeKey(cosKey), start, end)
	}
	if start < 0 || end < start {
		return nil, fmt.Errorf("invalid cos byte range: %d-%d", start, end)
	}
	key := c.normalizeKey(cosKey)
	resp, err := c.raw.Object.Get(ctx, key, &tencentcos.ObjectGetOptions{Range: fmt.Sprintf("bytes=%d-%d", start, end)})
	if err != nil {
		return nil, fmt.Errorf("cos range get object failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("cos range get returned unexpected status: %d", resp.StatusCode)
	}
	want := end - start + 1
	data, err := io.ReadAll(io.LimitReader(resp.Body, want+1))
	if err != nil {
		return nil, fmt.Errorf("read cos object range failed: %w", err)
	}
	if int64(len(data)) != want {
		return nil, fmt.Errorf("cos range length mismatch: want=%d actual=%d", want, len(data))
	}
	return data, nil
}

// GetObject 获取 COS 对象内容
func (c *Client) GetObject(ctx context.Context, cosKey string) ([]byte, error) {
	return c.DownloadData(ctx, cosKey)
}

// DeleteObject 删除 COS 对象
func (c *Client) DeleteObject(ctx context.Context, cosKey string) error {
	if c.local != nil {
		return c.local.deleteObject(ctx, c.normalizeKey(cosKey))
	}
	if c.s3 != nil {
		return c.s3.deleteObject(ctx, c.normalizeKey(cosKey))
	}
	key := c.normalizeKey(cosKey)
	if _, err := c.raw.Object.Delete(ctx, key); err != nil {
		return fmt.Errorf("cos delete object failed: %w", err)
	}
	return nil
}

// DeleteObjects 批量删除 COS 对象
func (c *Client) DeleteObjects(ctx context.Context, cosKeys []string) error {
	if c.local != nil {
		for _, cosKey := range cosKeys {
			if err := c.local.deleteObject(ctx, c.normalizeKey(cosKey)); err != nil {
				return err
			}
		}
		return nil
	}
	if c.s3 != nil {
		keys := make([]string, 0, len(cosKeys))
		for _, cosKey := range cosKeys {
			if key := c.normalizeKey(cosKey); key != "" {
				keys = append(keys, key)
			}
		}
		return c.s3.deleteObjects(ctx, keys)
	}
	if len(cosKeys) == 0 {
		return nil
	}

	objects := make([]tencentcos.Object, 0, len(cosKeys))
	for _, cosKey := range cosKeys {
		key := c.normalizeKey(cosKey)
		if key == "" {
			continue
		}
		objects = append(objects, tencentcos.Object{Key: key})
	}
	if len(objects) == 0 {
		return nil
	}

	opt := &tencentcos.ObjectDeleteMultiOptions{
		Objects: objects,
		Quiet:   true,
	}
	if _, _, err := c.raw.Object.DeleteMulti(ctx, opt); err != nil {
		return fmt.Errorf("cos delete multi objects failed: %w", err)
	}
	return nil
}

// IsExist 检查 COS 对象是否存在
func (c *Client) IsExist(ctx context.Context, cosKey string) (bool, error) {
	if c.local != nil {
		return c.local.isExist(ctx, c.normalizeKey(cosKey))
	}
	if c.s3 != nil {
		return c.s3.isExist(ctx, c.normalizeKey(cosKey))
	}
	key := c.normalizeKey(cosKey)
	if _, err := c.raw.Object.Head(ctx, key, nil); err != nil {
		if tencentcos.IsNotFoundError(err) {
			return false, nil
		}
		return false, fmt.Errorf("cos head object failed: %w", err)
	}
	return true, nil
}

// GetURL 获取对象的临时访问 URL（带签名）
func (c *Client) GetURL(ctx context.Context, cosKey string, expire time.Duration) (string, error) {
	if c.local != nil {
		return c.local.getURL(c.normalizeKey(cosKey))
	}
	if c.s3 != nil {
		return c.s3.getURL(ctx, c.normalizeKey(cosKey), expire)
	}
	key := c.normalizeKey(cosKey)
	u, err := c.raw.Object.GetPresignedURL(
		ctx,
		http.MethodGet,
		key,
		c.config.SecretID,
		c.config.SecretKey,
		expire,
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("cos get presigned url failed: %w", err)
	}
	return u.String(), nil
}

// ListObjects 按前缀列举 COS 对象
func (c *Client) ListObjects(ctx context.Context, prefix string, maxCount int) ([]string, error) {
	if c.local != nil {
		return c.local.listObjects(c.normalizeKey(prefix), maxCount)
	}
	if c.s3 != nil {
		return c.s3.listObjects(ctx, c.normalizeKey(prefix), maxCount)
	}
	if maxCount <= 0 {
		maxCount = 1000
	}
	opt := &tencentcos.BucketGetOptions{
		Prefix:  c.normalizeKey(prefix),
		MaxKeys: maxCount,
	}
	result, _, err := c.raw.Bucket.Get(ctx, opt)
	if err != nil {
		return nil, fmt.Errorf("cos list objects failed: %w", err)
	}

	objects := make([]string, 0, len(result.Contents))
	for _, item := range result.Contents {
		objects = append(objects, item.Key)
	}
	return objects, nil
}

// ReadFile 读取 COS 文件内容返回字符串
func (c *Client) ReadFile(ctx context.Context, cosKey string) (string, error) {
	data, err := c.GetObject(ctx, cosKey)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// AppendObject 向 COS 对象追加内容（下载后拼接再上传）
func (c *Client) AppendObject(ctx context.Context, cosKey string, appendData []byte, contentType string) error {
	if c.local != nil {
		return c.local.appendObject(ctx, c.normalizeKey(cosKey), appendData, contentType)
	}
	if c.s3 != nil {
		return c.s3.appendObject(ctx, c.normalizeKey(cosKey), appendData, contentType)
	}
	exists, err := c.IsExist(ctx, cosKey)
	if err != nil {
		return err
	}

	newData := appendData
	if exists {
		origin, err := c.GetObject(ctx, cosKey)
		if err != nil {
			return err
		}
		newData = append(origin, appendData...)
	}
	return c.UploadData(ctx, newData, cosKey, contentType)
}

// ClearObject 清空对象内容；对象不存在时为幂等空操作。
func (c *Client) ClearObject(ctx context.Context, cosKey string) error {
	if c.local != nil {
		return c.local.clearObject(ctx, c.normalizeKey(cosKey))
	}
	if c.s3 != nil {
		return c.s3.clearObject(ctx, c.normalizeKey(cosKey))
	}
	exists, err := c.IsExist(ctx, cosKey)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	key := c.normalizeKey(cosKey)
	contentType := detectContentType(cosKey)
	if headResp, err := c.raw.Object.Head(ctx, key, nil); err == nil && headResp != nil && headResp.Header != nil {
		if v := strings.TrimSpace(headResp.Header.Get("Content-Type")); v != "" {
			contentType = v
		}
	}
	return c.UploadData(ctx, []byte{}, cosKey, contentType)
}

func (c *Client) normalizeKey(cosKey string) string {
	key := strings.TrimSpace(cosKey)
	key = strings.TrimPrefix(key, "/")
	prefix := strings.Trim(strings.TrimSpace(c.config.Path), "/")
	if prefix == "" {
		return key
	}
	if key == "" {
		return prefix
	}
	if strings.HasPrefix(key, prefix+"/") {
		return key
	}
	return prefix + "/" + key
}

func detectContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".txt":
		return "text/plain"
	case ".md":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".html", ".htm":
		return "text/html"
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	case ".pdf":
		return "application/pdf"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".zip":
		return "application/zip"
	case ".doc", ".docx":
		return "application/msword"
	case ".xls", ".xlsx":
		return "application/vnd.ms-excel"
	default:
		return "application/octet-stream"
	}
}

func buildBucketHost(bucket, appID, endpoint, region string) (string, error) {
	bucketName := ensureBucketWithAppID(strings.TrimSpace(bucket), strings.TrimSpace(appID))

	ep := normalizeEndpoint(endpoint)
	if ep != "" {
		return fmt.Sprintf("%s.%s", bucketName, ep), nil
	}

	r := strings.TrimSpace(region)
	if r == "" {
		return "", fmt.Errorf("cos配置不完整: endpoint或region至少配置一个")
	}
	return fmt.Sprintf("%s.cos.%s.myqcloud.com", bucketName, r), nil
}

func ensureBucketWithAppID(bucket, appID string) string {
	if appID == "" || strings.HasSuffix(bucket, "-"+appID) {
		return bucket
	}
	return bucket + "-" + appID
}

func normalizeEndpoint(endpoint string) string {
	ep := strings.TrimSpace(endpoint)
	ep = strings.TrimPrefix(ep, "https://")
	ep = strings.Trim(ep, "/")
	return ep
}
