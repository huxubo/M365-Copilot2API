package web

import (
	"encoding/json"
	"testing"

	"m365-copilot2api/internal/chathub"
)

func retryTestTools() []map[string]any {
	return []map[string]any{
		{"type": "function", "function": map[string]any{"name": "analyze_text", "description": "d", "parameters": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}}}},
		{"type": "function", "function": map[string]any{"name": "summarize", "description": "d", "parameters": map[string]any{"type": "object", "properties": map[string]any{"findings": map[string]any{"type": "string"}}, "required": []string{"findings"}}}},
	}
}

func TestExtractToolCalls_StandardWrapped(t *testing.T) {
	text := `请分析：<m365-tool-call>{"name":"analyze_text","arguments":{"text":"量子计算"}}</m365-tool-call>`
	calls, ok := extractToolCalls(text, retryTestTools(), nil)
	if !ok || len(calls) != 1 {
		t.Fatalf("want 1 call, got %d ok=%v", len(calls), ok)
	}
	if calls[0].Name != "analyze_text" {
		t.Fatalf("name=%s", calls[0].Name)
	}
}

func TestExtractToolCalls_ArrayWrapped(t *testing.T) {
	text := `<m365-tool-call>[{"name":"analyze_text","arguments":{"text":"a"}},{"name":"summarize","arguments":{"findings":"b"}}]</m365-tool-call>`
	calls, ok := extractToolCalls(text, retryTestTools(), nil)
	if !ok || len(calls) != 2 {
		t.Fatalf("want 2 calls, got %d ok=%v", len(calls), ok)
	}
}

func TestExtractToolCalls_AttributeStyle(t *testing.T) {
	text := `<m365-tool-call name="analyze_text" arguments='{"text":"量子"}' />`
	calls, ok := extractToolCalls(text, retryTestTools(), nil)
	if !ok || len(calls) != 1 || calls[0].Name != "analyze_text" {
		t.Fatalf("attr style failed: %d ok=%v", len(calls), ok)
	}
	if string(calls[0].Arguments) != `{"text":"量子"}` {
		t.Fatalf("args=%s", string(calls[0].Arguments))
	}
}

func TestExtractToolCalls_LooseJSON(t *testing.T) {
	// Trailing comma + unquoted key should be repaired.
	text := `<m365-tool-call>{name:"analyze_text",arguments:{text:"x",}}</m365-tool-call>`
	calls, ok := extractToolCalls(text, retryTestTools(), nil)
	if !ok || len(calls) != 1 {
		t.Fatalf("loose json failed: %d ok=%v", len(calls), ok)
	}
}

func TestExtractToolCalls_Unclosed(t *testing.T) {
	text := `请执行：<m365-tool-call>{"name":"analyze_text","arguments":{"text":"x"}}`
	calls, ok := extractToolCalls(text, retryTestTools(), nil)
	if !ok || len(calls) != 1 {
		t.Fatalf("unclosed failed: %d ok=%v", len(calls), ok)
	}
}

func TestExtractToolCalls_FullwidthVariant(t *testing.T) {
	text := "＜ｍ３６５－ｔｏｏｌ－ｃａｌｌ＞{\"name\":\"analyze_text\",\"arguments\":{\"text\":\"x\"}}＜/ｍ３６５－ｔｏｏｌ－ｃａｌｌ＞"
	calls, ok := extractToolCalls(text, retryTestTools(), nil)
	if !ok || len(calls) != 1 {
		t.Fatalf("fullwidth variant failed: %d ok=%v", len(calls), ok)
	}
}

func TestExtractToolCalls_NotAToolText(t *testing.T) {
	text := "这是一个普通的文本回复，没有工具调用"
	calls, ok := extractToolCalls(text, retryTestTools(), nil)
	if ok || len(calls) != 0 {
		t.Fatalf("should be no tool calls, got %d ok=%v", len(calls), ok)
	}
}

func TestRepairLooseJSON(t *testing.T) {
	cases := map[string]string{
		`{name:"a",arguments:{"x":1,}}`: `{"name":"a","arguments":{"x":1}}`,
	}
	for in, want := range cases {
		got := repairLooseJSON(in)
		if !json.Valid(json.RawMessage(got)) {
			t.Fatalf("repair(%q) -> %q not valid", in, got)
		}
		var g, w any
		_ = json.Unmarshal([]byte(got), &g)
		_ = json.Unmarshal([]byte(want), &w)
		gb, _ := json.Marshal(g)
		wb, _ := json.Marshal(w)
		if string(gb) != string(wb) {
			t.Fatalf("repair(%q)=%q want %q", in, got, want)
		}
	}
}

func TestEmptyRetry_Trigger(t *testing.T) {
	e := emptyOutputRetry{maxAttempts: 3}
	res := chathub.Result{Text: "", Reasoning: ""}
	if !e.shouldRetryEmpty(res, 0) {
		t.Fatal("empty text should retry")
	}
	if e.shouldRetryEmpty(res, 2) {
		t.Fatal("attempt cap must stop retry")
	}
	res2 := chathub.Result{Text: "   ", Reasoning: ""}
	if !e.shouldRetryEmpty(res2, 0) {
		t.Fatal("whitespace text should retry")
	}
}

func TestEmptyRetry_NoTrigger(t *testing.T) {
	e := emptyOutputRetry{maxAttempts: 3}
	res := chathub.Result{Text: "正常回复", Reasoning: ""}
	if e.shouldRetryEmpty(res, 0) {
		t.Fatal("normal text must not retry")
	}
	res2 := chathub.Result{Text: "OK", Reasoning: ""}
	if e.shouldRetryEmpty(res2, 0) {
		t.Fatal("short but legitimate answer must not retry")
	}
}