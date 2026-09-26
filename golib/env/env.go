// Package env 提供应用名与配置加载的最小实现，替代原内部框架 env 包。
// 配置文件从工作目录下的 conf/mount/ 读取。
package env

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

var (
	appName  string
	rootPath string
)

// envRefPattern 匹配 ${VAR} 与 ${VAR:-default} 两种环境变量引用。
var envRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^{}]*))?\}`)

// expandEnvRefs 展开配置内容中的环境变量引用：
//
//	${VAR}          → os.Getenv(VAR)（未设置展开为空串）
//	${VAR:-default} → os.Getenv(VAR)，为空时取 default
//
// 用途：API Key 等凭据不再明文写入 conf/mount/*.yaml，改为环境变量/密钥服务注入。
func expandEnvRefs(data string) string {
	return envRefPattern.ReplaceAllStringFunc(data, func(match string) string {
		groups := envRefPattern.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		value := os.Getenv(groups[1])
		if value == "" && len(groups) >= 3 {
			return groups[2]
		}
		return value
	})
}

// SetAppName 设置应用名（本地实现仅存储）。
func SetAppName(name string) {
	appName = name
}

// AppName 读取应用名。
func AppName() string {
	return appName
}

// SetRootPath 设置配置加载根路径（测试中用于指向仓库根目录）。
func SetRootPath(path string) {
	rootPath = path
}

// SubConfType 配置加载子目录类型。
type SubConfType int

// SubConfMount 表示从 conf/mount 子目录加载。
const SubConfMount SubConfType = iota

// LoadConf 从 <rootPath>/conf/mount 读取指定配置文件并反序列化到 target。
// 读取后先展开 ${VAR} / ${VAR:-default} 环境变量引用，再反序列化。
func LoadConf(filename string, _ SubConfType, target interface{}) {
	path := filepath.Join(rootPath, "conf", "mount", filename)
	data, err := os.ReadFile(path)
	if err != nil {
		panic(fmt.Sprintf("load conf %s failed: %v", path, err))
	}
	expanded := expandEnvRefs(string(data))
	if err := yaml.Unmarshal([]byte(expanded), target); err != nil {
		panic(fmt.Sprintf("parse conf %s failed: %v", path, err))
	}
}
