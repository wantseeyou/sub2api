package service

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type openAIPassthroughReadEvent struct {
	line string // 已完成的 SSE 文本行。
	err  error  // 上游扫描失败原因。
	done bool   // 扫描已结束。
}

// readOpenAIWSWithKeepalive 等待 WS 事件期间由调用者串行发送 SSE 心跳。
func readOpenAIWSWithKeepalive(ctx context.Context, lease *openAIWSConnLease, timeout time.Duration, keepalive <-chan time.Time, beat func()) ([]byte, error) {
	if keepalive == nil {
		return lease.ReadMessageWithContextTimeout(ctx, timeout)
	}
	type readResult struct {
		message []byte // 上游完整消息。
		err     error  // 上游读取错误。
	}
	result := make(chan readResult, 1)
	go func() {
		message, err := lease.ReadMessageWithContextTimeout(ctx, timeout)
		result <- readResult{message: message, err: err}
	}()
	for {
		select {
		case r := <-result:
			return r.message, r.err
		case <-keepalive:
			beat()
		}
	}
}

const openAIForwardAttemptLockedKey = "openai_forward_attempt_locked"
const openAIWSExecutionStageKey = "openai_ws_execution_stage"

const (
	openAIWSNotSent          = "not_sent"
	openAIWSExecutionUnknown = "execution_unknown"
	openAIWSResponseReceived = "response_received"
	openAIWSRejected         = "rejected"
	openAIWSFinished         = "finished"
)

// IsOpenAIHTTPToWSEnabled 读取默认关闭的 HTTP 入站转 WS 账号开关。
func (a *Account) IsOpenAIHTTPToWSEnabled() bool {
	if a == nil || !a.IsOpenAIOAuthLike() || a.IsOpenAIAgentIdentity() {
		return false
	}
	enabled, _ := a.Extra["openai_http_to_ws_enabled"].(bool)
	return enabled
}

// resolveOpenAIForwardTransport 统一入口传输选择并保留账号、插件及 compact 限制。
func (s *OpenAIGatewayService) resolveOpenAIForwardTransport(c *gin.Context, account *Account) OpenAIWSProtocolDecision {
	decision := s.getOpenAIWSProtocolResolver().Resolve(account)
	if isOpenAIResponsesCompactPath(c) {
		return openAIWSHTTPDecision("compact_http")
	}
	if s.pluginManager != nil && s.pluginManager.ShouldRouteOpenAIOAuth(account) {
		return openAIWSHTTPDecision("plugin_http_transport")
	}
	if !account.IsOpenAIHTTPToWSEnabled() {
		return resolveOpenAIWSDecisionByClientTransport(decision, GetOpenAIClientTransport(c))
	}
	return decision
}

// OpenAIForwardAttemptLocked 判断响应提交或 WS 执行状态是否已禁止透明换号。
func OpenAIForwardAttemptLocked(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if c.GetBool(openAIForwardAttemptLockedKey) {
		return true
	}
	switch c.GetString(openAIWSExecutionStageKey) {
	case openAIWSExecutionUnknown, openAIWSResponseReceived, openAIWSFinished:
		return true
	}
	return false
}

// lockOpenAIForwardAttempt 在首次提交 Codex 响应头时锁定当前上游尝试。
func lockOpenAIForwardAttempt(c *gin.Context, account *Account) {
	if c != nil && account != nil && account.IsOpenAIOAuthLike() {
		c.Set(openAIForwardAttemptLockedKey, true)
	}
}

// writeOpenAICommittedFailure 在已提交的 SSE 中仅写一次合法失败终态。
func writeOpenAICommittedFailure(c *gin.Context, responseID, model, message string) {
	if c == nil || c.Writer == nil || !c.Writer.Written() || IsResponseCommitted(c) {
		return
	}
	if !strings.HasPrefix(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
		return
	}
	MarkResponseCommitted(c)
	// 上游可能在最后一条 data 行后直接 EOF，先补事件边界。
	if _, err := io.WriteString(c.Writer, "\n"+buildOpenAIResponseFailedSSE(responseID, model, 0, nil, message)); err != nil {
		return
	}
	c.Writer.Flush()
}
