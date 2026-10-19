package service

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// openAICodexTurnStateHeader 是 Codex 的回合状态头。上游在响应头中铸造该
// 不透明 blob，客户端在同一回合的后续请求中原样回带（codex-rs 侧从
// /responses SSE、/responses/compact JSON 与 WS 握手三种响应中捕获，见
// codex-api/src/sse/responses.rs 与 endpoint/compact.rs）。
const openAICodexTurnStateHeader = "x-codex-turn-state"

const (
	openAICodexOriginCapacity = 8192
	codexDiagnosticContextKey = "codex_diagnostic_request"
)

// turn-state blob 是上游在"出站身份"（含 #5553 指纹收敛改写后的
// installation/session/thread 标识）下铸造的，同账号回放自洽；跨账号回放
// （failover 换号后客户端仍回带旧账号的 blob）是代理链独有、真实 Codex
// 永远不会产生的矛盾信号。溯源表按实际 blob 摘要保存其交付身份，
// 同一会话先后从多个账号取得的不同状态不会互相覆盖。
type openAICodexTurnStateOrigin struct {
	accountID int64     // 实际交付状态的上游账号。
	callerID  int64     // 实际接收状态的调用方 API Key。
	threadKey string    // 原始线程标识，旧客户端退回显式会话。
	turnID    string    // 状态所属的客户端回合。
	namespace string    // 签发时采用的身份规则。
	ambiguous bool      // 同一状态曾交付给不同身份，不能作为自动补入依据。
	expiresAt time.Time // 来源记录的失效时间。
}

// relayOpenAICodexTurnState 将上游响应中的 turn-state 显式写入下游响应头，
// 并记录铸造账号。必须在响应头提交点调用（WriteHeader 之前、且确认本次
// 上游响应就是将要写回客户端的响应之后）。上游无该头时主动清除 writer 上
// 可能残留的上一 failover attempt 的值——否则换号后旧账号的 blob 会粘到
// 新账号的响应上，这正是本文件要防止的跨账号矛盾。
func (s *OpenAIGatewayService) relayOpenAICodexTurnState(c *gin.Context, account *Account, upstream http.Header) {
	if c == nil || c.Writer == nil {
		return
	}
	canonical := http.CanonicalHeaderKey(openAICodexTurnStateHeader)
	state := extractOpenAICodexTurnState(upstream)
	if state == "" {
		c.Writer.Header().Del(canonical)
		return
	}
	c.Writer.Header().Set(canonical, state)
	s.noteOpenAICodexStateValue(c, account, state)
}

// stageOpenAICodexTurnState 将上游 turn-state 暂存到延迟提交的响应头集合
// （首输出守卫路径先缓存头、见到首个输出事件才提交）。此处**不**记录铸造
// 账号：该 attempt 仍可能在首输出超时后 failover，暂存头会被整体丢弃，
// 客户端从未收到该 blob。溯源必须在真正提交时记录，见
// noteStagedOpenAICodexTurnStateCommitted。
func stageOpenAICodexTurnState(dst *http.Header, upstream http.Header) {
	if dst == nil {
		return
	}
	canonical := http.CanonicalHeaderKey(openAICodexTurnStateHeader)
	state := extractOpenAICodexTurnState(upstream)
	if state == "" {
		if *dst != nil {
			dst.Del(canonical)
		}
		return
	}
	if *dst == nil {
		*dst = http.Header{}
	}
	dst.Set(canonical, state)
}

// noteStagedOpenAICodexTurnStateCommitted 在暂存响应头真正写入下游时记录
// 铸造账号——只有此刻客户端才确定收到了该 blob，溯源表才与客户端持有的
// 值一致（否则被 failover 丢弃的 attempt 会污染溯源，导致后续误剥离）。
func (s *OpenAIGatewayService) noteStagedOpenAICodexTurnStateCommitted(c *gin.Context, account *Account, staged http.Header) {
	if staged == nil || strings.TrimSpace(staged.Get(openAICodexTurnStateHeader)) == "" {
		return
	}
	s.noteOpenAICodexStateValue(c, account, extractOpenAICodexTurnState(staged))
}

func extractOpenAICodexTurnState(upstream http.Header) string {
	if upstream == nil {
		return ""
	}
	return strings.TrimSpace(upstream.Get(openAICodexTurnStateHeader))
}

// noteOpenAICodexTurnStateProvenance 记录当前已交付响应头中实际状态值的来源。
func (s *OpenAIGatewayService) noteOpenAICodexTurnStateProvenance(c *gin.Context, account *Account) {
	if c == nil || c.Writer == nil {
		return
	}
	s.noteOpenAICodexStateValue(c, account, extractOpenAICodexTurnState(c.Writer.Header()))
}

// guardOpenAICodexTurnStateEcho 剥离已知账号、调用方或回合不符的回带状态；
// 旧身份保留未知来源值，新隔离身份要求有效来源证明。此处只校验，不注入。
func (s *OpenAIGatewayService) guardOpenAICodexTurnStateEcho(c *gin.Context, account *Account, h http.Header) {
	if s == nil || h == nil || account == nil {
		return
	}
	state := strings.TrimSpace(h.Get(openAICodexTurnStateHeader))
	if state == "" {
		return
	}
	origin, known := s.openAICodexOrigin("state", state)
	// 新身份不能继承任何未证明属于该身份的旧状态；旧模式保留未知回带值。
	if (!known && account.IsCodexThreadIsolationEnabled()) || (known && !origin.ambiguous && !codexOriginMatches(c, account, origin, true)) || (origin.ambiguous && account.IsCodexThreadIsolationEnabled()) {
		h.Del(openAICodexTurnStateHeader)
	}
}

// sweepOpenAICodexTurnStateOrigins 机会式清扫过期溯源记录：每 256 次写入
// 全量遍历一轮，防止仅靠读侧惰性删除导致的慢泄漏（会话键无上界）。
func (s *OpenAIGatewayService) sweepOpenAICodexTurnStateOrigins() {
	if s == nil {
		return
	}
	if s.openaiCodexTurnStateWrites.Add(1)%256 != 0 {
		return
	}
	s.openaiCodexTurnStateMu.Lock()
	defer s.openaiCodexTurnStateMu.Unlock()
	now := time.Now()
	count := 0
	s.openaiCodexTurnStateOrigins.Range(func(key, value any) bool {
		origin, ok := value.(openAICodexTurnStateOrigin)
		if !ok || (!origin.expiresAt.IsZero() && now.After(origin.expiresAt)) {
			s.openaiCodexTurnStateOrigins.Delete(key)
		} else {
			count++
		}
		return true
	})
	s.openaiCodexTurnStateCount = count
}

// openAICodexOriginKey 只以实际状态或响应 ID 的摘要作为缓存键，不保留不透明原文。
func openAICodexOriginKey(kind, value string) string {
	return fmt.Sprintf("%s:%x", kind, sha256.Sum256([]byte(value)))
}

// openAICodexOrigin 返回未过期的实际值来源；缺失及过期均视为来源未知。
func (s *OpenAIGatewayService) openAICodexOrigin(kind, value string) (openAICodexTurnStateOrigin, bool) {
	if s == nil || value == "" {
		return openAICodexTurnStateOrigin{}, false
	}
	raw, ok := s.openaiCodexTurnStateOrigins.Load(openAICodexOriginKey(kind, value))
	if !ok {
		return openAICodexTurnStateOrigin{}, false
	}
	origin, ok := raw.(openAICodexTurnStateOrigin)
	if ok && (origin.expiresAt.IsZero() || time.Now().Before(origin.expiresAt)) {
		return origin, true
	}
	s.openaiCodexTurnStateMu.Lock()
	// 锁内复查，避免删除并发交付刚刷新的同一状态来源。
	key := openAICodexOriginKey(kind, value)
	if current, exists := s.openaiCodexTurnStateOrigins.Load(key); exists {
		latest, valid := current.(openAICodexTurnStateOrigin)
		if !valid || (!latest.expiresAt.IsZero() && !time.Now().Before(latest.expiresAt)) {
			s.openaiCodexTurnStateOrigins.Delete(key)
			if s.openaiCodexTurnStateCount > 0 {
				s.openaiCodexTurnStateCount--
			}
		}
	}
	s.openaiCodexTurnStateMu.Unlock()
	return openAICodexTurnStateOrigin{}, false
}

// codexOriginMatches 按可得的账号、调用方、原始线程与回合信息校验实际值来源。
func codexOriginMatches(c *gin.Context, account *Account, origin openAICodexTurnStateOrigin, includeTurn bool) bool {
	identity := currentCodexClientIdentity(c)
	threadKey := codexIdentityThreadKey(identity)
	if account == nil || account.ID != origin.accountID || origin.namespace != codexIdentityNamespace(c, account) {
		return false
	}
	if origin.callerID != identity.callerID || (origin.threadKey != "" && threadKey != "" && origin.threadKey != threadKey) {
		return false
	}
	return !includeTurn || origin.turnID == "" || identity.turnID == "" || origin.turnID == identity.turnID
}

// noteOpenAICodexOrigin 在交付点记录来源，以锁保证 TTL 清理与硬容量限制。
func (s *OpenAIGatewayService) noteOpenAICodexOrigin(c *gin.Context, account *Account, kind, value string, ttl time.Duration) {
	if s == nil || account == nil || account.ID <= 0 || value == "" || (c != nil && c.GetBool(codexDiagnosticContextKey)) {
		return
	}
	identity := currentCodexClientIdentity(c)
	origin := openAICodexTurnStateOrigin{
		accountID: account.ID,
		callerID:  identity.callerID,
		threadKey: codexIdentityThreadKey(identity),
		turnID:    identity.turnID,
		namespace: codexIdentityNamespace(c, account),
		expiresAt: time.Now().Add(ttl),
	}
	key := openAICodexOriginKey(kind, value)
	s.openaiCodexTurnStateMu.Lock()
	if previous, exists := s.openaiCodexTurnStateOrigins.Load(key); exists {
		if previousOrigin, ok := previous.(openAICodexTurnStateOrigin); ok && time.Now().Before(previousOrigin.expiresAt) {
			origin.ambiguous = previousOrigin.ambiguous || previousOrigin.accountID != origin.accountID || previousOrigin.callerID != origin.callerID || previousOrigin.threadKey != origin.threadKey || previousOrigin.namespace != origin.namespace || (kind == "state" && previousOrigin.turnID != origin.turnID)
		}
	} else {
		if s.openaiCodexTurnStateCount >= openAICodexOriginCapacity {
			s.openaiCodexTurnStateOrigins.Range(func(oldKey, _ any) bool {
				s.openaiCodexTurnStateOrigins.Delete(oldKey)
				s.openaiCodexTurnStateCount--
				return false
			})
		}
		s.openaiCodexTurnStateCount++
	}
	s.openaiCodexTurnStateOrigins.Store(key, origin)
	s.openaiCodexTurnStateMu.Unlock()
	s.sweepOpenAICodexTurnStateOrigins()
}

// noteOpenAICodexStateValue 仅在状态真正交付客户端时记录该状态的来源。
func (s *OpenAIGatewayService) noteOpenAICodexStateValue(c *gin.Context, account *Account, state string) {
	s.noteOpenAICodexOrigin(c, account, "state", state, s.openAIWSSessionStickyTTL())
}

// canReuseOpenAICodexTurnState 仅允许完整匹配已知账号、调用方、线程、回合的自动补入。
func (s *OpenAIGatewayService) canReuseOpenAICodexTurnState(c *gin.Context, account *Account, state string) bool {
	if c != nil && c.GetBool(codexDiagnosticContextKey) {
		return false
	}
	identity := currentCodexClientIdentity(c)
	if identity.callerID <= 0 || codexIdentityThreadKey(identity) == "" || identity.turnID == "" {
		return false
	}
	origin, known := s.openAICodexOrigin("state", state)
	return known && !origin.ambiguous && origin.callerID == identity.callerID && origin.threadKey == codexIdentityThreadKey(identity) && origin.turnID == identity.turnID && codexOriginMatches(c, account, origin, true)
}

// noteCodexResponseIdentity 记录实际交付的响应 ID，供新隔离身份验证后续响应链。
func (s *OpenAIGatewayService) noteCodexResponseIdentity(c *gin.Context, account *Account, responseID string) {
	if !account.IsCodexThreadIsolationEnabled() {
		return
	}
	s.noteOpenAICodexOrigin(c, account, "response", strings.TrimSpace(responseID), s.openAIWSResponseStickyTTL())
}

// guardCodexIdentityContinuation 阻止新隔离身份继续未知或旧身份签发的响应链。
func (s *OpenAIGatewayService) guardCodexIdentityContinuation(c *gin.Context, account *Account, body []byte) error {
	if !account.IsCodexThreadIsolationEnabled() {
		return nil
	}
	identity := currentCodexClientIdentity(c)
	if identity.callerID <= 0 || codexIdentityThreadKey(identity) == "" {
		return fmt.Errorf("codex thread isolation requires a caller and an explicit thread or session identifier")
	}
	responseID := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if responseID == "" {
		return nil
	}
	origin, known := s.openAICodexOrigin("response", responseID)
	if !known || origin.ambiguous || origin.threadKey != codexIdentityThreadKey(identity) || !codexOriginMatches(c, account, origin, false) {
		return fmt.Errorf("previous_response_id belongs to an unknown or different Codex identity; start a new conversation")
	}
	return nil
}
