package skill

import (
	"strings"
	"testing"
)

// TestBundledSkillManifestSync 校验 manifest 与技能文件的同步完整性：
// 每条声明的正文文件必须存在且非空，name 全局唯一。
func TestBundledSkillManifestSync(t *testing.T) {
	manifest := loadBundledSkillManifest()
	if len(manifest.Skills) == 0 {
		t.Fatalf("内置技能包不应为空（种子逻辑依赖 manifest.Skills）")
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Skills {
		if entry.Name == "" || entry.File == "" {
			t.Fatalf("manifest 条目缺少 name/file: %+v", entry)
		}
		if seen[entry.Name] {
			t.Fatalf("manifest 技能名重复: %s", entry.Name)
		}
		seen[entry.Name] = true
		body, err := readBundledSkillBody(entry)
		if err != nil {
			t.Fatalf("技能正文读取失败: %s (%s): %v", entry.Name, entry.File, err)
		}
		if strings.TrimSpace(body) == "" {
			t.Fatalf("技能正文为空: %s (%s)", entry.Name, entry.File)
		}
	}
}

// TestBundledSkillCodeInvestigationSync 校验「代码工作区调查」技能与 code-reader
// 内置子代理白名单引用（service/agent）的名字同步：常量改名/manifest 改名时在此失败提醒同步。
func TestBundledSkillCodeInvestigationSync(t *testing.T) {
	manifest := loadBundledSkillManifest()
	for _, entry := range manifest.Skills {
		if entry.Name == BundledSkillCodeInvestigation {
			return
		}
	}
	t.Fatalf("manifest 缺少 %q 条目（code-reader 内置子代理的 skills_json 按此名引用）", BundledSkillCodeInvestigation)
}
