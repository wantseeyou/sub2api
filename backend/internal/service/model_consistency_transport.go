package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/modeltrace"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const modelConsistencyResponseLimit = 2 << 20
const modelConsistencyErrorBodyLimit = 8 << 10

// diagnosticToken 仅读取可用凭据，不进入刷新、停调或错误标记路径。
func (s *ModelConsistencyService) diagnosticToken(ctx context.Context, account *Account) string {
	token := strings.TrimSpace(account.GetOpenAIAccessToken())
	expires := account.GetCredentialAsTime("expires_at")
	if expires == nil && token != "" {
		if claims, err := openai.DecodeIDToken(token); err == nil && claims.Exp > 0 {
			expiry := time.Unix(claims.Exp, 0)
			expires = &expiry
		}
	}
	if token != "" && ((expires != nil && expires.After(time.Now().Add(modelConsistencySampleTimeout))) || (expires == nil && account.IsOpenAIPersonalAccessToken())) {
		return token
	}
	provider := s.gateway.openAITokenProvider
	if provider != nil && provider.tokenCache != nil {
		identity := *account
		if account.ParentAccountID != nil {
			identity.ID = *account.ParentAccountID
		}
		if cached, err := provider.tokenCache.GetAccessToken(ctx, OpenAITokenCacheKey(&identity)); err == nil {
			return strings.TrimSpace(cached)
		}
	}
	return ""
}

// sample 为单份样本取得账号并发名额，使用独立身份和可取消的专用传输。
func (s *ModelConsistencyService) sample(parent context.Context, account *Account, model string, challenge modeltrace.ModelTraceChallenge, transport string) (string, *ModelConsistencyUsage, int, string) {
	ctx, cancel := context.WithTimeout(parent, modelConsistencySampleTimeout)
	defer cancel()
	slot, err := s.concurrency.AcquireAccountSlot(ctx, account.ID, account.Concurrency)
	if err != nil || slot == nil || !slot.Acquired {
		return "", nil, 0, "account_busy"
	}
	defer slot.ReleaseFunc()
	token := s.diagnosticToken(ctx, account)
	if token == "" {
		return "", nil, 0, "credentials_unavailable"
	}
	proxyURL := ""
	if account.ProxyID != nil {
		if account.Proxy == nil || !account.Proxy.IsActive() || account.Proxy.IsExpired(time.Now()) {
			return "", nil, 0, "proxy_unavailable"
		}
		proxyURL = account.Proxy.URL()
		if proxyURL == "" {
			return "", nil, 0, "proxy_unavailable"
		}
	}
	c := modelConsistencyContext(ctx)
	thread, turn, cacheKey := uuid.NewString(), uuid.NewString(), uuid.NewString()
	metadata := map[string]any{"thread_id": thread, "turn_id": turn, "turn_started_at_unix_ms": time.Now().UnixMilli(), "session_id": thread, "window_id": thread + ":0"}
	metadataJSON, err := marshalCodexTurnMetadata(metadata)
	if err != nil {
		return "", nil, 0, "invalid_request"
	}
	for _, header := range []string{"session-id", "session_id", "thread-id", "x-client-request-id", "conversation_id"} {
		c.Request.Header.Set(header, thread)
	}
	c.Request.Header.Set("x-codex-window-id", thread+":0")
	c.Request.Header.Set("x-codex-turn-metadata", string(metadataJSON))
	ids := resolveCodexFingerprintIDsFromRequest(account, c.Request.Header)
	if ids == nil {
		ids = &codexFingerprintIDs{accountID: account.ID, mode: codexFingerprintFull}
	}
	// 诊断保留安装指纹，并对所有模式完整应用独立样本身份。
	ids.mode = codexFingerprintFull
	ids.sessionID, ids.threadID, ids.turnID, ids.windowID = thread, thread, turn, thread+":0"
	ids.turnStartedAtUnixMs = time.Now().UnixMilli()
	stageCodexFingerprintIDs(c, ids)
	body := map[string]any{
		"model": model, "stream": true, "store": false, "instructions": "",
		"input":            []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": challenge.Prompt}}}},
		"prompt_cache_key": cacheKey,
		"client_metadata":  map[string]any{"x-codex-turn-metadata": string(metadataJSON)},
	}
	applyStagedCodexFingerprintClientMetadata(c, account, body)
	// 不将随机缓存键收敛到任何业务会话。
	body["prompt_cache_key"] = cacheKey
	if transport == "websocket" {
		decision := s.gateway.resolveOpenAIForwardTransport(c, account)
		if decision.Transport != OpenAIUpstreamTransportResponsesWebsocketV2 {
			return "", nil, 0, "configuration_changed"
		}
		headers, _, err := s.gateway.buildOpenAIWSHeaders(ctx, c, account, token, decision, true, "", string(metadataJSON), cacheKey, model, "")
		if err != nil {
			return "", nil, 0, "invalid_request"
		}
		headers.Del(openAIWSTurnStateHeader)
		headers.Set("conversation_id", thread)
		// 将构造后的回合头同步到帧，避免出现不同的 turn_id/开始时间。
		setOpenAIWSTurnMetadata(body, headers.Get(openAIWSTurnMetadataHeader))
		wsURL, err := s.gateway.buildOpenAIResponsesWSURL(account)
		if err != nil {
			return "", nil, 0, "invalid_request"
		}
		conn, status, _, err := s.gateway.getOpenAIWSPassthroughDialer().Dial(ctx, wsURL, headers, proxyURL)
		if err != nil {
			if status >= http.StatusBadRequest {
				var body []byte
				var handshakeErr *openAIWSHandshakeError
				if errors.As(err, &handshakeErr) && handshakeErr != nil {
					body = handshakeErr.Body
				}
				return "", nil, status, modelConsistencyHTTPFailureReason(status, body)
			}
			if status > 0 {
				return "", nil, status, "websocket_handshake_rejected"
			}
			return "", nil, 0, modelConsistencyTransportReason(ctx)
		}
		if conn == nil {
			return "", nil, 0, "transport_error"
		}
		defer func() {
			if closer, ok := conn.(openAIWSForceCloser); ok {
				_ = closer.CloseNow()
			} else {
				_ = conn.Close()
			}
		}()
		if err := conn.WriteJSON(ctx, s.gateway.buildOpenAIWSCreatePayload(body, account)); err != nil {
			return "", nil, 0, modelConsistencyTransportReason(ctx)
		}
		output, usage, reason := readModelConsistencyWebSocket(ctx, conn)
		return output, usage, 0, reason
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", nil, 0, "invalid_request"
	}
	request, err := s.gateway.buildUpstreamRequest(ctx, c, account, encoded, token, true, cacheKey, true)
	if err != nil {
		return "", nil, 0, "invalid_request"
	}
	request.Header.Del(openAIWSTurnStateHeader)
	request.Header.Del("Cookie")
	request.Header.Set("conversation_id", thread)
	response, err := s.gateway.doOpenAIUpstream(request, proxyURL, account)
	if err != nil {
		return "", nil, 0, modelConsistencyTransportReason(ctx)
	}
	if response == nil || response.Body == nil {
		return "", nil, 0, "invalid_response"
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		body, err := io.ReadAll(io.LimitReader(response.Body, modelConsistencyErrorBodyLimit))
		if err != nil {
			return "", nil, response.StatusCode, modelConsistencyHTTPFailureReason(response.StatusCode, nil)
		}
		return "", nil, response.StatusCode, modelConsistencyHTTPFailureReason(response.StatusCode, body)
	}
	if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return "", nil, 0, "invalid_response"
	}
	output, usage, reason := readModelConsistencySSE(ctx, response.Body)
	return output, usage, 0, reason
}

// modelConsistencyHTTPFailureReason 仅输出固定错误分类，不保存或回显上游错误正文。
func modelConsistencyHTTPFailureReason(status int, body []byte) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		if json.Valid(body) {
			if isOpenAIInstructionsRequiredError(status, "", body) {
				return "upstream_instructions_rejected"
			}
			switch gjson.GetBytes(body, "error.code").String() {
			case "model_not_found", "model_not_supported", "unsupported_model":
				return "upstream_model_unavailable"
			}
		}
		return "upstream_request_rejected"
	case http.StatusUnauthorized:
		return "upstream_authentication_failed"
	case http.StatusForbidden:
		return "upstream_access_denied"
	case http.StatusNotFound:
		return "upstream_model_unavailable"
	case http.StatusTooManyRequests:
		return "upstream_rate_limited"
	default:
		if status >= http.StatusInternalServerError && status <= 599 {
			return "upstream_service_unavailable"
		}
		return "upstream_http_error"
	}
}

// modelConsistencyTransportReason 将传输错误转换为不含上游正文的稳定原因码。
func modelConsistencyTransportReason(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if ctx.Err() != nil {
		return "cancelled"
	}
	return "transport_error"
}

// readModelConsistencySSE 仅接受正常成功终态，部分文本和缺失终态的 EOF 均失败。
func readModelConsistencySSE(ctx context.Context, reader io.Reader) (string, *ModelConsistencyUsage, string) {
	scanner := bufio.NewScanner(io.LimitReader(reader, modelConsistencyResponseLimit))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data strings.Builder
	for scanner.Scan() {
		if ctx.Err() != nil {
			return "", nil, modelConsistencyTransportReason(ctx)
		}
		line := scanner.Text()
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			output, usage, done, reason := parseModelConsistencyEvent([]byte(strings.TrimSuffix(data.String(), "\n")))
			data.Reset()
			if done {
				return output, usage, reason
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			_, _ = data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			_ = data.WriteByte('\n')
		}
	}
	if scanner.Err() != nil {
		return "", nil, modelConsistencyTransportReason(ctx)
	}
	if data.Len() > 0 {
		output, usage, done, reason := parseModelConsistencyEvent([]byte(strings.TrimSuffix(data.String(), "\n")))
		if done {
			return output, usage, reason
		}
	}
	return "", nil, "missing_completion"
}

// readModelConsistencyWebSocket 读取诊断独占连接，任何发送后异常均直接失败而不重放。
func readModelConsistencyWebSocket(ctx context.Context, conn openAIWSClientConn) (string, *ModelConsistencyUsage, string) {
	bytesRead := 0
	for bytesRead < modelConsistencyResponseLimit {
		message, err := conn.ReadMessage(ctx)
		if err != nil {
			return "", nil, modelConsistencyTransportReason(ctx)
		}
		bytesRead += len(message)
		if bytesRead > modelConsistencyResponseLimit {
			return "", nil, "invalid_response"
		}
		output, usage, done, reason := parseModelConsistencyEvent(message)
		if done {
			return output, usage, reason
		}
	}
	return "", nil, "invalid_response"
}

// parseModelConsistencyEvent 只提取成功终态的文本和用量，并拒绝工具调用或异常终态。
func parseModelConsistencyEvent(data []byte) (string, *ModelConsistencyUsage, bool, string) {
	if string(data) == "[DONE]" {
		return "", nil, true, "missing_completion"
	}
	if !json.Valid(data) {
		return "", nil, true, "invalid_response"
	}
	event := gjson.ParseBytes(data)
	typeName := event.Get("type").String()
	switch typeName {
	case "response.failed", "error":
		return "", nil, true, modelConsistencyEventFailureReason(event)
	case "response.incomplete":
		return "", nil, true, "response_incomplete"
	case "response.output_item.added", "response.output_item.done":
		itemType := event.Get("item.type").String()
		if itemType != "message" && itemType != "reasoning" {
			return "", nil, true, "invalid_response"
		}
	case "response.completed", "response.done":
		response := event.Get("response")
		var usage *ModelConsistencyUsage
		if response.Get("usage").IsObject() {
			usage = &ModelConsistencyUsage{InputTokens: response.Get("usage.input_tokens").Int(), OutputTokens: response.Get("usage.output_tokens").Int()}
		}
		if response.Get("status").String() != "completed" || !response.Get("output").IsArray() {
			return "", usage, true, "invalid_response"
		}
		var text strings.Builder
		invalid := false
		response.Get("output").ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "reasoning" {
				return true
			}
			if item.Get("type").String() != "message" || item.Get("role").String() != "assistant" {
				invalid = true
				return false
			}
			item.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() != "output_text" || part.Get("text").Type != gjson.String {
					invalid = true
					return false
				}
				_, _ = text.WriteString(part.Get("text").String())
				return true
			})
			return !invalid
		})
		if invalid || text.Len() == 0 || text.Len() > 32<<10 {
			return "", usage, true, "invalid_response"
		}
		return text.String(), usage, true, ""
	}
	if typeName == "" {
		return "", nil, true, "invalid_response"
	}
	return "", nil, false, ""
}

// modelConsistencyEventFailureReason 仅将已知上游错误码归类，不保存错误正文或推测 HTTP 状态。
func modelConsistencyEventFailureReason(event gjson.Result) string {
	for _, path := range []string{"error.code", "response.error.code", "error.type", "response.error.type"} {
		switch strings.ToLower(strings.TrimSpace(event.Get(path).String())) {
		case "server_is_overloaded", "slow_down", "server_error", "service_unavailable", "service_unavailable_error", "service_unavailable_err":
			return "upstream_service_unavailable"
		case "rate_limit_exceeded", "rate_limit_error", "insufficient_quota", "usage_limit_reached":
			return "upstream_rate_limited"
		case "invalid_api_key", "token_expired", "invalid_token", "authentication_error":
			return "upstream_authentication_failed"
		case "permission_denied", "permission_error", "access_denied":
			return "upstream_access_denied"
		case "model_not_found", "model_not_supported", "unsupported_model":
			return "upstream_model_unavailable"
		case "invalid_request_error", "invalid_request", "unsupported_parameter", "invalid_value":
			return "upstream_request_rejected"
		}
	}
	return "response_failed"
}
