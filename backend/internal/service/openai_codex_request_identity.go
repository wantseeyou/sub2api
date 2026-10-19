package service

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	codexThreadIsolationExtraKey   = "codex_thread_isolation_enabled"
	codexClientIdentityContextKey  = "codex_client_identity"
	codexWSFrameIdentityContextKey = "codex_ws_frame_identity"
	codexWSFrameFollowupContextKey = "codex_ws_frame_followup"
)

// codexClientIdentity 保存改写前的调用方、线程及回合语义。
type codexClientIdentity struct {
	callerID            int64  // 调用方 API Key 标识。
	sessionID           string // 客户端原始会话标识。
	threadID            string // 客户端原始线程标识。
	turnID              string // 客户端声明的回合标识。
	windowID            string // 客户端声明的上下文窗口标识。
	turnStartedAtUnixMs int64  // 客户端声明的回合开始时间，毫秒。
}

// IsCodexThreadIsolationEnabled 返回账号是否显式启用新的线程隔离规则。
func (a *Account) IsCodexThreadIsolationEnabled() bool {
	if a == nil || !a.UsesOpenAICodexProtocol() {
		return false
	}
	enabled, _ := a.Extra[codexThreadIsolationExtraKey].(bool)
	return enabled
}

// codexIdentityText 选取首个不含控制字符的有效客户端标识。
func codexIdentityText(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !strings.ContainsAny(value, "\x00\r\n") {
			return value
		}
	}
	return ""
}

// codexMetadataString 仅接受 JSON 字符串形式的身份字段。
func codexMetadataString(metadata gjson.Result, name string) string {
	value := metadata.Get(name)
	if value.Type != gjson.String {
		return ""
	}
	return value.String()
}

// codexWindowThread 提取合法窗口标识的线程部分，保留窗口后缀用于出站重写。
func codexWindowThread(window string) string {
	thread, suffix, ok := strings.Cut(window, ":")
	if !ok || thread == "" || suffix == "" {
		return ""
	}
	if _, err := strconv.ParseUint(suffix, 10, 64); err != nil {
		return ""
	}
	return thread
}

// parseCodexClientIdentity 统一读取 HTTP 头及 WS 帧中的原始身份，不生成回合信息。
func parseCodexClientIdentity(c *gin.Context, body []byte) codexClientIdentity {
	var headers http.Header
	if c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	headerMetadata := gjson.Parse(headers.Get(openAIWSTurnMetadataHeader))
	metadata := gjson.GetBytes(body, "client_metadata")
	bodyMetadata := gjson.Parse(metadata.Get(openAIWSTurnMetadataHeader).String())
	frameIdentity := c != nil && c.GetBool(codexWSFrameIdentityContextKey)
	headerTurnID := codexMetadataString(headerMetadata, "turn_id")
	headerTurnStarted := headerMetadata.Get("turn_started_at_unix_ms")
	headerSessionID := codexIdentityText(extractClientSessionID(headers), codexMetadataString(headerMetadata, "session_id"))
	headerThreadID := codexIdentityText(headers.Get(openAIWSThreadIDHeader), codexMetadataString(headerMetadata, "thread_id"), codexWindowThread(strings.TrimSpace(headers.Get(openAIWSWindowIDHeader))))
	headerWindowID := codexIdentityText(headers.Get(openAIWSWindowIDHeader), codexMetadataString(headerMetadata, "window_id"))
	if c != nil && c.GetBool(codexWSFrameFollowupContextKey) {
		headerTurnID = ""
		headerTurnStarted = gjson.Result{}
		if value, ok := c.Get(codexClientIdentityContextKey); ok {
			if previous, ok := value.(codexClientIdentity); ok {
				headerSessionID = codexIdentityText(previous.sessionID, headerSessionID)
				headerThreadID = codexIdentityText(previous.threadID, headerThreadID)
				headerWindowID = codexIdentityText(previous.windowID, headerWindowID)
			}
		}
	}
	read := func(header, flat, embedded string) string {
		if frameIdentity {
			return codexIdentityText(codexMetadataString(metadata, flat), codexMetadataString(bodyMetadata, embedded), header)
		}
		return codexIdentityText(header, codexMetadataString(metadata, flat), codexMetadataString(bodyMetadata, embedded))
	}
	identity := codexClientIdentity{
		callerID:  getAPIKeyIDFromContext(c),
		sessionID: read(headerSessionID, "session_id", "session_id"),
		threadID:  read(headerThreadID, "thread_id", "thread_id"),
		turnID:    read(headerTurnID, "turn_id", "turn_id"),
		windowID:  read(headerWindowID, openAIWSWindowIDHeader, "window_id"),
	}
	if identity.threadID == "" {
		identity.threadID = codexWindowThread(identity.windowID)
	}
	startValues := []gjson.Result{headerTurnStarted, metadata.Get("turn_started_at_unix_ms"), bodyMetadata.Get("turn_started_at_unix_ms")}
	if frameIdentity {
		startValues = []gjson.Result{startValues[1], startValues[2], startValues[0]}
	}
	for _, value := range startValues {
		if started, err := strconv.ParseInt(value.String(), 10, 64); err == nil && started > 0 {
			identity.turnStartedAtUnixMs = started
			break
		}
	}
	return identity
}

// currentCodexClientIdentity 读取本次请求的原始身份，缺少快照时仅解析现有请求头。
func currentCodexClientIdentity(c *gin.Context) codexClientIdentity {
	if c != nil {
		if value, ok := c.Get(codexClientIdentityContextKey); ok {
			if identity, ok := value.(codexClientIdentity); ok {
				return identity
			}
		}
	}
	return parseCodexClientIdentity(c, nil)
}

// codexIdentityThreadKey 返回可稳定复用的原始线程，旧客户端退回显式会话标识。
func codexIdentityThreadKey(identity codexClientIdentity) string {
	if identity.threadID != "" {
		return "thread:" + identity.threadID
	}
	if identity.sessionID != "" {
		return "session:" + identity.sessionID
	}
	return ""
}

// codexIdentityNamespace 将账号隔离配置与原始调用方、线程绑定为新的身份域。
func codexIdentityNamespace(c *gin.Context, account *Account) string {
	if !account.IsCodexThreadIsolationEnabled() {
		return "legacy"
	}
	identity := currentCodexClientIdentity(c)
	seed, _ := codexFingerprintSeed(account.Extra)
	return deriveStableUUIDv4(fmt.Sprintf("sub2api:codex-thread:v3:%d:%s:%s:%d:%s", account.ID, seed, account.GetCodexFingerprintMode(), identity.callerID, codexIdentityThreadKey(identity)))
}

// resolveCodexFingerprintIDsForRequest 为同一次发送解析唯一身份快照，供请求头与请求体共用。
func resolveCodexFingerprintIDsForRequest(c *gin.Context, account *Account, body []byte) *codexFingerprintIDs {
	identity := parseCodexClientIdentity(c, body)
	if c != nil {
		c.Set(codexClientIdentityContextKey, identity)
	}
	if account == nil {
		return nil
	}
	ids := resolveCodexFingerprintIDs(account, identity.sessionID, account.GetCodexFingerprintMode())
	if account.IsCodexThreadIsolationEnabled() && codexIdentityThreadKey(identity) != "" {
		if ids == nil {
			ids = &codexFingerprintIDs{accountID: account.ID, mode: codexFingerprintOff}
		}
		ids.threadID = codexIdentityNamespace(c, account)
		ids.threadIsolated = true
	}
	if ids == nil {
		return nil
	}
	applyCodexFingerprintTurnSemantics(ids, identity)
	return ids
}

// applyCodexFingerprintTurnSemantics 保留回合及窗口语义，仅替换窗口中的线程前缀。
func applyCodexFingerprintTurnSemantics(ids *codexFingerprintIDs, identity codexClientIdentity) {
	if ids == nil {
		return
	}
	ids.turnID = identity.turnID
	ids.turnStartedAtUnixMs = identity.turnStartedAtUnixMs
	ids.windowID = identity.windowID
	if ids.threadID != "" && codexWindowThread(identity.windowID) != "" {
		_, suffix, _ := strings.Cut(identity.windowID, ":")
		ids.windowID = ids.threadID + ":" + suffix
	}
}

// codexFingerprintMetadataFields 统一生成头与请求体需要改写的身份字段。
func codexFingerprintMetadataFields(ids *codexFingerprintIDs) map[string]any {
	fields := make(map[string]any, 7)
	if ids.installationID != "" {
		fields["installation_id"] = ids.installationID
	}
	if ids.mode == codexFingerprintDevice && !ids.threadIsolated {
		return fields
	}
	for name, value := range map[string]string{"session_id": ids.sessionID, "thread_id": ids.threadID, "turn_id": ids.turnID, "window_id": ids.windowID} {
		if value != "" {
			fields[name] = value
		}
	}
	if ids.turnStartedAtUnixMs > 0 {
		fields["turn_started_at_unix_ms"] = ids.turnStartedAtUnixMs
	}
	return fields
}

// codexIdentityRouteScope 避免新的隔离身份复用旧身份的 WS 会话路由状态。
func (s *OpenAIGatewayService) codexIdentityRouteScope(c *gin.Context, account *Account, scope string) string {
	if scope == "" || !account.IsCodexThreadIsolationEnabled() {
		return scope
	}
	return deriveStableUUIDv4("sub2api:codex-route:v3:" + codexIdentityNamespace(c, account) + ":" + scope)
}
