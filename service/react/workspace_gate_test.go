package react

import (
	"slices"
	"testing"

	"react-base-service/conf"
)

// TestWorkspaceSettingGatesLoadRuntimeCode 锁定「检索代码和工作区」运行时设置对
// load_runtime_code 注册的门控：workspace 设置关闭时工具不注册，开启（默认）时注册。
// 全局口径验证：不同 appKey 不影响该工具可见性（内置工具不按 app 隔离）。
func TestWorkspaceSettingGatesLoadRuntimeCode(t *testing.T) {
	origWorkspace := conf.CustomConf.LLM.React.Workspace
	origOverride := conf.GetRuntimeSettingOverride()
	defer func() {
		conf.CustomConf.LLM.React.Workspace = origWorkspace
		conf.SetRuntimeSettingOverride(origOverride)
	}()

	// 基础设施可用：解析表配置一个服务。
	conf.CustomConf.LLM.React.Workspace = conf.ReactWorkspaceConfig{
		Resolvers: []conf.ReactWorkspaceResolverConf{{Service: "demo-svc", RepoURL: "https://example.com/demo.git"}},
	}

	contains := func(names []string) bool { return slices.Contains(names, metaToolLoadRuntimeCode) }
	collect := func() (names []string) {
		for _, d := range internalMetaToolDefinitions() {
			names = append(names, d.Name)
		}
		return names
	}

	// 1. 无 DB 覆盖（默认启用）：注册。
	conf.SetRuntimeSettingOverride(conf.RuntimeSettingOverride{})
	if names := collect(); !contains(names) {
		t.Fatalf("默认启用时应注册 load_runtime_code，实际: %v", names)
	}

	// 2. DB 覆盖关闭：不注册。
	disabled := false
	conf.SetRuntimeSettingOverride(conf.RuntimeSettingOverride{
		Workspace: &conf.WorkspaceSettingOverride{Enabled: &disabled},
	})
	if names := collect(); contains(names) {
		t.Fatalf("workspace 设置关闭时不应注册 load_runtime_code，实际: %v", names)
	}

	// 3. DB 覆盖重新开启：注册。
	enabled := true
	conf.SetRuntimeSettingOverride(conf.RuntimeSettingOverride{
		Workspace: &conf.WorkspaceSettingOverride{Enabled: &enabled},
	})
	if names := collect(); !contains(names) {
		t.Fatalf("workspace 设置开启时应注册 load_runtime_code，实际: %v", names)
	}

	// 4. 开启但基础设施不可用（解析表为空）：不注册。
	conf.CustomConf.LLM.React.Workspace.Resolvers = nil
	if names := collect(); contains(names) {
		t.Fatalf("解析表为空时不应注册 load_runtime_code，实际: %v", names)
	}
}
