package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestMemoryOwnerScopeConditionCarriesStateInEveryBranch 防回归：
// owners OR 查询的每个分支必须带 state = active 条件（GORM Where().Or() 不会把先置
// 条件下推进 Or 分支，漏并会把软删条目泄漏进注入/列表）。
func TestMemoryOwnerScopeConditionCarriesStateInEveryBranch(t *testing.T) {
	owners := []MemoryOwner{
		BuildCallerMemoryOwner("demo-app"),
		BuildCallerUserMemoryOwner("demo-app", "zhangsan"),
	}
	condition, args := buildMemoryOwnerScopeCondition(owners)

	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "user:password@tcp(127.0.0.1:3306)/test?charset=utf8mb4&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	require.NoError(t, err)

	tx := db.Model(&MemoryItem{}).Where(condition, args...).Find(&MemoryItem{})
	require.NoError(t, tx.Error)

	sql := tx.Statement.SQL.String()
	require.Equal(t, 2, strings.Count(sql, "state = ?"), "每个 owner 分支都必须带 state 条件: %s", sql)
	require.Equal(t, 2, strings.Count(sql, "owner_type = ?"), "两个 owner 分支: %s", sql)
	require.Contains(t, sql, "(owner_type = ? AND owner_key = ? AND state = ?) OR (owner_type = ? AND owner_key = ? AND state = ?)", "分支必须括号包裹且逐支带 state: %s", sql)
}

// TestBuildMemoryKeywordLikeConditionCoversOwnerStateAndColumns 防回归：
// V2 候选召回的 LIKE 条件必须锚定 owner+state（防越权/软删泄漏），且覆盖四个检索列；
// 短于 2 rune 的关键词不参与（防单字符全表扫），无有效关键词时调用方应短路返回空集。
func TestBuildMemoryKeywordLikeConditionCoversOwnerStateAndColumns(t *testing.T) {
	owner := BuildCallerUserMemoryOwner("demo-app", "zhangsan")
	condition, args, ok := buildMemoryKeywordLikeCondition(owner, []string{"Go", "架构图", "a", " "})
	require.True(t, ok)
	require.Contains(t, condition, "owner_type = ? AND owner_key = ? AND state = ?")
	require.Equal(t, 1, strings.Count(condition, "state = ?"), "单 owner 单分支: %s", condition)
	require.Equal(t, 2, strings.Count(condition, "(title LIKE ?"), "两个有效关键词各生成一组括号包裹的条件: %s", condition)
	for _, column := range []string{"title LIKE ?", "description LIKE ?", "content LIKE ?", "tags LIKE ?"} {
		require.Equal(t, 2, strings.Count(condition, column), "每个关键词都应覆盖 %s", column)
	}
	require.Len(t, args, 11, "owner三参 + 每关键词四列")
	require.Equal(t, "caller_user", args[0])

	_, _, ok = buildMemoryKeywordLikeCondition(owner, []string{"a", " ", ""})
	require.False(t, ok, "全部关键词无效时应短路")
}

