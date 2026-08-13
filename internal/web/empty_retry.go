package web

import (
	"strings"

	"m365-copilot2api/internal/chathub"
)

// emptyOutputRetry 判断一次上游回答是否为空输出,并控制重试次数。
// 借鉴 ds2api 的 empty-output retry:上游偶发返回空正文/空思考/无工具调用,
// 自动重试(同账号或换账号)而不是把空回复透传给客户端。
type emptyOutputRetry struct {
	// maxAttempts 最多尝试次数(含首次)。
	maxAttempts int
}

// shouldRetryEmpty reports whether res looks like an empty/failed generation
// that is worth retrying on the same or a different account:
//   - no visible text, no reasoning, and no semantic tool/progress events
//   - or upstream produced an explicit empty marker
//
// It never retries when the caller already passed the attempt cap.
func (e emptyOutputRetry) shouldRetryEmpty(res chathub.Result, attempt int) bool {
	if attempt >= e.maxAttempts-1 {
		return false
	}
	text := strings.TrimSpace(res.Text)
	reasoning := strings.TrimSpace(res.Reasoning)
	// Semantic events include tool progress (web search, plugins). If the
	// upstream did real work, the round is not empty even when final text is
	// blank; a client-side executor may still be mid-flight.
	for _, ev := range chathub.SemanticEvents(res.Events) {
		kind := strings.ToLower(ev.Kind)
		contentType := strings.ToLower(ev.ContentType)
		if strings.Contains(contentType, "tool") || strings.Contains(contentType, "search") ||
			strings.Contains(contentType, "plugin") || strings.Contains(kind, "tool") {
			return false
		}
	}
	if text == "" && reasoning == "" {
		return true
	}
	// Explicit empty marker from upstream (short responses only, to avoid
	// false positives on legitimate one-word answers).
	low := strings.ToLower(text)
	if len(text) < 40 && (low == "empty" || low == "no output" || strings.Contains(low, "no response generated")) {
		return true
	}
	return false
}

// maxEmptyRetryAttempts is the default cap for empty-output retries.
const maxEmptyRetryAttempts = 3

// newEmptyOutputRetry builds the retry policy. Env override:
//
//	M365_EMPTY_RETRY_ATTEMPTS=0  disables empty-output retry entirely
//	M365_EMPTY_RETRY_ATTEMPTS=N   allows up to N total attempts (default 3)
func newEmptyOutputRetry() emptyOutputRetry {
	n := envInt("M365_EMPTY_RETRY_ATTEMPTS", maxEmptyRetryAttempts)
	if n < 1 {
		n = 1
	}
	if n > 5 {
		n = 5
	}
	return emptyOutputRetry{maxAttempts: n}
}
