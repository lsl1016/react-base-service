package react

import (
	"strings"
	"testing"

	model "react-base-service/models/llm"
)

func TestValidateMemoryOwnerAppScope(t *testing.T) {
	owner, err := validateMemoryOwner("app", "demo-app")
	if err != nil {
		t.Fatalf("app owner should pass: %v", err)
	}
	if owner != model.BuildAppMemoryOwner("demo-app") {
		t.Fatalf("unexpected owner: %+v", owner)
	}
}

func TestValidateMemoryOwnerAppUserScope(t *testing.T) {
	owner, err := validateMemoryOwner("app_user", "demo-app|zhangsan")
	if err != nil {
		t.Fatalf("app_user owner should pass: %v", err)
	}
	if owner.OwnerKey != "demo-app|zhangsan" || owner.OwnerType != model.MemoryOwnerTypeAppUser {
		t.Fatalf("unexpected owner: %+v", owner)
	}
}

func TestValidateMemoryOwnerRejectsInvalid(t *testing.T) {
	if _, err := validateMemoryOwner("route", "x"); err == nil || !strings.Contains(err.Error(), "ownerType") {
		t.Fatalf("unknown ownerType should fail, got %v", err)
	}
	if _, err := validateMemoryOwner("app", "  "); err == nil || !strings.Contains(err.Error(), "ownerKey") {
		t.Fatalf("empty app ownerKey should fail, got %v", err)
	}
	if _, err := validateMemoryOwner("app_user", "no-separator"); err == nil || !strings.Contains(err.Error(), "格式") {
		t.Fatalf("app_user ownerKey without separator should fail, got %v", err)
	}
}
