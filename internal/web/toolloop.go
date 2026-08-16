package web

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

type detectedToolCall struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// isWebSearchTool reports whether a tool map is the web_search declaration.
// Web search is a Copilot built-in (BingWebSearch) performed server-side, so
// it must not enter the router decision; the answer stream handles it.
func isWebSearchTool(t map[string]any) bool {
	if s, _ := t["type"].(string); strings.EqualFold(s, "web_search") {
		return true
	}
	if f, ok := t["function"].(map[string]any); ok {
		if n, _ := f["name"].(string); strings.EqualFold(n, "web_search") {
			return true
		}
	}
	return false
}

// routeableTools drops web_search from the router decision set while keeping
// every declared tool visible to the streaming JSON guard and prompt.
func routeableTools(tools []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		if !isWebSearchTool(t) {
			out = append(out, t)
		}
	}
	return out
}

func toolType(name string, tools []map[string]any) string {
	for _, t := range tools {
		f, _ := t["function"].(map[string]any)
		if n, _ := f["name"].(string); n == name {
			if typ, _ := t["type"].(string); typ != "" {
				return typ
			}
		}
	}
	return "function"
}

func allowedToolNames(tools []map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, t := range tools {
		if f, ok := t["function"].(map[string]any); ok {
			if n, ok := f["name"].(string); ok && n != "" {
				out[n] = true
			}
		}
	}
	return out
}

type rejectedToolCall struct {
	Name   string
	Reason string
}

// validateDetectedToolCalls is the final trust boundary before a model-selected
// call is serialized to the client. ChatHub/native events and model-generated
// routing text are both untrusted: an undeclared name such as "unknown_tool"
// must never escape to Claude Code, Codex, or another local tool runner.
func validateDetectedToolCalls(calls []detectedToolCall, tools []map[string]any, choice any) ([]detectedToolCall, []rejectedToolCall) {
	valid := make([]detectedToolCall, 0, len(calls))
	rejected := make([]rejectedToolCall, 0)
	for _, call := range calls {
		fn := toolFunction(call.Name, tools)
		if fn == nil {
			rejected = append(rejected, rejectedToolCall{Name: call.Name, Reason: "tool was not declared by the client"})
			continue
		}
		if !toolChoiceAllows(choice, call.Name) {
			rejected = append(rejected, rejectedToolCall{Name: call.Name, Reason: "tool_choice does not allow this tool"})
			continue
		}
		args := map[string]any{}
		if len(call.Arguments) == 0 || string(call.Arguments) == "null" {
			call.Arguments = json.RawMessage(`{}`)
		} else if err := json.Unmarshal(call.Arguments, &args); err != nil {
			rejected = append(rejected, rejectedToolCall{Name: call.Name, Reason: "arguments are not a JSON object"})
			continue
		}
		if err := schemaValid(args, fn); err != nil {
			rejected = append(rejected, rejectedToolCall{Name: call.Name, Reason: err.Error()})
			continue
		}
		if call.ID == "" {
			call.ID = callID(call.Name, string(call.Arguments), len(valid))
		}
		if call.Type == "" {
			call.Type = toolType(call.Name, tools)
		}
		valid = append(valid, call)
	}
	return valid, rejected
}

func toolChoiceAllows(choice any, name string) bool {
	if choice == nil {
		return true
	}
	if s, ok := choice.(string); ok {
		return s != "none" && (s != "required" || name != "")
	}
	if m, ok := choice.(map[string]any); ok {
		if f, ok := m["function"].(map[string]any); ok {
			n, _ := f["name"].(string)
			return n == name
		}
		if n, ok := m["name"].(string); ok {
			return n == name
		}
	}
	return true
}

// callID returns a globally unique tool call id. Content hashes previously
// collided when the same tool+arguments was invoked again (duplicate tool call
// id errors from clients), so uniqueness must not depend on call content.
func callID(name, args string, index int) string {
	return "call_" + uuid.NewString()
}

// toolCallOpenPresentRE 宽松探测工具标签是否存在(兼容全角/半角/大小写/空白漂移)。
var toolCallOpenPresentRE = regexp.MustCompile(`(?is)[＜<]\s*[ｍM][３3][６6][５5]\s*[－-]?\s*[ｔt][ｏo][ｏo][ｌl]\s*[－_-]?\s*[ｃc][ａa][ｌl][ｌl]\s*[＞>]?`)

// normalizeToolCallText 将模型输出中的工具调用区段提取为可解析 JSON。
// 支持:
//   - <m365-tool-call>...</m365-tool-call> 标准包裹(JSON 内容)
//   - 属性风格 <m365-tool-call name="x" arguments='...'> 单调用
//   - 未闭合标签容错(取到行尾)
//   - 全角/大小写变体标签名
func normalizeToolCallText(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	// Attribute style first: <m365-tool-call name="..." arguments="...">
	attrRE := regexp.MustCompile(`(?is)<m365-tool-call\s+name\s*=\s*["']([^"']+)["']\s+arguments\s*=\s*["']([^"']*)["']\s*/?\s*>`)
	if m := attrRE.FindStringSubmatch(trimmed); len(m) == 3 {
		obj := map[string]any{"name": m[1], "arguments": json.RawMessage(m[2])}
		if !json.Valid(json.RawMessage(m[2])) {
			obj["arguments"] = json.RawMessage(`"` + m[2] + `"`)
		}
		b, _ := json.Marshal(obj)
		return string(b), true
	}
	// Wrapped JSON style: <m365-tool-call>{...}</m365-tool-call>
	start := strings.Index(trimmed, ">")
	if start < 0 {
		return "", false
	}
	body := trimmed[start+1:]
	end := strings.Index(body, "</")
	if end < 0 {
		// Unclosed tag: take everything to end of input.
		end = len(body)
	}
	payload := strings.TrimSpace(body[:end])
	raw := json.RawMessage(payload)
	if !json.Valid(raw) {
		// Lightweight repair: JSON written by models often has trailing
		// commas or unquoted keys. Try brace-balanced extraction first.
		repaired := repairLooseJSON(payload)
		if repaired == "" {
			return "", false
		}
		raw = json.RawMessage(repaired)
		if !json.Valid(raw) {
			return "", false
		}
		payload = repaired
	}
	return payload, true
}

// repairLooseJSON 轻量修复模型常犯的 JSON 错误:尾逗号、单引号、未引用 key。
// 返回修复后的 JSON 字符串;无法修复时返回空串。
func repairLooseJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Single-quoted strings -> double-quoted.
	if !strings.Contains(s, "\"") {
		s = strings.ReplaceAll(s, "'", "\"")
	}
	// Trailing commas before } or ].
	s = regexp.MustCompile(`,\s*([}\]])`).ReplaceAllString(s, "$1")
	// Unquoted keys: {name: ...} -> {"name": ...}
	s = regexp.MustCompile(`([{,]\s*)([A-Za-z_][A-Za-z0-9_]*)\s*:`).ReplaceAllString(s, `${1}"${2}":`)
	if json.Valid(json.RawMessage(s)) {
		return s
	}
	return ""
}

func extractToolCalls(text string, tools []map[string]any, choice any) ([]detectedToolCall, bool) {
	normalized := normalizeTagASCII(text)
	if !toolCallOpenPresentRE.MatchString(normalized) {
		return nil, false
	}
	text = normalized
	allowed := allowedToolNames(tools)
	out := make([]detectedToolCall, 0, 2)
	// Attribute style: <m365-tool-call name="x" arguments='...'>.
	// arguments 通常是 JSON,可能内含双引号,因此优先匹配单引号包裹(内容可含 ")。
	attrRE := regexp.MustCompile(`(?is)<m365-tool-call\s+name\s*=\s*["']([^"']+)["']\s+arguments\s*=\s*'([^']*)'\s*/?\s*>`)
	attrREAlt := regexp.MustCompile(`(?is)<m365-tool-call\s+name\s*=\s*["']([^"']+)["']\s+arguments\s*=\s*"([^"]*)"\s*/?\s*>`)
	var matched [][]string
	matched = attrRE.FindAllStringSubmatch(text, -1)
	if len(matched) == 0 {
		matched = attrREAlt.FindAllStringSubmatch(text, -1)
	}
	for _, m := range matched {
		name := m[1]
		args := m[2]
		if !allowed[name] || !toolChoiceAllows(choice, name) {
			continue
		}
		raw := json.RawMessage(args)
		if !json.Valid(raw) {
			raw = json.RawMessage(`"` + args + `"`)
		}
		out = append(out, detectedToolCall{ID: callID(name, string(raw), len(out)), Type: toolType(name, tools), Name: name, Arguments: raw})
	}
	// Wrapped JSON style blocks.
	wrapRE := regexp.MustCompile(`(?is)<m365-tool-call>([\s\S]*?)</m365-tool-call>`)
	for _, m := range wrapRE.FindAllStringSubmatch(text, -1) {
		payload, ok := normalizeToolCallText("<m365-tool-call>" + m[1] + "</m365-tool-call>")
		if !ok {
			continue
		}
		var raw any
		if json.Unmarshal([]byte(payload), &raw) != nil {
			continue
		}
		items := []any{raw}
		if arr, ok := raw.([]any); ok {
			items = arr
		}
		for i, item := range items {
			mm, ok := item.(map[string]any)
			if !ok {
				continue
			}
			n, _ := mm["name"].(string)
			if !allowed[n] || !toolChoiceAllows(choice, n) {
				continue
			}
			a, _ := json.Marshal(mm["arguments"])
			out = append(out, detectedToolCall{ID: callID(n, string(a), i), Type: toolType(n, tools), Name: n, Arguments: a})
		}
	}
	// Unclosed wrapper fallback: content after the last open tag.
	if len(out) == 0 {
		lastOpen := strings.LastIndex(text, "<m365-tool-call>")
		if lastOpen >= 0 && !strings.Contains(text[lastOpen:], "</m365-tool-call>") {
			payload, ok := normalizeToolCallText(text[lastOpen:])
			if ok {
				var raw any
				if json.Unmarshal([]byte(payload), &raw) == nil {
					if mm, ok := raw.(map[string]any); ok {
						n, _ := mm["name"].(string)
						if allowed[n] && toolChoiceAllows(choice, n) {
							a, _ := json.Marshal(mm["arguments"])
							out = append(out, detectedToolCall{ID: callID(n, string(a), 0), Type: toolType(n, tools), Name: n, Arguments: a})
						}
					}
				}
			}
		}
	}
	return out, len(out) > 0
}

// normalizeTagASCII 将工具标签中的全角字符折叠为半角,使后续解析统一。
// 仅处理标签壳字符(＜＞ ｍ３６５－ｔｏｏｌｃａｌｌ),不改动正文内容。
func normalizeTagASCII(s string) string {
	r := strings.NewReplacer(
		"＜", "<", "＞", ">",
		"ｍ", "m", "Ｍ", "m",
		"３", "3", "６", "6", "５", "5",
		"－", "-",
		"ｔ", "t", "Ｔ", "t",
		"ｏ", "o", "Ｏ", "o",
		"ｌ", "l", "Ｌ", "l",
		"ｃ", "c", "Ｃ", "c",
		"ａ", "a", "Ａ", "a",
	)
	return r.Replace(s)
}

func validateToolResult(messages []oaiMsg, known map[string]bool) error {
	for _, m := range messages {
		if m.Role == "tool" {
			if m.ToolCallID == "" {
				return fmt.Errorf("tool_call_id required")
			}
			if len(known) > 0 && !known[m.ToolCallID] {
				return fmt.Errorf("unknown tool_call_id: %s", m.ToolCallID)
			}
		}
	}
	return nil
}

var toolRefusalPatterns = []string{
	"tools are not available",
	"tool is not available",
	"cannot access the Windows path",
	"only provides Linux",
	"只提供 Linux 容器",
	"工具未暴露",
	"工具不可用",
	"没有可调用的",
	"无法继续操作",
	"will not pretend",
	"will not fake",
	"cannot fake",
	"would be fabricated",
	"cannot fabricate",
	"refuse to fabricate",
	"not actually registered",
	"not actually available",
	"not exposed in this",
	"not available in this session",
	"cannot execute on this platform",
	"没有 Windows 执行接口",
	"回复通道没有",
	"没有执行接口",
	"不会虚构",
	"不会!转入",
	"不会转入",
	"execution environment has changed",
	"执行环境已经切换",
	"无法访问上一会话",
	"/mnt/data",
	"current execution environment has changed",
	"linux sandbox",
	"linux container",
	"running in a container",
	"cannot modify source code",
	"没有连接到",
	"Windows 执行接口",
	"I can run that for you",
	"running in sandbox",
	"executing in sandbox",
	"code interpreter",
	"python sandbox",
	"sandbox environment",
}

func isToolRefusal(text string) bool {
	low := strings.ToLower(text)
	for _, p := range toolRefusalPatterns {
		if strings.Contains(low, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

var sandboxHallucinationPatterns = []string{
	"I can run that for you",
	"I'll run that",
	"let me run that",
	"let me execute",
	"running in sandbox",
	"executing in sandbox",
	"code interpreter",
	"python sandbox",
	"sandbox environment",
	"/mnt/data",
	"linux container",
	"linux sandbox",
	"cloud sandbox",
	"execution environment has changed",
	"cannot access the Windows path",
	"only provides Linux",
	"只提供 Linux 容器",
	"执行环境已经切换",
	"I don't have SSH access tools",
	"I don't have any tools",
	"none of which can reach",
}

func isSandboxHallucination(text string) bool {
	low := strings.ToLower(text)
	for _, p := range sandboxHallucinationPatterns {
		if strings.Contains(low, strings.ToLower(p)) {
			return true
		}
	}
	return false
}
