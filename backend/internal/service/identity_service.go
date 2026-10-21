package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// defaultFingerprint 从同一份固定版本头部生成账号环境，避免 SDK 与运行时配置漂移。
func defaultFingerprint() Fingerprint {
	headers := claude.DefaultHeaders()
	return Fingerprint{
		UserAgent:               headers["User-Agent"],
		StainlessLang:           headers["X-Stainless-Lang"],
		StainlessPackageVersion: headers["X-Stainless-Package-Version"],
		StainlessOS:             headers["X-Stainless-OS"],
		StainlessArch:           headers["X-Stainless-Arch"],
		StainlessRuntime:        headers["X-Stainless-Runtime"],
		StainlessRuntimeVersion: headers["X-Stainless-Runtime-Version"],
	}
}

// Fingerprint represents account fingerprint data
type Fingerprint struct {
	ClientID                string
	UserAgent               string
	StainlessLang           string
	StainlessPackageVersion string
	StainlessOS             string
	StainlessArch           string
	StainlessRuntime        string
	StainlessRuntimeVersion string
	UpdatedAt               int64 `json:",omitempty"` // Unix timestamp，用于判断是否需要续期TTL
}

// IdentityCache defines cache operations for identity service
type IdentityCache interface {
	GetFingerprint(ctx context.Context, accountID int64) (*Fingerprint, error)
	SetFingerprint(ctx context.Context, accountID int64, fp *Fingerprint) error
	// GetMaskedSessionID 获取固定的会话ID（用于会话ID伪装功能）
	// 返回的 sessionID 是一个 UUID 格式的字符串
	// 如果不存在或已过期（15分钟无请求），返回空字符串
	GetMaskedSessionID(ctx context.Context, accountID int64) (string, error)
	// SetMaskedSessionID 设置固定的会话ID，TTL 为 15 分钟
	// 每次调用都会刷新 TTL
	SetMaskedSessionID(ctx context.Context, accountID int64, sessionID string) error
}

// IdentityService 管理OAuth账号的请求身份指纹
type IdentityService struct {
	cache IdentityCache
}

// NewIdentityService 创建新的IdentityService
func NewIdentityService(cache IdentityCache) *IdentityService {
	return &IdentityService{cache: cache}
}

// GetOrCreateFingerprint 获取账号的固定版本指纹，迁移环境字段时保留已有设备标识。
func (s *IdentityService) GetOrCreateFingerprint(ctx context.Context, accountID int64, _ http.Header) (*Fingerprint, error) {
	fp := defaultFingerprint()
	cached, err := s.cache.GetFingerprint(ctx, accountID)
	if err == nil && cached != nil {
		fp.ClientID = cached.ClientID
		fp.UpdatedAt = cached.UpdatedAt
		if fp.ClientID != "" && fp == *cached && time.Since(time.Unix(fp.UpdatedAt, 0)) <= 24*time.Hour {
			return &fp, nil
		}
	} else {
		if err != nil {
			logger.LegacyPrintf("service.identity", "Warning: failed to read fingerprint for account %d: %v", accountID, err)
		}
	}
	if fp.ClientID == "" {
		fp.ClientID = generateClientID()
	}

	fp.UpdatedAt = time.Now().Unix()
	if err := s.cache.SetFingerprint(ctx, accountID, &fp); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to cache fingerprint for account %d: %v", accountID, err)
	}
	return &fp, nil
}

// ApplyFingerprint 将指纹应用到请求头（覆盖原有的x-stainless-*头）
// 使用 setHeaderRaw 保持原始大小写（如 X-Stainless-OS 而非 X-Stainless-Os）
func (s *IdentityService) ApplyFingerprint(req *http.Request, fp *Fingerprint) {
	if fp == nil {
		return
	}

	// 设置user-agent
	if fp.UserAgent != "" {
		setHeaderRaw(req.Header, "User-Agent", fp.UserAgent)
	}

	// 设置x-stainless-*头（保持与 claude.DefaultHeaders() 一致的大小写）
	if fp.StainlessLang != "" {
		setHeaderRaw(req.Header, "X-Stainless-Lang", fp.StainlessLang)
	}
	if fp.StainlessPackageVersion != "" {
		setHeaderRaw(req.Header, "X-Stainless-Package-Version", fp.StainlessPackageVersion)
	}
	if fp.StainlessOS != "" {
		setHeaderRaw(req.Header, "X-Stainless-OS", fp.StainlessOS)
	}
	if fp.StainlessArch != "" {
		setHeaderRaw(req.Header, "X-Stainless-Arch", fp.StainlessArch)
	}
	if fp.StainlessRuntime != "" {
		setHeaderRaw(req.Header, "X-Stainless-Runtime", fp.StainlessRuntime)
	}
	if fp.StainlessRuntimeVersion != "" {
		setHeaderRaw(req.Header, "X-Stainless-Runtime-Version", fp.StainlessRuntimeVersion)
	}
}

// RewriteUserID 用上游账号身份重建 metadata，仅保留协议支持的 user_id，不改动对话字节。
func (s *IdentityService) RewriteUserID(body []byte, accountID int64, accountUUID, cachedClientID, fingerprintUA string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	if cachedClientID == "" {
		return nil, fmt.Errorf("rewrite OAuth metadata: missing gateway device identity")
	}
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("rewrite OAuth metadata: invalid JSON body")
	}

	metadata := gjson.GetBytes(body, "metadata")
	if !metadata.Exists() {
		return body, nil
	}

	// 无法识别的客户端标识也只作为派生种子，不能回退为原样透传。
	userID := metadata.Get("user_id")
	sessionTail := ""
	if userID.Type == gjson.String && userID.String() != "" {
		if parsed := ParseMetadataUserID(userID.String()); parsed != nil {
			sessionTail = parsed.SessionID
		} else {
			sessionTail = userID.String()
		}
	} else {
		sessionTail = generateRandomUUID()
	}

	// 生成新的session hash: SHA256(accountID::sessionTail) -> UUID格式
	seed := fmt.Sprintf("%d::%s", accountID, sessionTail)
	newSessionHash := generateUUIDFromSeed(seed)

	// 根据客户端版本选择输出格式
	version := ExtractCLIVersion(fingerprintUA)
	newUserID := FormatMetadataUserID(cachedClientID, accountUUID, newSessionHash, version)
	rawMetadata, err := marshalAnthropicMetadata(newUserID)
	if err != nil {
		return nil, fmt.Errorf("marshal OAuth metadata: %w", err)
	}
	newBody, err := sjson.SetRawBytes(body, "metadata", rawMetadata)
	if err != nil {
		return nil, fmt.Errorf("rewrite OAuth metadata: %w", err)
	}
	return newBody, nil
}

// RewriteUserIDWithMasking 重写body中的metadata.user_id，支持会话ID伪装
// 如果账号启用了会话ID伪装（session_id_masking_enabled），
// 则在完成常规重写后，将 session 部分替换为固定的伪装ID（15分钟内保持不变）
//
// 重要：此函数使用 json.RawMessage 保留其他字段的原始字节，
// 避免重新序列化导致 thinking 块等内容被修改。
func (s *IdentityService) RewriteUserIDWithMasking(ctx context.Context, body []byte, account *Account, accountUUID, cachedClientID, fingerprintUA string) ([]byte, error) {
	// 先执行常规的 RewriteUserID 逻辑
	newBody, err := s.RewriteUserID(body, account.ID, accountUUID, cachedClientID, fingerprintUA)
	if err != nil {
		return newBody, err
	}

	// 检查是否启用会话ID伪装
	if !account.IsSessionIDMaskingEnabled() {
		return newBody, nil
	}

	metadata := gjson.GetBytes(newBody, "metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return newBody, nil
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return newBody, nil
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return newBody, nil
	}
	userID := userIDResult.String()
	if userID == "" {
		return newBody, nil
	}

	// 解析已重写的 user_id
	uidParsed := ParseMetadataUserID(userID)
	if uidParsed == nil {
		return newBody, nil
	}

	// 获取或生成固定的伪装 session ID
	maskedSessionID, err := s.cache.GetMaskedSessionID(ctx, account.ID)
	if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to get masked session ID for account %d: %v", account.ID, err)
		return newBody, nil
	}

	if maskedSessionID == "" {
		// 首次或已过期，生成新的伪装 session ID
		maskedSessionID = generateRandomUUID()
		logger.LegacyPrintf("service.identity", "Generated new masked session ID for account %d: %s", account.ID, maskedSessionID)
	}

	// 刷新 TTL（每次请求都刷新，保持 15 分钟有效期）
	if err := s.cache.SetMaskedSessionID(ctx, account.ID, maskedSessionID); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to set masked session ID for account %d: %v", account.ID, err)
	}

	// 用 FormatMetadataUserID 重建（保持与 RewriteUserID 相同的格式）
	version := ExtractCLIVersion(fingerprintUA)
	newUserID := FormatMetadataUserID(uidParsed.DeviceID, uidParsed.AccountUUID, maskedSessionID, version)

	slog.Debug("session_id_masking_applied",
		"account_id", account.ID,
		"before", userID,
		"after", newUserID,
	)

	if newUserID == userID {
		return newBody, nil
	}

	maskedBody, setErr := sjson.SetBytes(newBody, "metadata.user_id", newUserID)
	if setErr != nil {
		return nil, fmt.Errorf("rewrite masked OAuth metadata: %w", setErr)
	}
	return maskedBody, nil
}

// generateRandomUUID 生成随机 UUID v4 格式字符串
func generateRandomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// fallback: 使用时间戳生成
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		b = h[:16]
	}

	// 设置 UUID v4 版本和变体位
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// generateClientID 生成64位十六进制客户端ID（32字节随机数）
func generateClientID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// 极罕见的情况，使用时间戳+固定值作为fallback
		logger.LegacyPrintf("service.identity", "Warning: crypto/rand.Read failed: %v, using fallback", err)
		// 使用SHA256(当前纳秒时间)作为fallback
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return hex.EncodeToString(h[:])
	}
	return hex.EncodeToString(b)
}

// generateUUIDFromSeed 从种子生成确定性UUID v4格式字符串
func generateUUIDFromSeed(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	bytes := hash[:16]

	// 设置UUID v4版本和变体位
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}
