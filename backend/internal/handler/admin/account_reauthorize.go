package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// AccountReauthorizeHandler 在服务端完成授权交换和账号凭据更新。
type AccountReauthorizeHandler struct {
	accounts *AccountHandler           // 复用账号服务和脱敏响应构建能力。
	grok     *service.GrokOAuthService // 执行 Grok 授权码及 SSO 交换。
}

// AccountReauthorizeRequest 只接收授权输入，禁止直接提交凭据和账号配置。
type AccountReauthorizeRequest struct {
	Platform     string `json:"platform"`       // 目标平台，必须与账号一致。
	Method       string `json:"method"`         // 授权方式：code、cookie、refresh_token 或 sso。
	Type         string `json:"type,omitempty"` // Claude 授权类型，省略时沿用账号类型。
	SessionID    string `json:"session_id"`     // 服务端创建的授权会话。
	Code         string `json:"code"`           // 授权码或 Claude Cookie。
	State        string `json:"state"`          // OAuth 回调的状态校验值。
	RefreshToken string `json:"refresh_token"`  // 用户新提交的 Refresh Token。
	SSOToken     string `json:"sso_token"`      // 用户新提交的 Grok SSO Cookie。
}

// AccountReauthorizeAuthURLRequest 仅允许选择 Claude 的重新授权类型。
type AccountReauthorizeAuthURLRequest struct {
	Type string `json:"type,omitempty"` // Claude 授权类型，省略时沿用账号类型。
}

// accountReauthorizeAuthURL 统一各平台的授权链接响应。
type accountReauthorizeAuthURL struct {
	AuthURL   string `json:"auth_url"`        // 平台授权页面。
	SessionID string `json:"session_id"`      // 服务端授权会话。
	State     string `json:"state,omitempty"` // OAuth 回调校验值。
}

// accountReauthorization 保存由平台服务生成的认证结果。
type accountReauthorization struct {
	credentials map[string]any // 允许更新的认证字段。
	extra       map[string]any // 上游返回的身份和订阅信息。
}

// NewAccountReauthorizeHandler 创建只返回脱敏账号的重新授权入口。
func NewAccountReauthorizeHandler(accounts *AccountHandler, grok *service.GrokOAuthService) *AccountReauthorizeHandler {
	return &AccountReauthorizeHandler{accounts: accounts, grok: grok}
}

// GenerateAuthURL 依据账号既有配置生成重新授权会话，不接受客户端覆盖配置。
func (h *AccountReauthorizeHandler) GenerateAuthURL(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req AccountReauthorizeAuthURLRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil && err != io.EOF {
		response.BadRequest(c, "Invalid reauthorization request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		response.BadRequest(c, "Invalid reauthorization request")
		return
	}
	ctx := c.Request.Context()
	account, err := h.accounts.adminService.GetAccount(ctx, accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !account.IsOAuth() || account.IsCredentialShadow() {
		response.BadRequest(c, "Account must be an OAuth account with its own credentials")
		return
	}
	if req.Type == "" {
		req.Type = account.Type
	}
	if (req.Type != service.AccountTypeOAuth && req.Type != service.AccountTypeSetupToken) || (account.Platform != service.PlatformAnthropic && req.Type != account.Type) {
		response.BadRequest(c, "Invalid OAuth account type")
		return
	}
	result, err := h.generateAuthURL(ctx, account, req.Type, deriveGeminiRedirectURI(c))
	if err != nil {
		slog.Warn("account_reauthorization.generate_auth_url_failed", "account_id", accountID, "platform", account.Platform)
		response.BadRequest(c, "Unable to generate authorization URL using the current account configuration")
		return
	}
	response.Success(c, result)
}

// generateAuthURL 只将账号已有配置传给对应平台的授权服务。
func (h *AccountReauthorizeHandler) generateAuthURL(ctx context.Context, account *service.Account, accountType, redirectURI string) (*accountReauthorizeAuthURL, error) {
	result := &accountReauthorizeAuthURL{}
	switch account.Platform {
	case service.PlatformAnthropic:
		var auth *service.GenerateAuthURLResult
		var err error
		if accountType == service.AccountTypeSetupToken {
			auth, err = h.accounts.oauthService.GenerateSetupTokenURL(ctx, account.ProxyID)
		} else {
			auth, err = h.accounts.oauthService.GenerateAuthURL(ctx, account.ProxyID)
		}
		if err != nil {
			return nil, err
		}
		result.AuthURL, result.SessionID = auth.AuthURL, auth.SessionID
	case service.PlatformOpenAI:
		auth, err := h.accounts.openaiOAuthService.GenerateReauthorizationURL(ctx, account)
		if err != nil {
			return nil, err
		}
		result.AuthURL, result.SessionID = auth.AuthURL, auth.SessionID
	case service.PlatformGemini:
		oauthType := strings.TrimSpace(account.GetCredential("oauth_type"))
		if oauthType == "" {
			oauthType = "code_assist"
		}
		auth, err := h.accounts.geminiOAuthService.GenerateAuthURL(ctx, account.ProxyID, redirectURI, account.GetCredential("project_id"), oauthType, account.GetCredential("tier_id"))
		if err != nil {
			return nil, err
		}
		result.AuthURL, result.SessionID, result.State = auth.AuthURL, auth.SessionID, auth.State
	case service.PlatformAntigravity:
		auth, err := h.accounts.antigravityOAuthService.GenerateAuthURL(ctx, account.ProxyID)
		if err != nil {
			return nil, err
		}
		result.AuthURL, result.SessionID, result.State = auth.AuthURL, auth.SessionID, auth.State
	case service.PlatformGrok:
		auth, err := h.grok.GenerateReauthorizationURL(ctx, account)
		if err != nil {
			return nil, err
		}
		result.AuthURL, result.SessionID, result.State = auth.AuthURL, auth.SessionID, auth.State
	default:
		return nil, fmt.Errorf("unsupported OAuth platform")
	}
	if result.State == "" {
		parsed, err := url.Parse(result.AuthURL)
		if err != nil {
			return nil, err
		}
		result.State = parsed.Query().Get("state")
	}
	return result, nil
}

// Reauthorize 交换授权输入并更新既有 OAuth 账号，不向客户端返回上游凭据。
func (h *AccountReauthorizeHandler) Reauthorize(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req AccountReauthorizeRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		response.BadRequest(c, "Invalid reauthorization request")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		response.BadRequest(c, "Invalid reauthorization request")
		return
	}
	ctx := c.Request.Context()
	account, err := h.accounts.adminService.GetAccount(ctx, accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !account.IsOAuth() || account.IsCredentialShadow() || req.Platform != account.Platform {
		response.BadRequest(c, "Account must be an OAuth account matching the requested platform")
		return
	}
	if req.Type == "" {
		req.Type = account.Type
	}
	if err := req.validate(account); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	saver, ok := h.accounts.adminService.(service.AccountReauthorizationService)
	if !ok {
		response.InternalError(c, "Account reauthorization persistence is unavailable")
		return
	}
	proxyURL := ""
	if account.ProxyID != nil {
		proxy, err := h.accounts.adminService.GetProxy(ctx, *account.ProxyID)
		if err != nil || proxy == nil {
			response.BadRequest(c, "Account proxy is unavailable")
			return
		}
		proxyURL = proxy.URL()
	}
	result, err := h.exchange(ctx, account, &req, proxyURL)
	if err != nil {
		// 上游错误可能包含原始响应和 token；此入口只发送固定错误信息。
		slog.Warn("account_reauthorization.exchange_failed", "account_id", accountID, "platform", account.Platform, "method", req.Method)
		response.BadRequest(c, "OAuth authorization failed; check the authorization input and current account configuration")
		return
	}
	if strings.TrimSpace(credentialString(result.credentials["access_token"])) == "" {
		response.BadRequest(c, "OAuth authorization returned no access token")
		return
	}
	if account.Platform == service.PlatformGrok {
		result.extra["grok_needs_reauth"] = false
		result.extra["grok_needs_reauth_reason"] = ""
		result.extra["grok_needs_reauth_at"] = ""
	}
	updated, err := saver.ApplyAccountReauthorization(ctx, account, req.Type, result.credentials, result.extra)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if h.accounts.tokenCacheInvalidator != nil {
		var invalidateErr error
		service.AfterAccountCommit(ctx, func(commitCtx context.Context) {
			invalidateErr = h.accounts.tokenCacheInvalidator.InvalidateToken(commitCtx, updated)
			if invalidateErr != nil {
				slog.Error("account_reauthorization.invalidate_token_failed", "account_id", accountID)
			}
		})
		if invalidateErr != nil {
			response.InternalError(c, "OAuth credentials were saved, but token cache invalidation failed")
			return
		}
	}
	response.Success(c, h.accounts.buildAccountResponseWithRuntime(ctx, updated))
}

// validate 校验各平台允许的授权方式及最少必要输入。
func (r *AccountReauthorizeRequest) validate(account *service.Account) error {
	if r.Type != service.AccountTypeOAuth && r.Type != service.AccountTypeSetupToken {
		return fmt.Errorf("invalid OAuth account type")
	}
	if account.Platform != service.PlatformAnthropic && r.Type != account.Type {
		return fmt.Errorf("OAuth account type cannot be changed for this platform")
	}
	switch r.Method {
	case "code":
		if strings.TrimSpace(r.SessionID) == "" || strings.TrimSpace(r.Code) == "" {
			return fmt.Errorf("session_id and code are required")
		}
		if account.Platform != service.PlatformAnthropic && account.Platform != service.PlatformGrok && strings.TrimSpace(r.State) == "" {
			return fmt.Errorf("state is required")
		}
	case "cookie":
		if account.Platform != service.PlatformAnthropic || strings.TrimSpace(r.Code) == "" {
			return fmt.Errorf("cookie authorization requires an Anthropic account and code")
		}
	case "refresh_token":
		if (account.Platform != service.PlatformOpenAI && account.Platform != service.PlatformAntigravity && account.Platform != service.PlatformGrok) || strings.TrimSpace(r.RefreshToken) == "" {
			return fmt.Errorf("refresh_token authorization is unavailable for this account or missing refresh_token")
		}
	case "sso":
		if account.Platform != service.PlatformGrok || strings.TrimSpace(r.SSOToken) == "" {
			return fmt.Errorf("sso authorization requires a Grok account and sso_token")
		}
	default:
		return fmt.Errorf("invalid authorization method")
	}
	return nil
}

// exchange 调用对应平台服务，所有交换结果仅留在服务端。
func (h *AccountReauthorizeHandler) exchange(ctx context.Context, account *service.Account, req *AccountReauthorizeRequest, proxyURL string) (*accountReauthorization, error) {
	switch account.Platform {
	case service.PlatformAnthropic:
		return h.exchangeClaude(ctx, account, req, proxyURL)
	case service.PlatformOpenAI:
		return h.exchangeOpenAI(ctx, account, req, proxyURL)
	case service.PlatformGemini:
		return h.exchangeGemini(ctx, account, req, proxyURL)
	case service.PlatformAntigravity:
		return h.exchangeAntigravity(ctx, account, req, proxyURL)
	case service.PlatformGrok:
		return h.exchangeGrok(ctx, account, req, proxyURL)
	default:
		return nil, fmt.Errorf("unsupported OAuth platform")
	}
}

// exchangeClaude 将 Claude 授权码或 Cookie 交换为 OAuth 凭据。
func (h *AccountReauthorizeHandler) exchangeClaude(ctx context.Context, account *service.Account, req *AccountReauthorizeRequest, proxyURL string) (*accountReauthorization, error) {
	var info *service.TokenInfo
	var err error
	if req.Method == "cookie" {
		scope := "full"
		if req.Type == service.AccountTypeSetupToken {
			scope = "inference"
		}
		info, err = h.accounts.oauthService.CookieAuth(ctx, &service.CookieAuthInput{SessionKey: strings.TrimSpace(req.Code), ProxyID: account.ProxyID, Scope: scope})
	} else {
		if err = h.accounts.oauthService.ValidateReauthorizationSession(req.SessionID, proxyURL, req.Type); err != nil {
			return nil, err
		}
		info, err = h.accounts.oauthService.ExchangeCode(ctx, &service.ExchangeCodeInput{SessionID: req.SessionID, Code: strings.TrimSpace(req.Code), ProxyID: account.ProxyID})
	}
	if err != nil {
		return nil, err
	}
	credentials := service.BuildClaudeAccountCredentials(info)
	extra := map[string]any{}
	for key, value := range map[string]string{"org_uuid": info.OrgUUID, "account_uuid": info.AccountUUID, "email_address": info.EmailAddress} {
		if value != "" {
			credentials[key] = value
			extra[key] = value
		}
	}
	return &accountReauthorization{credentials: credentials, extra: extra}, nil
}

// exchangeOpenAI 按账号既有 OAuth 客户端交换授权码或 Refresh Token。
func (h *AccountReauthorizeHandler) exchangeOpenAI(ctx context.Context, account *service.Account, req *AccountReauthorizeRequest, proxyURL string) (*accountReauthorization, error) {
	var info *service.OpenAITokenInfo
	var err error
	if req.Method == "refresh_token" {
		clientID := strings.TrimSpace(account.GetCredential("client_id"))
		if clientID == "" {
			clientID = openai.ClientID
		}
		info, err = h.accounts.openaiOAuthService.RefreshTokenWithClientID(ctx, strings.TrimSpace(req.RefreshToken), proxyURL, clientID)
	} else {
		if err = h.accounts.openaiOAuthService.ValidateReauthorizationSession(req.SessionID, proxyURL, account); err != nil {
			return nil, err
		}
		info, err = h.accounts.openaiOAuthService.ExchangeCode(ctx, &service.OpenAIExchangeCodeInput{SessionID: req.SessionID, Code: strings.TrimSpace(req.Code), State: strings.TrimSpace(req.State), ProxyID: account.ProxyID})
	}
	if err != nil {
		return nil, err
	}
	if req.Method == "refresh_token" && info.RefreshToken == "" {
		info.RefreshToken = strings.TrimSpace(req.RefreshToken)
	}
	extra := map[string]any{}
	if info.Email != "" {
		extra["email"] = info.Email
	}
	if info.PrivacyMode != "" {
		extra["privacy_mode"] = info.PrivacyMode
	}
	return &accountReauthorization{credentials: h.accounts.openaiOAuthService.BuildAccountCredentials(info), extra: extra}, nil
}

// exchangeGemini 使用账号已有项目和 OAuth 类型交换 Gemini 授权码。
func (h *AccountReauthorizeHandler) exchangeGemini(ctx context.Context, account *service.Account, req *AccountReauthorizeRequest, proxyURL string) (*accountReauthorization, error) {
	if err := h.accounts.geminiOAuthService.ValidateReauthorizationSession(req.SessionID, proxyURL, account); err != nil {
		return nil, err
	}
	info, err := h.accounts.geminiOAuthService.ExchangeCode(ctx, &service.GeminiExchangeCodeInput{SessionID: req.SessionID, Code: strings.TrimSpace(req.Code), State: strings.TrimSpace(req.State), ProxyID: account.ProxyID, OAuthType: account.GetCredential("oauth_type"), TierID: account.GetCredential("tier_id")})
	if err != nil {
		return nil, err
	}
	credentials := h.accounts.geminiOAuthService.BuildAccountCredentials(info)
	// 项目和 OAuth 类型属于既有配置，不能由会话或探测结果修改。
	delete(credentials, "project_id")
	delete(credentials, "oauth_type")
	return &accountReauthorization{credentials: credentials}, nil
}

// exchangeAntigravity 交换 Antigravity 授权码或 Refresh Token 并构建认证字段。
func (h *AccountReauthorizeHandler) exchangeAntigravity(ctx context.Context, account *service.Account, req *AccountReauthorizeRequest, proxyURL string) (*accountReauthorization, error) {
	var info *service.AntigravityTokenInfo
	var err error
	if req.Method == "refresh_token" {
		info, err = h.accounts.antigravityOAuthService.ValidateRefreshToken(ctx, strings.TrimSpace(req.RefreshToken), account.ProxyID)
	} else {
		if err = h.accounts.antigravityOAuthService.ValidateReauthorizationSession(req.SessionID, proxyURL); err != nil {
			return nil, err
		}
		info, err = h.accounts.antigravityOAuthService.ExchangeCode(ctx, &service.AntigravityExchangeCodeInput{SessionID: req.SessionID, Code: strings.TrimSpace(req.Code), State: strings.TrimSpace(req.State), ProxyID: account.ProxyID})
	}
	if err != nil {
		return nil, err
	}
	if req.Method == "refresh_token" && info.RefreshToken == "" {
		info.RefreshToken = strings.TrimSpace(req.RefreshToken)
	}
	return &accountReauthorization{credentials: h.accounts.antigravityOAuthService.BuildAccountCredentials(info)}, nil
}

// exchangeGrok 将授权码、Refresh Token 或 SSO 换成 Build OAuth 凭据。
func (h *AccountReauthorizeHandler) exchangeGrok(ctx context.Context, account *service.Account, req *AccountReauthorizeRequest, proxyURL string) (*accountReauthorization, error) {
	var info *service.GrokTokenInfo
	var err error
	clientID := strings.TrimSpace(account.GetCredential("client_id"))
	if clientID == "" {
		clientID = xai.EffectiveClientID()
	}
	switch req.Method {
	case "refresh_token":
		info, err = h.grok.RefreshToken(ctx, strings.TrimSpace(req.RefreshToken), proxyURL, clientID)
	case "sso":
		info, err = h.grok.ValidateSSOToken(ctx, strings.TrimSpace(req.SSOToken), account.ProxyID)
	default:
		if err = h.grok.ValidateReauthorizationSession(req.SessionID, proxyURL, account); err != nil {
			return nil, err
		}
		info, err = h.grok.ExchangeCode(ctx, &service.GrokExchangeCodeInput{SessionID: req.SessionID, Code: strings.TrimSpace(req.Code), State: strings.TrimSpace(req.State), ProxyID: account.ProxyID})
	}
	if err != nil {
		return nil, err
	}
	if info.ClientID != clientID {
		return nil, fmt.Errorf("OAuth client does not match account")
	}
	credentials := h.grok.BuildAccountCredentials(info)
	// BuildAccountCredentials 为新建账号提供默认网络地址，重新授权保留原地址。
	delete(credentials, "base_url")
	extra := map[string]any{}
	for key, value := range map[string]string{"email": info.Email, "subscription_tier": info.SubscriptionTier, "entitlement_status": info.EntitlementStatus} {
		if value != "" {
			extra[key] = value
		}
	}
	return &accountReauthorization{credentials: credentials, extra: extra}, nil
}

// credentialString 读取平台构建结果中的字符串字段。
func credentialString(value any) string {
	text, _ := value.(string)
	return text
}
