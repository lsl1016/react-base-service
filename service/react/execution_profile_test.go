package react

import (
	"testing"

	"react-base-service/components/params"
	"react-base-service/conf"
	model "react-base-service/models/llm"
)

// setGlobalAllowPlanForTest 临时改写全局 allow_plan 配置并在测试结束后恢复。
func setGlobalAllowPlanForTest(t *testing.T, v *bool) {
	t.Helper()
	original := conf.CustomConf.LLM.React.AllowPlan
	t.Cleanup(func() { conf.CustomConf.LLM.React.AllowPlan = original })
	conf.CustomConf.LLM.React.AllowPlan = v
}

func boolPtr(v bool) *bool { return &v }

// TestEffectiveAllowPlan 语义：caller 显式覆盖优先，未覆盖跟随全局（全局未配置默认开）。
func TestEffectiveAllowPlan(t *testing.T) {
	setGlobalAllowPlanForTest(t, nil)
	if !effectiveAllowPlan(nil) {
		t.Fatalf("未覆盖且全局未配置时应默认开启")
	}
	if effectiveAllowPlan(boolPtr(false)) {
		t.Fatalf("caller 级强制关必须生效")
	}

	setGlobalAllowPlanForTest(t, boolPtr(false))
	if effectiveAllowPlan(nil) {
		t.Fatalf("未覆盖时应跟随全局关")
	}
	if !effectiveAllowPlan(boolPtr(true)) {
		t.Fatalf("caller 级强制开必须覆盖全局关")
	}

	setGlobalAllowPlanForTest(t, boolPtr(true))
	if effectiveAllowPlan(boolPtr(false)) {
		t.Fatalf("caller 级强制关必须覆盖全局开")
	}
}

// TestExecutionProfileForRunResolvesCallerAllowPlan 各运行形态对 caller 覆盖的继承：
// 外层/委派子 run/定时 run 继承覆盖；plan step 无论覆盖取值一律强制关（防递归编排）。
func TestExecutionProfileForRunResolvesCallerAllowPlan(t *testing.T) {
	setGlobalAllowPlanForTest(t, boolPtr(true))

	newReq := func(callerOverride *bool, sessionType, agentPath string) *runtimeRequest {
		return &runtimeRequest{
			payload:                 params.ReactRunPayload{Type: sessionType},
			runtimeRequestIdentity:  runtimeRequestIdentity{callerAllowPlan: callerOverride},
			runtimeRequestExecution: runtimeRequestExecution{agentPath: agentPath},
		}
	}

	if p := executionProfileForRun(newReq(boolPtr(false), model.ReactSessionTypeChat, "")); p.AllowPlan {
		t.Fatalf("外层 run 应继承 caller 级强制关")
	}
	if p := executionProfileForRun(newReq(boolPtr(false), model.ReactSessionTypeChat, "dba-agent/step1")); p.AllowPlan {
		t.Fatalf("委派子 run 应继承 caller 级强制关")
	}
	if p := executionProfileForRun(newReq(boolPtr(false), model.ReactSessionTypeScheduled, "")); p.AllowPlan {
		t.Fatalf("定时 run 应继承 caller 级强制关")
	}
	if p := executionProfileForRun(newReq(boolPtr(true), model.ReactSessionTypeChat, "plan/step1")); p.AllowPlan {
		t.Fatalf("plan step 无论 caller 覆盖取值都必须强制关（防递归编排）")
	}
	if p := executionProfileForRun(newReq(boolPtr(true), model.ReactSessionTypeChat, "")); !p.AllowPlan {
		t.Fatalf("caller 级强制开应透传到外层档案")
	}
}

// TestRuntimeToolDefinitionsCreatePlanVisibility create_plan 可见性统一按执行档案判定：
// caller 覆盖关时不进工具列表；reflection 受限域即使强制开也不可见。
func TestRuntimeToolDefinitionsCreatePlanVisibility(t *testing.T) {
	setGlobalAllowPlanForTest(t, boolPtr(true))

	newReq := func(callerOverride *bool, sessionType string) *runtimeRequest {
		return &runtimeRequest{
			payload:                params.ReactRunPayload{Type: sessionType},
			runtimeRequestIdentity: runtimeRequestIdentity{callerAllowPlan: callerOverride},
		}
	}

	hasCreatePlan := func(req *runtimeRequest) bool {
		defs := runtimeToolDefinitions(req, executionProfileForRun(req))
		for _, def := range defs {
			if def.Name == metaToolCreatePlan {
				return true
			}
		}
		return false
	}

	if !hasCreatePlan(newReq(nil, model.ReactSessionTypeChat)) {
		t.Fatalf("未覆盖（跟随全局开）时 create_plan 应可见")
	}
	if hasCreatePlan(newReq(boolPtr(false), model.ReactSessionTypeChat)) {
		t.Fatalf("caller 级强制关时 create_plan 不应可见")
	}
	if hasCreatePlan(newReq(boolPtr(true), model.ReactSessionTypeReflection)) {
		t.Fatalf("reflection 受限域即使 caller 强制开也不应暴露 create_plan")
	}
}
