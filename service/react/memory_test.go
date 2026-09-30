package react

import (
	"strings"
	"testing"

	"react-base-service/conf"
	memoryService "react-base-service/service/memory"
)

func TestValidateMemoryPayloadRules(t *testing.T) {
	if err := memoryService.ValidatePayload("", "内容", "提示"); err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("empty title should fail, got %v", err)
	}
	if err := memoryService.ValidatePayload(strings.Repeat("标", 33), "内容", "提示"); err == nil || !strings.Contains(err.Error(), "title 超长") {
		t.Fatalf("long title should fail, got %v", err)
	}
	if err := memoryService.ValidatePayload("标题", "", "提示"); err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("empty content should fail, got %v", err)
	}
	if err := memoryService.ValidatePayload("标题", strings.Repeat("文", 501), "提示"); err == nil || !strings.Contains(err.Error(), "content 超长") {
		t.Fatalf("long content should fail, got %v", err)
	}
	if err := memoryService.ValidatePayload("标题", "内容", ""); err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("empty description should fail, got %v", err)
	}
	if err := memoryService.ValidatePayload("标题", "内容", "提示"); err != nil {
		t.Fatalf("valid payload should pass, got %v", err)
	}
}

func TestInternalMetaToolDefinitionsFollowMemorySwitch(t *testing.T) {
	original := conf.CustomConf.LLM.React.Memory
	defer func() { conf.CustomConf.LLM.React.Memory = original }()

	off := false
	conf.CustomConf.LLM.React.Memory = conf.ReactMemoryConfig{Enabled: &off}
	for _, def := range internalMetaToolDefinitions() {
		switch def.Name {
		case metaToolMemoryList, metaToolMemoryRead, metaToolMemoryWrite:
			t.Fatalf("memory tools must be absent when disabled: %s", def.Name)
		}
	}

	on := true
	conf.CustomConf.LLM.React.Memory = conf.ReactMemoryConfig{Enabled: &on}
	names := make(map[string]bool)
	for _, def := range internalMetaToolDefinitions() {
		names[def.Name] = true
	}
	for _, expected := range []string{metaToolMemoryList, metaToolMemoryRead, metaToolMemoryWrite} {
		if !names[expected] {
			t.Fatalf("memory tool missing when enabled: %s", expected)
		}
	}
}
