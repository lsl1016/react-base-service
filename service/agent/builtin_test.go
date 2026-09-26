package agent

import (
	"testing"

	model "react-base-service/models/llm"
)

// TestMergeBuiltinAgentsShadowing（WP2 验收）：同 agent_key 的 DB 行整体覆盖内置 profile，
// 未遮蔽的内置 profile 追加在尾部；覆盖后行为完全来自 DB 行（同名即 fork）。
func TestMergeBuiltinAgentsShadowing(t *testing.T) {
	dbAgents := []model.Agent{
		{AgentKey: "dba-agent", AgentID: "agent_db", Name: "DBA"},
		// DB fork：同名 researcher，改为非只读、换系统提示词——执行必须按这一行
		{AgentKey: "researcher", AgentID: "agent_fork", Name: "自定义调查员", SystemPrompt: "自定义提示词", ReadOnly: 0},
	}
	merged := mergeBuiltinAgents(dbAgents)

	builtinSeen := map[string]bool{}
	for i, agent := range merged {
		if i < len(dbAgents) {
			if agent.AgentKey != dbAgents[i].AgentKey {
				t.Fatalf("DB 行应保持在前且顺序不变: index=%d, got=%s", i, agent.AgentKey)
			}
		}
		if IsBuiltinAgent(agent) {
			builtinSeen[agent.AgentKey] = true
		}
	}
	if builtinSeen["researcher"] {
		t.Fatalf("DB 行遮蔽后内置 researcher 不应再出现")
	}
	// researcher 在本夹具中被 DB fork 遮蔽；其余三个内置 profile 应保持可见
	for _, key := range []string{"general-purpose", "report-writer", "code-reader"} {
		if !builtinSeen[key] {
			t.Fatalf("未被遮蔽的内置 profile %s 应出现在可见清单", key)
		}
	}
	// fork 行保留 DB 定义原值
	fork := merged[1]
	if fork.AgentID != "agent_fork" || fork.SystemPrompt != "自定义提示词" || fork.ReadOnly != 0 {
		t.Fatalf("同名 DB 行应完整生效: %+v", fork)
	}
}

// TestPolicyReadOnly（WP2）：RuntimePolicy.ReadOnly 透传定义值，供只读执行域单调收紧。
func TestPolicyReadOnly(t *testing.T) {
	runtime := &Runtime{}
	if runtime.Policy(model.Agent{ReadOnly: 1}).ReadOnly != true {
		t.Fatalf("read_only=1 应映射 ReadOnly=true")
	}
	if runtime.Policy(model.Agent{ReadOnly: 0}).ReadOnly != false {
		t.Fatalf("read_only=0 应映射 ReadOnly=false")
	}
}

// TestBuiltinProfilesWellFormed：内置 profile 基本合法性（key 合法、only-known tokens、只读域自洽）。
func TestBuiltinProfilesWellFormed(t *testing.T) {
	for _, profile := range BuiltinAgentProfiles() {
		if profile.AgentKey == "" || profile.Name == "" || profile.Description == "" || profile.SystemPrompt == "" {
			t.Fatalf("内置 profile %s 缺少必要字段", profile.AgentKey)
		}
		if profile.ReadOnly == 1 && profile.ToolsJSON != `["`+AgentToolRefReadOnly+`"]` {
			t.Fatalf("只读内置 profile %s 的工具白名单应为 @readonly", profile.AgentKey)
		}
	}
}
