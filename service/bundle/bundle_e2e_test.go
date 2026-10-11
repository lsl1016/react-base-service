package bundle

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"react-base-service/components/params"
	"react-base-service/conf"
	"react-base-service/golib/env"
	"react-base-service/golib/zlog"
	"react-base-service/helpers"
	model "react-base-service/models/llm"

	"github.com/gin-gonic/gin"
)

// TestBundleInstallUninstallE2E 端到端验证 Bundle 安装/卸载回滚（真实 MySQL，无需 LLM）：
//
//	REACT_DELEGATE_E2E=1 go test ./service/bundle/ -run TestBundleInstallUninstallE2E -v -timeout 300s
//
// 验证点：
//   - 安装本地 bundle 目录：agent/skill 落库、mcp 服务器行落库（连接失败不阻断安装）、资源清单带快照；
//   - 覆盖安装语义：预先存在的同名 skill 被覆盖且快照记录旧值；
//   - 卸载回滚：新建资源软删、覆盖资源按快照恢复、bundle 行软删。
func TestBundleInstallUninstallE2E(t *testing.T) {
	if os.Getenv("REACT_DELEGATE_E2E") == "" {
		t.Skip("set REACT_DELEGATE_E2E=1 to run e2e (requires react-base-mysql)")
	}

	env.SetRootPath("../..")
	conf.InitConf()
	zlog.InitLog(conf.BasicConf.Log)
	helpers.InitMysql()

	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/internal/bundle-e2e", nil)
	helpers.SetUserName(ctx, "e2e-bundle")

	// 前置清理：同名残留。
	cleanup := func() {
		_ = Uninstall(ctx, &params.BundleUninstallReq{Name: "e2e-probe-bundle"})
	}
	cleanup()
	t.Cleanup(cleanup)

	// 预置一个会被 bundle 覆盖的同名 skill（验证快照恢复）。
	preexisting := "e2e-bundle覆盖探针技能"
	if existing, err := model.FindActiveSkillByAppAndName(ctx, "demo-app", preexisting); err != nil {
		t.Fatalf("预置查询失败: %v", err)
	} else if existing != nil {
		_ = model.SoftDeleteSkillBySkillID(ctx, existing.SkillID)
	}
	routeValues := []string{}
	preSkill, err := skillCreateForE2E(ctx, preexisting, "覆盖前的旧描述")
	if err != nil {
		t.Fatalf("预置 skill 失败: %v", err)
	}

	// 构造本地 bundle 目录（白名单：custom.yaml 已放行 C:/Users/keke/Desktop/xm/，用 t.TempDir 会不在白名单，
	// 因此临时放宽白名单注入测试配置——validateBundleSource 使用传入 cfg）。
	root := t.TempDir()
	writeE2EFile := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeE2EFile(".plugin/plugin.json", `{"name":"e2e-probe-bundle","version":"0.1.0","description":"e2e 探针包"}`)
	writeE2EFile("agents/e2e-probe-agent.md",
		"---\nagent_key: e2e-bundle-probe-agent\napp_key: demo-app\ndescription: e2e 探针子代理\nmax_tokens_per_run: 5000\n---\n你是 e2e 探针子代理。")
	writeE2EFile("skills/cover/SKILL.md",
		"---\nname: "+preexisting+"\ntriggers: [e2e探针词]\n---\n覆盖后的新正文。")
	writeE2EFile("skills/fresh/SKILL.md",
		"---\nname: e2e-bundle新建探针技能\n---\n全新技能正文。")
	// MCP 用一个必然连不上的端点：注册成功（行落库）但 reconnect 失败不阻断安装。
	writeE2EFile(".mcp.json", `{"mcpServers":{"e2e-bundle-probe-mcp":{"url":"https://mcp.invalid.example.com/api"}}}`)

	installed, err := installForE2E(ctx, root, "demo-app", routeValues)
	if err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	if installed.Name != "e2e-probe-bundle" || installed.Version != "0.1.0" {
		t.Fatalf("安装结果不符合预期: %+v", installed)
	}
	if installed.AgentCount != 1 || installed.SkillCount != 2 || installed.McpServerCount != 1 {
		t.Fatalf("资源计数不符合预期: %+v", installed)
	}

	// 断言 1：agent 落库且带预算字段。
	agentRow, err := model.GetAgentByAppAndAgentKey(ctx, "demo-app", "e2e-bundle-probe-agent")
	if err != nil || agentRow == nil {
		t.Fatalf("bundle agent 未落库: %v", err)
	}
	if agentRow.MaxTokensPerRun != 5000 {
		t.Fatalf("bundle agent 预算字段不符合预期: %d", agentRow.MaxTokensPerRun)
	}

	// 断言 2：覆盖的 skill 内容被更新，资源清单记录覆盖前快照。
	covered, err := model.FindActiveSkillByAppAndName(ctx, "demo-app", preexisting)
	if err != nil || covered == nil {
		t.Fatalf("覆盖 skill 查询失败: %v", err)
	}
	if covered.SkillID != preSkill.SkillID {
		t.Fatalf("覆盖应沿用 skill_id: %s != %s", covered.SkillID, preSkill.SkillID)
	}
	if covered.Content != "覆盖后的新正文。" || covered.Description != "覆盖后的新正文。" {
		t.Fatalf("bundle skill 覆盖语义不符合预期（正文写入、描述缺省取正文首行）: %+v", covered)
	}
	resources, err := model.ListBundleResourcesByBundleID(ctx, installed.BundleID)
	if err != nil {
		t.Fatalf("资源清单查询失败: %v", err)
	}
	snapshotRecorded := false
	for _, resource := range resources {
		if resource.ResourceType == model.BundleResourceTypeSkill && resource.ResourceKey == preexisting && resource.PreviousStateJSON != "" {
			snapshotRecorded = true
		}
	}
	if !snapshotRecorded {
		t.Fatal("覆盖安装应记录 previous_state 快照")
	}

	// 断言 3：mcp 服务器行落库。
	mcpRow, err := model.GetMcpServerByName(ctx, "e2e-bundle-probe-mcp")
	if err != nil || mcpRow == nil {
		t.Fatalf("bundle mcp 服务器未落库: %v", err)
	}

	// 卸载 → 回滚断言。
	if err := Uninstall(ctx, &params.BundleUninstallReq{Name: "e2e-probe-bundle"}); err != nil {
		t.Fatalf("卸载失败: %v", err)
	}

	if agentRow2, err := model.GetAgentByAppAndAgentKey(ctx, "demo-app", "e2e-bundle-probe-agent"); err != nil {
		t.Fatalf("回滚查询 agent 失败: %v", err)
	} else if agentRow2 != nil {
		t.Fatal("新建 agent 卸载后应软删（不可见）")
	}
	restored, err := model.GetSkillBySkillID(ctx, preSkill.SkillID)
	if err != nil || restored == nil {
		t.Fatalf("回滚查询 skill 失败: %v", err)
	}
	if restored.TriggersJSON != "" || restored.Content != "" || restored.Description != "覆盖前的旧描述" {
		t.Fatalf("覆盖 skill 卸载后应按快照恢复: %+v", restored)
	}
	if mcpRow2, err := model.GetMcpServerByName(ctx, "e2e-bundle-probe-mcp"); err != nil {
		t.Fatalf("回滚查询 mcp 失败: %v", err)
	} else if mcpRow2 != nil {
		t.Fatal("新建 mcp 服务器卸载后应软删（不可见）")
	}
	if bundleRow, err := model.GetBundleByName(ctx, "e2e-probe-bundle"); err != nil {
		t.Fatalf("回滚查询 bundle 失败: %v", err)
	} else if bundleRow != nil {
		t.Fatal("卸载后 bundle 行应软删（不可见）")
	}
}

// installForE2E 用测试白名单临时替换配置后走真实 Install（含配置开关检查）。
// GetReactRuntimeConfig 每次从 CustomConf 拷贝，因此替换全局配置源即可生效。
func installForE2E(ctx *gin.Context, root, appKey string, routeValues []string) (*params.BundleListItemResp, error) {
	original := conf.CustomConf.LLM.React.Bundle
	enabled := true
	conf.CustomConf.LLM.React.Bundle = conf.ReactBundleConfig{
		Enabled:               &enabled,
		CacheDir:              original.CacheDir,
		GitTimeoutSec:         original.GitTimeoutSec,
		AllowedSourcePrefixes: []string{root},
	}
	defer func() { conf.CustomConf.LLM.React.Bundle = original }()

	installed, err := Install(ctx, &params.BundleInstallReq{
		Source:      root,
		AppKey:   appKey,
		RouteValues: routeValues,
	}, "e2e-bundle")
	if err != nil {
		return nil, err
	}
	items, err := ListInstalled(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].BundleID == installed.BundleID {
			return &items[i], nil
		}
	}
	return nil, nil
}

// skillCreateForE2E 直插一行 skill 作为覆盖安装前的旧状态。
func skillCreateForE2E(ctx *gin.Context, name, description string) (*model.Skill, error) {
	status := 1
	skill := &model.Skill{
		SkillID:     "skill_e2ebundlecover0001",
		Name:        name,
		Description: description,
		AppKey:   "demo-app",
		RouteValues: "[]",
		Status:      status,
		CreatedBy:   "e2e-bundle",
		UpdatedBy:   "e2e-bundle",
	}
	if err := model.CreateSkill(ctx, skill); err != nil {
		return nil, err
	}
	return skill, nil
}
