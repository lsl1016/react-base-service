package graphmemory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

var groupIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func TestSanitizeGroupID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"demo_app", "demo_app"},
		{"  spaced  ", "spaced"},
		{"", "default"},
	}
	for _, tc := range cases {
		if got := SanitizeGroupID(tc.in); got != tc.want {
			t.Fatalf("SanitizeGroupID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := SanitizeGroupID(strings.Repeat("a", 100)); len(got) != groupIDMaxLen {
		t.Fatalf("oversized group id should truncate to %d, got %d", groupIDMaxLen, len(got))
	}
	// 连字符是 FalkorDB 全文检索的非法字符（见 scope.go 注释），含 - 的标识清洗后必须无 - 且带哈希后缀。
	if got := SanitizeGroupID("demo-app"); strings.Contains(got, "-") || !strings.HasPrefix(got, "demo_app") || !groupIDPattern.MatchString(got) {
		t.Fatalf("hyphen must be sanitized away: %q", got)
	}
	// 含被清洗字符（信息损失）时：追加稳定哈希后缀，不同原文不碰撞、相同原文稳定。
	if got := SanitizeGroupID("ops@work"); !strings.HasPrefix(got, "ops_work") || !groupIDPattern.MatchString(got) {
		t.Fatalf("lossy sanitize should append hash suffix: %q", got)
	}
	chineseA := SanitizeGroupID("陈总")
	chineseB := SanitizeGroupID("王五")
	if chineseA == "default" || chineseB == "default" || chineseA == chineseB {
		t.Fatalf("distinct non-ascii identifiers must map to distinct groups: %q vs %q", chineseA, chineseB)
	}
	if SanitizeGroupID("陈总") != chineseA {
		t.Fatalf("same identifier must map stably: %q", chineseA)
	}
	if !groupIDPattern.MatchString(chineseA) {
		t.Fatalf("sanitized id must stay valid: %q", chineseA)
	}
}

func TestResolveScope(t *testing.T) {
	appGroup := AppGroupID("demo_app")
	userGroup := AppUserGroupID("demo_app", "alice")

	appOnly := ResolveScope("demo_app", "alice", false)
	if len(appOnly.Groups) != 1 || appOnly.Groups[0] != appGroup || appOnly.WriteGroup != appGroup {
		t.Fatalf("app scope mismatch: %+v", appOnly)
	}

	userScope := ResolveScope("demo_app", "alice", true)
	if len(userScope.Groups) != 2 || userScope.Groups[0] != appGroup || userScope.Groups[1] != userGroup {
		t.Fatalf("user scope groups mismatch: %+v", userScope.Groups)
	}
	if userScope.WriteGroup != userGroup {
		t.Fatalf("user scope write group mismatch: %s", userScope.WriteGroup)
	}
}

func TestClientSearchPostsScopeAndParsesFacts(t *testing.T) {
	var gotBody searchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"facts":[{"uuid":"e1","name":"DEPLOYED_ON","fact":"service-A 部署在 cluster-02","valid_at":"2026-09-01T00:00:00Z","invalid_at":null,"created_at":"2026-09-01T08:00:00Z","expired_at":null,"episodes":["ep1"]}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, TimeoutMs: 3000})
	facts, err := client.Search(context.Background(), []string{"demo-app", "demo-app__alice"}, "service-A 在哪", 8)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotBody.Query != "service-A 在哪" || gotBody.MaxFacts != 8 || len(gotBody.GroupIDs) != 2 {
		t.Fatalf("request body mismatch: %+v", gotBody)
	}
	if len(facts) != 1 || facts[0].Fact != "service-A 部署在 cluster-02" || facts[0].Name != "DEPLOYED_ON" {
		t.Fatalf("facts mismatch: %+v", facts)
	}
	if facts[0].InvalidAt != "" || facts[0].ValidAt == "" {
		t.Fatalf("time window mismatch: %+v", facts[0])
	}
}

func TestClientAddEpisodeAccepted(t *testing.T) {
	var gotBody addMessagesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"message":"Messages added to processing queue","success":true}`))
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, TimeoutMs: 3000})
	err := client.AddEpisode(context.Background(), "demo-app__alice", EpisodeMessage{
		Content:   "job_123 慢查询根因是 ods_order 未加速",
		Name:      "hive 慢查询事件",
		RoleType:  "user",
		Timestamp: parseTestTime(t, "2026-09-13T10:00:00Z"),
	})
	if err != nil {
		t.Fatalf("add episode: %v", err)
	}
	if gotBody.GroupID != "demo-app__alice" || len(gotBody.Messages) != 1 || gotBody.Messages[0].RoleType != "user" {
		t.Fatalf("request body mismatch: %+v", gotBody)
	}
	if gotBody.Messages[0].Content == "" || gotBody.Messages[0].Timestamp.IsZero() {
		t.Fatalf("message content/timestamp must be serialized: %+v", gotBody.Messages[0])
	}
}

func TestClientServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(Config{Endpoint: server.URL, TimeoutMs: 3000})
	if _, err := client.Search(context.Background(), []string{"g"}, "q", 5); err == nil {
		t.Fatalf("search should fail on 500")
	}
	// endpoint 未配置时 SharedClient 返回 nil。
	if SharedClient(Config{Endpoint: ""}) != nil {
		t.Fatalf("empty endpoint should yield nil client")
	}
}

func TestSharedClientRebuildsOnEndpointChange(t *testing.T) {
	first := SharedClient(Config{Endpoint: "http://first:8000/", TimeoutMs: 1000})
	if first == nil || first.Endpoint() != "http://first:8000" {
		t.Fatalf("first client mismatch: %v", first)
	}
	same := SharedClient(Config{Endpoint: "http://first:8000", TimeoutMs: 1000})
	if same != first {
		t.Fatalf("same config should reuse client")
	}
	second := SharedClient(Config{Endpoint: "http://second:8000", TimeoutMs: 1000})
	if second == first || second.Endpoint() != "http://second:8000" {
		t.Fatalf("endpoint change should rebuild client")
	}
}

func parseTestTime(t *testing.T, iso string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatalf("parse time %q: %v", iso, err)
	}
	return parsed
}
