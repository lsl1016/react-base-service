package helpers

import (
	"fmt"
	"sync"

	"react-base-service/components/cos"
	"react-base-service/conf"
)

// CosClient 全局 COS 客户端
var CosClient *cos.Client
var (
	cosInitOnce sync.Once
	cosInitErr  error
)

// InitCos 初始化 COS 客户端
func InitCos() {
	cfg := conf.RConf.Cos
	client, err := cos.NewClient(cos.Config{
		Provider:  cfg.Provider,
		SecretID:  cfg.SecretID,
		SecretKey: cfg.SecretKey,
		Bucket:    cfg.Bucket,
		AppID:     cfg.AppID,
		Region:    cfg.Region,
		Endpoint:  cfg.Endpoint,
		Timeout:   cfg.Timeout,
		LocalDir:  cfg.LocalDir,
		// Path 不在此处注入，避免和业务层 key 拼接重复。
	})
	if err != nil {
		panic(fmt.Sprintf("init cos failed: %v", err))
	}
	CosClient = client
}

// EnsureCos 按需初始化 COS 客户端
func EnsureCos() error {
	cosInitOnce.Do(func() {
		cfg := conf.RConf.Cos
		client, err := cos.NewClient(cos.Config{
			Provider:  cfg.Provider,
			SecretID:  cfg.SecretID,
			SecretKey: cfg.SecretKey,
			Bucket:    cfg.Bucket,
			AppID:     cfg.AppID,
			Region:    cfg.Region,
			Endpoint:  cfg.Endpoint,
			Timeout:   cfg.Timeout,
			LocalDir:  cfg.LocalDir,
		})
		if err != nil {
			cosInitErr = fmt.Errorf("init cos failed: %w", err)
			return
		}
		CosClient = client
	})
	return cosInitErr
}
