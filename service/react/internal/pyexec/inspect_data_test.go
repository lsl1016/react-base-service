package pyexec

import (
	"encoding/json"
	"testing"
)

func TestInspectJSONValueBuildsPathSummary(t *testing.T) {
	var value interface{}
	if err := json.Unmarshal([]byte(`{"data":[{"id":1,"name":"a"},{"id":2,"extra":true}],"summary":{"total":2}}`), &value); err != nil {
		t.Fatalf("unmarshal fixture failed: %v", err)
	}
	acc := make(map[string]*jsonPathAccumulator)
	samples := make(map[string]interface{})
	inspectJSONValue(value, "$", acc, samples, 1)
	summaries := buildJSONPathSummaries(acc)
	byPath := make(map[string]jsonPathSummary, len(summaries))
	for _, item := range summaries {
		byPath[item.Path] = item
	}
	for _, path := range []string{"$", "$.data", "$.data[]", "$.data[].id", "$.data[].name", "$.data[].extra", "$.summary.total"} {
		if _, ok := byPath[path]; !ok {
			t.Fatalf("expected path %s in summaries: %#v", path, summaries)
		}
	}
	if got := byPath["$.data[].id"].Types; len(got) != 1 || got[0] != "number" {
		t.Fatalf("expected id number type, got %#v", got)
	}
	if sample, ok := samples["$.data[]"].([]interface{}); !ok || len(sample) != 1 {
		t.Fatalf("expected one sampled array item, got %#v", samples["$.data[]"])
	}
}
