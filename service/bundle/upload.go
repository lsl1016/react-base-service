package bundle

import (
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"react-base-service/components"
	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/zlog"

	"github.com/gin-gonic/gin"
)

// 上传本地 bundle 目录的防滥用限额：前端用 webkitdirectory 一次选出全部文件
//（filename 携带目录内相对路径），逐文件/总量/条目数三重限额（安装时 readCapped
// 还会按资源类型再卡更紧的单文件上限）。
const (
	maxBundleUploadEntries     = 512
	maxBundleUploadEntryBytes  = 8 << 20
	maxBundleUploadTotalBytes  = 64 << 20
	bundleUploadMaxAge         = 24 * time.Hour
	bundleUploadNameMax        = 48
	maxBundleUploadRequestSize = 128 << 20 // Content-Length 硬上限（提前拒绝，防无谓传输）
)

// bundleUploadNamePattern 上传暂存目录名仅允许安全字符（其余替换为中划线）。
var bundleUploadNamePattern = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// bundleUploadRoot 是上传 bundle 的解压根目录：cache_dir 下的 uploads 子目录。
// 属服务端专属暂存区，validateBundleSource 对它按清洗后的严格前缀单独放行——
// 其内容只可能来自 /react/bundle/upload 上传的文件，不经用户任意指定路径。
func bundleUploadRoot(cfg conf.ReactBundleConfig) string {
	return filepath.Join(filepath.Clean(cfg.CacheDir), "uploads")
}

// UploadFiles 接收前端目录选择器上传的 bundle 全部文件：multipart files[] 携带文件
// 内容，普通字段 paths 携带同名顺序的目录内相对路径 JSON（Go 的 multipart 会把
// filename 剥成 basename，故路径必须走独立字段）。写入
// cache_dir/uploads/<目录名>-<时间戳>，自动下钻定位 bundle 根并预检可解析
//（坏包立即拒绝并清理，不留垃圾目录），返回可直接作为 install source 的目录路径。
func UploadFiles(ctx *gin.Context, files []*multipart.FileHeader, paths []string) (*params.BundleUploadResp, error) {
	cfg := conf.GetReactRuntimeConfig().Bundle
	if !cfg.BundleEnabled() {
		return nil, components.ErrorParamInvalid.Sprintf("bundle 未启用（llm.react.bundle.enabled）")
	}
	if strings.TrimSpace(cfg.CacheDir) == "" {
		return nil, components.ErrorParamInvalid.Sprintf("llm.react.bundle.cache_dir 未配置，无法接收上传")
	}
	// Content-Length 预检（传输前拒绝，实际限额在逐文件读取时强制）。
	if ctx != nil && ctx.Request != nil && ctx.Request.ContentLength > maxBundleUploadRequestSize {
		return nil, components.ErrorParamInvalid.Sprintf("上传内容超过 %dMB 上限", maxBundleUploadRequestSize>>20)
	}
	if len(files) == 0 {
		return nil, components.ErrorParamInvalid.Sprintf("请选择 bundle 目录（未收到任何文件）")
	}
	if len(paths) != len(files) {
		return nil, components.ErrorParamInvalid.Sprintf("文件与路径信息不一致（files=%d paths=%d）", len(files), len(paths))
	}
	if len(files) > maxBundleUploadEntries {
		return nil, components.ErrorBundleImportInvalid.Sprintf("目录文件数超过 %d 上限（bundle 只需 manifest/agents/skills/.mcp.json，请勿包含 node_modules 等无关内容）", maxBundleUploadEntries)
	}
	// 误选单个压缩包给定向报错（目录内文件的相对路径必含分隔符）。
	if len(files) == 1 && !strings.Contains(paths[0], "/") &&
		(strings.EqualFold(path.Ext(paths[0]), ".zip") || strings.Contains(paths[0], ".tar.")) {
		return nil, components.ErrorParamInvalid.Sprintf("检测到压缩文件 %s：请先解压，再直接选择 bundle 目录", path.Base(paths[0]))
	}

	root := bundleUploadRoot(cfg)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	cleanupAgedBundleUploads(ctx, root)
	stage := filepath.Join(root, sanitizeBundleUploadName(bundleUploadTopDir(paths))+"-"+time.Now().UTC().Format("20060102150405")+"-"+fmt.Sprintf("%x", time.Now().UnixNano()))
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return nil, err
	}
	if err := writeBundleFiles(files, paths, stage); err != nil {
		_ = os.RemoveAll(stage)
		return nil, err
	}
	bundleRoot, err := resolveBundleRoot(stage)
	if err == nil {
		_, err = readBundleDir(bundleRoot)
	}
	if err != nil {
		_ = os.RemoveAll(stage)
		return nil, err
	}
	zlog.Infof(ctx, "[Bundle.Upload] 目录上传暂存完成: files=%d stage=%s root=%s", len(files), stage, bundleRoot)
	return &params.BundleUploadResp{Path: bundleRoot, Name: bundleUploadTopDir(paths)}, nil
}

// bundleUploadTopDir 取所选目录名（首个非垃圾相对路径的首段），用于暂存目录命名与回显。
func bundleUploadTopDir(paths []string) string {
	for _, raw := range paths {
		cleaned := path.Clean(strings.ReplaceAll(raw, "\\", "/"))
		if isJunkUploadSegment(cleaned) {
			continue
		}
		if segments := strings.Split(cleaned, "/"); len(segments) > 1 && segments[0] != "" && segments[0] != "." {
			return segments[0]
		}
	}
	return "bundle"
}

// sanitizeBundleUploadName 由所选目录名生成安全暂存目录段（非法字符折叠为中划线）。
func sanitizeBundleUploadName(name string) string {
	base := bundleUploadNamePattern.ReplaceAllString(name, "-")
	base = strings.Trim(base, "-_")
	runes := []rune(base)
	if len(runes) > bundleUploadNameMax {
		runes = runes[:bundleUploadNameMax]
	}
	if len(runes) == 0 {
		return "bundle"
	}
	return string(runes)
}

// writeBundleFiles 把上传文件按 paths 相对路径写入 stage：拒绝绝对路径、../ 逃逸与
// 盘符；跳过 __MACOSX/.DS_Store 垃圾条目；逐条目限流读取（不信任 multipart 头声明的大小）。
func writeBundleFiles(files []*multipart.FileHeader, paths []string, stage string) error {
	var total uint64
	for i, header := range files {
		cleaned := path.Clean(strings.ReplaceAll(paths[i], "\\", "/"))
		if cleaned == "." || cleaned == ".." || path.IsAbs(cleaned) || strings.HasPrefix(cleaned, "../") ||
			strings.HasPrefix(cleaned, "/") || len(cleaned) > 1 && cleaned[1] == ':' {
			return components.ErrorBundleImportInvalid.Sprintf("文件路径非法: %q", paths[i])
		}
		if isJunkUploadSegment(cleaned) {
			continue
		}
		target := filepath.Join(stage, filepath.FromSlash(cleaned))
		if header.Size > maxBundleUploadEntryBytes {
			return components.ErrorBundleImportInvalid.Sprintf("文件 %q 超过 %dMB 上限", cleaned, maxBundleUploadEntryBytes>>20)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeCappedUploadFile(header, target, &total); err != nil {
			return err
		}
	}
	return nil
}

func writeCappedUploadFile(header *multipart.FileHeader, target string, total *uint64) error {
	src, err := header.Open()
	if err != nil {
		return components.ErrorBundleImportInvalid.Sprintf("读取上传文件 %q 失败: %v", header.Filename, err)
	}
	defer src.Close()
	// 限流多读 1 字节识别超限（multipart 头可虚报小尺寸，必须按实际流卡）。
	data, err := io.ReadAll(io.LimitReader(src, maxBundleUploadEntryBytes+1))
	if err != nil {
		return components.ErrorBundleImportInvalid.Sprintf("读取 %q 失败: %v", header.Filename, err)
	}
	if len(data) > maxBundleUploadEntryBytes {
		return components.ErrorBundleImportInvalid.Sprintf("文件 %q 实际大小超过 %dMB 上限", header.Filename, maxBundleUploadEntryBytes>>20)
	}
	*total += uint64(len(data))
	if *total > maxBundleUploadTotalBytes {
		return components.ErrorBundleImportInvalid.Sprintf("上传总量超过 %dMB 上限", maxBundleUploadTotalBytes>>20)
	}
	return os.WriteFile(target, data, 0o644)
}

// isJunkUploadSegment 判定 macOS 目录垃圾条目（__MACOSX 目录树 / .DS_Store / ._ 资源叉）。
func isJunkUploadSegment(cleaned string) bool {
	for _, segment := range strings.Split(cleaned, "/") {
		if segment == "__MACOSX" || segment == ".DS_Store" || strings.HasPrefix(segment, "._") {
			return true
		}
	}
	return false
}

// hasBundleManifest 判定目录是否直接含 manifest（.plugin/plugin.json 或根 manifest.json）。
func hasBundleManifest(root string) bool {
	for _, candidate := range []string{filepath.Join(root, ".plugin", "plugin.json"), filepath.Join(root, "manifest.json")} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// resolveBundleRoot 定位 bundle 根：目录选择器选中的常是「外层目录包一层 bundle」
//（如选中 bundles/ 后进入唯一子目录）。最多下钻 3 层唯一非隐藏目录；无 manifest 且
// 无法唯一下钻时返回当前层，由 readBundleDir 预检给出精确的「缺少 manifest」错误。
func resolveBundleRoot(stage string) (string, error) {
	root := stage
	for i := 0; i < 3; i++ {
		if hasBundleManifest(root) {
			return root, nil
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return "", components.ErrorBundleImportInvalid.Sprintf("读取暂存目录失败: %v", err)
		}
		subdirs := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() && !isHiddenSegment(entry.Name()) && entry.Name() != "__MACOSX" {
				subdirs = append(subdirs, entry.Name())
			}
		}
		if len(subdirs) == 1 {
			root = filepath.Join(root, subdirs[0])
			continue
		}
		return root, nil
	}
	return root, nil
}

// cleanupAgedBundleUploads 尽力清理超过 24h 的上传暂存目录（安装解析纯内存，暂存仅在
// 上传到安装的窗口内需要；失败不影响本次上传）。
func cleanupAgedBundleUploads(ctx *gin.Context, root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-bundleUploadMaxAge)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			zlog.Warnf(ctx, "[Bundle.Upload] 清理过期暂存失败: dir=%s err=%v", entry.Name(), err)
		}
	}
}
