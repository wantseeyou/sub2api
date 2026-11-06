package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// GenerateReauthorizationURL 按既有 OpenAI 账号客户端生成 PKCE 授权链接。
func (s *OpenAIOAuthService) GenerateReauthorizationURL(ctx context.Context, account *Account) (*OpenAIAuthURLResult, error) {
	result, err := s.GenerateAuthURL(ctx, account.ProxyID, "", PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(account.GetCredential("client_id"))
	if clientID == "" {
		return result, nil
	}
	session, ok := s.sessionStore.Get(result.SessionID)
	if !ok {
		return nil, accountReauthorizationSessionError()
	}
	copy := *session
	copy.ClientID = clientID
	s.sessionStore.Set(result.SessionID, &copy)
	result.AuthURL, err = accountReauthorizationURLClient(result.AuthURL, clientID)
	return result, err
}

// GenerateReauthorizationURL 按既有 Grok 账号客户端生成 PKCE 授权链接。
func (s *GrokOAuthService) GenerateReauthorizationURL(ctx context.Context, account *Account) (*GrokAuthURLResult, error) {
	result, err := s.GenerateAuthURL(ctx, account.ProxyID, "")
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(account.GetCredential("client_id"))
	if clientID == "" {
		return result, nil
	}
	session, ok := s.sessionStore.Get(result.SessionID)
	if !ok {
		return nil, accountReauthorizationSessionError()
	}
	s.sessionStore.Set(result.SessionID, &xai.OAuthSession{
		State: session.State, CodeVerifier: session.CodeVerifier, CodeChallenge: session.CodeChallenge,
		ClientID: clientID, Scope: session.Scope, ProxyURL: session.ProxyURL,
		RedirectURI: session.RedirectURI, CreatedAt: session.CreatedAt,
	})
	result.AuthURL, err = accountReauthorizationURLClient(result.AuthURL, clientID)
	return result, err
}

// accountReauthorizationURLClient 将既有客户端写入平台已生成的标准授权链接。
func accountReauthorizationURLClient(authURL, clientID string) (string, error) {
	parsed, err := url.Parse(authURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("client_id", clientID)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// AccountReauthorizationService 原子更新认证结果，保留账号当前的业务配置。
type AccountReauthorizationService interface {
	ApplyAccountReauthorization(ctx context.Context, expected *Account, accountType string, credentials, extra map[string]any) (*Account, error)
}

// ApplyAccountReauthorization 在短事务内锁定最新账号并应用上游认证结果。
func (s *adminServiceImpl) ApplyAccountReauthorization(ctx context.Context, expected *Account, accountType string, credentials, extra map[string]any) (*Account, error) {
	tx := dbent.TxFromContext(ctx)
	owned := tx == nil
	if owned {
		if s.entClient == nil {
			return nil, fmt.Errorf("account reauthorization transaction is unavailable")
		}
		var err error
		tx, err = s.entClient.Tx(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
	}
	txCtx := dbent.NewTxContext(ctx, tx)
	if _, err := tx.Client().Account.Query().Where(dbaccount.IDEQ(expected.ID)).Select(dbaccount.FieldID).ForUpdate().Only(txCtx); err != nil {
		return nil, err
	}
	current, err := s.accountRepo.GetByID(txCtx, expected.ID)
	if err != nil {
		return nil, err
	}
	if !sameAccountReauthorizationConfig(expected, current) {
		return nil, infraerrors.New(http.StatusConflict, "ACCOUNT_REAUTHORIZE_CONFIGURATION_CHANGED", "Account authorization configuration changed; restart authorization")
	}
	merged := SanitizeStoredCredentials(current.Platform, MergeCredentials(current.Credentials, credentials))
	// 调度快照由事务 outbox 更新；版本号阻止旧快照把旧 token 重新写入缓存。
	version := time.Now().UnixMilli()
	if previous := current.GetCredentialAsInt64("_token_version"); version <= previous {
		version = previous + 1
	}
	merged["_token_version"] = version
	if _, err := s.UpdateAccount(txCtx, current.ID, &UpdateAccountInput{Type: accountType, Credentials: merged}); err != nil {
		return nil, err
	}
	if len(extra) > 0 {
		if err := s.UpdateAccountExtra(txCtx, current.ID, extra); err != nil {
			return nil, err
		}
	}
	if _, err := s.ClearAccountError(txCtx, current.ID); err != nil {
		return nil, err
	}
	if owned {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return s.GetAccount(ctx, current.ID)
}

// sameAccountReauthorizationConfig 检查交换期间影响认证的账号配置是否变化。
func sameAccountReauthorizationConfig(expected, current *Account) bool {
	if !current.IsOAuth() || current.IsCredentialShadow() || current.Platform != expected.Platform || current.Type != expected.Type {
		return false
	}
	if (current.ProxyID == nil) != (expected.ProxyID == nil) || (current.ProxyID != nil && *current.ProxyID != *expected.ProxyID) {
		return false
	}
	for _, key := range []string{"client_id", "oauth_type", "project_id"} {
		if strings.TrimSpace(current.GetCredential(key)) != strings.TrimSpace(expected.GetCredential(key)) {
			return false
		}
	}
	return true
}

// accountReauthorizationSessionError 返回不包含会话或凭据的配置不匹配错误。
func accountReauthorizationSessionError() error {
	return infraerrors.BadRequest("ACCOUNT_REAUTHORIZE_SESSION_MISMATCH", "Generate an authorization URL using the current account configuration")
}

// ValidateReauthorizationSession 校验 Claude 会话的代理和授权类型符合目标账号。
func (s *OAuthService) ValidateReauthorizationSession(sessionID, proxyURL, accountType string) error {
	session, ok := s.sessionStore.Get(sessionID)
	if !ok || session.ProxyURL != proxyURL || (session.Scope == oauth.ScopeInference) != (accountType == AccountTypeSetupToken) {
		return accountReauthorizationSessionError()
	}
	return nil
}

// ValidateReauthorizationSession 校验 OpenAI 会话使用账号既有的代理和 OAuth 客户端。
func (s *OpenAIOAuthService) ValidateReauthorizationSession(sessionID, proxyURL string, account *Account) error {
	session, ok := s.sessionStore.Get(sessionID)
	if !ok || session.ProxyURL != proxyURL {
		return accountReauthorizationSessionError()
	}
	clientID := strings.TrimSpace(session.ClientID)
	if clientID == "" {
		clientID = openai.ClientID
	}
	expectedClientID := strings.TrimSpace(account.GetCredential("client_id"))
	if expectedClientID == "" {
		expectedClientID = openai.ClientID
	}
	if clientID != expectedClientID {
		return accountReauthorizationSessionError()
	}
	return nil
}

// ValidateReauthorizationSession 校验 Gemini 会话不能改变账号的 OAuth 类型和项目配置。
func (s *GeminiOAuthService) ValidateReauthorizationSession(sessionID, proxyURL string, account *Account) error {
	session, ok := s.sessionStore.Get(sessionID)
	if !ok || session.ProxyURL != proxyURL {
		return accountReauthorizationSessionError()
	}
	oauthType := strings.TrimSpace(account.GetCredential("oauth_type"))
	if oauthType == "" {
		oauthType = "code_assist"
	}
	sessionOAuthType := strings.TrimSpace(session.OAuthType)
	if sessionOAuthType == "" {
		sessionOAuthType = "code_assist"
	}
	if sessionOAuthType != oauthType || strings.TrimSpace(session.ProjectID) != strings.TrimSpace(account.GetCredential("project_id")) ||
		session.TierID != canonicalGeminiTierIDForOAuthType(oauthType, account.GetCredential("tier_id")) {
		return accountReauthorizationSessionError()
	}
	return nil
}

// ValidateReauthorizationSession 校验 Antigravity 会话使用账号既有代理。
func (s *AntigravityOAuthService) ValidateReauthorizationSession(sessionID, proxyURL string) error {
	session, ok := s.sessionStore.Get(sessionID)
	if !ok || session.ProxyURL != proxyURL {
		return accountReauthorizationSessionError()
	}
	return nil
}

// ValidateReauthorizationSession 校验 Grok 会话使用账号既有的代理和 OAuth 客户端。
func (s *GrokOAuthService) ValidateReauthorizationSession(sessionID, proxyURL string, account *Account) error {
	session, ok := s.sessionStore.Get(sessionID)
	if !ok || session.ProxyURL != proxyURL {
		return accountReauthorizationSessionError()
	}
	clientID := strings.TrimSpace(account.GetCredential("client_id"))
	if clientID == "" {
		clientID = xai.EffectiveClientID()
	}
	if strings.TrimSpace(session.ClientID) != clientID {
		return accountReauthorizationSessionError()
	}
	return nil
}
